package analysis

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type Rule struct {
	ID        string            `json:"id"`
	Version   int               `json:"version"`
	Expires   int64             `json:"expires_at"`
	Scope     event.Source      `json:"source_scope"`
	Shape     map[string]string `json:"shape"`
	Literals  map[string]any    `json:"required_literals"`
	Normalize []string          `json:"normalize_paths"`
	Protected map[string]any    `json:"protected_literals"`
	Review    struct {
		Reviewer string `json:"reviewer"`
		ID       string `json:"review_id"`
		Compiler string `json:"compiler"`
		At       int64  `json:"reviewed_at"`
	} `json:"review"`
	Prefix struct {
		Text  string `json:"text"`
		Clock string `json:"clock_grammar"`
	} `json:"prefix_identity"`
}
type Rules struct {
	Artifact int    `json:"artifact_version"`
	Schema   int    `json:"schema_version"`
	Rules    []Rule `json:"rules"`
}

func LoadRules(raw []byte, now time.Time) (*Rules, error) {
	var r Rules
	if len(raw) > 1<<20 || provider.Strict(raw, &r) != nil || r.Artifact != 2 || r.Schema != 1 || len(r.Rules) > 256 {
		return nil, errors.New("invalid reviewed rules")
	}
	ids := map[string]bool{}
	for _, rule := range r.Rules {
		if rule.ID == "" || len(rule.ID) > 128 || ids[rule.ID] || rule.Version < 1 || rule.Expires <= now.Unix() || rule.Review.At <= 0 || rule.Review.At > now.Unix() || rule.Review.Compiler == "" || rule.Review.Reviewer == "" || rule.Review.ID == "" || len(rule.Scope) == 0 || len(rule.Shape) == 0 || len(rule.Normalize) > 64 {
			return nil, errors.New("invalid or expired reviewed rule")
		}
		ids[rule.ID] = true
		if rule.Prefix.Clock != "" {
			return nil, errors.New("reviewed logger-clock prefix normalization is unsupported")
		}
		seenPaths := map[string]bool{}
		for _, p := range rule.Normalize {
			if seenPaths[p] || len(p) > 128 {
				return nil, errors.New("invalid reviewed paths")
			}
			seenPaths[p] = true
			if !strings.HasPrefix(p, "/") || protectedField(p) || rule.Shape[p] != "string" {
				return nil, errors.New("unsafe reviewed normalization")
			}
			if _, ok := rule.Protected[p]; ok {
				return nil, errors.New("unsafe reviewed normalization")
			}
			if _, ok := rule.Literals[p]; ok {
				return nil, errors.New("unsafe reviewed normalization")
			}
		}
	}
	return &r, nil
}
func walk(v any, path string, shape map[string]string, values map[string]any) {
	values[path] = v
	switch x := v.(type) {
	case map[string]any:
		shape[path] = "object"
		for k, v := range x {
			walk(v, path+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), shape, values)
		}
	case []any:
		shape[path] = "array"
		for i, v := range x {
			walk(v, fmt.Sprintf("%s/%d", path, i), shape, values)
		}
	case string:
		shape[path] = "string"
	case float64, json.Number:
		shape[path] = "number"
	case bool:
		shape[path] = "boolean"
	case nil:
		shape[path] = "null"
	}
}
func (r *Rules) Template(e event.Event, now time.Time) (string, bool) {
	if !Eligible(e) {
		return "", false
	}
	for _, rule := range r.Rules {
		if now.Unix() >= rule.Expires {
			continue
		}
		matches := true
		for k, v := range rule.Scope {
			if event.Hash(e.Source[k]) != event.Hash(v) {
				matches = false
			}
		}
		if !matches || !strings.HasPrefix(e.Text, rule.Prefix.Text) {
			continue
		}
		payload := strings.TrimPrefix(e.Text, rule.Prefix.Text)
		if !json.Valid([]byte(payload)) {
			continue
		}
		var v any
		if provider.Strict([]byte(payload), &v) != nil {
			continue
		}
		d := json.NewDecoder(strings.NewReader(payload))
		d.UseNumber()
		if d.Decode(&v) != nil {
			continue
		}
		shape := map[string]string{}
		values := map[string]any{}
		walk(v, "", shape, values)
		if event.Hash(shape) != event.Hash(rule.Shape) {
			continue
		}
		for k, l := range rule.Literals {
			if event.Hash(values[k]) != event.Hash(l) {
				matches = false
			}
		}
		for k, l := range rule.Protected {
			if event.Hash(values[k]) != event.Hash(l) {
				matches = false
			}
		}
		if !matches {
			continue
		} // Canonical flattened leaf map preserves every non-normalized value.
		leaves := map[string]any{}
		for k, v := range values {
			if shape[k] != "object" && shape[k] != "array" {
				leaves[k] = v
			}
		}
		seenPaths := map[string]bool{}
		for _, p := range rule.Normalize {
			if seenPaths[p] || len(p) > 128 {
				return "", false
			}
			seenPaths[p] = true
			leaves[p] = "<reviewed>"
		}
		return event.Hash([]any{rule.ID, rule.Version, rule.Scope, shape, leaves}), true
	}
	return "", false
}
