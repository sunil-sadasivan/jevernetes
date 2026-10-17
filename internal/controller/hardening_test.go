package controller

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"golang.org/x/sys/unix"
)

func fillTable(t *testing.T, s *Store, table string) {
	t.Helper()
	values := map[string]string{
		"seen":      "printf('%064d',n),1000,'abstain'",
		"incidents": "printf('%064d',n),1000,1,0,1000,n,1000,'review'",
		"verdicts":  "printf('%064d',n),'{}',1000,2000",
		"outbox":    "printf('%064d',n),printf('%064d',n),'{}','pending',0,1000,1000",
	}
	if _, err := s.db.Exec("WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers WHERE n<10000) INSERT INTO " + table + " SELECT " + values[table] + " FROM numbers"); err != nil {
		t.Fatal(err)
	}
}
func TestYoungStateAtCapacityKeepsProcessing(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, table := range []string{"seen", "incidents", "verdicts", "outbox"} {
		fillTable(t, s, table)
	}
	if _, err := s.db.Exec("UPDATE outbox SET status='delivered'"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		e := occurrence(20000 + i)
		e.Text = event.Hash(i)
		if a, err := s.Apply(ctx, e, "c", DefaultPolicy(), 1001); err != nil || !a.Enqueued {
			t.Fatal(a, err)
		}
		if err := s.PutVerdict(ctx, event.Hash(i), occurrence(1).Judgment, 1001, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"seen", "incidents", "verdicts", "outbox"} {
		var count, tracked int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("SELECT rows FROM state_counts WHERE name=?", table).Scan(&tracked); err != nil {
			t.Fatal(err)
		}
		if count != stateCapacity || tracked != count || s.Metrics()["state_"+table+"_evicted"] != 20 {
			t.Fatal(table, count, tracked, s.Metrics())
		}
	}
	// An update must not evict another cache row at capacity.
	if err := s.PutVerdict(ctx, event.Hash(19), occurrence(1).Judgment, 1002, time.Minute); err != nil {
		t.Fatal(err)
	}
	if s.Metrics()["state_verdicts_evicted"] != 20 {
		t.Fatal("update evicted")
	}
	var exists int
	if err := s.db.QueryRow("SELECT 1 FROM seen WHERE key=printf('%064d',1)").Scan(&exists); err != sql.ErrNoRows {
		t.Fatal("oldest tie not evicted")
	}
}
func TestPendingOutboxBackpressureRollsBackAndRecovers(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	fillTable(t, s, "outbox")
	e := occurrence(1)
	if _, err := s.Apply(ctx, e, "c", DefaultPolicy(), 1001); !errors.Is(err, ErrBackpressure) {
		t.Fatal(err)
	}
	var count int
	s.db.QueryRow("SELECT count(*) FROM seen").Scan(&count)
	if count != 0 {
		t.Fatal("novelty committed without intent")
	}
	s.db.QueryRow("SELECT count(*) FROM outbox WHERE status='pending'").Scan(&count)
	if count != 10000 {
		t.Fatal("pending dropped")
	}
	// A terminal slot lets the exact same event commit on retry.
	if _, err := s.db.Exec("UPDATE outbox SET status='delivered' WHERE id=printf('%064d',1)"); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Apply(ctx, e, "c", DefaultPolicy(), 1002); err != nil || !a.Enqueued || a.Duplicate {
		t.Fatal(a, err)
	}
}
func TestIncidentEvictionDoesNotReuseNotificationID(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	e := occurrence(1)
	if _, err := s.Apply(ctx, e, "c", DefaultPolicy(), 1000); err != nil {
		t.Fatal(err)
	}
	var first string
	s.db.QueryRow("SELECT id FROM outbox").Scan(&first)
	if _, err := s.db.Exec("DELETE FROM incidents"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, occurrence(2), "c", DefaultPolicy(), 1001); err != nil {
		t.Fatal(err)
	}
	var second string
	s.db.QueryRow("SELECT id FROM outbox WHERE id!=?", first).Scan(&second)
	if first == second || second == "" {
		t.Fatal("notification identity reused")
	}
	status, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.Detail(ctx, status.Recent[0].ID)
	if err != nil || detail.Notification.ID != second {
		t.Fatal("detail lookup failed", err)
	}
}
func TestPrivateDescriptorRejectsSymlinksModesAndHardlinks(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "hardlink", "fifo", "lock_symlink", "lock_public"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateTestDir(t)
			path := filepath.Join(dir, "state.db")
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("synthetic sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				os.Symlink(target, path)
			case "public":
				os.WriteFile(path, nil, 0644)
			case "hardlink":
				os.Link(target, path)
			case "fifo":
				unix.Mkfifo(path, 0600)
			case "lock_symlink":
				os.Symlink(target, path+".lock")
			case "lock_public":
				os.WriteFile(path+".lock", nil, 0644)
			}
			if s, err := Open(path); err == nil {
				s.Close()
				t.Fatal("unsafe state accepted")
			}
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != "synthetic sentinel" {
				t.Fatal("target changed")
			}
		})
	}
}
func TestSQLiteActualDescriptorRejectsReplacementAfterVerification(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "symlink"}[symlink], func(t *testing.T) {
			dir := privateTestDir(t)
			path := filepath.Join(dir, "state.db")
			target := filepath.Join(dir, "target")
			f, err := openPrivate(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			v, err := newVerifiedVFS(f)
			if err != nil {
				t.Fatal(err)
			}
			defer v.close()
			if err = os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(target, []byte("synthetic sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			if symlink {
				err = os.Symlink(target, path)
			} else {
				err = os.WriteFile(path, nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", stateURI(path, v.name))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("CREATE TABLE must_not_exist(x)"); err == nil {
				t.Fatal("SQLite used replaced target")
			}
			raw, _ := os.ReadFile(target)
			if string(raw) != "synthetic sentinel" {
				t.Fatal("target modified")
			}
			if !symlink {
				st, _ := os.Stat(path)
				if st.Size() != 0 {
					t.Fatal("replacement modified")
				}
			}
		})
	}
}
func TestStatePathURICharacters(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "state?#.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		t.Fatal("URI path was misinterpreted", err)
	}
}
