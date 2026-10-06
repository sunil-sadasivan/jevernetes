package controller

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func checkConnectionPragmas(t *testing.T, s *Store) {
	t.Helper()
	for name, want := range map[string]int{"busy_timeout": 100, "page_size": 4096, "synchronous": 2, "secure_delete": 1, "max_page_count": 65536, "journal_size_limit": 1048576, "temp_store": 2} {
		var got int
		if err := s.db.QueryRow("PRAGMA " + name).Scan(&got); err != nil || got != want {
			t.Errorf("%s: got %d want %d (%v)", name, got, want, err)
		}
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatal(mode, err)
	}
}
func TestInterruptedAndReplacedConnectionsRestorePragmas(t *testing.T) {
	s := openTest(t)
	checkConnectionPragmas(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var n int
	err := s.db.QueryRowContext(ctx, "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT sum(x) FROM n").Scan(&n)
	if err == nil || ctx.Err() == nil {
		t.Fatal("query did not interrupt", err)
	}
	// Driver may safely reuse or discard after sqlite3_interrupt. Either path
	// must restore the connection contract and permit a real policy transaction.
	checkConnectionPragmas(t, s)
	if _, err := s.Apply(context.Background(), occurrence(812), "fixture", DefaultPolicy(), 1000); err != nil {
		t.Fatal(err)
	}
	// Force replacement too; relying on interruption to discard is driver-specific.
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(any) error { return driver.ErrBadConn })
	conn.Close()
	if !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	checkConnectionPragmas(t, s)
	if _, err := s.Apply(context.Background(), occurrence(813), "fixture", DefaultPolicy(), 1400); err != nil {
		t.Fatal(err)
	}
}
