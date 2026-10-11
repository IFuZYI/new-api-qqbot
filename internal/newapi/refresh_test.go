package newapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// refreshServer stands in for the gateway's session endpoints: login issues a
// token and a refresh cookie, refresh rotates them.
type refreshServer struct {
	logins    atomic.Int32
	refreshes atomic.Int32
	// refuseRefresh makes refresh fail, as it does once the session is revoked.
	refuseRefresh atomic.Bool
	// tokenTTL is how long each issued token claims to live.
	tokenTTL time.Duration
}

func (s *refreshServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user/login", func(w http.ResponseWriter, r *http.Request) {
		s.logins.Add(1)
		exp := time.Now().Add(s.tokenTTL).Unix()
		http.SetCookie(w, &http.Cookie{Name: "new_api_refresh", Value: "sid.secret1", Path: "/api/user/auth", HttpOnly: true})
		_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"tok1","access_expires_at":` + itoa(exp) + `}}`))
	})
	mux.HandleFunc("/api/user/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		s.refreshes.Add(1)
		if s.refuseRefresh.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_SESSION_REVOKED","message":"Unauthorized"}`))
			return
		}
		c, err := r.Cookie("new_api_refresh")
		if err != nil || c.Value == "" {
			// No cookie means the jar never stored it, which is the defect this
			// test exists to catch.
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_UNAUTHORIZED","message":"no refresh cookie"}`))
			return
		}
		exp := time.Now().Add(s.tokenTTL).Unix()
		http.SetCookie(w, &http.Cookie{Name: "new_api_refresh", Value: "sid.secret2", Path: "/api/user/auth", HttpOnly: true})
		_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"tok2","access_expires_at":` + itoa(exp) + `}}`))
	})
	mux.HandleFunc("/api/user/self", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":1,"username":"napcat"}}`))
	})
	return mux
}

// The client must keep the refresh cookie: without a cookie jar the refresh call
// goes out unauthenticated, which is a silent fallback to a full login on every
// token expiry.
func TestClientKeepsTheRefreshCookie(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := c.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := s.refreshes.Load(); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
	if c.Token() != "tok2" {
		t.Errorf("the refreshed token must be installed, got %q", c.Token())
	}
	if exp := c.TokenExpiresAt(); exp == nil || time.Until(*exp) > 16*time.Minute {
		t.Errorf("the new expiry must be recorded, got %v", exp)
	}
}

// A refresh must not create a session: that is the whole point. Counting logins
// proves it — the gateway's session cap is what locks the service out.
func TestRefreshDoesNotLogIn(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := c.Refresh(); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}
	if got := s.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1: refreshing must not create sessions", got)
	}
}

// When the session behind the refresh token is gone, refresh fails with an auth
// error so the caller knows a real login is required.
func TestRefreshFailsWhenTheSessionIsGone(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	s.refuseRefresh.Store(true)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatal(err)
	}
	err := c.Refresh()
	if err == nil {
		t.Fatal("a revoked session must fail the refresh")
	}
	if !IsAuthFailure(err) {
		t.Errorf("the caller must be able to tell a login is needed: %v", err)
	}
}

// A refresh with no cookie at all must fail rather than return a zero token:
// silently succeeding would leave the client with no usable credential.
func TestRefreshWithoutASessionFails(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	if err := New(srv.URL).Refresh(); err == nil {
		t.Fatal("refresh without a session must fail")
	}
}

// Reauthenticate is the policy the service actually needs: refresh when that is
// possible, log in only when it is not. Every avoided login is a session the
// account does not spend.
func TestReauthenticatePrefersRefresh(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatal(err)
	}
	// A second reauthentication must refresh, not log in again.
	if err := c.Reauthenticate("admin", "pw"); err != nil {
		t.Fatalf("reauthenticate: %v", err)
	}
	if got := s.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1 (the refresh should have covered it)", got)
	}
	if got := s.refreshes.Load(); got != 1 {
		t.Errorf("refreshes = %d, want 1", got)
	}
}

// With no session to refresh, Reauthenticate falls back to a real login: that is
// the first run, or a session that was revoked.
func TestReauthenticateFallsBackToLogin(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Reauthenticate("admin", "pw"); err != nil {
		t.Fatalf("reauthenticate: %v", err)
	}
	if got := s.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
	if c.Token() == "" {
		t.Error("a token must be installed")
	}
}

// A revoked session must fall back to a login, and that login must work: the
// operator's fix (revoking sessions) has to be enough to bring the service back.
func TestReauthenticateRecoversFromARevokedSession(t *testing.T) {
	s := &refreshServer{tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatal(err)
	}
	s.refuseRefresh.Store(true)
	// The first attempt fails (refresh is refused, and the login that follows
	// also gets a fresh session, which the stub serves happily).
	if err := c.Reauthenticate("admin", "pw"); err != nil {
		t.Fatalf("a revoked session must fall back to a login: %v", err)
	}
	if got := s.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2 (the fallback login)", got)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
