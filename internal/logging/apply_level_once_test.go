package logging

import (
	"testing"
	"time"
)

// Concurrent Applies must record a level change exactly ONCE.
//
// Every Apply reads the current level, sets the new one and announces the change
// if they differ. Without serialisation two overlapping Applies both read the old
// level, both see the new one land, and both record 日志等级已生效 — so the
// operator's log page shows the switch being flipped twice. This is what
// applyMu is load-bearing for.
//
// (The other thing its comment claims — that overlapping sweeps log a false
// ENOENT ERROR — is handled by removeExpired's tolerance, verified separately:
// with applyMu removed this package's other tests still pass on that count.)
//
// The race window is a single level comparison, so ONE round catches it only
// occasionally (measured: ~1 run in 10 with 64 goroutines behind a start
// barrier). The test therefore runs many independent rounds — each is
// microseconds, and the per-round probability compounds.
func TestConcurrentAppliesRecordTheLevelChangeOnce(t *testing.T) {
	const rounds = 200
	const workers = 64

	for round := 0; round < rounds; round++ {
		clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
		m, _ := openTest(t, clock)
		if m.Level() != LevelInfo {
			t.Fatalf("round %d: precondition level = %q, want info", round, m.Level())
		}

		// A start barrier, so all the Applies pile up at once: without it the
		// goroutines run mostly one after another and the race never fires
		// (measured: removing applyMu still passed 5/5 runs without this).
		start := make(chan struct{})
		done := make(chan struct{}, workers)
		for i := 0; i < workers; i++ {
			go func() {
				defer func() { done <- struct{}{} }()
				<-start
				m.Apply(Policy{Level: LevelDebug, RetentionDays: 0})
			}()
		}
		close(start)
		for i := 0; i < workers; i++ {
			<-done
		}

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
			t.Fatalf("round %d: the level change was recorded %d times by %d concurrent applies, want exactly 1", round, n, workers)
		}
		if got := m.Level(); got != LevelDebug {
			t.Fatalf("round %d: level = %q, want debug", round, got)
		}
		_ = m.Close()
	}
}
