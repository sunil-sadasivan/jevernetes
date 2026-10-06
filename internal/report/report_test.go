package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

func TestReportSchemaAndRetention(t *testing.T) {
	r := New(1)
	for i := 0; i < 2; i++ {
		r.Add(event.Event{ID: event.Hash(i), GroupID: "group", Text: "synthetic", Judgment: event.Judgment{Importance: "important"}, LineCount: 1})
	}
	value := r.Snapshot("offline-rules", nil, false, provider.Usage{Complete: true}, nil, nil)
	summary := value["summary"].(map[string]any)
	if value["schema_version"] != 2 || value["runtime"] != "go" || summary["events"] != uint64(2) || summary["retained_events"] != 1 || summary["complete_within_window"] != false {
		t.Fatal(value)
	}
}
func TestAtomicPrivateReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "report.json")
	if err := Write(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	st, _ = os.Stat(filepath.Dir(path))
	if st.Mode().Perm() != 0700 {
		t.Fatal(st.Mode())
	}
	if err := Write(path, map[string]int{"a": 2}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var v map[string]int
	if json.Unmarshal(b, &v) != nil || v["a"] != 2 {
		t.Fatal(string(b))
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	if len(files) != 1 {
		t.Fatal("temp leaked")
	}
}
func TestBoundedCoverage(t *testing.T) {
	r := New(1)
	for i := 0; i < 600; i++ {
		r.Gap(event.Source{}, "bounded")
	}
	v := r.Snapshot("offline", nil, false, provider.Usage{}, nil, nil)
	if len(v["coverage"].([]Coverage)) != 500 || v["metrics"].(map[string]uint64)["coverage_records_omitted"] != 100 {
		t.Fatal("unbounded coverage")
	}
}

func TestEveryKnownLossPreventsCompleteCoverage(t *testing.T) {
	signals := []string{"coverage_input_incomplete", "coverage_event_limit", "coverage_queue_drop", "coverage_stream_capacity", "coverage_log_unavailable", "coverage_log_incomplete", "coverage_identity_changed", "coverage_stream_reconnect", "coverage_discovery_unavailable", "coverage_cursor_overflow", "coverage_cursor_untimestamped", "coverage_cursor_truncated", "coverage_cursor_out_of_order", "coverage_bounded_history", "coverage_provider_budget_stopped", "coverage_collection_interrupted", "coverage_records_omitted", "coverage_future_loss", "dropped", "truncated_events", "parse_uncertain", "evicted_events", "budget_stopped", "outbox_backpressure", "state_seen_evicted", "state_incidents_evicted", "state_verdicts_evicted", "state_outbox_evicted"}
	if !Complete(false, nil) || Complete(true, nil) {
		t.Fatal("base completeness")
	}
	for _, signal := range signals {
		t.Run(signal, func(t *testing.T) {
			metrics := map[string]uint64{signal: 1}
			if Complete(false, metrics) {
				t.Fatal("false completeness")
			}
			if New(1).Snapshot("offline", nil, false, provider.Usage{}, metrics, nil)["summary"].(map[string]any)["complete_within_window"] != false {
				t.Fatal("snapshot erased cumulative loss")
			}
		})
	}
	for _, e := range []event.Event{{Truncated: true}, {ParseUncertain: true}} {
		r := New(1)
		e.Importance = "routine"
		r.Add(e)
		if r.Snapshot("offline", nil, false, provider.Usage{}, nil, nil)["summary"].(map[string]any)["complete_within_window"] != false {
			t.Fatal("event loss omitted")
		}
	}
}
