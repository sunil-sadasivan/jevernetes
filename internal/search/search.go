// Package search evaluates a frozen retained window with independent usage bounds.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Client interface {
	Search(context.Context, string, []event.Event) ([]provider.Relevance, error)
	Usage() provider.Usage
}
type entry struct {
	at    time.Time
	value provider.Relevance
}
type Cache struct {
	mu      sync.Mutex
	entries map[string]entry
}

func (c *Cache) get(key string) (provider.Relevance, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if ok && time.Since(e.at) < 5*time.Minute {
		return e.value, true
	}
	delete(c.entries, key)
	return provider.Relevance{}, false
}
func (c *Cache) put(key string, v provider.Relevance) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]entry{}
	}
	if len(c.entries) >= 2048 {
		old := ""
		var at time.Time
		for k, e := range c.entries {
			if old == "" || e.at.Before(at) {
				old, at = k, e.at
			}
		}
		delete(c.entries, old)
	}
	c.entries[key] = entry{time.Now(), v}
}

type Result struct {
	GroupID string `json:"group_id"`
	EventID string `json:"event_id"`
	Count   int    `json:"count"`
	Cached  bool   `json:"cached"`
	provider.Relevance
}
type Run struct {
	mu                                sync.Mutex
	ID, Query, Mode                   string
	Events                            []event.Event
	groups                            []event.Event
	results                           []Result
	status, message                   string
	checked, failed, hits, candidates int
	started, finished                 time.Time
	usage                             provider.Usage
	cache                             *Cache
	budget                            int
}

func New(events []event.Event, query, mode string, budget int, cache *Cache) (*Run, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 1 || len([]rune(query)) > 500 || strings.IndexFunc(query, unicode.IsControl) >= 0 || (mode != "literal" && mode != "jev") || budget < 1 || budget > 64 || len(events) > 100000 {
		return nil, errors.New("invalid search options")
	}
	query, _ = event.Redact(query)
	if cache == nil {
		cache = &Cache{}
	}
	r := &Run{ID: event.Hash([]any{time.Now().UnixNano(), query}), Query: query, Mode: mode, Events: append([]event.Event{}, events...), status: "running", message: "Searching the frozen log window…", started: time.Now(), cache: cache, budget: budget, usage: provider.Usage{Complete: true}}
	by := map[string]int{}
	seen := map[string]bool{}
	for _, e := range r.Events {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		e.Text, _ = event.Redact(e.Text)
		key := event.Hash([]any{e.Source, e.Text, e.Truncated, e.Sensitive, e.ParseUncertain})
		i, ok := by[key]
		if !ok {
			i = len(r.groups)
			by[key] = i
			e.GroupID = key
			e.SearchOccurrences = &event.Occurrences{}
			r.groups = append(r.groups, e)
		}
		occ := r.groups[i].SearchOccurrences
		occ.Count++
		if e.Timestamp != nil {
			if occ.First == nil || *e.Timestamp < *occ.First {
				v := *e.Timestamp
				occ.First = &v
			}
			if occ.Last == nil || *e.Timestamp > *occ.Last {
				v := *e.Timestamp
				occ.Last = &v
			}
		}
	}
	r.candidates = len(r.groups)
	return r, nil
}
func (r *Run) key(e event.Event) string {
	return event.Hash([]any{"go-search-v1", "jev-latest", r.Query, e.GroupID, e.SearchOccurrences})
}
func (r *Run) record(e event.Event, v provider.Relevance, cached bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checked++
	if cached {
		r.hits++
	}
	if v.Relevance == "unknown" {
		r.failed++
	}
	if v.Relevance != "unrelated" {
		r.results = append(r.results, Result{e.GroupID, e.ID, e.SearchOccurrences.Count, cached, v})
	}
}
func (r *Run) Execute(ctx context.Context, client Client) {
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.finished = time.Now()
		r.status = "complete"
		r.message = "Finished searching the frozen log window."
		if r.checked < len(r.groups) || r.failed > 0 {
			r.message = "Partial search: some groups were not evaluated."
		}
		if ctx.Err() != nil {
			r.status = "cancelled"
			r.message = "Search stopped; completed results preserved."
		}
		if client != nil {
			r.usage = client.Usage()
		}
	}()
	pending := []event.Event{}
	for _, e := range r.groups {
		if ctx.Err() != nil {
			return
		}
		if r.Mode == "literal" {
			v := "unrelated"
			if strings.Contains(strings.ToLower(e.Text), strings.ToLower(r.Query)) {
				v = "match"
			}
			r.record(e, provider.Relevance{Relevance: v}, false)
		} else if v, ok := r.cache.get(r.key(e)); ok {
			r.record(e, v, true)
		} else {
			pending = append(pending, e)
		}
	}
	if r.Mode == "literal" {
		return
	}
	sort.SliceStable(pending, func(i, j int) bool {
		a, _ := json.Marshal(pending[i].Source)
		b, _ := json.Marshal(pending[j].Source)
		return rank(pending[i].Text+string(a), r.Query) > rank(pending[j].Text+string(b), r.Query)
	})
	pending = pending[:min(len(pending), r.budget*8)]
	r.mu.Lock()
	r.candidates = r.checked + len(pending)
	r.mu.Unlock()
	for len(pending) > 0 && ctx.Err() == nil {
		batch := pending[:min(8, len(pending))]
		pending = pending[len(batch):]
		var values []provider.Relevance
		var err error
		if client == nil {
			err = errors.New("semantic search credentials unavailable")
		} else {
			values, err = client.Search(ctx, r.Query, batch)
		}
		for i, e := range batch {
			v := provider.Relevance{Relevance: "unknown", Error: "Search could not evaluate this evidence"}
			if err == nil && len(values) == len(batch) {
				v = values[i]
			}
			if v.Relevance != "unknown" {
				r.cache.put(r.key(e), v)
			}
			r.record(e, v, false)
		}
		if client != nil {
			u := client.Usage()
			r.mu.Lock()
			r.usage = u
			r.mu.Unlock()
			if u.Breaker != "" {
				return
			}
		}
	}
}

var words = regexp.MustCompile(`[a-z0-9_]+`)

func rank(text, query string) int {
	stop := map[string]bool{}
	for _, word := range strings.Fields("a an the me find show get all logs log lines line events event requests request against from for to with ip address major issue issues any are there what which is in of and") {
		stop[word] = true
	}
	terms := map[string]bool{}
	for _, word := range words.FindAllString(strings.ToLower(query), -1) {
		if !stop[word] {
			terms[word] = true
		}
	}
	for _, alias := range []string{"db database postgres postgresql mysql sql mongo mongodb redis", "error errors failure failed failing unavailable exhausted deadlock timeout timeouts issue issues"} {
		matched := false
		for _, word := range strings.Fields(alias) {
			matched = matched || terms[word]
		}
		if matched {
			for _, word := range strings.Fields(alias) {
				terms[word] = true
			}
		}
	}
	score := 0
	text = strings.ToLower(text)
	for term := range terms {
		if strings.Contains(text, term) {
			score++
		}
	}
	return score
}

func (r *Run) Snapshot(offset int) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	results := append([]Result{}, r.results...)
	rank := map[string]int{"match": 0, "possible": 1, "unknown": 2}
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if rank[a.Relevance.Relevance] != rank[b.Relevance.Relevance] {
			return rank[a.Relevance.Relevance] < rank[b.Relevance.Relevance]
		}
		if a.Confidence != nil && b.Confidence != nil && *a.Confidence != *b.Confidence {
			return *a.Confidence > *b.Confidence
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.GroupID < b.GroupID
	})
	matched, possible, instances := 0, 0, 0
	for _, v := range results {
		switch v.Relevance.Relevance {
		case "match":
			matched++
			instances += v.Count
		case "possible":
			possible++
		}
	}
	offset = max(0, min(offset, len(results)))
	end := time.Now()
	if !r.finished.IsZero() {
		end = r.finished
	}
	return map[string]any{"id": r.ID, "query": r.Query, "mode": r.Mode, "status": r.status, "message": r.message, "total_events": len(r.Events), "total_groups": len(r.groups), "candidate_groups": r.candidates, "examined_groups": r.checked, "unexamined_groups": len(r.groups) - r.checked, "failed_groups": r.failed, "cache_hits": r.hits, "matched_groups": matched, "possible_groups": possible, "matching_instances": instances, "partial": r.checked < len(r.groups) || r.failed > 0, "usage": r.usage, "max_cost_usd": .01, "elapsed_seconds": end.Sub(r.started).Seconds(), "result_count": len(results), "offset": offset, "results": results[offset:min(offset+20, len(results))]}
}
