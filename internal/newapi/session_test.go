package newapi

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// The refresh token is the session. Persisting it means a restart reuses the
// session it already has instead of logging in again — and every avoided login
// is a session the account does not spend against its cap. A service that
// re-logged-in on each restart is how the cap gets exhausted by nothing but
// ordinary restarts.
func TestSessionRoundTrip(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatalf("login: %v", err)
	}
	saved := c.ExportSession()
	if saved == "" {
		t.Fatal("a logged-in client must export its session")
	}

	// A fresh client (as after a restart) restored from the saved value must be
	// able to refresh, without logging in.
	restored := New(srv.URL)
	if err := restored.ImportSession(saved); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := restored.Refresh(); err != nil {
		t.Fatalf("a restored session must refresh: %v", err)
	}
	if got := s.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1: the restored session must not log in again", got)
	}
	if restored.Token() == "" {
		t.Error("the restored client must hold a token")
	}
}

// A client with no session exports nothing, so the caller stores nothing rather
// than an empty credential.
func TestSessionExportIsEmptyWithoutASession(t *testing.T) {
	c := New("http://example.invalid")
	if got := c.ExportSession(); got != "" {
		t.Errorf("no session must export as empty, got %q", got)
	}
}

// A saved session that is no longer valid must not break the client: importing
// it succeeds (the value is well-formed) and the next refresh reports the real
// failure, which sends the caller on to a login.
func TestImportingAStaleSessionStillWorks(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	s.refuseRefresh.Store(true)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.ImportSession("3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret1"); err != nil {
		t.Fatalf("a well-formed value must import: %v", err)
	}
	if err := c.Refresh(); err == nil {
		t.Fatal("a revoked session must fail to refresh")
	} else if !IsAuthFailure(err) {
		t.Errorf("the caller must learn a login is needed: %v", err)
	}
}

// A malformed saved value must be refused rather than installed, so a corrupted
// setting cannot leave the client sending nonsense.
func TestImportingGarbageIsRefused(t *testing.T) {
	c := New("http://example.invalid")
	for _, bad := range []string{"", "   ", "no-dot", "a.b.c"} {
		if err := c.ImportSession(bad); err == nil {
			t.Errorf("ImportSession(%q) must be refused", bad)
		}
	}
}

// The imported cookie must be scoped to the gateway's own host, so it is never
// sent anywhere else.
func TestImportedCookieIsScopedToTheGateway(t *testing.T) {
	c := New("https://api.example.test")
	if err := c.ImportSession("3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret1"); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://api.example.test/api/user/auth")
	cookies := c.http.Jar.Cookies(u)
	if len(cookies) != 1 || cookies[0].Name != refreshCookieName {
		t.Fatalf("the cookie must be stored for the gateway host: %+v", cookies)
	}
	other, _ := url.Parse("https://elsewhere.test/api/user/auth")
	if got := c.http.Jar.Cookies(other); len(got) != 0 {
		t.Errorf("the cookie must not be sent elsewhere: %+v", got)
	}
}

// ExportSession must round-trip a rotated cookie: after a refresh the gateway
// issues a new one, and saving the old value would send a stale session.
func TestExportFollowsARotation(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatal(err)
	}
	first := c.ExportSession()
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	second := c.ExportSession()
	if first == second {
		t.Errorf("the exported session must follow the rotation (%q)", first)
	}
	if second == "" {
		t.Error("a rotated session must still export")
	}
}
