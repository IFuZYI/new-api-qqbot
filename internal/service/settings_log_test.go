package service

import (
	"testing"
)

// A fresh instance logs at INFO and keeps a week: enough history to look back
// over a weekend's failures without the directory growing without bound.
func TestLogSettingsDefaults(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if s.LogLevel != "info" {
		t.Errorf("log level default = %q, want info", s.LogLevel)
	}
	if s.LogRetentionDays != 7 {
		t.Errorf("log retention default = %d, want 7", s.LogRetentionDays)
	}
}

func TestLogSettingsRoundTrip(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.LogLevel = "debug"
	s.LogRetentionDays = 30
	if err := svc.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, _ := svc.Settings()
	if got.LogLevel != "debug" || got.LogRetentionDays != 30 {
		t.Errorf("stored = %q / %d", got.LogLevel, got.LogRetentionDays)
	}
}

// 0 is "keep forever", a deliberate choice that must survive a save.
func TestLogRetentionZeroIsPreserved(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.LogRetentionDays = 0
	if err := svc.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, _ := svc.Settings()
	if got.LogRetentionDays != 0 {
		t.Errorf("retention 0 became %d", got.LogRetentionDays)
	}
}

// A level the logger does not know, or a negative retention, is refused at
// save time rather than silently becoming INFO / the default.
func TestSaveSettingsRejectsBadLogSettings(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.LogLevel = "verbose"
	if err := svc.SaveSettings(s); err == nil {
		t.Error("an unknown log level was accepted")
	}
	s, _ = svc.Settings()
	s.LogRetentionDays = -1
	if err := svc.SaveSettings(s); err == nil {
		t.Error("a negative retention was accepted")
	}
	s, _ = svc.Settings()
	s.LogRetentionDays = MaxLogRetentionDays + 1
	if err := svc.SaveSettings(s); err == nil {
		t.Error("a retention above the maximum was accepted")
	}
}

// An empty level in the request (an older panel that does not send the field)
// keeps the default instead of failing validation.
func TestSaveSettingsEmptyLogLevelFallsBack(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.LogLevel = ""
	if err := svc.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, _ := svc.Settings()
	if got.LogLevel != "info" {
		t.Errorf("empty level stored as %q", got.LogLevel)
	}
}
