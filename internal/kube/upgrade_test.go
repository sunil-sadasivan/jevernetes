package kube

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
	clientspdy "k8s.io/client-go/transport/spdy"
)

func TestSPDYSocketCancellationStages(t *testing.T) {
	for _, stage := range []string{"request-write", "tls", "https-proxy-tls", "socks5", "connect", "error-body"} {
		for _, deadline := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "/cancel", true: "/deadline"}[deadline], func(t *testing.T) {
				client, server := net.Pipe()
				defer server.Close()
				started, peerDone := make(chan struct{}), make(chan struct{})
				allowRead := make(chan struct{})
				var releaseOnce sync.Once
				releaseRead := func() { releaseOnce.Do(func() { close(allowRead) }) }
				defer releaseRead()
				go func() {
					defer close(peerDone)
					switch stage {
					case "request-write":
						<-allowRead
						io.Copy(io.Discard, server)
					case "error-body":
						http.ReadRequest(bufio.NewReader(server))
						io.WriteString(server, "HTTP/1.1 403 Forbidden\r\nContent-Length: 999\r\n\r\n")
						close(started)
						io.Copy(io.Discard, server)
					case "connect":
						http.ReadRequest(bufio.NewReader(server))
						close(started)
						io.Copy(io.Discard, server)
					default:
						var b [1]byte
						server.Read(b[:])
						close(started)
						io.Copy(io.Discard, server)
					}
				}()
				cfg := &rest.Config{Host: "http://fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) {
					if stage == "request-write" {
						return &writeObservedConn{Conn: client, started: started}, nil
					}
					return client, nil
				}}
				switch stage {
				case "tls":
					cfg.Host = "https://fixture.invalid"
				case "https-proxy-tls", "socks5", "connect":
					scheme := map[string]string{"https-proxy-tls": "https", "socks5": "socks5", "connect": "http"}[stage]
					p, _ := url.Parse(scheme + "://proxy.invalid:1234")
					cfg.Proxy = http.ProxyURL(p)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if deadline {
					ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
					defer cancel()
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					tunnel, err := (SPDYForwarder{Config: cfg}).Open(ctx, "local", "pod", 9091)
					if tunnel != nil {
						tunnel.Close()
					}
					if err == nil {
						t.Error("stalled upgrade succeeded")
					}
				}()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("stage did not start")
				}
				if !deadline {
					cancel()
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					client.Close()
					t.Fatal("socket stage did not cancel")
				}
				releaseRead()
				select {
				case <-peerDone:
				case <-time.After(time.Second):
					t.Fatal("peer socket remained open")
				}
			})
		}
	}
}

// Exercise TLS verification, custom dial hooks, exec token auth, CONNECT auth,
// and the transfer from HTTP handshake cancellation to SPDY stream ownership.
func TestSPDYAuthenticatedTLSAndProxy(t *testing.T) {
	for _, proxyKind := range []string{"direct", "http", "https"} {
		t.Run(proxyKind, func(t *testing.T) {
			serverDone := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				if len(r.TLS.PeerCertificates) != 1 || r.TLS.PeerCertificates[0].SerialNumber.Int64() != 42 {
					t.Error("exec client certificate missing")
				}
				if r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("Proxy-Authorization") != "" {
					t.Error("incorrect API auth")
				}
				if _, err := httpstream.Handshake(r, w, []string{"fixture"}); err != nil {
					t.Error(err)
					return
				}
				conn := streamspdy.NewResponseUpgrader().UpgradeResponse(w, r, func(httpstream.Stream, <-chan struct{}) error { return nil })
				if conn == nil {
					t.Error("upgrade failed")
					return
				}
				defer conn.Close()
				<-conn.CloseChan()
			}))
			server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
			server.StartTLS()
			defer server.Close()
			cert, key := syntheticCertificate(t, 42)
			payload, _ := json.Marshal(map[string]any{"apiVersion": "client.authentication.k8s.io/v1", "kind": "ExecCredential", "status": map[string]string{"token": "synthetic", "clientCertificateData": cert, "clientKeyData": key}})
			cfg, _, _ := payloadConfig(t, string(payload))
			cfg.Host = server.URL
			cfg.TLSClientConfig = rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}
			cfg.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
			var proxyWorkers sync.WaitGroup
			if proxyKind != "direct" {
				proxyServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "CONNECT" || r.Header.Get("Proxy-Authorization") != "Basic c3ludGhldGljOnBhc3M=" || r.Header.Get("Authorization") != "" {
						t.Error("incorrect proxy auth")
					}
					target, err := net.Dial("tcp", server.Listener.Addr().String())
					if err != nil {
						t.Error(err)
						return
					}
					conn, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						target.Close()
						t.Error(err)
						return
					}
					proxyWorkers.Add(1)
					defer proxyWorkers.Done()
					defer target.Close()
					defer conn.Close()
					io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
					done := make(chan struct{})
					go func() { defer close(done); io.Copy(target, buffer); target.Close() }()
					io.Copy(conn, target)
					conn.Close()
					<-done
				}))
				if proxyKind == "https" {
					proxyServer.StartTLS()
					cfg.CAData = append(cfg.CAData, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxyServer.Certificate().Raw})...)
				} else {
					proxyServer.Start()
				}
				defer func() { proxyServer.Close(); proxyWorkers.Wait() }()
				p, _ := url.Parse(proxyServer.URL)
				p.User = url.UserPassword("synthetic", "pass")
				cfg.Proxy = func(r *http.Request) (*url.URL, error) {
					if r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("Upgrade") != streamspdy.HeaderSpdy31 {
						t.Error("proxier did not receive authenticated upgrade request")
					}
					return p, nil
				}
			}
			dialed := false
			cfg.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				dialed = true
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt, upgrader, err := spdyTransport(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer upgrader.close()
			if config, err := utilnet.TLSClientConfig(rt); err != nil || config == nil || config.RootCAs == nil {
				t.Fatal("transport TLS configuration no longer unwraps")
			}
			u, _ := url.Parse(server.URL)
			conn, _, err := clientspdy.NewDialer(upgrader, &http.Client{Transport: rt, Timeout: 5 * time.Second}, "POST", u).Dial("fixture")
			if err != nil {
				t.Fatal(err)
			}
			if !dialed {
				t.Error("custom dial hook ignored")
			}
			// The response body has closed already. Its canceled context must not kill
			// the upgraded connection; server close is the independent observation.
			select {
			case <-serverDone:
				t.Error("handshake cleanup closed live SPDY connection")
			case <-time.After(30 * time.Millisecond):
			}
			conn.Close()
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("SPDY peer not joined")
			}
		})
	}
}

func TestSPDYRejectsUntrustedTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted TLS accepted") }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cfg := &rest.Config{Host: server.URL, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}
	tunnel, err := (SPDYForwarder{Config: cfg}).Open(ctx, "local", "pod", 9091)
	if tunnel != nil {
		tunnel.Close()
	}
	if err == nil {
		t.Fatal("untrusted TLS succeeded")
	}
}

// Ensure cancellation's socket callback has finished before Close returns.
func TestUpgradeSocketJoinsCancellation(t *testing.T) {
	for i := 0; i < 100; i++ {
		a, b := net.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		c := newUpgradeSocket(ctx, a)
		done := make(chan struct{})
		go func() { defer close(done); cancel() }()
		c.Close()
		<-done
		c.detach()
		b.Close()
	}
}

type writeObservedConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *writeObservedConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}
