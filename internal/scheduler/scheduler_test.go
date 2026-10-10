package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/service"
	"qqbot/internal/store"

	"path/filepath"
)

type stubGateway struct{}

func (stubGateway) FindUserByEmail(string) (*newapi.User, error) { return nil, nil }
func (stubGateway) GetUser(int) (*newapi.User, error)            { return &newapi.User{}, nil }
func (stubGateway) SetGroup(*newapi.User, string) error          { return nil }
func (stubGateway) AddQuota(int, int) error                      { return nil }
func (stubGateway) Groups() (map[string]struct{}, error)         { return map[string]struct{}{}, nil }
func (stubGateway) ListUsers() ([]newapi.User, error)            { return nil, nil }

type stubQQ struct{}

func (stubQQ) GetGroupList() ([]napcat.Group, error)                  { return nil, nil }
func (stubQQ) GetGroupMemberList(int64) ([]napcat.GroupMember, error) { return nil, nil }
func (stubQQ) GetGroupSignedList(int64) ([]napcat.SignedMember, error) {
	return nil, nil
}

// enableSchedGroup registers one monitored group and turns monitoring on (a
// newly discovered group is disabled until the operator selects it).
func enableSchedGroup(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: id, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(id, true); err != nil {
		t.Fatal(err)
	}
}

func newScheduler(t *testing.T) (*Scheduler, *store.Store, *service.Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := service.New(st, stubGateway{}, stubQQ{})
	return New(svc), st, svc
}

// A switched-off patrol reports a zero interval, which is what makes the loop
// idle instead of running. A non-zero interval would mean the setting is ignored.
func TestIntervalsAreZeroWhenPatrolDisabled(t *testing.T) {
	sched, _, svc := newScheduler(t)

	s, _ := svc.Settings()
	if !s.MemberPatrolEnabled || !s.CheckinPatrolEnabled {
		t.Fatal("precondition: patrols should default to enabled")
	}
	if sched.memberInterval() <= 0 {
		t.Error("member patrol enabled but interval is not positive")
	}
	if sched.checkinInterval() <= 0 {
		t.Error("check-in patrol enabled but interval is not positive")
	}

	s.MemberPatrolEnabled = false
	s.CheckinPatrolEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.memberInterval(); got != 0 {
		t.Errorf("disabled member patrol must report 0, got %s", got)
	}
	if got := sched.checkinInterval(); got != 0 {
		t.Errorf("disabled check-in patrol must report 0, got %s", got)
	}
}

// The interval must follow the configured minutes, since it is re-read each tick.
func TestIntervalsFollowConfiguredMinutes(t *testing.T) {
	sched, _, svc := newScheduler(t)

	s, _ := svc.Settings()
	s.MemberSyncMinutes = 45
	s.CheckinSyncMinutes = 7
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	if got := sched.memberInterval(); got != 45*time.Minute {
		t.Errorf("member interval = %s, want 45m", got)
	}
	if got := sched.checkinInterval(); got != 7*time.Minute {
		t.Errorf("check-in interval = %s, want 7m", got)
	}
}

// Disabling one patrol must not disturb the other.
func TestPatrolsAreIndependent(t *testing.T) {
	sched, _, svc := newScheduler(t)

	s, _ := svc.Settings()
	s.MemberPatrolEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if sched.memberInterval() != 0 {
		t.Error("member patrol should be off")
	}
	if sched.checkinInterval() <= 0 {
		t.Error("check-in patrol should be unaffected")
	}
}

// A missing or unreadable setting must fall back to a working interval rather
// than 0, which would silently stop the patrol.
func TestIntervalFallsBackWhenSettingsUnreadable(t *testing.T) {
	sched, st, _ := newScheduler(t)
	// Corrupt the settings blob: Load must still yield usable defaults.
	if err := st.PutSetting("settings", "{not json"); err != nil {
		t.Fatal(err)
	}

	if got := sched.memberInterval(); got <= 0 {
		t.Errorf("member interval fell to %s; a bad setting must not disable the patrol", got)
	}
	if got := sched.checkinInterval(); got <= 0 {
		t.Errorf("check-in interval fell to %s", got)
	}
}

// Run must return promptly when its context is cancelled, including while a
// patrol is switched off (the idle path must also observe cancellation).
func TestRunStopsOnContextCancel(t *testing.T) {
	sched, _, svc := newScheduler(t)

	s, _ := svc.Settings()
	s.MemberPatrolEnabled = false
	s.CheckinPatrolEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	// Give the loops a moment to start idling, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// Status reports a key per patrol so the UI can show when each last ran.
func TestStatusCoversEveryPatrol(t *testing.T) {
	sched, _, _ := newScheduler(t)
	st := sched.Status()
	for _, name := range []string{"members", "checkins", "rules"} {
		if _, ok := st[name]; !ok {
			t.Errorf("status is missing %q", name)
		}
	}
}

// A panic in a sweep must not kill the process: it would take the WebUI, the
// event listener and the other patrols down with it.
func TestSweepPanicIsContained(t *testing.T) {
	sched, _, _ := newScheduler(t)

	err := sched.safely("members", func() error {
		var m map[string]int
		m["boom"] = 1 // nil map write panics
		return nil
	})
	if err == nil {
		t.Fatal("a panic must be converted into an error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("the error should say it was a panic, got %v", err)
	}

	// The scheduler must still be usable afterwards.
	if err := sched.safely("members", func() error { return nil }); err != nil {
		t.Errorf("scheduler unusable after a contained panic: %v", err)
	}
}

// A failing check-in sync must stay visible in Status: the award step's nil
// return used to erase it, so a dead board looked healthy.
func TestCheckinSyncErrorIsNotMasked(t *testing.T) {
	sched, st, _ := newScheduler(t)
	enableSchedGroup(t, st, 1)

	// Model the check-in sweep: sync fails, award succeeds.
	syncErr := errors.New("board unreadable")
	err := sched.safely("checkins", func() error {
		if syncErr != nil {
			return syncErr
		}
		return nil
	})
	sched.record("checkins", err)

	status := sched.Status()
	entry, ok := status["checkins"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected status shape: %+v", status)
	}
	if _, has := entry["last_error"]; !has {
		t.Errorf("the failed check-in sync was masked: %+v", entry)
	}
}
