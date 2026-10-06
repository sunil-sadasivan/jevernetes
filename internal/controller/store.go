// Package controller serializes policy, novelty and notification intent in SQLite transactions.
package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

const Schema = 4

var ErrState = errors.New("controller state operation failed")
var ErrBusy = errors.New("controller state busy")

type Store struct {
	mu   sync.Mutex
	db   *sql.DB
	lock *os.File
	vfs  *verifiedVFS
	file *os.File
}

func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrState
	}
	abs, directory, err := openStateDirectory(abs)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	f, err := openPrivate(abs)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			f.Close()
		}
	}()
	lock, err := openPrivate(abs + ".lock")
	if err != nil {
		return nil, err
	}
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		lock.Close()
		return nil, errors.New("controller state already locked")
	}
	// Check SHM under the writer lock, before any SQLite initialization.
	if err := validateSharedMemory(directory, filepath.Base(abs)+"-shm"); err != nil {
		lock.Close()
		return nil, err
	}
	vfs, err := newVerifiedVFS(f)
	if err != nil {
		lock.Close()
		return nil, err
	}
	db, err := sql.Open("sqlite", stateURI(abs, vfs.name))
	if err != nil {
		lock.Close()
		vfs.close()
		return nil, ErrState
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock, vfs: vfs, file: f}
	owned = true
	if err = s.migrate(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	s.vfs.close()
	_ = s.file.Close()
	_ = unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	_ = s.lock.Close()
	return err
}
func (s *Store) migrate() error {
	var version int
	if s.db.QueryRow("PRAGMA user_version").Scan(&version) != nil {
		return ErrState
	}
	if version < 0 || version > Schema {
		return errors.New("unsupported controller state schema")
	}
	if version == 0 {
		var n int
		if s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&n) != nil || n != 0 {
			return errors.New("unsupported unversioned controller state")
		}
	}

	var page int
	if s.db.QueryRow("PRAGMA page_size").Scan(&page) != nil || page != 4096 {
		return ErrState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ErrState
	}
	defer tx.Rollback()
	if version == 0 {
		_, err = tx.Exec(`CREATE TABLE verdicts (key TEXT PRIMARY KEY, judgment TEXT NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL);
 CREATE INDEX verdict_expiry ON verdicts(expires);
 CREATE TABLE seen (key TEXT PRIMARY KEY, at INTEGER NOT NULL, decision TEXT NOT NULL);
 CREATE INDEX seen_time ON seen(at);
 CREATE TABLE incidents (key TEXT PRIMARY KEY, window INTEGER NOT NULL, count INTEGER NOT NULL, level INTEGER NOT NULL, last INTEGER NOT NULL, sequence INTEGER NOT NULL, touched INTEGER NOT NULL, last_decision TEXT CHECK(last_decision IN ('review','notify')));
 CREATE INDEX incident_time ON incidents(touched);
 CREATE TABLE outbox (id TEXT PRIMARY KEY, incident TEXT NOT NULL, payload TEXT NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL, due INTEGER NOT NULL, updated INTEGER NOT NULL);
 CREATE INDEX outbox_due ON outbox(status,due);
 CREATE INDEX outbox_time ON outbox(status,updated);
 CREATE TABLE delivery_clock (id INTEGER PRIMARY KEY CHECK(id=1), next INTEGER NOT NULL);
 INSERT INTO delivery_clock VALUES(1,0);`)
		if err != nil {
			return ErrState
		}
	}
	if version == 1 {
		if _, err = tx.Exec("ALTER TABLE incidents ADD COLUMN last_decision TEXT CHECK(last_decision IN ('review','notify'))"); err != nil {
			return ErrState
		}
	}
	// Prior verdict identities are incompatible. Queued delivery IDs/status survive;
	// notification evidence is converted to opaque metadata in the same transaction.
	if version > 0 && version < 3 {
		if err := migrateNotifications(tx); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM verdicts"); err != nil {
			return ErrState
		}
	}
	if err = installCapacity(tx); err != nil {
		return err
	}
	if _, err = tx.Exec("PRAGMA user_version=4"); err != nil {
		return ErrState
	}
	if tx.Commit() != nil {
		return ErrState
	}
	return nil
}

type Notification struct {
	Schema      int            `json:"schema"`
	ID          string         `json:"notification_id"`
	IncidentID  string         `json:"incident_id"`
	EventID     string         `json:"event_id"`
	SourceID    string         `json:"source_id"`
	Decision    string         `json:"decision"`
	Judgment    event.Judgment `json:"judgment"`
	Occurrences int            `json:"recurrence_count"`
	Created     int64          `json:"observed_at"`
}
type Applied struct {
	Duplicate, Enqueued bool
	Decision            string
}

func (s *Store) Apply(ctx context.Context, e event.Event, contract string, p Policy, now int64) (Applied, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Applied
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, ErrState
	}
	defer tx.Rollback()
	key := event.Hash([]any{"go-v1", contract, e.Source, e.Text})
	occurrence := event.Hash([]any{contract, e.Source, e.Timestamp, e.ID})
	var exists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM seen WHERE key=?", occurrence).Scan(&exists)
	if err == nil {
		result.Duplicate = true
		return result, nil
	}
	if err != sql.ErrNoRows {
		return result, ErrState
	}
	if err = makeRoom(ctx, tx, "seen", occurrence); err != nil {
		return result, err
	}
	decision := p.Decide(e)
	result.Decision = decision
	if _, err = tx.ExecContext(ctx, "INSERT INTO seen VALUES(?,?,?)", occurrence, now, decision); err != nil {
		return result, ErrState
	}
	var window, last, touched, seq int64
	var count, level int
	var previous sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT window,count,level,last,sequence,touched,last_decision FROM incidents WHERE key=?", key).Scan(&window, &count, &level, &last, &seq, &touched, &previous)
	if err != nil && err != sql.ErrNoRows {
		return result, ErrState
	}
	if decision == "notify" || decision == "review" {
		if err = makeRoom(ctx, tx, "incidents", key); err != nil {
			return result, err
		}
		if now < window || now-window >= p.Window {
			window = now
			count = 0
			level = 0
		}
		count++
		newLevel := min(p.MaxEscalation, count/p.Recurrence)
		enqueue := seq == 0 || now < last || now-last >= p.Cooldown || newLevel > level || (decision == "notify" && previous.String != "notify")
		level = max(level, newLevel)
		if enqueue {
			if err = makeRoom(ctx, tx, "outbox", ""); err != nil {
				return result, err
			}
			// A durable global sequence survives incident eviction and prevents ID reuse.
			if err = tx.QueryRowContext(ctx, "UPDATE delivery_clock SET next=next+1 WHERE id=1 RETURNING next").Scan(&seq); err != nil {
				return result, ErrState
			}
			id := notificationID(key, seq)
			n := Notification{2, id, key, e.ID, event.Hash(e.Source), decision, e.Judgment, count, now}
			payload, _ := json.Marshal(n)
			if _, err = tx.ExecContext(ctx, "INSERT INTO outbox VALUES(?,?,?,'pending',0,?,?)", id, key, string(payload), now, now); err != nil {
				return result, ErrState
			}
			last = now
			previous = sql.NullString{String: decision, Valid: true}
			result.Enqueued = true
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO incidents VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET window=excluded.window,count=excluded.count,level=excluded.level,last=excluded.last,sequence=excluded.sequence,touched=excluded.touched,last_decision=excluded.last_decision`, key, window, count, level, last, seq, now, previous); err != nil {
			return result, ErrState
		}
	}
	if tx.Commit() != nil {
		return result, ErrState
	}
	return result, nil
}
func (s *Store) PutVerdict(ctx context.Context, key string, j event.Judgment, now int64, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !j.Reusable() || !ValidID(key) || ttl <= 0 || ttl > 7*24*time.Hour {
		return errors.New("unsafe verdict cache entry")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrState
	}
	defer tx.Rollback()
	if err = makeRoom(ctx, tx, "verdicts", key); err != nil {
		return err
	}
	raw, _ := json.Marshal(j)
	_, err = tx.ExecContext(ctx, "INSERT INTO verdicts VALUES(?,?,?,?) ON CONFLICT(key) DO UPDATE SET judgment=excluded.judgment,created=excluded.created,expires=excluded.expires", key, string(raw), now, now+int64(ttl.Seconds()))
	if err != nil {
		return ErrState
	}
	if tx.Commit() != nil {
		return ErrState
	}
	return nil
}
func (s *Store) Lookup(ctx context.Context, key string, now int64, ttl time.Duration) (*event.Judgment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var created, expires int64
	err := s.db.QueryRowContext(ctx, "SELECT substr(judgment,1,4097),created,expires FROM verdicts WHERE key=?", key).Scan(&raw, &created, &expires)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, ErrState
	}
	if len(raw) > 4096 || now < created || now >= expires || now-created >= int64(ttl.Seconds()) {
		return nil, nil
	}
	var j event.Judgment
	if provider.Strict([]byte(raw), &j) != nil || !j.Reusable() {
		return nil, nil
	}
	return &j, nil
}
