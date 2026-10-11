package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	for _, outcome := range []string{"safe", "unknown", "uncertain", "invalid", "error", "schema"} {
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
					if calls == 2 && outcome == "schema" {
						return nil, nil
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
			ev.Text = "INFO request_id=1234567890abcdef"
			sampleB := ev
			sampleB.Text = "INFO request_id=abcdef1234567890"
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
			case "uncertain":
				verdict.Importance = "uncertain"
			case "schema":
				verdict = event.Unknown("synthetic unavailable")
			case "invalid":
				verdict.Severity = "invalid"
			case "error":
				failed = true
			}
			audit := engine.Batch(ctx, []event.Event{sampleB})[0]
			if calls != 2 || audit.Assessment.Reason != "stable_audit" || audit.AnalysisReused {
				t.Fatal("audit reused durable cache", calls, audit)
			}
			engine = newEngine(nil)
			got := batch(engine)
			if outcome == "safe" {
				if calls != 2 || got.Importance != "important" {
					t.Fatal("safe audit not persisted", calls, got)
				}
			} else if calls != 3 || got.Importance == "routine" {
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

// The reviewed family is discovered first before restart and second afterward.
// A one-entry grouper also recreates it between repeated, multi-sample batches.
func TestEngineReviewIdentityAcrossRestartAndEviction(t *testing.T) {
	for _, capacity := range []int{1, 8} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(privateTestDir(t), "state.db")
			var store *Store
			t.Cleanup(func() {
				if store != nil {
					store.Close()
				}
			})
			a, b, other := occurrence(1), occurrence(2), occurrence(3)
			a.Text = "INFO request_id=1234567890abcdef"
			b.Text = "INFO request_id=abcdef1234567890"
			other.Text = "INFO another family"
			calls := 0
			newEngine := func(review []string) *analysis.Engine {
				t.Helper()
				if store != nil {
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				store, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				judge := CachedJudge{Store: store, Contract: "review-identity", TTL: time.Hour, Next: partialJudge(func(ds []provider.Dossier) []event.Judgment {
					js := make([]event.Judgment, len(ds))
					for i, d := range ds {
						if d.Samples[0] == other.Text {
							js[i] = event.Unknown("other family")
						} else {
							calls++
							js[i] = a.Judgment
						}
					}
					return js
				})}
				e, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: capacity, BatchSize: 8, TTL: time.Hour, NormalInterval: 32, ReviewIDs: review}, judge)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			engine := newEngine(nil)
			first := engine.Batch(ctx, []event.Event{a})[0]
			engine.Batch(ctx, []event.Event{other})
			if calls != 1 {
				t.Fatal("missing seed", calls)
			}
			for range 2 {
				engine = newEngine([]string{first.Assessment.TemplateID})
				for range 2 {
					engine.Batch(ctx, []event.Event{other})
					for range 2 {
						before := calls
						out := engine.Batch(ctx, []event.Event{b, a})
						if calls != before+1 || out[0].Assessment.Reason != "review_required" || out[0].Assessment.TemplateID != first.Assessment.TemplateID || out[0].Assessment.VersionID != first.Assessment.VersionID || out[0].AnalysisReused || out[1].AnalysisReused || out[1].Importance != "unknown" {
							t.Fatal("review bypassed after reorder/eviction", calls, out)
						}
						var count int
						if err := store.db.QueryRow("SELECT count(*) FROM verdicts").Scan(&count); err != nil || count != 0 {
							t.Fatal("veto populated durable cache", count, err)
						}
					}
				}
				if engine.Metrics["drain_verdict_reuses"] != 0 || engine.Metrics["drain_group_fanouts"] != 0 {
					t.Fatal("veto reported reuse", engine.Metrics)
				}
			}
		})
	}
}

func TestEngineStaleGenerationCannotSeedDurableCache(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTestDir(t), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	a, b, other := occurrence(1), occurrence(2), occurrence(3)
	a.Text = "INFO request_id=1234567890abcdef"
	b.Text = "INFO request_id=abcdef1234567890"
	other.Text = "INFO another family"
	calls := 0
	newEngine := func() *analysis.Engine {
		t.Helper()
		judge := CachedJudge{Store: store, Contract: "stale", TTL: time.Hour, Next: partialJudge(func(ds []provider.Dossier) []event.Judgment {
			calls++
			js := make([]event.Judgment, len(ds))
			for i := range ds {
				js[i] = event.Unknown("synthetic failure")
				// Only the first, already evicted generation gets a safe result.
				if calls == 1 && i == 0 {
					js[i] = a.Judgment
				}
			}
			return js
		})}
		e, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 1, BatchSize: 8, TTL: time.Hour, NormalInterval: 32}, judge)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	out := newEngine().Batch(ctx, []event.Event{a, other, b})
	if out[0].AnalysisError != "stale template generation" || out[2].Importance != "unknown" || out[0].Assessment.TemplateID != out[2].Assessment.TemplateID {
		t.Fatal("stale generation accepted or family changed", out)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := newEngine().Batch(ctx, []event.Event{a})[0]
	if calls != 2 || got.Importance != "unknown" {
		t.Fatal("stale verdict survived restart", calls, got)
	}
}

func TestEngineLimitedAuditInvalidatesDurableFamily(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTestDir(t), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	calls := 0
	a := occurrence(1)
	a.Text = "INFO request_id=1234567890abcdef"
	newEngine := func() *analysis.Engine {
		t.Helper()
		judge := CachedJudge{Store: store, Contract: "limited", TTL: time.Hour, Next: partialJudge(func(ds []provider.Dossier) []event.Judgment {
			calls++
			if calls == 2 && (len(ds) != 1 || !ds[0].SamplesLimited || ds[0].CacheMode != provider.CacheRefresh) {
				t.Fatal("limited audit not exercised", ds)
			}
			return []event.Judgment{a.Judgment}
		})}
		e, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 16, TTL: time.Hour, NormalInterval: 32, Adaptive: true}, judge)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	engine := newEngine()
	for range 32 {
		engine.Batch(ctx, []event.Event{a})
	}
	var samples []event.Event
	for i := range 9 {
		b := a
		b.Text = fmt.Sprintf("INFO request_id=%016x", i)
		samples = append(samples, b)
	}
	out := engine.Batch(ctx, samples)
	if calls != 2 || out[0].Assessment.Reason != "stable_audit" || !out[0].Assessment.SamplesLimited || out[1].AnalysisReused {
		t.Fatal("limited audit reused", calls, out)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	newEngine().Batch(ctx, []event.Event{a})
	if calls != 3 {
		t.Fatal("limited audit resurrected prior family", calls)
	}
}

func TestEngineDurableSemanticIdentityIsolation(t *testing.T) {
	for _, scenario := range []string{"normalized-sample", "drain-pod", "scope", "baseline", "template", "masking", "strategy", "contract", "exact-pod", "reviewed-version", "reviewed-pod"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(privateTestDir(t), "state.db")
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			a := occurrence(1)
			a.Source = event.Source{"type": "kubernetes", "context": "synthetic", "namespace": "demo", "container": "app", "pod": "pod-a"}
			a.Text = `{"request_id":"1234567890abcdef","status":200}`
			b := a
			b.Text = `{"request_id":"abcdef1234567890","status":200}`
			cfg := analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 1, BatchSize: 8, TTL: time.Hour, NormalInterval: 32}
			if scenario == "exact-pod" {
				cfg.Strategy = "exact"
				b.Text = a.Text
			}
			if strings.HasPrefix(scenario, "reviewed-") {
				cfg.Strategy = "semantic"
				rule := analysis.Rule{ID: "synthetic", Version: 1, Expires: time.Now().Add(time.Hour).Unix(), Scope: event.Source{"type": "kubernetes"}, Shape: map[string]string{"": "object", "/request_id": "string", "/status": "number"}, Normalize: []string{"/request_id"}, Protected: map[string]any{"/status": 200}}
				rule.Review.Compiler, rule.Review.Reviewer, rule.Review.ID, rule.Review.At = "manual-v1", "fixture", "fixture", time.Now().Unix()
				raw, err := json.Marshal(analysis.Rules{Artifact: 2, Schema: 1, Rules: []analysis.Rule{rule}})
				if err != nil {
					t.Fatal(err)
				}
				cfg.Rules, err = analysis.LoadRules(raw, time.Now())
				if err != nil {
					t.Fatal(err)
				}
			}
			contract := "identity"
			calls := 0
			other := occurrence(3)
			other.Text = "INFO unrelated family"
			newEngine := func() *analysis.Engine {
				t.Helper()
				judge := CachedJudge{Store: store, Contract: contract, TTL: time.Hour, Next: partialJudge(func(ds []provider.Dossier) []event.Judgment {
					js := make([]event.Judgment, len(ds))
					for i, d := range ds {
						if d.Samples[0] != other.Text {
							calls++
						}
						js[i] = a.Judgment
					}
					return js
				})}
				e, err := analysis.New(cfg, judge)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			first := newEngine().Batch(ctx, []event.Event{a})[0]
			switch scenario {
			case "drain-pod", "exact-pod", "reviewed-pod":
				b.Source = event.Source{"type": "kubernetes", "context": "synthetic", "namespace": "demo", "container": "app", "pod": "pod-b"}
			case "scope":
				b.Source = event.Source{"type": "kubernetes", "context": "synthetic", "namespace": "other", "container": "app", "pod": "pod-a"}
			case "baseline":
				b.Baseline.Important = true
			case "template":
				b.Text = `{"request_id":"abcdef1234567890","status":201}`
			case "masking":
				cfg.Masking = "classic"
			case "strategy":
				cfg.Strategy = "exact"
			case "contract":
				contract = "changed"
			case "reviewed-version":
				cfg.Rules.Rules[0].Version++
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			engine := newEngine()
			engine.Batch(ctx, []event.Event{other}) // Reverse discovery order.
			got := engine.Batch(ctx, []event.Event{b})[0]
			wantReuse := scenario == "normalized-sample" || scenario == "drain-pod"
			if (calls == 1) != wantReuse || got.Importance != a.Importance {
				t.Fatal("durable semantic isolation/reuse failed", calls, got)
			}
			wantSameID := wantReuse || scenario == "contract"
			if (first.Assessment.TemplateID == got.Assessment.TemplateID) != wantSameID {
				t.Fatal("review identity crossed semantic boundary", first.Assessment, got.Assessment)
			}
		})
	}
}
