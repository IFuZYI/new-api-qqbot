package newapi

import (
	"net/http"
	"net/url"
	"testing"
)

// originPort is what makes http://h and http://h:80 the same origin while
// https://h means :443. The redirect guard compares effective ports, so a
// missing branch here would either refuse a legitimate same-origin redirect or
// follow a cross-origin one with the admin password in the body.
func TestEffectiveOriginPortIsSchemeAware(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"http://gw.example.test:3000/api/user/1", "3000"},
		{"https://gw.example.test:8443", "8443"},
		{"https://gw.example.test", "443"},
		{"http://gw.example.test", "80"},
		{"http://gw.example.test/api/user/1", "80"},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", tc.raw, err)
		}
		if got := originPort(u); got != tc.want {
			t.Errorf("originPort(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	guard := func(from, to string) error {
		t.Helper()
		orig, err := http.NewRequest(http.MethodPost, from, nil)
		if err != nil {
			t.Fatal(err)
		}
		next, err := http.NewRequest(http.MethodPost, to, nil)
		if err != nil {
			t.Fatal(err)
		}
		return sameOriginRedirect(next, []*http.Request{orig})
	}
	if err := guard("http://gw.example.test/api/user/login", "http://gw.example.test:80/api/user/login"); err != nil {
		t.Errorf("the default-port spelling of the same origin must be followed: %v", err)
	}
	if err := guard("https://gw.example.test/api/x", "https://gw.example.test:443/api/y"); err != nil {
		t.Errorf("the https default-port spelling must be followed: %v", err)
	}
	if err := guard("http://gw.example.test:3000/api/x", "http://gw.example.test:3001/api/y"); err == nil {
		t.Error("a different port is a different origin and must be refused")
	}
	if err := guard("http://gw.example.test/api/x", "https://gw.example.test/api/y"); err == nil {
		t.Error("a scheme change must be refused (a 307 replays the login body)")
	}
	if err := guard("http://gw.example.test/api/x", "http://user:secret@gw.example.test/api/y"); err == nil {
		t.Error("a redirect carrying userinfo must be refused")
	}
}
