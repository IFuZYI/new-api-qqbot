package logging

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedClock is a settable clock, so day boundaries are tested without sleeping.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { return c.t }

func openTest(t *testing.T, clock *fixedClock) (*Manager, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	m, err := Open(Options{Dir: dir, Stderr: io.Discard, Now: clock.now})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// At the default INFO level an INFO record reaches the day's file and a DEBUG
// record does not: DEBUG is the noisy level an operator turns on deliberately.
func TestInfoLevelWritesInfoAndDropsDebug(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)

	m.Logger().Info("visible message", "qq", 12345)
	m.Logger().Debug("hidden message")

	body := readFile(t, filepath.Join(dir, "qqbot-2026-10-12.log"))
	if !strings.Contains(body, "visible message") || !strings.Contains(body, `"qq":12345`) {
		t.Errorf("INFO record missing from the file: %q", body)
	}
	if strings.Contains(body, "hidden message") {
		t.Errorf("DEBUG record written at INFO level: %q", body)
	}
}

// Switching to DEBUG takes effect on the running logger, with no reopen: the
// operator changes the level in the panel to chase a live problem.
func TestSetLevelDebugEnablesDebugRecords(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)

	if err := m.SetLevel("debug"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	m.Logger().Debug("now visible")
	if got := m.Level(); got != "debug" {
		t.Errorf("Level() = %q, want debug", got)
	}
	body := readFile(t, filepath.Join(dir, "qqbot-2026-10-12.log"))
	if !strings.Contains(body, "now visible") {
		t.Errorf("DEBUG record missing after SetLevel(debug): %q", body)
	}
}

// An unknown level is refused rather than silently read as INFO, so a typo in
// the settings cannot quietly hide the records the operator asked for.
func TestSetLevelRejectsUnknownLevel(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)
	if err := m.SetLevel("verbose"); err == nil {
		t.Fatal("SetLevel(verbose) succeeded, want an error")
	}
	if got := m.Level(); got != "info" {
		t.Errorf("a refused level changed the logger to %q", got)
	}
}
