package service

import (
	"encoding/json"
	"testing"

	"qqbot/internal/command"
)

// The new switches must default to a working configuration, and to the shipped
// command lists, so a fresh instance behaves the way the docs describe.
func TestFeatureSettingsDefaults(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}

	if !s.BindCommandEnabled {
		t.Error("the bind command must default to on")
	}
	if s.BindCommandPatterns != command.DefaultBindPatterns {
		t.Errorf("bind patterns default = %q, want the shipped list", s.BindCommandPatterns)
	}
	if !s.BindOnJoinEnabled {
		t.Error("auto-bind on join must default to on")
	}
	if !s.BindPatrolEnabled {
		t.Error("the bind patrol must default to on")
	}
	if s.BindPatrolMinutes <= 0 {
		t.Errorf("bind patrol interval must default to a positive value, got %d", s.BindPatrolMinutes)
	}
	if !s.CheckinByMessageEnabled {
		t.Error("message-driven check-in must default to on")
	}
	if s.CheckinPatterns != command.DefaultCheckinPatterns {
		t.Errorf("check-in patterns default = %q, want the shipped list", s.CheckinPatterns)
	}
}

// Every new switch must round-trip, or the UI would silently drop it.
func TestFeatureSettingsRoundTrip(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()

	s.BindCommandEnabled = false
	s.BindCommandPatterns = "/绑定一下\n<绑定>"
	s.BindOnJoinEnabled = false
	s.BindPatrolEnabled = false
	s.BindPatrolMinutes = 15
	s.CheckinByMessageEnabled = false
	s.CheckinPatterns = "/打卡\n<打卡>"

	if err := svc.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got.BindCommandEnabled || got.BindOnJoinEnabled || got.BindPatrolEnabled || got.CheckinByMessageEnabled {
		t.Errorf("switches did not persist as false: %+v", got)
	}
	if got.BindPatrolMinutes != 15 {
		t.Errorf("bind patrol interval = %d, want 15", got.BindPatrolMinutes)
	}
	if got.BindCommandPatterns != "/绑定一下\n<绑定>" {
		t.Errorf("bind patterns = %q", got.BindCommandPatterns)
	}
	if got.CheckinPatterns != "/打卡\n<打卡>" {
		t.Errorf("check-in patterns = %q", got.CheckinPatterns)
	}
}

// An older settings blob (written before these fields existed) must still yield
// usable defaults rather than an enabled feature with an empty pattern list -
// which would be a feature that silently does nothing.
func TestSettingsBlobWithoutNewFieldsGetsDefaults(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	// The pre-feature blob: patrols, reward and timezone only. It is stored as
	// raw JSON (a RawMessage marshals to itself), which is how the pre-feature
	// code wrote it.
	legacy := json.RawMessage(`{"member_patrol_enabled":true,"member_sync_minutes":120,` +
		`"checkin_patrol_enabled":true,"checkin_sync_minutes":30,` +
		`"reward_min":100,"reward_max":200,"timezone":"Asia/Shanghai"}`)
	if err := st.PutSetting("settings", legacy); err != nil {
		t.Fatal(err)
	}

	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if s.BindCommandPatterns != command.DefaultBindPatterns {
		t.Errorf("an absent pattern list must fall back to the defaults, got %q", s.BindCommandPatterns)
	}
	if s.CheckinPatterns != command.DefaultCheckinPatterns {
		t.Errorf("check-in patterns = %q", s.CheckinPatterns)
	}
	if s.BindPatrolMinutes <= 0 {
		t.Errorf("bind patrol interval fell to %d", s.BindPatrolMinutes)
	}
	// A field the blob DOES carry as empty stays empty: the operator cleared it.
	if s.MemberPatrolEnabled != true {
		t.Error("a stated field must be honoured")
	}
}

// An invalid pattern must be refused at save time, so a broken expression is
// caught by the operator who typed it rather than silently disabling a feature.
func TestSaveSettingsRejectsInvalidPattern(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.BindCommandPatterns = "/([unclosed/"
	if err := svc.SaveSettings(s); err == nil {
		t.Fatal("an invalid regular expression must be rejected")
	}

	s, _ = svc.Settings()
	s.CheckinPatterns = "/([unclosed/"
	if err := svc.SaveSettings(s); err == nil {
		t.Fatal("an invalid check-in expression must be rejected")
	}
}

// A feature that is switched on with no patterns at all matches nothing, which
// is the silent no-op the defaults exist to prevent. Saving it must still be
// allowed (an operator may be mid-edit) but the stored value must be what they
// typed, not silently replaced.
func TestEmptyPatternListIsStoredAsGiven(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.BindCommandPatterns = ""
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Settings()
	if got.BindCommandPatterns != "" {
		t.Errorf("an explicitly cleared pattern list must stay cleared, got %q", got.BindCommandPatterns)
	}
}

// A negative or zero bind patrol interval means "not on a timer"; it must not
// be rewritten to a positive value, or the operator could not switch the timer
// off.
func TestZeroBindPatrolIntervalIsPreserved(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.BindPatrolEnabled = true
	s.BindPatrolMinutes = 0
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Settings()
	if got.BindPatrolMinutes != 0 {
		t.Errorf("an explicit 0 interval must be preserved, got %d", got.BindPatrolMinutes)
	}
}
