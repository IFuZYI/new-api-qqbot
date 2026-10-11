package config

import "qqbot/internal/store"

// The gateway session is stored as its own setting rather than inside the
// connection config: it is not something the operator edits, it must never be
// rendered in the WebUI, and a config save must not wipe it.
const (
	newAPISessionKey = "newapi.session"
)

// LoadNewAPISession returns the stored refresh token, or "" when there is none.
//
// It exists so a restart reuses the session it already has. The alternative —
// logging in on every start — spends one of the account's 50 sessions each time,
// which is how the gateway ends up refusing everybody.
func (m *Manager) LoadNewAPISession() string {
	var raw string
	if err := m.st.GetSetting(newAPISessionKey, &raw); err != nil {
		return ""
	}
	return raw
}

// SaveNewAPISession stores the refresh token. An empty value clears it, which is
// what a rejected session should do: keeping a dead credential would only make
// the next start send it and fail.
func (m *Manager) SaveNewAPISession(raw string) error {
	if raw == "" {
		return m.st.PutSetting(newAPISessionKey, "")
	}
	return m.st.PutSetting(newAPISessionKey, raw)
}

// ClearNewAPISession forgets the stored session.
func (m *Manager) ClearNewAPISession() error {
	return m.st.PutSetting(newAPISessionKey, "")
}

// sessionStore is the narrow slice of store the session helpers need, so they
// stay testable and the dependency is visible.
type sessionStore interface {
	GetSetting(key string, out any) error
	PutSetting(key string, value any) error
}

var _ sessionStore = (*store.Store)(nil)
