// Package newapi is a thin client for a new-api gateway's admin API. It only
// ever calls HTTP endpoints: the service never touches new-api's database.
package newapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrAmbiguousEmail means more than one account matched an email exactly, so
// binding must not happen automatically.
var ErrAmbiguousEmail = errors.New("more than one new-api account matches this email")

// RateLimitError is the gateway refusing a request because the client hit a rate
// limiter. The login endpoint sits behind new-api's critical limiter
// (CRITICAL_RATE_LIMIT, 20 attempts per 20 minutes per IP by default), which
// answers with a bare 429 and — when it can — a Retry-After hint.
//
// It is a distinct type rather than a plain error because the caller can act on
// it: the wait is known, so the client backs off for exactly that long instead
// of burning more attempts and extending its own lockout.
type RateLimitError struct {
	// Method and Path identify what was refused.
	Method, Path string
	// RetryAfter is the server's hint; zero when it gave none, which means the
	// caller must choose its own backoff rather than assume "immediately".
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	what := "请求过于频繁"
	if e.Path == "/api/user/login" {
		// The one a lost session actually surfaces as, so it is worth naming:
		// an operator seeing this in the WebUI needs to know the fix is waiting,
		// not that their credentials are wrong.
		what = "登录被限流"
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s %s: %s（网关限流，需等待约 %s 后重试）",
			e.Method, e.Path, what, e.RetryAfter.Round(time.Second))
	}
	return fmt.Sprintf("%s %s: %s（网关限流，请稍后重试）", e.Method, e.Path, what)
}

// IsRateLimited reports whether err is the gateway refusing on rate limits.
func IsRateLimited(err error) bool {
	var rl *RateLimitError
	return errors.As(err, &rl)
}

// RetryAfterOf returns the server's suggested wait, or 0 when it gave none or
// the error is not a rate limit.
func RetryAfterOf(err error) time.Duration {
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return rl.RetryAfter
	}
	return 0
}

// SessionLimitError is the gateway refusing a login because the account already
// has its maximum number of active sessions. Retrying cannot fix it — the
// operator must revoke sessions (or wait for them to expire) — so it is kept
// distinct from a rate limit, whose fix is simply to wait.
type SessionLimitError struct {
	Method, Path string
}

func (e *SessionLimitError) Error() string {
	return fmt.Sprintf("%s %s: 会话数已达上限（AUTH_SESSION_LIMIT）：请撤销其他登录会话或等待其过期",
		e.Method, e.Path)
}

// IsSessionLimit reports whether err is the gateway's active-session cap.
func IsSessionLimit(err error) bool {
	var sl *SessionLimitError
	return errors.As(err, &sl)
}

// userAgent identifies this service. The gateway is fronted by Cloudflare,
// which rejects requests with Go's default User-Agent.
const userAgent = "qqbot-group-monitor/0.1 (+self-hosted)"

// User is the subset of a new-api account this service cares about.
type User struct {
	ID           int    `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Group        string `json:"group"`
	Role         int    `json:"role"`
	Status       int    `json:"status"`
	Remark       string `json:"remark"`
	Quota        int    `json:"quota"`
	UsedQuota    int    `json:"used_quota"`
	RequestCount int    `json:"request_count"`
	// Upstream gorm.DeletedAt marshals as null or an RFC3339 timestamp.
	DeletedAt *time.Time `json:"DeletedAt,omitempty"`
}

// Client talks to one new-api instance.
type Client struct {
	baseURL string
	http    *http.Client

	// now is injectable so token expiry is testable without sleeping.
	now func() time.Time

	mu        sync.RWMutex
	token     string
	expiresAt time.Time // zero means "not announced"
	// fingerprint identifies this service to the gateway as one device across
	// restarts, so its session entry stays recognisable. See fingerprint.go.
	fingerprint string
}

// New builds a client for baseURL (e.g. https://api.fuzy.site).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 30 * time.Second,
			// A cookie jar is required, not a nicety: the gateway hands out its
			// refresh token as an HttpOnly cookie on login, and the refresh call
			// carries it. Without a jar every refresh goes out unauthenticated
			// and the client silently falls back to a full login — a new session
			// every 15 minutes, which is what walks into the session cap.
			Jar: newCookieJar(),
			// Never follow a redirect to another origin: the login body and the
			// bearer/refresh credentials would be handed to whoever the
			// configured address redirects to (a wrong proxy, a hijacked
			// endpoint). A same-origin redirect is still allowed.
			CheckRedirect: sameOriginRedirect,
		},
		now: time.Now,
	}
}

// SetClock replaces the time source, for tests.
func (c *Client) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	c.now = now
}

// SetToken installs a previously obtained access token.
func (c *Client) SetToken(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = tok
}

// SetTokenExpiry records when the current token dies.
func (c *Client) SetTokenExpiry(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expiresAt = at
}

// Token returns the current access token.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// TokenExpiresAt returns the token's expiry, or nil when it was never announced.
func (c *Client) TokenExpiresAt() *time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.expiresAt.IsZero() {
		return nil
	}
	at := c.expiresAt
	return &at
}

// refreshMargin is how long before expiry a token stops being reused for new
// work. A sweep that starts just before expiry would otherwise send a batch of
// calls with a token that dies mid-flight, turning one expiry into a burst of
// failures.
const refreshMargin = 30 * time.Second

// TokenValid reports whether the token can still be sent. An unannounced expiry
// counts as valid: discarding a working token on a guess is worse than letting
// the server reject it.
func (c *Client) TokenValid() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.validLocked(c.now())
}

// TokenFresh reports whether the token is far enough from expiry to start new
// work with it. This is the check a caller makes before a sweep, as opposed to
// TokenValid, which is about a request already going out.
func (c *Client) TokenFresh() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := c.now()
	if c.token == "" || c.expiresAt.IsZero() {
		return c.token != ""
	}
	return now.Add(refreshMargin).Before(c.expiresAt)
}

func (c *Client) validLocked(now time.Time) bool {
	if c.token == "" {
		return false
	}
	if c.expiresAt.IsZero() {
		return true
	}
	return now.Before(c.expiresAt)
}

// envelope is the standard new-api response wrapper. Code carries the machine
// readable error (e.g. AUTH_SESSION_LIMIT) which is far more useful than the
// bare HTTP status.
type envelope struct {
	Success *bool           `json:"success"`
	Message string          `json:"message"`
	Code    string          `json:"code"`
	Data    json.RawMessage `json:"data"`
}

// maxResponseBody bounds how much of a gateway response is read into memory.
//
// A new-api answer is a few tens of kilobytes, so 8 MiB is far above any real
// payload while still bounding a runaway one: the gateway is fronted by
// Cloudflare and a proxy's error page, a login-wall HTML document or a hijacked
// response would otherwise be buffered whole by io.ReadAll, growing the process
// until it dies.
const maxResponseBody = 8 << 20

// ErrResponseTooLarge means the gateway sent more than maxResponseBody. The read
// is abandoned at the limit rather than truncated, so the caller gets a named
// cause instead of a decode failure on half a JSON document.
var ErrResponseTooLarge = errors.New("response body is too large")

// readBody reads a response body up to maxResponseBody and refuses anything
// larger.
//
// The limit is enforced by reading one byte past it: io.LimitReader reports EOF
// at the cap either way, so a body cut off mid-JSON would surface as an
// unexplained decode error ("unexpected response (HTTP 200): {\"success\":tru…"),
// which reads like a gateway bug. The extra byte makes "the body ended" and "the
// cap was hit" distinguishable.
func readBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBody {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrResponseTooLarge, maxResponseBody)
	}
	return body, nil
}

func (c *Client) do(method, path string, body any, out any) error {
	return c.doWithOrigin(method, path, body, out, "")
}

// doWithOrigin is do plus an explicit Origin header, for the one route that
// needs it (the refresh; see Client.Refresh).
func (c *Client) doWithOrigin(method, path string, body any, out any, origin string) (result error) {
	// Only AddQuota is non-idempotent here. Do not infer certainty from an
	// unreadable acknowledgement after dispatch, or retry inside this client.
	uncertain := false
	defer func() {
		if result != nil && uncertain {
			result = &AmbiguousWriteError{Err: result}
		}
	}()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgentWith())
	if origin != "" {
		req.Header.Set("Origin", strings.TrimRight(origin, "/"))
	}
	if tok := c.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	uncertain = method == http.MethodPost && path == "/api/user/manage"
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := readBody(resp.Body)
	if err != nil {
		// Name the call: "response body is too large" alone leaves the operator
		// guessing which of the sweep's many requests hit it.
		return fmt.Errorf("%s %s: %w", method, path, err)
	}

	// A 5xx can follow a committed write, including a misleading success body.
	if resp.StatusCode >= 500 {
		return &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Message: "gateway server failure"}
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		uncertain = false
	}

	// The rate limiter answers with a bare status and an empty body, so it must
	// be recognised before the JSON decode: reporting it as "unexpected
	// response" throws away the one thing the server told us (Retry-After) and
	// tells the operator nothing about what to do.
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitError{Method: method, Path: path, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// A non-JSON body is a proxy or an HTML error page, not the gateway's
		// own envelope. Name the status; do not paste the page into a message
		// the operator will read in a toast.
		return &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Message: fmt.Sprintf("网关返回了非 JSON 响应（%d 字节）", len(raw))}
	}
	if env.Success != nil && !*env.Success {
		uncertain = false
		switch env.Code {
		case "AUTH_SESSION_LIMIT":
			return &SessionLimitError{Method: method, Path: path}
		case "AUTH_SESSION_ISSUANCE_LIMIT":
			return &RateLimitError{Method: method, Path: path}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Code: env.Code, Message: env.Message}
	}
	if env.Success == nil {
		return fmt.Errorf("%s %s: missing required success", method, path)
	}
	if !*env.Success {
		uncertain = false
		msg := env.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Code: env.Code, Message: msg}
	}
	if out != nil {
		if len(env.Data) == 0 || string(env.Data) == "null" {
			return fmt.Errorf("%s %s: missing required data", method, path)
		}
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s %s: decode data: %w", method, path, err)
		}
	}
	return nil
}

// parseRetryAfter reads a Retry-After header in seconds. The header may also be
// an HTTP date, which this gateway does not send; an unparsable value means "no
// hint" rather than a guess.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Login authenticates the admin account and stores the returned access token.
func (c *Client) Login(username, password string) error {
	var data loginData
	err := c.do(http.MethodPost, "/api/user/login",
		map[string]string{"username": username, "password": password}, &data)
	if err != nil {
		return err
	}
	return c.installToken(data)
}

// loginData is what both /api/user/login and /api/user/auth/refresh return.
type loginData struct {
	AccessToken string `json:"access_token"`
	// The gateway announces when the token dies. It must be recorded: the token
	// lives 15 minutes while the session behind it lives 30 days, so
	// re-logging-in on expiry creates a new session every time — which is how a
	// long-running service walks into the 50-session cap.
	AccessExpiresAt int64 `json:"access_expires_at"`
}

func (c *Client) installToken(data loginData) error {
	if data.AccessToken == "" {
		return errors.New("the gateway returned no access token")
	}
	c.mu.Lock()
	c.token = data.AccessToken
	if data.AccessExpiresAt > 0 {
		c.expiresAt = time.Unix(data.AccessExpiresAt, 0)
	}
	c.mu.Unlock()
	return nil
}

// Refresh exchanges the session cookie for a fresh access token.
//
// This is the cheap way to stay authenticated: it reuses the login session
// instead of creating one. Logging in again would work too, but every login
// adds a session to the account's 50-session cap while the old ones live on for
// 30 days, so a service that re-logged in every 15 minutes would eventually lock
// itself (and the operator) out of the gateway.
//
// It fails with an auth failure when the session is gone, which tells the caller
// a real login is required.
//
// The Origin header is not decoration: new-api installs SessionCookieOriginGuard
// on this route, and once SESSION_COOKIE_SECURE is on — which its own startup
// warning tells operators to set in production — the guard refuses any request
// without a same-origin Origin or Referer. Omitting it therefore breaks refresh
// entirely against such a gateway: measured, every refresh answers 403
// AUTH_ORIGIN_FORBIDDEN and every call fails. (It does not fall back to logging
// in — a 403 is not a dead session — so the service is simply unusable there,
// which is why the header is sent rather than left to the caller's error
// handling.) The Origin is the gateway's own origin: the guard accepts a
// request whose Origin equals the host it arrived at, which for a
// non-browser client is the gateway itself.
func (c *Client) Refresh() error {
	var data loginData
	if err := c.doWithOrigin(http.MethodPost, "/api/user/auth/refresh", nil, &data, c.baseURL); err != nil {
		return err
	}
	return c.installToken(data)
}

// Logout revokes the session this client holds, so it stops occupying one of the
// account's 50 active sessions.
//
// It exists for the connection probe: proving credentials work requires logging
// in, and a login mints a session, so a probe that did not revoke would leave a
// permanent entry behind on every click with changed credentials — sessions live
// 30 days, so repeated testing would eventually lock the account out. The route
// sits behind the same SessionCookieOriginGuard as refresh, hence the Origin.
func (c *Client) Logout() error {
	return c.doWithOrigin(http.MethodPost, "/api/user/auth/logout", nil, nil, c.baseURL)
}

// Reauthenticate gets a usable token, preferring the cheap path: refresh the
// existing session, and log in only when there is nothing to refresh.
//
// This is the policy the service needs rather than a bare Login, because the
// difference between the two is a session spent per 15 minutes: the access token
// lives 15 minutes, a login session 30 days, and the account's session cap is
// what eventually locks everyone out.
//
// Retrying is decided per failure, because only some of them are worth another
// attempt:
//
//	429 / AUTH_SESSION_ISSUANCE_LIMIT  -> not retried: the server said when to return
//	AUTH_SESSION_LIMIT                 -> not retried: waiting does not free a session
//	bad credentials                    -> not retried: it is a decision, not a blip
//	5xx / unreadable response          -> retried: those are the blips a retry fixes
func (c *Client) Reauthenticate(username, password string) error {
	// With no session there is nothing to refresh, and the refresh endpoint is
	// behind the same CriticalRateLimit as login: the guaranteed failure would
	// spend one slot of the budget this service is trying to conserve. Go
	// straight to the login it is going to need anyway.
	if _, ok := c.refreshCookie(); !ok {
		return c.loginWithRetry(username, password)
	}
	err := c.Refresh()
	if err == nil {
		return nil
	}
	// Only a genuinely unusable session justifies a new one. A rate limit or a
	// transport failure is reported as-is: logging in anyway would spend the
	// very budget that just ran out, and the caller (with its lockout) decides
	// when to try again.
	if !IsAuthFailure(err) {
		return err
	}
	return c.loginWithRetry(username, password)
}

// loginWithRetry logs in, retrying only the failures another attempt could fix.
func (c *Client) loginWithRetry(username, password string) error {
	var err error
	for attempt := 0; attempt < loginAttempts; attempt++ {
		if err = c.Login(username, password); err == nil {
			return nil
		}
		if !retryableLoginError(err) {
			return err
		}
		// Only wait when another attempt will actually follow.
		if attempt < loginAttempts-1 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}
	return err
}

// searchPageSize is the page size used when scanning the user search. It is the
// server's maximum, so a realistic result set is covered in one round trip.
const searchPageSize = 100

// FindUserByEmail returns the account whose email equals addr, ignoring case.
// It returns (nil, nil) when nothing matches and ErrAmbiguousEmail when more
// than one does.
//
// /api/user/search is a LIKE across several columns and is paginated, so the
// whole result set is scanned: reading only the first page would report a real
// match as absent (and leave the member unbound forever) as soon as the exact
// row sorts beyond the page size.
func (c *Client) FindUserByEmail(addr string) (*User, error) {
	want := strings.ToLower(strings.TrimSpace(addr))
	if want == "" {
		return nil, nil
	}

	users, err := c.readUsers("/api/user/search?keyword=" + queryEscape(addr) + "&")
	if err != nil {
		return nil, err
	}
	var matches []User
	for _, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == want {
			matches = append(matches, u)
		}
	}

	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrAmbiguousEmail, addr)
	}
}

// ListUsers pages through every account on the instance.
//
// It reads /api/user/ (the admin list), not /search: the search endpoint is a
// keyword LIKE, so an empty keyword does not reliably mean "everything". The
// list is paginated, so the whole set is scanned and the caller is expected to
// treat the result as complete - a partial read must never be cached as if it
// were the full roster (see store.ReplaceNewAPIUsers).
func (c *Client) ListUsers() ([]User, error) {
	return c.readUsers("/api/user/?")
}

// SetGroup moves an account into group. new-api's PUT /api/user/ takes the full
// user object and silently discards fields it does not accept, so everything it
// does persist is resent verbatim.
func (c *Client) SetGroup(u *User, group string) error {
	body := map[string]any{
		"id":           u.ID,
		"username":     u.Username,
		"display_name": u.DisplayName,
		"role":         u.Role,
		"group":        group,
		"remark":       u.Remark,
	}
	return c.do(http.MethodPut, "/api/user/", body, nil)
}

// AddQuota grants quota to an account. new-api records the change itself, so
// this service keeps no ledger of its own.
func (c *Client) AddQuota(userID, value int) error {
	if userID <= 0 {
		return errors.New("user id must be positive")
	}
	if value <= 0 {
		return fmt.Errorf("quota value must be positive, got %d", value)
	}
	return c.do(http.MethodPost, "/api/user/manage", map[string]any{
		"id":     userID,
		"action": "add_quota",
		"mode":   "add",
		"value":  value,
	}, nil)
}

// GetUser loads one account by id.
func (c *Client) GetUser(id int) (*User, error) {
	if id <= 0 {
		return nil, errors.New("user id must be positive")
	}
	var u User
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/user/%d", id), nil, &u); err != nil {
		return nil, err
	}
	if u.ID <= 0 || u.ID != id {
		return nil, errors.New("gateway returned a mismatched user id")
	}
	return &u, nil
}

// Self returns the account the current token belongs to. It is what a
// connection test reports as the identity behind the credentials, so the
// operator can see *which* account they configured rather than just "ok".
func (c *Client) Self() (*User, error) {
	var u User
	if err := c.do(http.MethodGet, "/api/user/self", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Groups returns the group names configured on the instance, which is how the
// service validates that its source/target groups really exist.
//
// The ratio is decoded as json.RawMessage, not a number: new-api's built-in
// "auto" entry carries the string "auto" as its ratio, and a float64 field would
// fail the whole response, leaving the group pickers empty.
func (c *Client) Groups() (map[string]struct{}, error) {
	var data map[string]struct {
		Desc  string          `json:"desc"`
		Ratio json.RawMessage `json:"ratio"`
	}
	if err := c.do(http.MethodGet, "/api/user/groups", nil, &data); err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(data))
	for name := range data {
		out[name] = struct{}{}
	}
	return out, nil
}

func queryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~', ch == '@':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
