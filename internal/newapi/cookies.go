package newapi

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
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

// refreshCookie returns the session cookie the gateway set, for diagnostics and
// tests. It reports false when the client has no session cookie for its host.
func (c *Client) refreshCookie() (*http.Cookie, bool) {
	u, err := url.Parse(c.baseURL)
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

// refreshCookieName matches the gateway's own cookie name (service.RefreshCookieName
// upstream). It is only read, never set, so a change there would simply mean
// "no cookie", which Refresh already reports as a failure.
const refreshCookieName = "new_api_refresh"
