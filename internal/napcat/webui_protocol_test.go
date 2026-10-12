package napcat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestR6WebUIEnvelopeFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"empty", `{}`, 200, false},
		{"null", `null`, 200, false},
		{"missing code", `{"data":{"network":{}}}`, 200, false},
		{"null code", `{"code":null,"data":{"network":{}}}`, 200, false},
		{"wrong code type", `{"code":"0","data":{"network":{}}}`, 200, false},
		{"missing data", `{"code":0}`, 200, false},
		{"null data", `{"code":0,"data":null}`, 200, false},
		{"business error", `{"code":-1,"message":"Not Login"}`, 200, false},
		{"http denial", `{"code":0,"data":{"network":{}}}`, 403, false},
		{"http server error", `{"code":0,"data":{"network":{}}}`, 500, false},
		{"valid", `{"code":0,"data":{"network":{}}}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			_, err := NewWebUI(srv.URL, "secret").GetOneBotConfig()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestR6WebUIWriteAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		valid  bool
	}{
		{`{}`, 200, false}, {`{"code":null}`, 200, false}, {`{"code":-1}`, 200, false},
		{`{"code":0,"data":null}`, 500, false}, {`{"code":0,"data":null}`, 200, true}, {`{"code":0}`, 200, true},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.body, tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			err := NewWebUI(srv.URL, "secret").SetOneBotConfig(&OneBotConfig{Network: map[string]json.RawMessage{}})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestR6WebUIInvalidResponseDoesNotEchoSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>credential-secret hash-secret</html>`)
	}))
	defer srv.Close()
	_, err := NewWebUI(srv.URL, "secret").GetOneBotConfig()
	if err == nil || strings.Contains(err.Error(), "credential-secret") || strings.Contains(err.Error(), "hash-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}
