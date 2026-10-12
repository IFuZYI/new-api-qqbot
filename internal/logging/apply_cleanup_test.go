package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A refused level must not be announced as having taken effect: the record would
// say "已生效 new_level=info" right next to the warning that the value was
// ignored, which reads as the opposite of what happened.
func TestApplyDoesNotRecordARefusedLevel(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)

	m.Apply(Policy{Level: "verbose", RetentionDays: 0})

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.Msg == "日志等级已生效" {
			t.Errorf("a refused level was announced as in force: %+v", e)
		}
	}
	if m.Level() != LevelInfo {
		t.Errorf("level = %q, want the unchanged info", m.Level())
	}
}

// A level already in force is not a change, whatever spelling it arrives in.
// The maintenance loop re-applies the stored policy hourly, so a comparison
// against the raw stored string would record a "change" 24 times a day.
func TestApplyRecordsNothingWhenTheLevelIsUnchanged(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)

	// "DEBUG" (the spelling a form may carry) and "debug" are the same level.
	m.Apply(Policy{Level: LevelDebug, RetentionDays: 0})
	m.Apply(Policy{Level: "DEBUG", RetentionDays: 0})
	m.Apply(Policy{Level: " debug ", RetentionDays: 0})

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range res.Entries {
		if e.Msg == "日志等级已生效" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the level change was recorded %d times, want exactly 1", n)
	}
}

// Two sweeps can overlap: a settings save applies the policy while the hourly
// maintenance tick runs, and both delete the same expired files. The loser's
// os.Remove returns ENOENT, which must not be reported as a cleanup failure —
// the file is gone, which is the outcome the sweep wanted.
func TestCleanupTreatsAMissingFileAsSuccess(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	// A directory entry that vanishes between ReadDir and Remove: a subdirectory
	// named like a day file cannot be removed as a file, and a concurrent sweep
	// is modelled by removing the file ourselves mid-loop. Simplest faithful
	// model: remove the file after ReadDir by racing a second Cleanup.
	touch(t, dir, fileName("2026-10-01"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Cleanup(3)
	}()
	removed, err := m.Cleanup(3)
	<-done

	if err != nil {
		t.Errorf("a file already gone was reported as a failure: %v", err)
	}
	// One of the two removed it; the other found nothing to do.
	if removed > 1 {
		t.Errorf("removed %d files, want at most 1", removed)
	}
	if _, statErr := os.Stat(filepath.Join(dir, fileName("2026-10-01"))); !os.IsNotExist(statErr) {
		t.Error("the expired file survived")
	}
}

// A genuine removal failure is still reported.
func TestCleanupReportsARealFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unwritable directory cannot be simulated")
	}
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2026-10-01"))
	// Make the directory itself unwritable so Remove fails with EACCES.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := m.Cleanup(3); err == nil {
		t.Error("a real removal failure was swallowed")
	}
}

// Concurrent applies must not turn a successful sweep into an ERROR record.
func TestConcurrentAppliesDoNotLogFalseErrors(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	for _, day := range []string{"2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04"} {
		touch(t, dir, fileName(day))
	}

	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			m.Apply(Policy{Level: LevelInfo, RetentionDays: 3})
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.Level == "ERROR" {
			t.Errorf("an overlapping sweep logged an ERROR: %s %+v", e.Msg, e.Attrs)
		}
	}
}
