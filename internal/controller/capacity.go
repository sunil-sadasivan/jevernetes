package controller

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

const stateCapacity = 10000

var ErrBackpressure = errors.New("controller pending outbox capacity reached")

// Counts and eviction counters change in the same transaction as the rows.
// Reconcile once at open; triggers avoid per-event full-table scans.
func installCapacity(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS state_counts (name TEXT PRIMARY KEY, rows INTEGER NOT NULL, evicted INTEGER NOT NULL DEFAULT 0);
 UPDATE delivery_clock SET next=max(next,coalesce((SELECT max(sequence) FROM incidents),0));
 DROP INDEX IF EXISTS seen_time;
 DROP INDEX IF EXISTS incident_time;
 DROP INDEX IF EXISTS verdict_expiry;
 CREATE INDEX IF NOT EXISTS seen_eviction ON seen(at,key);
 CREATE INDEX IF NOT EXISTS incident_eviction ON incidents(touched,key);
 CREATE INDEX IF NOT EXISTS verdict_eviction ON verdicts(expires,key);
 CREATE INDEX IF NOT EXISTS outbox_terminal ON outbox(updated,id) WHERE status!='pending';`); err != nil {
		return ErrState
	}
	for _, table := range []string{"seen", "incidents", "verdicts", "outbox"} {
		if _, err := tx.Exec("INSERT INTO state_counts(name,rows) SELECT ?,count(*) FROM "+table+" WHERE true ON CONFLICT(name) DO UPDATE SET rows=excluded.rows", table); err != nil {
			return ErrState
		}
		for _, op := range []struct{ name, delta string }{{"INSERT", "+1"}, {"DELETE", "-1"}} {
			if _, err := tx.Exec("CREATE TRIGGER IF NOT EXISTS count_" + table + "_" + op.name + " AFTER " + op.name + " ON " + table + " BEGIN UPDATE state_counts SET rows=rows" + op.delta + " WHERE name='" + table + "'; END"); err != nil {
				return ErrState
			}
		}
	}
	return nil
}
func makeRoom(ctx context.Context, tx *sql.Tx, table, key string) error {
	id, order, where := "key", "at,key", ""
	switch table {
	case "incidents":
		order = "touched,key"
	case "verdicts":
		order = "expires,key"
	case "outbox":
		id, order, where = "id", "updated,id", " WHERE status!='pending'"
	}
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE "+id+"=?", key).Scan(&exists)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return ErrState
	}
	var n int
	if tx.QueryRowContext(ctx, "SELECT rows FROM state_counts WHERE name=?", table).Scan(&n) != nil {
		return ErrState
	}
	if n < stateCapacity {
		return nil
	}
	// Deterministic oldest-first eviction. Pending delivery intent is never eligible.
	result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+id+" IN (SELECT "+id+" FROM "+table+where+" ORDER BY "+order+" LIMIT ?)", n-stateCapacity+1)
	if err != nil {
		return ErrState
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return ErrState
	}
	if int64(n)-removed >= stateCapacity {
		return ErrBackpressure
	}
	if _, err = tx.ExecContext(ctx, "UPDATE state_counts SET evicted=evicted+? WHERE name=?", removed, table); err != nil {
		return ErrState
	}
	return nil
}
func (s *Store) Metrics() map[string]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]uint64{}
	rows, err := s.db.Query("SELECT name,evicted FROM state_counts")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n uint64
		if rows.Scan(&name, &n) == nil {
			out["state_"+name+"_evicted"] = n
		}
	}
	return out
}

func notificationID(key string, seq int64) string {
	return event.Hash([]any{"go-notification-v2", key, seq})
}
