package napcat

import (
	"net/http"
	"net/url"
	"testing"
)

// originPort is what makes http://h and http://h:80 the same origin, and
// https://h mean :443 — the redirect guard compares effective ports rather
// than strings. All three branches are pinned: an explicit port and both
// scheme defaults.
func TestOriginPortEffectivePort(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"http://napcat.internal:6099/webui/", "6099"},
		{"https://napcat.example.com:8443", "8443"},
		{"https://napcat.example.com", "443"},
		{"http://127.0.0.1", "80"},
		{"http://127.0.0.1/", "80"},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", tc.raw, err)
		}
		if got := originPort(u); got != tc.want {
			t.Errorf("originPort(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	// The comparison is what matters: the default-port spelling of the same
	// origin must be followed, a different port must not be.
	redirect := func(from, to string) error {
		t.Helper()
		orig, err := http.NewRequest(http.MethodGet, from, nil)
		if err != nil {
			t.Fatal(err)
		}
		next, err := http.NewRequest(http.MethodGet, to, nil)
		if err != nil {
			t.Fatal(err)
		}
		return sameOriginRedirect(next, []*http.Request{orig})
	}
	if err := redirect("http://napcat.internal/api/x", "http://napcat.internal:80/api/y"); err != nil {
		t.Errorf("the default-port spelling of the same origin was refused: %v", err)
	}
	if err := redirect("https://napcat.internal/api/x", "https://napcat.internal:443/api/y"); err != nil {
		t.Errorf("the https default-port spelling was refused: %v", err)
	}
	if err := redirect("http://napcat.internal:6099/api/x", "http://napcat.internal:7000/api/y"); err == nil {
		t.Error("a different port is a different origin and must be refused")
	}
	if err := redirect("http://napcat.internal/api/x", "https://napcat.internal/api/y"); err == nil {
		t.Error("a scheme change must be refused (a 307 replays the login body)")
	}
}

// The host the OneBot URLs are built from: "localhost" must become 127.0.0.1
// because the adapters bind IPv4 loopback while localhost resolves to ::1
// first, and every other host passes through untouched.
func TestWebUIHostMapsLoopbackAndPassesOthersThrough(t *testing.T) {
	if got := WebUIHost("http://localhost:6099/webui/"); got != "127.0.0.1" {
		t.Errorf("localhost must map to IPv4 loopback, got %q", got)
	}
	if got := WebUIHost("http://LOCALHOST:6099"); got != "127.0.0.1" {
		t.Errorf("the loopback mapping is case-insensitive, got %q", got)
	}
	for _, tc := range []struct{ in, want string }{
		{"http://napcat.internal:6099/webui/", "napcat.internal"},
		{"https://napcat.example.com", "napcat.example.com"},
		{"http://127.0.0.1:6099", "127.0.0.1"},
	} {
		if got := WebUIHost(tc.in); got != tc.want {
			t.Errorf("WebUIHost(%q) = %q, want %q (pass-through)", tc.in, got, tc.want)
		}
	}
}
