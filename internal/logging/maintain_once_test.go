package logging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The caller applies the policy once at start-up (so the level is in force and
// the retention sweep has run before the server accepts requests), and Maintain
// then owns the ticks. It must NOT apply again on entry.
//
// The duplicate is observable: two sweeps in the same second record two
// "cleaned up expired logs" lines with different counts for one start-up, which
// reads as two separate sweeps having deleted different things.
//
// The test makes the second sweep DO something, because a second sweep that
// finds nothing to delete records nothing and the defect hides: a file appears
// between the caller's apply and the loop's entry, so an on-entry apply deletes
// it and records a second line.
func TestMaintainDoesNotRepeatTheStartupApply(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2026-10-01"))
	touch(t, dir, fileName("2026-10-12"))

	// Exactly what cmd/qqbot does at start-up: apply once, then hand the loop
	// the same policy. This deletes 2026-10-01 and records one sweep.
	m.Apply(Policy{Level: LevelInfo, RetentionDays: 3})

	// A file that appears after the caller's apply but before the loop starts
	// (a day rolling over at that instant, or a restored file). An on-entry
	// apply deletes it; a tick-only loop leaves it for the next tick.
	touch(t, dir, fileName("2026-09-01"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.maintain(ctx, time.Hour, func() Policy { return Policy{Level: LevelInfo, RetentionDays: 3} })
	}()
	time.Sleep(250 * time.Millisecond)
	cancel()
	<-done

	if _, err := os.Stat(filepath.Join(dir, fileName("2026-09-01"))); os.IsNotExist(err) {
		t.Error("the loop applied the policy on entry: the file it must not have swept is gone")
	}

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	sweeps := 0
	for _, e := range res.Entries {
		if e.Msg == "已清理过期日志" {
			sweeps++
		}
	}
	if sweeps != 1 {
		t.Errorf("the start-up sweep was recorded %d times, want exactly 1", sweeps)
	}
	if _, err := os.Stat(filepath.Join(dir, fileName("2026-10-01"))); !os.IsNotExist(err) {
		t.Error("the expired file survived the sweep")
	}
	if _, err := os.Stat(filepath.Join(dir, fileName("2026-10-12"))); err != nil {
		t.Error("the current file was deleted")
	}
}

// The tick still applies the policy: that is what catches a day rolling over.
func TestMaintainAppliesOnTick(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.maintain(ctx, 20*time.Millisecond, func() Policy { return Policy{Level: LevelDebug, RetentionDays: 0} })
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && m.Level() != LevelDebug {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if m.Level() != LevelDebug {
		t.Errorf("the tick did not apply the policy (level = %q)", m.Level())
	}
}
