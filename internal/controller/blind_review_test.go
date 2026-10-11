package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

func TestEngineLimitedDefaultReassessmentAcrossRestart(t *testing.T) {
	for _, outcome := range []string{"routine", "important", "unknown", "uncertain", "invalid", "error", "schema"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(privateTestDir(t), "state.db")
			var store *Store
			defer func() {
				if store != nil {
					store.Close()
				}
			}()
			calls := 0
			a := occurrence(1)
			a.Text = "INFO request_id=1234567890abcdef"
			routine := a.Judgment
			routine.Importance = "routine"
			next := reassessmentJudge(func(ds []provider.Dossier) ([]event.Judgment, error) {
				calls++
				if calls == 1 {
					return []event.Judgment{routine}, nil
				}
				if calls != 2 {
					return []event.Judgment{event.Unknown("unavailable")}, nil
				}
				if len(ds) != 1 || !ds[0].SamplesLimited || ds[0].CacheMode != provider.CacheDefault {
					t.Fatal("default limited reassessment missing", ds)
				}
				j := routine
				switch outcome {
				case "important":
					j.Importance = "important"
				case "unknown":
					j = event.Unknown("unavailable")
				case "uncertain":
					j.Importance = "uncertain"
				case "invalid":
					j.Importance = "invalid"
				case "error":
					return nil, errors.New("fixture failure")
				case "schema":
					return nil, nil
				}
				return []event.Judgment{j}, nil
			})
			reopen := func() *analysis.Engine {
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
				e, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 16, TTL: time.Hour, NormalInterval: 32, Adaptive: true}, CachedJudge{Store: store, Next: next, Contract: "limited-default", TTL: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			reopen().Batch(ctx, []event.Event{a})
			e := reopen()
			samples := make([]event.Event, 9)
			for i := range samples {
				samples[i] = a
				samples[i].Text = fmt.Sprintf("INFO request_id=%016x", i)
			}
			out := e.Batch(ctx, samples)
			if calls != 2 || !out[0].Assessment.SamplesLimited || out[1].AnalysisReused {
				t.Fatal("limited dispatch/fanout", calls, out)
			}
			var rows int
			if err := store.db.QueryRow("SELECT count(*) FROM verdicts").Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 0 {
				t.Errorf("unsafe reassessment retained %d durable verdicts", rows)
			}
			for attempt := range 2 {
				engine := e
				if attempt == 1 {
					engine = reopen()
				}
				before := calls
				got := engine.Batch(ctx, []event.Event{a})[0]
				if calls != before+1 || got.Importance != "unknown" || got.AnalysisReused {
					t.Errorf("old verdict resurrected: calls=%d before=%d result=%+v", calls, before, got)
				}
			}
		})
	}
}

func TestEngineDurableTTLAcrossRestart(t *testing.T) {
	for _, scenario := range []string{"boundary", "past", "shorter-ttl", "longer-ttl", "rollback-before-created", "rollback-after-hit", "rollback-within-lifetime", "slow-provider"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(privateTestDir(t), "state.db")
			var store *Store
			defer func() {
				if store != nil {
					store.Close()
				}
			}()
			start := time.Unix(1000, 0)
			now := start
			ttl := time.Minute
			calls := 0
			a := occurrence(1)
			a.Text = "INFO request_id=1234567890abcdef"
			next := partialJudge(func(ds []provider.Dossier) []event.Judgment {
				calls++
				if scenario == "slow-provider" && calls == 1 {
					now = start.Add(time.Minute)
				}
				return []event.Judgment{a.Judgment}
			})
			reopen := func() *analysis.Engine {
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
				clock := func() time.Time { return now }
				e, err := analysis.New(analysis.Config{Now: clock, Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 16, TTL: ttl, NormalInterval: 32}, CachedJudge{Now: clock, Store: store, Next: next, Contract: "ttl", TTL: ttl})
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			e := reopen()
			e.Batch(ctx, []event.Event{a})
			if scenario == "slow-provider" {
				e.Batch(ctx, []event.Event{a})
				if calls != 2 {
					t.Fatal("provider latency extended cache lifetime", calls)
				}
				return
			}
			now = start.Add(59 * time.Second)
			if scenario == "shorter-ttl" {
				ttl = 30 * time.Second
				now = start.Add(29 * time.Second)
			}
			if scenario == "longer-ttl" {
				ttl = 2 * time.Minute
			}
			if scenario == "rollback-before-created" {
				now = start.Add(-time.Second)
			}
			e = reopen()
			e.Batch(ctx, []event.Event{a})
			if scenario == "rollback-before-created" {
				if calls != 2 {
					t.Fatal("rollback accepted durable entry", calls)
				}
				return
			}
			if calls != 1 {
				t.Fatal("near-expiry durable hit missing", calls)
			}
			got := e.Batch(ctx, []event.Event{a})[0]
			if !got.AnalysisReused || calls != 1 {
				t.Fatal("remaining lifetime was not reusable", got, calls)
			}
			now = start.Add(time.Minute)
			if scenario == "past" {
				now = now.Add(time.Nanosecond)
			}
			if scenario == "shorter-ttl" {
				now = start.Add(30 * time.Second)
			}
			if scenario == "rollback-after-hit" {
				now = start.Add(-time.Second)
			}
			if scenario == "rollback-within-lifetime" {
				now = start.Add(58 * time.Second)
			}
			got = e.Batch(ctx, []event.Event{a})[0]
			if calls != 2 || got.AnalysisReused {
				t.Fatal("in-memory cache escaped original lifetime", calls, got)
			}
		})
	}
}

type vetoTeacher struct{ calls int }

func (v *vetoTeacher) Propose(context.Context, provider.TemplateEvidence) (provider.Proposal, error) {
	return provider.Proposal{}, errors.New("unexpected proposal")
}
func (v *vetoTeacher) Review(context.Context, provider.TemplateEvidence) (provider.Review, error) {
	v.calls++
	return provider.Review{Fields: []string{}, Importance: "important", Severity: "impact", Category: "dependency", Confidence: event.Confidence(.99), Explanation: "upstream_failure"}, nil
}
func TestEngineLearningVetoAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTestDir(t), "state.db")
	var store *Store
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	a, b := occurrence(1), occurrence(2)
	a.Text = "INFO request_id=1234567890abcdef"
	b.Text = "INFO request_id=abcdef1234567890"
	tch := &vetoTeacher{}
	cfg := analysis.Config{Strategy: "drain", Masking: "classic", Adaptive: true, Capacity: 8, BatchSize: 16, TTL: time.Minute, NormalInterval: 32, Teacher: tch, TemplateMinSamples: 2, TemplateCapacity: 8, TemplateTTL: time.Minute, TemplateConfidence: .85}
	reopen := func() *analysis.Engine {
		t.Helper()
		if store != nil {
			store.Close()
		}
		var err error
		store, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		e, err := analysis.New(cfg, CachedJudge{Store: store, Contract: "learning-veto", TTL: time.Minute, Next: partialJudge(func(ds []provider.Dossier) []event.Judgment {
			js := make([]event.Judgment, len(ds))
			for i := range js {
				js[i] = event.Unknown("review fixture")
			}
			return js
		})})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	seed := reopen().Batch(ctx, []event.Event{a})[0]
	cfg.ReviewIDs = []string{seed.Assessment.TemplateID}
	for range 2 {
		e := reopen()
		for range 3 {
			out := e.Batch(ctx, []event.Event{b, a})
			for _, got := range out {
				if got.Importance != "unknown" || got.Assessment.Reason != "review_required" || got.AnalysisReused {
					t.Errorf("learning bypassed veto: %+v", got)
				}
			}
			var rows int
			if err := store.db.QueryRow("SELECT count(*) FROM verdicts").Scan(&rows); err != nil || rows != 0 {
				t.Fatal("veto published durable verdict", rows, err)
			}
		}
		for _, metric := range []string{"drain_verdict_reuses", "drain_group_fanouts", "refine_escalations", "template_provider_attempts"} {
			if e.Metrics[metric] != 0 {
				t.Errorf("veto altered %s=%d", metric, e.Metrics[metric])
			}
		}
	}
	if tch.calls != 0 {
		t.Errorf("veto sent to teacher %d times", tch.calls)
	}
}

// These flags normally make an event ineligible before it gets a family ID.
// CachedJudge must also fail closed when an identified dossier carries them.
func TestUnsafeDossierInvalidatesBeforeDispatch(t *testing.T) {
	for _, flag := range []string{"sensitive", "truncated", "evidence", "samples", "security"} {
		t.Run(flag, func(t *testing.T) {
			ctx := context.Background()
			store := openTest(t)
			d := provider.Dossier{TemplateID: event.Hash("template"), VersionID: event.Hash("version"), ScopeID: event.Hash("scope")}
			key := event.Hash([]any{"go-group-cache-v2", "unsafe", d.ScopeID, d.TemplateID, d.VersionID, false})
			j := occurrence(1).Judgment
			if err := store.PutVerdict(ctx, key, j, time.Now().Unix(), time.Minute); err != nil {
				t.Fatal(err)
			}
			switch flag {
			case "sensitive":
				d.Sensitive = true
			case "truncated":
				d.Truncated = true
			case "evidence":
				d.EvidenceLimited = true
			case "samples":
				d.SamplesLimited = true
			case "security":
				d.SecuritySignal = true
			}
			calls := 0
			judge := CachedJudge{Store: store, Contract: "unsafe", TTL: time.Minute, Next: partialJudge(func(ds []provider.Dossier) []event.Judgment {
				calls++
				var rows int
				if err := store.db.QueryRow("SELECT count(*) FROM verdicts").Scan(&rows); err != nil || rows != 0 {
					t.Fatal("invalidation happened after dispatch", rows, err)
				}
				return []event.Judgment{j}
			})}
			if _, err := judge.Judge(ctx, []provider.Dossier{d}); err != nil {
				t.Fatal(err)
			}
			got, err := store.Lookup(ctx, key, time.Now(), time.Minute)
			if err != nil || got != nil || calls != 1 {
				t.Fatal("unsafe publication/reuse", got, calls, err)
			}
		})
	}
}

func TestDurableLifetimeMetadataStaysLocal(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	key := event.Hash("lifetime")
	j := occurrence(1).Judgment
	if err := store.PutVerdict(ctx, key, j, 1000, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := store.Lookup(ctx, key, time.Unix(1029, 0), 30*time.Second)
	if err != nil || got == nil || !got.CacheCreatedAt.Equal(time.Unix(1000, 0)) || !got.CacheExpiresAt.Equal(time.Unix(1030, 0)) {
		t.Fatal("effective durable bounds", got, err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded event.Judgment
	if err := provider.Strict(raw, &decoded); err != nil || !decoded.CacheCreatedAt.IsZero() || !decoded.CacheExpiresAt.IsZero() || !decoded.CacheObservedAt.IsZero() || !decoded.CacheDeadline.IsZero() {
		t.Fatal("local metadata entered persisted/provider schema", string(raw), err)
	}
	for _, field := range []string{"CacheExpiresAt", "cache_expires_at", "CacheCreatedAt", "cache_created_at", "CacheObservedAt", "cache_observed_at", "CacheDeadline", "cache_deadline"} {
		injected := append([]byte(fmt.Sprintf(`{"%s":"2099-01-01T00:00:00Z",`, field)), raw[1:]...)
		if provider.Strict(injected, &decoded) == nil {
			t.Fatal("untrusted lifetime accepted", field)
		}
	}
}
