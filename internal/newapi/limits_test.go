package newapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// byteRepeater is an endless source of one byte. A handler that wrote a fixed
// buffer would return before the client stopped reading, and the test would not
// exercise "the cap stops the read" at all.
type byteRepeater byte

func (b byteRepeater) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// The gateway is fronted by Cloudflare and can answer with something that is not
// the gateway at all — a proxy error page, a login wall, a hijacked response.
// io.ReadAll would buffer an unbounded body until the process dies, so the read
// must stop at the cap and say why.
func TestOversizedResponseBodyIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.CopyN(w, byteRepeater('x'), maxResponseBody+1)
	}))
	defer srv.Close()

	err := New(srv.URL).Login("admin", "pw")
	if err == nil {
		t.Fatal("an oversized response must be an error, not a silent read")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("the message must say the body was too large: %v", err)
	}
	// The call must be named: the operator needs to know which request of a
	// sweep met the runaway body.
	if !strings.Contains(err.Error(), "/api/user/login") {
		t.Errorf("the message must name the call: %v", err)
	}
}

// The cap must not turn an ordinary answer into an error, and a body exactly at
// the limit must not be mistaken for a truncated one (the read goes one byte
// past the cap to tell them apart).
func TestReadBodyAcceptsBodiesUpToTheCap(t *testing.T) {
	atCap := strings.Repeat("x", maxResponseBody)
	got, err := readBody(strings.NewReader(atCap))
	if err != nil {
		t.Fatalf("a body exactly at the cap must be read, got %v", err)
	}
	if len(got) != maxResponseBody {
		t.Errorf("read %d bytes, want %d", len(got), maxResponseBody)
	}

	onePast := strings.Repeat("x", maxResponseBody+1)
	if _, err := readBody(strings.NewReader(onePast)); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("a body one byte past the cap must be refused, got %v", err)
	}
}

// readBody must stop reading at the cap, not merely refuse an oversized body
// after buffering it. The finite tests above pass even with the cap removed —
// the reader ends on its own and the length check then fires — so only an
// endless source distinguishes a bounded read from an unbounded one.
//
// The mirror of this test in internal/napcat (TestReadBodyStopsAtTheCap) caught
// exactly that: with `io.ReadAll` in place of the LimitReader the process was
// OOM-killed rather than failing.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestReadBodyStopsAtTheCap(t *testing.T) {
	done := make(chan struct{})
	var (
		got []byte
		err error
	)
	go func() {
		defer close(done)
		got, err = readBody(endlessReader{})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("readBody did not return on an endless body: the read is not capped")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
	if len(got) > maxResponseBody+1 {
		t.Errorf("held %d bytes, want at most %d", len(got), maxResponseBody+1)
	}
}

// A normal call is unaffected: a small, well-formed envelope still decodes.
func TestNormalResponseIsUnaffectedByTheCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okEnvelope(map[string]any{
			"access_token": "tok-123", "access_expires_at": 1791630000,
		})))
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatalf("a small response must be unaffected: %v", err)
	}
	if c.Token() != "tok-123" {
		t.Errorf("token not stored: %q", c.Token())
	}
}
