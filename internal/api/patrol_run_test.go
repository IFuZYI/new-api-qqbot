package api

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/scheduler"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// fakeRunner stands in for the scheduler: the API's job is to route the name,
// report the outcome, and surface a failure — not to run sweeps itself.
type fakeRunner struct {
	// got is the last patrol name asked for.
	got string
	// calls counts invocations, so a test can prove a bad name ran nothing.
	calls int
	// known is the set of patrol names this runner accepts; nil accepts all.
	known []string
	res   any
	err   error
}

func (f *fakeRunner) RunNow(name string) (any, error) {
	// Model the real scheduler: a name it does not have is refused before
	// anything runs (see scheduler.RunNow).
	if f.known != nil && !slices.Contains(f.known, name) {
		return nil, fmt.Errorf("%w：%s", scheduler.ErrUnknownPatrol, name)
	}
	f.got = name
	f.calls++
	return f.res, f.err
}

// The patrol card's 测试一次 button posts the patrol's name and gets its result
// back, so the row can report what the run did.
func TestPatrolRunEndpointReportsTheResult(t *testing.T) {
	srv, cl, run := newRunnerEnv(t)
	run.res = map[string]any{"bound": 2, "pending": 1}

	var out struct {
		Patrol string         `json:"patrol"`
		Result map[string]any `json:"result"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/patrols/bind/run", map[string]any{}, &out); code != 200 {
		t.Fatalf("status %d", code)
	}
	if run.got != "bind" {
		t.Errorf("the sweep name must reach the runner, got %q", run.got)
	}
	if out.Patrol != "bind" {
		t.Errorf("the response must name the patrol, got %q", out.Patrol)
	}
	if out.Result["bound"] != float64(2) {
		t.Errorf("the sweep's own result must be reported: %+v", out.Result)
	}
}

// A sweep that fails must be reported as a failure, with the sweep's message:
// the operator clicked 测试一次 precisely to find out whether it works.
func TestPatrolRunEndpointReportsAFailure(t *testing.T) {
	srv, cl, run := newRunnerEnv(t)
	run.err = errText("board unreachable")

	var out struct {
		Error string `json:"error"`
	}
	code := postJSONWith(t, cl, srv.URL+"/api/patrols/checkins/run", map[string]any{}, &out)
	if code != 502 {
		t.Fatalf("status = %d, want 502", code)
	}
	if !strings.Contains(out.Error, "board unreachable") {
		t.Errorf("the failure must be surfaced, got %q", out.Error)
	}
}

// An unknown patrol name is refused before anything runs.
func TestPatrolRunEndpointRejectsAnUnknownPatrol(t *testing.T) {
	srv, cl, run := newRunnerEnv(t)
	run.known = []string{"members", "bind", "checkins", "rules"}
	code := postJSONWith(t, cl, srv.URL+"/api/patrols/bogus/run", map[string]any{}, nil)
	if code != 404 {
		t.Fatalf("status = %d, want 404", code)
	}
	if run.calls != 0 {
		t.Errorf("an unknown name must not reach the scheduler (%d calls)", run.calls)
	}
}

// The endpoint is behind the session like every other action.
func TestPatrolRunEndpointNeedsASession(t *testing.T) {
	srv, _, run := newRunnerEnv(t)
	resp, err := http.Post(srv.URL+"/api/patrols/bind/run", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if run.calls != 0 {
		t.Errorf("an unauthenticated request must not run a sweep")
	}
}

// The status endpoint must keep working when no runner is installed (a test
// server without a scheduler, or the brief window before Run starts).
func TestPatrolRunWithoutARunner(t *testing.T) {
	env := newTestEnv(t)
	code := postJSONWith(t, env.client, env.srv.URL+"/api/patrols/bind/run", map[string]any{}, nil)
	if code != 503 {
		t.Errorf("status = %d, want 503 when no scheduler is installed", code)
	}
}

// errText is a tiny error type so a test can name an error inline.
type errText string

func (e errText) Error() string { return string(e) }

// newRunnerEnv builds an authenticated server with a fake patrol runner.
func newRunnerEnv(t *testing.T) (*httptest.Server, *http.Client, *fakeRunner) {
	t.Helper()
	run := &fakeRunner{}
	st, err := store.Open(filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	r := NewRouter(st, svc, cfgMgr, t.TempDir())
	r.SetPatrolRunner(run)
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	cl := &http.Client{Jar: jar}
	resp, err := cl.Post(srv.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"username":"admin","password":"test-password"}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	resp.Body.Close()
	return srv, cl, run
}
