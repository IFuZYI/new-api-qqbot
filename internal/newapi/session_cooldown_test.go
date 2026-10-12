package newapi

import (
	"errors"
	"testing"
	"time"
)

// The 50-session cap must arm a cooldown, not be retried once per call.
//
// Measured on the reference gateway: 12 calls while the cap was hit produced 12
// login attempts, every one against the same CriticalRateLimit-protected route
// (reference/new-api/router/api-router.go:81). The error is not fixed by
// waiting a moment, but it IS fixed by the operator revoking sessions, and
// until they do, each attempt spends a limiter the next attempt then trips. The
// cooldown is deliberately long: unlike a 429, this needs a human.
func TestSessionLimitArmsALongCooldown(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	l := NewLockout(clock)

	if err := l.Blocked(); err != nil {
		t.Fatalf("a fresh lockout must not block: %v", err)
	}

	l.Fail(&SessionLimitError{Method: "POST", Path: "/api/user/login"})

	// The next call must be refused without a network attempt.
	if err := l.Blocked(); err == nil {
		t.Fatal("the session cap did not arm a cooldown")
	} else {
		var sl *SessionLimitError
		if !errors.As(err, &sl) {
			t.Errorf("the cooldown reports %T; it must stay a SessionLimitError so the message keeps naming the real cause", err)
		}
	}

	// It must outlast a 429's window (2 minutes by default): revoking sessions
	// is a human action.
	now = now.Add(5 * time.Minute)
	if err := l.Blocked(); err == nil {
		t.Error("the session-cap cooldown expired after 5 minutes; the operator needs longer than that to react")
	}
	now = now.Add(31 * time.Minute)
	if err := l.Blocked(); err != nil {
		t.Errorf("the cooldown still blocks after 36 minutes: %v", err)
	}
}

// A rate limit must still use the server's hint, not the session-cap window.
func TestRateLimitAndSessionCapHaveDifferentWindows(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	l := NewLockout(func() time.Time { return now })

	l.Fail(&RateLimitError{Method: "POST", Path: "/api/user/login", RetryAfter: 30 * time.Second})
	now = now.Add(31 * time.Second)
	if err := l.Blocked(); err != nil {
		t.Errorf("a 429 with a 30s hint must clear after 31s: %v", err)
	}
}

// A wrong password must not arm anything: waiting cannot fix it, and blocking
// would hide the real error.
func TestABadCredentialArmsNothing(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	l := NewLockout(func() time.Time { return now })

	l.Fail(errors.New("用户名或密码错误"))
	if err := l.Blocked(); err != nil {
		t.Errorf("a credential failure armed a cooldown: %v", err)
	}
}
