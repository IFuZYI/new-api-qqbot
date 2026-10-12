package logging

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openIn opens a Manager whose day boundary is the operator's timezone, not the
// process clock's.
func openIn(t *testing.T, clock *fixedClock, loc *time.Location) (*Manager, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	m, err := Open(Options{Dir: dir, Stderr: io.Discard, Now: clock.now, Location: loc})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, dir
}

// The day a record belongs to is the operator's day, not the host's. The host
// runs on UTC while the business day is Asia/Shanghai, so a record written at
// 00:30 Beijing (= 16:30 UTC) belongs to the Beijing day — the same day the
// check-in rows and the panel's "今天" use.
func TestDayFollowsOperatorTimezone(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	clock := &fixedClock{t: time.Date(2026, 10, 12, 16, 30, 0, 0, time.UTC)} // 10-13 00:30 +08
	m, dir := openIn(t, clock, shanghai)

	m.Logger().Info("just after midnight in Beijing")

	if _, err := os.Stat(filepath.Join(dir, "qqbot-2026-10-13.log")); err != nil {
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("record not in the operator's day (2026-10-13); files: %v", names)
	}
}

// The panel's default day is the operator's today too, so opening the log page
// at 00:30 Beijing shows the file that is being written.
func TestDefaultQueryDayFollowsTimezone(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	clock := &fixedClock{t: time.Date(2026, 10, 12, 16, 30, 0, 0, time.UTC)}
	m, _ := openIn(t, clock, shanghai)

	res, err := m.Query(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Day != "2026-10-13" {
		t.Errorf("default day = %q, want the operator's 2026-10-13", res.Day)
	}
}

// Retention counts days on the operator's calendar: the boundary is their
// midnight, so a file is never deleted while it is still one of the kept days.
func TestCleanupBoundaryUsesOperatorDay(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 16:30 UTC on 10-12 is 00:30 on 10-13 in Beijing. Keeping 3 days keeps
	// 10-11, 10-12 and 10-13; 10-10 must go.
	clock := &fixedClock{t: time.Date(2026, 10, 12, 16, 30, 0, 0, time.UTC)}
	m, dir := openIn(t, clock, shanghai)
	for _, day := range []string{"2026-10-13", "2026-10-12", "2026-10-11", "2026-10-10"} {
		touch(t, dir, fileName(day))
	}
	removed, err := m.Cleanup(3)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed %d, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, fileName("2026-10-11"))); err != nil {
		t.Error("the oldest kept day was deleted: the boundary used the host day")
	}
	if _, err := os.Stat(filepath.Join(dir, fileName("2026-10-10"))); !os.IsNotExist(err) {
		t.Error("a day past the boundary survived")
	}
}
