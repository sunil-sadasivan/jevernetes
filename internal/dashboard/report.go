package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

// ReportView is an immutable index of the complete retained report. Only pages,
// not duplicate important_groups/coverage/learning payloads, reach the browser.
type ReportView struct {
	schema   int
	summary  json.RawMessage
	events   []json.RawMessage
	texts    []string
	complete bool
}

func LoadReport(raw []byte) (*ReportView, error) {
	var value map[string]json.RawMessage
	if len(raw) > 64<<20 || provider.Strict(raw, &value) != nil {
		return nil, errors.New("invalid report")
	}
	v := &ReportView{}
	if json.Unmarshal(value["schema_version"], &v.schema) != nil || (v.schema != 1 && v.schema != 2) {
		return nil, errors.New("dashboard requires schema-1 or schema-2 report")
	}
	var summary map[string]any
	if json.Unmarshal(value["summary"], &summary) != nil || summary == nil || len(value["summary"]) > 32<<10 {
		return nil, errors.New("invalid or oversized report summary")
	}
	v.summary, _ = json.Marshal(summary)
	if len(v.summary) > 32<<10 {
		return nil, errors.New("escaped report summary exceeds limit")
	}
	v.complete, _ = summary["complete_within_window"].(bool)
	v.complete = v.schema == 2 && v.complete
	if json.Unmarshal(value["events"], &v.events) != nil || v.events == nil || len(v.events) > 100000 {
		return nil, errors.New("invalid report events")
	}
	for _, raw := range v.events {
		var e struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal(raw, &e) != nil || e.Text == nil {
			return nil, errors.New("invalid report event")
		}
		v.texts = append(v.texts, strings.ToLower(*e.Text))
	}
	return v, nil
}
func (v *ReportView) page(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 2048 {
		inspect.JSON(w, 400, map[string]string{"error": "invalid report query"})
		return
	}
	for key, values := range q {
		if (key != "offset" && key != "limit" && key != "q") || len(values) != 1 {
			inspect.JSON(w, 400, map[string]string{"error": "invalid report query"})
			return
		}
	}
	offset, limit := 0, 50
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || offset < 0 || offset > 100000 {
			inspect.JSON(w, 400, map[string]string{"error": "invalid offset"})
			return
		}
	}
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 200 {
			inspect.JSON(w, 400, map[string]string{"error": "invalid limit"})
			return
		}
	}
	query := strings.ToLower(q.Get("q"))
	if len(query) > 1024 {
		inspect.JSON(w, 400, map[string]string{"error": "query too long"})
		return
	}
	matches := []int{}
	if v != nil {
		for i, text := range v.texts {
			if strings.Contains(text, query) {
				matches = append(matches, i)
			}
		}
	}
	if offset > len(matches) {
		inspect.JSON(w, 400, map[string]string{"error": "offset beyond matched events"})
		return
	}
	page := []json.RawMessage{}
	used := 0
	next := offset
	unavailable := 0
	// Reserve 40 KiB for summary and pagination metadata. Marshal each event to
	// measure escaped JSON bytes, including HTML escaping, before admission.
	for next < len(matches) && len(page) < limit {
		raw, _ := json.Marshal(v.events[matches[next]])
		oversized := false
		if len(raw) > inspect.MaxResponse-(40<<10) {
			raw, _ = json.Marshal(map[string]any{"text": "Event exceeds dashboard page limit; inspect the original report", "importance": "unknown", "event_unavailable": true, "report_index": matches[next]})
			oversized = true
		}
		if used+len(raw)+1 > inspect.MaxResponse-(40<<10) {
			break
		}
		if oversized {
			unavailable++
		}
		page = append(page, raw)
		used += len(raw) + 1
		next++
	}
	var cursor any
	if next < len(matches) {
		cursor = next
	}
	summary := json.RawMessage(`{"events":0}`)
	schema, retained := 2, 0
	complete := false
	if v != nil {
		summary = v.summary
		schema = v.schema
		retained = len(v.events)
		complete = v.complete
	}
	inspect.JSON(w, 200, map[string]any{"schema_version": schema, "summary": summary, "events": page, "page": map[string]any{
		"offset": offset, "next_offset": cursor, "matched_events": len(matches), "retained_events": retained, "returned_events": len(page), "unavailable_events": unavailable,
		"all_matches_in_page": offset == 0 && next == len(matches) && unavailable == 0, "coverage_complete": complete,
		"scope": "retained report events; full metadata remains in the original report",
	}})
}
