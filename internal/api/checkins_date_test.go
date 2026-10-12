package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// GET /api/checkins must reject a malformed date instead of answering 200 with
// an empty list.
//
// The value is bound as a SQL parameter, so there is no injection — but a
// typo'd day reads as "no check-ins that day", which is exactly the class of
// input the logs endpoint rejects with 400 (a malformed limit). A caller cannot
// tell a misspelled date from a genuinely empty day.
func TestListCheckinsRejectsAMalformedDate(t *testing.T) {
	srv, _, cl := newTestServer(t)

	for _, bad := range []string{"garbage", "2026-13-01", "2026-1-1", "20261012", "2026-10-12x"} {
		var out map[string]any
		code := getJSONWith(t, cl, srv.URL+"/api/checkins?date="+bad, &out)
		if code != http.StatusBadRequest {
			t.Errorf("date=%q answered %d, want 400 (a typo must not read as an empty day)", bad, code)
		}
	}

	// A well-formed day still works.
	var ok map[string]any
	if code := getJSONWith(t, cl, srv.URL+"/api/checkins?date=2026-10-12", &ok); code != http.StatusOK {
		t.Errorf("a valid date answered %d, want 200", code)
	}
	// And the default (no date) still answers for today.
	var dflt map[string]any
	if code := getJSONWith(t, cl, srv.URL+"/api/checkins", &dflt); code != http.StatusOK {
		t.Errorf("no date answered %d, want 200", code)
	}
	if got, _ := dflt["date"].(string); got == "" {
		t.Error("the default answer carries no date")
	}
}

// The rejection must name the expected shape, so the operator can fix it.
func TestListCheckinsDateRejectionExplainsTheFormat(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var out map[string]any
	code := getJSONWith(t, cl, srv.URL+"/api/checkins?date=nope", &out)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	body, _ := json.Marshal(out)
	if !strings.Contains(string(body), "YYYY-MM-DD") {
		t.Errorf("the error does not state the expected format: %s", body)
	}
}
