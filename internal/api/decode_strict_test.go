package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qqbot/internal/config"
)

// A body carrying more than one JSON document must be refused: the service
// would act on the first one while every other reader (a proxy, a log parser)
// sees a malformed request. A single document with trailing whitespace is fine.
func TestDecodeRequiresExactlyOneDocument(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool // true = accepted
	}{
		{"single object", `{"a":1}`, true},
		{"trailing whitespace", "{\"a\":1}\n\t ", true},
		{"second object", `{"a":1}{"b":2}`, false},
		{"trailing junk", `{"a":1}xyz`, false},
		{"trailing null", `{"a":1}null`, false},
		{"trailing array", `{"a":1}[1]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/anything", strings.NewReader(tc.body))
			var out struct {
				A int `json:"a"`
			}
			err := decode(req, &out)
			if got := err == nil; got != tc.want {
				t.Fatalf("accepted=%v want %v (err=%v)", got, tc.want, err)
			}
		})
	}
}

// A negative QQ or account id can never be real; persisting one leaves a
// binding no lookup will ever match. The single-account group change already
// rejects <= 0, so the binding path must agree.
func TestManualBindingRejectsNonPositiveIDs(t *testing.T) {
	r, _, srv, cl := newBareRouter(t)
	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "hunter2hunter2"}); err != nil {
		t.Fatal(err)
	}
	if code, _ := post(t, cl, srv.URL+"/api/auth/login", `{"username":"admin","password":"hunter2hunter2"}`); code != 200 {
		t.Fatalf("login: %d", code)
	}
	for _, body := range []string{
		`{"qq":-1,"newapi_user_id":5}`,
		`{"qq":100,"newapi_user_id":-5}`,
		`{"qq":0,"newapi_user_id":5}`,
		`{"qq":100,"newapi_user_id":0}`,
	} {
		code, resp := post(t, cl, srv.URL+"/api/bindings", body)
		if code != 400 {
			t.Fatalf("body %s: got %d (%s), want 400", body, code, resp)
		}
	}
	// The positive case must still work, so the check is not simply rejecting
	// every binding.
	if code, resp := post(t, cl, srv.URL+"/api/bindings", `{"qq":100,"newapi_user_id":5}`); code != 200 {
		t.Fatalf("valid binding rejected: %d (%s)", code, resp)
	}
}
