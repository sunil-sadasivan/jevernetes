package kube

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	"k8s.io/client-go/rest"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
)

func oidcJWT(exp time.Time) string {
	b, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(b) + ".synthetic"
}
func oidcFixture(t *testing.T) string {
	t.Helper()
	c := api.NewConfig()
	c.Clusters["fixture"] = &api.Cluster{Server: "https://api.fixture.invalid"}
	c.AuthInfos["fixture"] = &api.AuthInfo{AuthProvider: &api.AuthProviderConfig{Name: "oidc", Config: map[string]string{"idp-issuer-url": "http://issuer.fixture.invalid", "client-id": fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()), "id-token": oidcJWT(time.Now().Add(-time.Hour)), "refresh-token": "synthetic-old"}}}
	c.Contexts["fixture"] = &api.Context{Cluster: "fixture", AuthInfo: "fixture"}
	c.CurrentContext = "fixture"
	b, _ := clientcmd.Write(*c)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	return path
}

// Real HTTP protocol over local pipes; no network/listener permission required.
func oidcServer(t *testing.T, handler func(*http.Request) string) {
	oidcServerConnection(t, handler, nil)
}
func oidcServerConnection(t *testing.T, handler func(*http.Request) string, wrap func(net.Conn) net.Conn) {
	t.Helper()
	old := http.DefaultTransport
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		a, b := net.Pipe()
		mu.Lock()
		conns = append(conns, a, b)
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer b.Close()
			var peer net.Conn = b
			if wrap != nil {
				peer = wrap(b)
			}
			r, e := http.ReadRequest(bufio.NewReader(peer))
			if e != nil {
				return
			}
			body := handler(r)
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			fmt.Fprintf(peer, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		}()
		return a, nil
	}}
	http.DefaultTransport = tr
	t.Cleanup(func() {
		http.DefaultTransport = old
		tr.CloseIdleConnections()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
}
func TestOIDCRotationPersists(t *testing.T) {
	path := oidcFixture(t)
	if os.Getenv("JEV_OIDC_UMASK") == "1" {
		old := unix.Umask(0777)
		defer unix.Umask(old)
	}
	token := oidcJWT(time.Now().Add(time.Hour))
	oidcServer(t, func(r *http.Request) string {
		if r.Method == "GET" {
			return `{"issuer":"http://issuer.fixture.invalid","token_endpoint":"http://issuer.fixture.invalid/token"}`
		}
		return fmt.Sprintf(`{"access_token":"synthetic-access","token_type":"Bearer","id_token":%q,"refresh_token":"synthetic-new"}`, token)
	})
	cfg, err := LoadContext(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Transport = fileRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	client, err := HTTPClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(cfg.Host)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	b, _ := os.ReadFile(path)
	saved, err := clientcmd.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AuthInfos["fixture"].AuthProvider.Config["refresh-token"] != "synthetic-new" {
		t.Fatal("rotated refresh token was not persisted")
	}
}
func TestOIDCRequestCancellation(t *testing.T) {
	for _, stage := range []string{"discovery", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			oidcFixture(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			var released, started sync.Once
			defer released.Do(func() { close(release) })
			oidcServer(t, func(r *http.Request) string {
				if stage == "refresh" && r.Method == "GET" {
					return `{"issuer":"http://issuer.fixture.invalid","token_endpoint":"http://issuer.fixture.invalid/token"}`
				}
				started.Do(func() { close(entered) })
				<-release
				return `{}`
			})
			cfg, err := LoadContext(context.Background(), cfg())
			if err != nil {
				t.Fatal(err)
			}
			client, err := HTTPClientFor(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", cfg.Host, nil)
			done := make(chan error, 1)
			go func() {
				r, e := client.Do(req)
				if r != nil {
					r.Body.Close()
				}
				done <- e
			}()
			<-entered
			cancel()
			select {
			case e := <-done:
				if e == nil {
					t.Error("cancelled request succeeded")
				}
			case <-time.After(400 * time.Millisecond):
				t.Error("OIDC issuer outlived request cancellation")
				released.Do(func() { close(release) })
				<-done
			}
		})
	}
}

func oidcDo(t *testing.T, config *rest.Config, ctx context.Context) error {
	t.Helper()
	config.Transport = fileRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	client, err := HTTPClientFor(config)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", config.Host, nil)
	res, err := client.Do(req)
	if res != nil {
		res.Body.Close()
	}
	return err
}
func TestOIDCConcurrentClientsAndEdits(t *testing.T) {
	path := oidcFixture(t)
	token := oidcJWT(time.Now().Add(time.Hour))
	var calls atomic.Int32
	oidcServer(t, func(r *http.Request) string {
		if r.Method == "GET" {
			return `{"token_endpoint":"http://issuer.fixture.invalid/token"}`
		}
		calls.Add(1)
		// A separate writer's unrelated changes during issuer I/O must survive.
		b, _ := os.ReadFile(path)
		c, _ := clientcmd.Load(b)
		c.Clusters["unrelated"] = &api.Cluster{Server: "https://other.fixture.invalid"}
		b, _ = clientcmd.Write(*c)
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Error(e)
		}
		return fmt.Sprintf(`{"id_token":%q,"refresh_token":"synthetic-new"}`, token)
	})
	a, e := LoadContext(context.Background(), cfg())
	if e != nil {
		t.Fatal(e)
	}
	b, e := LoadContext(context.Background(), cfg())
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 2)
	go func() { done <- oidcDo(t, a, context.Background()) }()
	go func() { done <- oidcDo(t, b, context.Background()) }()
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate refresh", calls.Load())
	}
	raw, _ := os.ReadFile(path)
	saved, e := clientcmd.Load(raw)
	if e != nil {
		t.Fatal(e)
	}
	if saved.Clusters["unrelated"] == nil || saved.AuthInfos["fixture"].AuthProvider.Config["refresh-token"] != "synthetic-new" {
		t.Fatal("concurrent edit or rotation lost")
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal("public credentials")
	}
	// A fresh client (including retained clients reading YAML) sees the rotation.
	c, e := LoadContext(context.Background(), cfg())
	if e != nil {
		t.Fatal(e)
	}
	if e := oidcDo(t, c, context.Background()); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("fresh client refreshed cached token")
	}
	// The standard client-go consumer can load the rewritten kubeconfig and
	// forward its cached token, just as kubectl does in a separate process.
	retained, e := clientcmd.NewNonInteractiveClientConfig(*saved, "fixture", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if e != nil {
		t.Fatal(e)
	}
	retained.Transport = fileRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("retained consumer lost token")
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	consumer, e := rest.HTTPClientFor(retained)
	if e != nil {
		t.Fatal(e)
	}
	response, e := consumer.Get(retained.Host)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if calls.Load() != 1 {
		t.Fatal("retained consumer refreshed cached token")
	}
}
func TestOIDCUnsafeRefreshDestinations(t *testing.T) {
	for _, kind := range []string{"public-file", "public-directory", "symlink", "hardlink", "fifo", "parent-symlink", "multiple-files", "lock", "auth-edit"} {
		t.Run(kind, func(t *testing.T) {
			path := oidcFixture(t)
			config, e := LoadContext(context.Background(), cfg())
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "public-file":
				os.Chmod(path, 0644)
			case "public-directory":
				os.Chmod(filepath.Dir(path), 0755)
			case "symlink":
				os.Rename(path, path+".original")
				os.Symlink(path+".original", path)
			case "hardlink":
				os.Link(path, path+".alias")
			case "fifo":
				os.Remove(path)
				if e := unix.Mkfifo(path, 0600); e != nil {
					t.Fatal(e)
				}
			case "parent-symlink":
				dir := filepath.Dir(path)
				os.Rename(dir, dir+"-original")
				os.Symlink(dir+"-original", dir)
				defer os.RemoveAll(dir + "-original")
			case "multiple-files":
				t.Setenv("KUBECONFIG", path+string(os.PathListSeparator)+path+".missing")
				if _, e := LoadContext(context.Background(), cfg()); e == nil {
					t.Fatal("merged OIDC accepted")
				}
				return
			case "lock":
				if e := unix.Mkfifo(path+".lock", 0600); e != nil {
					t.Fatal(e)
				}
			case "auth-edit":
				raw, _ := os.ReadFile(path)
				c, _ := clientcmd.Load(raw)
				c.AuthInfos["fixture"].AuthProvider.Config["client-id"] = "changed"
				raw, _ = clientcmd.Write(*c)
				os.WriteFile(path, raw, 0600)
			}
			var calls atomic.Int32
			oidcServer(t, func(*http.Request) string { calls.Add(1); return `{}` })
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			if e := oidcDo(t, config, ctx); e == nil {
				t.Fatal("unsafe refresh accepted")
			}
			if calls.Load() != 0 {
				t.Fatal("consumed refresh before destination validation")
			}
		})
	}
}
func TestOIDCSPDYLifecycleAndDeadline(t *testing.T) {
	for _, kind := range []string{"lifecycle", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			oidcFixture(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			oidcServer(t, func(*http.Request) string { close(entered); <-release; return `{}` })
			owner, cancel := context.WithCancel(context.Background())
			defer cancel()
			config, e := LoadContext(owner, cfg())
			if e != nil {
				t.Fatal(e)
			}
			reqctx := context.Background()
			if kind == "deadline" {
				var stop context.CancelFunc
				reqctx, stop = context.WithTimeout(reqctx, 100*time.Millisecond)
				defer stop()
			}
			rt, up, e := spdyTransport(owner, config)
			if e != nil {
				t.Fatal(e)
			}
			defer up.close()
			req, _ := http.NewRequestWithContext(reqctx, "POST", config.Host, nil)
			done := make(chan error, 1)
			go func() {
				r, e := rt.RoundTrip(req)
				if r != nil {
					r.Body.Close()
				}
				done <- e
			}()
			<-entered
			if kind == "lifecycle" {
				cancel()
			}
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("cancellation lost")
				}
			case <-time.After(time.Second):
				t.Fatal("SPDY auth not joined")
			}
		})
	}
}

func TestOIDCSignalChild(t *testing.T) {
	if os.Getenv("JEV_OIDC_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOIDCSignalChild$")
		child.Env = append(os.Environ(), "JEV_OIDC_CHILD=1")
		if b, e := child.CombinedOutput(); e != nil {
			t.Fatalf("OIDC shutdown child failed: %v %s", e, b)
		}
		return
	}
	oidcFixture(t)
	owner, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	release := make(chan struct{})
	defer close(release)
	oidcServer(t, func(*http.Request) string {
		if e := syscall.Kill(os.Getpid(), syscall.SIGTERM); e != nil {
			t.Error(e)
		}
		<-release
		return `{}`
	})
	config, e := LoadContext(owner, cfg())
	if e != nil {
		t.Fatal(e)
	}
	config.Timeout = 0 // The owning command, not http.Client.Timeout, must stop auth.
	if e := oidcDo(t, config, context.Background()); e == nil || owner.Err() == nil {
		t.Fatal("SIGTERM did not cancel authentication", e)
	}
}

func TestOIDCRefreshRejectsUnsafeCAAndResponses(t *testing.T) {
	for _, kind := range []string{"ca-fifo", "ca-symlink", "empty-ca", "discovery-oversize", "token-oversize", "issuer-mismatch", "invalid-token", "destination-replaced"} {
		t.Run(kind, func(t *testing.T) {
			path := oidcFixture(t)
			if strings.HasPrefix(kind, "ca-") || kind == "empty-ca" {
				raw, _ := os.ReadFile(path)
				c, _ := clientcmd.Load(raw)
				if kind == "empty-ca" {
					c.AuthInfos["fixture"].AuthProvider.Config["idp-certificate-authority-data"] = "\n"
				} else {
					ca := filepath.Join(filepath.Dir(path), "issuer-ca")
					if kind == "ca-fifo" {
						if e := unix.Mkfifo(ca, 0600); e != nil {
							t.Fatal(e)
						}
					} else {
						if e := os.Symlink(path, ca); e != nil {
							t.Fatal(e)
						}
					}
					c.AuthInfos["fixture"].AuthProvider.Config["idp-certificate-authority"] = "issuer-ca"
				}
				raw, _ = clientcmd.Write(*c)
				if e := os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
			}
			var calls atomic.Int32
			oidcServer(t, func(r *http.Request) string {
				calls.Add(1)
				if r.Method == "GET" {
					if kind == "discovery-oversize" {
						return strings.Repeat(" ", (1<<20)+1)
					}
					if kind == "issuer-mismatch" {
						return `{"issuer":"https://wrong.fixture.invalid","token_endpoint":"http://issuer.fixture.invalid/token"}`
					}
					return `{"token_endpoint":"http://issuer.fixture.invalid/token"}`
				}
				if kind == "token-oversize" {
					return strings.Repeat(" ", (1<<20)+1)
				}
				if kind == "destination-replaced" {
					if e := os.Rename(path, path+".retained"); e != nil {
						t.Error(e)
					}
					if e := os.Symlink(path+".retained", path); e != nil {
						t.Error(e)
					}
					return fmt.Sprintf(`{"id_token":%q,"refresh_token":"synthetic-new"}`, oidcJWT(time.Now().Add(time.Hour)))
				}
				return `{"id_token":"invalid","refresh_token":"synthetic-new"}`
			})
			config, e := LoadContext(context.Background(), cfg())
			if e != nil {
				t.Fatal(e)
			}
			if e := oidcDo(t, config, context.Background()); e == nil {
				t.Fatal("unsafe refresh accepted")
			}
			if strings.HasPrefix(kind, "ca-") || kind == "empty-ca" {
				if calls.Load() != 0 {
					t.Fatal("unsafe CA contacted issuer")
				}
			}
			if kind == "destination-replaced" {
				st, e := os.Lstat(path)
				if e != nil || st.Mode()&os.ModeSymlink == 0 {
					t.Fatal("unsafe replacement overwritten", e)
				}
			}
			if _, e := os.Lstat(path + ".lock"); !os.IsNotExist(e) {
				t.Fatal("refresh lock leaked", e)
			}
		})
	}
}
func TestOIDCTransportHeaderTimeout(t *testing.T) {
	oidcFixture(t)
	release := make(chan struct{})
	defer close(release)
	oidcServer(t, func(*http.Request) string { <-release; return `{}` })
	config, e := LoadContext(context.Background(), cfg())
	if e != nil {
		t.Fatal(e)
	}
	config.Timeout = 0
	start := time.Now()
	if e := oidcDo(t, config, context.Background()); e == nil {
		t.Fatal("stalled issuer succeeded")
	}
	if elapsed := time.Since(start); elapsed > 7*time.Second {
		t.Fatal("response header timeout not enforced", elapsed)
	}
}

func TestOIDCVerifiedIssuerTLS(t *testing.T) {
	cert, ca := compatibilityCertificate(t)
	for _, kind := range []string{"embedded", "relative-file", "untrusted"} {
		t.Run(kind, func(t *testing.T) {
			path := oidcFixture(t)
			raw, _ := os.ReadFile(path)
			c, _ := clientcmd.Load(raw)
			p := c.AuthInfos["fixture"].AuthProvider.Config
			p["idp-issuer-url"] = "https://api.fixture.invalid"
			if kind == "embedded" {
				p["idp-certificate-authority-data"] = base64.StdEncoding.EncodeToString(ca)
			}
			if kind == "relative-file" {
				p["idp-certificate-authority"] = "issuer.pem"
				if e := os.WriteFile(filepath.Join(filepath.Dir(path), "issuer.pem"), ca, 0600); e != nil {
					t.Fatal(e)
				}
			}
			raw, _ = clientcmd.Write(*c)
			if e := os.WriteFile(path, raw, 0600); e != nil {
				t.Fatal(e)
			}
			var calls atomic.Int32
			oidcServerConnection(t, func(r *http.Request) string {
				calls.Add(1)
				if r.Method == "GET" {
					return `{"issuer":"https://api.fixture.invalid","token_endpoint":"https://api.fixture.invalid/token"}`
				}
				return fmt.Sprintf(`{"id_token":%q,"refresh_token":"synthetic-new"}`, oidcJWT(time.Now().Add(time.Hour)))
			}, func(c net.Conn) net.Conn { return tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}}) })
			config, e := LoadContext(context.Background(), cfg())
			if e != nil {
				t.Fatal(e)
			}
			e = oidcDo(t, config, context.Background())
			if kind == "untrusted" {
				if e == nil || calls.Load() != 0 {
					t.Fatal("issuer TLS trust bypassed", e, calls.Load())
				}
			} else if e != nil || calls.Load() != 2 {
				t.Fatal("verified issuer refresh failed", e, calls.Load())
			}
		})
	}
}

func TestOIDCPrivateUmaskChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOIDCRotationPersists$")
	child.Env = append(os.Environ(), "JEV_OIDC_UMASK=1")
	if b, e := child.CombinedOutput(); e != nil {
		t.Fatalf("private umask rotation failed: %v %s", e, b)
	}
}
