package store

import (
	"path/filepath"
	"testing"
)

// The SQLite pragmas must be in force on the live connection.
//
// The concurrency test pins the OBSERVABLE property (writers do not fail with
// "database is locked"), and a mutation check showed it only pins the single
// connection: setting busy_timeout(0) or switching WAL back to DELETE still
// passed. Both matter for the multi-process case — a second instance, or the
// operator's own sqlite3 session — so they are asserted directly.
func TestConnectionPragmasAreInForce(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pragma.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// busy_timeout: how long a writer waits for a lock before failing. Zero
	// means "fail immediately", which is what made concurrent writers lose rows.
	var timeout int
	if err := st.db.Raw("PRAGMA busy_timeout").Scan(&timeout).Error; err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if timeout < 1000 {
		t.Errorf("busy_timeout = %d ms; a second writer (another instance, or sqlite3) would be refused instead of waiting", timeout)
	}

	// WAL: readers do not block the writer and vice versa.
	var mode string
	if err := st.db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	// foreign_keys is on, so an orphan row cannot be written silently.
	var fk int
	if err := st.db.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

// One connection only: the member and check-in patrols write from separate
// goroutines, and with two connections each can hold a read lock and then both
// try to upgrade, which deadlocks (busy_timeout does not cover it).
func TestThePoolIsSerialisedToOneConnection(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sqlDB, err := st.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1: two connections can deadlock on a read-then-write upgrade", got)
	}
}
