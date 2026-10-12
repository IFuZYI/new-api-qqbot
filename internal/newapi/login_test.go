package newapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// authStub serves the two session endpoints separately, the way the gateway
// does: /api/user/auth/refresh answers 401 when the client has no session (which
// is the case for a client that never logged in), while /api/user/login carries
// the failure a test is actually about.
func authStub(t *testing.T, login http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var logins atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_UNAUTHORIZED","message":"no session"}`))
	})
	mux.HandleFunc("/api/user/login", func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		login(w, r)
	})
	return httptest.NewServer(mux), &logins
}

// The lockout is what stops a service that cannot log in from making it worse.
// The gateway's critical limiter allows 20 login attempts per 20 minutes per
// IP; a patrol that retries on every tick burns that budget in minutes and
// keeps the operator locked out for as long as it keeps trying.
func TestLockoutBlocksUntilTheServerSaysSo(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewLockout(func() time.Time { return now })

	if err := l.Blocked(); err != nil {
		t.Fatalf("a fresh lockout must not block: %v", err)
	}

	l.Fail(&RateLimitError{Method: "POST", Path: "/api/user/login", RetryAfter: 10 * time.Minute})
	err := l.Blocked()
	if err == nil {
		t.Fatal("after a rate limit the caller must be told to wait")
	}
	if !IsRateLimited(err) {
		t.Errorf("the blocking error must keep the rate-limit type: %v", err)
	}
	if d := RetryAfterOf(err); d != 10*time.Minute {
		t.Errorf("the remaining wait must shrink with the clock, got %v", d)
	}

	// Five minutes later it is still blocked, with five minutes left.
	now = now.Add(5 * time.Minute)
	if err := l.Blocked(); err == nil {
		t.Fatal("still inside the window")
	} else if d := RetryAfterOf(err); d != 5*time.Minute {
		t.Errorf("remaining wait = %v, want 5m", d)
	}

	// Past the window it must clear itself, with no manual reset.
	now = now.Add(5*time.Minute + time.Second)
	if err := l.Blocked(); err != nil {
		t.Errorf("the lockout must expire on its own: %v", err)
	}
}

// A successful login clears the lockout immediately: the operator may have
// revoked sessions on the gateway, and making them wait out a stale window
// would be wrong.
func TestLockoutClearsOnSuccess(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewLockout(func() time.Time { return now })
	l.Fail(&RateLimitError{Method: "POST", Path: "/api/user/login", RetryAfter: time.Hour})
	l.Success()
	if err := l.Blocked(); err != nil {
		t.Errorf("a success must clear the lockout: %v", err)
	}
}

// A rate limit with no hint still blocks: the server said "too many", and
// hammering it again immediately is the one response that cannot help.
func TestLockoutWithoutAHintStillBacksOff(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewLockout(func() time.Time { return now })
	l.Fail(&RateLimitError{Method: "POST", Path: "/api/user/login"})

	err := l.Blocked()
	if err == nil {
		t.Fatal("a rate limit without a hint must still back off")
	}
	if d := RetryAfterOf(err); d <= 0 {
		t.Errorf("a default backoff must be applied, got %v", d)
	}
	// It must be short enough not to park the service for the rest of the day.
	if d := RetryAfterOf(err); d > 15*time.Minute {
		t.Errorf("the default backoff is too long to be useful: %v", d)
	}
}

// A failure that is not a rate limit must not block anything: a wrong password
// or a permission error is not something waiting fixes.
func TestLockoutIgnoresOtherFailures(t *testing.T) {
	l := NewLockout(func() time.Time { return time.Unix(1_000_000, 0) })
	l.Fail(errors.New("用户名或密码错误"))
	if err := l.Blocked(); err != nil {
		t.Errorf("a non-limit failure must not lock the caller out: %v", err)
	}
	// The session cap DOES arm a cooldown, and deliberately so: measured, 12
	// calls with the cap hit produced 12 login attempts against a rate-limited
	// route. Waiting cannot fix the cap, but retrying per call makes it worse
	// and buries the cause; the operator still sees the real error (Blocked
	// returns a SessionLimitError naming the fix).
	l.Fail(&SessionLimitError{Method: "POST", Path: "/api/user/login"})
	if err := l.Blocked(); err == nil {
		t.Error("the session cap must stop the per-call login attempts")
	} else {
		var sl *SessionLimitError
		if !errors.As(err, &sl) {
			t.Errorf("the cooldown must keep naming the session cap, got %T: %v", err, err)
		}
	}
}

// Reauthenticate must NOT burn attempts on a rate limit: the whole point is
// that the service stops asking until the window passes.
func TestReauthenticateDoesNotRetryARateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.Reauthenticate("admin", "pw")
	if err == nil {
		t.Fatal("a 429 must be reported")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("a rate limit must be tried once, not %d times: each retry extends the lockout", got)
	}
	if !IsRateLimited(err) {
		t.Errorf("the rate-limit type must survive: %v", err)
	}
}

// A transient network failure is worth retrying: those are the blips the old
// blind retry was for. (A refresh failure that is not an auth failure is
// reported as-is — see the session-cap test — so this drives the login path
// through a client with no session.)
func TestReauthenticateRetriesTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	srv, logins := authStub(t, func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"tok"}}`))
	})
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Reauthenticate("admin", "pw"); err != nil {
		t.Fatalf("a transient failure must be retried: %v", err)
	}
	if c.Token() != "tok" {
		t.Errorf("the token must be installed, got %q", c.Token())
	}
	if got := logins.Load(); got != 3 {
		t.Errorf("logins = %d, want 3 (two 502s then the success)", got)
	}
}

// A rate-limited *login* must not be retried either. The earlier test only
// reaches the refresh, so this drives the login path specifically: without a
// session to refresh, the 429 comes from the login itself.
func TestReauthenticateDoesNotRetryARateLimitedLogin(t *testing.T) {
	srv, logins := authStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer srv.Close()

	err := New(srv.URL).Reauthenticate("admin", "pw")
	if !IsRateLimited(err) {
		t.Fatalf("the rate limit must survive to the caller: %v", err)
	}
	if got := logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1: each retry extends the lockout the server just announced", got)
	}
	if d := RetryAfterOf(err); d != 10*time.Minute {
		t.Errorf("the retry hint must reach the caller, got %v", d)
	}
}

// The session cap is not retryable either: waiting does not free a session, and
// a retry loop just delays the operator's real fix (revoking sessions).
func TestReauthenticateDoesNotRetryASessionLimit(t *testing.T) {
	srv, logins := authStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_SESSION_LIMIT","message":"Conflict"}`))
	})
	defer srv.Close()

	err := New(srv.URL).Reauthenticate("admin", "pw")
	if !IsSessionLimit(err) {
		t.Fatalf("the session cap must be recognisable: %v", err)
	}
	if got := logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1: waiting does not free a session, so retrying is pointless", got)
	}
}

// A wrong password must be reported at once: retrying it only burns the login
// budget and delays the message the operator needs.
func TestReauthenticateDoesNotRetryBadCredentials(t *testing.T) {
	srv, logins := authStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"用户名或密码错误"}`))
	})
	defer srv.Close()

	err := New(srv.URL).Reauthenticate("admin", "wrong")
	if err == nil {
		t.Fatal("a rejected login must be an error")
	}
	if got := logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1: a wrong password must not be tried repeatedly", got)
	}
}

// The gateway answers a bad credential in English (reference/new-api's
// controller/user.go: "Username or password is incorrect, or user has been
// banned"), so matching only the Chinese wording left the real message
// classified as retryable: a wrong password burned three attempts of the
// 20-per-20-minutes login budget on every sweep.
func TestReauthenticateDoesNotRetryBadCredentialsInTheGatewaysWording(t *testing.T) {
	srv, logins := authStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"Username or password is incorrect, or user has been banned"}`))
	})
	defer srv.Close()

	err := New(srv.URL).Reauthenticate("admin", "wrong")
	if err == nil {
		t.Fatal("a rejected login must be an error")
	}
	// The caller shows this error to the operator; swallowing the gateway's
	// wording would turn a clear cause into an anonymous failure.
	if !strings.Contains(err.Error(), "Username or password") {
		t.Errorf("the error must carry the gateway's wording, got: %v", err)
	}
	if got := logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1: the gateway's own wording must not be retried", got)
	}
}

// A disabled account is refused on the password path too, through
// setupLoginAtAuthVersion (reference/new-api controller/user.go:166), with the
// standalone wording "User has been banned" — capital U, no 'password'. It is a
// decision like a wrong credential, not a blip, so it must not be retried.
func TestReauthenticateDoesNotRetryABannedAccount(t *testing.T) {
	srv, logins := authStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"User has been banned"}`))
	})
	defer srv.Close()

	err := New(srv.URL).Reauthenticate("admin", "right")
	if err == nil {
		t.Fatal("a banned account must be an error")
	}
	if got := logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1: a banned account must not be retried", got)
	}
}

// Other decisions the gateway can answer a login with, all of which a retry
// cannot fix: login disabled instance-wide, a Turnstile challenge the service
// does not solve, and a 2FA challenge (success:true carrying no token). Each
// otherwise burns three attempts of the 20-per-20-minutes login budget on every
// reauthentication.
func TestReauthenticateDoesNotRetryLoginBlockedByGatewayPolicy(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"password login disabled", `{"success":false,"message":"Password login has been disabled by administrator"}`},
		{"turnstile required", `{"success":false,"message":"Turnstile token is empty"}`},
		{"turnstile rejected", `{"success":false,"message":"Turnstile verification failed, please refresh and try again!"}`},
		// A 2FA challenge answers success with no token, which surfaces as the
		// client's own wording.
		{"two-factor challenge", `{"success":true,"data":{"challenge":{"id":"c1"}}}`},
		// The banned wording is matched case-insensitively; pin that, since the
		// gateway's own two forms both happen to use a lowercase 'has' and a
		// case-sensitive match would therefore still pass the cases above.
		{"banned, title-cased", `{"success":false,"message":"User Has Been Banned"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, logins := authStub(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			defer srv.Close()

			if err := New(srv.URL).Reauthenticate("admin", "pw"); err == nil {
				t.Fatal("a blocked login must be an error")
			}
			if got := logins.Load(); got != 1 {
				t.Errorf("logins = %d, want 1: %s must not be retried", got, tc.name)
			}
		})
	}
}

// IsAuthFailure separates "the token is no longer good" from every other
// failure. Only the first should throw away a cached session: invalidating on
// a rate limit or a permission error forces a re-login that burns the very
// budget the service just ran out of.
func TestIsAuthFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"rate limit", &RateLimitError{Method: "POST", Path: "/api/user/login"}, false},
		{"session cap", &SessionLimitError{Method: "POST", Path: "/api/user/login"}, false},
		// The rendered form is what the lazyGateway actually sees, and it names
		// AUTH_SESSION_LIMIT in its text: treating that as an auth failure is
		// how one rejection becomes a re-login on every later call.
		{"session cap, as rendered", (&SessionLimitError{Method: "POST", Path: "/api/user/login"}), false},
		{"wrong password", errors.New("POST /api/user/login: 用户名或密码错误 (HTTP 200)"), false},
		{"permission", errors.New(`PUT /api/user/: No permission to access users of same or higher level (HTTP 200)`), false},
		// The gateway denies a permission with 403, not a 200 carrying a
		// message: authHelper answers AUTH_INSUFFICIENT_PRIVILEGE (and some
		// routes a bare 403) when the role is too low. Classifying that as an
		// auth failure drops the cached session on a condition a re-login
		// cannot fix, spending the CriticalRateLimit-protected refresh (and a
		// login when nothing is stored) on every such call.
		{"insufficient privilege, 403", errors.New("PUT /api/user/: AUTH_INSUFFICIENT_PRIVILEGE: Unauthorized, insufficient privileges (HTTP 403)"), false},
		{"bare 403 privilege refusal", errors.New("POST /api/user/manage: Unauthorized, insufficient privileges (HTTP 403)"), false},
		{"unauthorized", errors.New("GET /api/user/self: AUTH_UNAUTHORIZED (HTTP 401)"), true},
		{"expired token", errors.New("GET /api/user/self: AUTH_TOKEN_EXPIRED (HTTP 401)"), true},
		{"revoked session", errors.New("GET /api/user/self: AUTH_SESSION_REVOKED (HTTP 401)"), true},
		// A session is dead, but the account is disabled — re-logging in cannot
		// help, so this must not count as an auth failure either.
		{"user disabled, 401", errors.New("GET /api/user/self: AUTH_USER_DISABLED: User has been banned (HTTP 401)"), false},
		// A conflict is not an auth failure, however it is worded.
		{"session limit by code only", errors.New("POST /api/user/login: AUTH_SESSION_LIMIT (HTTP 409)"), false},
	}
	for _, tc := range cases {
		if got := IsAuthFailure(tc.err); got != tc.want {
			t.Errorf("%s: IsAuthFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The 401 case must be driven by the status, not by a substring that a
// legitimate message could contain.
func TestAuthFailureFromRealResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_TOKEN_EXPIRED","message":"Unauthorized"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.SetToken("stale")
	_, err := c.Self()
	if err == nil {
		t.Fatal("a 401 must be an error")
	}
	if !IsAuthFailure(err) {
		t.Errorf("a 401 must be classified as an auth failure: %v", err)
	}
}
