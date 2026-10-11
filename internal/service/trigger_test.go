package service

import "testing"

// The master switch enables the path; the pattern list decides which messages
// count. An empty list means "any message", and a non-empty one means "only
// these" - so a configured command list must not also let every other message
// through, which is what would happen if the switch alone meant "any message".
func TestCheckinTriggerFollowsThePatternList(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	s, _ := svc.Settings()
	s.CheckinByMessageEnabled = true
	s.CheckinPatterns = "/签到"
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if svc.CheckinByAnyMessage() {
		t.Error("a configured command list must not make every message a check-in")
	}
	if !svc.IsCheckinCommand("/签到") {
		t.Error("the configured command should be recognised")
	}
	if svc.IsCheckinCommand("早上好") {
		t.Error("an ordinary message must not be a check-in command")
	}

	// An empty list is the explicit "any message" mode.
	s.CheckinPatterns = ""
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if !svc.CheckinByAnyMessage() {
		t.Error("an empty pattern list should mean every message counts")
	}
	if svc.IsCheckinCommand("早上好") {
		t.Error("with an empty list there is no command list to match against")
	}
}

// The master switch gates both modes: switched off, nothing is a check-in.
func TestCheckinTriggerRespectsTheMasterSwitch(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	s, _ := svc.Settings()
	s.CheckinByMessageEnabled = false
	s.CheckinPatterns = "/签到"
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if svc.CheckinByAnyMessage() {
		t.Error("a disabled path must not accept any message")
	}
	if svc.IsCheckinCommand("/签到") {
		t.Error("a disabled path must not recognise commands")
	}

	s.CheckinPatterns = ""
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if svc.CheckinByAnyMessage() {
		t.Error("a disabled path must not accept any message even with an empty list")
	}
}

// The bind command list is gated by its own switch in the same way.
func TestBindCommandRespectsItsSwitch(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	s, _ := svc.Settings()
	s.BindCommandEnabled = true
	s.BindCommandPatterns = "/绑定"
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if !svc.IsBindCommand("/绑定") {
		t.Error("the configured command should be recognised")
	}

	s.BindCommandEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if svc.IsBindCommand("/绑定") {
		t.Error("a disabled bind command must not be recognised")
	}
}
