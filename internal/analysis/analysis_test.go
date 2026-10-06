package analysis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type judgeFunc func(context.Context, []provider.Dossier) ([]event.Judgment, error)

func (f judgeFunc) Judge(c context.Context, d []provider.Dossier) ([]event.Judgment, error) {
	return f(c, d)
}
func verdict() event.Judgment {
	return event.Judgment{Importance: "routine", Severity: "info", Category: "routine", ImportanceConfidence: event.Confidence(.95), SeverityConfidence: event.Confidence(.95), CategoryConfidence: event.Confidence(.95)}
}
func ev(n int) event.Event {
	return event.Event{ID: event.Hash(n)[:20], Source: event.Source{"type": "file", "path": "fixture"}, Text: "INFO request_id=1234567890abcdef", LineCount: 1, Baseline: event.Rules("INFO ready")}
}
func config() Config {
	return Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 8, TTL: time.Minute, NormalInterval: 32}
}
func judge() provider.Judge {
	return judgeFunc(func(_ context.Context, d []provider.Dossier) ([]event.Judgment, error) {
		out := make([]event.Judgment, len(d))
		for i := range out {
			out[i] = verdict()
		}
		return out, nil
	})
}
func TestMicrobatchSingletonNoBlindWindow(t *testing.T) {
	called := make(chan int, 1)
	e, _ := New(config(), judgeFunc(func(_ context.Context, d []provider.Dossier) ([]event.Judgment, error) {
		called <- d[0].WindowOccurrences
		return []event.Judgment{verdict()}, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan event.Event, 1)
	in <- ev(1)
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, in, func(event.Event) error { return nil }) }()
	select {
	case count := <-called:
		if count != 1 {
			t.Fatal(count)
		}
	case <-time.After(time.Second):
		t.Fatal("singleton not assessed")
	}
	close(in)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestGroupDossierFanoutPreservesEvents(t *testing.T) {
	e, _ := New(config(), judge())
	a, b := ev(1), ev(2)
	b.Text = "INFO request_id=abcdef1234567890"
	out := e.Batch(context.Background(), []event.Event{a, b})
	if len(out) != 2 || !out[1].AnalysisReused || out[0].Text != a.Text || out[1].Text != b.Text || out[0].Assessment.WindowOccurrences != 2 || e.Metrics["classifications"] != 1 {
		t.Fatal(out)
	}
	if out[0].ID != a.ID || out[1].ID != b.ID {
		t.Fatal("events changed")
	}
}
func TestProtectedScopeAndUnsafeEvidenceIsolation(t *testing.T) {
	for _, pair := range [][2]string{{"status=200 n=1", "status=500 n=2"}, {`{"level":"info","n":1}`, `{"level":"error","n":2}`}, {"HTTP/1.1 200", "HTTP/1.1 500"}, {"Completed 200", "Completed 503"}} {
		if Template(pair[0], true) == Template(pair[1], true) {
			t.Fatal("protected values merged")
		}
	}
	e, _ := New(config(), judge())
	a, b := ev(1), ev(2)
	b.Source = event.Source{"type": "file", "path": "other"}
	out := e.Batch(context.Background(), []event.Event{a, b})
	if out[1].AnalysisReused {
		t.Fatal("cross scope reuse")
	}
	for _, mutate := range []func(*event.Event){func(e *event.Event) { e.Sensitive = true }, func(e *event.Event) { e.Truncated = true }, func(e *event.Event) { e.Text = "INFO permission granted" }, func(e *event.Event) { e.Text = "INFO ready\n continuation" }} {
		e, _ := New(config(), judge())
		a, b := ev(1), ev(2)
		mutate(&a)
		mutate(&b)
		out := e.Batch(context.Background(), []event.Event{a, b})
		if out[1].AnalysisReused || e.Metrics["classifications"] != 2 {
			t.Fatal("unsafe evidence merged")
		}
	}
}
func TestStaleGenerationCannotPublish(t *testing.T) {
	g := NewGrouper("drain", "strict", 1, time.Minute, false, 32)
	now := time.Now()
	ticket, _, _ := g.Observe(ev(1), now)
	other := ev(2)
	other.Text = "another family"
	g.Observe(other, now)
	g.Observe(ev(3), now)
	if g.Publish(ticket, verdict(), "old", now) {
		t.Fatal("stale published")
	}
}
func TestTTLAndAdaptiveAudits(t *testing.T) {
	g := NewGrouper("drain", "strict", 8, time.Minute, true, 32)
	now := time.Now()
	ticket, _, _ := g.Observe(ev(1), now)
	g.Publish(ticket, verdict(), "id", now)
	for i := 0; i < 31; i++ {
		_, j, _ := g.Observe(ev(i+2), now)
		if j == nil {
			t.Fatal("premature audit")
		}
	}
	_, j, reason := g.Observe(ev(40), now)
	if j != nil || reason != "stable_audit" {
		t.Fatal(reason)
	}
	g.Publish(ticket, verdict(), "id", now)
	_, j, _ = g.Observe(ev(41), now.Add(time.Minute))
	if j != nil {
		t.Fatal("expired reuse")
	}
}
func TestFailedAndSecurityVerdictsNeverSeed(t *testing.T) {
	for _, j := range []event.Judgment{event.Unknown("failed"), {Importance: "uncertain", Severity: "info", Category: "routine", ImportanceConfidence: event.Confidence(.9)}, {Importance: "important", Severity: "impact", Category: "security", ImportanceConfidence: event.Confidence(.99), SeverityConfidence: event.Confidence(.99), CategoryConfidence: event.Confidence(.99)}} {
		g := NewGrouper("drain", "strict", 8, time.Minute, false, 32)
		ticket, _, _ := g.Observe(ev(1), time.Now())
		if g.Publish(ticket, j, "id", time.Now()) {
			t.Fatal("unsafe verdict published")
		}
	}
}
func TestCheckpointHasNoEvidence(t *testing.T) {
	g := NewGrouper("drain", "strict", 8, time.Minute, false, 32)
	a := ev(1)
	a.Text = "unique synthetic body"
	g.Observe(a, time.Now())
	raw, _ := json.Marshal(g.Checkpoint())
	for _, value := range []string{"unique", "fixture", "INFO", "request_id"} {
		if strings.Contains(string(raw), value) {
			t.Fatal("evidence persisted")
		}
	}
}
func TestReviewVeto(t *testing.T) {
	g := NewGrouper("drain", "strict", 8, time.Minute, false, 32)
	ticket, _, _ := g.Observe(ev(1), time.Now())
	g.Publish(ticket, verdict(), "id", time.Now())
	g.Veto([]string{ticket.TemplateID})
	_, j, reason := g.Observe(ev(2), time.Now())
	if j != nil || reason != "review_required" {
		t.Fatal("veto ignored")
	}
}
func TestReviewedRulesRejectProtectedAndHonorShape(t *testing.T) {
	r := Rules{Artifact: 2, Schema: 1}
	rule := Rule{ID: "synthetic", Version: 1, Expires: time.Now().Add(time.Hour).Unix(), Scope: event.Source{"type": "file"}, Shape: map[string]string{"": "object", "/request_id": "string", "/status": "number"}, Normalize: []string{"/request_id"}, Protected: map[string]any{"/status": 200}}
	rule.Review.Compiler = "manual-v1"
	rule.Review.Reviewer = "fixture"
	rule.Review.ID = "fixture"
	rule.Review.At = time.Now().Unix()
	r.Rules = []Rule{rule}
	raw, _ := json.Marshal(r)
	loaded, err := LoadRules(raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a := ev(1)
	a.Text = `{"request_id":"one","status":200}`
	b := a
	b.Text = `{"request_id":"two","status":200}`
	x, ok := loaded.Template(a, time.Now())
	y, ok2 := loaded.Template(b, time.Now())
	if !ok || !ok2 || x != y {
		t.Fatal("reviewed normalization failed")
	}
	b.Text = `{"request_id":"two","status":500}`
	if _, ok := loaded.Template(b, time.Now()); ok {
		t.Fatal("protected changed")
	}
	r.Rules[0].Normalize = []string{"/status"}
	raw, _ = json.Marshal(r)
	if _, err := LoadRules(raw, time.Now()); err == nil {
		t.Fatal("unsafe rule accepted")
	}
}
func TestReviewedRuleReuseInSemanticMode(t *testing.T) {
	rule := Rule{ID: "fixture", Version: 1, Expires: time.Now().Add(time.Hour).Unix(), Scope: event.Source{"type": "file"}, Shape: map[string]string{"": "object", "/request_id": "string", "/status": "number"}, Normalize: []string{"/request_id"}, Protected: map[string]any{"/status": 200}}
	rules := &Rules{Artifact: 2, Schema: 1, Rules: []Rule{rule}}
	c := config()
	c.Strategy = "semantic"
	c.Rules = rules
	e, _ := New(c, judge())
	a, b := ev(1), ev(2)
	a.Text = `{"request_id":"one","status":200}`
	b.Text = `{"request_id":"two","status":200}`
	out := e.Batch(context.Background(), []event.Event{a, b})
	if !out[1].AnalysisReused {
		t.Fatal("reviewed semantic rule did not authorize reuse")
	}
}
func TestProtectedNamesCannotBeObscuredBySeparators(t *testing.T) {
	for _, name := range []string{"sta_tus", "sta-tus", "error_code", "access-level", "operation"} {
		a := `{"` + name + `":200}`
		b := `{"` + name + `":500}`
		if Template(a, true) == Template(b, true) {
			t.Fatal("protected JSON values merged")
		}
	}
}
func TestReviewedRuleRejectsTrailingOrDuplicateJSON(t *testing.T) {
	rule := Rule{ID: "fixture", Version: 1, Expires: time.Now().Add(time.Hour).Unix(), Scope: event.Source{"type": "file"}, Shape: map[string]string{"": "object", "/request_id": "string"}, Normalize: []string{"/request_id"}}
	r := Rules{Rules: []Rule{rule}}
	for _, text := range []string{`{"request_id":"one"} extra`, `{"request_id":"one","request_id":"two"}`} {
		e := ev(1)
		e.Text = text
		if _, ok := r.Template(e, time.Now()); ok {
			t.Fatal("noncanonical evidence reused")
		}
	}
}
func TestReviewVetoAlsoPreventsSameBatchFanout(t *testing.T) {
	e, _ := New(config(), judge())
	first := e.Batch(context.Background(), []event.Event{ev(1)})
	e.groups.Veto([]string{first[0].Assessment.TemplateID})
	out := e.Batch(context.Background(), []event.Event{ev(2), ev(3)})
	if out[1].AnalysisReused || out[1].Importance != "unknown" {
		t.Fatal("review veto allowed fanout")
	}
	c := config()
	c.ReviewIDs = []string{"not-an-id"}
	if c.Validate() == nil {
		t.Fatal("invalid review ID accepted")
	}
}

func TestParseUncertaintyCannotPublishRoutineSafety(t *testing.T) {
	engine, err := New(config(), judge())
	if err != nil {
		t.Fatal(err)
	}
	a := ev(1)
	a.ParseUncertain = true
	out := engine.Batch(context.Background(), []event.Event{a, a})
	for _, e := range out {
		if e.AnalysisReused || e.Importance != "uncertain" || !e.Assessment.EvidenceLimited {
			t.Fatal("uncertain parse granted reusable safety")
		}
	}
}
