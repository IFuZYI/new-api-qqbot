package newapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A cold start has no session cookie, so a refresh can only fail — and the
// refresh endpoint sits behind the gateway's CriticalRateLimit, so that
// guaranteed failure spends one slot of the same login budget this service is
// trying not to exhaust. With no session, Reauthenticate must go straight to
// the login it is going to need anyway.
func TestReauthenticateLogsInDirectlyWithoutASession(t *testing.T) {
	var refreshes, logins atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/auth/refresh":
			refreshes.Add(1)
			_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_UNAUTHORIZED","message":"no session"}`))
		case "/api/user/login":
			logins.Add(1)
			_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"tok","access_expires_at":9999999999}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Reauthenticate("admin", "pw"); err != nil {
		t.Fatalf("Reauthenticate: %v", err)
	}
	if got := refreshes.Load(); got != 0 {
		t.Errorf("refreshes = %d, want 0: with no session a refresh can only waste a rate-limit slot", got)
	}
	if got := logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
}

// With a session present the cheap path is still preferred: refresh first, and
// only log in when the session is actually dead.
func TestReauthenticateRefreshesWhenASessionExists(t *testing.T) {
	var refreshes, logins atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/auth/refresh":
			refreshes.Add(1)
			_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"tok","access_expires_at":9999999999}}`))
		case "/api/user/login":
			logins.Add(1)
			_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"tok","access_expires_at":9999999999}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.ImportSession("3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"); err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	if err := c.Reauthenticate("admin", "pw"); err != nil {
		t.Fatalf("Reauthenticate: %v", err)
	}
	if got := refreshes.Load(); got != 1 {
		t.Errorf("refreshes = %d, want 1", got)
	}
	if got := logins.Load(); got != 0 {
		t.Errorf("logins = %d, want 0: a live session must be refreshed, not replaced", got)
	}
}
