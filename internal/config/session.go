package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"qqbot/internal/store"
)

// The gateway session is stored as its own setting rather than inside the
// connection config: it is not something the operator edits, it must never be
// rendered in the WebUI, and a config save must not wipe it.
const (
	newAPISessionKey = "newapi.session"
)

// SessionOrigin is the identity a stored session belongs to: the gateway host,
// the account, and the password it was issued under. A session is a credential
// for exactly that triple, so it is only ever loaded back under the same origin
// — changing the URL cannot hand one instance's credential to another host,
// changing the account cannot leave the service silently acting as the old one,
// and changing the password cannot mask a typo behind the still-valid session
// of the old credentials (the operator's 测试连接 would otherwise report success
// for a password the gateway has never seen).
//
// A trailing slash is normalised away on both sides: the operator pastes the
// address with or without it, and the two are the same origin.
func SessionOrigin(url, username, password string) string {
	return strings.TrimRight(url, "/") + "\x00" + username + "\x00" + password
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

// fingerprintSecretSetting holds the secret the gateway fingerprint is derived
// from, generated on first use.
const fingerprintSecretSetting = "newapi.fingerprint_secret"

// fingerprintMu serialises the get-or-create below. Two callers reach it on a
// cold row — the live client's build path (under lazyGateway's lock) and the
// connection probe (under no such lock) — and a read-then-write race there
// would let both generate, leaving the database holding the loser's value while
// a client keeps using its own, i.e. an identity that disagrees with the stored
// one until the next rebuild.
var fingerprintMu sync.Mutex

// SessionFingerprint returns the secret this service derives its gateway
// fingerprint from, generating and persisting one on first use.
//
// It is its own secret, deliberately NOT the panel's session-cookie signing key:
// the fingerprint travels in a header to the gateway (and therefore into
// whatever request logs sit in between), so deriving it from the signing key
// would put a hash of that key in a third party's log for no benefit. Keeping
// them separate also means rotating either one cannot silently change the other.
//
// It is stored, not computed from the host or the process, so it is identical
// after a restart and inside a rebuilt container — which is the whole point:
// the gateway records the fingerprint on the session row, and a value that
// changed per start would look like a new device every time.
//
// An empty return means the fingerprint is unavailable (the store could not be
// read, or the system RNG failed). Callers present the plain User-Agent in that
// case rather than an unstable value; the failure is logged because otherwise
// the service would look like an older build forever with no explanation.
func (m *Manager) SessionFingerprint() string {
	fingerprintMu.Lock()
	defer fingerprintMu.Unlock()

	var secret string
	if err := m.st.GetSetting(fingerprintSecretSetting, &secret); err != nil {
		slog.Warn("无法读取设备指纹密钥，本次运行将不带指纹", "err", err)
		return ""
	}
	if secret != "" {
		return secret
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		slog.Warn("无法生成设备指纹密钥，本次运行将不带指纹", "err", err)
		return ""
	}
	secret = hex.EncodeToString(raw)
	// A failed write still returns the value for this process: an unstable
	// identity is worse than an unpersisted one, and the next start simply
	// generates another.
	if err := m.st.PutSetting(fingerprintSecretSetting, secret); err != nil {
		slog.Warn("无法保存设备指纹密钥，重启后指纹会变", "err", err)
	}
	return secret
}

// sessionStore is the narrow slice of store the session helpers need, so they
// stay testable and the dependency is visible.
type sessionStore interface {
	GetSetting(key string, out any) error
	PutSetting(key string, value any) error
}

var _ sessionStore = (*store.Store)(nil)
