package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qqbot/internal/config"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// gwStub is a gateway that counts how the service authenticates: a login creates
// a session, a refresh reuses one. It is the difference the operator sees in
// new-api's session list, where every login shows up as another browser session
// living for 30 days.
type gwStub struct {
	mu        sync.Mutex
	logins    int
	refreshes int
	sessions  map[string]bool
}

func newGWStub() *gwStub { return &gwStub{sessions: map[string]bool{}} }

func (s *gwStub) counts() (logins, refreshes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins, s.refreshes
}

func (s *gwStub) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/user/login", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.logins++
		sid := fmt.Sprintf("%08x-4f89-11d3-9a0c-%012x", s.logins, s.logins)
		s.sessions[sid] = true
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name: "new_api_refresh", Value: sid + ".secret", Path: "/api/user/auth", HttpOnly: true,
		})
		// A one-second token, so every later call finds it stale and has to
		// reauthenticate: that is the moment the old code logged in again.
		writeEnvelope(w, map[string]any{
			"access_token":      "tok-login",
			"access_expires_at": time.Now().Add(time.Second).Unix(),
		})
	})

	mux.HandleFunc("/api/user/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie("new_api_refresh")
		s.mu.Lock()
		s.refreshes++
		sid := ""
		if err == nil {
			sid = strings.SplitN(ck.Value, ".", 2)[0]
		}
		known := sid != "" && s.sessions[sid]
		s.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusUnauthorized)
			// A real failure envelope: success must be false, or a client would
			// read this as a success carrying no token.
			writeEnvelopeErr(w, "AUTH_UNAUTHORIZED")
			return
		}
		writeEnvelope(w, map[string]any{
			"access_token":      "tok-refresh",
			"access_expires_at": time.Now().Add(time.Second).Unix(),
		})
	})

	mux.HandleFunc("/api/user/self", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, map[string]any{"id": 1, "username": "napcat", "role": 100})
	})
	return mux
}

func writeEnvelope(w http.ResponseWriter, data map[string]any) {
	body, _ := json.Marshal(map[string]any{"success": true, "message": "", "data": data})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// writeEnvelopeErr writes the gateway's failure shape, which the client keys on.
func writeEnvelopeErr(w http.ResponseWriter, code string) {
	body, _ := json.Marshal(map[string]any{"success": false, "message": "no", "code": code})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// newGatewayEnv builds a lazyGateway pointed at the stub, with a real store and
// config manager behind it (a restart is just a second gateway over the same
// store).
func newGatewayEnv(t *testing.T) (*gwStub, *httptest.Server, *config.Manager, *lazyGateway) {
	t.Helper()
	s := newGWStub()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "gw.db"))
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
	return s, srv, mgr, &lazyGateway{cfg: mgr, lockout: newapi.NewLockout(nil)}
}

// The operator's report: several sessions created seconds apart in new-api's
// session list. Calling the API must not log in again — the service logs in once
// and then reuses that session, refreshing its short-lived access token in
// place. Rebuilding the client (which is what dropped the session cookie) is
// what forced a fresh login every time the token aged out.
func TestGatewayClientReusesItsSessionInsteadOfLoggingInAgain(t *testing.T) {
	stub, _, _, gw := newGatewayEnv(t)

	var first *newapi.Client
	for i := 0; i < 5; i++ {
		c, err := gw.client()
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if i == 0 {
			first = c
			continue
		}
		if c != first {
			t.Errorf("call %d built a new client: that drops the session cookie, so the "+
				"next request has to log in again", i)
		}
	}

	logins, refreshes := stub.counts()
	if logins != 1 {
		t.Errorf("logins = %d, want 1: each login adds a session to the gateway's list", logins)
	}
	if refreshes < 4 {
		t.Errorf("refreshes = %d, want at least 4: the token must be refreshed in place", refreshes)
	}
}

// A restart must not log in either: the session from the last run is reused.
func TestGatewayClientReusesTheStoredSessionAfterARestart(t *testing.T) {
	stub, _, mgr, gw := newGatewayEnv(t)

	if _, err := gw.client(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	loginsAfterFirst, _ := stub.counts()
	if loginsAfterFirst != 1 {
		t.Fatalf("precondition: logins = %d, want 1", loginsAfterFirst)
	}

	// A restart: a brand-new gateway over the same store.
	restarted := &lazyGateway{cfg: mgr, lockout: newapi.NewLockout(nil)}
	if _, err := restarted.client(); err != nil {
		t.Fatalf("after restart: %v", err)
	}

	logins, refreshes := stub.counts()
	if logins != 1 {
		t.Errorf("logins = %d, want 1: a restart must reuse the stored session, "+
			"not spend another one", logins)
	}
	if refreshes < 1 {
		t.Errorf("refreshes = %d, want at least 1: the stored session must be refreshed", refreshes)
	}
}

// The connection test is a manual action, but it is still "calling the API":
// testing the settings that are already saved must not log in again either.
func TestProbeReusesTheLiveSessionWhenTheSettingsAreUnchanged(t *testing.T) {
	stub, srv, mgr, gw := newGatewayEnv(t)
	p := prober{gw: gw}

	if _, _, err := p.ProbeGateway(srv.URL, "napcat", "pw"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	loginsAfterFirst, _ := stub.counts()
	if loginsAfterFirst != 1 {
		t.Fatalf("precondition: logins = %d, want 1 (the first probe must authenticate)", loginsAfterFirst)
	}

	// Same settings again: no new session.
	user, reused, err := p.ProbeGateway(srv.URL, "napcat", "pw")
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if user == nil || user.Username != "napcat" {
		t.Errorf("the probe must report the account: %+v", user)
	}
	if !reused {
		t.Error("the second probe must report that it reused the session")
	}
	if logins, _ := stub.counts(); logins != 1 {
		t.Errorf("logins = %d, want 1: clicking 测试连接 must not create a session", logins)
	}
	_ = mgr
}

// Different credentials cannot be checked without logging in: a session proves
// the old ones. The probe must still do the real thing, and say so.
func TestProbeLogsInForChangedCredentials(t *testing.T) {
	stub, srv, _, gw := newGatewayEnv(t)
	p := prober{gw: gw}

	if _, _, err := p.ProbeGateway(srv.URL, "napcat", "pw"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, reused, err := p.ProbeGateway(srv.URL, "someone-else", "other-pw"); err != nil {
		t.Fatalf("probe with new credentials: %v", err)
	} else if reused {
		t.Error("a credential change must be verified with a real login")
	}
	if logins, _ := stub.counts(); logins != 2 {
		t.Errorf("logins = %d, want 2 (the first probe and the credential change)", logins)
	}
}
