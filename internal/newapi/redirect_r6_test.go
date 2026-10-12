package newapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// A redirect to a different origin must be refused before the credentials are
// re-sent: the login body carries the admin password, and the jar carries the
// refresh cookie. A same-origin redirect stays allowed.
func TestR6CrossOriginRedirectRefused(t *testing.T) {
	var leak struct {
		path, auth, cookie string
		hit                bool
	}
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leak.hit = true
		leak.path = r.URL.Path
		leak.auth = r.Header.Get("Authorization")
		if ck, err := r.Cookie(refreshCookieName); err == nil {
			leak.cookie = ck.Value
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer evil.Close()

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer gw.Close()

	c := New(gw.URL)
	if err := c.ImportSession("3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"); err != nil {
		t.Fatal(err)
	}
	err := c.do(http.MethodPost, "/api/user/login", map[string]string{"username": "u", "password": "p"}, nil)
	if err == nil {
		t.Fatal("a cross-origin redirect must fail the call")
	}
	if leak.hit {
		t.Fatalf("credentials reached the redirect target: %+v", leak)
	}
}

// A same-origin redirect is legitimate (an http->https upgrade in front of the
// gateway is configured that way on purpose) and must still work.
func TestR6SameOriginRedirectAllowed(t *testing.T) {
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			seen = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":1,"username":"u"}}`))
			return
		}
		http.Redirect(w, r, "/moved", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	c := New(srv.URL)
	var u User
	if err := c.do(http.MethodGet, "/api/user/1", nil, &u); err != nil {
		t.Fatalf("same-origin redirect must be followed: %v", err)
	}
	if !seen || u.ID != 1 {
		t.Fatalf("redirect was not followed: seen=%v user=%+v", seen, u)
	}
}

// The imported cookie must keep the gateway's transport protection. The
// standard jar does not echo attributes back from Cookies(), so this asserts
// the enforcement instead: an HTTPS gateway's session is Secure, which the jar
// refuses to hand out for a plaintext URL, while a plaintext gateway's session
// still travels on its own scheme.
func TestR6ImportedCookieKeepsSecure(t *testing.T) {
	const token = "3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"

	secure := New("https://api.example.test")
	if err := secure.ImportSession(token); err != nil {
		t.Fatal(err)
	}
	httpsURL, _ := url.Parse("https://api.example.test" + authPath)
	if !hasRefreshCookie(secure, httpsURL) {
		t.Fatal("HTTPS session cookie must be sent to its own origin")
	}
	httpURL, _ := url.Parse("http://api.example.test" + authPath)
	if hasRefreshCookie(secure, httpURL) {
		t.Fatal("imported HTTPS session cookie lost Secure: a plaintext hop would carry it")
	}

	plain := New("http://api.example.test")
	if err := plain.ImportSession(token); err != nil {
		t.Fatal(err)
	}
	plainURL, _ := url.Parse("http://api.example.test" + authPath)
	if !hasRefreshCookie(plain, plainURL) {
		t.Fatal("plaintext gateway's session cookie must still be sent (Secure would strand it)")
	}
}

func hasRefreshCookie(c *Client, u *url.URL) bool {
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == refreshCookieName && ck.Value != "" {
			return true
		}
	}
	return false
}
