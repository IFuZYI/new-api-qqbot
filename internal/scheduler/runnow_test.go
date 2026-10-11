package scheduler

import (
	"testing"

	"qqbot/internal/service"
)

// A manual test must run the patrol's real sweep — the same body the loop runs —
// and record it, or the operator clicks 测试一次, sees a toast, and the row still
// says 尚未运行: they cannot tell whether the test actually did anything.
func TestRunNowRunsTheSweepAndRecordsIt(t *testing.T) {
	sched, st, _ := newScheduler(t)
	resetStubQQ()
	enableSchedGroup(t, st, 1)
	enableSchedMember(t, st, 1, 54321)

	res, err := sched.RunNow("bind")
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	br, ok := res.(service.BindResult)
	if !ok {
		t.Fatalf("the result must be the sweep's own, got %T", res)
	}
	// The stub gateway knows no account, so the member is pending registration.
	if br.Pending != 1 {
		t.Errorf("pending = %d, want 1 (%+v)", br.Pending, br)
	}

	entry, ok := sched.Status()["bind"].(map[string]any)
	if !ok {
		t.Fatalf("no status entry for bind: %+v", sched.Status())
	}
	if _, ok := entry["last_run"]; !ok {
		t.Error("a manual run must be recorded, so the health badge stops saying 尚未运行")
	}
	if _, bad := entry["last_error"]; bad {
		t.Errorf("a successful run must not leave an error: %+v", entry)
	}
}

// An unknown name must be refused, and must not run some other sweep instead.
func TestRunNowRejectsAnUnknownPatrol(t *testing.T) {
	sched, st, _ := newScheduler(t)
	resetStubQQ()
	enableSchedGroup(t, st, 1)
	enableSchedMember(t, st, 1, 54321)

	if _, err := sched.RunNow("bogus"); err == nil {
		t.Fatal("an unknown patrol name must be an error")
	}
	for name, entry := range sched.Status() {
		if m, ok := entry.(map[string]any); ok {
			if _, ran := m["last_run"]; ran {
				t.Errorf("refusing a bad name must not run %q", name)
			}
		}
	}
}

// A failing sweep is recorded as a failure, which is what the badge reads.
func TestRunNowRecordsAFailedSweep(t *testing.T) {
	sched, st, _ := newScheduler(t)
	resetStubQQ()
	enableSchedGroup(t, st, 1)
	setStubBoardErr(errString("board down"))
	t.Cleanup(func() { setStubBoardErr(nil) })

	if _, err := sched.RunNow("checkins"); err == nil {
		t.Fatal("the board failure must be reported")
	}
	entry := sched.Status()["checkins"].(map[string]any)
	if _, ok := entry["last_error"]; !ok {
		t.Errorf("the failure must be recorded for the badge: %+v", entry)
	}
}

// Testing a switched-off patrol is the point of the button: an operator wants to
// try it once before committing to it. That does not weaken the "a switched-off
// feature makes no request" rule, which is about the automatic loops — the click
// is an explicit request, and the timer stays idle.
func TestRunNowRunsASwitchedOffPatrolButTheLoopStaysIdle(t *testing.T) {
	sched, st, svc := newScheduler(t)
	resetStubQQ()
	enableSchedGroup(t, st, 1)
	enableSchedMember(t, st, 1, 54321)

	s, _ := svc.Settings()
	s.BindPatrolEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.bindInterval(); got != 0 {
		t.Fatalf("precondition: the loop must be idle, got %s", got)
	}

	res, err := sched.RunNow("bind")
	if err != nil {
		t.Fatalf("a manual test of a switched-off patrol must still run: %v", err)
	}
	if br, ok := res.(service.BindResult); !ok || br.Pending != 1 {
		t.Errorf("the sweep must have run: %T %+v", res, res)
	}
	if got := sched.bindInterval(); got != 0 {
		t.Errorf("a manual test must not arm the loop, got %s", got)
	}
}
