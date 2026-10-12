package scheduler

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// captureLog points the default logger at a buffer for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A failed sweep is an ERROR record naming the patrol, whether the timer or the
// 测试一次 button ran it: the log page filters on ERROR to find what broke, and a
// manual run that failed silently would be missing from exactly that view.
func TestFailedSweepIsLoggedAsError(t *testing.T) {
	sched, st, _ := newScheduler(t)
	resetStubQQ()
	enableSchedGroup(t, st, 1)
	setStubBoardErr(errString("board down"))
	t.Cleanup(func() { setStubBoardErr(nil) })
	buf := captureLog(t)

	_, _ = sched.RunNow("checkins")

	out := buf.String()
	if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, `"patrol":"checkins"`) || !strings.Contains(out, "board down") {
		t.Errorf("no ERROR record for the failed sweep: %s", out)
	}
}

// A successful sweep is recorded only at DEBUG, so INFO is not flooded with one
// line per patrol tick.
func TestSuccessfulSweepIsDebugOnly(t *testing.T) {
	sched, st, _ := newScheduler(t)
	resetStubQQ()
	enableSchedGroup(t, st, 1)
	buf := captureLog(t)

	if _, err := sched.RunNow("bind"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `"level":"DEBUG"`) || !strings.Contains(out, `"patrol":"bind"`) {
		t.Errorf("no DEBUG record for the successful sweep: %s", out)
	}
	if strings.Contains(out, `"level":"INFO"`) || strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("a successful sweep logged above DEBUG: %s", out)
	}
}
