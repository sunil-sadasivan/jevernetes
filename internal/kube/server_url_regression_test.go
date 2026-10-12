package kube

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

func TestSPDYResolvedServerURL(t *testing.T) {
	cert, ca := compatibilityCertificate(t)
	for _, tc := range []struct {
		name, host, sni, path string
		secure                bool
	}{
		{"schemeless-http", "localhost:8001", "", "", false},
		{"http-prefix", "http://localhost:8001/proxy", "", "/proxy", false},
		{"https", "https://api.fixture.invalid:8443/proxy", "api.fixture.invalid", "/proxy", true},
		{"schemeless-https", "api.fixture.invalid:8443", "api.fixture.invalid", "", true},
		{"explicit-sni", "https://127.0.0.1:8443", "api.fixture.invalid", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, peer := net.Pipe()
			defer client.Close()
			defer peer.Close()
			done := make(chan error, 1)
			go func() {
				var conn net.Conn = peer
				if tc.secure {
					conn = tls.Server(peer, &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
						if h.ServerName != tc.sni {
							return nil, fmt.Errorf("SNI=%q, want %q", h.ServerName, tc.sni)
						}
						return nil, nil
					}})
				}
				r, err := http.ReadRequest(bufio.NewReader(conn))
				if err == nil {
					defer r.Body.Close()
					if r.Method != "POST" || r.URL.Path != tc.path+"/api/v1/namespaces/local/pods/pod/portforward" || r.Header.Get("Upgrade") != "SPDY/3.1" {
						err = fmt.Errorf("unexpected upgrade request: %s %s", r.Method, r.URL)
					}
					io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
				}
				done <- err
			}()
			cfg := &rest.Config{Host: tc.host, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
			if tc.secure {
				cfg.CAData = ca
			}
			if tc.name == "explicit-sni" {
				cfg.ServerName = tc.sni
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			tunnel, err := (SPDYForwarder{Config: cfg}).Open(ctx, "local", "pod", 9091)
			if tunnel != nil {
				tunnel.Close()
			}
			if err == nil {
				t.Error("fixture rejection ignored")
			}
			client.Close()
			if err := <-done; err != nil {
				t.Fatalf("upgrade never reached resolved endpoint: %v", err)
			}
		})
	}
}
