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
	if err := NewManager(filepath.Join(dir, FileName), st).SaveNewAPISession(raw); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A second Manager stands in for the next start of the process.
	got := NewManager(filepath.Join(dir, FileName), st).LoadNewAPISession()
	if got != raw {
		t.Errorf("LoadNewAPISession = %q, want %q", got, raw)
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

	if got := NewManager(filepath.Join(dir, FileName), st).LoadNewAPISession(); got != "" {
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
	if err := m.SaveNewAPISession("a.b"); err != nil {
		t.Fatal(err)
	}
	if err := m.ClearNewAPISession(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := m.LoadNewAPISession(); got != "" {
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
	if err := m.SaveNewAPISession(raw); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(Config{NewAPI: NewAPI{URL: "https://api.example.test", Username: "napcat", Password: "pw"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := m.LoadNewAPISession(); got != raw {
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
	if err := m.SaveNewAPISession(raw); err != nil {
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
