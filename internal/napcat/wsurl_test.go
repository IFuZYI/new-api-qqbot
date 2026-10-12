package napcat

import (
	"strings"
	"testing"
)

// A stored WebSocket URL is typed by the operator, so the accepted forms are
// pinned here: the two ws schemes pass through (trimmed and canonicalised),
// while anything else is refused with a message naming the scheme it saw — an
// http:// address stored as the event URL fails later with a confusing error
// and no hint that the address was the problem.
func TestParseWSURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ws://127.0.0.1:3001", "ws://127.0.0.1:3001"},
		{"  ws://127.0.0.1:3001  ", "ws://127.0.0.1:3001"},
		{"wss://napcat.example.com/onebot?token=x", "wss://napcat.example.com/onebot?token=x"},
		{"WS://127.0.0.1:3001", "ws://127.0.0.1:3001"},
	} {
		got, err := ParseWSURL(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseWSURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}

	for _, tc := range []struct{ in, names string }{
		{"http://127.0.0.1:3001", "http"},
		{"https://napcat.example.com", "https"},
		{"", "ws:// or wss://"},
	} {
		got, err := ParseWSURL(tc.in)
		if err == nil {
			t.Errorf("ParseWSURL(%q) accepted a non-WebSocket URL: %q", tc.in, got)
			continue
		}
		if got != "" {
			t.Errorf("ParseWSURL(%q) returned %q alongside an error", tc.in, got)
		}
		if !strings.Contains(err.Error(), tc.names) {
			t.Errorf("ParseWSURL(%q) error should name %q, got %q", tc.in, tc.names, err)
		}
	}

	// A bare host:port is the shape an operator copies off the connection page,
	// and it is not a URL: it must be refused rather than half-parsed into a
	// value that fails at connect time.
	if got, err := ParseWSURL("127.0.0.1:3001"); err == nil || got != "" {
		t.Errorf("bare host:port accepted: %q %v", got, err)
	}
}
