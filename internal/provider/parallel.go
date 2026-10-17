package provider

import (
	"context"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"sync"
)

// ParallelJudge preserves input order while bounding independent provider calls.
// The underlying client owns the shared cumulative admission/usage budget.
type ParallelJudge struct {
	Next               Judge
	Workers, BatchSize int
}

func (p ParallelJudge) Judge(ctx context.Context, groups []Dossier) ([]event.Judgment, error) {
	if p.Workers < 1 || p.Workers > 16 || p.BatchSize < 1 || p.BatchSize > 64 {
		return nil, ErrSchema
	}
	if len(groups) <= p.BatchSize {
		return p.Next.Judge(ctx, groups)
	}
	out := make([]event.Judgment, len(groups))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < min(p.Workers, (len(groups)+p.BatchSize-1)/p.BatchSize); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for start := range jobs {
				end := min(start+p.BatchSize, len(groups))
				values, err := p.Next.Judge(ctx, groups[start:end])
				for index := start; index < end; index++ {
					out[index] = event.Unknown("provider analysis unavailable")
					if err == nil && len(values) == end-start {
						out[index] = values[index-start]
					}
				}
			}
		}()
	}
	for start := 0; start < len(groups); start += p.BatchSize {
		jobs <- start
	}
	close(jobs)
	wg.Wait()
	return out, nil
}
