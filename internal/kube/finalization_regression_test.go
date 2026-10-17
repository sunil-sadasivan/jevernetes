package kube

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Cancellation occurs inside the first Read: the bytes have been read, but the
// parser has not seen them yet. There must be no subsequent fresh Read.
type cancellingLog struct {
	cancel context.CancelFunc
	reads  int
	closed bool
	mu     sync.Mutex
}

func (r *cancellingLog) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.reads != 1 {
		return 0, errors.New("fresh read after cancellation")
	}
	n := copy(p, "2026-01-01T00:00:00Z ERROR quiet failure\n")
	r.cancel()
	return n, nil
}
func (r *cancellingLog) Close() error { r.mu.Lock(); defer r.mu.Unlock(); r.closed = true; return nil }

func TestLiveCancellationFinalizesAlreadyReadChunkAndJoins(t *testing.T) {
	p := pod("one")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &cancellingLog{cancel: cancel}
	c := &Collector{API: fake.NewClientset(p).CoreV1(), Config: cfg(), OpenLog: func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error) { return r, nil }}
	var events []event.Event
	err := c.followStreamWithFinalizer(ctx, streams(*p, false, true)[0], func(event.Event) error { return ctx.Err() }, func(e event.Event) error { events = append(events, e); return nil }, func(event.Source, string) {})
	if !errors.Is(err, context.Canceled) || len(events) != 1 {
		t.Fatalf("lost read chunk on cancellation: %v %+v", err, events)
	}
	if !events[0].Truncated || !events[0].ParseUncertain || events[0].Text != "ERROR quiet failure" {
		t.Fatalf("unsafe final evidence: %+v", events[0])
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reads != 1 || !r.closed {
		t.Fatalf("reader not stopped/joined: reads=%d closed=%v", r.reads, r.closed)
	}
}
