package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func TestFreshIdentityRejectsReplacementRestartAndContainerChange(t *testing.T) {
	for _, change := range []string{"uid", "restart", "container", "removed", "terminating", "missing_id"} {
		t.Run(change, func(t *testing.T) {
			p := pod("one")
			p.UID = "original"
			p.Status.ContainerStatuses[0].ContainerID = "runtime://original"
			fresh := p.DeepCopy()
			switch change {
			case "uid":
				fresh.UID = "replacement"
			case "restart":
				fresh.Status.ContainerStatuses[0].RestartCount++
			case "container":
				fresh.Status.ContainerStatuses[0].ContainerID = "runtime://replacement"
			case "missing_id":
				p.Status.ContainerStatuses[0].ContainerID = ""
				fresh.Status.ContainerStatuses[0].ContainerID = ""
			case "removed":
				fresh.Status.ContainerStatuses = nil
			case "terminating":
				fresh.DeletionTimestamp = &fresh.CreationTimestamp
			}
			client := fake.NewClientset(p)
			client.PrependReactor("get", "pods", func(kt.Action) (bool, runtime.Object, error) { return true, fresh, nil })
			opened, gaps := 0, 0
			c := Collector{API: client.CoreV1(), Config: cfg(), OpenLog: func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error) {
				opened++
				return io.NopCloser(strings.NewReader("x")), nil
			}}
			gap := func(event.Source, string) { gaps++ }
			if err := c.Snapshot(context.Background(), func(event.Event) error { return nil }, gap); err != nil {
				t.Fatal(err)
			}
			if err := c.followStream(context.Background(), streams(*p, false, true)[0], func(event.Event) error { return nil }, gap); err != nil {
				t.Fatal(err)
			}
			if opened != 0 || gaps != 2 {
				t.Fatal(opened, gaps)
			}
		})
	}
}
func TestPreviousRestartAttribution(t *testing.T) {
	p := pod("one")
	p.Status.ContainerStatuses[0].RestartCount = 4
	p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &v1.ContainerStateTerminated{ContainerID: "runtime://previous"}
	ss := streams(*p, true, false)
	if len(ss) != 2 || ss[0].restart != 4 || ss[1].restart != 3 || ss[1].containerID != "runtime://previous" {
		t.Fatal(ss)
	}
}
func TestRawReplayMultiplicityAndLaterOccurrences(t *testing.T) {
	gaps := map[string]int{}
	c := newCursor(func(s string) { gaps[s]++ })
	p := event.NewParser(event.Source{"type": "kubernetes"})
	p.AcceptLine = c.accept
	read := func(raw string) []event.Event { out := p.Feed([]byte(raw)); return append(out, p.Finish(false)...) }
	a := "2026-01-01T00:00:00Z password=fixture-one\n"
	b := "2026-01-01T00:00:00Z password=fixture-two\n"
	if out := read(a + a + b); len(out) != 3 || out[0].Text != out[2].Text {
		t.Fatal("redaction fixture")
	}
	c.reconnect()
	if out := read(a + a + b + a); len(out) != 1 {
		t.Fatal("same timestamp multiplicity", len(out))
	}
	c.reconnect()
	if out := read(a + a + b + a + "2026-01-01T00:00:01Z password=fixture-one\n"); len(out) != 1 {
		t.Fatal("later occurrence lost", len(out))
	}
	if len(gaps) != 0 {
		t.Fatal(gaps)
	}
}
func TestCursorBoundsUncertaintyAndTruncatedReplay(t *testing.T) {
	gaps := map[string]int{}
	c := newCursor(func(s string) { gaps[s]++ })
	stamp := time.Now()
	for i := 0; i < cursorCapacity+1; i++ {
		if !c.accept(&stamp, fmt.Sprint(i), false) {
			t.Fatal("new line suppressed")
		}
	}
	if len(c.counts) != cursorCapacity || gaps["cursor_overflow"] != 1 {
		t.Fatal("unbounded cursor")
	}
	c.accept(nil, "", false)
	c.accept(&stamp, "fragment", true)
	if gaps["cursor_untimestamped"] != 1 || gaps["cursor_truncated"] != 1 {
		t.Fatal(gaps)
	}
	p := event.NewParser(event.Source{"type": "kubernetes"})
	p.AcceptLine = c.accept
	raw := "2026-01-01T00:00:00Z " + strings.Repeat("x", event.MaxLine) + "\n"

	c = newCursor(func(s string) { gaps[s]++ })
	p.AcceptLine = c.accept
	for i := 0; i < 2; i++ {
		c.reconnect()
		out := p.Feed([]byte(raw))
		out = append(out, p.Finish(false)...)
		if i == 0 && (len(out) != 1 || !out[0].Truncated) {
			t.Fatal("oversize lost")
		}
		if i == 1 && len(out) != 0 {
			t.Fatal("oversize replay not suppressed")
		}
	}
	// Different discarded suffixes must remain distinct despite identical retained text.
	out := p.Feed([]byte(strings.TrimSuffix(raw, "\n") + "different suffix\n"))
	out = append(out, p.Finish(false)...)
	if len(out) != 1 || !out[0].Truncated {
		t.Fatal("discarded raw suffix not hashed")
	}
	c.reconnect()
	out = p.Feed([]byte(strings.TrimSuffix(raw, "\n")))
	out = append(out, p.Finish(true)...)
	if len(out) != 1 || gaps["cursor_truncated"] < 2 {
		t.Fatal("partial line uncertainty missing")
	}

}
func TestLiveEmitFailureIsFatal(t *testing.T) {
	p := pod("one")
	client := fake.NewClientset(p)
	want := errors.New("consumer stopped")
	c := Collector{API: client.CoreV1(), Config: cfg(), OpenLog: func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("2026-01-01T00:00:00Z INFO x\n")), nil
	}}
	if err := c.followStream(context.Background(), streams(*p, false, true)[0], func(event.Event) error { return want }, func(event.Source, string) {}); !errors.Is(err, want) {
		t.Fatal(err)
	}
}
func TestEOFReconnectValidatesAgain(t *testing.T) {
	p := pod("one")
	client := fake.NewClientset(p)
	gets, opens, gaps := 0, 0, 0
	client.PrependReactor("get", "pods", func(kt.Action) (bool, runtime.Object, error) {
		gets++
		fresh := p.DeepCopy()
		if gets > 1 {
			fresh.Status.ContainerStatuses[0].RestartCount++
		}
		return true, fresh, nil
	})
	c := Collector{API: client.CoreV1(), Config: cfg(), OpenLog: func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error) {
		opens++
		return io.NopCloser(strings.NewReader("")), nil
	}}
	if err := c.followStream(context.Background(), streams(*p, false, true)[0], func(event.Event) error { return nil }, func(_ event.Source, s string) {
		if s == "stream_reconnect" {
			gaps++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if gets != 2 || opens != 1 || gaps != 1 {
		t.Fatal(gets, opens, gaps)
	}
}

func TestSnapshotPropagatesConsumerFailure(t *testing.T) {
	p := pod("one")
	client := fake.NewClientset(p)
	want := errors.New("consumer stopped")
	c := Collector{API: client.CoreV1(), Config: cfg(), OpenLog: func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("2026-01-01T00:00:00Z INFO x\n")), nil
	}}
	if err := c.Snapshot(context.Background(), func(event.Event) error { return want }, func(event.Source, string) {}); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestFollowReconnectUsesRawCursorAndSinceTime(t *testing.T) {
	p := pod("one")
	client := fake.NewClientset(p)
	opens := 0
	gaps := 0
	received := 0
	a := "2026-01-01T00:00:00Z password=fixture-one\n"
	b := "2026-01-01T00:00:00Z password=fixture-two\n"
	done := errors.New("test received expected occurrences")
	c := Collector{API: client.CoreV1(), Config: cfg(), OpenLog: func(_ context.Context, _, _ string, o *v1.PodLogOptions) (io.ReadCloser, error) {
		opens++
		if opens == 1 {
			return io.NopCloser(strings.NewReader(a + a + b)), nil
		}
		if opens != 2 || o.SinceTime == nil || o.SinceTime.UTC().Format(time.RFC3339) != "2026-01-01T00:00:00Z" || o.TailLines != nil || o.SinceSeconds != nil {
			t.Fatal("invalid reconnect request")
		}
		return io.NopCloser(strings.NewReader(a + a + b + a + "2026-01-01T00:00:01Z password=fixture-one\n")), nil
	}}
	err := c.followStream(context.Background(), streams(*p, false, true)[0], func(event.Event) error {
		received++
		if received == 5 {
			return done
		}
		return nil
	}, func(_ event.Source, reason string) {
		if reason == "stream_reconnect" {
			gaps++
		}
	})
	if !errors.Is(err, done) || received != 5 || gaps != 1 || opens != 2 {
		t.Fatal(err, received, gaps, opens)
	}
}
