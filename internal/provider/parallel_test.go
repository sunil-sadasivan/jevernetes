package provider

import (
	"context"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"sync"
	"testing"
	"time"
)

type parallelFixture struct {
	mu          sync.Mutex
	active, max int
}

func (f *parallelFixture) Judge(ctx context.Context, groups []Dossier) ([]event.Judgment, error) {
	f.mu.Lock()
	f.active++
	f.max = max(f.max, f.active)
	f.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Millisecond):
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	out := []event.Judgment{}
	for _, g := range groups {
		out = append(out, event.Unknown(g.EvidenceID))
	}
	return out, nil
}
func TestParallelJudgeBoundAndOrdering(t *testing.T) {
	next := &parallelFixture{}
	groups := make([]Dossier, 12)
	for i := range groups {
		groups[i].EvidenceID = string(rune('a' + i))
	}
	out, err := (ParallelJudge{Next: next, Workers: 3, BatchSize: 2}).Judge(context.Background(), groups)
	if err != nil || len(out) != 12 || next.max != 3 {
		t.Fatal(err, next.max)
	}
	for i, j := range out {
		if j.AnalysisError != groups[i].EvidenceID {
			t.Fatal("provider order changed")
		}
	}
}
