package kube

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
	"k8s.io/apimachinery/pkg/util/httpstream"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/transport"
)

// One instance per Open, like client-go's stateful SPDY upgrader. The raw
// connection is cancellable before proxy/TLS negotiation or ReadResponse starts.
// client-go still owns HTTP/SPDY negotiation, auth wrappers and stream handling.
type cancellableSPDY struct {
	base   *streamspdy.SpdyRoundTripper
	socket *upgradeSocket
}

func (s *cancellableSPDY) WrappedRoundTripper() http.RoundTripper { return s.base }

type upgradeRequestKey struct{}

func newCancellableSPDY(tc *transport.Config, tlsConfig *tls.Config, targetHost string, proxier func(*http.Request) (*url.URL, error)) (*cancellableSPDY, error) {
	s := &cancellableSPDY{}
	dial := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if tc.DialHolder != nil {
		dial = tc.DialHolder.Dial
	}
	// DialURL supplies target TLS and HTTP/1.1 ALPN. A non-nil
	// config is essential: its nil fallback allows insecure TLS with custom dialers.
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	}
	// DialURL only infers SNI when verification is enabled. Match Go TLS
	// dialing for insecure targets too, without changing proxy TLS identity.
	targetTLS := tlsConfig.Clone()
	if targetTLS.ServerName == "" {
		targetTLS.ServerName = targetHost
	}
	base, err := streamspdy.NewRoundTripperWithConfig(streamspdy.RoundTripperConfig{
		PingPeriod: 5 * time.Second,
		UpgradeTransport: &http.Transport{TLSClientConfig: targetTLS, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			req := ctx.Value(upgradeRequestKey{}).(*http.Request)
			p, err := proxier(req)
			if err != nil {
				return nil, err
			}
			rawDial := func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := dial(ctx, network, address)
				if err != nil {
					return nil, err
				}
				s.socket = newUpgradeSocket(ctx, conn)
				return s.socket, nil
			}
			if p == nil {
				return rawDial(ctx, network, address)
			}
			switch p.Scheme {
			case "socks5", "socks5h":
				d, err := proxy.FromURL(p, contextProxyDialer{rawDial})
				if err != nil {
					return nil, err
				}
				return d.(proxy.ContextDialer).DialContext(ctx, network, address)
			case "", "http", "https":
				conn, err := rawDial(ctx, network, proxyAddress(p))
				if err != nil {
					return nil, err
				}
				if p.Scheme == "https" {
					cfg := tlsConfig.Clone()
					if cfg.ServerName == "" {
						cfg.ServerName = p.Hostname()
					}
					cfg.NextProtos = []string{"http/1.1"}
					secure := tls.Client(conn, cfg)
					if err := secure.HandshakeContext(ctx); err != nil {
						return nil, err
					}
					conn = secure
				}
				connect := &http.Request{Method: http.MethodConnect, URL: &url.URL{}, Host: address, Header: make(http.Header)}
				if p.User != nil {
					password, _ := p.User.Password()
					connect.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.User.Username()+":"+password)))
				}
				if err := connect.Write(conn); err != nil {
					return nil, err
				}
				reader := bufio.NewReader(conn)
				response, err := http.ReadResponse(reader, connect)
				if err != nil {
					return nil, err
				}
				if response.StatusCode < 200 || response.StatusCode >= 300 {
					return nil, errors.New("proxy CONNECT failed")
				}
				// Preserve any bytes read ahead, including TLS bytes after CONNECT.
				return &bufferedUpgradeConn{Conn: conn, reader: reader}, nil
			default:
				return nil, errors.New("unsupported proxy scheme")
			}
		}},
	})
	s.base = base
	return s, err
}

func proxyAddress(p *url.URL) string {
	if p.Port() != "" {
		return p.Host
	}
	port := "80"
	if p.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(p.Hostname(), port)
}

type contextProxyDialer struct {
	dial func(context.Context, string, string) (net.Conn, error)
}

func (d contextProxyDialer) Dial(network, address string) (net.Conn, error) {
	return d.dial(context.Background(), network, address)
}
func (d contextProxyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dial(ctx, network, address)
}

type bufferedUpgradeConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedUpgradeConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (s *cancellableSPDY) RoundTrip(req *http.Request) (*http.Response, error) {
	// Match the request seen by client-go's native proxier, including upgrade
	// headers. Keep it separate from the clone the SPDY transport mutates.
	proxyRequest := req.Clone(req.Context())
	proxyRequest.Header.Add(httpstream.HeaderConnection, httpstream.HeaderUpgrade)
	proxyRequest.Header.Add(httpstream.HeaderUpgrade, streamspdy.HeaderSpdy31)
	ctx := context.WithValue(req.Context(), upgradeRequestKey{}, proxyRequest)
	response, err := s.base.RoundTrip(req.Clone(ctx))
	if err != nil && s.socket != nil {
		s.socket.Close()
	}
	return response, err
}
func (s *cancellableSPDY) NewConnection(response *http.Response) (httpstream.Connection, error) {
	// client-go reads rejected bodies in full. Keep its Kubernetes Status
	// decoder, but force a fixed read error beyond the cap. Match its upgrade
	// predicate so successful SPDY sockets retain their existing ownership.
	if response.StatusCode != http.StatusSwitchingProtocols ||
		!strings.Contains(strings.ToLower(response.Header.Get(httpstream.HeaderConnection)), strings.ToLower(httpstream.HeaderUpgrade)) ||
		!strings.Contains(strings.ToLower(response.Header.Get(httpstream.HeaderUpgrade)), strings.ToLower(streamspdy.HeaderSpdy31)) {
		response.Body = &rejectedUpgradeBody{body: response.Body, socket: s.socket, remaining: maxUpgradeErrorBytes}
	}
	conn, err := s.base.NewConnection(response)
	if s.socket != nil {
		if err != nil {
			s.socket.Close()
		} else {
			// Negotiate closes the response body (and its request context) on success.
			// Once upgraded, portforward owns socket closure through the tunnel stop.
			s.socket.detach()
		}
	}
	return conn, err
}

const maxUpgradeErrorBytes = 64 << 10

// Read at most cap+1 bytes: the extra byte distinguishes an exact-cap body
// from truncation. Never hand a truncated Status or attacker-controlled prefix
// to client-go's error decoder as a complete response.
type rejectedUpgradeBody struct {
	body      io.ReadCloser
	socket    *upgradeSocket
	remaining int64
}

func (b *rejectedUpgradeBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, errors.New("upgrade error response too large")
	}
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return n, errors.New("upgrade error response too large")
	}
	return n, err
}

func (b *rejectedUpgradeBody) Close() error {
	// http.ReadResponse bodies may drain on Close. Close the transport first
	// so cleanup cannot consume the rest of an untrusted response or stall.
	if b.socket != nil {
		_ = b.socket.Close()
	}
	return b.body.Close()
}

// Cancellation closes the underlying socket, never this wrapper (which joins
// that callback). Close and detach join it even when cancellation races success.
type upgradeSocket struct {
	net.Conn
	stop func() bool
	done chan struct{}
	once sync.Once
}

func newUpgradeSocket(ctx context.Context, conn net.Conn) *upgradeSocket {
	c := &upgradeSocket{Conn: conn, done: make(chan struct{})}
	c.stop = context.AfterFunc(ctx, func() { conn.Close(); close(c.done) })
	return c
}
func (c *upgradeSocket) detach() {
	c.once.Do(func() {
		if !c.stop() {
			<-c.done
		}
	})
}
func (c *upgradeSocket) Close() error { err := c.Conn.Close(); c.detach(); return err }

// Called after ForwardPorts joins, including redirect/auth/validation failures
// where client-go never calls NewConnection.
func (s *cancellableSPDY) close() {
	if s.socket != nil {
		_ = s.socket.Close()
	}
}
