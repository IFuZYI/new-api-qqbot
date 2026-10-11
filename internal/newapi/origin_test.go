package newapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// new-api gates its refresh endpoint behind SessionCookieOriginGuard once
// SESSION_COOKIE_SECURE is on (which its own startup warning tells operators to
// set in production). That guard refuses any request without a same-origin
// Origin/Referer, so a client that sends neither gets a 403 on every refresh —
// and since login has no such guard, the service would silently fall back to a
// login on every call, exhausting the account's session cap.
//
// The refresh request must therefore carry an Origin matching the gateway it is
// talking to, and the client must recognise the refusal as "this session cannot
// be refreshed here" rather than as a dead session it should discard.
func TestRefreshSendsASameOriginHeader(t *testing.T) {
	var gotOrigin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/auth/refresh" {
			gotOrigin = r.Header.Get("Origin")
			_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"tok","access_expires_at":9999999999}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.ImportSession("3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"); err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	if err := c.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if gotOrigin != srv.URL {
		t.Errorf("Origin = %q, want %q (the gateway's own origin)", gotOrigin, srv.URL)
	}
}

// A refusal from the origin guard is not a dead session: the credential may be
// perfectly good, so throwing it away would force a login.
//
// The 403 form is already handled by the status fallback (only a bare 401 counts
// as a dead session). The 401 form is the one that needs the explicit code: the
// gateway renders some codes alongside 401, and without the entry the bare-401
// rule would classify a refused refresh as a dead session and force a login —
// the exact failure this change exists to prevent.
func TestOriginRefusalIsNotADeadSession(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"403, the guard's own status", 403,
			`{"success":false,"code":"AUTH_ORIGIN_FORBIDDEN","message":"request origin is not allowed"}`},
		{"401, which the bare-status fallback would call dead", 401,
			`{"success":false,"code":"AUTH_ORIGIN_FORBIDDEN","message":"request origin is not allowed"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := New(srv.URL)
			if err := c.ImportSession("3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"); err != nil {
				t.Fatalf("ImportSession: %v", err)
			}
			err := c.Refresh()
			if err == nil {
				t.Fatal("a refused refresh must be an error")
			}
			if IsAuthFailure(err) {
				t.Errorf("an origin refusal must not be treated as a dead session: %v", err)
			}
		})
	}
}
