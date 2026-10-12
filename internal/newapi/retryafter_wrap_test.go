package newapi

import (
	"sync"
	"testing"
	"time"
)

// A wrapped-positive Retry-After must be rejected, not stored.
//
// The seconds form is multiplied by 1e9 into a time.Duration. An int64 seconds
// value whose product wraps modulo 2^64 into a POSITIVE result passed the
// `secs > 0` guard and the caller's `wait <= 0` check, so it was stored verbatim
// as the lockout's expiry: measured, parseRetryAfter("5504833656916111959")
// returned ~292 years, which parks every gateway call until the process
// restarts and shows the operator "需等待约 2561989h30m57s 后重试".
//
// The original test chose 9223372036854775807 and 9223372037 — both of which
// wrap NEGATIVE, the one case the guard already caught — so the class stayed
// open while reading as covered.
func TestParseRetryAfterRejectsAWrappedPositiveHint(t *testing.T) {
	// The two values that wrap POSITIVE (found by an independent reviewer).
	for _, v := range []string{"5504833656916111959", "6924913093273118368"} {
		if got := parseRetryAfter(v); got != 0 {
			t.Errorf("parseRetryAfter(%q) = %v (%v); a wrapped hint must be reported as absent, not stored", v, got, got)
		}
	}
	// And a plainly absurd but non-wrapping value is refused too: no window this
	// service acts on is longer than a day.
	if got := parseRetryAfter("86401"); got != 0 {
		t.Errorf("parseRetryAfter(86401) = %v, want 0 (a day is the ceiling)", got)
	}
	// The real hints still work.
	for _, c := range []struct {
		in   string
		want time.Duration
	}{{"60", time.Minute}, {"1", time.Second}, {"86400", 24 * time.Hour}} {
		if got := parseRetryAfter(c.in); got != c.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A wrapped hint must not wedge the lockout in practice, not just in the parser.
func TestAWrappedHintDoesNotWedgeTheLockout(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	l := NewLockout(func() time.Time { return now })

	hint := parseRetryAfter("5504833656916111959")
	l.Fail(&RateLimitError{Method: "POST", Path: "/api/user/login", RetryAfter: hint})

	// The lockout's own default applies (2 minutes), not the wrapped value.
	now = now.Add(3 * time.Minute)
	if err := l.Blocked(); err != nil {
		t.Errorf("the lockout is still blocked after 3 minutes: %v (a wrapped hint was stored as the expiry)", err)
	}
}

// Lockout is reached from two goroutines with no shared caller lock: the lazy
// gateway's client() (holding its own mutex) and the connection prober's
// ProbeGateway (an HTTP handler goroutine holding nothing). Run with -race, the
// unsynchronised fields were a real data race on both `until` and `last`.
func TestLockoutIsRaceFree(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	l := NewLockout(clock)

	var wg sync.WaitGroup
	// Writers: what the prober and the gateway client do.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if i%2 == 0 {
					l.Fail(&RateLimitError{Method: "POST", Path: "/api/user/login", RetryAfter: time.Minute})
				} else {
					l.Success()
				}
			}
		}(i)
	}
	// Readers: every call site consults Blocked first.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = l.Blocked()
			}
		}()
	}
	wg.Wait()
}
