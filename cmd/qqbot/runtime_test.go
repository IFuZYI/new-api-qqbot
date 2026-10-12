package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"qqbot/internal/newapi"
	"qqbot/internal/service"
	"qqbot/internal/store"
	"sync"
	"testing"
	"time"
)

func TestRuntimeShutdownJoinsWorkers(t *testing.T) {
	var workers sync.WaitGroup
	release := make(chan struct{})
	workers.Go(func() { <-release })
	done := make(chan struct{})
	go func() { shutdownRuntime(&http.Server{}, &workers, time.Second); close(done) }()
	select {
	case <-done:
		close(release)
		t.Fatal("shutdown returned before workers finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join")
	}
}

func TestRuntimeHTTPShutdownHasDeadline(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	defer srv.Close()
	defer close(release)
	go func() {
		resp, err := http.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() { shutdownRuntime(srv.Config, &workers, 20*time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP shutdown ignored deadline")
	}
}

func TestRuntimeProbeHonorsExistingLockout(t *testing.T) {
	stub, srv, _, gw := newGatewayEnv(t)
	gw.lockout.Fail(&newapi.RateLimitError{RetryAfter: time.Minute})
	_, _, err := (prober{gw: gw}).ProbeGateway(srv.URL, "napcat", "pw")
	var rate *newapi.RateLimitError
	if !errors.As(err, &rate) {
		t.Errorf("expected cooldown, got %v", err)
	}
	if logins, _ := stub.counts(); logins != 0 {
		t.Fatalf("cooldown bypassed: %d logins", logins)
	}
}

func TestRuntimeProbeHonorsNewLockout(t *testing.T) {
	_, _, mgr, gw := newGatewayEnv(t)
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	cfg := mgr.MustLoad()
	cfg.NewAPI.URL = srv.URL
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, _, err := (prober{gw: gw}).ProbeGateway(srv.URL, "napcat", "pw")
		var rate *newapi.RateLimitError
		if !errors.As(err, &rate) {
			t.Errorf("expected cooldown, got %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected single rate-limited attempt, got %d", calls)
	}
}

func runtimeStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestBindAdapterUsesConfiguredCalendar(t *testing.T) {
	st := runtimeStore(t)
	svc := service.New(st, nil, nil)
	cfg, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Timezone = "Pacific/Kiritimati"
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	calendar, ok := any(bindAdapter{svc}).(interface{ MessageIsToday(int64) bool })
	if !ok {
		t.Fatal("adapter does not supply the configured calendar")
	}
	today, err := svc.Today()
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	midnight, err := time.ParseInLocation("2006-01-02", today, loc)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{midnight.Add(-time.Second), false}, {midnight, true},
		{midnight.Add(23 * time.Hour), true}, {midnight.AddDate(0, 0, 1), false},
	} {
		if got := calendar.MessageIsToday(tc.at.Unix()); got != tc.want {
			t.Errorf("%v got %v want %v", tc.at, got, tc.want)
		}
	}
}
