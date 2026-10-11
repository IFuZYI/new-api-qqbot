package newapi

import (
	"strings"
	"testing"
)

// A fingerprint is what makes the session look like the same device across
// restarts. It must be stable for a given service, distinct across services and
// versions, and safe to put in a header (no control characters or quotes).
func TestFingerprintIsStableAndHeaderSafe(t *testing.T) {
	a := NewFingerprint("test-secret-1")
	b := NewFingerprint("test-secret-1")
	if a != b {
		t.Fatalf("the same secret must give the same fingerprint: %q vs %q", a, b)
	}
	if c := NewFingerprint("test-secret-2"); c == a {
		t.Fatalf("a different secret must give a different fingerprint: %q", c)
	}
	if strings.TrimSpace(a) == "" {
		t.Fatal("a fingerprint must not be empty")
	}
	for _, r := range a {
		if r < 0x21 || r > 0x7e {
			t.Fatalf("fingerprint %q has a character that is not header-safe: %q", a, r)
		}
	}
	if len(a) > 64 {
		t.Fatalf("fingerprint is too long for a header: %d chars", len(a))
	}
}

// The User-Agent is what the gateway records as the session's device. It must
// name this service and carry the fingerprint, so an operator looking at the
// gateway's session list can tell which entry is this service.
func TestUserAgentCarriesTheFingerprint(t *testing.T) {
	fp := NewFingerprint("abc")
	c := New("http://example.test")
	c.SetFingerprint(fp)

	ua := c.UserAgent()
	if !strings.Contains(ua, fp) {
		t.Fatalf("User-Agent %q must carry the fingerprint %q", ua, fp)
	}
	if !strings.Contains(ua, "qqbot") {
		t.Fatalf("User-Agent %q must name this service", ua)
	}
}

// A client with no fingerprint still sends a usable User-Agent: the gateway is
// behind Cloudflare, which rejects Go's default.
func TestUserAgentWithoutFingerprintIsStillIdentifying(t *testing.T) {
	c := New("http://example.test")
	if got := c.UserAgent(); !strings.Contains(got, "qqbot") || got == "" {
		t.Fatalf("User-Agent %q must still identify the service", got)
	}
}
