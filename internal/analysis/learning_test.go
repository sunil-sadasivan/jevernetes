package analysis

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type teacher struct{ proposals, reviews int }

func (t *teacher) Propose(context.Context, provider.TemplateEvidence) (provider.Proposal, error) {
	t.proposals++
	return provider.Proposal{Name: "fixture", Version: 1, Required: []string{"/request_id"}, Normalize: []string{"/request_id"}, Protected: []string{}, Cacheable: true, Confidence: event.Confidence(.99), Explanation: "opaque_identifiers"}, nil
}
func (t *teacher) Review(context.Context, provider.TemplateEvidence) (provider.Review, error) {
	t.reviews++
	return provider.Review{Fields: []string{}, Importance: "important", Severity: "impact", Category: "dependency", Confidence: event.Confidence(.99), Explanation: "upstream_failure"}, nil
}
func TestTemplateShadowReplayNeverPublishes(t *testing.T) {
	c := config()
	c.Strategy = "semantic"
	teacher := &teacher{}
	c.Teacher = teacher
	c.TemplateMinSamples = 2
	c.TemplateCapacity = 8
	c.TemplateTTL = time.Minute
	c.TemplateConfidence = .85
	e, err := New(c, judge())
	if err != nil {
		t.Fatal(err)
	}
	a, b := ev(1), ev(2)
	a.Text = `{"request_id":"1234567890abcdef","status":200}`
	b.Text = `{"request_id":"abcdef1234567890","status":200}`
	out := e.Batch(context.Background(), []event.Event{a, b})
	if teacher.proposals != 1 || e.Metrics["semantic_replay_passed"] != 1 || out[1].AnalysisReused {
		t.Fatal("shadow contract")
	}
	out = e.Batch(context.Background(), []event.Event{b})
	if out[0].AnalysisReused {
		t.Fatal("proposal enabled reuse")
	}
}
func TestTemplateEscalationOnlyAndNoVolumeInference(t *testing.T) {
	tch := &teacher{}
	c := config()
	c.Masking = "classic"
	c.Teacher = tch
	c.TemplateMinSamples = 2
	c.TemplateCapacity = 8
	c.TemplateTTL = time.Minute
	c.TemplateConfidence = .85
	e, _ := New(c, judge())
	out := e.Batch(context.Background(), []event.Event{ev(1), ev(2)})
	if tch.reviews != 1 || out[0].Importance != "important" || out[1].Importance != "important" {
		t.Fatal("review not applied")
	}
	out = e.Batch(context.Background(), []event.Event{ev(3)})
	if out[0].Importance != "important" {
		t.Fatal("escalation lost")
	}
	r, _ := tch.Review(context.Background(), provider.TemplateEvidence{})
	r.Explanation = "pattern_burst"
	if Escalation(verdict(), r) != nil {
		t.Fatal("volume-only escalation")
	}
	r.Importance = "routine"
	if Escalation(out[0].Judgment, r) != nil {
		t.Fatal("downgrade")
	}
}
func TestProposalProtectedPathAndShapeRejected(t *testing.T) {
	p, _ := (&teacher{}).Propose(context.Background(), provider.TemplateEvidence{})
	samples := []string{`{"request_id":"1234567890abcdef","status":"200"}`, `{"request_id":"abcdef1234567890","status":"500"}`}
	p.Normalize = []string{"/status"}
	if ValidateProposal(p, samples) {
		t.Fatal("protected normalize")
	}
	p.Normalize = []string{"/request_id"}
	samples[1] = strings.Replace(samples[1], "request_id", "other_id", 1)
	if ValidateProposal(p, samples) {
		t.Fatal("shape changed")
	}
}

func TestLearningCannotOverrideStaleGeneration(t *testing.T) {
	for _, prelearned := range []bool{false, true} {
		t.Run(fmt.Sprint(prelearned), func(t *testing.T) {
			c := config()
			c.Capacity = 1
			c.Teacher = &teacher{}
			c.TemplateMinSamples = 2
			c.TemplateCapacity = 8
			c.TemplateTTL = time.Minute
			c.TemplateConfidence = .85
			e, err := New(c, judge())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			a, b, other := ev(1), ev(2), ev(3)
			b.Text = "INFO request_id=abcdef1234567890"
			other.Text = "another family"
			if prelearned {
				e.Batch(ctx, []event.Event{a, b})
				e.Batch(ctx, []event.Event{other})
			}
			out := e.Batch(ctx, []event.Event{a, other, b, a})
			if out[0].Importance != "unknown" || out[0].AnalysisError != "stale template generation" || out[0].AnalysisReused {
				t.Fatal("learning overrode stale generation", out[0])
			}
			if out[2].Importance != "important" || out[3].Importance != "important" {
				t.Fatal("current generation lost escalation", out)
			}
		})
	}
}

func TestLearningSavedEscalationRespectsVeto(t *testing.T) {
	c := config()
	c.Masking, c.Adaptive = "classic", true
	tch := &teacher{}
	c.Teacher, c.TemplateMinSamples, c.TemplateCapacity, c.TemplateTTL, c.TemplateConfidence = tch, 2, 8, time.Minute, .85
	e, err := New(c, judge())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, b := ev(1), ev(2)
	b.Text = "INFO request_id=abcdef1234567890"
	out := e.Batch(ctx, []event.Event{a, b})
	if tch.reviews != 1 || out[1].Importance != "important" {
		t.Fatal("saved escalation not seeded")
	}
	e.groups.Veto([]string{out[0].Assessment.TemplateID})
	before := e.Metrics["drain_group_fanouts"]
	for range 3 {
		out = e.Batch(ctx, []event.Event{b, a})
		if out[1].Importance != "unknown" || out[1].Assessment.Reason != "review_required" || out[0].Importance == "important" || out[0].AnalysisReused || out[1].AnalysisReused {
			t.Errorf("saved escalation overrode veto: %+v", out)
		}
	}
	if tch.reviews != 1 || e.Metrics["drain_group_fanouts"] != before || e.Metrics["drain_verdict_reuses"] != 0 {
		t.Fatal("veto reuse metrics", e.Metrics, tch.reviews)
	}
}

func TestLearningLimitedBatchPreservesIndependentReview(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(fmt.Sprint(saved), func(t *testing.T) {
			c := config()
			c.Masking, c.Adaptive, c.BatchSize = "classic", true, 16
			tch := &teacher{}
			c.Teacher, c.TemplateMinSamples, c.TemplateCapacity, c.TemplateTTL, c.TemplateConfidence = tch, 2, 8, time.Minute, .85
			e, err := New(c, judge())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if saved {
				out := e.Batch(ctx, []event.Event{ev(1), ev(2)})
				// Retain teacher state while forcing a primary reassessment.
				for _, p := range e.groups.entries {
					e.groups.Invalidate(p.ticket)
				}
				if out[1].Importance != "important" {
					t.Fatal("escalation seed missing")
				}
			}
			samples := make([]event.Event, 9)
			for i := range samples {
				samples[i] = ev(i)
				samples[i].Text = fmt.Sprintf("INFO request_id=%016x", i)
			}
			out := e.Batch(ctx, samples)
			if !out[0].Assessment.SamplesLimited || out[0].Importance != "uncertain" {
				t.Fatal("limited evidence not preserved", out[0])
			}
			for _, got := range out[1:] {
				if got.Importance != "unknown" || got.AnalysisReused {
					t.Fatal("learning escalated limited fanout", got)
				}
			}
			want := 0
			if saved {
				want = 1
			}
			if tch.reviews != want {
				t.Fatal("limited evidence taught", tch.reviews)
			}
		})
	}
}

func TestLearningLifetimeDoesNotSlideOrSurviveRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprint(rollback), func(t *testing.T) {
			start := time.Unix(1000, 0)
			now := start
			c := config()
			c.Now = func() time.Time { return now }
			tch := &teacher{}
			c.Teacher, c.TemplateMinSamples, c.TemplateCapacity, c.TemplateTTL, c.TemplateConfidence = tch, 2, 8, time.Minute, .85
			e, err := New(c, judge())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			e.Batch(ctx, []event.Event{ev(1), ev(2)})
			now = start.Add(59 * time.Second)
			if got := e.Batch(ctx, []event.Event{ev(3)})[0]; got.Importance != "important" {
				t.Fatal("saved escalation missing", got)
			}
			now = start.Add(time.Minute)
			if rollback {
				now = start.Add(58 * time.Second)
			}
			got := e.Batch(ctx, []event.Event{ev(4)})[0]
			if got.Importance != "routine" || tch.reviews != 1 {
				t.Fatal("teacher lifetime escaped", got, tch.reviews)
			}
		})
	}
}
