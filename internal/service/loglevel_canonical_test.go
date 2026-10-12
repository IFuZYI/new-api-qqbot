package service

import "testing"

// A level is stored in its canonical form. ParseLevel accepts "DEBUG" and
// " debug " (an operator typing into a form should not be punished for case or
// a stray space), but storing that verbatim means a reader comparing it against
// the logger's own name ("debug") never sees a match — the "level changed"
// record then fires on every maintenance tick.
func TestLogLevelIsStoredCanonically(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()

	for _, in := range []string{"DEBUG", " debug ", "Debug"} {
		s.LogLevel = in
		if err := svc.SaveSettings(s); err != nil {
			t.Fatalf("SaveSettings(%q): %v", in, err)
		}
		got, err := svc.Settings()
		if err != nil {
			t.Fatal(err)
		}
		if got.LogLevel != "debug" {
			t.Errorf("stored %q as %q, want the canonical \"debug\"", in, got.LogLevel)
		}
	}

	s.LogLevel = "INFO"
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Settings()
	if got.LogLevel != "info" {
		t.Errorf("stored INFO as %q", got.LogLevel)
	}
}
