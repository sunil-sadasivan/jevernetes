package controller

import (
	"errors"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

type Policy struct {
	Categories    []string `json:"categories"`
	Severities    []string `json:"severities"`
	MinConfidence float64  `json:"min_confidence"`
	Cooldown      int64    `json:"cooldown_seconds"`
	Window        int64    `json:"recurrence_window_seconds"`
	Recurrence    int      `json:"recurrence_count"`
	MaxEscalation int      `json:"max_escalation"`
	Abstention    string   `json:"abstention"`
}

func DefaultPolicy() Policy {
	return Policy{[]string{"security", "fraud"}, []string{"degraded", "impact", "outage"}, 0.85, 300, 900, 5, 8, "review"}
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
func (p Policy) Validate() error {
	if len(p.Categories) == 0 || len(p.Categories) > 9 || len(p.Severities) == 0 || len(p.Severities) > 5 || !(p.MinConfidence >= 0 && p.MinConfidence <= 1) || p.Cooldown < 1 || p.Cooldown > 86400 || p.Window < 1 || p.Window > 604800 || p.Recurrence < 2 || p.Recurrence > 100000 || p.MaxEscalation < 1 || p.MaxEscalation > 32 || !contains([]string{"record", "review"}, p.Abstention) {
		return errors.New("invalid controller policy")
	}
	for _, v := range p.Categories {
		if !contains([]string{"deploy", "capacity", "dependency", "security", "fraud", "data", "config", "transient", "routine"}, v) {
			return errors.New("invalid policy category")
		}
	}
	for _, v := range p.Severities {
		if !contains([]string{"noise", "info", "degraded", "impact", "outage"}, v) {
			return errors.New("invalid policy severity")
		}
	}
	return nil
}
func (p Policy) Decide(e event.Event) string {
	review := "abstain"
	if p.Abstention == "review" {
		review = "review"
	}
	if e.ParseUncertain || e.Truncated || e.Sensitive || e.AnalysisError != "" || e.Importance == "unknown" || e.Importance == "uncertain" {
		return review
	}
	for _, c := range []*float64{e.ImportanceConfidence, e.SeverityConfidence, e.CategoryConfidence} {
		if c == nil || !(*c >= p.MinConfidence && *c <= 1) {
			return review
		}
	}
	if e.Importance == "important" && (contains(p.Categories, e.Category) || contains(p.Severities, e.Severity)) {
		return "notify"
	}
	return "ignore"
}
