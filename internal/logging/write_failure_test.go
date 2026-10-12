package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A log file that cannot be written must be REPORTED, not swallowed.
//
// The slog handler returns the write error, the stdlib logger drops it
// (`_ = l.Handler().Handle(...)`) and the stderr handler still succeeds, so a
// probe measured 50 ERROR records with an unopenable day file: the process
// survived, the request path was unaffected, and NOTHING anywhere said the log
// file was broken. The panel reads those files, so the day simply showed zero
// records — indistinguishable from "nothing happened".
//
// The day file's path is occupied by a DIRECTORY, which makes os.OpenFile fail
// with EISDIR whatever the process's uid is (a read-only directory does not
// deny root, so a mode-based test would silently pass as root).
func unwritableManager(t *testing.T, stderr *bytes.Buffer) *Manager {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The day the manager will write to.
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	if err := os.Mkdir(filepath.Join(dir, fileName("2026-10-12")), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := Open(Options{Dir: dir, Stderr: stderr, Now: clock.now})
	if err != nil {
		t.Fatalf("Open must survive an unwritable day file: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestAnUnwritableLogFileIsReported(t *testing.T) {
	var stderr bytes.Buffer
	m := unwritableManager(t, &stderr)

	m.Logger().Error("something failed")

	// The record still reached stderr (the operator's fallback view).
	if !strings.Contains(stderr.String(), "something failed") {
		t.Fatalf("the record did not reach stderr: %q", stderr.String())
	}
	// And the file failure is itself visible on stderr.
	out := stderr.String()
	if !strings.Contains(out, "日志") || !strings.Contains(out, "写入") {
		t.Errorf("the file-write failure was not reported: %q", out)
	}
}

// The failure must be reported once per distinct problem, not once per record:
// a DEBUG day writes tens of thousands of records, and one line per record would
// be its own denial of service.
func TestTheFileFailureIsReportedOnce(t *testing.T) {
	var stderr bytes.Buffer
	m := unwritableManager(t, &stderr)

	for i := 0; i < 50; i++ {
		m.Logger().Error("boom", "i", i)
	}
	if n := strings.Count(stderr.String(), "写入"); n != 1 {
		t.Errorf("the write failure was reported %d times for 50 records, want 1", n)
	}
}
