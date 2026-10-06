package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

var ErrSchema = errors.New("provider returned invalid or incomplete typed response")

// Strict rejects unknown fields, duplicate keys, trailing values and excessive nesting.
func Strict(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := unique(d, 0); err != nil {
		return ErrSchema
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrSchema
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrSchema
	}
	return nil
}
func unique(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrSchema
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			t, e := d.Token()
			if e != nil {
				return e
			}
			k, ok := t.(string)
			if !ok || seen[k] {
				return ErrSchema
			}
			seen[k] = true
			if err := unique(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := unique(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrSchema
	}
	_, err = d.Token()
	return err
}
func member(s string, choices ...string) bool {
	for _, v := range choices {
		if v == s {
			return true
		}
	}
	return false
}
func Validate(j event.Judgment) error {
	if !member(j.Importance, "important", "routine", "uncertain") || !member(j.Severity, "noise", "info", "degraded", "impact", "outage") || !member(j.Category, "deploy", "capacity", "dependency", "security", "fraud", "data", "config", "transient", "routine", "unknown") || j.AnalysisError != "" {
		return ErrSchema
	}
	for _, c := range []*float64{j.ImportanceConfidence, j.SeverityConfidence, j.CategoryConfidence} {
		if c == nil || !(*c >= 0 && *c <= 1) {
			return ErrSchema
		}
	}
	return nil
}

type wireJudgment struct {
	Importance           string   `json:"importance"`
	Severity             string   `json:"severity"`
	Category             string   `json:"category"`
	ImportanceConfidence *float64 `json:"importance_confidence"`
	SeverityConfidence   *float64 `json:"severity_confidence"`
	CategoryConfidence   *float64 `json:"category_confidence"`
}

func decodeTyped(raw []byte, count int) ([]event.Judgment, error) {
	var w struct {
		Judgments []wireJudgment `json:"judgments"`
	}
	if Strict(raw, &w) != nil || len(w.Judgments) != count {
		return nil, ErrSchema
	}
	out := make([]event.Judgment, count)
	for i, j := range w.Judgments {
		out[i] = event.Judgment{Importance: j.Importance, Severity: j.Severity, Category: j.Category, ImportanceConfidence: j.ImportanceConfidence, SeverityConfidence: j.SeverityConfidence, CategoryConfidence: j.CategoryConfidence}
		if Validate(out[i]) != nil {
			return nil, ErrSchema
		}
	}
	return out, nil
}
func judgmentSchema() map[string]any {
	props := map[string]any{}
	for k, v := range map[string][]string{"importance": {"important", "routine", "uncertain"}, "severity": {"noise", "info", "degraded", "impact", "outage"}, "category": {"deploy", "capacity", "dependency", "security", "fraud", "data", "config", "transient", "routine", "unknown"}} {
		props[k] = map[string]any{"type": "string", "enum": v}
	}
	for _, k := range []string{"importance_confidence", "severity_confidence", "category_confidence"} {
		props[k] = map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"judgments"}, "properties": map[string]any{"judgments": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"importance", "severity", "category", "importance_confidence", "severity_confidence", "category_confidence"}, "properties": props}}}}
}

func Required(raw []byte, keys ...string) bool {
	var values map[string]json.RawMessage
	if Strict(raw, &values) != nil {
		return false
	}
	for _, k := range keys {
		v, ok := values[k]
		if !ok || string(v) == "null" {
			return false
		}
	}
	return true
}
