package newapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

// newCookieJar returns the jar a client uses to hold the gateway's refresh
// cookie.
//
// The gateway scopes that cookie to /api/user/auth (see its WriteRefreshCookie),
// and Go's cookiejar honours that path, so a jar is all that is needed for the
// refresh call to carry it. The jar must be per client: two clients sharing one
// would exchange session cookies.
func newCookieJar() http.CookieJar {
	jar, err := cookiejar.New(nil)
	if err != nil {
		// cookiejar.New only fails on a nil options value, which cannot happen
		// here; a nil jar would silently drop the cookie, so it is worth
		// surfacing rather than ignoring.
		panic("newapi: cannot build a cookie jar: " + err.Error())
	}
	return jar
}

// authPath is where the gateway scopes its session cookie (its own
// WriteRefreshCookie uses "/api/user/auth"). The jar only hands the cookie back
// for URLs under this path, so both reading and installing it must use it.
const authPath = "/api/user/auth"

// refreshCookie returns the session cookie the gateway set, for diagnostics and
// tests. It reports false when the client has no session cookie for its host.
//
// It asks for the auth path, not the bare host: the gateway scopes the cookie to
// /api/user/auth, and a jar queried at "/" does not match it — which would make a
// perfectly good session look absent.
func (c *Client) refreshCookie() (*http.Cookie, bool) {
	u, err := url.Parse(c.baseURL + authPath)
	if err != nil {
		return nil, false
	}
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == refreshCookieName {
			return ck, true
		}
	}
	return nil, false
}

// ExportSession returns the refresh token for storage, so a restart can reuse
// the session it already has instead of logging in again.
//
// This matters for the account's session cap, not for speed: the token lives 15
// minutes but the session lives 30 days, so a service that logged in on every
// restart would spend one of the account's 50 sessions each time and eventually
// lock everyone out — the failure that made the operator's panel unusable.
//
// The value is a credential: callers must store it beside the password, never
// log it, and never send it anywhere but the gateway.
func (c *Client) ExportSession() string {
	ck, ok := c.refreshCookie()
	if !ok {
		return ""
	}
	return ck.Value
}

// ImportSession installs a previously exported session, so the next
// reauthentication can refresh it rather than log in.
//
// A malformed value is refused: installing nonsense would leave the client
// sending an invalid cookie and reporting a confusing failure instead of the
// real one. A well-formed but expired or revoked session imports fine and fails
// at the next refresh, which is the honest place to find out.
func (c *Client) ImportSession(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("no session to import")
	}
	// The gateway's format is <uuid>.<secret> (see its splitRefreshToken): one
	// dot, neither part empty, and the id must look like a UUID.
	sid, secret, ok := strings.Cut(raw, ".")
	if !ok || sid == "" || secret == "" || strings.Contains(secret, ".") {
		return errors.New("saved session is not a valid refresh token")
	}
	if !looksLikeUUID(sid) {
		return errors.New("saved session id is not a UUID")
	}
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("client URL: %w", err)
	}
	// The gateway scopes its refresh cookie to /api/user/auth; mirroring that
	// keeps the cookie from being attached to any other request. Secure is
	// inferred from the gateway scheme: the upstream sets Secure on an HTTPS
	// deployment, and re-importing it without Secure would let a downgrade
	// redirect carry the session in cleartext.
	c.http.Jar.SetCookies(u, []*http.Cookie{{
		Name:     refreshCookieName,
		Value:    raw,
		Path:     authPath,
		HttpOnly: true,
		Secure:   u.Scheme == "https",
	}})
	return nil
}

// looksLikeUUID checks the 8-4-4-4-12 hex shape without pulling in a UUID
// dependency: the value only has to be recognisable, since the gateway is the
// authority on whether the session is real.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// refreshCookieName matches the gateway's own cookie name (service.RefreshCookieName
// upstream). It is only read, never set, so a change there would simply mean
// "no cookie", which Refresh already reports as a failure.
const refreshCookieName = "new_api_refresh"
