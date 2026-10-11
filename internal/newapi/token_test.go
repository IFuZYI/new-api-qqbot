package newapi

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The gateway's access token lives 15 minutes, but the login session behind it
// lives 30 days. Re-logging-in when the token expires therefore does not just
// cost a request: each login creates a NEW session, so a service that runs for
// long enough walks into the 50-session cap and locks itself out of the gateway
// entirely (see AUTH_SESSION_LIMIT). The token's own expiry is the signal to
// re-authenticate, and it must be tracked rather than ignored.
func TestClientTracksTokenExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := New("http://example.invalid")
	c.SetClock(func() time.Time { return now })

	if c.TokenValid() {
		t.Error("a client with no token must not report a valid one")
	}
	c.SetToken("tok")
	// A token with no recorded expiry is treated as valid: an older server, or a
	// token installed by hand, must not be discarded on a guess.
	if !c.TokenValid() {
		t.Error("a token with no expiry must be usable")
	}

	c.SetTokenExpiry(now.Add(15 * time.Minute))
	if !c.TokenValid() {
		t.Error("a fresh token must be valid")
	}

	// Still valid one second before expiry...
	now = now.Add(15*time.Minute - time.Second)
	if !c.TokenValid() {
		t.Error("a token must be usable right up to its expiry")
	}

	// ...and not one second after.
	now = now.Add(2 * time.Second)
	if c.TokenValid() {
		t.Error("an expired token must be reported as invalid")
	}
}

// The refresh margin exists so the token is replaced before a request can fail
// on it: a sweep that starts just before expiry would otherwise send a batch of
// calls with a token that dies mid-flight.
func TestClientRefreshesBeforeExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := New("http://example.invalid")
	c.SetClock(func() time.Time { return now })
	c.SetToken("tok")
	c.SetTokenExpiry(now.Add(15 * time.Minute))

	// Inside the margin the token is still technically valid, but the client
	// must not keep using it for new work.
	now = now.Add(15*time.Minute - 10*time.Second)
	if !c.TokenValid() {
		t.Error("the token is still valid for an in-flight request")
	}
	if c.TokenFresh() {
		t.Error("a token this close to expiry must not be reused for new work")
	}
}

// The login response carries the token's expiry, so it is read from there
// rather than assumed.
func TestLoginRecordsTokenExpiry(t *testing.T) {
	exp := time.Unix(1_700_000_900, 0)
	srv := jsonServer(t, `{"success":true,"data":{"access_token":"tok","access_expires_at":1700000900}}`)
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatalf("login: %v", err)
	}
	got := c.TokenExpiresAt()
	if got == nil || !got.Equal(exp) {
		t.Errorf("expiry = %v, want %v", got, exp)
	}
}

// A login response without an expiry must leave the previous value alone rather
// than invent one: guessing short would cause needless re-logins, guessing long
// would send requests with a dead token.
func TestLoginWithoutExpiryKeepsTheTokenUsable(t *testing.T) {
	srv := jsonServer(t, `{"success":true,"data":{"access_token":"tok"}}`)
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("admin", "pw"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !c.TokenValid() {
		t.Error("a token with no announced expiry must still be usable")
	}
}

// Two goroutines must not both decide the token is stale and both log in: that
// is exactly the double-spend of the login budget the lockout exists to avoid.
func TestConcurrentTokenChecksAreSafe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := New("http://example.invalid")
	c.SetClock(func() time.Time { return now })
	c.SetToken("tok")
	c.SetTokenExpiry(now.Add(time.Minute))

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = c.TokenValid()
				_ = c.TokenFresh()
				_ = c.TokenExpiresAt()
			}
		}()
	}
	wg.Wait()
}

// jsonServer answers every request with one canned body.
func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}
