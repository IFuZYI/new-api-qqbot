package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/store"
)

// The WebUI bridge must be logged in once and reused, not re-authenticated per
// call.
//
// A roster read is one call per group, so a degraded sweep (QQ login expired,
// OneBot answering with error envelopes) would otherwise perform one WebUI
// login per group. NapCat rate-limits its own login endpoint, so the fallback
// would fail partway through the sweep and leave the service with no working
// path — the opposite of what a fallback is for.
func TestWebUIClientLogsInOnce(t *testing.T) {
	var logins int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			atomic.AddInt32(&logins, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{"Credential": "cred-1", "require2FA": false},
			})
		case "/api/Debug/call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{"group_list": []map[string]any{
					{"group_id": 111, "group_name": "g1"},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "wu.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := config.NewManager(filepath.Join(dir, config.FileName), st)
	if err := mgr.SaveSection(config.SectionNapCat, config.Config{
		NapCat: config.NapCat{WebUIURL: srv.URL, WebUIToken: "tok"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &lazyQQ{cfg: mgr}

	// Three reads, as a sweep over three groups would do.
	for i := 0; i < 3; i++ {
		if _, err := l.webuiClient(); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&logins); got != 1 {
		t.Errorf("logged in %d times, want 1: the bridge is not being reused", got)
	}

	// Changing the stored token must force a fresh login rather than reusing a
	// credential minted for the old one.
	if err := mgr.SaveSection(config.SectionNapCat, config.Config{
		NapCat: config.NapCat{WebUIURL: srv.URL, WebUIToken: "tok-2"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.webuiClient(); err != nil {
		t.Fatalf("after config change: %v", err)
	}
	if got := atomic.LoadInt32(&logins); got != 2 {
		t.Errorf("logged in %d times after the token changed, want 2", got)
	}
}
