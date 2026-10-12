package newapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The gateway's "Invalid parameters" answer (password encryption enabled,
// plaintext password sent) is a permanent configuration incompatibility: the
// same request can never succeed. Retrying it three times spends three slots of
// the CriticalRateLimit-protected login budget per sweep.
func TestR6InvalidParametersIsNotRetried(t *testing.T) {
	var logins int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/login" {
			atomic.AddInt32(&logins, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":false,"message":"Invalid parameters"}`))
			return
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"success":false,"message":"Unauthorized"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.Reauthenticate("napcat", "pw")
	if err == nil {
		t.Fatal("expected the login to fail")
	}
	if got := atomic.LoadInt32(&logins); got != 1 {
		t.Fatalf("permanent refusal retried: %d login attempts", got)
	}
}
