package newapi

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// parseRetryAfter is the whole value of a bare 429: the limiter answers with no
// body, so the header is the only thing that says how long to wait. The two
// failures it must avoid are inventing a wait the server never gave (the client
// would sleep and then hammer the limiter) and producing a value that reads as
// "not rate limited".
func TestParseRetryAfterBoundaries(t *testing.T) {
	// No header / no usable value means "no hint": exactly zero, never a
	// negative or invented period.
	for _, v := range []string{"", "   ", "0", "-5", "-1", "soon", "37s", "1.5", "0x10"} {
		if got := parseRetryAfter(v); got != 0 {
			t.Errorf("parseRetryAfter(%q) = %v, want 0 (no hint)", v, got)
		}
	}

	// A valid number of seconds is the gateway's normal answer.
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"37", 37 * time.Second},
		{"  120  ", 120 * time.Second},
		{"1", time.Second},
	} {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	// An HTTP-date is the other form the header is allowed to take. A date in
	// the future yields the remaining wait; one already past is no hint at all,
	// not a negative period.
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 90*time.Second {
		t.Errorf("parseRetryAfter(future date) = %v, want (0s, 90s]", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("parseRetryAfter(past date) = %v, want 0", got)
	}

	// A value that overflows a time.Duration must never be accepted as a real
	// wait: the client would sleep on a wrapped-around number.
	if got := parseRetryAfter("9223372036854775807"); got > 0 {
		t.Errorf("an overflowing Retry-After must not become a wait, got %v", got)
	}
	if got := parseRetryAfter("9223372037"); got > 0 {
		t.Errorf("an overflowing Retry-After must not become a wait, got %v", got)
	}

	// The consequence that matters: a hint that cannot be represented must not
	// disable the lockout. A negative RetryAfter would read as "not rate
	// limited" and the client would retry immediately, so the lockout must
	// still impose a positive wait of its own.
	err := &RateLimitError{Method: http.MethodPost, Path: "/api/user/login", RetryAfter: parseRetryAfter("9223372036854775807")}
	if !IsRateLimited(err) {
		t.Fatal("an overflowing hint must still be a rate limit")
	}
	if got := RetryAfterOf(err); got > 0 {
		t.Errorf("an overflowing hint must not be reported as a wait, got %v", got)
	}
	l := NewLockout(func() time.Time { return time.Unix(1000, 0) })
	l.Fail(err)
	blocked := l.Blocked()
	if blocked == nil {
		t.Fatal("a rate limit must still arm the lockout")
	}
	var rl *RateLimitError
	if !errors.As(blocked, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("the lockout must impose a positive wait, got %+v (%v)", rl, blocked)
	}
}
