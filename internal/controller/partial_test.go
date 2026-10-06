package controller

import (
	"context"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

func TestTransportPartialThroughReuseGates(t *testing.T) {
	for _, raw := range []string{"2026-01-01T00:00:00Z INFO routine", "2026-01-01T00:00:00Z INFO routine\n2026-01-01T00:00:01Z   at continuation"} {
		p := event.NewParser(event.Source{"type": "kubernetes"})
		out := p.Feed([]byte(raw))
		out = append(out, p.Finish(false)...)
		if len(out) != 1 {
			t.Fatal(out)
		}
		e := out[0]
		if !e.Truncated || !e.ParseUncertain {
			t.Errorf("partial transport considered safe: %+v", e)
		}
		var d provider.Dossier
		d.Observe(e)
		if !d.EvidenceLimited || !d.Truncated || analysis.Eligible(e) {
			t.Error("partial evidence reusable")
		}
		// Exercise both grouping/fanout and durable reuse with a local fixed
		// judgment fixture; no provider transport or credentials are involved.
		store := openTest(t)
		calls := 0
		judge := CachedJudge{Store: store, Contract: "partial", TTL: time.Minute, Next: partialJudge(func(groups []provider.Dossier) []event.Judgment {
			calls++
			out := make([]event.Judgment, len(groups))
			for i := range out {
				out[i] = event.Judgment{Importance: "routine", Severity: "info", Category: "routine", ImportanceConfidence: event.Confidence(.99), SeverityConfidence: event.Confidence(.99), CategoryConfidence: event.Confidence(.99)}
			}
			return out
		})}
		engine, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 8, TTL: time.Minute, NormalInterval: 32}, judge)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			for _, result := range engine.Batch(context.Background(), []event.Event{e, e}) {
				if result.AnalysisReused || result.Importance != "uncertain" || !result.Assessment.EvidenceLimited || DefaultPolicy().Decide(result) != "review" {
					t.Fatal("partial event acquired routine safety", result)
				}
			}
		}
		var cached int
		if err := store.db.QueryRow("SELECT count(*) FROM verdicts").Scan(&cached); err != nil || cached != 0 || calls != 2 {
			t.Fatal("partial result cached", cached, calls, err)
		}
		e.Judgment = occurrence(1).Judgment
		if DefaultPolicy().Decide(e) != "review" {
			t.Error("partial event bypassed policy review")
		}
	}
	p := event.NewParser(event.Source{"type": "file"})
	p.Feed([]byte("INFO routine"))
	out := p.Finish(false)
	if len(out) != 1 || out[0].Truncated || out[0].ParseUncertain || !analysis.Eligible(out[0]) {
		t.Fatal("clean file EOF changed", out)
	}
}

type partialJudge func([]provider.Dossier) []event.Judgment

func (f partialJudge) Judge(_ context.Context, groups []provider.Dossier) ([]event.Judgment, error) {
	return f(groups), nil
}
