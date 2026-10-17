// A deterministic synthetic group-risk replay. No credentials or sockets.
package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type oracle struct{}

func (oracle) Judge(_ context.Context, ds []provider.Dossier) ([]event.Judgment, error) {
	out := make([]event.Judgment, len(ds))
	for i := range out {
		out[i] = event.Judgment{Importance: "routine", Severity: "info", Category: "routine", ImportanceConfidence: event.Confidence(.99), SeverityConfidence: event.Confidence(.99), CategoryConfidence: event.Confidence(.99)}
	}
	return out, nil
}
func main() {
	engine, err := analysis.New(analysis.Config{Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 8, TTL: time.Minute, NormalInterval: 32}, oracle{})
	if err != nil {
		panic(err)
	}
	var reused int
	for i := 0; i < 1000; i++ {
		e := event.Event{ID: event.Hash(i)[:20], Source: event.Source{"type": "file", "path": "synthetic"}, Text: "INFO request_id=1234567890abcdef"}
		out := engine.Batch(context.Background(), []event.Event{e})
		if out[0].AnalysisReused {
			reused++
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"events": 1000, "reused": reused, "metrics": engine.Metrics})
}
