package logging

import (
	"context"
	"testing"
	"time"
)

// The maintenance loop is what keeps the settings live while the service runs:
// each tick re-reads the stored policy and applies it (level and retention).
// The start-up apply belongs to the caller (see Maintain's doc), so this pins
// the tick behaviour only.
func TestMaintainAppliesThePolicyOnEveryTick(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2026-10-01"))
	touch(t, dir, fileName("2026-10-12"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.maintain(ctx, 20*time.Millisecond, func() Policy { return Policy{Level: LevelDebug, RetentionDays: 1} })
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(listDir(t, dir)) == 1 && m.Level() == LevelDebug {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if m.Level() != LevelDebug {
		t.Errorf("level = %q, want debug", m.Level())
	}
	if got := listDir(t, dir); len(got) != 1 || got[0] != fileName("2026-10-12") {
		t.Errorf("files after the sweep = %v", got)
	}
}

// A policy with a level the logger refuses leaves the current level alone: the
// settings validation should have caught it, and a bad stored value must not
// silence the log.
func TestApplyPolicyIgnoresBadLevel(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)
	m.Apply(Policy{Level: "nonsense", RetentionDays: 0})
	if m.Level() != LevelInfo {
		t.Errorf("level = %q after a bad policy", m.Level())
	}
}
