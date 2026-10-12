package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/store"
)

// A NapCat restart invalidates the WebUI credential (it is signed with a
// per-process secret). The cached bridge must notice the refusal, drop the dead
// credential, and log in again on the next call — otherwise the fallback path
// stays broken until the operator edits the configuration or restarts this
// service.
func TestWebUICacheRecoversAfterCredentialRefused(t *testing.T) {
	var mu sync.Mutex
	logins := 0
	generation := 1 // the credential the server currently accepts

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			mu.Lock()
			logins++
			generation++
			cred := "cred-" + string(rune('0'+generation))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{"Credential": cred, "require2FA": false},
			})
		case "/api/Debug/call":
			mu.Lock()
			want := "cred-" + string(rune('0'+generation))
			mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer "+want {
				// NapCat's real shape: HTTP 200 with code -1 and "Unauthorized".
				_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "message": "Unauthorized"})
				return
			}
			// The bridge forwards a full OneBot envelope inside data.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{
					"status": "ok", "retcode": 0,
					"data": []map[string]any{{"group_id": 111, "group_name": "g1"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "recover.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := config.NewManager(filepath.Join(dir, config.FileName), st)
	if err := mgr.SaveSection(config.SectionNapCat, config.Config{
		NapCat: config.NapCat{WebUIURL: srv.URL, WebUIToken: "tok"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &lazyQQ{cfg: mgr}

	// First call logs in and works.
	if _, err := l.GetGroupList(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	mu.Lock()
	after1 := logins
	mu.Unlock()
	if after1 != 1 {
		t.Fatalf("logins after first call = %d, want 1", after1)
	}

	// Simulate a NapCat restart: bump the generation so the cached credential
	// is now refused.
	mu.Lock()
	generation++
	mu.Unlock()

	// The refusal must be recognised, the cache dropped, and a re-login issued
	// — within this call's failover or the next one, not never.
	var lastErr error
	for i := 0; i < 3; i++ {
		gs, err := l.GetGroupList()
		if err == nil && len(gs) == 1 {
			lastErr = nil
			break
		}
		lastErr = err
	}
	if lastErr != nil {
		t.Fatalf("fallback never recovered: %v", lastErr)
	}
	mu.Lock()
	after := logins
	mu.Unlock()
	if after <= after1 {
		t.Fatalf("no re-login after the credential was refused (logins=%d)", after)
	}
}

// A business failure (an expired QQ login) must NOT drop the cached bridge:
// re-logging-in cannot fix it and each login spends NapCat's login rate limit.
func TestWebUICacheSurvivesBusinessFailure(t *testing.T) {
	var mu sync.Mutex
	logins := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			mu.Lock()
			logins++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{"Credential": "cred", "require2FA": false},
			})
		default:
			// A business failure, not an auth rejection.
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "message": "QQ Not Login"})
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "biz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := config.NewManager(filepath.Join(dir, config.FileName), st)
	if err := mgr.SaveSection(config.SectionNapCat, config.Config{
		NapCat: config.NapCat{WebUIURL: srv.URL, WebUIToken: "tok"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &lazyQQ{cfg: mgr}

	for i := 0; i < 3; i++ {
		_, _ = l.GetGroupList()
	}
	mu.Lock()
	got := logins
	mu.Unlock()
	if got != 1 {
		t.Fatalf("business failure caused %d logins, want 1 (login budget spent)", got)
	}
}
