package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(privateTestDir(t), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func occurrence(n int) event.Event {
	return event.Event{ID: event.Hash(n)[:20], Source: event.Source{"type": "file", "path": "synthetic"}, Text: "unique synthetic log body", Judgment: event.Judgment{Importance: "important", Severity: "impact", Category: "dependency", ImportanceConfidence: event.Confidence(.99), SeverityConfidence: event.Confidence(.99), CategoryConfidence: event.Confidence(.99)}}
}
func TestPolicyGates(t *testing.T) {
	p := DefaultPolicy()
	if p.Validate() != nil {
		t.Fatal("default invalid")
	}
	e := occurrence(1)
	if p.Decide(e) != "notify" {
		t.Fatal("notify missing")
	}
	e.ImportanceConfidence = nil
	if p.Decide(e) != "review" {
		t.Fatal("missing confidence ignored")
	}
	p.Abstention = "record"
	if p.Decide(e) != "abstain" {
		t.Fatal("abstain ignored")
	}
	p.MinConfidence = 2
	if p.Validate() == nil {
		t.Fatal("invalid confidence")
	}
}
func TestAtomicOutboxNoveltyCooldownAndPrivacy(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	p := DefaultPolicy()
	a, err := s.Apply(ctx, occurrence(1), "contract", p, 1000)
	if err != nil || !a.Enqueued {
		t.Fatal(a, err)
	}
	a, err = s.Apply(ctx, occurrence(1), "contract", p, 1001)
	if err != nil || !a.Duplicate {
		t.Fatal(a, err)
	}
	a, err = s.Apply(ctx, occurrence(2), "contract", p, 1002)
	if err != nil || a.Enqueued {
		t.Fatal("cooldown failed", err)
	}
	status, err := s.Status(ctx)
	if err != nil || status.Pending != 1 || len(status.Recent) != 1 || status.Recent[0].Count != 2 {
		t.Fatal(status, err)
	}
	d, err := s.Detail(ctx, status.Recent[0].ID)
	if err != nil || d.Notification == nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "unique synthetic") || strings.Contains(string(raw), "synthetic\"") {
		t.Fatal("log/source persisted")
	}
	for _, table := range []string{"incidents", "outbox", "seen", "verdicts"} {
		rows, err := s.db.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if rows.Scan(dest...) != nil {
				t.Fatal("scan")
			}
			b, _ := json.Marshal(values)
			if strings.Contains(string(b), "unique synthetic log body") {
				t.Fatal("raw log in state")
			}
		}
		rows.Close()
	}
}
func TestOutboxDeliveryRetryAndDead(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, _ = s.Apply(ctx, occurrence(1), "c", DefaultPolicy(), 1000)
	for i := 1; i <= 8; i++ {
		p, err := s.Pending(ctx, int64(1000+i*400))
		if err != nil || p == nil || p.Attempts != i {
			t.Fatal(p, err)
		}
		if err = s.Delivered(ctx, *p, false, int64(1000+i*400)); err != nil {
			t.Fatal(err)
		}
	}
	status, err := s.Status(ctx)
	if err != nil || status.Dead != 1 || status.Pending != 0 {
		t.Fatal(status, err)
	}
}
func TestStateLockAndFutureVersion(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(path); err == nil {
		other.Close()
		t.Fatal("second writer allowed")
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec("PRAGMA user_version=99")
	db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("future schema accepted")
	}
}
func TestPersistedVerdictTTLAndRollbackClock(t *testing.T) {
	s := openTest(t)
	key := event.Hash("fixture")
	j := occurrence(1).Judgment
	ctx := context.Background()
	if err := s.PutVerdict(ctx, key, j, 1000, time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{999, 1060} {
		got, err := s.Lookup(ctx, key, at, time.Minute)
		if err != nil || got != nil {
			t.Fatal(got, err)
		}
	}
	got, err := s.Lookup(ctx, key, 1001, time.Minute)
	if err != nil || got == nil {
		t.Fatal(err)
	}
}
func TestInspectionContentionAndMissing(t *testing.T) {
	s := openTest(t)
	s.mu.Lock()
	_, err := s.Status(context.Background())
	s.mu.Unlock()
	if err != ErrBusy {
		t.Fatal(err)
	}
	d, err := s.Detail(context.Background(), event.Hash("absent"))
	if err != nil || d != nil {
		t.Fatal(d, err)
	}
}
func TestControllerCancellation(t *testing.T) {
	s := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Deliver(ctx, StdoutSink{Writer: &strings.Builder{}}); err != nil {
		t.Fatal(err)
	}
}
func TestWebhookValidation(t *testing.T) {
	credentialURL := strings.Join([]string{"https://", "user", ":", "pass", "@example.invalid/"}, "")
	for _, u := range []string{"http://example.invalid", credentialURL, "https://example.invalid/#fragment"} {
		if _, err := NewWebhook(u); err == nil {
			t.Fatal("unsafe URL")
		}
	}
}
func TestOutboxCrashAfterFinalAttemptBecomesDead(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, err := s.Apply(ctx, occurrence(1), "c", DefaultPolicy(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("UPDATE outbox SET attempts=8,due=0")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(ctx, 2000)
	if err != nil || p != nil {
		t.Fatal(p, err)
	}
	status, err := s.Status(ctx)
	if err != nil || status.Dead != 1 {
		t.Fatal(status, err)
	}
}

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
