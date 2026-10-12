package napcat

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestR6OneBotRejectsInvalidSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"empty", `{}`, 200, false},
		{"missing retcode", `{"status":"ok","data":[]}`, 200, false},
		{"missing status", `{"retcode":0,"data":[]}`, 200, false},
		{"missing data", `{"status":"ok","retcode":0}`, 200, false},
		{"null data", `{"status":"ok","retcode":0,"data":null}`, 200, false},
		{"null retcode", `{"status":"ok","retcode":null,"data":[]}`, 200, false},
		{"failed status", `{"status":"failed","retcode":0,"data":[]}`, 200, false},
		{"http denial", `{"status":"ok","retcode":0,"data":[]}`, 403, false},
		{"valid empty", `{"status":"ok","retcode":0,"data":[]}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			users, err := New(srv.URL, "token").GetGroupMemberList(1)
			if (err == nil) != tc.valid {
				t.Fatalf("users=%v err=%v valid=%v", users, err, tc.valid)
			}
		})
	}
}
