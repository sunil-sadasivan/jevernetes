package kube

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type interruptedJudge struct{ calls int }

func (j *interruptedJudge) Judge(_ context.Context, ds []provider.Dossier) ([]event.Judgment, error) {
	j.calls++
	js := make([]event.Judgment, len(ds))
	for i := range js {
		js[i] = event.Judgment{Importance: "routine", Severity: "info", Category: "routine", ImportanceConfidence: event.Confidence(.99), SeverityConfidence: event.Confidence(.99), CategoryConfidence: event.Confidence(.99)}
	}
	return js, nil
}

func TestLiveTerminatedHeaderTransportFailureThroughReuseGates(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	go func() {
		_, _ = io.WriteString(w, "2026-01-01T00:00:00Z INFO routine\n")
		_ = w.CloseWithError(errors.New("synthetic transport failure"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var events []event.Event
	err := readLive(ctx, r, event.NewParser(event.Source{"type": "kubernetes"}), func(e event.Event) error { events = append(events, e); return nil })
	if !errors.Is(err, errStreamRead) || len(events) != 1 {
		t.Fatal(err, events)
	}
	e := events[0]
	if !e.Truncated || !e.ParseUncertain || analysis.Eligible(e) {
		t.Fatal("interrupted header reusable", e)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := controller.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	next := &interruptedJudge{}
	judge := controller.CachedJudge{Store: store, Next: next, Contract: "interrupted", TTL: time.Minute}
	engine, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 8, TTL: time.Minute, NormalInterval: 32}, judge)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, got := range engine.Batch(ctx, []event.Event{e, e}) {
			if got.AnalysisReused || got.Importance != "uncertain" || !got.Assessment.EvidenceLimited || controller.DefaultPolicy().Decide(got) != "review" {
				t.Fatal("interruption bypassed gates", got)
			}
		}
	}
	// Explicit template identity also exercises the durable safety gate directly.
	d := provider.Dossier{TemplateID: event.Hash("synthetic")}
	d.Observe(e)
	for range 2 {
		if _, err := judge.Judge(ctx, []provider.Dossier{d}); err != nil {
			t.Fatal(err)
		}
	}
	if next.calls != 4 {
		t.Fatal("interrupted evidence cached", next.calls)
	}
}
