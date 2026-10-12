package logging

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An expired file that is already gone is a SUCCESS, deterministically.
//
// Two sweeps can overlap (a settings save applies the policy while the hourly
// maintenance tick runs) and both delete the same expired files; the loser's
// os.Remove returns ENOENT. Reporting that as a failure would put a false ERROR
// in the one view the operator is meant to trust.
//
// The previous version of this test raced two Cleanup calls, so the ENOENT
// landed in whichever goroutine lost: an independent reviewer measured the
// tolerance being exercised 0/100 times, and detected the removal of the
// os.ErrNotExist branch in only ~3% of single runs. Calling the removal directly
// makes it deterministic.
func TestRemoveExpiredTreatsAlreadyGoneAsSuccess(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "qqbot-2026-10-01.log")
	if err := os.Remove(missing); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: %v", err)
	}
	m := &Manager{dir: dir}
	if err := m.removeExpired(missing); err != nil {
		t.Errorf("removing an already-gone file = %v, want nil (the file is gone, which is the outcome the sweep wanted)", err)
	}

	// A REAL failure must still surface, or the tolerance has swallowed the
	// whole error path. A non-empty directory gives ENOTEMPTY, which is not
	// ErrNotExist and is deterministic — a chmod-based failure is useless here
	// because the test runs as root, which bypasses the permission bits.
	real := filepath.Join(dir, "qqbot-2026-10-02.log")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.removeExpired(real); err == nil {
		t.Error("a real removal failure was swallowed by the tolerance")
	}
}

// A DIRECTORY named like a day file must survive: the sweep only deletes files
// this package wrote, and an operator's own directory in the log dir is not one.
//
// The e.IsDir() guard was unpinned: removing it made Cleanup call os.Remove on a
// directory, and the existing test's fixture contained only files.
func TestCleanupLeavesADirectoryNamedLikeADayFile(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	sub := filepath.Join(dir, fileName("2026-10-01"))
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file inside it, so an accidental recursive delete would be visible too.
	if err := os.WriteFile(filepath.Join(sub, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cleanup(3); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
		t.Errorf("a directory named like an expired day file was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, "keep.txt")); err != nil {
		t.Errorf("the contents of that directory were removed: %v", err)
	}
}
