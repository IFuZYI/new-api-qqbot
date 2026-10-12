package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// The connection prober and the patrols must be able to use the login lockout
// at the same time.
//
// An independent reviewer reproduced a real data race here with -race: the
// prober's ProbeGateway touched the shared *newapi.Lockout from an HTTP handler
// goroutine while lazyGateway.client() mutated it from a sweep goroutine, with
// no common lock. A torn `until` reads as "not rate limited" and silently
// disarms the cooldown — the mechanism that stops a refusal from feeding itself.
// The green -race gate missed it because no test drove the two concurrently.
//
// The fix moved the synchronisation into Lockout itself (Blocked/Fail/Success),
// so this test pins the property at the level the two owners actually share.
func TestProberAndSweepsShareTheLockoutSafely(t *testing.T) {
	// A gateway that always answers 429, so every attempt arms the lockout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"success":false,"message":"rate limited"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "race.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := config.NewManager(filepath.Join(dir, config.FileName), st)
	if err := mgr.Save(config.Config{NewAPI: config.NewAPI{
		URL: srv.URL, Username: "u", Password: "p",
	}}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	gw := &lazyGateway{cfg: mgr, lockout: newapi.NewLockout(nil)}
	p := prober{gw: gw}

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers * 2)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			// The HTTP handler path: the connection test.
			_, _, _ = p.ProbeGateway(srv.URL, "u", "p")
		}()
		go func() {
			defer wg.Done()
			// The sweep path: what the lockout looks like from a patrol
			// goroutine, through the same lazy gateway.
			if _, err := gw.client(); err != nil {
				// Expected: the stub refuses. The point is the shared state.
				_ = err
			}
			_ = gw.lockout.Blocked()
		}()
	}
	wg.Wait()

	// The lockout must still be coherent: rate limits were recorded, so it is
	// blocking, and the message names the wait rather than being torn.
	if err := gw.lockout.Blocked(); err == nil {
		t.Error("the lockout was disarmed by the concurrent access; a torn until reads as not-blocked")
	} else {
		slog.Debug("lockout coherent after concurrent use", "err", err)
	}
}
