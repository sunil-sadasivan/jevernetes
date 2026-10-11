package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

func TestKubernetesWireSnapshotAndReconnectContracts(t *testing.T) {
	p := pod("one")
	p.TypeMeta = metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"}
	p.Status.ContainerStatuses[0].RestartCount = 1
	p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &v1.ContainerStateTerminated{ContainerID: "runtime://previous"}
	p.Status.InitContainerStatuses = []v1.ContainerStatus{{Name: "init", ContainerID: "runtime://init"}}
	p.Status.EphemeralContainerStatuses = []v1.ContainerStatus{{Name: "debug", ContainerID: "runtime://debug"}}
	var mu sync.Mutex
	gets, lists, logs, previous := 0, 0, 0, 0
	live := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("non-read operation or wrong auth")
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/namespaces/demo/pods":
			lists++
			if r.URL.Query().Get("limit") != "200" {
				t.Error("pagination bound missing")
			}
			result := v1.PodList{TypeMeta: metav1.TypeMeta{Kind: "PodList", APIVersion: "v1"}, Items: []v1.Pod{}}
			if r.URL.Query().Get("continue") == "" {
				result.Items = []v1.Pod{*p}
				result.Continue = "next"
			} else if r.URL.Query().Get("continue") != "next" {
				t.Error("wrong continuation")
			}
			_ = json.NewEncoder(w).Encode(result)
		case "/api/v1/namespaces/demo/pods/one":
			gets++
			_ = json.NewEncoder(w).Encode(p)
		case "/api/v1/namespaces/demo/pods/one/log":
			logs++
			q := r.URL.Query()
			w.Header().Set("Content-Type", "text/plain")
			if q.Get("timestamps") != "true" || q.Get("container") == "" {
				t.Error("missing log attribution")
			}
			if !live {
				if q.Get("sinceSeconds") != "3600" || q.Get("tailLines") != "500" {
					t.Error("snapshot bounds", q)
				}
				if q.Get("previous") == "true" {
					previous++
				}
				fmt.Fprintln(w, "2026-01-01T00:00:00Z ERROR synthetic snapshot")
			} else {
				if q.Get("follow") != "true" || q.Get("container") != "app" {
					t.Error("follow contract")
				}
				if logs == 1 {
					fmt.Fprint(w, "2026-01-01T00:00:00.125Z INFO older\n2026-01-01T00:00:00.875Z ERROR current\n")
				} else {
					if q.Get("sinceTime") != "2026-01-01T00:00:00Z" || q.Has("tailLines") || q.Has("sinceSeconds") {
						t.Error("reconnect query", q)
					}
					fmt.Fprint(w, "2026-01-01T00:00:00.125Z INFO older\n2026-01-01T00:00:00.875Z ERROR current\n2026-01-01T00:00:01Z INFO next\n")
				}
			}
		default:
			t.Error("unexpected API path", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := cfg()
	c, err := New(&rest.Config{Host: server.URL, BearerToken: "synthetic"}, config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []event.Event
	err = c.Snapshot(ctx, func(e event.Event) error { events = append(events, e); return nil }, func(event.Source, string) { t.Error("snapshot gap") })
	if err != nil || len(events) != 4 {
		t.Fatal(err, len(events))
	}
	mu.Lock()
	if lists != 2 || gets != 4 || logs != 4 || previous != 1 {
		t.Error("snapshot wire count", lists, gets, logs, previous)
	}
	live = true
	logs = 0
	gets = 0
	mu.Unlock()
	if events[1].Source["restart_count"] != float64(0) || events[1].Source["previous"] != true {
		t.Fatal("previous instance identity", events[1].Source)
	}
	c.Config.Follow = true
	events = nil
	finished := errors.New("fixture finished")
	err = c.followStream(ctx, streams(*p, false, true)[0], func(e event.Event) error {
		events = append(events, e)
		if len(events) == 3 {
			return finished
		}
		return nil
	}, func(event.Source, string) {})
	if !errors.Is(err, finished) || len(events) != 3 || events[2].Text != "INFO next" {
		t.Fatal(err, events)
	}
	mu.Lock()
	defer mu.Unlock()
	if logs != 2 || gets != 2 {
		t.Fatal("reconnect must revalidate", logs, gets)
	}
}
