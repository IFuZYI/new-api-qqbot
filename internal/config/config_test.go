package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qqbot/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cfg.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	path := filepath.Join(t.TempDir(), "data", FileName)
	return NewManager(path, st), st
}

func sample() Config {
	return Config{
		NewAPI: NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "pw"},
		NapCat: NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "wt"},
	}
}

func TestSaveWritesBothCopies(t *testing.T) {
	m, st := newTestManager(t)

	if err := m.Save(sample()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The file must exist and hold the values.
	raw, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	if !strings.Contains(string(raw), "api.fuzy.site") {
		t.Errorf("file missing the gateway URL:\n%s", raw)
	}
	// It carries credentials, so it must not be world readable.
	info, err := os.Stat(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config file should be owner-only, got %o", perm)
	}

	// The database copy must match.
	var got NewAPI
	if err := st.GetSetting(newAPIKey, &got); err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://api.fuzy.site" || got.Password != "pw" {
		t.Errorf("database copy wrong: %+v", got)
	}
}

func TestLoadReadsBackSavedConfig(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}

	got, err := m.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.NewAPI.URL != "https://api.fuzy.site" || got.NapCat.WebUIToken != "wt" {
		t.Errorf("round trip failed: %+v", got)
	}
}

// Hand-editing the file must take effect without a restart: this is the whole
// point of keeping a file at all.
func TestHandEditOfFileIsAdopted(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}

	edited := strings.Replace(
		string(mustRead(t, m.Path())),
		"https://api.fuzy.site", "https://edited.example.com", 1)
	if err := os.WriteFile(m.Path(), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := m.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.NewAPI.URL != "https://edited.example.com" {
		t.Errorf("hand edit was not adopted: %q", got.NewAPI.URL)
	}
}

// A malformed file must not destroy a working configuration.
func TestMalformedFileDoesNotWipeStoredConfig(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(m.Path(), []byte("this: [is: not: valid: yaml"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := m.Load()
	if err == nil {
		t.Error("a malformed config file should be reported")
	}
	if got.NewAPI.URL != "https://api.fuzy.site" {
		t.Errorf("the stored config was lost to a typo: %+v", got)
	}
}

// Saving from the WebUI must not be mistaken for a hand edit on the next read.
func TestWebUISaveIsNotReAdopted(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}

	// A second save with different values, then a load: the load must return the
	// saved values, not stale ones from the file.
	updated := sample()
	updated.NewAPI.URL = "https://second.example.com"
	if err := m.Save(updated); err != nil {
		t.Fatal(err)
	}

	got, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.NewAPI.URL != "https://second.example.com" {
		t.Errorf("got %q, want the most recent save", got.NewAPI.URL)
	}
}

func TestEnsureFileSeedsFromDatabase(t *testing.T) {
	m, _ := newTestManager(t)
	// Configure through the "WebUI" path only: database, no file.
	if err := m.saveToStore(sample()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Path()); !os.IsNotExist(err) {
		t.Fatal("precondition: file should not exist yet")
	}

	if err := m.EnsureFile(); err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}
	raw := mustRead(t, m.Path())
	if !strings.Contains(string(raw), "api.fuzy.site") {
		t.Errorf("seeded file missing the stored config:\n%s", raw)
	}

	// Calling it again must not clobber an edited file.
	if err := os.WriteFile(m.Path(), []byte("# my own notes\nnewapi:\n  url: https://keep.me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureFile(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustRead(t, m.Path())), "keep.me") {
		t.Error("EnsureFile overwrote an existing file")
	}
}

func TestLoadWithoutFileOrDatabaseIsEmptyNotError(t *testing.T) {
	m, _ := newTestManager(t)
	got, err := m.Load()
	if err != nil {
		t.Fatalf("a fresh install should load cleanly: %v", err)
	}
	if got.NewAPI.URL != "" {
		t.Errorf("expected empty config, got %+v", got)
	}
}

func TestRedactedAndMergeProtectSecrets(t *testing.T) {
	c := sample()
	red := c.Redacted()
	if red.NewAPI.Password != RedactedPassword || red.NapCat.WebUIToken != RedactedPassword {
		t.Errorf("secrets not redacted: %+v", red)
	}
	if red.NewAPI.URL != c.NewAPI.URL {
		t.Error("redaction must not touch non-secrets")
	}

	// Saving the redacted form back must keep the real secrets.
	merged := red.Merge(c)
	if merged.NewAPI.Password != "pw" || merged.NapCat.WebUIToken != "wt" {
		t.Errorf("merge lost the secrets: %+v", merged)
	}
}

func TestValidate(t *testing.T) {
	good := sample()
	if err := good.NewAPI.Validate(); err != nil {
		t.Errorf("valid gateway rejected: %v", err)
	}
	if err := good.NapCat.Validate(); err != nil {
		t.Errorf("valid napcat rejected: %v", err)
	}

	bad := []NewAPI{
		{Username: "u", Password: "p"},
		{URL: "not-a-url", Username: "u", Password: "p"},
		{URL: "https://x", Password: "p"},
		{URL: "https://x", Username: "u"},
	}
	for i, n := range bad {
		if err := n.Validate(); err == nil {
			t.Errorf("gateway case %d should be rejected", i)
		}
	}

	if err := (NapCat{}).Validate(); err == nil {
		t.Error("napcat with no transport should be rejected")
	}
	if err := (NapCat{WebUIURL: "http://x"}).Validate(); err == nil {
		t.Error("webui without a token should be rejected")
	}
	if err := (NapCat{OneBotURL: "http://x:3002"}).Validate(); err != nil {
		t.Errorf("onebot-only config should be valid: %v", err)
	}
	if err := (NapCat{OneBotURL: "http://x", EventWSURL: "http://not-ws"}).Validate(); err == nil {
		t.Error("event URL must be ws:// or wss://")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
