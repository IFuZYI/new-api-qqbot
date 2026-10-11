package config

import (
	"path/filepath"
	"testing"

	"qqbot/internal/store"
)

func newFingerprintStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "f.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// The fingerprint secret must be stable across managers (a restart) and across
// processes: a value that changed per start would present the gateway with a new
// device every time, which is the opposite of what it is for.
func TestSessionFingerprintSecretIsStable(t *testing.T) {
	st := newFingerprintStore(t)
	m1 := NewManager(filepath.Join(t.TempDir(), FileName), st)
	m2 := NewManager(filepath.Join(t.TempDir(), FileName), st)

	got1, got2 := m1.SessionFingerprint(), m2.SessionFingerprint()
	if got1 == "" {
		t.Fatal("the fingerprint secret must be available")
	}
	if got1 != got2 {
		t.Fatalf("secret must be stable across managers: %q vs %q", got1, got2)
	}
}

// The fingerprint is sent to the gateway in a header, so it must be derived from
// its OWN secret, never from the key that signs the panel's session cookies: a
// signing key that leaks into a third party's request log is a different (and
// worse) problem than a fingerprint. Keeping the two independent also means
// rotating either one cannot silently change the other.
func TestSessionFingerprintDoesNotUseThePanelSigningKey(t *testing.T) {
	st := newFingerprintStore(t)
	if err := st.PutSetting("auth.session_key", "the-panel-signing-key"); err != nil {
		t.Fatalf("seed signing key: %v", err)
	}
	m := NewManager(filepath.Join(t.TempDir(), FileName), st)

	secret := m.SessionFingerprint()
	if secret == "" {
		t.Fatal("the fingerprint secret must be available")
	}
	if secret == "the-panel-signing-key" {
		t.Fatal("the fingerprint must not be the panel's session-cookie signing key")
	}

	// Changing the signing key must leave the fingerprint secret alone.
	if err := st.PutSetting("auth.session_key", "a-rotated-signing-key"); err != nil {
		t.Fatalf("rotate signing key: %v", err)
	}
	if got := NewManager(filepath.Join(t.TempDir(), FileName), st).SessionFingerprint(); got != secret {
		t.Fatalf("rotating the signing key changed the fingerprint secret: %q -> %q", secret, got)
	}
}

// A fingerprint secret is generated on first use and persisted, so the very
// first call has one and every later call (and every restart) gets the same.
func TestSessionFingerprintSecretIsGeneratedOnceAndPersisted(t *testing.T) {
	st := newFingerprintStore(t)

	first := NewManager(filepath.Join(t.TempDir(), FileName), st).SessionFingerprint()
	if first == "" {
		t.Fatal("the first read must generate a secret, not return empty")
	}
	// A fresh manager over the same store (i.e. a restart) must read it back.
	if again := NewManager(filepath.Join(t.TempDir(), FileName), st).SessionFingerprint(); again != first {
		t.Fatalf("secret was not persisted: %q then %q", first, again)
	}
}
