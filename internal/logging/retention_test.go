package logging

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// A record written after midnight goes to the new day's file: retention deletes
// whole files by their date, so a file must never hold two days.
func TestRecordsRotateAtMidnight(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 23, 59, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)

	m.Logger().Info("before midnight")
	clock.t = clock.t.Add(2 * time.Minute)
	m.Logger().Info("after midnight")

	first := readFile(t, filepath.Join(dir, "qqbot-2026-10-12.log"))
	second := readFile(t, filepath.Join(dir, "qqbot-2026-10-13.log"))
	if !strings.Contains(first, "before midnight") || strings.Contains(first, "after midnight") {
		t.Errorf("day 1 file = %q", first)
	}
	if !strings.Contains(second, "after midnight") {
		t.Errorf("day 2 file = %q", second)
	}
}

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Keeping N days means today plus the N-1 days before it. The boundary is
// tested on both sides: the oldest kept day and the first deleted one.
func TestCleanupKeepsExactlyRetentionDays(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	for _, day := range []string{"2026-10-12", "2026-10-11", "2026-10-06", "2026-10-05", "2026-09-01"} {
		touch(t, dir, fileName(day))
	}

	removed, err := m.Cleanup(7)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed %d files, want 2", removed)
	}
	want := []string{"qqbot-2026-10-06.log", "qqbot-2026-10-11.log", "qqbot-2026-10-12.log"}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Errorf("left %v, want %v", got, want)
	}
}

// Cleanup deletes only files it wrote itself: the operator may keep notes or an
// exported copy in the same directory, and a sweep that matched loosely would
// destroy them.
func TestCleanupLeavesForeignFilesAlone(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	for _, name := range []string{"notes.txt", "qqbot-old.log", "qqbot-2020-01-01.log.bak", "other-2020-01-01.log"} {
		touch(t, dir, name)
	}
	if _, err := m.Cleanup(1); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); len(got) != 4 {
		t.Errorf("foreign files were touched: %v", got)
	}
}

// Retention 0 means keep forever, so a sweep with nothing configured is a no-op
// rather than "keep zero days", which would delete today's file.
func TestCleanupZeroKeepsEverything(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2000-01-01"))
	removed, err := m.Cleanup(0)
	if err != nil || removed != 0 {
		t.Fatalf("Cleanup(0) = %d, %v", removed, err)
	}
	if got := listDir(t, dir); len(got) != 1 {
		t.Errorf("Cleanup(0) deleted files: %v", got)
	}
}
