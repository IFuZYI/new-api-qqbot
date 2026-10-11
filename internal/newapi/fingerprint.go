package newapi

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The gateway keys a login session on the request's User-Agent and IP (it
// records both on the session row, see service/auth_session.go upstream) and
// hands the credential back as an HttpOnly cookie. It never dedupes: every
// login mints a new session, and the account's active-session cap is what
// eventually locks everyone out.
//
// A stable, service-specific fingerprint therefore does two things:
//
//   - it makes every request from this service look like the same device, so
//     the gateway's session list shows one entry for this service rather than
//     an anonymous crowd, which is what an operator needs to revoke the right
//     one; and
//   - it is derived from stored state, not from the process, so a restart
//     presents the identical identity and can keep reusing its session instead
//     of being treated as a new device.
//
// It cannot by itself stop a login (the gateway has no "reuse my session"
// endpoint) — what stops logins is reusing the stored session and refreshing
// it, which is what the rest of this package does. The fingerprint is what
// keeps that identity recognisable across restarts.
// fingerprintSetting is the setting the fingerprint is derived from. The secret
// itself lives in config (which generates and persists it); this name is here so
// the two ends of that agreement are greppable together.
const fingerprintSetting = "newapi.fingerprint_secret"

// fingerprintBytes is how much of the digest is kept. 12 bytes is 24 hex
// characters: far beyond any collision concern for a handful of clients, and
// short enough to sit in a header next to the rest of the User-Agent.
const fingerprintBytes = 12

// NewFingerprint derives the device fingerprint from a stable secret.
//
// The secret is the panel's own HMAC key, which lives in the database and
// survives restarts and container rebuilds. Deriving rather than storing a
// second secret keeps one thing to back up, and hashing keeps the key itself
// out of a header the gateway (and any proxy log) can see.
func NewFingerprint(secret string) string {
	sum := sha256.Sum256([]byte("qqbot-fingerprint\x00" + secret))
	return hex.EncodeToString(sum[:fingerprintBytes])
}

// SetFingerprint installs the fingerprint this client presents.
func (c *Client) SetFingerprint(fp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fingerprint = fp
}

// Fingerprint returns the installed fingerprint, or "" when none is set.
func (c *Client) Fingerprint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fingerprint
}

// UserAgent is the identity this client presents to the gateway.
//
// It is exposed (rather than kept a package constant) because the session list
// on the gateway shows the User-Agent it recorded, so the panel can tell the
// operator which entry belongs to this service.
func (c *Client) UserAgent() string {
	fp := c.Fingerprint()
	if fp == "" {
		return userAgent
	}
	return userAgent + " fp/" + fp
}

// userAgentWith returns the request's User-Agent value.
func (c *Client) userAgentWith() string {
	return c.UserAgent()
}

// looksLikeFingerprint reports whether s has the shape NewFingerprint emits.
// A stored value that does not is treated as absent, so a corrupted setting
// cannot be presented to the gateway as an identity.
func looksLikeFingerprint(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != fingerprintBytes*2 {
		return false
	}
	for _, r := range s {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			return false
		}
	}
	return true
}
