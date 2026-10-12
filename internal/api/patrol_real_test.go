package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/scheduler"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// The router must be driven by the REAL scheduler.
//
// Every existing test wires a fakeRunner / stubPatrols, so the two contracts the
// panel actually depends on were unpinned: the real ErrUnknownPatrol reaching
// the router (a fake that re-implements the sentinel proves nothing), and the
// exact key set Status emits — the panel's health badge looks up \"checkins\", so
// renaming that key would leave it showing 尚未运行 forever while every test
// passed.
func newRealSchedulerEnv(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	r := NewRouter(st, svc, cfgMgr, t.TempDir())
	// The real scheduler, exactly as main wires it.
	sched := scheduler.New(svc)
	r.SetPatrolRunner(sched)
	r.SetPatrolStatus(sched)
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
	return srv, cl
}

// An unknown patrol name must answer 404, not 502: the real scheduler reports it
// with a sentinel the router maps to a client error, and a 502 would tell the
// operator the upstream failed when their request was the problem.
func TestUnknownPatrolIsNotFoundAgainstTheRealScheduler(t *testing.T) {
	srv, cl := newRealSchedulerEnv(t)

	resp, err := cl.Post(srv.URL+"/api/patrols/bogus/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (the name is the caller's error)", resp.StatusCode)
	}
}

// A known patrol runs and answers 200 with the patrol's own result.
func TestKnownPatrolRunsAgainstTheRealScheduler(t *testing.T) {
	srv, cl := newRealSchedulerEnv(t)

	resp, err := cl.Post(srv.URL+"/api/patrols/bind/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := make([]byte, 400)
		n, _ := resp.Body.Read(body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body[:n])
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["patrol"]; !ok {
		t.Errorf("the response names no patrol: %+v", out)
	}
}

// GET /api/status must carry exactly the four patrol keys the scheduler emits:
// the panel's health badge reads them by name.
func TestStatusCarriesTheRealPatrolKeys(t *testing.T) {
	srv, cl := newRealSchedulerEnv(t)

	resp, err := cl.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Patrols map[string]map[string]any `json:"patrols"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, name := range []string{"members", "bind", "checkins", "rules"} {
		if _, ok := out.Patrols[name]; !ok {
			t.Errorf("patrol %q is missing from /api/status: %v (the panel's badge reads it by name)", name, out.Patrols)
		}
	}
	if len(out.Patrols) != 4 {
		t.Errorf("patrols = %v, want exactly the four the scheduler emits", out.Patrols)
	}
}
