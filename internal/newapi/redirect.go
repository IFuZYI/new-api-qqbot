package newapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// sameOriginRedirect refuses a redirect that would move the request to another
// origin, before the next hop is sent.
//
// Same-origin path redirects are followed. A scheme change (including an
// http->https upgrade) is a different origin and is refused. What must never
// happen is the credential travelling with it: the login body carries the
// admin password and the jar carries the refresh cookie, and Go's client
// replays both on a 307/308. Following a redirect to a different origin hands
// them to whatever the configured address points at.
func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return fmt.Errorf("redirect without an original request")
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	origin := via[0].URL
	if req.URL.User != nil {
		return fmt.Errorf("redirect to a URL with userinfo refused")
	}
	if !strings.EqualFold(req.URL.Scheme, origin.Scheme) ||
		!strings.EqualFold(req.URL.Hostname(), origin.Hostname()) ||
		originPort(req.URL) != originPort(origin) {
		return fmt.Errorf("cross-origin redirect refused (%s -> %s)", origin.Redacted(), req.URL.Redacted())
	}
	return nil
}

// originPort returns the effective port, so http://h:80 and http://h compare
// equal while https://h and http://h do not.
func originPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}
