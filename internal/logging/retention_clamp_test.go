package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An absurd retention value must not delete today's file.
//
// `cutoff` is computed as `today.Day() - (keepDays-1)`, so a huge keepDays
// overflows the int and wraps the cutoff FORWARD — MaxInt64 produced a cutoff of
// "2026-10-14" on a host at 2026-10-12, which makes today's file expired. The
// service bounds the setting to 0..3650, so this is Cleanup being safe on its own
// terms; the clamp is what makes that true for any caller.
func TestAnAbsurdRetentionValueCannotDeleteTodaysFile(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	today := fileName("2026-10-12")
	touch(t, dir, today)

	for _, keep := range []int{1 << 62, int(^uint(0) >> 1), MaxRetentionDays + 1, 1 << 40} {
		removed, err := m.Cleanup(keep)
		if err != nil {
			t.Fatalf("Cleanup(%d): %v", keep, err)
		}
		if removed != 0 {
			t.Errorf("Cleanup(%d) removed %d files, want 0", keep, removed)
		}
		if _, err := os.Stat(filepath.Join(dir, today)); err != nil {
			t.Fatalf("Cleanup(%d) deleted today's file: %v", keep, err)
		}
	}
}

// The clamp must not change the ordinary cases: the boundary still counts today
// as the first kept day.
func TestTheRetentionClampKeepsTheOrdinaryBoundary(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2026-10-09")) // 4 days back: outside a 3-day window
	touch(t, dir, fileName("2026-10-10")) // 3 days back: the first kept day
	touch(t, dir, fileName("2026-10-12")) // today

	removed, err := m.Cleanup(3)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("Cleanup(3) removed %d files, want 1", removed)
	}
	for _, day := range []string{"2026-10-10", "2026-10-12"} {
		if _, err := os.Stat(filepath.Join(dir, fileName(day))); err != nil {
			t.Errorf("%s should have been kept: %v", day, err)
		}
	}
}
