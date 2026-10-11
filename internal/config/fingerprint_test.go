package config

import (
	"path/filepath"
	"testing"

	"qqbot/internal/store"
)

// The fingerprint secret must be stable across managers (a restart) and across
// processes, because a value that changed per start would present the gateway
// with a new device every time — the opposite of what it is for.
func TestSessionFingerprintSecretIsStable(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "f.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	// The api package mints this key on first use; seed it as that package does.
	if err := st.PutSetting("auth.session_key", "a-stable-signing-key"); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	m1 := NewManager(filepath.Join(dir, FileName), st)
	m2 := NewManager(filepath.Join(dir, FileName), st)
	got1, got2 := m1.SessionFingerprint(), m2.SessionFingerprint()
	if got1 == "" {
		t.Fatal("the fingerprint secret must be readable when the key exists")
	}
	if got1 != got2 {
		t.Fatalf("secret must be stable across managers: %q vs %q", got1, got2)
	}
	if got1 != "a-stable-signing-key" {
		t.Fatalf("secret = %q, want the stored signing key", got1)
	}
}

// With no key yet (a very early start), the secret is empty rather than a
// value derived from something unstable — the caller then omits the
// fingerprint instead of presenting one that would change next start.
func TestSessionFingerprintSecretIsEmptyWithoutAKey(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "f.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	if got := NewManager(filepath.Join(dir, FileName), st).SessionFingerprint(); got != "" {
		t.Fatalf("secret = %q, want empty when no key is stored", got)
	}
}
