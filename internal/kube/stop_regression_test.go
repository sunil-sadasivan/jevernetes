package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

// A fast, allocation-free source makes client-go's unbounded ReadAll visible.
type largeUpgradeBody struct {
	remaining, read int64
	closed          bool
}

func (b *largeUpgradeBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.remaining)
	for i := range p[:n] {
		p[i] = 'x'
	}
	b.remaining -= n
	b.read += n
	return int(n), nil
}
func (b *largeUpgradeBody) Close() error { b.closed = true; return nil }

func TestSPDYRejectedBodyAllocationBound(t *testing.T) {
	base, err := streamspdy.NewRoundTripper(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &cancellableSPDY{base: base}
	body := &largeUpgradeBody{remaining: 32 << 20}
	response := &http.Response{StatusCode: 403, Header: make(http.Header), Body: body}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	conn, err := s.NewConnection(response)
	runtime.ReadMemStats(&after)
	if conn != nil || err == nil {
		t.Fatal("rejected upgrade succeeded")
	}
	if body.read > (64<<10)+1 {
		t.Errorf("consumed %d bytes from rejected body", body.read)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Errorf("allocated %d bytes for rejected body", allocated)
	}
	if !body.closed {
		t.Error("rejected body not closed")
	}
	if len(err.Error()) > 128 || strings.Contains(err.Error(), "xxx") {
		t.Error("oversized response leaked into error")
	}
}

func TestSPDYRejectedStatusWithinCap(t *testing.T) {
	status := `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","message":"synthetic denial","code":403}`
	for _, size := range []int{len(status), 64 << 10, (64 << 10) + 1} {
		for _, code := range []int{http.StatusForbidden, http.StatusSwitchingProtocols} {
			t.Run(fmt.Sprintf("size=%d/code=%d", size, code), func(t *testing.T) {
				base, err := streamspdy.NewRoundTripper(nil)
				if err != nil {
					t.Fatal(err)
				}
				s := &cancellableSPDY{base: base}
				// A 101 without upgrade headers is also a rejected handshake.
				response := &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(status + strings.Repeat(" ", size-len(status))))}
				_, err = s.NewConnection(response)
				if size <= 64<<10 {
					if !apierrors.IsForbidden(err) {
						t.Fatalf("lost Kubernetes Status decoding: %v", err)
					}
				} else if err == nil || apierrors.IsForbidden(err) || strings.Contains(err.Error(), "synthetic denial") {
					t.Fatalf("oversized Status was decoded or exposed: %v", err)
				}
			})
		}
	}
}

func TestSPDYLargeFast403ClosesSocket(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	sent := make(chan int64, 1)
	go func() {
		defer b.Close()
		_, _ = http.ReadRequest(bufio.NewReader(b))
		_, _ = io.WriteString(b, "HTTP/1.1 403 Forbidden\r\nContent-Length: 33554432\r\n\r\n")
		body := &largeUpgradeBody{remaining: 32 << 20}
		n, _ := io.Copy(b, body)
		sent <- n
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := &rest.Config{Host: "http://fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return a, nil }}
	rt, s, err := spdyTransport(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Host, nil)
	response, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.NewConnection(response)
	if err == nil || ctx.Err() != nil {
		t.Fatalf("upgrade not rejected promptly: %v", err)
	}
	select {
	case n := <-sent:
		if n > (64<<10)+4096 {
			t.Errorf("fast 403 consumed %d bytes", n)
		}
	case <-ctx.Done():
		t.Fatal("rejected body/socket cleanup did not join")
	}
}

func TestFollowRepeatedIdentityRetirementReleasesChildren(t *testing.T) {
	p := pod("one")
	client := fake.NewClientset(p)
	var gets atomic.Int32
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, kruntime.Object, error) {
		copy := p.DeepCopy()
		// Open once per discovery, then retire normally on reconnect validation.
		if gets.Add(1)%2 == 0 {
			copy.UID = "replacement"
		}
		return true, copy, nil
	})
	opened := make(chan context.Context, 1)
	retired := make(chan struct{}, 1)
	c := &Collector{Config: cfg(), API: client.CoreV1(), OpenLog: func(ctx context.Context, _ string, _ string, _ *v1.PodLogOptions) (io.ReadCloser, error) {
		opened <- ctx
		return io.NopCloser(strings.NewReader("")), nil
	}}
	c.Config.MaxStreams = 1
	ticks := make(chan time.Time)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.follow(ctx, func(event.Event) error { return nil }, func(event.Event) error { return nil }, func(_ event.Source, reason string) {
			if reason == "identity_changed" {
				retired <- struct{}{}
			}
		}, ticks)
	}()
	defer func() { cancel(); <-done }()
	for cycle := 0; cycle < 3; cycle++ {
		var child context.Context
		select {
		case child = <-opened:
		case <-ctx.Done():
			t.Fatal("worker not opened", cycle)
		}
		select {
		case <-retired:
		case <-ctx.Done():
			t.Fatal("identity not retired", cycle)
		}
		select {
		case <-child.Done():
		case <-time.After(200 * time.Millisecond):
			t.Fatal("normally retired worker retained uncanceled child", cycle)
		}
		if ctx.Err() != nil {
			t.Fatal("parent stopped during retirement")
		}
		if cycle < 2 {
			// Discovery may precede receipt of the completion notification. Retry
			// ticks until the next worker opens, with one bounded resident slot.
			for gets.Load() < int32((cycle+1)*2+1) {
				select {
				case ticks <- time.Now():
				case <-ctx.Done():
					t.Fatal("discovery stalled", cycle)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
}
