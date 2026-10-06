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
	"k8s.io/client-go/kubernetes/scheme"
	kt "k8s.io/client-go/testing"
)

func TestValidationUnavailablePreservesCursor(t *testing.T) {
	for _, failure := range []string{"get", "runtime", "uid"} {
		t.Run(failure, func(t *testing.T) {
			p := pod("one")
			client := fake.NewClientset(p)
			gets, opens, received := 0, 0, 0
			gaps := map[string]int{}
			client.PrependReactor("get", "pods", func(kt.Action) (bool, runtime.Object, error) {
				gets++
				if gets == 2 {
					switch failure {
					case "get":
						return true, nil, errors.New("temporary unavailable")
					case "runtime":
						fresh := p.DeepCopy()
						fresh.Status.ContainerStatuses[0].ContainerID = ""
						return true, fresh, nil
					default:
						fresh := p.DeepCopy()
						fresh.UID = ""
						return true, fresh, nil
					}
				}
				return true, p.DeepCopy(), nil
			})
			done := errors.New("complete")
			line := "2026-01-01T00:00:00.125Z INFO first\n"
			c := Collector{API: client.CoreV1(), Config: cfg(), OpenLog: func(_ context.Context, _, _ string, o *v1.PodLogOptions) (io.ReadCloser, error) {
				opens++
				if opens == 1 {
					return io.NopCloser(strings.NewReader(line)), nil
				}
				if gets != 3 || o.SinceTime == nil || o.SinceTime.Nanosecond() != 125000000 {
					t.Fatal("validation/cursor lost", gets, o)
				}
				return io.NopCloser(strings.NewReader(line + "2026-01-01T00:00:01Z INFO second\n")), nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			err := c.followStream(ctx, streams(*p, false, true)[0], func(e event.Event) error {
				received++
				if received == 2 {
					if e.Text != "INFO second" || e.LineStart != 3 {
						t.Fatal("replay/parser lost", e)
					}
					return done
				}
				return nil
			}, func(_ event.Source, reason string) { gaps[reason]++ })
			if !errors.Is(err, done) || received != 2 || opens != 2 || gaps["identity_changed"] != 0 || gaps["identity_unavailable"] != 1 {
				t.Fatal(err, received, opens, gaps)
			}
		})
	}
}

func TestFractionalWireCursorRepeatedReconnect(t *testing.T) {
	p := pod("one")
	client := fake.NewClientset(p)
	opens, received := 0, 0
	gaps := map[string]int{}
	a := "2026-01-01T00:00:00.125Z INFO older\n"
	b := "2026-01-01T00:00:00.875Z INFO latest\n"
	done := errors.New("complete")
	c := Collector{API: client.CoreV1(), Config: cfg(), OpenLog: func(_ context.Context, _, _ string, o *v1.PodLogOptions) (io.ReadCloser, error) {
		opens++
		if opens == 1 {
			return io.NopCloser(strings.NewReader(a + b + b)), nil
		}
		query, err := scheme.ParameterCodec.EncodeParameters(o, v1.SchemeGroupVersion)
		if err != nil || query.Get("sinceTime") != "2026-01-01T00:00:00Z" {
			t.Fatal("wire fixture", query, err)
		}
		if opens == 2 {
			return io.NopCloser(strings.NewReader(a + b + b + b)), nil
		}
		if opens == 3 {
			return io.NopCloser(strings.NewReader(a + b + b + b + "2026-01-01T00:00:01Z INFO caught up\n" + a)), nil
		}
		t.Fatal("unexpected reconnect")
		return nil, io.EOF
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	err := c.followStream(ctx, streams(*p, false, true)[0], func(e event.Event) error {
		received++
		if received == 6 {
			if e.Text != "INFO older" {
				t.Fatal("lost out of order occurrence", e.Text)
			}
			return done
		}
		return nil
	}, func(_ event.Source, reason string) { gaps[reason]++ })
	if !errors.Is(err, done) || received != 6 || opens != 3 || gaps["cursor_out_of_order"] != 1 {
		t.Fatal(err, received, opens, gaps)
	}
}

func TestConsumerEOFDoesNotReconnect(t *testing.T) {
	for _, want := range []error{io.EOF, fmt.Errorf("consumer: %w", io.EOF)} {
		t.Run(want.Error(), func(t *testing.T) {
			p := pod("one")
			opens := 0
			c := Collector{API: fake.NewClientset(p).CoreV1(), Config: cfg(), OpenLog: func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error) {
				opens++
				return io.NopCloser(strings.NewReader("2026-01-01T00:00:00Z INFO x\n")), nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			gaps := 0
			err := c.followStream(ctx, streams(*p, false, true)[0], func(event.Event) error { return want }, func(event.Source, string) { gaps++ })
			if err != want || opens != 1 || gaps != 0 {
				t.Fatal(err, opens, gaps)
			}
		})
	}
}

func TestReplayCatchupEndsAtSavedMultiplicity(t *testing.T) {
	gaps := 0
	c := newCursor(func(string) { gaps++ })
	last := time.Date(2026, 1, 1, 0, 0, 0, 875000000, time.UTC)
	older := last.Add(-500 * time.Millisecond)
	c.accept(&older, "older", false)
	c.accept(&last, "latest", false)
	c.reconnect()
	if c.accept(&older, "older", false) || c.accept(&last, "latest", false) {
		t.Fatal("replayed lines accepted")
	}
	if !c.accept(&older, "new late occurrence", false) || gaps != 1 {
		t.Fatal("out of order after catchup lost", gaps)
	}
}
