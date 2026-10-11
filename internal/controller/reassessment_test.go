package controller

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type reassessmentJudge func([]provider.Dossier) ([]event.Judgment, error)

func (f reassessmentJudge) Judge(_ context.Context, ds []provider.Dossier) ([]event.Judgment, error) {
	return f(ds)
}

func TestEngineDurableReassessmentAcrossRestart(t *testing.T) {
	for _, outcome := range []string{"safe", "unknown", "invalid", "error"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(privateTestDir(t), "state.db")
			var store *Store
			calls := 0
			failed := false
			verdict := occurrence(1).Judgment
			verdict.Importance = "routine"
			newEngine := func(review []string) *analysis.Engine {
				if store != nil {
					store.Close()
				}
				var err error
				store, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				judge := CachedJudge{Store: store, Contract: "reassessment", TTL: time.Hour, Next: reassessmentJudge(func(ds []provider.Dossier) ([]event.Judgment, error) {
					calls++
					if len(ds) != 1 {
						t.Fatalf("unexpected dossiers: %+v", ds)
					}
					if failed {
						return nil, errors.New("synthetic provider failure")
					}
					return []event.Judgment{verdict}, nil
				})}
				e, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 8, TTL: time.Hour, NormalInterval: 32, Adaptive: true, ReviewIDs: review}, judge)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			t.Cleanup(func() {
				if store != nil {
					store.Close()
				}
			})
			ev := occurrence(1)
			batch := func(e *analysis.Engine) event.Event { return e.Batch(ctx, []event.Event{ev})[0] }
			engine := newEngine(nil)
			first := batch(engine)
			if calls != 1 {
				t.Fatal("initial provider call missing", calls)
			}
			engine = newEngine(nil)
			if got := batch(engine); calls != 1 || got.Importance != "routine" {
				t.Fatal("persisted cache not exercised", calls, got)
			}
			for range 31 {
				if got := batch(engine); !got.AnalysisReused || calls != 1 {
					t.Fatal("early reassessment", calls, got)
				}
			}
			switch outcome {
			case "safe":
				verdict.Importance = "important"
			case "unknown":
				verdict = event.Unknown("synthetic unavailable")
			case "invalid":
				verdict.Severity = "invalid"
			case "error":
				failed = true
			}
			audit := batch(engine)
			if calls != 2 || audit.Assessment.Reason != "stable_audit" || audit.AnalysisReused {
				t.Fatal("audit reused durable cache", calls, audit)
			}
			engine = newEngine(nil)
			got := batch(engine)
			if outcome == "safe" {
				if calls != 2 || got.Importance != "important" {
					t.Fatal("safe audit not persisted", calls, got)
				}
			} else if calls != 3 || got.Importance != "unknown" {
				t.Fatal("unsafe audit resurrected prior cache", calls, got)
			}
			// Seed a safe entry again, then restart with a human review veto.
			verdict = occurrence(1).Judgment
			failed = false
			judge := CachedJudge{Store: store, Next: partialJudge(func(ds []provider.Dossier) []event.Judgment { return []event.Judgment{verdict} }), Contract: "reassessment", TTL: time.Hour, Rescore: true}
			seed, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 8, TTL: time.Hour, NormalInterval: 32}, judge)
			if err != nil {
				t.Fatal(err)
			}
			batch(seed)
			before := calls
			for range 2 {
				engine = newEngine([]string{first.Assessment.TemplateID})
				for range 2 {
					results := engine.Batch(ctx, []event.Event{ev, ev})
					before++
					if calls != before || results[0].Assessment.Reason != "review_required" || results[0].AnalysisReused || results[1].AnalysisReused || results[1].Importance != "unknown" {
						t.Fatal("review veto reused/fanned out", calls, results)
					}
				}
			}
			var count int
			if err := store.db.QueryRow("SELECT count(*) FROM verdicts").Scan(&count); err != nil || count != 0 {
				t.Fatal("veto published cache", count, err)
			}
		})
	}
}
