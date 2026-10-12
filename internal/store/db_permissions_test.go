package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The database file, its WAL/SHM siblings and the data directory must be
// owner-only.
//
// The database holds the session-cookie signing key (settings.auth.session_key),
// the plaintext gateway password and a live 30-day gateway session — strictly
// more than data/config.yaml, which this codebase already hardens to 0600. With
// SQLite's default (the process umask, 0644 on the documented bare-metal run)
// any local user could read the signing key and mint an admin cookie without
// the password. An independent reviewer measured 0644 on all three files.
func TestDatabaseFilesAreOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := Open(filepath.Join(dir, "qqbot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// A write makes SQLite materialise the WAL/SHM siblings.
	if err := st.PutSetting("mode-probe", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("data dir mode = %04o, want 0700", got)
	}

	for _, name := range []string{"qqbot.db", "qqbot.db-wal", "qqbot.db-shm"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			// SQLite may have checkpointed the WAL away; only the database
			// itself is mandatory.
			if name == "qqbot.db" {
				t.Fatalf("%s is missing: %v", name, err)
			}
			continue
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600 (it holds the cookie signing key and the gateway password)", name, got)
		}
	}
}

// Reopening an existing world-readable database must tighten it: an operator
// upgrading from a version that created it 0644 gets the fix without a manual
// chmod.
func TestReopeningTightensAnExistingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "qqbot.db")

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// Simulate the old, world-readable state.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("after reopening, mode = %04o, want 0600", got)
	}
}
