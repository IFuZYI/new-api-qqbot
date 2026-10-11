package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// deadSessionGateway models the operator revoking sessions in new-api's list:
// while `revoked` is set, the session the client holds is refused. A login
// clears it, because a login creates a new session.
//
// seenRefresh, when set, receives the value of every refresh cookie presented
// to this instance — a test uses it to prove which credential reached which
// host.
type deadSessionGateway struct {
	mu          sync.Mutex
	revoked     bool
	logins      atomic.Int32
	refreshes   atomic.Int32
	seenRefresh chan string
}

func (g *deadSessionGateway) isRevoked() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.revoked
}

func (g *deadSessionGateway) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/user/login", func(w http.ResponseWriter, r *http.Request) {
		g.logins.Add(1)
		g.mu.Lock()
		g.revoked = false // a new session is not the revoked one
		g.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name: "new_api_refresh", Value: "3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret",
			Path: "/api/user/auth", HttpOnly: true,
		})
		writeEnvelope(w, map[string]any{"access_token": "tok", "access_expires_at": 9999999999})
	})

	mux.HandleFunc("/api/user/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		g.refreshes.Add(1)
		// No cookie presented: the real gateway answers 401, and that is what
		// makes the client log in. Modelling this is what lets a test tell
		// "reused the stored session" from "logged in again".
		ck, cerr := r.Cookie("new_api_refresh")
		if cerr != nil || ck.Value == "" {
			w.WriteHeader(http.StatusUnauthorized)
			writeEnvelopeErr(w, "AUTH_UNAUTHORIZED")
			return
		}
		if g.seenRefresh != nil {
			select {
			case g.seenRefresh <- ck.Value:
			default:
			}
		}
		if g.isRevoked() {
			w.WriteHeader(http.StatusUnauthorized)
			writeEnvelopeErr(w, "AUTH_SESSION_REVOKED")
			return
		}
		writeEnvelope(w, map[string]any{"access_token": "tok", "access_expires_at": 9999999999})
	})

	// The endpoints the sweeps use: they refuse a revoked session with the same
	// code the real gateway sends.
	serve := func(w http.ResponseWriter) {
		if g.isRevoked() {
			w.WriteHeader(http.StatusUnauthorized)
			writeEnvelopeErr(w, "AUTH_SESSION_REVOKED")
			return
		}
		writeEnvelope(w, map[string]any{"items": []any{}, "total": 0})
	}
	mux.HandleFunc("/api/user/search", func(w http.ResponseWriter, r *http.Request) { serve(w) })
	mux.HandleFunc("/api/user/", func(w http.ResponseWriter, r *http.Request) { serve(w) })
	mux.HandleFunc("/api/user/1", func(w http.ResponseWriter, r *http.Request) { serve(w) })
	mux.HandleFunc("/api/user/groups", func(w http.ResponseWriter, r *http.Request) {
		if g.isRevoked() {
			w.WriteHeader(http.StatusUnauthorized)
			writeEnvelopeErr(w, "AUTH_SESSION_REVOKED")
			return
		}
		// The real shape: a map of group name to its settings.
		writeEnvelope(w, map[string]any{"default": map[string]any{"desc": "默认", "ratio": 1}})
	})
	mux.HandleFunc("/api/user/manage", func(w http.ResponseWriter, r *http.Request) { serve(w) })
	return mux
}

// Every gateway call must notice a revoked session and drop the dead token, so
// the next call logs in again.
//
// This is the bug the operator kept hitting: FindUserByEmail, Groups and
// ListUsers returned their error without invalidating, so the cached client kept
// its revoked token and every later call failed the same way — the service never
// recovered on its own, and a restart was the only thing that fixed it.
func TestEveryGatewayCallRecoversFromARevokedSession(t *testing.T) {
	readers := []struct {
		name string
		call func(*lazyGateway) error
	}{
		{"FindUserByEmail", func(g *lazyGateway) error { _, err := g.FindUserByEmail("x@qq.com"); return err }},
		{"Groups", func(g *lazyGateway) error { _, err := g.Groups(); return err }},
		{"ListUsers", func(g *lazyGateway) error { _, err := g.ListUsers(); return err }},
		{"GetUser", func(g *lazyGateway) error { _, err := g.GetUser(1); return err }},
		{"SetGroup", func(g *lazyGateway) error { return g.SetGroup(&newapi.User{ID: 1}, "vip") }},
		{"AddQuota", func(g *lazyGateway) error { return g.AddQuota(1, 10) }},
	}

	for _, tc := range readers {
		t.Run(tc.name, func(t *testing.T) {
			stub := &deadSessionGateway{}
			_, _, _, gw := newGatewayEnvFor(t, stub)

			// Establish a working session first, so the client is CACHED. Only
			// then does a revoke test whether this call notices the dead token:
			// with nothing cached there is nothing to invalidate, and the test
			// would pass even with the invalidate call removed.
			if err := gw.waitForSession(); err != nil {
				t.Fatalf("precondition: could not establish a session: %v", err)
			}
			before := stub.logins.Load()

			// The operator revokes every session, as in new-api's session list.
			stub.mu.Lock()
			stub.revoked = true
			stub.mu.Unlock()

			// This call fails on the revoked token (that is expected)...
			_ = tc.call(gw)
			// ...but it must have dropped it, so the next call works again.
			for i := 0; i < 3; i++ {
				if err := tc.call(gw); err != nil {
					t.Fatalf("the service must recover by itself, but call %d still failed: %v", i, err)
				}
			}
			if got := stub.logins.Load() - before; got != 1 {
				t.Errorf("recovery cost %d logins, want exactly 1", got)
			}
		})
	}
}

// A single revoked read must not be retried on every call forever: after the
// re-login the client is cached again, so the session count stays at one.
func TestRecoveryDoesNotLogInRepeatedly(t *testing.T) {
	// Start with no session at all, so the first call has to log in.
	stub := &deadSessionGateway{revoked: true}
	_, _, _, gw := newGatewayEnvFor(t, stub)

	// The first call finds the dead session and recovers by logging in.
	_, _ = gw.ListUsers()
	if err := gw.waitForSession(); err != nil {
		t.Fatalf("the service must establish a session: %v", err)
	}
	if _, err := gw.ListUsers(); err != nil {
		t.Fatalf("call after recovery: %v", err)
	}
	// The operator revokes it. Recovering must cost exactly ONE more login, no
	// matter how many calls follow.
	before := stub.logins.Load()
	stub.mu.Lock()
	stub.revoked = true
	stub.mu.Unlock()
	_, _ = gw.ListUsers()
	for i := 0; i < 5; i++ {
		if _, err := gw.ListUsers(); err != nil {
			t.Fatalf("call %d after the revoke: %v", i, err)
		}
	}
	if got := stub.logins.Load() - before; got != 1 {
		t.Errorf("recovery cost %d logins, want exactly 1", got)
	}
}

// waitForSession drives one successful call, so a test can assert on the state
// after the service has established a working session.
func (l *lazyGateway) waitForSession() error {
	for i := 0; i < 3; i++ {
		if _, err := l.ListUsers(); err == nil {
			return nil
		}
	}
	_, err := l.ListUsers()
	return err
}

// A stored session belongs to the host and account that issued it. Repointing
// the service at another gateway must NOT present the first host's credential
// to the second one — the refresh cookie would be transmitted verbatim to a
// host that never issued it, which is both a credential leak and, when the two
// are unrelated instances, a silent identity confusion.
//
// This is the defect the review proved: client() imported the stored session
// whenever it rebuilt the client, and a URL change rebuilds it.
func TestStoredSessionIsNotSentToANewHost(t *testing.T) {
	// Host A: the service logs in and persists its session.
	stubA := &deadSessionGateway{}
	srvA, st, mgr, gw := newGatewayEnvFor(t, stubA)
	if err := gw.waitForSession(); err != nil {
		t.Fatalf("host A: could not establish a session: %v", err)
	}
	if got := mgr.LoadNewAPISession(config.SessionOrigin(srvA.URL, "napcat", "pw")); got == "" {
		t.Fatal("host A: the session must be persisted")
	}

	// The operator repoints the service at host B. A fresh lazyGateway stands
	// in for the rebuilt client (a config change drops the cached one).
	seen := make(chan string, 4)
	stubB := &deadSessionGateway{seenRefresh: seen}
	srvB := httptest.NewServer(stubB.handler())
	t.Cleanup(srvB.Close)

	cfgB := mgr.MustLoad()
	cfgB.NewAPI.URL = srvB.URL
	if err := mgr.Save(cfgB); err != nil {
		t.Fatalf("save config: %v", err)
	}
	gwB := &lazyGateway{cfg: mgr, lockout: newapi.NewLockout(nil)}
	if err := gwB.waitForSession(); err != nil {
		t.Fatalf("host B: the service must work against the new host: %v", err)
	}

	// Whatever host B received, it must not be host A's token.
	close(seen)
	for token := range seen {
		if token != "" && token == "3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret" {
			t.Fatalf("host B received host A's stored session verbatim — the credential leaked across hosts")
		}
	}
	_ = st
}

// newGatewayEnvFor points a lazyGateway at an arbitrary stub, with a real store
// and config manager behind it.
func newGatewayEnvFor(t *testing.T, stub *deadSessionGateway) (*httptest.Server, *store.Store, *config.Manager, *lazyGateway) {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rec.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mgr := config.NewManager(filepath.Join(dir, config.FileName), st)
	if err := mgr.Save(config.Config{NewAPI: config.NewAPI{
		URL: srv.URL, Username: "napcat", Password: "pw",
	}}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return srv, st, mgr, &lazyGateway{cfg: mgr, lockout: newapi.NewLockout(nil)}
}

// A trailing slash is the same gateway address. The operator pastes the URL
// with or without it and it is stored verbatim, so comparing the raw stored
// value against a trimmed argument made the identical address fail to match —
// and 测试连接 then logged in for real (a new session plus a login-budget
// spend) on a configuration that had not changed.
func TestMatchesTreatsATrailingSlashAsTheSameAddress(t *testing.T) {
	_, _, mgr, _ := newGatewayEnvFor(t, &deadSessionGateway{})
	cfg := mgr.MustLoad()

	withSlash := mgr
	if err := withSlash.Save(config.Config{NewAPI: config.NewAPI{
		URL: cfg.NewAPI.URL + "/", Username: "napcat", Password: "pw",
	}}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	gw := &lazyGateway{cfg: withSlash}

	for _, arg := range []string{cfg.NewAPI.URL, cfg.NewAPI.URL + "/"} {
		if !gw.matches(arg, "napcat", "pw") {
			t.Errorf("a stored URL with a trailing slash must match %q", arg)
		}
	}
	// A different address must still not match.
	if gw.matches("http://other.example.test", "napcat", "pw") {
		t.Error("a different host must not match")
	}
	// Nor must a different password.
	if gw.matches(cfg.NewAPI.URL, "napcat", "other") {
		t.Error("a different password must not match")
	}
}
