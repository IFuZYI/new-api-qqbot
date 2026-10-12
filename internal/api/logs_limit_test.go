package api

import (
	"testing"

	"qqbot/internal/logging"
)

// limit=0 means "no explicit cap" rather than "use the default": a caller that
// wants everything the server is willing to return has no other way to say so.
func TestLogsLimitZeroMeansMaxNotDefault(t *testing.T) {
	env, m := newLogEnv(t)
	if err := m.SetLevel(logging.LevelDebug); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		m.Logger().Info("record")
	}

	var got logging.Result
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs?limit=0", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Entries) != 250 {
		t.Errorf("limit=0 returned %d entries, want all 250 (it must not fall back to the default 200)", len(got.Entries))
	}

	// No limit parameter at all keeps the small default.
	var dflt logging.Result
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs", &dflt); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(dflt.Entries) != logging.DefaultLimit || !dflt.Truncated {
		t.Errorf("no-limit returned %d entries (truncated=%v), want the default %d", len(dflt.Entries), dflt.Truncated, logging.DefaultLimit)
	}
}

// An explicit limit is honoured.
func TestLogsLimitIsHonoured(t *testing.T) {
	env, m := newLogEnv(t)
	for i := 0; i < 30; i++ {
		m.Logger().Info("record")
	}
	var got logging.Result
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/logs?limit=5", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Entries) != 5 {
		t.Errorf("limit=5 returned %d entries", len(got.Entries))
	}
}
