package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"qqbot/internal/napcat"
)

// The connection page's 自动配置 button must log in before writing adapters.
//
// napcatAdmin.EnsureAdapters is never constructed in any test (the api tests
// inject fakes), so dropping its w.Login() failed nothing — and the button would
// then write the adapters unauthenticated and report whatever NapCat answered.
func TestEnsureAdaptersLogsInFirst(t *testing.T) {
	var logins int32
	var writes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			atomic.AddInt32(&logins, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{"Credential": "cred-1", "require2FA": false},
			})
		case "/api/OB11Config/GetConfig":
			// A write cannot have happened before the login.
			if atomic.LoadInt32(&logins) == 0 {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "message": "Unauthorized"})
				return
			}
			atomic.AddInt32(&writes, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{
					"network": map[string]any{"websocketClients": []any{}, "httpClients": []any{}},
				},
			})
		case "/api/OB11Config/SetConfig":
			if atomic.LoadInt32(&logins) == 0 {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "message": "Unauthorized"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "ok", "data": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res, err := napcatAdmin{}.EnsureAdapters(srv.URL, "tok", napcat.AdapterOptions{
		Token: "tok", ConnectHost: "127.0.0.1", BindHost: "127.0.0.1",
		HTTPPort: 3002, WSPort: 3001,
	})
	if err != nil {
		t.Fatalf("EnsureAdapters: %v", err)
	}
	if res == nil {
		t.Fatal("no adapter result")
	}
	if atomic.LoadInt32(&logins) == 0 {
		t.Error("the adapters were provisioned without logging in")
	}
	if atomic.LoadInt32(&writes) == 0 {
		t.Error("NapCat was never asked for its config")
	}
}

// A refused login must be reported, not turned into a partial write.
func TestEnsureAdaptersReportsALoginFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "message": "token is invalid"})
			return
		}
		// Any other call means the code wrote without a session.
		t.Errorf("NapCat was called at %s despite the failed login", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := napcatAdmin{}.EnsureAdapters(srv.URL, "bad", napcat.AdapterOptions{
		Token: "bad", ConnectHost: "127.0.0.1", BindHost: "127.0.0.1",
		HTTPPort: 3002, WSPort: 3001,
	})
	if err == nil {
		t.Fatal("a refused login must be reported")
	}
	if !strings.Contains(err.Error(), "token is invalid") {
		t.Errorf("the reason was lost: %v", err)
	}
}
