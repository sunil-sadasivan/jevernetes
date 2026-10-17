package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"github.com/sunil-sadasivan/jevernetes/internal/report"
	"github.com/sunil-sadasivan/jevernetes/internal/safefile"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"unicode"
)

type object = map[string]any

func str(m object, k string) string { s, _ := m[k].(string); return s }
func obj(m object, k string) object {
	v, _ := m[k].(map[string]any)
	if v == nil {
		return object{}
	}
	return v
}
func list(m object, k string) []any { v, _ := m[k].([]any); return v }
func clone(m object) object {
	raw, _ := json.Marshal(m)
	var v object
	_ = json.Unmarshal(raw, &v)
	return v
}
func number(m object, k string) float64 { v, _ := m[k].(float64); return v }
func validReport(v object) bool {
	schema := number(v, "schema_version")
	summary, summaryOK := v["summary"].(map[string]any)
	events, eventsOK := v["events"].([]any)
	if (schema != 1 && schema != 2) || !summaryOK || summary == nil || !eventsOK || len(events) > 100000 {
		return false
	}
	for _, key := range []string{"events", "tail_events"} {
		if raw, ok := v[key]; ok {
			items, ok := raw.([]any)
			if !ok || len(items) > 100000 {
				return false
			}
			for _, raw := range items {
				e, ok := raw.(map[string]any)
				if !ok || str(e, "id") == "" {
					return false
				}
				if _, ok := e["text"].(string); !ok {
					return false
				}
				if _, ok := e["source"].(map[string]any); !ok {
					return false
				}
			}
		}
	}
	return true
}
func (s *Server) rules() (object, error) {
	raw, err := safefile.ReadConfig(s.ctx, s.rulesPath, 16<<20)
	if errors.Is(err, os.ErrNotExist) {
		return object{"version": float64(1), "rules": []any{}, "acknowledged": object{}}, nil
	}
	if err != nil {
		if _, e := os.Lstat(s.rulesPath); os.IsNotExist(e) {
			return object{"version": float64(1), "rules": []any{}, "acknowledged": object{}}, nil
		}
		return nil, errors.New("review rules unavailable")
	}
	var v object
	if provider.Strict(raw, &v) != nil || number(v, "version") != 1 || v["rules"] == nil || v["acknowledged"] == nil {
		return nil, errors.New("invalid review rules")
	}
	rules, rulesOK := v["rules"].([]any)
	acks, acksOK := v["acknowledged"].(map[string]any)
	if !rulesOK || !acksOK {
		return nil, errors.New("invalid review rules")
	}
	for key, raw := range acks {
		ids, ok := raw.([]any)
		if !ok || len(key) > 4096 {
			return nil, errors.New("invalid acknowledgements")
		}
		for _, raw := range ids {
			if id, ok := raw.(string); !ok || id == "" || len(id) > 256 {
				return nil, errors.New("invalid acknowledgement identity")
			}
		}
	}
	identities := map[string]object{}
	for _, raw := range rules {
		r, ok := raw.(map[string]any)
		if !ok || str(r, "id") == "" || len([]rune(str(r, "id"))) > 256 || strings.IndexFunc(str(r, "id"), unicode.IsControl) >= 0 || str(r, "pattern") == "" || len(obj(r, "scope")) == 0 || len(obj(r, "scope")) > 32 {
			return nil, errors.New("invalid review rule")
		}
		if _, ok := r["enabled"].(bool); !ok {
			return nil, errors.New("invalid rule enabled state")
		}
		if len([]rune(str(r, "pattern"))) > 500 || strings.IndexFunc(str(r, "pattern"), unicode.IsControl) >= 0 {
			return nil, errors.New("invalid rule pattern")
		}
		match := str(r, "match")
		if match != "" && match != "exact" && match != "contains" {
			return nil, errors.New("invalid rule match")
		}
		for key, value := range obj(r, "scope") {
			if len(key) > 256 {
				return nil, errors.New("invalid literal source scope")
			}
			switch value := value.(type) {
			case string:
				if len([]rune(value)) > 4096 {
					return nil, errors.New("invalid literal source scope")
				}
			case bool:
			case float64:
				if value != math.Trunc(value) {
					return nil, errors.New("invalid literal source scope")
				}
			default:
				return nil, errors.New("invalid literal source scope")
			}
		}
		if prior := identities[str(r, "id")]; prior != nil && !reflect.DeepEqual(prior, r) {
			return nil, errors.New("conflicting duplicate rule identity")
		}
		identities[str(r, "id")] = r

	}
	return v, nil
}

var judgments = []string{"importance", "severity", "category", "importance_confidence", "severity_confidence", "category_confidence", "analysis_error"}

func project(v object, key string, rules object) object {
	v = clone(v)
	acks := map[string]bool{}
	for _, id := range list(obj(rules, "acknowledged"), key) {
		if v, ok := id.(string); ok {
			acks[v] = true
		}
	}
	for _, collection := range []string{"events", "tail_events"} {
		for _, raw := range list(v, collection) {
			e, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			before := str(e, "importance")
			if original, ok := e["original_judgment"].(map[string]any); ok {
				for _, k := range judgments {
					delete(e, k)
				}
				for k, value := range original {
					e[k] = value
				}
				delete(e, "original_judgment")
			}
			delete(e, "review")
			var matched object
			for _, rawRule := range list(rules, "rules") {
				r := rawRule.(map[string]any)
				if r["enabled"] != true {
					continue
				}
				pattern, text := str(r, "pattern"), str(e, "text")
				match := strings.Contains(text, pattern)
				if str(r, "match") == "exact" {
					match = strings.TrimSpace(text) == pattern
				}
				for k, value := range obj(r, "scope") {
					match = match && reflect.DeepEqual(obj(e, "source")[k], value)
				}
				if match {
					matched = r
					break
				}
			}
			if matched != nil || acks[str(e, "id")] {
				original := object{}
				for _, k := range judgments {
					if value, ok := e[k]; ok {
						original[k] = value
					}
				}
				e["original_judgment"] = original
				e["importance"] = "routine"
				for _, k := range judgments[3:] {
					delete(e, k)
				}
				review := object{"status": "acknowledged"}
				if matched != nil {
					review = object{"status": "expected", "rule_id": matched["id"]}
				}
				e["review"] = review
			}
			after := str(e, "importance")
			if collection == "events" && before != after {
				summary := obj(v, "summary")
				summary[before] = max(0, number(summary, before)-1)
				summary[after] = number(summary, after) + 1
			}
		}
	}
	groups := map[string]object{}
	for _, raw := range list(v, "events") {
		e, ok := raw.(map[string]any)
		if !ok || str(e, "importance") != "important" {
			continue
		}
		id := event.Hash([]any{e["source"], e["text"]})
		g := groups[id]
		if g == nil {
			g = object{"id": id, "source": e["source"], "text": e["text"], "severity": e["severity"], "category": e["category"], "count": float64(0), "event_ids": []any{}}
			groups[id] = g
		}
		g["count"] = number(g, "count") + 1
		g["event_ids"] = append(list(g, "event_ids"), e["id"])
	}
	ordered := []any{}
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i].(object), ordered[j].(object)
		if number(a, "count") != number(b, "count") {
			return number(a, "count") > number(b, "count")
		}
		return str(a, "id") < str(b, "id")
	})
	v["important_groups"] = ordered
	if number(obj(v, "summary"), "unknown") > 0 {
		obj(v, "summary")["complete_within_window"] = false
	}
	return v
}
func (s *Server) review(p object) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.rules()
	if err != nil {
		return err
	}
	action := str(p, "action")
	switch action {
	case "toggle":
		enabled, ok := p["enabled"].(bool)
		if !ok {
			return errors.New("invalid enabled value")
		}
		found := false
		for _, raw := range list(rules, "rules") {
			r := raw.(map[string]any)
			if str(r, "id") == str(p, "rule_id") {
				r["enabled"] = enabled
				found = true
			}
		}
		if !found {
			return errors.New("rule not found")
		}
	case "acknowledge", "unacknowledge", "expected":
		v, key, err := s.sourceLocked(p)
		if err != nil {
			return err
		}
		available := map[string]object{}
		for _, collection := range []string{"tail_events", "events"} {
			for _, raw := range list(v, collection) {
				if e, ok := raw.(map[string]any); ok {
					available[str(e, "id")] = e
				}
			}
		}
		entries := list(p, "events")
		if entries == nil {
			entries = []any{p}
		}
		limit := 100000
		if action == "expected" {
			limit = 50
		}
		if len(entries) < 1 || len(entries) > limit {
			return errors.New("invalid selection size")
		}
		selected := map[string]bool{}
		if ids := list(p, "selected_event_ids"); ids != nil {
			if len(ids) < 1 || len(ids) > 100000 {
				return errors.New("invalid selection")
			}
			for _, raw := range ids {
				id, ok := raw.(string)
				if !ok || available[id] == nil || selected[id] {
					return errors.New("selected event is unavailable")
				}
				selected[id] = true
			}
		}
		seen := map[string]bool{}
		for _, raw := range entries {
			item, ok := raw.(map[string]any)
			id := str(item, "event_id")
			e := available[id]
			if !ok || id == "" || e == nil || seen[id] || (len(selected) > 0 && !selected[id]) {
				return errors.New("selected event is unavailable or duplicated")
			}
			seen[id] = true
			if action == "expected" {
				pattern := strings.TrimSpace(str(item, "pattern"))
				if len([]rune(pattern)) < 1 || len([]rune(pattern)) > 500 || strings.IndexFunc(pattern, unicode.IsControl) >= 0 || !strings.Contains(str(e, "text"), pattern) {
					return errors.New("pattern must be literal text from the selected event")
				}
				match := "contains"
				if len([]rune(pattern)) < 8 {
					match = "exact"
					if strings.TrimSpace(str(e, "text")) != pattern {
						return errors.New("short pattern must match the entire event")
					}
				}
				scopeMode := str(p, "scope")
				if scopeMode == "" {
					scopeMode = "workload"
				}
				if scopeMode != "workload" && scopeMode != "source" {
					return errors.New("invalid rule scope")
				}
				src := obj(e, "source")
				keys := []string{"type", "path"}
				if str(src, "type") == "kubernetes" {
					keys = []string{"type", "context", "namespace", "container"}
					if scopeMode == "source" {
						keys = append(keys, "pod")
					}
				}
				scope := object{}
				for _, k := range keys {
					if value, ok := src[k]; ok {
						scope[k] = value
					}
				}
				found := false
				for _, rawRule := range list(rules, "rules") {
					r := rawRule.(map[string]any)
					if str(r, "pattern") == pattern && str(r, "match") == match && reflect.DeepEqual(obj(r, "scope"), scope) {
						r["enabled"] = true
						found = true
						break
					}
				}
				if !found {
					rules["rules"] = append(list(rules, "rules"), object{"id": event.Hash([]any{pattern, scope, match})[:16], "pattern": pattern, "scope": scope, "match": match, "enabled": true})
				}
			}
		}
		if action != "expected" {
			acks := obj(rules, "acknowledged")
			ids := map[string]bool{}
			for _, raw := range list(acks, key) {
				if id, ok := raw.(string); ok {
					ids[id] = true
				}
			}
			for id := range seen {
				if action == "acknowledge" {
					ids[id] = true
				} else {
					delete(ids, id)
				}
			}
			sorted := []string{}
			for id := range ids {
				sorted = append(sorted, id)
			}
			sort.Strings(sorted)
			acks[key] = sorted
			rules["acknowledged"] = acks
		}
	default:
		return errors.New("unknown review action")
	}
	err = report.WriteBounded(s.rulesPath, rules, 16<<20)
	if errors.Is(err, report.ErrSizeLimit) {
		return errors.New("review storage limit reached")
	}
	return err
}
func readReport(ctx context.Context, path string) (object, error) {
	raw, err := safefile.ReadConfig(ctx, path, 64<<20)
	if err != nil {
		return nil, errors.New("report unavailable")
	}
	var v object
	if provider.Strict(raw, &v) != nil || !validReport(v) {
		return nil, errors.New("invalid report")
	}
	return normalize(v), nil
}
func normalize(v object) object {
	if v["coverage"] == nil {
		v["coverage"] = []any{}
	}
	if v["important_groups"] == nil {
		v["important_groups"] = []any{}
	}
	summary := obj(v, "summary")
	for _, k := range []string{"events", "lines", "important", "routine", "uncertain", "unknown", "streams", "coverage_gaps", "api_requests", "reused_events"} {
		if summary[k] == nil {
			summary[k] = float64(0)
		}
	}
	if str(v, "mode") == "typesafe" {
		v["mode"] = "jev"
	}
	if u := obj(v, "usage"); len(u) > 0 {
		if u["input_usd_per_million"] == nil {
			u["input_usd_per_million"] = v["input_price"]
			if u["input_usd_per_million"] == nil {
				u["input_usd_per_million"] = "unknown"
			}
		}
		if u["output_usd_per_million"] == nil {
			u["output_usd_per_million"] = v["output_price"]
			if u["output_usd_per_million"] == nil {
				u["output_usd_per_million"] = "unknown"
			}
		}
		u["in_flight"] = max(0, number(u, "request_attempts")-number(u, "requests_finished"))
	}
	return v
}
