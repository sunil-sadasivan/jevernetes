package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

func TestEvictionRetainsDurableLifetime(t *testing.T) {
	for _, scenario := range []string{"rollback", "recovered-wall", "multiple-evictions", "provider-failure", "provider-error", "remaining-lifetime", "rollback-before-expiry"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t)
			base := time.Now()
			base = base.Add(time.Unix(1000, 0).Sub(base.Round(0)))
			now := base
			a := occurrence(1)
			a.Text = "INFO alpha"
			calls := 0
			fail := false
			next := reassessmentJudge(func(ds []provider.Dossier) ([]event.Judgment, error) {
				calls++
				if fail {
					if scenario == "provider-error" {
						return nil, errors.New("synthetic provider failure")
					}
					return []event.Judgment{event.Unknown("unavailable")}, nil
				}
				return []event.Judgment{a.Judgment}, nil
			})
			clock := func() time.Time { return now }
			e, err := analysis.New(analysis.Config{Now: clock, Strategy: "drain", Masking: "strict", Capacity: 1, BatchSize: 16, TTL: time.Minute, NormalInterval: 32}, CachedJudge{Now: clock, Store: s, Next: next, Contract: "eviction", TTL: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			e.Batch(ctx, []event.Event{a})
			var expiry int64
			if err := s.db.QueryRow("SELECT expires FROM verdicts").Scan(&expiry); err != nil || expiry != 1060 {
				t.Fatal(expiry, err)
			}
			now = base.Add(59 * time.Second)
			count := 1
			if scenario == "multiple-evictions" {
				count = 8
			}
			for i := 0; i < count; i++ {
				b := a
				b.Text = fmt.Sprintf("INFO distinct-%d", i)
				e.Batch(ctx, []event.Event{b})
			}
			before := calls
			wall, elapsed := 20*time.Second, 60*time.Second
			if scenario == "recovered-wall" {
				wall, elapsed = 59*time.Second, 89*time.Second
			}
			if scenario == "rollback-before-expiry" {
				elapsed = 59500 * time.Millisecond
			}
			if scenario == "remaining-lifetime" {
				wall, elapsed = 59500*time.Millisecond, 59500*time.Millisecond
			}
			now = steppedClock(t, base, wall, elapsed)
			fail = scenario == "provider-failure" || scenario == "provider-error"
			got := e.Batch(ctx, []event.Event{a, a})
			if scenario == "remaining-lifetime" {
				if calls != before {
					t.Fatal("lost valid remaining lifetime")
				}
				now = steppedClock(t, base, 59*time.Second, 60*time.Second)
				e.Batch(ctx, []event.Event{a})
			}
			if calls != before+1 {
				t.Fatalf("eviction renewed old durable authority: calls=%d want=%d, fanout=%v", calls, before+1, got[1].AnalysisReused)
			}
			if fail {
				if got[0].AnalysisReused || got[1].AnalysisReused {
					t.Fatal("failed provider reused expired verdict")
				}
				b := a
				b.Text = "INFO final eviction"
				e.Batch(ctx, []event.Event{b})
				now = steppedClock(t, base, 49*time.Second, 89*time.Second)
				before = calls
				got = e.Batch(ctx, []event.Event{a, a})
				if calls != before+1 || got[1].AnalysisReused {
					t.Fatal("failed refresh resurrected after recovery/eviction")
				}
			}
		})
	}
}

func TestStoreLifetimeCapacityFailsClosed(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	base := time.Now()
	base = base.Add(time.Unix(1000, 0).Sub(base.Round(0)))
	j := occurrence(1).Judgment
	// Populate real durable rows in one transaction, then observe each lifetime.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < stateCapacity; i++ {
		if _, err := tx.Exec("INSERT INTO verdicts VALUES(?,?,1000,1060)", event.Hash(i), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < stateCapacity; i++ {
		got, err := s.Lookup(ctx, event.Hash(i), base, time.Minute)
		if err != nil || got == nil {
			t.Fatal(i, err)
		}
	}
	for i := stateCapacity; i < stateCapacity+10; i++ {
		key := event.Hash(i)
		if err := s.PutVerdict(ctx, key, j, 1000, time.Minute); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Lookup(ctx, key, base, time.Minute); err != nil || got != nil {
			t.Fatal("capacity bypass", got, err)
		}
	}
	if len(s.lifetimes) != stateCapacity {
		t.Fatal("unbounded lifetime registry", len(s.lifetimes))
	}
	// Capacity pressure cannot erase old deadlines; freeing a slot must not make
	// a previously untracked fresh row reloadable with a new lifetime.
	now := steppedClock(t, base, 20*time.Second, 60*time.Second)
	if got, err := s.Lookup(ctx, event.Hash(0), now, time.Minute); err != nil || got != nil {
		t.Fatal("old lifetime renewed", got, err)
	}
	if got, err := s.Lookup(ctx, event.Hash(stateCapacity), now, time.Minute); err != nil || got != nil {
		t.Fatal("untracked row resurrected", got, err)
	}
	if len(s.lifetimes) > stateCapacity {
		t.Fatal("capacity exceeded")
	}
	// Expiration/rollback deletion persists even if the next provider fails.
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM verdicts WHERE key=?", event.Hash(0)).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
