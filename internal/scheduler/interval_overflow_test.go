package scheduler

import (
	"testing"
	"time"

	"qqbot/internal/service"
)

// A stored interval large enough to overflow time.Duration must never wrap into
// a negative period: the loop reads a negative duration as "this patrol is
// switched off", so an overflow would silently disable a feature the operator
// turned on.
func TestIntervalMinutesNeverOverflowsNegative(t *testing.T) {
	capMinutes := service.MaxIntervalMinutes
	for _, tc := range []struct {
		name    string
		minutes int
		want    time.Duration
	}{
		{"zero stays off", 0, 0},
		{"negative stays off", -5, 0},
		{"ordinary", 30, 30 * time.Minute},
		{"at the cap", capMinutes, time.Duration(capMinutes) * time.Minute},
		{"beyond the cap clamps", capMinutes + 1, time.Duration(capMinutes) * time.Minute},
		{"max int clamps", int(^uint(0) >> 1), time.Duration(capMinutes) * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := intervalMinutes(tc.minutes)
			if got != tc.want {
				t.Fatalf("intervalMinutes(%d) = %v, want %v", tc.minutes, got, tc.want)
			}
			if tc.minutes > 0 && got <= 0 {
				t.Fatalf("an enabled interval became non-positive: %v", got)
			}
		})
	}
}

// A settings blob an older version (or a manual edit) could have left behind
// must still produce a positive period for every patrol, not a disabled one.
func TestPatrolIntervalsSurviveExtremeSettings(t *testing.T) {
	sched, st, _ := newScheduler(t)
	huge := service.MaxIntervalMinutes + 1000
	// Write the blob directly: SaveSettings refuses out-of-range values, but
	// the read path must not trust that the stored one is sane.
	if err := st.PutSetting("settings", map[string]any{
		"member_patrol_enabled": true, "member_sync_minutes": huge,
		"checkin_patrol_enabled": true, "checkin_sync_minutes": huge,
		"bind_patrol_enabled": true, "bind_patrol_minutes": huge,
		"checkin_by_message_enabled": true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := sched.memberInterval(); got <= 0 {
		t.Fatalf("member patrol period = %v (reads as disabled)", got)
	}
	if got := sched.checkinInterval(); got <= 0 {
		t.Fatalf("check-in patrol period = %v (reads as disabled)", got)
	}
	if got := sched.bindInterval(); got <= 0 {
		t.Fatalf("bind patrol period = %v (reads as disabled)", got)
	}
}
