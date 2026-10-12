package api

import (
	"net/http"
	"testing"
)

// Both log routes must be behind the session gate.
//
// The reviewer's mutation run removed the requireAuth wrapper from
// /api/logs/days and the whole suite stayed green: only /api/logs had an auth
// test. The guard was correct, but nothing pinned it — and a route that leaks
// the log directory listing and file sizes is exactly what the gate is for.
func TestBothLogRoutesRequireASession(t *testing.T) {
	env, _ := newLogEnv(t)
	for _, path := range []string{"/api/logs", "/api/logs/days", "/api/logs?day=2026-10-12&limit=10"} {
		resp, err := http.Get(env.srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401 (the log routes must be behind requireAuth)", path, resp.StatusCode)
		}
	}
}
