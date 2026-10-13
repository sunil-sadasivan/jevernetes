package search

import (
	"context"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"testing"
)

type fakeClient struct {
	calls int
	seen  []event.Event
}

func (f *fakeClient) Search(_ context.Context, _ string, events []event.Event) ([]provider.Relevance, error) {
	f.calls++
	f.seen = append(f.seen, events...)
	v := []provider.Relevance{}
	for range events {
		v = append(v, provider.Relevance{Relevance: "match", Confidence: event.Confidence(.9)})
	}
	return v, nil
}
func (f *fakeClient) Usage() provider.Usage { return provider.Usage{Complete: true, Attempts: f.calls} }
func TestFrozenGroupingCacheAndOccurrenceChange(t *testing.T) {
	events := []event.Event{{ID: "a", Source: event.Source{"path": "one"}, Text: "database unavailable"}, {ID: "b", Source: event.Source{"path": "one"}, Text: "database unavailable"}, {ID: "c", Source: event.Source{"path": "two"}, Text: "database unavailable"}}
	cache := &Cache{}
	client := &fakeClient{}
	r, _ := New(events, "database", "jev", 16, cache)
	events[0].Text = "changed"
	r.Execute(context.Background(), client)
	s := r.Snapshot(0)
	if s["total_groups"] != 2 || s["matching_instances"] != 3 || client.calls != 1 || client.seen[0].SearchOccurrences.Count != 2 {
		t.Fatalf("unexpected search: %#v", s)
	}
	repeat, _ := New(r.Events, "database", "jev", 16, cache)
	repeat.Execute(context.Background(), client)
	if client.calls != 1 || repeat.Snapshot(0)["cache_hits"] != 2 {
		t.Fatal("cache not reused")
	}
	more := append(r.Events, event.Event{ID: "d", Source: event.Source{"path": "one"}, Text: "database unavailable"})
	changed, _ := New(more, "database", "jev", 16, cache)
	changed.Execute(context.Background(), client)
	if client.calls != 2 || changed.Snapshot(0)["cache_hits"] != 1 {
		t.Fatal("occurrence change reused stale answer")
	}
}
func TestLiteralCancelledAndBudget(t *testing.T) {
	events := []event.Event{}
	for i := 0; i < 20; i++ {
		events = append(events, event.Event{ID: string(rune('a' + i)), Source: event.Source{}, Text: string(rune('a'+i)) + " DATABASE"})
	}
	r, _ := New(events, "database", "literal", 1, nil)
	r.Execute(context.Background(), nil)
	if r.Snapshot(0)["matching_instances"] != 20 || r.Snapshot(0)["partial"] != false {
		t.Fatal(r.Snapshot(0))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ = New(events, "database", "jev", 1, nil)
	r.Execute(ctx, &fakeClient{})
	if r.Snapshot(0)["status"] != "cancelled" {
		t.Fatal("cancel ignored")
	}
	r, _ = New(events, "database", "jev", 1, nil)
	r.Execute(context.Background(), &fakeClient{})
	if r.Snapshot(0)["examined_groups"] != 8 || r.Snapshot(0)["partial"] != true {
		t.Fatal("budget ignored")
	}
}
