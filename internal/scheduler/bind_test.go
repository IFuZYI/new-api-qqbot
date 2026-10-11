package scheduler

import (
	"testing"
	"time"
)

// The bind patrol follows its own switch and interval, independent of the
// roster patrol: an operator may want to retry bindings hourly without reading
// every roster hourly.
func TestBindIntervalFollowsItsOwnSetting(t *testing.T) {
	sched, _, svc := newScheduler(t)

	s, _ := svc.Settings()
	s.BindPatrolEnabled = true
	s.BindPatrolMinutes = 45
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.bindInterval(); got != 45*time.Minute {
		t.Errorf("bind interval = %s, want 45m", got)
	}

	s.BindPatrolEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.bindInterval(); got != 0 {
		t.Errorf("a disabled bind patrol must report 0, got %s", got)
	}
}

// An interval of 0 pauses the timer even with the switch on, so a schedule can
// be paused without losing the switch state.
func TestBindIntervalZeroWhenMinutesIsZero(t *testing.T) {
	sched, _, svc := newScheduler(t)
	s, _ := svc.Settings()
	s.BindPatrolEnabled = true
	s.BindPatrolMinutes = 0
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := sched.bindInterval(); got != 0 {
		t.Errorf("a zero interval must pause the patrol, got %s", got)
	}
}

// Disabling the bind patrol must not disturb the roster or check-in patrols.
func TestBindPatrolIsIndependent(t *testing.T) {
	sched, _, svc := newScheduler(t)

	s, _ := svc.Settings()
	s.BindPatrolEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if sched.bindInterval() != 0 {
		t.Error("bind patrol should be off")
	}
	if sched.memberInterval() <= 0 {
		t.Error("the roster patrol should be unaffected")
	}
	if sched.checkinInterval() <= 0 {
		t.Error("the check-in patrol should be unaffected")
	}
}

// A settings blob that predates the bind patrol must still yield a working
// interval rather than 0, which would silently stop it.
func TestBindIntervalFallsBackWhenSettingsUnreadable(t *testing.T) {
	sched, st, _ := newScheduler(t)
	if err := st.PutSetting("settings", "{not json"); err != nil {
		t.Fatal(err)
	}
	if got := sched.bindInterval(); got <= 0 {
		t.Errorf("bind interval fell to %s; a bad setting must not disable it", got)
	}
}

// Status reports the bind patrol alongside the others, so the UI can show when
// it last ran.
func TestStatusCoversBindPatrol(t *testing.T) {
	sched, _, _ := newScheduler(t)
	if _, ok := sched.Status()["bind"]; !ok {
		t.Error("status is missing the bind patrol")
	}
}
