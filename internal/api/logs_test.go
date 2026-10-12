package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qqbot/internal/logging"
)

func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A 5xx is a fault on our side, so it is an ERROR record carrying the method,
// path, status and the error text the client saw — that is what an operator
// searches for when a button "did nothing".
func TestServerErrorIsLoggedAsError(t *testing.T) {
	env := newTestEnv(t)
	buf := captureDefaultLog(t)

	// No patrol runner is installed in tests, so this answers 503.
	code := postJSONWith(t, env.client, env.srv.URL+"/api/patrols/bind/run", map[string]any{}, nil)
	if code != 503 {
		t.Fatalf("status %d, want 503", code)
	}
	out := buf.String()
	for _, want := range []string{`"level":"ERROR"`, `"path":"/api/patrols/bind/run"`, `"status":503`, "巡检尚未启动"} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %s: %s", want, out)
		}
	}
}

// A client mistake (4xx) is not a server error: it is traced at DEBUG only, so
// the ERROR view is not full of mistyped forms.
func TestClientErrorIsNotLoggedAsError(t *testing.T) {
	env := newTestEnv(t)
	buf := captureDefaultLog(t)

	resp, err := env.client.Post(env.srv.URL+"/api/settings", "application/json", strings.NewReader("{bad"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	out := buf.String()
	if strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("a 400 was logged as ERROR: %s", out)
	}
	if !strings.Contains(out, `"level":"DEBUG"`) || !strings.Contains(out, `"status":400`) {
		t.Errorf("the request was not traced at DEBUG: %s", out)
	}
}

// A panic is an ERROR record with its stack, not an INFO line.
func TestPanicIsLoggedAsError(t *testing.T) {
	buf := captureDefaultLog(t)
	h := recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
	out := buf.String()
	if rec.Code != 500 || !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, "kaboom") || !strings.Contains(out, "goroutine") {
		t.Errorf("panic log = %s (code %d)", out, rec.Code)
	}
}

func newLogEnv(t *testing.T) (*testEnv, *logging.Manager) {
	t.Helper()
	env := newTestEnv(t)
	m, err := logging.Open(logging.Options{Dir: filepath.Join(t.TempDir(), "logs"), Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	env.router.SetLogs(m)
	return env, m
}

// The log page reads today's records through the API, filtered by level.
func TestLogsEndpointReturnsFilteredRecords(t *testing.T) {
	env, m := newLogEnv(t)
	m.Logger().Info("hello info")
	m.Logger().Error("hello error", "qq", 42)

	var got logging.Result
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs?level=error", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Entries) != 1 || got.Entries[0].Msg != "hello error" {
		t.Errorf("entries = %+v", got.Entries)
	}
	if got.Counts["INFO"] != 1 || got.Counts["ERROR"] != 1 {
		t.Errorf("counts = %+v", got.Counts)
	}
	if got.Day != time.Now().Format(time.DateOnly) {
		t.Errorf("day = %q", got.Day)
	}
}

// A malformed day is a 400, not a 500 and never a file read.
func TestLogsEndpointRejectsBadDay(t *testing.T) {
	env, _ := newLogEnv(t)
	var out map[string]any
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs?day=..%2F..%2Fetc", &out); code != 400 {
		t.Errorf("status %d, want 400", code)
	}
}

func TestLogDaysEndpoint(t *testing.T) {
	env, m := newLogEnv(t)
	m.Logger().Info("x")
	var got struct {
		Days []logging.DayFile `json:"days"`
		Dir  string            `json:"dir"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs/days", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Days) != 1 || got.Dir != m.Dir() {
		t.Errorf("days = %+v", got)
	}
}

// The log endpoints sit behind the session like everything else: records carry
// QQ numbers and error detail.
func TestLogsEndpointRequiresAuth(t *testing.T) {
	env, _ := newLogEnv(t)
	resp, err := http.Get(env.srv.URL + "/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("unauthenticated /api/logs = %d", resp.StatusCode)
	}
}

// Saving the settings applies the new level at once: the operator switching to
// DEBUG wants the next request traced, not the one after the hourly tick.
func TestSaveSettingsAppliesLogLevelImmediately(t *testing.T) {
	env, m := newLogEnv(t)
	var s map[string]any
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/settings", &s); code != 200 {
		t.Fatalf("status %d", code)
	}
	s["log_level"] = "debug"
	if code := postJSONWith(t, env.client, env.srv.URL+"/api/settings", s, nil); code != 200 {
		t.Fatalf("save status %d", code)
	}
	if m.Level() != logging.LevelDebug {
		t.Errorf("level after save = %q", m.Level())
	}
}

// Without a log manager installed the endpoint says so instead of panicking.
func TestLogsEndpointWithoutManager(t *testing.T) {
	env := newTestEnv(t)
	var out map[string]any
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs", &out); code != 503 {
		t.Errorf("status %d, want 503", code)
	}
}
