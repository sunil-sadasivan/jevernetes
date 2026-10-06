package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

// These fixtures create the historical schema directly on an empty database.
// No current migrations or capacity DDL are used to construct the input.
func historicalState(t *testing.T, version int) (string, string, string) {
	t.Helper()
	path := filepath.Join(privateTestDir(t), "state.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl, err := os.ReadFile(fmt.Sprintf("testdata/schema%d.sql", version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='state_counts' OR type='trigger' OR name IN ('seen_eviction','incident_eviction','verdict_eviction','outbox_terminal')").Scan(&n); err != nil || n != 0 {
		t.Fatal("fixture contains schema 4 artifacts", n, err)
	}
	incident := event.Hash("historical incident")
	id := event.Hash([]any{incident, int64(41)})
	var payload any = map[string]any{"schema": 1, "notification_id": id, "incident_id": incident, "decision": "notify", "recurrence_count": 2, "observed_at": 1000, "source": event.Source{"type": "file", "path": "private-fixture"}, "evidence": "private-body-fixture", "judgment": occurrence(1).Judgment}
	if version == 3 {
		payload = Notification{Schema: 2, ID: id, IncidentID: incident, EventID: occurrence(1).ID, SourceID: event.Hash("source"), Decision: "notify", Judgment: occurrence(1).Judgment, Occurrences: 2, Created: 1000}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	incidentValues := "?,1000,2,0,1000,41,1000"
	if version >= 2 {
		incidentValues += ",'notify'"
	}
	if _, err = db.Exec("INSERT INTO incidents VALUES("+incidentValues+")", incident); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO outbox VALUES(?,?,?,'pending',2,1001,1000)", id, incident, string(raw)); err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(occurrence(1).Judgment)
	if _, err = db.Exec("INSERT INTO verdicts VALUES(?,?,1000,2000)", event.Hash("legacy verdict"), string(j)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO seen VALUES(?,1000,'notify')", event.Hash("legacy occurrence")); err != nil {
		t.Fatal(err)
	}
	// Historically the clock was a delivery throttle, not an identity sequence.
	if _, err = db.Exec("UPDATE delivery_clock SET next=7"); err != nil {
		t.Fatal(err)
	}
	return path, incident, id
}

func TestPristineHistoricalMigrations(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path, incident, id := historicalState(t, version)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var got int
			if err = s.db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != 4 {
				t.Fatal(got, err)
			}
			st, err := os.Stat(path)
			if err != nil || st.Mode().Perm() != 0600 {
				t.Fatal(st, err)
			}
			ctx := context.Background()
			verdict, err := s.Lookup(ctx, event.Hash("legacy verdict"), 1001, time.Hour)
			if err != nil || (version < 3 && verdict != nil) || (version == 3 && verdict == nil) {
				t.Fatal("verdict migration", verdict, err)
			}
			for _, table := range []string{"seen", "incidents", "outbox", "verdicts"} {
				var actual, tracked, triggers int
				if err = s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&actual); err != nil {
					t.Fatal(err)
				}
				if err = s.db.QueryRow("SELECT rows FROM state_counts WHERE name=?", table).Scan(&tracked); err != nil || tracked != actual {
					t.Fatal(table, actual, tracked, err)
				}
				if err = s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND tbl_name=?", table).Scan(&triggers); err != nil || triggers != 2 {
					t.Fatal(table, triggers, err)
				}
				tx, err := s.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec("DELETE FROM " + table); err != nil {
					t.Fatal(err)
				}
				if err = tx.QueryRow("SELECT rows FROM state_counts WHERE name=?", table).Scan(&tracked); err != nil || tracked != 0 {
					t.Fatal("delete trigger", table, tracked, err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			}
			detail, err := s.Detail(ctx, incident)
			if err != nil || detail.Notification == nil || detail.Notification.ID != id || detail.Notification.Schema != 2 || *detail.Attempts != 2 || *detail.DeliveryStatus != "pending" {
				t.Fatal("legacy detail", detail, err)
			}
			pending, err := s.Pending(ctx, 1002)
			if err != nil || pending == nil || pending.ID != id || pending.Attempts != 3 || strings.Contains(string(pending.Payload), "private-") {
				t.Fatal("legacy delivery", pending, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || strings.Contains(string(raw), "private-body-fixture") {
				t.Fatal("migration left raw evidence", err)
			}
			var seq int64
			if err = s.db.QueryRow("SELECT next FROM delivery_clock").Scan(&seq); err != nil || seq < 41 {
				t.Fatal("sequence not migrated", seq, err)
			}
			// Pending advances the shared clock; the next identity must advance beyond it.
			if _, err = s.Apply(ctx, occurrence(900), "new", DefaultPolicy(), 1500); err != nil {
				t.Fatal(err)
			}
			var newID string
			var newSeq int64
			if err = s.db.QueryRow("SELECT key,sequence FROM incidents WHERE key!=?", incident).Scan(&newID, &newSeq); err != nil || newSeq <= seq {
				t.Fatal(newSeq, seq, err)
			}
			detail, err = s.Detail(ctx, newID)
			if err != nil || detail.Notification == nil || detail.Notification.ID != notificationID(newID, newSeq) || detail.Notification.ID == id {
				t.Fatal("new sequence detail", detail, err)
			}
			if err = s.db.QueryRow("SELECT rows FROM state_counts WHERE name='outbox'").Scan(&got); err != nil || got != 2 {
				t.Fatal("insert trigger", got, err)
			}
		})
	}
}
