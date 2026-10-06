package controller

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestHotJournalFIFORejectedWithoutBlocking(t *testing.T) {
	if path := os.Getenv("JEV_FIFO_PROBE"); path != "" {
		s, err := Open(path)
		if err == nil {
			s.Close()
			t.Fatal("FIFO journal accepted")
		}
		return
	}
	path := filepath.Join(privateTestDir(t), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Any existing journal triggers SQLite's hot-journal read-only open, which
	// blocks on a FIFO unless rejected before calling the underlying Unix VFS.
	if err := unix.Mkfifo(path+"-journal", 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestHotJournalFIFORejectedWithoutBlocking$")
	cmd.Env = []string{"JEV_FIFO_PROBE=" + path}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("SQLite blocked opening FIFO journal")
	}
	if err != nil {
		t.Fatalf("probe: %v %s", err, out)
	}
}

func TestPrivateStateDirectory(t *testing.T) {
	for _, kind := range []string{"public", "symlink", "writable_ancestor", "foreign_owner"} {
		t.Run(kind, func(t *testing.T) {
			root := privateTestDir(t)
			dir := filepath.Join(root, "private")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "public":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filepath.Join(root, "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				dir = link
			case "writable_ancestor":
				if err := os.Chmod(root, 0777); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(root, 0700)
			case "foreign_owner":
				if os.Geteuid() != 0 {
					t.Skip("chown requires root")
				}
				if err := os.Chown(dir, 1, -1); err != nil {
					t.Fatal(err)
				}
				defer os.Chown(dir, 0, -1)
			}
			s, err := Open(filepath.Join(dir, "state.db"))
			if err == nil {
				s.Close()
				t.Fatal("unsafe directory accepted")
			}
		})
	}
}

func TestHotJournalRecoveryRetainsCommittedState(t *testing.T) {
	if path := os.Getenv("JEV_CRASH_PROBE"); path != "" {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec("PRAGMA cache_size=1"); err != nil {
			t.Fatal(err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("UPDATE seen SET decision='uncommitted'"); err != nil {
			t.Fatal(err)
		}
		// Simulate abrupt death with dirty pages and a real hot rollback journal.
		os.Exit(0)
	}
	path := filepath.Join(privateTestDir(t), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<2000) INSERT INTO seen SELECT printf('%064d',x),1000,'committed' FROM n"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestHotJournalRecoveryRetainsCommittedState$")
	cmd.Env = []string{"JEV_CRASH_PROBE=" + path}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v %s", err, out)
	}
	journal, err := os.ReadFile(path + "-journal")
	if err != nil || len(journal) < 512 || journal[0] == 0 {
		t.Fatal("no hot journal", err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err = s.db.QueryRow("SELECT count(*) FROM seen WHERE decision='committed'").Scan(&n); err != nil || n != 2000 {
		t.Fatal("recovery lost committed rows", n, err)
	}
	var integrity string
	if err = s.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
}
