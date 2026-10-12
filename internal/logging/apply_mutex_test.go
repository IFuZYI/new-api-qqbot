package logging

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Overlapping sweeps must not report a cleanup failure for a file that is gone.
//
// Two Applies run concurrently in production: the hourly maintenance tick and a
// settings save. Both delete the same expired files, and the loser's os.Remove
// returns ENOENT — which is the outcome the sweep wanted, not a failure. The
// existing TestConcurrentAppliesDoNotLogFalseErrors used only 4 files, and an
// independent reviewer measured the tolerance being exercised 0/100 times at that
// size (it also detected removing the whole applyMu in 0 runs).
//
// This uses the reviewer's measured shape — many expired files, many concurrent
// sweeps — so the race actually fires. Mutation-verified: with applyMu removed
// this test fails on the false ERROR.
func TestOverlappingSweepsDoNotLogFalseErrors(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	// 23 expired files: the reviewer's measured fixture, where 38% of overlapping
	// pairs logged a false ERROR before serialisation.
	for i := 1; i <= 23; i++ {
		touch(t, dir, fileName(fmt.Sprintf("2026-09-%02d", i)))
	}

	const sweeps = 8
	done := make(chan struct{}, sweeps)
	for i := 0; i < sweeps; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			m.Apply(Policy{Level: LevelInfo, RetentionDays: 3})
		}()
	}
	for i := 0; i < sweeps; i++ {
		<-done
	}

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.Level == "ERROR" {
			t.Errorf("an overlapping sweep logged a false ERROR: %s %+v", e.Msg, e.Attrs)
		}
	}
	// Every expired file must be gone, so the assertion above cannot pass by
	// doing no work at all.
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range left {
		if day, ok := dayOf(e.Name()); ok && day < "2026-10-10" {
			t.Errorf("an expired file survived the sweeps: %s", e.Name())
		}
	}
}
