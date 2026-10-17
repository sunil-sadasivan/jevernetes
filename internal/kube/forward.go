package kube

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
	"k8s.io/client-go/transport/spdy"
)

func metaTime(t time.Time) *metav1.Time { v := metav1.NewTime(t); return &v }

type Tunnel interface {
	BaseURL() string
	Close() error
}
type Forwarder interface {
	Open(context.Context, string, string, int) (Tunnel, error)
}
type SPDYForwarder struct{ Config *rest.Config }
type tunnel struct {
	url         string
	stop, done  chan struct{}
	once        sync.Once
	closeSocket func()
	listener    net.Listener
	conn        httpstream.Connection
	mu          sync.Mutex
	locals      map[net.Conn]struct{}
	workers     sync.WaitGroup
}

func (t *tunnel) BaseURL() string { return t.url }
func (t *tunnel) shutdown() {
	t.once.Do(func() {
		t.closeSocket()
		close(t.stop)
		_ = t.listener.Close()
		t.mu.Lock()
		for local := range t.locals {
			_ = local.Close()
		}
		t.mu.Unlock()
	})
}
func (t *tunnel) Close() error { t.shutdown(); <-t.done; return nil }
func (f SPDYForwarder) Open(ctx context.Context, namespace, pod string, port int) (Tunnel, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid inspection port")
	}
	rt, upgrader, err := spdyTransport(ctx, f.Config)
	if err != nil {
		return nil, errors.New("port-forward unavailable")
	}
	u, _, err := rest.DefaultServerUrlFor(f.Config)
	if err != nil {
		return nil, errors.New("port-forward unavailable")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(pod) + "/portforward"
	u.RawQuery = ""
	u.Fragment = ""
	client := &http.Client{Transport: rt, Timeout: 20 * time.Second, CheckRedirect: rejectRedirect}
	conn, protocol, err := spdy.NewDialer(upgrader, client, "POST", u).Dial("portforward.k8s.io")
	if err != nil {
		upgrader.close()
		return nil, errors.New("port-forward failed")
	}
	if protocol != "portforward.k8s.io" {
		upgrader.close()
		conn.Close()
		return nil, errors.New("port-forward unavailable")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		upgrader.close()
		conn.Close()
		return nil, errors.New("port-forward unavailable")
	}
	t := &tunnel{url: "http://" + listener.Addr().String(), stop: make(chan struct{}), done: make(chan struct{}), closeSocket: upgrader.close, listener: listener, conn: conn, locals: make(map[net.Conn]struct{})}
	go t.run(ctx, port)
	if ctx.Err() != nil {
		t.Close()
		return nil, ctx.Err()
	}
	return t, nil
}

func (t *tunnel) run(ctx context.Context, port int) {
	defer close(t.done)
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for id := uint64(0); ; id++ {
			local, err := t.listener.Accept()
			if err != nil {
				return
			}
			t.mu.Lock()
			select {
			case <-t.stop:
				t.mu.Unlock()
				local.Close()
				return
			default:
			}
			// Bound simultaneous local forwarding workers as well as response bytes.
			if len(t.locals) >= 64 {
				t.mu.Unlock()
				local.Close()
				continue
			}
			t.locals[local] = struct{}{}
			t.workers.Add(1)
			t.mu.Unlock()
			go func() {
				defer t.workers.Done()
				defer func() { local.Close(); t.mu.Lock(); delete(t.locals, local); t.mu.Unlock() }()
				t.handleConnection(local, port, id)
			}()
		}
	}()
	select {
	case <-ctx.Done():
	case <-t.stop:
	case <-t.conn.CloseChan():
	case <-accepted:
	}
	t.shutdown()
	<-accepted // No worker can be added after this join.
	_ = t.conn.Close()
	t.workers.Wait()
}

func (t *tunnel) handleConnection(local net.Conn, port int, id uint64) {
	headers := http.Header{}
	headers.Set(v1.StreamType, v1.StreamTypeError)
	headers.Set(v1.PortHeader, strconv.Itoa(port))
	headers.Set(v1.PortForwardRequestIDHeader, strconv.FormatUint(id, 10))
	errorStream, err := t.conn.CreateStream(headers)
	if err != nil {
		return
	}
	defer t.conn.RemoveStreams(errorStream)
	_ = errorStream.Close()
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		// Any nonempty error stream means failure. Inspect only one byte, never
		// accumulate or print attacker-controlled error text, and do not require FIN.
		var b [1]byte
		n, err := errorStream.Read(b[:])
		if n > 0 || (err != nil && err != io.EOF) {
			t.shutdown()
		}
	}()
	defer func() { _ = errorStream.Reset(); readers.Wait() }()
	headers = headers.Clone()
	headers.Set(v1.StreamType, v1.StreamTypeData)
	data, err := t.conn.CreateStream(headers)
	if err != nil {
		return
	}
	defer t.conn.RemoveStreams(data)
	remoteDone, localDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(remoteDone); _, _ = io.Copy(local, data) }()
	localError := make(chan struct{})
	go func() {
		defer close(localDone)
		_, err := io.Copy(data, local)
		_ = data.Close()
		if err != nil {
			close(localError)
		}
	}()
	select {
	case <-remoteDone:
	case <-localError:
	case <-t.stop:
	}
	local.Close()
	_ = data.Reset()
	<-localDone
	<-remoteDone
	// Preserve a final error status that arrives after data EOF. Shutdown resets
	// the stream and joins this reader even if the peer never sends FIN.
	readers.Wait()
}

// Use the same bounded exec auth for upgrade requests. client-go's SPDY dialer
// creates requests without a context, so wrap the entire handshake lifecycle.
func spdyTransport(ctx context.Context, cfg *rest.Config) (http.RoundTripper, *cancellableSPDY, error) {
	tc, err := transportConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	tlsConfig, err := transport.TLSConfigFor(tc)
	if err != nil {
		return nil, nil, err
	}
	proxy := http.ProxyFromEnvironment
	if cfg.Proxy != nil {
		proxy = cfg.Proxy
	}
	endpoint, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return nil, nil, errors.New("Kubernetes endpoint unavailable")
	}
	upgrade, err := newCancellableSPDY(tc, tlsConfig, endpoint.Hostname(), proxy)
	if err != nil {
		return nil, nil, err
	}
	rt, err := transport.HTTPWrappersForConfig(tc, upgrade)
	if err != nil {
		return nil, nil, err
	}
	return lifecycleTransport{ctx, rt}, upgrade, nil
}
