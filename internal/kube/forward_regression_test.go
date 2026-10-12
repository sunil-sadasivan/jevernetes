package kube_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/dashboard"
	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

type observedForwarder struct {
	kube.Forwarder
	opened chan kube.Tunnel
}

func (f observedForwarder) Open(ctx context.Context, namespace, pod string, port int) (kube.Tunnel, error) {
	tunnel, err := f.Forwarder.Open(ctx, namespace, pod, port)
	if err == nil {
		f.opened <- tunnel
	}
	return tunnel, err
}

type countedTunnelSocket struct {
	net.Conn
	closes       atomic.Int32
	closed       chan struct{}
	watchWrites  atomic.Bool
	writeStarted chan struct{}
	writeOnce    sync.Once
}

func (c *countedTunnelSocket) Write(p []byte) (int, error) {
	if c.watchWrites.Load() {
		c.writeOnce.Do(func() { close(c.writeStarted) })
	}
	return c.Conn.Write(p)
}

func (c *countedTunnelSocket) Close() error {
	err := c.Conn.Close()
	if c.closes.Add(1) == 1 {
		close(c.closed)
	}
	return err
}

// net.Pipe has no write buffering: after the valid upgrade every SPDY write
// blocks until the tunnel closes its raw socket. The peer is only released
// after shutdown has been checked, including on the failing implementation.
func TestSPDYStalledPeerShutdown(t *testing.T) {
	for _, owner := range []string{"close", "active-close", "cancel", "concurrent", "server", "dashboard"} {
		t.Run(owner, func(t *testing.T) {
			client, peer := net.Pipe()
			socket := &countedTunnelSocket{Conn: client, closed: make(chan struct{}), writeStarted: make(chan struct{})}
			release, peerDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(peerDone)
				req, err := http.ReadRequest(bufio.NewReader(peer))
				if err == nil {
					if req.Method != "POST" || req.Header.Get("X-Stream-Protocol-Version") != "portforward.k8s.io" {
						t.Error("unexpected upgrade request")
					}
					_, err = io.WriteString(peer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\nX-Stream-Protocol-Version: portforward.k8s.io\r\n\r\n")
				}
				if err != nil {
					t.Errorf("peer handshake: %v", err)
				}
				<-release
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer func() {
				peer.Close()
				close(release)
				<-peerDone
			}()
			wait := func(done <-chan struct{}, message string) {
				t.Helper()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error(message)
					// Unblock and join test workers even when testing the broken code.
					peer.Close()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("worker failed to join after peer close")
					}
				}
			}
			cfg := &rest.Config{Host: "http://fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return socket, nil }}
			opened := make(chan kube.Tunnel, 1)
			forward := observedForwarder{Forwarder: kube.SPDYForwarder{Config: cfg}, opened: opened}
			done := make(chan struct{})
			if owner == "server" || owner == "dashboard" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					tunnel, err := forward.Open(r.Context(), "local", "pod", 9091)
					if err != nil {
						t.Error(err)
						return
					}
					defer tunnel.Close()
					<-r.Context().Done()
				}))
				if owner == "dashboard" {
					api := fake.NewClientset(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "local"}, Status: v1.PodStatus{Phase: v1.PodRunning}})
					remote := &inspect.Client{API: api.CoreV1(), Forward: forward, Config: kube.Config{Namespace: "local", Pod: "pod", MaxBytes: 1024, MaxStreams: 1}, Port: 9091}
					handler = (&dashboard.Dashboard{Remote: remote}).Handler(listener.Addr().String())
				}
				go func() { defer close(done); _ = inspect.Serve(ctx, listener, handler, true) }()
				clientDone := make(chan struct{})
				go func() {
					defer close(clientDone)
					res, err := http.Get("http://" + listener.Addr().String() + "/api/controller/status")
					if err == nil {
						io.Copy(io.Discard, res.Body)
						res.Body.Close()
					}
				}()
				defer func() { cancel(); wait(done, "server failed to join"); wait(clientDone, "HTTP client failed to join") }()
			} else {
				go func() {
					if _, err := forward.Open(ctx, "local", "pod", 9091); err != nil {
						t.Errorf("Open: %v", err)
					}
				}()
			}
			var tunnel kube.Tunnel
			select {
			case tunnel = <-opened:
			case <-time.After(5 * time.Second):
				t.Fatal("valid upgrade did not permit Open success")
			}
			switch owner {
			case "active-close":
				socket.watchWrites.Store(true)
				u, err := url.Parse(tunnel.BaseURL())
				if err != nil {
					t.Fatal(err)
				}
				local, err := net.DialTimeout("tcp", u.Host, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer local.Close()
				wait(socket.writeStarted, "local connection did not start a SPDY write")
				go func() {
					defer close(done)
					tunnel.Close()
					// The blocked stream creator must also exit and close its
					// local connection without the peer accepting its write.
					local.SetReadDeadline(time.Now().Add(time.Second))
					var b [1]byte
					if _, err := local.Read(b[:]); err != io.EOF {
						t.Errorf("local stream did not close: %v", err)
					}
				}()
			case "close":
				go func() { defer close(done); tunnel.Close() }()
			case "cancel":
				cancel()
				// Observe cancellation independently of an explicit Close call.
				wait(socket.closed, "cancellation did not close the raw socket")
				go func() { defer close(done); tunnel.Close() }()
			case "concurrent":
				go func() {
					defer close(done)
					var workers sync.WaitGroup
					for i := 0; i < 8; i++ {
						workers.Add(1)
						go func() { defer workers.Done(); tunnel.Close() }()
					}
					cancel()
					workers.Wait()
				}()
			default:
				cancel()
			}
			wait(done, "shutdown waited for the non-reading SPDY peer")
			if err := tunnel.Close(); err != nil {
				t.Error(err)
			}
			if count := socket.closes.Load(); count != 1 {
				t.Errorf("raw socket closed %d times, want exactly once", count)
			}
		})
	}
}

// The endpoint deliberately stays blocked until AFTER the assertions: closing
// the test server must never be what releases Open or the owning server.
func TestSPDYStalledUpgradeCancellation(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		for _, owner := range []string{"open", "server", "dashboard"} {
			t.Run(owner+map[bool]string{false: "/upgrade", true: "/proxy-connect"}[proxy], func(t *testing.T) {
				accepted, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					expected := "POST"
					if proxy {
						expected = "CONNECT"
					}
					if r.Method != expected {
						t.Errorf("method=%s", r.Method)
					}
					once.Do(func() { close(accepted) })
					<-release
				}))
				defer endpoint.Close()
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				cfg := &rest.Config{Host: endpoint.URL, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}
				if proxy {
					p, _ := url.Parse(endpoint.URL)
					cfg.Host = "https://fixture.invalid"
					cfg.Proxy = http.ProxyURL(p)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				open := func(ctx context.Context) {
					tunnel, err := (kube.SPDYForwarder{Config: cfg}).Open(ctx, "local", "pod", 9091)
					if tunnel != nil {
						tunnel.Close()
					}
					if err == nil {
						t.Error("cancelled upgrade succeeded")
					}
				}
				if owner == "open" {
					go func() { defer close(done); open(ctx) }()
				} else {
					listener, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { open(r.Context()) }))
					if owner == "dashboard" {
						api := fake.NewClientset(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "local"}, Status: v1.PodStatus{Phase: v1.PodRunning}})
						remote := &inspect.Client{API: api.CoreV1(), Forward: kube.SPDYForwarder{Config: cfg}, Config: kube.Config{Namespace: "local", Pod: "pod", MaxBytes: 1024, MaxStreams: 1}, Port: 9091}
						handler = (&dashboard.Dashboard{Remote: remote}).Handler(listener.Addr().String())
					}
					go func() { defer close(done); _ = inspect.Serve(ctx, listener, handler, true) }()
					clientDone := make(chan struct{})
					go func() {
						defer close(clientDone)
						res, err := http.Get("http://" + listener.Addr().String() + "/api/controller/status")
						if err == nil {
							io.Copy(io.Discard, res.Body)
							res.Body.Close()
						}
					}()
					defer func() {
						select {
						case <-clientDone:
						case <-time.After(5 * time.Second):
							t.Error("client failed to join")
						}
					}()
				}
				select {
				case <-accepted:
				case <-time.After(5 * time.Second):
					t.Fatal("request not accepted")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("cancellation waited for endpoint release")
					// Release and join even on the broken implementation.
					unblock()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("owner did not join after release")
					}
				}
			})
		}
	}
}
