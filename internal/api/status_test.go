package api

import (
	"testing"

	"qqbot/internal/service"
)

// The panel shows when each patrol last ran, so an operator can tell "nothing
// happened" from "the patrol is not running". Without this the only signal is
// the data itself, which is ambiguous exactly when it matters.
func TestStatusEndpointReportsPatrols(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var got struct {
		Patrols map[string]struct {
			LastRun   int64  `json:"last_run"`
			LastError string `json:"last_error"`
		} `json:"patrols"`
		Settings service.Settings `json:"settings"`
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/status", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, name := range []string{"members", "bind", "checkins", "rules"} {
		if _, ok := got.Patrols[name]; !ok {
			t.Errorf("status is missing the %q patrol", name)
		}
	}
	// The switches come along so the UI can show "停用" instead of an empty
	// last-run time, which would look like a broken patrol.
	if !got.Settings.BindCommandEnabled {
		t.Errorf("the settings must be reported alongside: %+v", got.Settings)
	}
}
