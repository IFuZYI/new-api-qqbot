package newapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The login endpoint is behind the gateway's critical rate limiter, which
// answers 429 with an EMPTY body and a Retry-After hint. A client that only
// decodes JSON turns that into "unexpected response (HTTP 429):" with nothing
// after the colon — the operator learns the number but not the cause, and the
// only hint the server gave (how long to wait) is thrown away.
func TestLoginRateLimitedIsRecognised(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "37")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.Login("admin", "pw")
	if err == nil {
		t.Fatal("a 429 must be an error")
	}
	if !IsRateLimited(err) {
		t.Errorf("a 429 must be recognisable as rate limiting: %v", err)
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("the error must carry the retry hint, got %T: %v", err, err)
	}
	if rl.RetryAfter != 37*time.Second {
		t.Errorf("Retry-After = %v, want 37s", rl.RetryAfter)
	}
	// The message must say what happened and what to do about it: an operator
	// reading the WebUI cannot act on a bare status code.
	msg := err.Error()
	if !strings.Contains(msg, "登录") || !strings.Contains(msg, "37") {
		t.Errorf("the message must name the cause and the wait: %q", msg)
	}
}

// A 429 without Retry-After must still be recognisable, with no fabricated wait.
func TestLoginRateLimitedWithoutRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	err := New(srv.URL).Login("admin", "pw")
	if !IsRateLimited(err) {
		t.Fatalf("a 429 without a hint is still rate limiting: %v", err)
	}
	var rl *RateLimitError
	if errors.As(err, &rl) && rl.RetryAfter != 0 {
		t.Errorf("no Retry-After means no wait may be invented, got %v", rl.RetryAfter)
	}
}

// Any other non-JSON failure must name the status, not just echo an empty body.
func TestNonJSONFailureNamesTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>nginx</html>"))
	}))
	defer srv.Close()

	err := New(srv.URL).Login("admin", "pw")
	if err == nil {
		t.Fatal("a 502 must be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") {
		t.Errorf("the status must appear in the message: %q", msg)
	}
	if strings.Contains(msg, "<html>") {
		t.Errorf("an HTML body must not be pasted into the message: %q", msg)
	}
}

// The session cap is a distinct, actionable state: retrying cannot fix it, and
// the operator has to revoke sessions or wait for them to expire.
func TestSessionLimitIsItsOwnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_SESSION_LIMIT","message":"Conflict"}`))
	}))
	defer srv.Close()

	err := New(srv.URL).Login("admin", "pw")
	if !IsSessionLimit(err) {
		t.Fatalf("AUTH_SESSION_LIMIT must be recognisable: %v", err)
	}
	if !strings.Contains(err.Error(), "会话") {
		t.Errorf("the message must explain the session cap: %q", err)
	}
}

// A rejection that is neither of the two known states must not be misreported
// as one of them: a wrong password is not a rate limit.
func TestWrongPasswordIsNotRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"用户名或密码错误"}`))
	}))
	defer srv.Close()

	err := New(srv.URL).Login("admin", "wrong")
	if err == nil {
		t.Fatal("a rejected login must be an error")
	}
	if IsRateLimited(err) || IsSessionLimit(err) {
		t.Errorf("a bad password must not be reported as a limiter state: %v", err)
	}
}

// The operator's report was a message ending in a colon with nothing after it:
// "POST /api/user/login: unexpected response (HTTP 429):". That is what a
// bare-429 body decodes to when the status is not checked first. The message
// must now name the cause, and the login must not be retried into a longer
// lockout.
func TestTheReported429IsExplained(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "116")
		w.WriteHeader(http.StatusTooManyRequests)
		// No body at all: exactly what the gateway's limiter sends.
	}))
	defer srv.Close()

	err := New(srv.URL).Login("admin", "pw")
	if err == nil {
		t.Fatal("a 429 must be reported")
	}
	msg := err.Error()
	if strings.Contains(msg, "unexpected response") {
		t.Errorf("the old opaque message must be gone: %q", msg)
	}
	if !strings.Contains(msg, "限流") {
		t.Errorf("the message must name rate limiting: %q", msg)
	}
	if !strings.Contains(msg, "1m56s") && !strings.Contains(msg, "116") {
		t.Errorf("the wait the server announced must be shown: %q", msg)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestRetryAfterOf(t *testing.T) {
	err := &RateLimitError{RetryAfter: 90 * time.Second}
	if got := RetryAfterOf(err); got != 90*time.Second {
		t.Errorf("RetryAfterOf = %v, want 90s", got)
	}
	if got := RetryAfterOf(nil); got != 0 {
		t.Errorf("RetryAfterOf(nil) = %v, want 0", got)
	}
	if got := RetryAfterOf(errRateString("x")); got != 0 {
		t.Errorf("an unrelated error has no hint, got %v", got)
	}
}

type errRateString string

func (e errRateString) Error() string { return string(e) }

var _ = strconv.Itoa
