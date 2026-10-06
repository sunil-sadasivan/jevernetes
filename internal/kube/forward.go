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
	url  string
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func (t *tunnel) BaseURL() string { return t.url }
func (t *tunnel) Close() error    { t.once.Do(func() { close(t.stop) }); <-t.done; return nil }
func (f SPDYForwarder) Open(ctx context.Context, namespace, pod string, port int) (Tunnel, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid inspection port")
	}
	transport, upgrader, err := spdy.RoundTripperFor(f.Config)
	if err != nil {
		return nil, errors.New("port-forward unavailable")
	}
	u, err := url.Parse(f.Config.Host)
	if err != nil {
		return nil, errors.New("port-forward unavailable")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(pod) + "/portforward"
	u.RawQuery = ""
	u.Fragment = ""
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	dialer := spdy.NewDialer(upgrader, client, "POST", u)
	stop := make(chan struct{})
	ready := make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, errors.New("port-forward unavailable")
	}
	t := &tunnel{stop: stop, done: make(chan struct{})}
	result := make(chan error, 1)
	go func() { defer close(t.done); result <- pf.ForwardPorts() }()
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
	go func() {
		select {
		case <-ctx.Done():
			t.Close()
		case <-t.done:
		}
	}()
	return t, nil
}
