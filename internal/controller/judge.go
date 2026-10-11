package controller

import (
	"context"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

// CachedJudge persists only hashed semantic identities and validated verdicts.
// Sensitive or incomplete dossiers always bypass durable reuse.
type CachedJudge struct {
	Now      func() time.Time
	Store    *Store
	Next     provider.Judge
	Contract string
	TTL      time.Duration
	Rescore  bool
}

func (c CachedJudge) Judge(ctx context.Context, groups []provider.Dossier) ([]event.Judgment, error) {
	out := make([]event.Judgment, len(groups))
	pending := []provider.Dossier{}
	indices := []int{}
	keys := []string{}
	clock := c.Now
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	for i, g := range groups {
		key := ""
		if g.TemplateID != "" {
			// One entry per semantic family: an audit of another normalized
			// sample must invalidate every prior verdict for that family. The
			// new namespace leaves old sample-specific entries unreachable.
			key = event.Hash([]any{"go-group-cache-v2", c.Contract, g.ScopeID, g.TemplateID, g.VersionID, g.BaselineImportant})
		}
		unsafe := g.Sensitive || g.Truncated || g.EvidenceLimited || g.SamplesLimited || g.SecuritySignal
		if key != "" && (c.Rescore || g.CacheMode != provider.CacheDefault || unsafe) {
			// Invalidate before dispatch: failed/unsafe reassessment must not
			// resurrect the previous verdict after a process restart.
			if err := c.Store.deleteVerdict(ctx, key); err != nil {
				return nil, err
			}
		}
		// Unsafe evidence still invalidates on reassessment, but can neither
		// consume nor publish a reusable family verdict.
		if unsafe {
			key = ""
		}
		if key != "" && !c.Rescore && g.CacheMode == provider.CacheDefault {
			j, err := c.Store.Lookup(ctx, key, now, c.TTL)
			if err != nil {
				return nil, err
			}
			if j != nil {
				out[i] = *j
				continue
			}
		}
		pending = append(pending, g)
		indices = append(indices, i)
		if g.CacheMode == provider.CacheBypass {
			key = ""
		}
		keys = append(keys, key)
	}
	if len(pending) == 0 {
		return out, nil
	}
	js, err := c.Next.Judge(ctx, pending)
	if err != nil {
		return nil, err
	}
	if len(js) != len(pending) {
		return nil, provider.ErrSchema
	}
	for n, j := range js {
		if keys[n] != "" && provider.Validate(j) == nil && j.Reusable() {
			j.CacheCreatedAt = time.Unix(now.Unix(), 0)
			j.CacheExpiresAt = time.Unix(now.Unix()+int64(c.TTL.Seconds()), 0)
			j.CacheObservedAt = now.Round(0)
			j.CacheDeadline = now.Add(j.CacheExpiresAt.Sub(now.Round(0)))
			// Provider latency consumes the original window. Never persist a result
			// whose wall or monotonic lifetime has already failed closed.
			if j.CacheValidAt(clock()) {
				if err := c.Store.PutVerdict(ctx, keys[n], j, now.Unix(), c.TTL); err != nil {
					return nil, err
				}
			}
		}
		out[indices[n]] = j
	}
	return out, nil
}
