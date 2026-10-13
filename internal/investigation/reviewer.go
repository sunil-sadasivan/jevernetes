package investigation

import (
	"context"
	"encoding/json"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"sync"
	"time"
)

// Reviewer applies explicit human rules before provider dispatch. Human decisions
// never enter the model-template cache. A skipped baseline remains unknown so
// disabling a rule cannot fabricate a judgment that was never requested.
func Reviewer(ctx context.Context, path string) (func(event.Event) (event.Event, bool), error) {
	var mu sync.Mutex
	rules, err := (&Server{ctx: ctx, rulesPath: path}).rules()
	if err != nil {
		return nil, err
	}
	loaded := time.Now()
	return func(e event.Event) (event.Event, bool) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(loaded) > 100*time.Millisecond {
			var err error
			rules, err = (&Server{ctx: ctx, rulesPath: path}).rules()
			if err != nil {
				rules = nil
			}
			loaded = time.Now()
		}
		if rules == nil {
			return e, false
		}
		original := event.Unknown("AI analysis was skipped by a local expected-event rule")
		e.Judgment = original
		raw, _ := json.Marshal(e)
		var value object
		_ = json.Unmarshal(raw, &value)
		v := project(object{"events": []any{value}, "summary": object{"unknown": float64(1)}}, "", rules)
		projected := list(v, "events")[0].(object)
		if projected["review"] == nil {
			return e, false
		}
		raw, _ = json.Marshal(projected)
		if json.Unmarshal(raw, &e) != nil {
			return e, false
		}
		return e, true
	}, nil
}
