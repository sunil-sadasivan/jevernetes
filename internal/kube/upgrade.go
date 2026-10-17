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
// The tunnel owns raw socket shutdown and the internal adapter joins streams.
type cancellableSPDY struct {
	base   *streamspdy.SpdyRoundTripper
	mu     sync.Mutex // protects socket publication against tunnel shutdown
	socket *upgradeSocket
	closed bool
	conn   net.Conn
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
				socket := newUpgradeSocket(ctx, conn)
				s.mu.Lock()
				s.socket = socket
				closed := s.closed
				s.mu.Unlock()
				// Cancellation may precede a custom dialer's return. Never
				// publish a live socket after its owner has already shut down.
				if closed {
					_ = socket.Close()
				}
				return socket, nil
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
				response, reader, err := readUpgradeResponse(conn, connect)
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

const maxUpgradeHeaderBytes = 64 << 10

var errUpgradeHeadersTooLarge = errors.New("upgrade response headers too large")

func readUpgradeResponse(r io.Reader, req *http.Request) (*http.Response, *bufio.Reader, error) {
	limited := &upgradeHeaderReader{reader: r}
	reader := bufio.NewReader(limited)
	response, err := http.ReadResponse(reader, req)
	// net/textproto can replace an underlying read error with a syntax error
	// containing the partial header. Keep over-limit failures fixed and small.
	if limited.read > maxUpgradeHeaderBytes {
		return nil, reader, errUpgradeHeadersTooLarge
	}
	return response, reader, err
}

// Bound plaintext before net/http can allocate status lines or MIME headers.
// Only the header section counts; read-ahead after its terminator is retained
// verbatim and all later reads are unlimited. Accept LF as well as CRLF, like
// net/http, so a valid LF-only response cannot leave a cap on the SPDY stream.
type upgradeHeaderReader struct {
	reader    io.Reader
	read      int
	lineBytes int
	last      byte
	done      bool
}

func (r *upgradeHeaderReader) Read(p []byte) (int, error) {
	if r.done {
		return r.reader.Read(p)
	}
	if r.read > maxUpgradeHeaderBytes {
		return 0, errUpgradeHeadersTooLarge
	}
	if limit := maxUpgradeHeaderBytes + 1 - r.read; len(p) > limit {
		p = p[:limit]
	}
	n, err := r.reader.Read(p)
	for _, b := range p[:n] {
		r.read++
		if r.read > maxUpgradeHeaderBytes {
			return 0, errUpgradeHeadersTooLarge
		}
		if b == '\n' {
			if r.lineBytes == 0 || (r.lineBytes == 1 && r.last == '\r') {
				r.done = true
				break
			}
			r.lineBytes = 0
		} else {
			r.lineBytes++
		}
		r.last = b
	}
	return n, err
}

func (s *cancellableSPDY) RoundTrip(req *http.Request) (*http.Response, error) {
	// Match the request seen by client-go's native proxier, including upgrade
	// headers. Keep it separate from the clone the SPDY transport mutates.
	proxyRequest := req.Clone(req.Context())
	proxyRequest.Header.Add(httpstream.HeaderConnection, httpstream.HeaderUpgrade)
	proxyRequest.Header.Add(httpstream.HeaderUpgrade, streamspdy.HeaderSpdy31)
	ctx := context.WithValue(req.Context(), upgradeRequestKey{}, proxyRequest)
	// v0.35.3's RoundTrip uses unbounded ReadResponse and discards its reader
	// on upgrade. Use its Dial (including request write and target TLS), then
	// retain our bounded reader for both rejected bodies and upgraded streams.
	conn, err := s.base.Dial(proxyRequest.Clone(ctx))
	if err != nil {
		s.close()
		return nil, err
	}
	response, reader, err := readUpgradeResponse(conn, nil)
	if err != nil {
		s.close()
		return nil, err
	}
	s.conn = &bufferedUpgradeConn{Conn: conn, reader: reader}
	return response, nil
}
func (s *cancellableSPDY) NewConnection(response *http.Response) (httpstream.Connection, error) {
	// client-go reads rejected bodies in full. Keep its Kubernetes Status
	// decoder, but force a fixed read error beyond the cap. Match its upgrade
	// predicate so successful SPDY sockets retain their existing ownership.
	var conn httpstream.Connection
	var err error
	if response.StatusCode != http.StatusSwitchingProtocols ||
		!strings.Contains(strings.ToLower(response.Header.Get(httpstream.HeaderConnection)), strings.ToLower(httpstream.HeaderUpgrade)) ||
		!strings.Contains(strings.ToLower(response.Header.Get(httpstream.HeaderUpgrade)), strings.ToLower(streamspdy.HeaderSpdy31)) {
		response.Body = &rejectedUpgradeBody{body: response.Body, socket: s.socket, remaining: maxUpgradeErrorBytes}
		conn, err = s.base.NewConnection(response)
	} else {
		// Keep the pinned wire engine and ping period with an owned lifecycle;
		// preserve bytes buffered after the HTTP header terminator.
		conn, err = newOwnedStreams(s.conn)
	}
	if s.socket != nil {
		if err != nil {
			s.socket.Close()
		} else {
			// Negotiate closes the response body (and its request context) on success.
			// Detach only HTTP cancellation. The tunnel retains raw socket
			// ownership and closes it before waiting for portforward cleanup.
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

// HTTP cancellation and tunnel/SPDY cleanup share exactly-once raw closure.
// The callback must not call Close, which joins that same callback. Detaching
// HTTP cancellation after upgrade does not relinquish tunnel ownership.
type upgradeSocket struct {
	net.Conn
	stop      func() bool
	done      chan struct{}
	once      sync.Once
	closeOnce sync.Once
	closeErr  error
}

func newUpgradeSocket(ctx context.Context, conn net.Conn) *upgradeSocket {
	c := &upgradeSocket{Conn: conn, done: make(chan struct{})}
	c.stop = context.AfterFunc(ctx, func() { c.closeRaw(); close(c.done) })
	return c
}
func (c *upgradeSocket) detach() {
	c.once.Do(func() {
		if !c.stop() {
			<-c.done
		}
	})
}
func (c *upgradeSocket) closeRaw() error {
	c.closeOnce.Do(func() { c.closeErr = c.Conn.Close() })
	return c.closeErr
}
func (c *upgradeSocket) Close() error { err := c.closeRaw(); c.detach(); return err }

// Called before signaling tunnel stop and on failed handshakes, including
// failures where client-go never calls NewConnection. Socket
// registration and shutdown can race while a custom dialer is returning.
func (s *cancellableSPDY) close() {
	s.mu.Lock()
	s.closed = true
	socket := s.socket
	s.mu.Unlock()
	if socket != nil {
		_ = socket.Close()
	}
}
