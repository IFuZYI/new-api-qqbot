package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The log directory and the day files must be owner-only.
//
// The day files hold group and member identifiers — QQ numbers and nicknames —
// for the whole retention period, and the panel can search them. They sit beside
// data/qqbot.db, which this codebase hardens to 0600 because it holds the cookie
// signing key; leaving the logs at 0644 in a 0755 directory meant any local user
// could read every member's identifiers out of the log directory while the
// database beside them was protected.
func TestTheLogDirectoryAndDayFilesAreOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	m, err := Open(Options{Dir: dir, Stderr: os.Stderr, Now: func() time.Time {
		return time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.Logger().Info("hello", "qq", 54321)

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("log directory mode = %04o, want 0700", got)
	}
	fi, err := os.Stat(filepath.Join(dir, fileName("2026-10-12")))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("day file mode = %04o, want 0600 (it holds member identifiers for the retention period)", got)
	}
}

// Reopening an existing world-readable day file must tighten it, so an operator
// upgrading from a version that created it 0644 gets the fix without a manual
// chmod. The file is opened O_APPEND rather than recreated, so the mode has to be
// applied explicitly.
func TestReopeningTightensAnExistingDayFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName("2026-10-12"))
	if err := os.WriteFile(path, []byte("{\"level\":\"INFO\",\"msg\":\"old\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Open(Options{Dir: dir, Stderr: os.Stderr, Now: func() time.Time {
		return time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.Logger().Info("append to the existing file")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("after appending, mode = %04o, want 0600", got)
	}
	// The existing content must survive: this is an append, not a recreate.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(body), "\"msg\":\"old\"") {
		t.Errorf("the existing record was lost: %s", body)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
