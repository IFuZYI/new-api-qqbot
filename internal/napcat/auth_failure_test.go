package napcat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NapCat answers an expired or missing credential with sendError(res,
// 'Unauthorized') — a 200 response whose envelope carries code -1 and the
// message "Unauthorized" (reference/.../utils/response.ts, middleware/auth.ts).
// The service must recognise that as an auth failure so its cached bridge can
// be dropped and re-logged-in; the credential is signed with a per-process
// secret and dies with a NapCat restart, which is a routine event.
func TestIsAuthFailureWebUICredentialRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":-1,"message":"Unauthorized"}`))
	}))
	defer srv.Close()

	w := NewWebUI(srv.URL, "tok")
	w.SetCredential("stale-credential")
	err := w.do("/api/OB11Config/GetConfig", map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected the call to fail")
	}
	if !IsAuthFailure(err) {
		t.Fatalf("rejected WebUI credential not classified as auth failure: %v", err)
	}
}

// A transport error, a rate limit and a business failure are not credential
// rejections: re-logging-in does not fix them and doing so spends NapCat's
// login rate limit.
func TestIsAuthFailureIsNarrow(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"connection refused", errors.New("Get \"http://127.0.0.1:6099/x\": dial tcp: connection refused")},
		{"login rate limit", errors.New("login rate limit")},
		{"business failure", errors.New("QQ Not Login")},
		{"server error", errors.New("get_group_list: HTTP 500")},
		{"decode failure", errors.New("invalid WebUI response")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsAuthFailure(tc.err) {
				t.Fatalf("%v must not be classified as an auth failure", tc.err)
			}
		})
	}
}

// A non-2xx credential rejection (a reverse proxy in front of NapCat answering
// 401/403, or a future NapCat version using the HTTP status) is also an auth
// failure, but a 429 or 5xx is not.
func TestIsAuthFailureHTTPStatus(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusUnauthorized:        true,
		http.StatusForbidden:           true,
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
		http.StatusBadGateway:          false,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		w := NewWebUI(srv.URL, "tok")
		w.SetCredential("cred")
		err := w.do("/api/OB11Config/GetConfig", map[string]any{}, nil)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if got := IsAuthFailure(err); got != want {
			t.Fatalf("status %d: IsAuthFailure=%v want %v (err=%v)", status, got, want, err)
		}
	}
}
