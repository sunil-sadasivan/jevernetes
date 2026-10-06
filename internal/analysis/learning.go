package analysis

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type LearningDecision struct {
	TemplateID string             `json:"template_id"`
	VersionID  string             `json:"version_id"`
	Seen       uint64             `json:"seen"`
	Outcome    string             `json:"outcome"`
	Proposal   *provider.Proposal `json:"proposal,omitempty"`
	Review     *provider.Review   `json:"review,omitempty"`
}
type learned struct {
	evidence   provider.TemplateEvidence
	version    string
	expires    time.Time
	escalation *event.Judgment
	next       int
}
type learning struct {
	teacher    provider.Teacher
	entries    map[string]*learned
	decisions  []LearningDecision
	minSamples int
	confidence float64
	ttl        time.Duration
	capacity   int
}

func newLearning(c Config) *learning {
	if c.Teacher == nil || c.Offline {
		return nil
	}
	return &learning{teacher: c.Teacher, entries: map[string]*learned{}, decisions: []LearningDecision{}, minSamples: c.TemplateMinSamples, confidence: c.TemplateConfidence, ttl: c.TemplateTTL, capacity: c.TemplateCapacity}
}
func (e *Engine) LearningReport() any {
	if e.learning == nil {
		return nil
	}
	return map[string]any{"schema_version": 1, "mode": "shadow-and-escalation", "decisions": append([]LearningDecision{}, e.learning.decisions...), "active_candidates": len(e.learning.entries)}
}
func shapeOf(text string) (string, map[string]any, bool) {
	var v any
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	if d.Decode(&v) != nil || !json.Valid([]byte(text)) {
		return "", nil, false
	}
	if _, ok := v.(map[string]any); !ok {
		return "", nil, false
	}
	shape := map[string]string{}
	values := map[string]any{}
	walk(v, "", shape, values)
	return event.Hash(shape), values, true
}

// ValidateProposal replays every required and normalized path against all samples.
// Only opaque ID fields can vary. Proposals never enter the active verdict cache.
func ValidateProposal(p provider.Proposal, samples []string) bool {
	if p.Validate() != nil || len(samples) < 2 {
		return false
	}
	var first string
	for _, s := range samples {
		shape, values, ok := shapeOf(s)
		if !ok {
			return false
		}
		if first == "" {
			first = shape
		} else if shape != first {
			return false
		}
		for _, path := range append(append([]string{}, p.Required...), p.Protected...) {
			if _, ok := values[path]; !ok {
				return false
			}
		}
		for _, path := range p.Normalize {
			parts := strings.Split(path, "/")
			name := parts[len(parts)-1]
			value, ok := values[path].(string)
			if !ok || protectedField(path) || !idField.MatchString(name) || !opaque.MatchString(value) {
				return false
			}
		}
	}
	return true
}
func Escalation(current event.Judgment, r provider.Review) *event.Judgment {
	if r.Validate() != nil || current.Importance == "important" || r.Importance != "important" || *r.Confidence < .75 || r.Category == "routine" || r.Category == "unknown" || r.Severity == "noise" || r.Severity == "unknown" || r.Explanation == "pattern_burst" || r.Explanation == "no_change" || r.Explanation == "free_text" || r.Explanation == "insufficient_evidence" {
		return nil
	}
	return &event.Judgment{Importance: r.Importance, Severity: r.Severity, Category: r.Category, ImportanceConfidence: r.Confidence, SeverityConfidence: r.Confidence, CategoryConfidence: r.Confidence}
}
func (e *Engine) teach(ctx context.Context, events []event.Event) {
	l := e.learning
	if l == nil || ctx.Err() != nil {
		return
	}
	now := time.Now()
	for k, v := range l.entries {
		if !now.Before(v.expires) {
			delete(l.entries, k)
		}
	}
	touched := []string{}
	seen := map[string]bool{}
	for i, ev := range events {
		if !Eligible(ev) {
			continue
		}
		key, version := "", ""
		if e.config.Strategy == "semantic" {
			shape, _, ok := shapeOf(ev.Text)
			if !ok {
				continue
			}
			key = event.Hash([]any{Scope(ev, true), shape, ev.Baseline.Important})
			version = shape
		} else {
			if ev.Assessment == nil || ev.Assessment.TemplateID == "" {
				continue
			}
			key = ev.Assessment.TemplateID
			version = ev.Assessment.VersionID
		}
		p := l.entries[key]
		if p == nil {
			if len(l.entries) >= l.capacity {
				continue
			}
			p = &learned{evidence: provider.TemplateEvidence{ScopeID: event.Hash(Scope(ev, true)), TemplateID: key, Samples: []string{}}, version: version, expires: now.Add(l.ttl)}
			l.entries[key] = p
		}
		if p.version != version {
			continue
		}
		p.evidence.Occurrences++
		p.evidence.Current = ev.Judgment
		duplicate := false
		for _, s := range p.evidence.Samples {
			duplicate = duplicate || s == ev.Text
		}
		if !duplicate && len(p.evidence.Samples) < 8 {
			p.evidence.Samples = append(p.evidence.Samples, ev.Text)
		}
		if p.escalation != nil && ev.Importance != "important" {
			events[i].Judgment = *p.escalation
		}
		if !seen[key] {
			seen[key] = true
			touched = append(touched, key)
		}
	}
	reviews := 0
	for _, key := range touched {
		p := l.entries[key]
		thresholds := []uint64{uint64(l.minSamples), 32, 256, 2048}
		if p.next >= len(thresholds) || p.evidence.Occurrences < thresholds[p.next] || reviews >= 2 {
			continue
		}
		p.next++
		reviews++
		decision := LearningDecision{TemplateID: key, VersionID: p.version, Seen: p.evidence.Occurrences, Outcome: "rejected"}
		e.Metrics["template_provider_attempts"]++
		if e.config.Strategy == "semantic" {
			proposal, err := l.teacher.Propose(ctx, p.evidence)
			if err == nil && proposal.Confidence != nil && *proposal.Confidence >= l.confidence && ValidateProposal(proposal, p.evidence.Samples) {
				decision.Proposal = &proposal
				decision.Outcome = "shadow_replay_passed"
				e.Metrics["semantic_replay_passed"]++
			} else {
				e.Metrics["semantic_rejections"]++
			}
		} else {
			review, err := l.teacher.Review(ctx, p.evidence)
			if err == nil && review.Validate() == nil {
				decision.Review = &review
				decision.Outcome = "reviewed_fields_require_artifact"
				if j := Escalation(p.evidence.Current, review); j != nil {
					p.escalation = j
					decision.Outcome = "escalated"
					e.Metrics["refine_escalations"]++
					for i := range events {
						if events[i].Assessment != nil && events[i].Assessment.TemplateID == key && events[i].Assessment.VersionID == p.version && Eligible(events[i]) && events[i].Importance != "important" {
							events[i].Judgment = *j
						}
					}
				}
			} else {
				e.Metrics["refine_rejections"]++
			}
		}
		if len(l.decisions) == 1000 {
			l.decisions = l.decisions[1:]
		}
		l.decisions = append(l.decisions, decision)
	}
}
