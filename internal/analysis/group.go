// Package analysis owns bounded template state in a single analysis lane.
package analysis

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

var protected = regexp.MustCompile(`(?i)status|code|level|error|auth|role|security|fraud|permission|success|fail|result|outcome|severity|payment|amount|balance|backup|bytes|count|denied|permit|privilege|access|allow|grant|acl|response|password|secret|token|credential|cookie|deny|category|importance|operation|messagekind|duration|latency|identity|tenant|username`)

func protectedField(s string) bool {
	return protected.MatchString(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s))
}

var opaque = regexp.MustCompile(`^(?:[0-9a-fA-F]{16,64}|[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12})$`)
var idField = regexp.MustCompile(`(?i)^(?:request[_-]?id|trace[_-]?id|span[_-]?id|correlation[_-]?id|session[_-]?id)$`)
var number = regexp.MustCompile(`^[+-]?\d+(?:\.\d+)?$`)
var ip = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)
var clock = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}(?:\.\d+)?$`)

func Eligible(e event.Event) bool {
	return !e.ParseUncertain && !e.Truncated && !e.Sensitive && !event.SecurityShaped(e.Text) && len(e.Text) <= 2048 && !strings.Contains(e.Text, "\n") && len(strings.Fields(e.Text)) <= 128
}
func Scope(e event.Event, drain bool) event.Source {
	if !drain || e.Source["type"] != "kubernetes" {
		return e.Source
	}
	s := event.Source{}
	for _, k := range []string{"type", "context", "namespace", "container", "kind", "previous", "restart_count"} {
		if v, ok := e.Source[k]; ok {
			s[k] = v
		}
	}
	return s
}
func maskedValue(key string, v any, classic bool) any {
	if protectedField(key) {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			x[k] = maskedValue(k, v, classic)
		}
		return x
	case []any: // Preserve arrays rather than generalizing their element relationships.
		return x
	case string:
		if idField.MatchString(key) && opaque.MatchString(x) {
			return "<id>"
		}
		if classic && (number.MatchString(x) || ip.MatchString(x) || clock.MatchString(x)) {
			return "<value>"
		}
	case json.Number:
		if classic {
			return "<number>"
		}
	}
	return v
}
func Template(text string, classic bool) string {
	var obj map[string]any
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	if d.Decode(&obj) == nil && obj != nil && json.Valid([]byte(text)) {
		b, _ := json.Marshal(maskedValue("", obj, classic))
		return string(b)
	}
	tokens := strings.Fields(text)
	for i, t := range tokens {
		if k, v, ok := strings.Cut(t, "="); ok {
			if protectedField(k) {
				continue
			}
			if idField.MatchString(k) && opaque.MatchString(v) {
				tokens[i] = k + "=<id>"
			} else if classic && (number.MatchString(v) || ip.MatchString(v) || clock.MatchString(v)) {
				tokens[i] = k + "=<value>"
			}
			continue
		}
		if classic { // Positional HTTP status and adjacent protected words remain literal.
			if i > 0 && (protectedField(tokens[i-1]) || strings.HasPrefix(tokens[i-1], "HTTP/") || tokens[i-1] == "Completed") {
				continue
			}
			if len(t) == 3 && t[0] >= '1' && t[0] <= '5' && number.MatchString(t) {
				continue
			}
			if number.MatchString(t) || ip.MatchString(t) || clock.MatchString(t) {
				tokens[i] = "<value>"
			}
		}
	}
	return strings.Join(tokens, " ")
}

type Ticket struct {
	Key, TemplateID, VersionID string
	Generation                 uint64
}

func (t Ticket) Compatible(o Ticket) bool { return t == o }

type partition struct {
	ticket         Ticket
	seen           uint64
	verdict        event.Judgment
	expires        time.Time
	representative string
	last           uint64
	auditAt        uint64
	interval       uint64
}
type Grouper struct {
	strategy, masking    string
	capacity             int
	ttl                  time.Duration
	entries              map[string]*partition
	generation, sequence uint64
	adaptive             bool
	maxInterval          uint64
	review               map[string]bool
	rules                *Rules
}

func NewGrouper(strategy, masking string, capacity int, ttl time.Duration, adaptive bool, maxInterval uint64) *Grouper {
	return &Grouper{strategy: strategy, masking: masking, capacity: capacity, ttl: ttl, entries: map[string]*partition{}, adaptive: adaptive, maxInterval: maxInterval, review: map[string]bool{}}
}
func (g *Grouper) Veto(ids []string) {
	for _, id := range ids {
		g.review[id] = true
	}
}
func (g *Grouper) Observe(e event.Event, now time.Time) (Ticket, *event.Judgment, string) {
	if g.strategy == "off" || !Eligible(e) {
		return Ticket{}, nil, "ineligible"
	}
	template := e.Text
	if g.strategy == "semantic" {
		if g.rules == nil {
			return Ticket{}, nil, "shadow_only"
		}
		t, ok := g.rules.Template(e, now)
		if !ok {
			return Ticket{}, nil, "reviewed_miss"
		}
		template = t
	}
	scope := Scope(e, g.strategy == "drain")
	if g.strategy == "drain" {
		template = Template(e.Text, g.masking == "classic")
	}
	if g.rules != nil {
		if t, ok := g.rules.Template(e, now); ok {
			template = t
			scope = e.Source
		}
	}
	key := event.Hash([]any{scope, template, e.Baseline.Important})
	g.sequence++
	p := g.entries[key]
	if p == nil {
		if len(g.entries) >= g.capacity {
			var oldest string
			minSeq := ^uint64(0)
			for k, p := range g.entries {
				if p.last < minSeq {
					oldest = k
					minSeq = p.last
				}
			}
			delete(g.entries, oldest)
		}
		g.generation++
		t := Ticket{Key: key, TemplateID: event.Hash([]any{key, g.generation}), VersionID: event.Hash([]any{template, g.generation}), Generation: g.generation}
		p = &partition{ticket: t, interval: 16}
		g.entries[key] = p
	}
	p.last = g.sequence
	p.seen++
	if g.review[p.ticket.TemplateID] {
		p.verdict = event.Unknown("")
		return p.ticket, nil, "review_required"
	}
	if p.verdict.Reusable() && now.Before(p.expires) {
		if !g.adaptive || p.seen < p.auditAt {
			j := p.verdict
			return p.ticket, &j, "cached"
		}
		p.verdict = event.Unknown("")
		return p.ticket, nil, "stable_audit"
	}
	return p.ticket, nil, "new_or_expired"
}
func (g *Grouper) Publish(t Ticket, j event.Judgment, representative string, now time.Time) bool {
	p := g.entries[t.Key]
	if p == nil || p.ticket != t || g.review[t.TemplateID] {
		return false
	}
	p.verdict = event.Unknown("")
	if !j.Reusable() {
		p.interval = 16
		return false
	}
	p.verdict = j
	p.representative = representative
	p.expires = now.Add(g.ttl)
	if j.Importance == "routine" {
		p.interval = min(g.maxInterval, max(uint64(32), p.interval*2))
	} else {
		p.interval = 32
	}
	p.auditAt = p.seen + p.interval
	return true
}
func (g *Grouper) Current(t Ticket) bool { p := g.entries[t.Key]; return p != nil && p.ticket == t }
func (g *Grouper) Invalidate(t Ticket) {
	if p := g.entries[t.Key]; p != nil && p.ticket == t {
		p.verdict = event.Unknown("")
	}
}

// Checkpoint intentionally contains only opaque identities and counters, never templates or samples.
func (g *Grouper) Checkpoint() any {
	type row struct {
		TemplateID string `json:"template_id"`
		VersionID  string `json:"version_id"`
		Seen       uint64 `json:"seen"`
		Generation uint64 `json:"generation"`
	}
	rows := []row{}
	for _, p := range g.entries {
		rows = append(rows, row{p.ticket.TemplateID, p.ticket.VersionID, p.seen, p.ticket.Generation})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].TemplateID < rows[j].TemplateID })
	return struct {
		Schema    int   `json:"schema_version"`
		Templates []row `json:"templates"`
	}{1, rows}
}
