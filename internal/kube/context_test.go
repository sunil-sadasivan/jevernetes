package kube

import (
	"context"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"io"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestContextVerifiesPreviousAndReplacement(t *testing.T) {
	p := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "test", UID: types.UID("uid")}, Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{Name: "app", RestartCount: 2, ContainerID: "new", LastTerminationState: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ContainerID: "old"}}}}}}
	api := fake.NewClientset(p)
	stamp := "2026-01-01T12:00:00Z"
	e := event.Event{ID: "a", Timestamp: &stamp, Source: event.Source{"type": "kubernetes", "namespace": "test", "pod": "demo", "container": "app", "pod_uid": "uid", "restart_count": 1, "container_id": "old"}}
	c := &Collector{API: api.CoreV1(), OpenLog: func(ctx context.Context, ns, pod string, o *v1.PodLogOptions) (io.ReadCloser, error) {
		if !o.Previous || o.LimitBytes == nil || *o.LimitBytes != 1<<20 || o.SinceTime == nil {
			t.Fatal("unbounded or wrong instance")
		}
		return io.NopCloser(strings.NewReader("2026-01-01T12:00:00Z INFO surrounding event\n")), nil
	}}
	v, err := c.Context(context.Background(), e, 120)
	if err != nil || len(v["events"].([]event.Event)) != 1 {
		t.Fatal(v, err)
	}
	original := c.OpenLog
	c.OpenLog = func(ctx context.Context, ns, pod string, o *v1.PodLogOptions) (io.ReadCloser, error) {
		p.UID = "replacement"
		_, _ = api.CoreV1().Pods(ns).Update(ctx, p, metav1.UpdateOptions{})
		return original(ctx, ns, pod, o)
	}
	if _, err = c.Context(context.Background(), e, 120); err == nil {
		t.Fatal("replacement accepted")
	}
}
func TestSnapshotWorkerBoundAndNoLostOccurrences(t *testing.T) {
	api := fake.NewClientset(pod("one"), pod("two"), pod("three"))
	config := cfg()
	config.SnapshotWorkers = 2
	var mu sync.Mutex
	active, peak, count := 0, 0, 0
	c := &Collector{API: api.CoreV1(), Config: config, OpenLog: func(ctx context.Context, ns, name string, o *v1.PodLogOptions) (io.ReadCloser, error) {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return io.NopCloser(strings.NewReader("2026-01-01T12:00:00Z INFO synthetic\n")), nil
	}}
	err := c.Snapshot(context.Background(), func(e event.Event) error { mu.Lock(); count++; mu.Unlock(); return nil }, func(event.Source, string) { t.Error("unexpected gap") })
	if err != nil || count != 3 || peak != 2 {
		t.Fatal(err, count, peak)
	}
}
