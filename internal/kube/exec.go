package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	authv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	upstream "k8s.io/client-go/plugin/pkg/client/auth/exec"
	"k8s.io/client-go/rest"
	api "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/transport"
	"k8s.io/client-go/util/connrotation"
)

var errExecAuth = errors.New("Kubernetes credential helper unavailable")

// Foreground terminal ownership is process-wide, unlike each client cache.
var interactiveExec = make(chan struct{}, 1)

// Context-free client-go exec auth is deliberately bypassed at both transport
// boundaries. Credentials are scoped to this client, never a global cache.
func transportConfig(cfg *rest.Config) (*transport.Config, error) {
	copy := rest.CopyConfig(cfg)
	plugin := copy.ExecProvider
	copy.ExecProvider = nil
	tc, err := copy.TransportConfig()
	if err != nil {
		return nil, err
	}
	if plugin == nil {
		return tc, nil
	}
	if cfg.AuthProvider != nil {
		return nil, errExecAuth
	}
	if err := upstream.ValidatePluginPolicy(plugin.PluginPolicy); err != nil {
		return nil, errExecAuth
	}
	if plugin.APIVersion != "client.authentication.k8s.io/v1" && plugin.APIVersion != "client.authentication.k8s.io/v1beta1" {
		return nil, errExecAuth
	}
	if tc.HasTokenAuth() || tc.HasBasicAuth() || tc.HasCertAuth() {
		return tc, nil
	}
	a := &execAuth{config: plugin.DeepCopy(), gate: make(chan struct{}, 1), tracker: connrotation.NewConnectionTracker()}
	input := map[string]any{"apiVersion": plugin.APIVersion, "kind": "ExecCredential"}
	spec := map[string]any{}
	if plugin.ProvideClusterInfo {
		cluster, err := rest.ConfigToExecCluster(cfg)
		if err != nil {
			return nil, errExecAuth
		}
		wire := &authv1.Cluster{}
		if authv1.Convert_clientauthentication_Cluster_To_v1_Cluster(cluster, wire, nil) != nil {
			return nil, errExecAuth
		}
		spec["cluster"] = wire
	}
	input["spec"] = spec
	a.input = input
	// Fetch credentials in RoundTrip, where the request context exists. A TLS
	// callback only returns the cached certificate and never executes a process.
	tc.TLS.GetCertHolder = &transport.GetCertHolder{GetCert: func() (*tls.Certificate, error) {
		a.certMu.RLock()
		defer a.certMu.RUnlock()
		if a.cert == nil {
			return &tls.Certificate{}, nil
		}
		return a.cert, nil
	}}
	dial := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if tc.DialHolder != nil {
		dial = tc.DialHolder.Dial
	}
	tracked := connrotation.NewDialerWithTracker(dial, a.tracker)
	tc.DialHolder = &transport.DialHolder{Dial: tracked.DialContext}
	// The configuration's lifecycle wrapper must be outside auth so cancellation
	// reaches helpers even for SPDY's otherwise context-free dial requests.
	original := tc.WrapTransport
	tc.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		var rt http.RoundTripper = &execTransport{a: a, base: base}
		if original != nil {
			rt = original(rt)
		}
		return rt
	}
	return tc, nil
}

// HTTPClientFor is the application's exec-aware counterpart of rest.HTTPClientFor.
func HTTPClientFor(cfg *rest.Config) (*http.Client, error) {
	tc, err := transportConfig(cfg)
	if err != nil {
		return nil, err
	}
	rt, err := transport.New(tc)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: rt, Timeout: cfg.Timeout}, nil
}

type execCredentials struct {
	token   string
	cert    *tls.Certificate
	expires time.Time
}
type execAuth struct {
	config  *api.ExecConfig
	input   map[string]any
	gate    chan struct{}
	cached  *execCredentials
	certMu  sync.RWMutex
	cert    *tls.Certificate
	tracker *connrotation.ConnectionTracker
}
type execTransport struct {
	a    *execAuth
	base http.RoundTripper
}

func (t *execTransport) WrappedRoundTripper() http.RoundTripper { return t.base }
func (t *execTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}
	creds, err := t.a.credentials(req.Context(), nil)
	if err != nil {
		return nil, err
	}
	copy := req.Clone(req.Context())
	if creds.token != "" {
		copy.Header.Set("Authorization", "Bearer "+creds.token)
	}
	res, err := t.base.RoundTrip(copy)
	if err == nil && res.StatusCode == http.StatusUnauthorized {
		// As in client-go, refresh for the next request without retrying this one.
		if _, refreshErr := t.a.credentials(req.Context(), creds); refreshErr != nil && req.Context().Err() != nil {
			res.Body.Close()
			return nil, req.Context().Err()
		}
	}
	return res, err
}
func (a *execAuth) credentials(ctx context.Context, previous *execCredentials) (*execCredentials, error) {
	select {
	case a.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.cached != nil && ((previous != nil && previous != a.cached) || (previous == nil && (a.cached.expires.IsZero() || time.Now().Before(a.cached.expires)))) {
		return a.cached, nil
	}
	fresh, err := a.run(ctx)
	if err != nil {
		return nil, err
	}
	old := a.cached
	a.cached = fresh
	a.certMu.Lock()
	a.cert = fresh.cert
	a.certMu.Unlock()
	if old != nil && !reflect.DeepEqual(old.cert, fresh.cert) {
		a.tracker.CloseAll()
	}
	return fresh, nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > 1<<20-b.Len() {
		return 0, errExecAuth
	}
	return b.Buffer.Write(p)
}

func (a *execAuth) run(ctx context.Context) (credentials *execCredentials, runErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	interactive := false
	switch a.config.InteractiveMode {
	case api.NeverExecInteractiveMode:
	case api.IfAvailableExecInteractiveMode:
		interactive = !a.config.StdinUnavailable && term.IsTerminal(int(os.Stdin.Fd()))
	case api.AlwaysExecInteractiveMode:
		if a.config.StdinUnavailable || !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil, errExecAuth
		}
		interactive = true
	default:
		return nil, errExecAuth
	}
	a.input["spec"].(map[string]any)["interactive"] = interactive
	data, err := json.Marshal(a.input)
	if err != nil {
		return nil, errExecAuth
	}
	command := filepath.Clean(a.config.Command)
	cmd := exec.CommandContext(ctx, command, a.config.Args...)
	if !allowedExec(a.config, command, cmd.Path) {
		return nil, errExecAuth
	}
	cmd.Env = os.Environ()
	for _, env := range a.config.Env {
		cmd.Env = append(cmd.Env, env.Name+"="+env.Value)
	}
	cmd.Env = append(cmd.Env, "KUBERNETES_EXEC_INFO="+string(data))
	output := &boundedOutput{}
	cmd.Stdout = output
	// Plugins can echo credentials to stderr, including on failure.
	cmd.Stderr = io.Discard
	if interactive {
		cmd.Stdin = os.Stdin
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if interactive {
		select {
		case interactiveExec <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-interactiveExec }()
		// Terminal restoration runs from the parent's background group. Ignore
		// SIGTTOU for this serialized handoff, preserving an inherited ignore.
		ignored := signal.Ignored(syscall.SIGTTOU)
		signal.Ignore(syscall.SIGTTOU)
		if !ignored {
			defer signal.Reset(syscall.SIGTTOU)
		}
		foreground, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
		if err != nil {
			return nil, errExecAuth
		}
		state, err := term.GetState(int(os.Stdin.Fd()))
		if err != nil {
			return nil, errExecAuth
		}
		// Deferred until Run has joined the helper and output readers; hold the
		// interactive gate and SIGTTOU protection through both restorations.
		defer func() {
			groupErr := unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP, foreground)
			stateErr := term.Restore(int(os.Stdin.Fd()), state)
			if groupErr != nil || stateErr != nil {
				credentials, runErr = nil, errExecAuth
			}
		}()
		cmd.SysProcAttr.Foreground = true
		cmd.SysProcAttr.Ctty = int(os.Stdin.Fd())
		cmd.Stderr = os.Stderr // Interactive prompts belong to the user's terminal.
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	err = cmd.Run() // Run joins the process and its stdout/stderr copying goroutines.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exited *exec.ExitError
		if interactive && errors.As(err, &exited) {
			if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGINT {
				return nil, context.Canceled
			}
		}
		return nil, errExecAuth
	}
	var cred authv1.ExecCredential
	if provider.Strict(output.Bytes(), &cred) != nil || cred.APIVersion != a.config.APIVersion || cred.Kind != "ExecCredential" || cred.Status == nil {
		return nil, errExecAuth
	}
	s := cred.Status
	if (s.ClientCertificateData == "") != (s.ClientKeyData == "") || (s.Token == "" && s.ClientCertificateData == "") {
		return nil, errExecAuth
	}
	if len(s.Token) > 16384 || strings.IndexFunc(s.Token, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 {
		return nil, errExecAuth
	}
	out := &execCredentials{token: s.Token}
	if s.ExpirationTimestamp != nil {
		out.expires = s.ExpirationTimestamp.Time
		if !time.Now().Before(out.expires) {
			return nil, errExecAuth
		}
	}
	if s.ClientCertificateData != "" {
		pair, err := tls.X509KeyPair([]byte(s.ClientCertificateData), []byte(s.ClientKeyData))
		if err != nil {
			return nil, errExecAuth
		}
		out.cert = &pair
	}
	return out, nil
}
func allowedExec(c *api.ExecConfig, command, resolved string) bool {
	switch c.PluginPolicy.PolicyType {
	case "", api.PluginPolicyAllowAll:
		return true
	case api.PluginPolicyAllowlist:
		for _, entry := range c.PluginPolicy.Allowlist {
			if entry.Name == command || entry.Name == resolved {
				return true
			}
			if path, err := exec.LookPath(entry.Name); err == nil && path == resolved {
				return true
			}
		}
	}
	return false
}

// Bind every request to its owning command even if a lower-level protocol
// creates it with context.Background(). The callback is stopped and joined.
type lifecycleTransport struct {
	life context.Context
	base http.RoundTripper
}

func (t lifecycleTransport) WrappedRoundTripper() http.RoundTripper { return t.base }
func (t lifecycleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	done := make(chan struct{})
	stop := context.AfterFunc(t.life, func() { cancel(); close(done) })
	cleanup := func() {
		if !stop() {
			<-done
		}
		cancel()
	}
	if t.life.Err() != nil {
		cleanup()
		return nil, t.life.Err()
	}
	res, err := t.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		cleanup()
		return nil, err
	}
	res.Body = &lifecycleBody{ReadCloser: res.Body, cleanup: cleanup}
	return res, nil
}

type lifecycleBody struct {
	io.ReadCloser
	once    sync.Once
	cleanup func()
}

func (b *lifecycleBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.cleanup); return err }
