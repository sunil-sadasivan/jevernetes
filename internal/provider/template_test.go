package provider

import (
	"encoding/json"
	"testing"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

func TestProposalAndReviewSchemas(t *testing.T) {
	p := Proposal{Name: "synthetic", Version: 1, Required: []string{"/request_id"}, Normalize: []string{"/request_id"}, Confidence: event.Confidence(.9), Explanation: "opaque_identifiers"}
	if p.Validate() != nil {
		t.Fatal("valid proposal")
	}
	p.Normalize = []string{"/request_id", "/request_id"}
	if p.Validate() == nil {
		t.Fatal("duplicate path")
	}
	r := Review{Fields: []string{}, Importance: "routine", Severity: "info", Category: "routine", Confidence: event.Confidence(.9), Explanation: "no_change"}
	if r.Validate() != nil {
		t.Fatal("valid review")
	}
	r.Confidence = event.Confidence(2)
	if r.Validate() == nil {
		t.Fatal("invalid confidence")
	}
}
func TestTemplateSchemaDeterministic(t *testing.T) {
	props := map[string]any{"z": 1, "a": 2}
	a, _ := json.Marshal(objectSchema(props))
	for i := 0; i < 50; i++ {
		b, _ := json.Marshal(objectSchema(props))
		if string(a) != string(b) {
			t.Fatal("nondeterministic schema")
		}
	}
}
