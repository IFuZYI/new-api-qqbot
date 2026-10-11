// Login backoff: how the client behaves when the gateway refuses it.
//
// The login endpoint sits behind new-api's critical rate limiter
// (CRITICAL_RATE_LIMIT: 20 attempts per 20 minutes per IP by default). A client
// that retries blindly does not merely fail — it spends the budget faster and
// keeps the operator locked out, because every rejected attempt counts.
//
// So the two retryable failures are separated from the two that are not:
//
//	429 / AUTH_SESSION_ISSUANCE_LIMIT  -> wait out the window (Retry-After)
//	AUTH_SESSION_LIMIT                 -> revoke sessions; waiting does not help
//	bad credentials                    -> report immediately; retrying burns budget
//	transient network / 5xx            -> retry a couple of times
package newapi

import (
	"errors"
	"strings"
	"time"
)

// defaultRateLimitBackoff is used when the gateway rate-limits a request without
// a Retry-After hint. It is deliberately short: long enough to stop a hot loop,
// short enough that the service recovers on its own when the window passes.
const defaultRateLimitBackoff = 2 * time.Minute

// loginAttempts bounds retries of a *transient* failure (a 5xx, a dropped
// connection). It must not be applied to rate limits or bad credentials — see
// Client.Reauthenticate.
const loginAttempts = 3

// Lockout remembers that the gateway refused us and until when.
//
// It is the piece that stops the failure from feeding itself: without it, every
// patrol tick and every UI action tries to log in again, each rejection counts
// against the gateway's limiter, and the window never closes.
type Lockout struct {
	// now is injectable so the expiry is testable without sleeping.
	now func() time.Time

	until time.Time
	last  error
}

// NewLockout builds a Lockout. A nil clock uses the real one.
func NewLockout(now func() time.Time) *Lockout {
	if now == nil {
		now = time.Now
	}
	return &Lockout{now: now}
}

// Blocked reports whether the caller must wait. The returned error carries the
// remaining wait, so a caller can surface it verbatim ("需等待约 5 分钟后重试")
// instead of a generic message.
func (l *Lockout) Blocked() error {
	if l.until.IsZero() || !l.now().Before(l.until) {
		return nil
	}
	remaining := l.until.Sub(l.now())
	if rl, ok := l.last.(*RateLimitError); ok {
		return &RateLimitError{Method: rl.Method, Path: rl.Path, RetryAfter: remaining}
	}
	return &RateLimitError{RetryAfter: remaining}
}

// Fail records a failed attempt. Only a rate limit arms the lockout: a wrong
// password or a session cap is not fixed by waiting, and blocking on them would
// hide the real error from the operator.
func (l *Lockout) Fail(err error) {
	if err == nil {
		return
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		return
	}
	wait := rl.RetryAfter
	if wait <= 0 {
		wait = defaultRateLimitBackoff
	}
	l.until = l.now().Add(wait)
	l.last = err
}

// Success clears the lockout. A working login proves the window has passed —
// the operator may have revoked sessions in the meantime — so waiting out a
// stale window would be wrong.
func (l *Lockout) Success() {
	l.until = time.Time{}
	l.last = nil
}

// retryableLoginError reports whether trying again could plausibly succeed.
func retryableLoginError(err error) bool {
	if IsRateLimited(err) || IsSessionLimit(err) {
		return false
	}
	// A rejected credential is a decision, not a blip. Match on the gateway's
	// own wording rather than a bare status, because the gateway answers 200
	// with success:false for this one. The gateway ships both wordings
	// (reference/new-api controller/user.go uses the English one; a localized
	// deployment answers in Chinese), so both must be recognized — matching
	// only 密码/credentials left the real English message retryable and burned
	// three attempts of the login budget per sweep. "has been banned" is
	// matched case-insensitively because it appears both inside the
	// bad-credentials sentence and standalone ("User has been banned", from
	// setupLoginAtAuthVersion when the account itself is disabled).
	//
	// The other decisions the gateway can answer a login with are equally
	// uncurable by retrying: login disabled instance-wide, a Turnstile
	// challenge this service does not solve, and a 2FA challenge (success:true
	// carrying no token). Each would otherwise burn three attempts of the
	// 20-per-20-minutes budget on every reauthentication.
	msg := err.Error()
	lower := strings.ToLower(msg)
	for _, decision := range []string{
		"密码", "credentials", "Username or password",
		"has been banned", // case-folded, see above
		"Password login has been disabled",
		"Turnstile",
		"the gateway returned no access token", // 2FA challenge
	} {
		if strings.Contains(msg, decision) || strings.Contains(lower, decision) {
			return false
		}
	}
	// A response the client could not understand (an HTML error page from a
	// proxy, a truncated body) may well work on the next attempt.
	return true
}

// IsAuthFailure reports whether err means the session is no longer usable, which
// is the only case where throwing away a cached client is right.
//
// It keys on the gateway's machine-readable codes for a dead session, never on
// a bare HTTP status and never on a loose substring: the gateway answers a
// permission denial (AUTH_INSUFFICIENT_PRIVILEGE) and a disabled account
// (AUTH_USER_DISABLED) with 403 and 401 respectively, and neither is fixed by
// logging in again. Invalidating on those forces a re-login that spends the
// CriticalRateLimit-protected refresh — and, with nothing stored, a login — on
// a condition the operator's role or account has to change.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	if IsRateLimited(err) || IsSessionLimit(err) {
		return false
	}
	msg := err.Error()
	// A dead session: the token is gone, expired, revoked or invalid. These are
	// the only codes a re-login can cure.
	for _, code := range []string{"AUTH_UNAUTHORIZED", "AUTH_TOKEN_EXPIRED", "AUTH_SESSION_REVOKED", "AUTH_TOKEN_INVALID"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	// A code we know is not curable must win over the status fallback: the
	// gateway renders some of these alongside 401/403. AUTH_ORIGIN_FORBIDDEN is
	// the refresh guard refusing the request itself, so the credential may be
	// perfectly good. The guard answers 403, which the status fallback below
	// already excludes; the entry is defence for a 401 rendering, where the
	// bare-401 rule would otherwise call the session dead and force a login.
	for _, code := range []string{"AUTH_INSUFFICIENT_PRIVILEGE", "AUTH_USER_DISABLED", "AUTH_ORIGIN_FORBIDDEN"} {
		if strings.Contains(msg, code) {
			return false
		}
	}
	// A 401 with no envelope code is still a dead session; a 403 with no code is
	// the privilege refusal above (the gateway's own "Unauthorized, insufficient
	// privileges" carries no code on some routes), so it is not.
	return strings.Contains(msg, "(HTTP 401)")
}
