package controller

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Exit without closing SQLite so committed rows remain in the real WAL and
// SQLite must initialize shared memory on the next open. No fabricated pages.
func TestWALCrashFixture(t *testing.T) {
	path := os.Getenv("JEV_WAL_FIXTURE")
	if path == "" {
		return
	}
	s, err := Open(path)
	mustVolume(t, err)
	var mode string
	mustVolume(t, s.db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode))
	if mode != "wal" {
		t.Fatal(mode)
	}
	_, err = s.db.Exec("PRAGMA wal_autocheckpoint=0")
	mustVolume(t, err)
	_, err = s.db.Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<2000) INSERT INTO seen SELECT printf('%064d',x),1000,'committed' FROM n")
	mustVolume(t, err)
	os.Exit(0)
}

func walFixture(t *testing.T, path string) {
	t.Helper()
	if !runWALProbe(t, "TestWALCrashFixture", "JEV_WAL_FIXTURE="+path) {
		t.Fatal("WAL fixture failed")
	}
	// Immutable reads deliberately ignore WAL: prove the committed rows have
	// not been checkpointed into the database used by the recovery tests.
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "immutable=1"}).String()
	db, err := sql.Open("sqlite", uri)
	mustVolume(t, err)
	defer db.Close()
	var count int
	mustVolume(t, db.QueryRow("SELECT count(*) FROM seen").Scan(&count))
	if count != 0 {
		t.Fatal("fixture rows already checkpointed", count)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		st, err := os.Stat(path + suffix)
		mustVolume(t, err)
		if st.Size() <= 32 {
			t.Fatal("missing real WAL recovery artifact", suffix)
		}
	}
}

func runWALProbe(t *testing.T, test, env string) bool {
	t.Helper()
	binary, err := os.Executable()
	mustVolume(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^"+test+"$")
	cmd.Env = []string{env}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Error("SQLite probe blocked until deadline", test)
	} else if err != nil {
		t.Errorf("SQLite probe: %v %s", err, out)
	}
	return ctx.Err() == nil && err == nil
}

func TestWALUnsafeSHMRejectedBeforeInitialization(t *testing.T) {
	if path := os.Getenv("JEV_WAL_OPEN"); path != "" {
		s, err := Open(path)
		if err == nil {
			s.Close()
			t.Fatal("unsafe SHM accepted")
		}
		if !errors.Is(err, ErrState) {
			t.Fatal("unexpected rejection", err)
		}
		return
	}
	for _, kind := range []string{"hardlink", "fifo", "readonly-fifo", "symlink", "public", "directory", "foreign-owner"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "foreign-owner" && os.Geteuid() != 0 {
				t.Skip("chown requires root")
			}
			path := filepath.Join(privateTestDir(t), "state.db")
			walFixture(t, path)
			before := map[string][]byte{}
			for _, suffix := range []string{"", "-wal"} {
				data, err := os.ReadFile(path + suffix)
				mustVolume(t, err)
				before[suffix] = data
			}
			mustVolume(t, os.Remove(path+"-shm"))
			target := filepath.Join(privateTestDir(t), "external")
			original := bytes.Repeat([]byte("external target must survive\n"), 4096)
			mustVolume(t, os.WriteFile(target, original, 0600))
			switch kind {
			case "symlink":
				mustVolume(t, os.Symlink(target, path+"-shm"))
			case "public", "foreign-owner":
				mustVolume(t, os.WriteFile(path+"-shm", original, 0600))
				if kind == "public" {
					mustVolume(t, os.Chmod(path+"-shm", 0640))
				} else {
					mustVolume(t, os.Chown(path+"-shm", 1, -1))
				}
			case "directory":
				mustVolume(t, os.Mkdir(path+"-shm", 0700))
			case "hardlink":
				mustVolume(t, os.Link(target, path+"-shm"))
			case "fifo", "readonly-fifo":
				mustVolume(t, unix.Mkfifo(path+"-shm", 0600))
				if kind == "readonly-fifo" {
					mustVolume(t, os.Chmod(path+"-shm", 0400))
				}
			}
			leaf, err := os.Lstat(path + "-shm")
			mustVolume(t, err)
			runWALProbe(t, "TestWALUnsafeSHMRejectedBeforeInitialization", "JEV_WAL_OPEN="+path)
			after, err := os.Lstat(path + "-shm")
			mustVolume(t, err)
			if !os.SameFile(leaf, after) || leaf.Mode() != after.Mode() || leaf.Size() != after.Size() {
				t.Error("rejection changed unsafe SHM leaf")
			}
			if kind == "public" || kind == "foreign-owner" {
				got, err := os.ReadFile(path + "-shm")
				mustVolume(t, err)
				if !bytes.Equal(got, original) {
					t.Error("rejection changed SHM bytes")
				}
			}
			for suffix, want := range before {
				got, err := os.ReadFile(path + suffix)
				mustVolume(t, err)
				if !bytes.Equal(got, want) {
					t.Error("SQLite initialized before rejection", suffix)
				}
			}
			got, err := os.ReadFile(target)
			mustVolume(t, err)
			if !bytes.Equal(got, original) {
				t.Errorf("external target modified: %d bytes became %d", len(original), len(got))
			}
		})
	}
}

func TestDeleteJournalDoesNotCreateSHM(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "state.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		mustVolume(t, err)
		if _, err := os.Lstat(path + "-shm"); !os.IsNotExist(err) {
			t.Error("DELETE database has unexpected SHM", err)
		}
		mustVolume(t, s.Close())
		if _, err := os.Lstat(path + "-shm"); !os.IsNotExist(err) {
			t.Fatal("DELETE database left SHM", err)
		}
	}
}

func TestPrepareVolumeWALRecovery(t *testing.T) {
	for _, layout := range []string{"legacy", "legacy-empty-private", "existing-private", "missing-shm"} {
		t.Run(layout, func(t *testing.T) {
			root := privateTestDir(t)
			source := root
			if layout == "existing-private" {
				source = filepath.Join(root, "private")
				mustVolume(t, os.Mkdir(source, 0700))
			} else if layout == "legacy-empty-private" {
				mustVolume(t, os.Mkdir(filepath.Join(root, "private"), 0700))
			}
			path := filepath.Join(source, "state.db")
			walFixture(t, path)
			if layout == "missing-shm" {
				mustVolume(t, os.Remove(path+"-shm"))
			}
			before := map[string][]byte{}
			for _, name := range volumeFiles {
				if layout == "missing-shm" && name == "state.db-shm" {
					continue
				}
				if name == "state.db-journal" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(source, name))
				mustVolume(t, err)
				before[name] = data
				mustVolume(t, os.Chmod(filepath.Join(source, name), 0660))
			}
			mustVolume(t, os.Chmod(source, 0770))
			mustVolume(t, os.Chmod(root, 0770))
			mustVolume(t, PrepareVolume(root))
			mustVolume(t, PrepareVolume(root))
			for name, want := range before {
				dest := filepath.Join(root, "private", name)
				got, err := os.ReadFile(dest)
				mustVolume(t, err)
				if !bytes.Equal(got, want) {
					t.Fatal("preparer changed WAL generation", name)
				}
				assertVolumeMode(t, dest, 0600)
			}
			// Model the deployment's direct subPath mount under protected ancestors.
			mounted := filepath.Join(privateTestDir(t), "mounted")
			mustVolume(t, os.Rename(filepath.Join(root, "private"), mounted))
			path = filepath.Join(mounted, "state.db")
			for i := 0; i < 2; i++ {
				s, err := Open(path)
				mustVolume(t, err)
				defer s.Close()
				var count int
				mustVolume(t, s.db.QueryRow("SELECT count(*) FROM seen WHERE decision='committed' AND at=1000 AND key=printf('%064d',CAST(key AS INTEGER)) AND CAST(key AS INTEGER) BETWEEN 1 AND 2000").Scan(&count))
				if count != 2000 {
					t.Fatal("WAL recovery lost committed rows", count)
				}
				var integrity, mode string
				mustVolume(t, s.db.QueryRow("PRAGMA integrity_check").Scan(&integrity))
				mustVolume(t, s.db.QueryRow("PRAGMA journal_mode").Scan(&mode))
				if integrity != "ok" || mode != "delete" {
					t.Fatal(integrity, mode)
				}
				mustVolume(t, s.Close())
				for _, suffix := range []string{"-wal", "-shm"} {
					if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
						t.Fatal("recovery left sidecar", suffix, err)
					}
				}
			}
		})
	}
}
