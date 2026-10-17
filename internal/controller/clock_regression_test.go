package controller

import (
	"context"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

// steppedClock keeps real time.Now monotonic readings while independently
// advancing wall time. Go has no public constructor for this production state.
// Touch only the reflected monotonic field, and verify both readings so a Go
// representation change fails the fixture rather than silently weakening it.
func steppedClock(t *testing.T, base time.Time, wall, elapsed time.Duration) time.Time {
	t.Helper()
	out := base.Add(wall)
	mono := base.Add(elapsed)
	field := reflect.ValueOf(&out).Elem().FieldByName("ext")
	if !field.IsValid() || field.Kind() != reflect.Int64 || base == base.Round(0) {
		t.Fatal("unsupported time.Time monotonic fixture")
	}
	*(*int64)(unsafe.Pointer(field.UnsafeAddr())) = reflect.ValueOf(mono).FieldByName("ext").Int()
	if out.Sub(base) != elapsed || out.Round(0).Sub(base.Round(0)) != wall {
		t.Fatal("clock fixture did not retain independent readings")
	}
	return out
}

func TestDurableMonotonicLifetime(t *testing.T) {
	for _, scenario := range []string{"exact-rollback-sequence", "rollback-at-1020", "rollback-at-1049", "recovered-wall", "boundary", "fractional", "forward-jump", "shorter-ttl", "longer-ttl", "delay-rollback", "delay-recovered-wall", "fresh-provider-rollback", "fresh-provider-expired"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store := openTest(t)
			// Reproduce wall 1000/1060 exactly, retaining a real monotonic component.
			base := time.Now()
			base = base.Add(time.Unix(1000, 0).Sub(base.Round(0)))
			now := base
			if scenario == "fresh-provider-rollback" {
				now = base.Add(900 * time.Millisecond)
			}
			ttl := time.Minute
			a := occurrence(1)
			a.Text = "INFO request_id=1234567890abcdef"
			calls := 0
			next := partialJudge(func(ds []provider.Dossier) []event.Judgment {
				calls++
				if scenario == "exact-rollback-sequence" && calls > 1 {
					return []event.Judgment{event.Unknown("unavailable")}
				}
				if calls == 1 && scenario == "fresh-provider-rollback" {
					now = steppedClock(t, base, 800*time.Millisecond, time.Second)
				}
				if calls == 1 && scenario == "fresh-provider-expired" {
					now = steppedClock(t, base, 30*time.Second, time.Minute)
				}
				return []event.Judgment{a.Judgment}
			})
			clock := func() time.Time { return now }
			newEngine := func(delay bool) *analysis.Engine {
				t.Helper()
				cached := CachedJudge{Now: clock, Store: store, Next: next, Contract: "monotonic", TTL: ttl}
				judge := partialJudge(func(ds []provider.Dossier) []event.Judgment {
					js, err := cached.Judge(ctx, ds)
					if err != nil {
						t.Fatal(err)
					}
					if delay {
						wall := 20 * time.Second
						if scenario == "delay-recovered-wall" {
							wall = 59 * time.Second
						}
						now = steppedClock(t, base, wall, 89*time.Second)
					}
					return js
				})
				e, err := analysis.New(analysis.Config{Now: clock, Strategy: "drain", Masking: "strict", Capacity: 8, BatchSize: 16, TTL: ttl, NormalInterval: 32}, judge)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			e := newEngine(false)
			e.Batch(ctx, []event.Event{a})
			if scenario == "fresh-provider-rollback" || scenario == "fresh-provider-expired" {
				e.Batch(ctx, []event.Event{a})
				if calls != 2 {
					t.Fatal("delayed fresh judgment reused", calls)
				}
				return
			}
			load := 59 * time.Second
			if scenario == "shorter-ttl" {
				ttl, load = 30*time.Second, 29*time.Second
			}
			if scenario == "longer-ttl" {
				ttl = 2 * time.Minute
			}
			if scenario == "fractional" {
				load += 900 * time.Millisecond
			}
			now = steppedClock(t, base, load, load)
			delay := scenario == "delay-rollback" || scenario == "delay-recovered-wall"
			e = newEngine(delay)
			out := e.Batch(ctx, []event.Event{a, a})
			if calls != 1 {
				t.Fatal("expected durable hit", calls)
			}
			if delay {
				if out[1].AnalysisReused {
					t.Fatal("delayed durable hit fanned out")
				}
				e.Batch(ctx, []event.Event{a})
				if calls != 2 {
					t.Fatal("rejected hit reloaded expired durable authority", calls)
				}
				return
			}
			if !e.Batch(ctx, []event.Event{a})[0].AnalysisReused {
				t.Fatal("remaining lifetime unavailable")
			}
			wall, elapsed := 60*time.Second, 60*time.Second
			switch scenario {
			case "exact-rollback-sequence", "rollback-at-1020":
				wall, elapsed = 20*time.Second, 60*time.Second
			case "rollback-at-1049":
				wall, elapsed = 49*time.Second, 89*time.Second
			case "recovered-wall":
				wall, elapsed = 59*time.Second, 89*time.Second
			case "fractional":
				wall, elapsed = load, 60*time.Second
			case "forward-jump":
				wall, elapsed = 61*time.Second, load+time.Millisecond
			case "shorter-ttl":
				wall, elapsed = 30*time.Second, 30*time.Second
			}
			now = steppedClock(t, base, wall, elapsed)
			got := e.Batch(ctx, []event.Event{a})[0]
			if calls != 2 || got.AnalysisReused {
				t.Fatalf("original lifetime extended: calls=%d reused=%v", calls, got.AnalysisReused)
			}
			if scenario == "exact-rollback-sequence" {
				now = steppedClock(t, base, 49*time.Second, 89*time.Second)
				got = e.Batch(ctx, []event.Event{a})[0]
				if calls != 3 || got.AnalysisReused || got.Importance != "unknown" {
					t.Fatal("old durable authority resurrected after rollback", calls, got)
				}
			}
		})
	}
}
