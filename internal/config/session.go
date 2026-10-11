package config

import (
	"encoding/json"
	"strings"

	"qqbot/internal/store"
)

// The gateway session is stored as its own setting rather than inside the
// connection config: it is not something the operator edits, it must never be
// rendered in the WebUI, and a config save must not wipe it.
const (
	newAPISessionKey = "newapi.session"
)

// SessionOrigin is the identity a stored session belongs to: the gateway host
// and the account it was issued for. A session is a credential for exactly
// that pair, so it is only ever loaded back under the same origin — changing
// the URL cannot hand one instance's credential to another host, and changing
// the account cannot leave the service silently acting as the old one.
func SessionOrigin(url, username string) string {
	return strings.TrimRight(url, "/") + "\x00" + username
}

// storedSession is the on-disk shape: the origin a token was issued for, plus
// the token itself.
type storedSession struct {
	Origin string `json:"origin"`
	Token  string `json:"token"`
}

// LoadNewAPISession returns the stored refresh token when it belongs to this
// origin, and "" otherwise.
//
// It exists so a restart reuses the session it already has. The alternative —
// logging in on every start — spends one of the account's 50 sessions each time,
// which is how the gateway ends up refusing everybody.
//
// A value stored before origin binding (a bare token) has no recorded origin,
// so it cannot be attributed to a host and reads as absent; the cost is one
// fresh login on the upgrade, which is the safe direction.
func (m *Manager) LoadNewAPISession(origin string) string {
	var raw string
	if err := m.st.GetSetting(newAPISessionKey, &raw); err != nil {
		return ""
	}
	if raw == "" {
		return ""
	}
	var s storedSession
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return ""
	}
	if s.Origin != origin {
		return ""
	}
	return s.Token
}

// SaveNewAPISession stores the refresh token under its origin. An empty value
// clears it, which is what a rejected session should do: keeping a dead
// credential would only make the next start send it and fail.
func (m *Manager) SaveNewAPISession(origin, raw string) error {
	if raw == "" {
		return m.st.PutSetting(newAPISessionKey, "")
	}
	b, err := json.Marshal(storedSession{Origin: origin, Token: raw})
	if err != nil {
		return err
	}
	return m.st.PutSetting(newAPISessionKey, string(b))
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
