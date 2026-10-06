package controller

import (
	"context"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

// CachedJudge persists only hashed evidence identities and validated verdicts.
// Sensitive or incomplete dossiers always bypass durable reuse.
type CachedJudge struct {
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
	now := time.Now().Unix()
	for i, g := range groups {
		key := ""
		if !g.Sensitive && !g.Truncated && !g.EvidenceLimited && !g.SamplesLimited && !g.SecuritySignal && g.TemplateID != "" {
			key = event.Hash([]any{"go-group-cache-v1", c.Contract, g.ScopeID, g.TemplateID, g.VersionID, event.Hash(g.Samples), g.BaselineImportant})
		}
		if key != "" && !c.Rescore {
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
		out[indices[n]] = j
		if keys[n] != "" && j.Reusable() {
			if err := c.Store.PutVerdict(ctx, keys[n], j, now, c.TTL); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
