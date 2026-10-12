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

// A dead WebUI credential must not cost one login per call.
//
// The credential dies with every NapCat restart (it is signed with a
// per-process secret unless NAPCAT_WEBUI_JWT_SECRET_KEY is set), which is
// routine. The cached bridge is correctly invalidated when a call proves the
// credential was refused — but with no cooldown, the NEXT call logs in again,
// and again: a sweep over 200 groups whose OneBot path is also down would spend
// up to 200 logins. NapCat allows 10 per 60s, so the sweep exhausts the window
// and the fallback path fails with "login rate limit" instead of recovering —
// exactly what a fallback exists to avoid.
func TestDeadCredentialDoesNotCostALoginPerCall(t *testing.T) {
	var logins, reads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			atomic.AddInt32(&logins, 1)
			// A credential NapCat will refuse on use (its signing secret
			// changed), which is the routine case.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{"Credential": "stale", "require2FA": false},
			})
		case "/api/Debug/call":
			atomic.AddInt32(&reads, 1)
			// The refused credential: an auth failure, which is what makes the
			// cache invalidate.
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "message": "Unauthorized"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "cooldown.db"))
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

	// Twenty refused reads through the REAL failover path, as a sweep over
	// twenty groups would do: OneBot is unreachable and the WebUI credential is
	// refused, so each call invalidates the cache.
	for i := 0; i < 20; i++ {
		_, _ = l.GetGroupList()
	}

	// One login for the first attempt; the cooldown must absorb the rest.
	if got := atomic.LoadInt32(&logins); got > 3 {
		t.Errorf("logged in %d times for 20 refused reads; NapCat allows 10 per 60s and the sweep must not spend them", got)
	}
	if got := atomic.LoadInt32(&logins); got < 1 {
		t.Error("the bridge never attempted a login at all")
	}
}
