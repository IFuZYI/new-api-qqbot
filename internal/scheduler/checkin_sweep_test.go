package scheduler

import (
	"testing"
	"time"
)

// The check-in patrol does two things: read the sign-in board (a QQ request per
// monitored group) and pay the day's rewards. They are separable, and the
// operator may legitimately want only one:
//
//   - the board is the expensive call, and a group without a board errors on
//     every poll, so "message check-ins only" is a normal configuration;
//   - rewards, however, must still be paid - a check-in that is never paid is
//     the failure mode where people sign in and get nothing.
//
// So the loop must keep running while either path is on, and only the board read
// may be skipped.
func TestCheckinIntervalRunsWhileEitherPathIsOn(t *testing.T) {
	sched, _, svc := newScheduler(t)

	// Board on, message off: runs.
	s, _ := svc.Settings()
	s.CheckinPatrolEnabled, s.CheckinByMessageEnabled = true, false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.checkinInterval(); got <= 0 {
		t.Errorf("board patrol on but interval is %s", got)
	}

	// Board off, message on: still runs, so message check-ins get paid.
	s.CheckinPatrolEnabled, s.CheckinByMessageEnabled = false, true
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.checkinInterval(); got <= 0 {
		t.Errorf("message check-ins are on but the reward loop is disabled (interval %s)", got)
	}

	// Both off: nothing to do, so no requests.
	s.CheckinPatrolEnabled, s.CheckinByMessageEnabled = false, false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.checkinInterval(); got != 0 {
		t.Errorf("both check-in paths are off but the loop still runs (%s)", got)
	}
}

// With the board patrol switched off, the sweep must not read a single board -
// that is the whole point of switching it off - while still paying. Rewards for
// message check-ins must not depend on the expensive path being on.
func TestBoardReadIsSkippedWhenThePatrolIsOff(t *testing.T) {
	resetStubQQ()
	sched, st, svc := newScheduler(t)
	enableSchedGroup(t, st, 1)

	// A message check-in for a member who has an account: there is a real reward
	// to pay, so "did the award step run" is observable.
	enableSchedMember(t, st, 1, 54321)
	enableSchedBinding(t, st, 54321, 7)
	if _, err := svc.CheckinByMessage(1, 54321); err != nil {
		t.Fatal(err)
	}

	s, _ := svc.Settings()
	s.CheckinPatrolEnabled = false
	s.CheckinByMessageEnabled = true
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	if err := sched.runCheckinSweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	// The stub QQ would have answered, so a call here is a real board request.
	if got := stubBoardCalls(); got != 0 {
		t.Errorf("the board was read %d times with the patrol off", got)
	}
	// And the reward must still have been paid: a check-in nobody pays is the
	// failure this split exists to prevent.
	if !stubAwardRan() {
		t.Error("a message check-in was not paid while the board patrol was off")
	}
}

// The other direction: with the board patrol on, the sweep does read it.
func TestBoardIsReadWhenThePatrolIsOn(t *testing.T) {
	resetStubQQ()
	sched, st, svc := newScheduler(t)
	enableSchedGroup(t, st, 1)

	s, _ := svc.Settings()
	s.CheckinPatrolEnabled = true
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	if err := sched.runCheckinSweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := stubBoardCalls(); got == 0 {
		t.Error("the board patrol is on but the board was never read")
	}
}

// A failing board read must not stop the reward step: they are independent, and
// losing a reward is worse than a stale board.
func TestAwardRunsEvenWhenTheBoardFails(t *testing.T) {
	resetStubQQ()
	sched, st, svc := newScheduler(t)
	enableSchedGroup(t, st, 1)

	// A check-in for a member who has an account, so the award step has real
	// work to do and reaching the gateway is observable.
	enableSchedMember(t, st, 1, 54321)
	enableSchedBinding(t, st, 54321, 7)
	if _, err := svc.CheckinByMessage(1, 54321); err != nil {
		t.Fatal(err)
	}

	s, _ := svc.Settings()
	s.CheckinPatrolEnabled = true
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	setStubBoardErr(errString("board unreachable"))

	err := sched.runCheckinSweep()
	if err == nil {
		t.Error("the board failure must still be reported")
	}
	if !stubAwardRan() {
		t.Error("the reward step must run even when the board read failed")
	}
}

// The interval the reward loop uses when only message check-ins are on is the
// check-in cadence: rewards are paid on that rhythm, not on the roster's.
func TestMessageOnlyRewardLoopUsesTheCheckinCadence(t *testing.T) {
	sched, _, svc := newScheduler(t)

	s, _ := svc.Settings()
	s.CheckinPatrolEnabled = false
	s.CheckinByMessageEnabled = true
	s.CheckinSyncMinutes = 11
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.checkinInterval(); got != 11*time.Minute {
		t.Errorf("interval = %s, want 11m", got)
	}
}
