package kube

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

func pod(name string) *v1.Pod {
	return &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, UID: "fixture-uid", Namespace: "demo", Labels: map[string]string{"app": "jevernetes"}}, Status: v1.PodStatus{Phase: v1.PodRunning, ContainerStatuses: []v1.ContainerStatus{{Name: "app", ContainerID: "runtime://fixture", State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}}}}}
}
func cfg() Config {
	return Config{Namespace: "demo", MaxBytes: 1024, MaxStreams: 2, Tail: 500, Since: 3600}
}
func TestExactSelectionAndReadOnlyActions(t *testing.T) {
	client := fake.NewClientset(pod("one"))
	c := cfg()
	p, err := SelectPod(context.Background(), client.CoreV1(), c)
	if err != nil || p.Name != "one" {
		t.Fatal(p, err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "list" || a.GetResource().Resource != "pods" {
			t.Fatal("non-read action", a)
		}
	}
	c.Pod = "one"
	p, err = SelectPod(context.Background(), client.CoreV1(), c)
	if err != nil || p.Name != "one" {
		t.Fatal(err)
	}
}
func TestSelectionRejectsAmbiguousTerminatingAndPaginated(t *testing.T) {
	for _, objects := range [][]runtime.Object{{}, {pod("one"), pod("two")}} {
		client := fake.NewClientset(objects...)
		if _, err := SelectPod(context.Background(), client.CoreV1(), cfg()); err == nil {
			t.Fatal("ambiguous selection")
		}
	}
	p := pod("gone")
	now := metav1.Now()
	p.DeletionTimestamp = &now
	client := fake.NewClientset(p)
	if _, err := SelectPod(context.Background(), client.CoreV1(), cfg()); err == nil {
		t.Fatal("terminating pod")
	}
	client = fake.NewClientset()
	client.PrependReactor("list", "pods", func(kt.Action) (bool, runtime.Object, error) {
		return true, &v1.PodList{ListMeta: metav1.ListMeta{Continue: "next"}, Items: []v1.Pod{*pod("one")}}, nil
	})
	if _, err := SelectPod(context.Background(), client.CoreV1(), cfg()); err == nil {
		t.Fatal("paginated selection")
	}
}
func TestSnapshotIncludesKindsPreviousAndCaps(t *testing.T) {
	p := pod("one")
	p.Status.ContainerStatuses[0].RestartCount = 1
	p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &v1.ContainerStateTerminated{ContainerID: "runtime://previous"}
	p.Status.InitContainerStatuses = []v1.ContainerStatus{{Name: "init", ContainerID: "runtime://init"}}
	p.Status.EphemeralContainerStatuses = []v1.ContainerStatus{{Name: "debug", ContainerID: "runtime://debug"}}
	client := fake.NewClientset(p)
	seen := []*v1.PodLogOptions{}
	c := Collector{API: client.CoreV1(), Config: cfg(), OpenLog: func(_ context.Context, ns, pod string, o *v1.PodLogOptions) (io.ReadCloser, error) {
		seen = append(seen, o)
		return io.NopCloser(strings.NewReader("INFO synthetic\n")), nil
	}}
	var events []event.Event
	err := c.Snapshot(context.Background(), func(e event.Event) error { events = append(events, e); return nil }, func(event.Source, string) { t.Fatal("unexpected gap") })
	if err != nil || len(seen) != 4 || len(events) != 4 {
		t.Fatal(len(seen), len(events), err)
	}
	if !seen[1].Previous || !seen[0].Timestamps {
		t.Fatal("instance contracts")
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "list" && a.GetVerb() != "get" {
			t.Fatal("mutation")
		}
	}
}
func TestLiveIdleFlushAndCancellationJoins(t *testing.T) {
	r, w := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	p := event.NewParser(event.Source{})
	events := make(chan event.Event, 1)
	done := make(chan error, 1)
	go func() { done <- readLive(ctx, r, p, func(e event.Event) error { events <- e; return nil }) }()
	_, _ = w.Write([]byte("INFO singleton\n"))
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("idle flush missing")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader leaked")
	}
	w.Close()
}
func TestCollectionConfigValidation(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.Namespace = "../demo" }, func(c *Config) { c.Pod = "a"; c.Selector = "x=y" }, func(c *Config) { c.InCluster = true; c.Context = "demo" }, func(c *Config) { c.MaxStreams = 0 }} {
		c := cfg()
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
