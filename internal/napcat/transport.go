package napcat

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Reject redirects before the next request: stripping Authorization alone does
// not protect the password hash replayed in a 307/308 login request body.
func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if len(via) == 0 {
		return fmt.Errorf("redirect without original request")
	}
	origin := via[0].URL
	if req.URL.User != nil || !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Hostname(), origin.Hostname()) || originPort(req.URL) != originPort(origin) {
		return fmt.Errorf("cross-origin redirect refused")
	}
	return nil
}

func originPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}
