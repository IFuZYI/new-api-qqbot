package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A day file INSIDE the retention window must be tightened by the sweep.
//
// This is the gap the Chmod-on-open cannot close: a past day's file is never
// written again, so dayFile.Write never touches it, and an operator upgrading
// from a version that wrote 0644 keeps that mode on every file older than today
// — the whole retention window, which is exactly the data the hardening exists
// for. Found by running the real service: after the fix, today's file was 0600
// and yesterday's was still 0644.
func TestCleanupTightensFilesInsideTheWindow(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	// Three files: expired, the first kept day, and today. All world-readable,
	// as an older version would have left them.
	for _, day := range []string{"2026-10-08", "2026-10-10", "2026-10-12"} {
		p := filepath.Join(dir, fileName(day))
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := m.Cleanup(3)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed %d files, want 1 (only 2026-10-08 is outside a 3-day window)", removed)
	}

	// The kept files must now be owner-only.
	for _, day := range []string{"2026-10-10", "2026-10-12"} {
		fi, err := os.Stat(filepath.Join(dir, fileName(day)))
		if err != nil {
			t.Fatalf("%s: %v", day, err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600 (the sweep must tighten the window it keeps)", day, got)
		}
	}
}

// The sweep must not touch a foreign file's mode either: it is not this
// package's file to change.
func TestCleanupDoesNotTouchForeignFiles(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	foreign := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(foreign, []byte("operator notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Cleanup(3); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("a foreign file's mode was changed to %04o; only files this package wrote may be touched", got)
	}
}
