package analysis

import (
	"context"
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
