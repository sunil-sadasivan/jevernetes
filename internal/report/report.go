// Package report retains bounded events and atomically writes private schema-2 reports.
package report

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type Coverage struct {
	Source   event.Source `json:"source"`
	Status   string       `json:"status"`
	Warnings []string     `json:"warnings"`
}
type Report struct {
	mu                                           sync.Mutex
	events                                       []event.Event
	limit                                        int
	total, lines, reused, evicted, gaps, omitted uint64
	counts                                       map[string]uint64
	losses                                       map[string]uint64
	coverage                                     []Coverage
	started                                      time.Time
}

func New(limit int) *Report {
	return &Report{limit: max(1, limit), counts: map[string]uint64{}, losses: map[string]uint64{}, events: []event.Event{}, coverage: []Coverage{}, started: time.Now()}
}
func (r *Report) Add(e event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total++
	if e.Truncated {
		r.losses["truncated_events"]++
	}
	if e.ParseUncertain {
		r.losses["parse_uncertain"]++
	}
	r.lines += e.LineCount
	r.counts[e.Importance]++
	if e.AnalysisReused {
		r.reused++
	}
	if len(r.events) == r.limit {
		copy(r.events, r.events[1:])
		r.events[len(r.events)-1] = e
		r.evicted++
	} else {
		r.events = append(r.events, e)
	}
}
func (r *Report) Gap(source event.Source, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gaps++
	r.losses["coverage_"+status]++
	if len(r.coverage) == 500 {
		r.coverage = r.coverage[1:]
		r.omitted++
	}
	r.coverage = append(r.coverage, Coverage{event.SafeSource(source), status, []string{"collection incomplete"}})
}
func (r *Report) Snapshot(mode string, scope any, live bool, usage provider.Usage, metrics map[string]uint64, checkpoint any) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := append([]event.Event{}, r.events...)
	coverage := append([]Coverage{}, r.coverage...)
	m := map[string]uint64{}
	for k, v := range metrics {
		m[k] = v
	}
	for k, v := range r.losses {
		m[k] = max(m[k], v)
	}
	m["evicted_events"] = max(m["evicted_events"], r.evicted)
	m["coverage_gaps"] = max(m["coverage_gaps"], r.gaps)
	m["coverage_records_omitted"] = max(m["coverage_records_omitted"], r.omitted)
	type group struct {
		ID       string       `json:"id"`
		Source   event.Source `json:"source"`
		Text     string       `json:"text"`
		Severity string       `json:"severity"`
		Category string       `json:"category"`
		Count    int          `json:"count"`
		IDs      []string     `json:"event_ids"`
	}
	byID := map[string]*group{}
	for _, e := range events {
		if e.Importance != "important" {
			continue
		}
		g := byID[e.GroupID]
		if g == nil {
			g = &group{ID: e.GroupID, Source: e.Source, Text: e.Text, Severity: e.Severity, Category: e.Category, IDs: []string{}}
			byID[e.GroupID] = g
		}
		g.Count++
		g.IDs = append(g.IDs, e.ID)
	}
	groups := []*group{}
	for _, g := range byID {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].ID < groups[j].ID
	})
	complete := Complete(live, m) && r.counts["unknown"] == 0
	return map[string]any{"schema_version": 2, "runtime": "go", "created_at": time.Now().UTC().Format(time.RFC3339), "mode": mode, "scope": scope, "summary": map[string]any{"events": r.total, "lines": r.lines, "important": r.counts["important"], "routine": r.counts["routine"], "uncertain": r.counts["uncertain"], "unknown": r.counts["unknown"], "coverage_gaps": r.gaps, "api_requests": usage.Attempts, "total_api_requests": usage.Attempts, "total_estimated_cost_usd": usage.Cost, "reused_events": r.reused, "elapsed_seconds": time.Since(r.started).Seconds(), "complete_within_window": complete, "retained_events": len(events), "evicted_events": r.evicted}, "provider_usage": map[string]any{"risk": usage, "template": nil}, "usage": usage, "metrics": m, "events": events, "coverage": coverage, "important_groups": groups, "adaptive_learning": checkpoint, "template_learning": nil, "template_refinement": nil, "reviewed_template_rules": nil, "risk_contract": nil, "batches": m["provider_batches"]}
}
func Write(path string, value any) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return errors.New("cannot create report directory")
	}
	f, err := os.CreateTemp(parent, ".jevernetes-*")
	if err != nil {
		return errors.New("cannot create private report")
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return errors.New("cannot secure report")
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if enc.Encode(value) != nil || f.Sync() != nil || f.Close() != nil {
		return errors.New("cannot write report")
	}
	if os.Rename(name, path) != nil {
		return errors.New("cannot replace report")
	}
	dir, err := os.Open(parent)
	if err != nil {
		return errors.New("cannot sync report directory")
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return errors.New("cannot sync report directory")
	}
	return nil
}

// Complete derives coverage from cumulative counters, never retained samples.
// All coverage_* indicators are loss signals, including future collector gaps.
func Complete(live bool, metrics map[string]uint64) bool {
	if live {
		return false
	}
	for k, v := range metrics {
		if v == 0 {
			continue
		}
		if strings.HasPrefix(k, "coverage_") || strings.HasPrefix(k, "state_") && strings.HasSuffix(k, "_evicted") {
			return false
		}
		switch k {
		case "dropped", "truncated_events", "parse_uncertain", "evicted_events", "budget_stopped", "outbox_backpressure":
			return false
		}
	}
	return true
}
