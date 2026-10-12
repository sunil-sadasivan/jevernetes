package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
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
	stop        chan struct{}
	done        chan struct{}
	once        sync.Once
	closeSocket func()
}

func (t *tunnel) BaseURL() string { return t.url }
func (t *tunnel) shutdown() {
	t.once.Do(func() {
		// client-go v0.35.3 resets streams and moby/spdystream v0.5.0
		// writes GOAWAY synchronously before closing the connection. Interrupt
		// raw I/O first so a non-reading peer cannot block that cleanup.
		t.closeSocket()
		close(t.stop)
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
	dialer := spdy.NewDialer(upgrader, client, "POST", u)
	stop := make(chan struct{})
	ready := make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, errors.New("port-forward unavailable")
	}
	t := &tunnel{stop: stop, done: make(chan struct{}), closeSocket: upgrader.close}
	result := make(chan error, 1)
	go func() {
		defer close(t.done)
		defer upgrader.close()
		watched := make(chan struct{})
		stopWatch := context.AfterFunc(ctx, func() {
			t.shutdown()
			close(watched)
		})
		defer func() {
			if !stopWatch() {
				<-watched
			}
		}()
		result <- pf.ForwardPorts()
	}()
	select {
	case <-ctx.Done():
		t.Close()
		return nil, ctx.Err()
	case <-result:
		t.Close()
		return nil, errors.New("port-forward failed")
	case <-ready:
	}
	ports, err := pf.GetPorts()
	if err != nil || len(ports) != 1 {
		t.Close()
		return nil, errors.New("port-forward unavailable")
	}
	t.url = fmt.Sprintf("http://127.0.0.1:%d", ports[0].Local)
	return t, nil
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
