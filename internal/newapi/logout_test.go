package newapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Logout revokes the session this client holds. It exists for the connection
// probe: the probe must log in to prove the credentials work, and a login
// creates a session, so without revoking it every probe with changed
// credentials would permanently add an entry to the account's 50-session cap
// (sessions live 30 days) until nobody can log in.
//
// It is behind the same SessionCookieOriginGuard as refresh, so it must carry
// the same-origin Origin too.
func TestLogoutRevokesTheSessionWithTheOriginHeader(t *testing.T) {
	var gotOrigin, gotCookie string
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/auth/logout" {
			calls.Add(1)
			gotOrigin = r.Header.Get("Origin")
			if ck, err := r.Cookie("new_api_refresh"); err == nil {
				gotCookie = ck.Value
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"revoked_sid":"s1","cookie_cleared":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	const tok = "3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"
	if err := c.ImportSession(tok); err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	if err := c.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("logout calls = %d, want 1", got)
	}
	if gotOrigin != srv.URL {
		t.Errorf("Origin = %q, want %q (logout sits behind the same guard)", gotOrigin, srv.URL)
	}
	if gotCookie != tok {
		t.Errorf("logout must carry the session cookie: got %q", gotCookie)
	}
}

// A refused logout is reported rather than swallowed: the caller decides whether
// a leftover session matters (for the probe it is only a warning, but a silent
// failure would hide a real permission problem).
func TestLogoutReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_ORIGIN_FORBIDDEN","message":"nope"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.ImportSession("3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"); err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	if err := c.Logout(); err == nil {
		t.Fatal("a refused logout must be an error")
	}
}
