package analysis

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type Config struct {
	Teacher                              provider.Teacher
	TemplateMinSamples, TemplateCapacity int
	TemplateConfidence                   float64
	TemplateTTL                          time.Duration
	Offline                              bool
	Strategy, Masking                    string
	Capacity, BatchSize                  int
	TTL                                  time.Duration
	Adaptive                             bool
	NormalInterval                       uint64
	ReviewIDs                            []string
	Rules                                *Rules
}

var opaqueID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c Config) Validate() error {
	for _, id := range c.ReviewIDs {
		if !opaqueID.MatchString(id) {
			return errors.New("invalid review identity")
		}
	}
	if c.Teacher != nil && (c.TemplateMinSamples < 2 || c.TemplateMinSamples > 16 || c.TemplateCapacity < 1 || c.TemplateCapacity > 256 || c.TemplateTTL < time.Second || c.TemplateTTL > time.Hour || !(c.TemplateConfidence >= 0 && c.TemplateConfidence <= 1)) {
		return errors.New("invalid template learning bounds")
	}
	if c.Strategy != "exact" && c.Strategy != "off" && c.Strategy != "drain" && c.Strategy != "semantic" {
		return errors.New("invalid grouping strategy")
	}
	if c.Masking != "strict" && c.Masking != "classic" {
		return errors.New("invalid drain masking")
	}
	if c.Capacity < 1 || c.Capacity > 8192 || c.BatchSize < 1 || c.BatchSize > 64 || c.TTL <= 0 || c.TTL > 7*24*time.Hour {
		return errors.New("invalid analysis bounds")
	}
	if c.Adaptive && (c.Offline || c.Strategy != "drain") {
		return errors.New("adaptive scheduling requires online drain grouping")
	}
	if c.NormalInterval < 32 || c.NormalInterval > 4096 || len(c.ReviewIDs) > 256 {
		return errors.New("invalid adaptive bounds")
	}
	return nil
}

type Engine struct {
	learning *learning
	config   Config
	judge    provider.Judge
	groups   *Grouper
	Metrics  map[string]uint64
}

func New(c Config, j provider.Judge) (*Engine, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Offline && j == nil {
		return nil, errors.New("provider required")
	}
	g := NewGrouper(c.Strategy, c.Masking, c.Capacity, c.TTL, c.Adaptive, c.NormalInterval)
	g.Veto(c.ReviewIDs)
	g.rules = c.Rules
	return &Engine{config: c, judge: j, groups: g, Metrics: map[string]uint64{}, learning: newLearning(c)}, nil
}
func (e *Engine) Checkpoint() any { return e.groups.Checkpoint() }
func (e *Engine) Batch(ctx context.Context, events []event.Event) []event.Event {
	out := append([]event.Event(nil), events...)
	defer func() { e.teach(ctx, out) }()
	if e.config.Offline {
		for i := range out {
			out[i].Judgment = event.Offline(out[i])
			e.Metrics["classifications"]++
		}
		return out
	}
	type group struct {
		ticket   Ticket
		d        provider.Dossier
		indices  []int
		eligible bool
	}
	groups := []group{}
	byTicket := map[Ticket]int{}
	now := time.Now()
	for i, ev := range out {
		ticket, cached, reason := e.groups.Observe(ev, now)
		if cached != nil {
			out[i].Judgment = *cached
			out[i].Assessment = &event.Assessment{TemplateID: ticket.TemplateID, VersionID: ticket.VersionID, WindowOccurrences: 1, Reason: "cached"}
			out[i].AnalysisReused = true
			e.Metrics["drain_verdict_reuses"]++
			continue
		}
		eligible := ticket.Key != ""
		if index, ok := byTicket[ticket]; ok && eligible {
			g := &groups[index]
			g.indices = append(g.indices, i)
			g.d.Observe(ev)
			continue
		}
		d := provider.Dossier{TemplateID: ticket.TemplateID, VersionID: ticket.VersionID, ScopeID: event.Hash(Scope(ev, true)), Source: event.Hash(ev.Source), EvidenceID: ev.ID, Reason: reason, Samples: []string{}}
		d.Observe(ev)
		if eligible {
			byTicket[ticket] = len(groups)
		}
		groups = append(groups, group{ticket, d, []int{i}, eligible})
	}
	if len(groups) == 0 {
		return out
	}
	dossiers := make([]provider.Dossier, len(groups))
	for i, g := range groups {
		dossiers[i] = g.d
	}
	var js []event.Judgment
	var err error
	if ctx.Err() != nil {
		err = ctx.Err()
	} else {
		js, err = e.judge.Judge(ctx, dossiers)
		e.Metrics["provider_batches"]++
	}
	if err == nil && len(js) != len(groups) {
		err = provider.ErrSchema
	}
	for n, g := range groups {
		j := event.Unknown("provider analysis unavailable")
		if err == nil {
			j = js[n]
			if provider.Validate(j) != nil {
				j = event.Unknown("invalid provider judgment")
			}
		}
		safe := !g.d.Sensitive && !g.d.Truncated && !g.d.EvidenceLimited && !g.d.SamplesLimited && !g.d.SecuritySignal
		if !safe && j.Importance == "routine" {
			j.Importance = "uncertain"
		}
		if j.Importance == "routine" && (j.ImportanceConfidence == nil || *j.ImportanceConfidence < 0.7) {
			j.Importance = "uncertain"
		}
		current := g.eligible && e.groups.Current(g.ticket)
		if g.eligible && !current {
			j = event.Unknown("stale template generation")
		}
		reusable := safe && current && j.Reusable()
		if reusable {
			reusable = e.groups.Publish(g.ticket, j, out[g.indices[0]].ID, now)
		} else if g.eligible {
			e.groups.Invalidate(g.ticket)
		}
		for k, i := range g.indices {
			out[i].Judgment = j
			if k > 0 && !reusable {
				out[i].Judgment = event.Unknown("group verdict requires independent review")
			}
			out[i].AnalysisReused = k > 0 && reusable
			if out[i].AnalysisReused {
				out[i].RepresentativeID = out[g.indices[0]].ID
				e.Metrics["drain_group_fanouts"]++
			}
			out[i].Assessment = &event.Assessment{Dispatched: ctx.Err() == nil, TemplateID: g.ticket.TemplateID, VersionID: g.ticket.VersionID, WindowOccurrences: g.d.WindowOccurrences, SampleCount: len(g.d.Samples), EvidenceLimited: g.d.EvidenceLimited, SamplesLimited: g.d.SamplesLimited, Reason: g.d.Reason}
		}
		e.Metrics["classifications"]++
		e.Metrics["drain_group_assessments"]++
		e.Metrics["drain_group_occurrences"] += uint64(len(g.indices))
	}
	return out
}

// Run has no occurrence-count warmup. Even one event is assessed within 350ms
// of entering this lane (excluding provider latency). Cancellation drains queued work.
func (e *Engine) Run(ctx context.Context, in <-chan event.Event, emit func(event.Event) error) error {
	for {
		var first event.Event
		var ok bool
		select {
		case first, ok = <-in:
			if !ok {
				return nil
			}
		case <-ctx.Done():
			for ev := range in {
				ev.Judgment = event.Unknown("analysis cancelled")
				if err := emit(ev); err != nil {
					return err
				}
			}
			return ctx.Err()
		}
		batch := []event.Event{first}
		timer := time.NewTimer(350 * time.Millisecond)
		closed := false
	fill:
		for len(batch) < e.config.BatchSize {
			select {
			case ev, ok := <-in:
				if !ok {
					closed = true
					break fill
				}
				batch = append(batch, ev)
			case <-timer.C:
				break fill
			case <-ctx.Done():
				break fill
			}
		}
		timer.Stop()
		for _, ev := range e.Batch(ctx, batch) {
			if err := emit(ev); err != nil {
				return err
			}
		}
		if closed {
			return nil
		}
	}
}
