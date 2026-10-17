package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sort"
	"time"
)

// Context fetches bounded surrounding evidence and verifies instance identity
// both before and after the read. The result is deliberately unclassified.
func (c *Collector) Context(ctx context.Context, e event.Event, seconds int) (map[string]any, error) {
	if seconds != 30 && seconds != 120 && seconds != 300 && seconds != 900 {
		return nil, errors.New("invalid context window")
	}
	text := func(k string) string { v, _ := e.Source[k].(string); return v }
	if text("type") != "kubernetes" || e.Timestamp == nil {
		return nil, errors.New("context requires a timestamped Kubernetes event")
	}
	stamp, err := time.Parse(time.RFC3339Nano, *e.Timestamp)
	if err != nil {
		return nil, errors.New("invalid event timestamp")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	ns, pod, name := text("namespace"), text("pod"), text("container")
	if len(validation.IsDNS1123Label(ns)) > 0 || len(validation.IsDNS1123Subdomain(pod)) > 0 || len(validation.IsDNS1123Subdomain(name)) > 0 {
		return nil, errors.New("invalid recorded source")
	}
	identity := func() (string, int32, string, string, error) {
		p, err := c.API.Pods(ns).Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			return "", 0, "", "", errors.New("recorded container unavailable")
		}
		for _, statuses := range [][]v1.ContainerStatus{p.Status.ContainerStatuses, p.Status.InitContainerStatuses, p.Status.EphemeralContainerStatuses} {
			for _, s := range statuses {
				if s.Name == name {
					return string(p.UID), s.RestartCount, s.ContainerID, previousID(s), nil
				}
			}
		}
		return "", 0, "", "", errors.New("recorded container unavailable")
	}
	uid, restarts, current, old, err := identity()
	if err != nil {
		return nil, err
	}
	if text("pod_uid") != "" && text("pod_uid") != uid {
		return nil, errors.New("recorded pod was replaced")
	}
	warnings := []string{}
	previous, _ := e.Source["previous"].(bool)
	if raw, ok := e.Source["restart_count"]; ok {
		b, _ := json.Marshal(raw)
		var count int32
		if json.Unmarshal(b, &count) != nil {
			return nil, errors.New("invalid recorded restart count")
		}
		if count == restarts {
			previous = false
		} else if count == restarts-1 {
			previous = true
		} else {
			return nil, errors.New("recorded container instance is no longer retained")
		}
	} else {
		warnings = append(warnings, "Older report has no restart identity; instance cannot be verified.")
	}
	if text("pod_uid") == "" {
		warnings = append(warnings, "Older report has no pod UID; replacement cannot be distinguished.")
	}
	wantID := current
	if previous {
		wantID = old
	}
	if text("container_id") != "" && text("container_id") != wantID {
		return nil, errors.New("recorded container identity changed")
	}
	if text("cluster_id") != "" && c.clusterID != "" && text("cluster_id") != c.clusterID {
		return nil, errors.New("recorded cluster identity changed")
	}
	start, end := stamp.Add(-time.Duration(seconds)*time.Second), stamp.Add(time.Duration(seconds)*time.Second)
	since := metav1.NewTime(start)
	limit, tail := int64(1<<20), int64(-1)
	r, err := c.OpenLog(ctx, ns, pod, &v1.PodLogOptions{Container: name, Previous: previous, Timestamps: true, SinceTime: &since, LimitBytes: &limit, TailLines: &tail})
	if err != nil {
		return nil, errors.New("context logs unavailable")
	}
	defer r.Close()
	events := []event.Event{}
	count := 0
	readErr := event.Read(ctx, r, e.Source, limit, func(v event.Event) error {
		count++
		if count > 2000 {
			return errors.New("context event limit")
		}
		if v.Timestamp != nil {
			t, err := time.Parse(time.RFC3339Nano, *v.Timestamp)
			if err == nil && !t.Before(start) && !t.After(end) {
				v.Judgment = event.Judgment{Importance: "unclassified"}
				events = append(events, v)
			}
		}
		return nil
	})
	uid2, restarts2, current2, old2, err := identity()
	if err != nil || uid != uid2 || restarts != restarts2 || current != current2 || old != old2 {
		return nil, errors.New("container changed while fetching context")
	}
	if readErr != nil {
		warnings = append(warnings, "Context byte/event limit or read interruption; surrounding evidence may be missing.")
	}
	if len(events) > 500 {
		sort.SliceStable(events, func(i, j int) bool {
			a, _ := time.Parse(time.RFC3339Nano, *events[i].Timestamp)
			b, _ := time.Parse(time.RFC3339Nano, *events[j].Timestamp)
			return absDuration(a.Sub(stamp)) < absDuration(b.Sub(stamp))
		})
		events = events[:500]
		warnings = append(warnings, "Showing nearest 500 events.")
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, *events[i].Timestamp)
		b, _ := time.Parse(time.RFC3339Nano, *events[j].Timestamp)
		if a.Equal(b) {
			return events[i].LineStart < events[j].LineStart
		}
		return a.Before(b)
	})
	warnings = append(warnings, "Kubernetes returns retained logs only; future and rotated logs may be unavailable.")
	if len(events) == 0 {
		warnings = append(warnings, "No retained logs returned in this window.")
	}
	return map[string]any{"events": events, "warnings": warnings, "source": e.Source, "start": start.Format(time.RFC3339Nano), "end": end.Format(time.RFC3339Nano), "previous": previous}, nil
}
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
func FetchContext(ctx context.Context, e event.Event, seconds int) (map[string]any, error) {
	contextName, _ := e.Source["context"].(string)
	if contextName == "" {
		return nil, fmt.Errorf("recorded Kubernetes context is missing")
	}
	cfg := Config{Context: contextName, MaxBytes: 1 << 20, MaxStreams: 1}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rest, err := LoadContext(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c, err := New(rest, cfg)
	if err != nil {
		return nil, err
	}
	return c.Context(ctx, e, seconds)
}
