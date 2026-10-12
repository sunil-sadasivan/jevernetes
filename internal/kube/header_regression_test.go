package kube

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
)

// Count bytes accepted by a synchronous pipe peer, rather than relying on heap
// sampling or a server that eventually finishes its oversized response.
func TestSPDYHandshakeHeaderBound(t *testing.T) {
	cert, ca := compatibilityCertificate(t)
	for _, route := range []string{"direct-http", "direct-https", "proxy-http", "proxy-https"} {
		for _, kind := range []string{"status", "single-header", "aggregate-headers"} {
			t.Run(route+"/"+kind, func(t *testing.T) {
				client, peer := net.Pipe()
				defer client.Close()
				defer peer.Close()
				cfg := &rest.Config{Host: "http://api.fixture.invalid", TLSClientConfig: rest.TLSClientConfig{CAData: ca}, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
				if route == "direct-https" {
					cfg.Host = "https://api.fixture.invalid"
				}
				if strings.HasPrefix(route, "proxy-") {
					p, _ := url.Parse(strings.TrimPrefix(route, "proxy-") + "://proxy.fixture.invalid")
					cfg.Proxy = http.ProxyURL(p)
				}
				status := "HTTP/1.1 101 "
				if strings.HasPrefix(route, "proxy-") {
					status = "HTTP/1.1 200 "
				}
				payload := status + strings.Repeat("x", 512<<10)
				if kind == "single-header" {
					payload = status + "OK\r\nX-Large: " + strings.Repeat("x", 512<<10)
				} else if kind == "aggregate-headers" {
					payload = status + "OK\r\n" + strings.Repeat("X-Small: value\r\n", 40000)
				}
				type peerResult struct {
					n   int
					err error
				}
				peerDone := make(chan peerResult, 1)
				go func() {
					var conn net.Conn = peer
					if strings.HasSuffix(route, "https") {
						secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
						if err := secure.Handshake(); err != nil {
							peerDone <- peerResult{err: err}
							return
						}
						conn = secure
					}
					if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
						peerDone <- peerResult{err: err}
						return
					}
					n, err := io.WriteString(conn, payload)
					peerDone <- peerResult{n, err}
				}()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				rt, up, err := spdyTransport(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer up.close()
				done := make(chan error, 1)
				go func() {
					req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Host, nil)
					_, err := rt.RoundTrip(req)
					done <- err
				}()
				select {
				case err := <-done:
					if err == nil || !strings.Contains(err.Error(), "response headers too large") {
						t.Errorf("expected bounded header error, got %v", err)
					}
				case <-time.After(time.Second):
					t.Error("oversized handshake did not promptly fail")
					cancel()
					client.Close()
					<-done
				}
				select {
				case result := <-peerDone:
					// TLS can buffer one plaintext record beyond the parser's budget.
					limit := (64 << 10) + 1
					if strings.HasSuffix(route, "https") {
						limit += 16 << 10
					}
					if result.err == nil || result.n == 0 || result.n > limit {
						t.Errorf("peer accepted %d bytes (limit %d), error %v", result.n, limit, result.err)
					}
				case <-time.After(time.Second):
					t.Error("rejected handshake left socket open")
					client.Close()
					<-peerDone
				}
			})
		}
	}
}

// Exercise exact limits and terminators split at every byte, including the LF
// variant accepted by net/http. Payload after the header is never budgeted.
func TestUpgradeHeaderBoundary(t *testing.T) {
	for _, newline := range []string{"\r\n", "\n"} {
		for _, size := range []int{maxUpgradeHeaderBytes - 1, maxUpgradeHeaderBytes, maxUpgradeHeaderBytes + 1} {
			for _, chunk := range []int{1, 3, 4096} {
				t.Run(fmt.Sprintf("%q/%d/%d", newline, size, chunk), func(t *testing.T) {
					prefix := "HTTP/1.1 101 Switching Protocols" + newline + "Connection: Upgrade" + newline + "Upgrade: SPDY/3.1" + newline + "X-Padding: "
					header := prefix + strings.Repeat("x", size-len(prefix)-2*len(newline)) + newline + newline
					payload := strings.Repeat("stream-data\r\n", 20000)
					source := &headerChunkReader{reader: strings.NewReader(header + payload), chunk: chunk}
					response, reader, err := readUpgradeResponse(source, nil)
					if size > maxUpgradeHeaderBytes {
						if err == nil || !strings.Contains(err.Error(), "response headers too large") {
							t.Fatalf("oversize: %v", err)
						}
						if source.read != maxUpgradeHeaderBytes+1 {
							t.Fatalf("read %d", source.read)
						}
						return
					}
					if err != nil || response.StatusCode != 101 {
						t.Fatalf("valid header: %v", err)
					}
					got, err := io.ReadAll(reader)
					if err != nil || string(got) != payload {
						t.Fatalf("stream capped or changed: length %d, %v", len(got), err)
					}
				})
			}
		}
	}
}

type headerChunkReader struct {
	reader      io.Reader
	chunk, read int
}

func (r *headerChunkReader) Read(p []byte) (int, error) {
	if len(p) > r.chunk {
		p = p[:r.chunk]
	}
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func TestSPDYBufferedHandshakeAndUnboundedStream(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		t.Run(fmt.Sprint(proxy), func(t *testing.T) {
			client, peer := net.Pipe()
			defer client.Close()
			defer peer.Close()
			// Deadlines are only a fixture fail-safe, including after detachment.
			client.SetDeadline(time.Now().Add(5 * time.Second))
			peer.SetDeadline(time.Now().Add(5 * time.Second))
			payload := strings.Repeat("synthetic-stream-data", 20000)
			peerDone := make(chan error, 1)
			pingEchoed := make(chan struct{})
			go func() {
				peerDone <- func() error {
					reader := bufio.NewReader(peer)
					if _, err := http.ReadRequest(reader); err != nil {
						return err
					}
					response := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n"
					if proxy {
						response = "HTTP/1.1 200 Connection established\r\n\r\n" + response
					}
					// A server-originated SPDY/3 ping coalesced with the HTTP headers
					// must survive both parsers and be echoed by the actual SPDY client.
					ping := []byte{0x80, 3, 0, 6, 0, 0, 0, 4, 0, 0, 0, 2}
					if _, err := peer.Write(append([]byte(response), ping...)); err != nil {
						return err
					}
					if proxy {
						if _, err := http.ReadRequest(reader); err != nil {
							return err
						}
					}
					echo := make([]byte, len(ping))
					if _, err := io.ReadFull(reader, echo); err != nil {
						return err
					}
					if !bytes.Equal(echo, ping) {
						return fmt.Errorf("buffered ping lost: %x", echo)
					}
					close(pingEchoed)
					written := make(chan error, 1)
					server, err := streamspdy.NewServerConnection(&bufferedUpgradeConn{Conn: peer, reader: reader}, func(s httpstream.Stream, reply <-chan struct{}) error {
						go func() { <-reply; _, err := io.WriteString(s, payload); s.Close(); written <- err }()
						return nil
					})
					if err != nil {
						return err
					}
					defer server.Close()
					select {
					case err := <-written:
						return err
					case <-time.After(5 * time.Second):
						return fmt.Errorf("stream not written")
					}
				}()
			}()
			cfg := &rest.Config{Host: "http://api.fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
			if proxy {
				p, _ := url.Parse("http://proxy.fixture.invalid")
				cfg.Proxy = http.ProxyURL(p)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt, up, err := spdyTransport(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer up.close()
			req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Host, nil)
			response, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := up.NewConnection(response)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			response.Body.Close()
			cancel() // HTTP lifecycle no longer owns the upgraded stream.
			select {
			case <-pingEchoed:
			case err := <-peerDone:
				t.Fatalf("buffered ping failed: %v", err)
			case <-time.After(time.Second):
				t.Fatal("buffered ping not echoed")
			}
			stream, err := conn.CreateStream(http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			got, err := io.ReadAll(stream)
			if err != nil || string(got) != payload {
				t.Errorf("stream truncated: %d bytes, %v", len(got), err)
			}
			if err := <-peerDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSPDYPartialHeaderCancellation(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		t.Run(fmt.Sprint(proxy), func(t *testing.T) {
			client, peer := net.Pipe()
			defer client.Close()
			defer peer.Close()
			started, peerDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(peerDone)
				http.ReadRequest(bufio.NewReader(peer))
				io.WriteString(peer, "HTTP/1.1 200 OK\r\nX-Incomplete: value\r\n\r")
				close(started)
				io.Copy(io.Discard, peer)
			}()
			cfg := &rest.Config{Host: "http://api.fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
			if proxy {
				p, _ := url.Parse("http://proxy.fixture.invalid")
				cfg.Proxy = http.ProxyURL(p)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt, up, err := spdyTransport(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer up.close()
			done := make(chan error, 1)
			go func() {
				req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Host, nil)
				_, err := rt.RoundTrip(req)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("header not reached")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Error("canceled handshake succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("header read did not cancel")
			}
			select {
			case <-peerDone:
			case <-time.After(time.Second):
				t.Fatal("peer not closed")
			}
		})
	}
}
