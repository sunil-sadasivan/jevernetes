package kube

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
)

// New must bind identity from the resolved config, not caller context or pod IDs.
// Fake API/log I/O keeps endpoint names synthetic and never resolves them.
func collectedCompatibilityEvents(t *testing.T, endpoint string, config Config) []event.Event {
	t.Helper()
	c, err := New(&rest.Config{Host: endpoint}, config)
	if err != nil {
		t.Fatal(err)
	}
	p := pod("one")
	p.Status.ContainerStatuses[0].RestartCount = 1
	p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &v1.ContainerStateTerminated{ContainerID: "runtime://previous"}
	p.Status.InitContainerStatuses = []v1.ContainerStatus{{Name: "init", ContainerID: "runtime://init"}}
	p.Status.EphemeralContainerStatuses = []v1.ContainerStatus{{Name: "debug", ContainerID: "runtime://debug"}}
	c.API = fake.NewClientset(p).CoreV1()
	c.OpenLog = func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("2026-01-01T00:00:00Z INFO ready\n")), nil
	}
	var events []event.Event
	if err := c.Snapshot(context.Background(), func(e event.Event) error { events = append(events, e); return nil }, func(event.Source, string) { t.Error("unexpected gap") }); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("fixture complete")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = c.followStream(ctx, streams(*p, false, true)[0], func(e event.Event) error { events = append(events, e); return stop }, func(event.Source, string) {})
	if !errors.Is(err, stop) || len(events) != 5 {
		t.Fatalf("collection: %v, events %d", err, len(events))
	}
	return events
}

type compatibilityJudge struct{ calls *int }

func (j compatibilityJudge) Judge(_ context.Context, ds []provider.Dossier) ([]event.Judgment, error) {
	out := make([]event.Judgment, len(ds))
	for i := range out {
		*j.calls++
		out[i] = event.Judgment{Importance: "routine", Severity: "info", Category: "routine", ImportanceConfidence: event.Confidence(.99), SeverityConfidence: event.Confidence(.99), CategoryConfidence: event.Confidence(.99)}
	}
	return out, nil
}
func TestCollectorEndpointDurableIdentity(t *testing.T) {
	for _, mode := range []string{"context", "default", "in-cluster"} {
		t.Run(mode, func(t *testing.T) {
			config := cfg()
			if mode == "context" {
				config.Context = "shared"
			}
			config.InCluster = mode == "in-cluster"
			a := collectedCompatibilityEvents(t, "https://cluster-a.invalid/prefix", config)
			b := collectedCompatibilityEvents(t, "https://cluster-b.invalid/prefix", config)
			same := collectedCompatibilityEvents(t, "https://cluster-a.invalid/prefix", config)
			for i := range a {
				if a[i].Source["context"] != config.Context || a[i].Source["pod_uid"] != "fixture-uid" {
					t.Error("attribution changed")
				}
				if event.Hash(a[i].Source) == event.Hash(b[i].Source) {
					t.Error("different endpoints share event source")
				}
				if event.Hash(analysis.Scope(a[i], true)) == event.Hash(analysis.Scope(b[i], true)) {
					t.Error("different endpoints share drain scope")
				}
				if event.Hash(a[i].Source) != event.Hash(same[i].Source) {
					t.Error("same endpoint identity unstable")
				}
			}
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "state.db")
			calls := 0
			batch := func(e event.Event) {
				store, err := controller.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				engine, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 8, TTL: time.Hour, NormalInterval: 32}, controller.CachedJudge{Store: store, Contract: "fixture", TTL: time.Hour, Next: compatibilityJudge{&calls}})
				if err != nil {
					t.Fatal(err)
				}
				engine.Batch(context.Background(), []event.Event{e})
			}
			batch(a[0])
			batch(b[0])
			batch(same[0])
			replica := same[0]
			replica.Source = maps.Clone(replica.Source)
			replica.Source["pod"] = "replica"
			replica.Source["pod_uid"] = "replica-uid"
			replica.Source["container_id"] = "runtime://replica"
			batch(replica)
			if calls != 2 {
				t.Fatalf("durable cache crossed endpoint or lost same-cluster reuse: calls=%d, want 2", calls)
			}
		})
	}
}

// Real TLS records travel over net.Pipe, including TLS-in-TLS after HTTPS
// CONNECT. No listener permissions, DNS, or external endpoint are required.
func compatibilityCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"api.fixture.invalid", "override.fixture.invalid", "proxy.fixture.invalid"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func TestSPDYTargetSNI(t *testing.T) {
	cert, ca := compatibilityCertificate(t)
	for _, route := range []string{"direct", "http", "https", "socks5"} {
		for _, mode := range []string{"insecure", "verified", "explicit", "ip"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				host, want := "api.fixture.invalid", "api.fixture.invalid"
				if mode == "explicit" {
					want = "override.fixture.invalid"
				}
				if mode == "ip" {
					host = "127.0.0.1"
					want = ""
				}
				client, peer := net.Pipe()
				defer client.Close()
				defer peer.Close()
				done := make(chan error, 1)
				go func() {
					var conn net.Conn = peer
					secure := func(want string) error {
						c := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
							if h.ServerName != want {
								return nil, fmt.Errorf("SNI %q, want %q", h.ServerName, want)
							}
							return nil, nil
						}})
						if err := c.Handshake(); err != nil {
							return err
						}
						conn = c
						return nil
					}
					run := func() error {
						if route == "https" {
							// Explicit ServerName retains the existing proxy TLS behavior too.
							proxyName := "proxy.fixture.invalid"
							if mode == "explicit" {
								proxyName = want
							}
							if err := secure(proxyName); err != nil {
								return err
							}
						}
						if route == "http" || route == "https" {
							r, err := http.ReadRequest(bufio.NewReader(conn))
							if err != nil {
								return err
							}
							if r.Method != "CONNECT" || r.Host != host+":443" {
								return errors.New("incorrect CONNECT target")
							}
							if _, err = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
								return err
							}
						}
						if route == "socks5" {
							var greeting [3]byte
							if _, err := io.ReadFull(conn, greeting[:]); err != nil {
								return err
							}
							if greeting != [3]byte{5, 1, 0} {
								return errors.New("bad SOCKS greeting")
							}
							if _, err := conn.Write([]byte{5, 0}); err != nil {
								return err
							}
							var head [4]byte
							if _, err := io.ReadFull(conn, head[:]); err != nil {
								return err
							}
							size := 4
							if head[3] == 3 {
								var n [1]byte
								if _, err := io.ReadFull(conn, n[:]); err != nil {
									return err
								}
								size = int(n[0])
							} else if head[3] != 1 {
								return errors.New("bad SOCKS address")
							}
							if _, err := io.ReadFull(conn, make([]byte, size+2)); err != nil {
								return err
							}
							if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
								return err
							}
						}
						if err := secure(want); err != nil {
							return err
						}
						if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
							return err
						}
						_, err := io.WriteString(conn, "HTTP/1.1 204 No Content\r\n\r\n")
						return err
					}
					done <- run()
				}()
				config := &rest.Config{Host: "https://" + host, TLSClientConfig: rest.TLSClientConfig{Insecure: mode != "verified"}, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}
				if mode == "verified" {
					config.CAData = ca
				}
				if mode == "explicit" {
					config.ServerName = want
				}
				expectedAddress := host + ":443"
				if route != "direct" {
					p, _ := url.Parse(route + "://proxy.fixture.invalid:1234")
					config.Proxy = http.ProxyURL(p)
					expectedAddress = p.Host
				}
				config.Dial = func(_ context.Context, _, address string) (net.Conn, error) {
					if address != expectedAddress {
						t.Errorf("dial %q, want %q", address, expectedAddress)
					}
					return client, nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				rt, up, err := spdyTransport(ctx, config)
				if err != nil {
					t.Fatal(err)
				}
				defer up.close()
				req, _ := http.NewRequestWithContext(ctx, "POST", config.Host, nil)
				response, err := rt.RoundTrip(req)
				client.Close()
				peerErr := <-done
				if err != nil || peerErr != nil {
					t.Fatalf("SNI-required TLS failed: client=%v; peer=%v", err, peerErr)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					t.Fatal(response.StatusCode)
				}
			})
		}
	}
}

func TestOIDCCachedTokenCompatibility(t *testing.T) {
	var refreshes atomic.Int32
	// The standard plugin uses DefaultTransport for issuer discovery. Tripwire
	// prevents all network access even if this regression accidentally refreshes.
	original := http.DefaultTransport
	http.DefaultTransport = fileRoundTripFunc(func(*http.Request) (*http.Response, error) {
		refreshes.Add(1)
		return nil, errors.New("refresh forbidden")
	})
	defer func() { http.DefaultTransport = original }()
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
	raw := api.NewConfig()
	raw.Clusters["fixture"] = &api.Cluster{Server: "https://api.fixture.invalid"}
	raw.AuthInfos["fixture"] = &api.AuthInfo{AuthProvider: &api.AuthProviderConfig{Name: "oidc", Config: map[string]string{"idp-issuer-url": "http://issuer.fixture.invalid", "client-id": fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()), "id-token": token, "refresh-token": "synthetic"}}}
	raw.Contexts["fixture"] = &api.Context{Cluster: "fixture", AuthInfo: "fixture"}
	raw.CurrentContext = "fixture"
	data, err := clientcmd.Write(*raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	config, err := LoadContext(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if config.AuthProvider == nil {
		t.Fatal("kubeconfig lost auth provider")
	}
	config.Host = "http://api.fixture.invalid"
	config.Transport = fileRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("cached id-token not applied")
		}
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	client, err := HTTPClientFor(config)
	if err != nil {
		t.Fatal("OIDC config initialization:", err)
	}
	response, err := client.Get(config.Host)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	config.Transport = nil
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	config.Dial = func(context.Context, string, string) (net.Conn, error) { return a, nil }
	config.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	done := make(chan error, 1)
	go func() {
		r, err := http.ReadRequest(bufio.NewReader(b))
		if err == nil {
			if r.Header.Get("Authorization") != "Bearer "+token {
				err = errors.New("SPDY cached id-token not applied")
			}
			io.WriteString(b, "HTTP/1.1 204 No Content\r\n\r\n")
		}
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rt, up, err := spdyTransport(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer up.close()
	req, _ := http.NewRequestWithContext(ctx, "POST", config.Host, nil)
	response, err = rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 0 {
		t.Fatal("valid cached token attempted refresh")
	}
}

func TestCollectorEndpointIdentityPrivacyAndLegacy(t *testing.T) {
	config := cfg()
	baseline := collectedCompatibilityEvents(t, "https://cluster.fixture.invalid/prefix", config)[0]
	id, ok := baseline.Source["cluster_id"].(string)
	if !ok || len(id) != 64 {
		t.Error("missing opaque cluster identity")
	}
	endpoint, _ := url.Parse("https://CLUSTER.fixture.invalid:443/prefix/")
	endpoint.User = url.UserPassword("synthetic-user", "synthetic-pass")
	endpoint.RawQuery = "synthetic-query=fixture"
	endpoint.Fragment = "synthetic-fragment"
	same := collectedCompatibilityEvents(t, endpoint.String(), config)[0]
	if baseline.Source["cluster_id"] != same.Source["cluster_id"] {
		t.Error("credentials/query or equivalent spelling changed identity")
	}
	for _, endpoint := range []string{"http://cluster.fixture.invalid/prefix", "https://cluster.fixture.invalid:8443/prefix", "https://cluster.fixture.invalid/other"} {
		other := collectedCompatibilityEvents(t, endpoint, config)[0]
		if baseline.Source["cluster_id"] == other.Source["cluster_id"] {
			t.Error("scheme, port, or API prefix identity collided")
		}
	}
	raw, _ := json.Marshal(same.Source)
	for _, private := range []string{"cluster.fixture.invalid", "synthetic-user", "synthetic-pass", "synthetic-query", "synthetic-fragment", "/prefix"} {
		if strings.Contains(strings.ToLower(string(raw)), private) {
			t.Error("endpoint material exposed")
		}
	}
	// Seed the exact pre-fix durable key and ensure it cannot be loaded again.
	legacyScope := maps.Clone(analysis.Scope(baseline, true))
	delete(legacyScope, "cluster_id")
	version := event.Hash([]any{"go-template-version-v2", "drain", "strict", "drain", analysis.Template(baseline.Text, false)})
	template := event.Hash([]any{"go-template-v2", legacyScope, version, baseline.Baseline.Important})
	oldKey := event.Hash([]any{"go-group-cache-v2", "legacy", event.Hash(legacyScope), template, version, baseline.Baseline.Important})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := controller.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	calls := 0
	judge := compatibilityJudge{&calls}
	verdicts, _ := judge.Judge(context.Background(), []provider.Dossier{{}})
	calls = 0
	if err := store.PutVerdict(context.Background(), oldKey, verdicts[0], time.Now().Unix(), time.Hour); err != nil {
		t.Fatal(err)
	}
	engine, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 8, TTL: time.Hour, NormalInterval: 32}, controller.CachedJudge{Store: store, Contract: "legacy", TTL: time.Hour, Next: judge})
	if err != nil {
		t.Fatal(err)
	}
	engine.Batch(context.Background(), []event.Event{baseline})
	if calls != 1 {
		t.Fatal("pre-endpoint cache key remained reachable")
	}
}

func TestUnsupportedAuthCompatibility(t *testing.T) {
	for _, name := range []string{"unsupported-fixture", "gcp", "azure"} {
		config := &rest.Config{Host: "https://api.fixture.invalid", AuthProvider: &api.AuthProviderConfig{Name: name}}
		if _, err := HTTPClientFor(config); err == nil {
			t.Errorf("HTTP accepted unsupported auth %s", name)
		}
		if _, _, err := spdyTransport(context.Background(), config); err == nil {
			t.Errorf("SPDY accepted unsupported auth %s", name)
		}
		if _, err := New(config, cfg()); err == nil || strings.Contains(err.Error(), name) {
			t.Error("collector must reject unsupported auth with sanitized error")
		}
	}
	config := &rest.Config{Host: "https://api.fixture.invalid", AuthProvider: &api.AuthProviderConfig{Name: "oidc", Config: map[string]string{"idp-issuer-url": "http://issuer.fixture.invalid", "client-id": t.Name()}}, ExecProvider: &api.ExecConfig{Command: "must-not-execute"}}
	if _, err := HTTPClientFor(config); err == nil {
		t.Error("mixed OIDC/exec auth accepted")
	}
	if _, _, err := spdyTransport(context.Background(), config); err == nil {
		t.Error("SPDY mixed OIDC/exec auth accepted")
	}
}

func TestSPDYTargetSNICancellation(t *testing.T) {
	for _, route := range []string{"direct", "http", "https", "socks5"} {
		t.Run(route, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			config := &rest.Config{Host: "https://api.fixture.invalid", TLSClientConfig: rest.TLSClientConfig{Insecure: true}, Dial: func(context.Context, string, string) (net.Conn, error) { return a, nil }, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}
			if route != "direct" {
				p, _ := url.Parse(route + "://proxy.fixture.invalid:1234")
				config.Proxy = http.ProxyURL(p)
			}
			started, joined := make(chan struct{}), make(chan struct{})
			go func() { defer close(joined); var v [1]byte; b.Read(v[:]); close(started); io.Copy(io.Discard, b) }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt, up, err := spdyTransport(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer up.close()
			done := make(chan error, 1)
			go func() {
				req, _ := http.NewRequestWithContext(ctx, "POST", config.Host, nil)
				_, err := rt.RoundTrip(req)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("negotiation did not start")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Error("stalled handshake succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("negotiation did not cancel")
			}
			select {
			case <-joined:
			case <-time.After(time.Second):
				t.Fatal("peer remained connected")
			}
		})
	}
}
