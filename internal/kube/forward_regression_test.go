package kube_test

import (
	"context"
	"github.com/sunil-sadasivan/jevernetes/internal/dashboard"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"io"
	"k8s.io/client-go/rest"
	"net"
	"sync"
)

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
