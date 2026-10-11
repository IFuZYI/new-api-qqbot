package config

import (
	"path/filepath"
	"testing"

	"qqbot/internal/store"
)

// The session must survive a process restart: it is the difference between
// reusing the gateway's login session and spending a new one every start.
func TestSessionPersistsAcrossManagers(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const raw = "3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"
	origin := SessionOrigin("https://api.example.test", "napcat")
	if err := NewManager(filepath.Join(dir, FileName), st).SaveNewAPISession(origin, raw); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A second Manager stands in for the next start of the process.
	got := NewManager(filepath.Join(dir, FileName), st).LoadNewAPISession(origin)
	if got != raw {
		t.Errorf("LoadNewAPISession = %q, want %q", got, raw)
	}
}

// A session is a credential for the host and account that issued it. Importing
// it into a client pointed at another host would hand one instance's credential
// to the other; loading it under a different account would silently keep acting
// as the old one. Both must read as "no session".
func TestSessionIsBoundToItsOrigin(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := NewManager(filepath.Join(dir, FileName), st)
	const raw = "3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"
	origin := SessionOrigin("https://api.example.test", "napcat")
	if err := m.SaveNewAPISession(origin, raw); err != nil {
		t.Fatalf("save: %v", err)
	}

	if got := m.LoadNewAPISession(origin); got != raw {
		t.Errorf("the same origin must load its own session, got %q", got)
	}
	for name, other := range map[string]string{
		"a different host":    SessionOrigin("https://other.example.test", "napcat"),
		"a different account": SessionOrigin("https://api.example.test", "someoneelse"),
	} {
		if got := m.LoadNewAPISession(other); got != "" {
			t.Errorf("%s must not load the stored session, got %q", name, got)
		}
	}
}

// No stored session must read as empty, not as an error: a first run is normal.
func TestSessionAbsentIsEmpty(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	origin := SessionOrigin("https://api.example.test", "napcat")
	if got := NewManager(filepath.Join(dir, FileName), st).LoadNewAPISession(origin); got != "" {
		t.Errorf("a missing session must read as empty, got %q", got)
	}
}

// A dead session must be clearable, or the next start would send a credential
// the gateway has already rejected.
func TestSessionCanBeCleared(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := NewManager(filepath.Join(dir, FileName), st)
	origin := SessionOrigin("https://api.example.test", "napcat")
	if err := m.SaveNewAPISession(origin, "a.b"); err != nil {
		t.Fatal(err)
	}
	if err := m.ClearNewAPISession(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := m.LoadNewAPISession(origin); got != "" {
		t.Errorf("after clearing, got %q", got)
	}
}

// Saving the connection config must not disturb the session: they are separate
// settings, and a config edit is routine.
func TestSavingConfigKeepsTheSession(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := NewManager(filepath.Join(dir, FileName), st)
	const raw = "3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"
	origin := SessionOrigin("https://api.example.test", "napcat")
	if err := m.SaveNewAPISession(origin, raw); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(Config{NewAPI: NewAPI{URL: "https://api.example.test", Username: "napcat", Password: "pw"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := m.LoadNewAPISession(origin); got != raw {
		t.Errorf("the session must survive a config save, got %q", got)
	}
}

// The session must never appear in the config the WebUI renders: it is a
// credential the operator does not edit, and the redaction helper only knows
// about the fields it was written for.
func TestSessionNeverAppearsInRenderedConfig(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := NewManager(filepath.Join(dir, FileName), st)
	const raw = "3f2504e0-4f89-11d3-9a0c-0305e82c3301.secret"
	if err := m.SaveNewAPISession(SessionOrigin("https://api.example.test", "napcat"), raw); err != nil {
		t.Fatal(err)
	}
	rendered := m.MustLoad().Redacted()
	if containsSecret(rendered, raw) {
		t.Error("the session must not be part of the rendered config")
	}
}

func containsSecret(c Config, raw string) bool {
	// The value only has somewhere to appear if it was copied into a field the
	// WebUI renders; check every string field for the token's secret half.
	secret := raw[len(raw)-6:]
	fields := []string{
		c.NewAPI.URL, c.NewAPI.Username, c.NewAPI.Password,
		c.NapCat.WebUIURL, c.NapCat.WebUIToken, c.NapCat.OneBotURL,
		c.NapCat.OneBotToken, c.NapCat.EventWSURL, c.NapCat.EventWSToken,
	}
	for _, f := range fields {
		if f != "" && (f == raw || len(f) >= len(secret) && f[len(f)-len(secret):] == secret) {
			return true
		}
	}
	return false
}
