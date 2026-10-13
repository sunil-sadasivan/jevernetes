package kube

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/sunil-sadasivan/jevernetes/internal/tui"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"io"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	api "k8s.io/client-go/tools/clientcmd/api"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"k8s.io/client-go/transport"
)

func TestCredentialHelperProcess(t *testing.T) {
	if os.Getenv("JEV_AUTH_HELPER") != "1" {
		return
	}
	marker := os.Getenv("JEV_AUTH_MARKER")
	if os.Getenv("JEV_AUTH_TERMINAL") == "1" {
		_ = os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600)
		if expected := os.Getenv("JEV_AUTH_COOKED"); expected != "" {
			_, _ = unix.Poll([]unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}, 0)
			state, err := term.GetState(int(os.Stdin.Fd()))
			if err != nil || fmt.Sprintf("%#v", state) != expected {
				os.Exit(4)
			}
		}
		// Change echo/canonical attributes for every exit path. The PTY driver
		// re-enables ISIG before sending a real foreground Ctrl-C.
		if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
			os.Exit(3)
		}
		// Python sets ISIG after seeing this marker, before sending Ctrl-C.
		fmt.Fprintln(os.Stderr, "synthetic terminal prompt")
		if os.Getenv("JEV_AUTH_PTY") == "error" {
			os.Exit(2)
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || line != "synthetic input\n" {
			os.Exit(2)
		}
		fmt.Print(tokenPayload("client.authentication.k8s.io/v1", "synthetic", ""))
		os.Exit(0)
	}
	if payload := os.Getenv("JEV_AUTH_PAYLOAD"); payload != "" {
		_ = os.WriteFile(marker+".info", []byte(os.Getenv("KUBERNETES_EXEC_INFO")), 0600)
		data, err := os.ReadFile(payload)
		if err != nil {
			os.Exit(2)
		}
		if string(data) != "HANG" {
			fmt.Print(string(data))
			os.Exit(0)
		}
	}
	if os.Getenv("JEV_AUTH_REFRESH") == "1" {
		if _, err := os.Stat(marker + ".first"); os.IsNotExist(err) {
			_ = os.WriteFile(marker+".first", []byte("first"), 0600)
			fmt.Print(`{"apiVersion":"client.authentication.k8s.io/v1","kind":"ExecCredential","status":{"token":"synthetic"}}`)
			os.Exit(0)
		}
	}
	_ = os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600)
	for {
		time.Sleep(time.Hour)
	}
}

func execFixture(t *testing.T, refresh bool) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "helper.pid")
	path := filepath.Join(dir, "config")
	raw := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: local
clusters:
- name: local
  cluster:
    server: https://fixture.invalid
contexts:
- name: local
  context:
    cluster: local
    user: local
users:
- name: local
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: %q
      args: ["-test.run=^TestCredentialHelperProcess$"]
      interactiveMode: Never
      env:
      - name: JEV_AUTH_HELPER
        value: "1"
      - name: JEV_AUTH_MARKER
        value: %q
      - name: JEV_AUTH_REFRESH
        value: %q
`, os.Args[0], marker, map[bool]string{true: "1", false: "0"}[refresh])
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	return marker
}

func TestExecAuthenticationCancellation(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		for _, lifecycle := range []bool{false, true} {
			t.Run(fmt.Sprintf("refresh=%v/lifecycle=%v", refresh, lifecycle), func(t *testing.T) {
				marker := execFixture(t, refresh)
				life, cancelLife := context.WithCancel(context.Background())
				defer cancelLife()
				request, cancelRequest := context.WithCancel(context.Background())
				defer cancelRequest()
				config, err := LoadContext(life, cfg())
				if err != nil {
					t.Fatal(err)
				}
				tc, err := transportConfig(config)
				if err != nil {
					t.Fatal(err)
				}
				rt, err := transport.HTTPWrappersForConfig(tc, fileRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					req, _ := http.NewRequestWithContext(request, "GET", "https://fixture.invalid", nil)
					res, err := rt.RoundTrip(req)
					if res != nil {
						res.Body.Close()
					}
					done <- err
				}()

				var pid int
				until := time.Now().Add(5 * time.Second)
				for time.Now().Before(until) {
					b, _ := os.ReadFile(marker)
					pid, _ = strconv.Atoi(string(b))
					if pid > 0 {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if pid == 0 {
					t.Fatal("helper did not start")
				}
				defer syscall.Kill(pid, syscall.SIGKILL)
				if lifecycle {
					cancelLife()
				} else {
					cancelRequest()
				}
				select {
				case err := <-done:
					if err == nil {
						t.Error("cancelled auth succeeded")
					}
				case <-time.After(time.Second):
					t.Error("credential helper blocked cancellation")
					_ = syscall.Kill(pid, syscall.SIGKILL)
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("helper did not join after test cleanup")
					}
				}
				if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
					t.Error("credential helper survived shutdown")
				}
			})
		}
	}
}

func credentialTransport(t *testing.T, config *rest.Config, status *int, seen *string) (http.RoundTripper, *transport.Config) {
	t.Helper()
	tc, err := transportConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := transport.HTTPWrappersForConfig(tc, fileRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if seen != nil {
			*seen = req.Header.Get("Authorization")
		}
		return &http.Response{StatusCode: *status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return rt, tc
}
func credentialRequest(t *testing.T, rt http.RoundTripper) error {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://fixture.invalid", nil)
	res, err := rt.RoundTrip(req)
	if res != nil {
		res.Body.Close()
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("mutated caller authorization")
	}
	return err
}
func payloadConfig(t *testing.T, payload string) (*rest.Config, string, string) {
	t.Helper()
	marker := execFixture(t, false)
	config, err := LoadContext(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "response.json")
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	config.ExecProvider.Env = append(config.ExecProvider.Env, api.ExecEnvVar{Name: "JEV_AUTH_PAYLOAD", Value: path})
	return config, path, marker
}
func tokenPayload(version, token, expiration string) string {
	status := map[string]any{"token": token}
	if expiration != "" {
		status["expirationTimestamp"] = expiration
	}
	raw, _ := json.Marshal(map[string]any{"apiVersion": version, "kind": "ExecCredential", "status": status})
	return string(raw)
}

func TestExecCredentialSchemaAndCompatibility(t *testing.T) {
	version := "client.authentication.k8s.io/v1"
	valid := tokenPayload(version, "synthetic", "")
	cases := map[string]string{
		"valid":                   valid,
		"wrong-version":           strings.Replace(valid, "/v1", "/v1beta1", 1),
		"wrong-kind":              strings.Replace(valid, "ExecCredential", "List", 1),
		"unknown":                 strings.Replace(valid, `"status":`, `"unknown":true,"status":`, 1),
		"duplicate":               strings.Replace(valid, `"token":"synthetic"`, `"token":"synthetic","token":"other"`, 1),
		"trailing":                valid + `{}`,
		"empty":                   tokenPayload(version, "", ""),
		"header-injection":        tokenPayload(version, "synthetic\r\nX-Fixture: value", ""),
		"expired":                 tokenPayload(version, "synthetic", "2000-01-01T00:00:00Z"),
		"certificate-without-key": `{"apiVersion":"client.authentication.k8s.io/v1","kind":"ExecCredential","status":{"clientCertificateData":"synthetic"}}`,
		"invalid-pair":            `{"apiVersion":"client.authentication.k8s.io/v1","kind":"ExecCredential","status":{"clientCertificateData":"synthetic","clientKeyData":"synthetic"}}`,
		"oversize":                strings.Repeat("x", (1<<20)+1),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			config, _, _ := payloadConfig(t, payload)
			status := 200
			seen := ""
			rt, _ := credentialTransport(t, config, &status, &seen)
			err := credentialRequest(t, rt)
			if name == "valid" {
				if err != nil || seen != "Bearer synthetic" {
					t.Fatal("valid credential rejected")
				}
			} else if err == nil {
				t.Fatal("invalid credential accepted")
			}
			if err != nil && strings.Contains(err.Error(), "synthetic") {
				t.Fatal("credential leaked in error")
			}
		})
	}
	for _, v := range []string{version, "client.authentication.k8s.io/v1beta1"} {
		t.Run(v, func(t *testing.T) {
			config, _, marker := payloadConfig(t, tokenPayload(v, "synthetic", ""))
			config.ExecProvider.APIVersion = v
			config.ExecProvider.ProvideClusterInfo = true
			config.ExecProvider.Config = &runtime.Unknown{Raw: []byte(`{"audience":"synthetic-cluster-only"}`)}
			config.ServerName = "fixture.invalid"
			config.DisableCompression = true
			status := 200
			rt, _ := credentialTransport(t, config, &status, nil)
			if credentialRequest(t, rt) != nil {
				t.Fatal("supported version rejected")
			}
			raw, err := os.ReadFile(marker + ".info")
			if err != nil {
				t.Fatal(err)
			}
			var info struct {
				APIVersion string `json:"apiVersion"`
				Spec       struct {
					Interactive bool `json:"interactive"`
					Cluster     struct {
						Server             string `json:"server"`
						ServerName         string `json:"tls-server-name"`
						DisableCompression bool   `json:"disable-compression"`
						Config             struct {
							Audience string `json:"audience"`
						} `json:"config"`
					} `json:"cluster"`
				} `json:"spec"`
			}
			if json.Unmarshal(raw, &info) != nil || info.APIVersion != v || info.Spec.Interactive || info.Spec.Cluster.Server != "https://fixture.invalid" || info.Spec.Cluster.Config.Audience != "synthetic-cluster-only" || info.Spec.Cluster.ServerName != "fixture.invalid" || !info.Spec.Cluster.DisableCompression {
				t.Fatal("exec input lost version, cluster, or audience scope")
			}
		})
	}
}

func syntheticCertificate(t *testing.T, serial int64) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "synthetic"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
}
func TestExecCredentialRotationAndTLS(t *testing.T) {
	version := "client.authentication.k8s.io/v1"
	config, path, _ := payloadConfig(t, tokenPayload(version, "first", ""))
	status := 200
	seen := ""
	rt, tc := credentialTransport(t, config, &status, &seen)
	if err := credentialRequest(t, rt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(tokenPayload(version, "second", "")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := credentialRequest(t, rt); err != nil || seen != "Bearer first" {
		t.Fatal("credential was not cached")
	}
	status = 401
	if err := credentialRequest(t, rt); err != nil || seen != "Bearer first" {
		t.Fatal("401 refresh changed current request")
	}
	status = 200
	if err := credentialRequest(t, rt); err != nil || seen != "Bearer second" {
		t.Fatal("401 refresh missing")
	}
	cert, key := syntheticCertificate(t, 1)
	raw, _ := json.Marshal(map[string]any{"apiVersion": version, "kind": "ExecCredential", "status": map[string]any{"clientCertificateData": cert, "clientKeyData": key}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	status = 401
	if err := credentialRequest(t, rt); err != nil {
		t.Fatal(err)
	}
	status = 200
	if err := credentialRequest(t, rt); err != nil || seen != "" {
		t.Fatal("certificate-only auth failed")
	}
	pair, err := tc.TLS.GetCertHolder.GetCert()
	if err != nil || len(pair.Certificate) != 1 {
		t.Fatal("TLS callback missing certificate")
	}
	first := append([]byte(nil), pair.Certificate[0]...)
	cert, key = syntheticCertificate(t, 2)
	raw, _ = json.Marshal(map[string]any{"apiVersion": version, "kind": "ExecCredential", "status": map[string]any{"clientCertificateData": cert, "clientKeyData": key, "token": "both"}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	status = 401
	if err := credentialRequest(t, rt); err != nil {
		t.Fatal(err)
	}
	status = 200
	if err := credentialRequest(t, rt); err != nil || seen != "Bearer both" {
		t.Fatal("combined token/certificate auth failed")
	}
	pair, err = tc.TLS.GetCertHolder.GetCert()
	if err != nil || bytes.Equal(first, pair.Certificate[0]) {
		t.Fatal("TLS certificate did not rotate")
	}
	// Callback never runs a helper, even after expiry or plugin output replacement.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.TLS.GetCertHolder.GetCert(); err != nil {
		t.Fatal("TLS callback unexpectedly executed helper")
	}
}

func TestExecCredentialPolicyAndPrecedence(t *testing.T) {
	for _, mode := range []string{"deny", "allow", "allow-resolved", "reject", "static-token", "basic", "request-header", "interactive-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			config, _, marker := payloadConfig(t, tokenPayload("client.authentication.k8s.io/v1", "synthetic", ""))
			switch mode {
			case "deny":
				config.ExecProvider.PluginPolicy.PolicyType = api.PluginPolicyDenyAll
			case "allow", "allow-resolved":
				if mode == "allow-resolved" {
					t.Setenv("PATH", filepath.Dir(os.Args[0]))
					config.ExecProvider.Command = filepath.Base(os.Args[0])
				}
				config.ExecProvider.PluginPolicy = api.PluginPolicy{PolicyType: api.PluginPolicyAllowlist, Allowlist: []api.AllowlistEntry{{Name: os.Args[0]}}}
			case "reject":
				config.ExecProvider.PluginPolicy = api.PluginPolicy{PolicyType: api.PluginPolicyAllowlist, Allowlist: []api.AllowlistEntry{{Name: "not-the-helper"}}}
			case "static-token":
				config.BearerToken = "static"
			case "basic":
				config.Username = "synthetic"
				config.Password = "synthetic"
			case "interactive-unavailable":
				config.ExecProvider.InteractiveMode = api.AlwaysExecInteractiveMode
				config.ExecProvider.StdinUnavailable = true
			}
			status := 200
			rt, _ := credentialTransport(t, config, &status, nil)
			req, _ := http.NewRequest("GET", "https://fixture.invalid", nil)
			if mode == "request-header" {
				req.Header.Set("Authorization", "Bearer explicit")
			}
			res, err := rt.RoundTrip(req)
			if res != nil {
				res.Body.Close()
			}
			reject := mode == "deny" || mode == "reject" || mode == "interactive-unavailable"
			if (err != nil) != reject {
				t.Fatal("incorrect policy outcome")
			}
			_, statErr := os.Stat(marker + ".info")
			expectRun := mode == "allow" || mode == "allow-resolved"
			if (statErr == nil) != expectRun {
				t.Fatal("unexpected helper invocation")
			}
		})
	}
}

func TestExecConcurrentCancellationAndSPDY(t *testing.T) {
	for _, spdy := range []bool{false, true} {
		t.Run(fmt.Sprint(spdy), func(t *testing.T) {
			marker := execFixture(t, false)
			life, cancel := context.WithCancel(context.Background())
			defer cancel()
			config, err := LoadContext(life, cfg())
			if err != nil {
				t.Fatal(err)
			}
			var rt http.RoundTripper
			if spdy {
				rt, _, err = spdyTransport(life, config)
			} else {
				client, e := HTTPClientFor(config)
				err = e
				if client != nil {
					rt = client.Transport
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Go(func() {
					req, _ := http.NewRequest("GET", "https://fixture.invalid", nil)
					res, err := rt.RoundTrip(req)
					if res != nil {
						res.Body.Close()
					}
					if err == nil {
						t.Error("cancelled auth succeeded")
					}
				})
			}
			go func() { wg.Wait(); close(done) }()
			var pid int
			for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
				raw, _ := os.ReadFile(marker)
				pid, _ = strconv.Atoi(string(raw))
				if pid > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("helper did not start")
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("concurrent requests failed to join")
			}
			if syscall.Kill(pid, 0) != syscall.ESRCH {
				t.Fatal("helper survived shutdown")
			}
		})
	}
}

func TestExecExpirationRefreshCancellation(t *testing.T) {
	config, path, marker := payloadConfig(t, tokenPayload("client.authentication.k8s.io/v1", "synthetic", time.Now().Add(3*time.Second).UTC().Format(time.RFC3339)))
	status := 200
	rt, _ := credentialTransport(t, config, &status, nil)
	if err := credentialRequest(t, rt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("HANG"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://fixture.invalid", nil)
		res, err := rt.RoundTrip(req)
		if res != nil {
			res.Body.Close()
		}
		done <- err
	}()
	var pid int
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		raw, _ := os.ReadFile(marker)
		pid, _ = strconv.Atoi(string(raw))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("expired credential did not invoke refresh")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled refresh succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("expired credential helper blocked cancellation")
	}
	if syscall.Kill(pid, 0) != syscall.ESRCH {
		t.Fatal("refresh helper survived cancellation")
	}
}

func TestExecInteractiveHarness(t *testing.T) {
	mode := os.Getenv("JEV_AUTH_PTY")
	if mode == "" {
		return
	}
	marker := execFixture(t, false)
	// A readiness observation settles Darwin's transient PENDIN bookkeeping
	// without consuming input; compare every termios field, without masking flags.
	_, _ = unix.Poll([]unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}, 0)
	before, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	config, err := LoadContext(life, cfg())
	if err != nil {
		t.Fatal(err)
	}
	config.ExecProvider.InteractiveMode = api.AlwaysExecInteractiveMode
	config.ExecProvider.Env = append(config.ExecProvider.Env, api.ExecEnvVar{Name: "JEV_AUTH_TERMINAL", Value: "1"})
	var stopUI context.CancelFunc
	var uiDone chan error
	if os.Getenv("JEV_AUTH_UI") == "1" {
		config.ExecProvider.Env = append(config.ExecProvider.Env, api.ExecEnvVar{Name: "JEV_AUTH_COOKED", Value: fmt.Sprintf("%#v", before)})
		uiCtx, stop := context.WithCancel(life)
		stopUI = stop
		defer stop()
		uiDone = make(chan error, 1)
		go func() { _, err := tui.Run(uiCtx, os.Stdin, nil, nil, tui.Options{Offline: true}); uiDone <- err }()
		deadline := time.Now().Add(time.Second)
		for {
			state, err := term.GetState(int(os.Stdin.Fd()))
			if err == nil && !reflect.DeepEqual(state, before) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("UI did not enter raw mode")
			}
			time.Sleep(time.Millisecond)
		}
	}
	status := 200
	rt, _ := credentialTransport(t, config, &status, nil)
	err = credentialRequest(t, rt)
	if stopUI != nil {
		stopUI()
		select {
		case <-uiDone:
		case <-time.After(time.Second):
			t.Fatal("UI did not release terminal after authentication")
		}
	}
	if (err != nil) != (mode != "success") {
		t.Fatal("incorrect interactive helper result")
	}
	group, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
	if err != nil || group != syscall.Getpgrp() {
		t.Fatal("terminal foreground group not restored")
	}
	_, _ = unix.Poll([]unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}, 0)
	after, stateErr := term.GetState(int(os.Stdin.Fd()))
	if stateErr != nil || !reflect.DeepEqual(before, after) {
		t.Errorf("complete terminal attributes not restored: before=%#v after=%#v", before, after)
	}
	raw, _ := os.ReadFile(marker)
	pid, _ := strconv.Atoi(string(raw))
	if pid == 0 || syscall.Kill(pid, 0) != syscall.ESRCH {
		t.Fatal("interactive helper survived")
	}
}

func TestExecInteractivePTY(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is required for the local PTY fixture")
	}
	script := `
import os, pty, select, signal, sys, time, termios
pid, fd = pty.fork()
if pid == 0:
    env = dict(os.environ, JEV_AUTH_PTY=sys.argv[2], JEV_AUTH_UI=sys.argv[3], GORACE="atexit_sleep_ms=0")
    os.execve(sys.argv[1], [sys.argv[1], "-test.run=^TestExecInteractiveHarness$"], env)
output = b""
sent = False
deadline = time.monotonic() + 10
try:
    while time.monotonic() < deadline:
        if select.select([fd], [], [], 0.05)[0]:
            try:
                data = os.read(fd, 4096)
            except OSError:
                break
            if not data:
                break
            output += data
            if b"synthetic terminal prompt" in output and not sent and sys.argv[2] in ("success", "interrupt"):
                attrs = termios.tcgetattr(fd)
                attrs[3] |= termios.ISIG
                termios.tcsetattr(fd, termios.TCSANOW, attrs)
                os.write(fd, b"synthetic input\n" if sys.argv[2] == "success" else b"\x03")
                sent = True
    else:
        os.kill(pid, signal.SIGKILL)
        raise RuntimeError("interactive authentication exceeded bound")
    _, status = os.waitpid(pid, 0)
    if status != 0 or b"PASS" not in output:
        raise RuntimeError("interactive authentication or terminal restoration failed: " + output.decode("utf-8", "replace"))
finally:
    os.close(fd)
`
	for _, ui := range []string{"0", "1"} {
		for _, mode := range []string{"success", "cancel", "error", "interrupt"} {
			t.Run(mode+"/tui="+ui, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, python, "-c", script, os.Args[0], mode, ui)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("PTY fixture failed: %v %s", err, out)
				}
			})
		}
	}
}
