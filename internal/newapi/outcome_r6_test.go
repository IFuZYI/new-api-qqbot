package newapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func r6Ambiguous(err error) bool { return IsAmbiguousWrite(fmt.Errorf("wrapped: %w", err)) }

func TestR6QuotaOutcome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		ambiguous bool
		ok        bool
	}{
		{"success", 200, `{"success":true}`, false, true},
		{"rejected", 200, `{"success":false,"message":"denied"}`, false, false},
		{"malformed", 200, `{"success":`, true, false},
		{"missing-success", 200, `{}`, true, false},
		{"null-success", 200, `{"success":null}`, true, false},
		{"empty", 200, ``, true, false},
		{"5xx-json", 500, `{"success":false,"message":"failed"}`, true, false},
		{"5xx-success", 502, `{"success":true}`, true, false},
		{"5xx-html", 503, `<html>`, true, false},
		{"unauthorized", 401, ``, false, false},
		{"rate", 429, ``, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			err := New(srv.URL).AddQuota(9, 100)
			if (err == nil) != tc.ok || r6Ambiguous(err) != tc.ambiguous {
				t.Fatalf("err=%v ambiguous=%v want=%v", err, r6Ambiguous(err), tc.ambiguous)
			}
			if calls != 1 {
				t.Fatalf("write retried: %d", calls)
			}
		})
	}
}

type r6Transport func(*http.Request) (*http.Response, error)

func (f r6Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type r6BrokenBody struct{}

func (r6BrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (r6BrokenBody) Close() error             { return nil }

func TestR6QuotaTransportAndReadUncertain(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			calls := 0
			c := New("http://example.invalid")
			c.http.Transport = r6Transport(func(*http.Request) (*http.Response, error) {
				calls++
				if read {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: r6BrokenBody{}}, nil
				}
				return nil, io.ErrUnexpectedEOF
			})
			err := c.AddQuota(9, 100)
			if !r6Ambiguous(err) || !errors.Is(err, io.ErrUnexpectedEOF) || calls != 1 {
				t.Fatalf("err=%v ambiguous=%v calls=%d", err, r6Ambiguous(err), calls)
			}
		})
	}
}

func TestR6QuotaValidationIsNotUncertain(t *testing.T) {
	c := New("http://example.invalid")
	calls := 0
	c.http.Transport = r6Transport(func(*http.Request) (*http.Response, error) { calls++; return nil, io.EOF })
	for _, args := range [][2]int{{9, 0}, {0, 100}, {-1, 100}} {
		err := c.AddQuota(args[0], args[1])
		if err == nil || r6Ambiguous(err) {
			t.Fatalf("bad validation: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid write dispatched %d times", calls)
	}
}

func TestR6AuthClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		auth   bool
	}{
		{401, "", true}, {401, "<html>unauthorized</html>", true},
		{401, `{"success":false,"code":"AUTH_ORIGIN_FORBIDDEN"}`, false},
		{403, `{"success":false,"code":"AUTH_ORIGIN_FORBIDDEN"}`, false},
		{401, `{"success":false,"code":"AUTH_USER_DISABLED"}`, false},
		{200, `{"success":false,"code":"AUTH_TOKEN_EXPIRED"}`, true},
		// A code the switch does not know must still fall back to the status rule.
		{401, `{"success":false,"code":"AUTH_SOMETHING_NEW"}`, true},
	} {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.body), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			_, err := New(srv.URL).Self()
			if IsAuthFailure(fmt.Errorf("wrapped: %w", err)) != tc.auth {
				t.Fatalf("auth=%v err=%v", tc.auth, err)
			}
		})
	}
	// Existing text-based callers remain compatible; this is not a bare status.
	if IsAuthFailure(errors.New(strings.Repeat("x", 3))) {
		t.Fatal("generic error classified as auth")
	}
}
