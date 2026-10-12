package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"qqbot/internal/config"
	"qqbot/internal/store"
)

// sessionCookie is the WebUI's session cookie.
const sessionCookie = "qqbot_session"

// sessionTTL is how long a WebUI login stays valid.
const sessionTTL = 7 * 24 * time.Hour

// minPasswordLength is enforced at setup and when changing a password.
const minPasswordLength = 8

// sessionKeySetting holds the signing key for session cookies, generated on
// first use. It lives in the database so logins survive a restart.
const sessionKeySetting = "auth.session_key"

// loadSessionKey returns the cookie signing key, creating one if needed.
func (r *Router) loadSessionKey() ([]byte, error) {
	var raw string
	if err := r.st.GetSetting(sessionKeySetting, &raw); err != nil {
		return nil, err
	}
	if raw != "" {
		key, err := hex.DecodeString(raw)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := r.st.PutSetting(sessionKeySetting, hex.EncodeToString(key)); err != nil {
		return nil, err
	}
	return key, nil
}

// authGenerationSetting holds a counter that invalidates every outstanding
// session when it changes. Bumping it is how "log out everywhere" and "the
// password changed" take effect on cookies that were already issued.
const authGenerationSetting = "auth.generation"

// generation reads the current session generation. An absent row reads as 0
// without writing one: this is the read path, hit by every request, and the
// writer path (bumpGeneration) creates the row. Reading 0 for "unset" is also
// what makes the very first bump revoke the sessions issued before it, since
// those were signed with 0.
func (r *Router) generation() (int64, error) {
	var g int64
	if err := r.st.GetSetting(authGenerationSetting, &g); err != nil {
		return 0, err
	}
	return g, nil
}

// bumpGeneration invalidates every outstanding session.
func (r *Router) bumpGeneration() error {
	// One atomic statement, so a bump can never be lost to a concurrent read.
	_, err := r.st.BumpSettingInt(authGenerationSetting)
	return err
}

// signSession builds a cookie value of the form base64(username)|expiry|mac.
//
// The username is base64-encoded because a raw username may contain the
// separator: "admin.ops" would otherwise split into four parts and the session
// would never verify, silently locking the operator out.
func signSession(key []byte, username string, gen int64, expiry time.Time) string {
	payload := encodeUsername(username) + sessionSep +
		strconv.FormatInt(expiry.Unix(), 10) + sessionSep + strconv.FormatInt(gen, 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + sessionSep + hex.EncodeToString(mac.Sum(nil))
}

// sessionSep separates the cookie's fields. It cannot appear in a base64 or hex
// encoding, so the payload always splits into exactly three parts.
const sessionSep = "."

func encodeUsername(u string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(u))
}

// verifySession checks a cookie value and returns the username it carries.
// The generation must match the current one, so bumping it revokes every cookie
// that was issued before.
func verifySession(key []byte, value string, currentGen int64) (string, error) {
	parts := strings.Split(value, sessionSep)
	if len(parts) != 4 {
		return "", errors.New("malformed session")
	}
	payload := parts[0] + sessionSep + parts[1] + sessionSep + parts[2]
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[3])) != 1 {
		return "", errors.New("bad signature")
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", errors.New("bad expiry")
	}
	if time.Now().After(time.Unix(expiry, 0)) {
		return "", errors.New("expired")
	}
	gen, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", errors.New("bad generation")
	}
	if gen != currentGen {
		return "", errors.New("session revoked")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("malformed username")
	}
	return string(raw), nil
}

// hashPassword returns a bcrypt hash.
func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// dummyHash is a valid bcrypt hash used to keep login timing constant when the
// username does not exist.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// checkPassword compares a password against its hash. An empty hash (no such
// user) still performs a bcrypt comparison against a dummy hash, so the call
// takes the same time either way and cannot be used to enumerate usernames.
func checkPassword(hash, pw string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// needsSetup reports whether no WebUI admin exists yet.
func (r *Router) needsSetup() (bool, error) {
	n, err := r.st.CountAdmins()
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// EnsureAdminFromEnv creates or updates the WebUI admin from the credential
// configured in the environment, and is meant to run at startup before the
// server accepts requests.
//
// The environment is authoritative, which makes it both the way to provision a
// fresh instance and the way to recover one whose password was lost: change the
// value and restart. It is deliberately idempotent - an unchanged credential
// leaves the stored hash alone, because rewriting it on every restart would
// revoke every session each time the service comes up.
func (r *Router) EnsureAdminFromEnv(c config.AdminCreds) error {
	if !c.Set {
		return nil
	}
	if len(c.Password) < minPasswordLength {
		return fmt.Errorf("%s 至少 %d 位", config.EnvAdminPassword, minPasswordLength)
	}
	if len(c.Password) > 72 {
		return fmt.Errorf("%s 最多 72 字节", config.EnvAdminPassword)
	}

	r.setupMu.Lock()
	defer r.setupMu.Unlock()

	existing, err := r.st.GetAdmin(c.Username)
	switch {
	case err == nil && existing != nil:
		if checkPassword(existing.PasswordHash, c.Password) {
			return nil
		}
		hash, err := hashPassword(c.Password)
		if err != nil {
			return err
		}
		if err := r.st.SetAdminPassword(c.Username, hash); err != nil {
			return err
		}
		// The password changed, so every session issued under the old one must
		// stop working - the same rule the WebUI's change-password applies.
		return r.bumpGeneration()

	case errors.Is(err, store.ErrNotFound):
		hash, err := hashPassword(c.Password)
		if err != nil {
			return err
		}
		// A configured username that differs from an existing one adds an
		// account rather than replacing it, so a typo in the name cannot lock
		// the operator out of the account they were using.
		return r.st.CreateAdmin(c.Username, hash)

	default:
		return err
	}
}

func (r *Router) setSessionCookie(w http.ResponseWriter, req *http.Request, key []byte, username string) error {
	gen, err := r.generation()
	if err != nil {
		return err
	}
	expiry := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    signSession(key, username, gen, expiry),
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		// Forwarded headers are untrusted without an explicit proxy policy.
		Secure:   req.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// currentUser returns the signed-in username, or "" when not signed in.
func (r *Router) currentUser(req *http.Request) string {
	c, err := req.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	key, err := r.loadSessionKey()
	if err != nil {
		return ""
	}
	gen, err := r.generation()
	if err != nil {
		return ""
	}
	name, err := verifySession(key, c.Value, gen)
	if err != nil {
		return ""
	}
	return name
}

// requireAuth guards the API. It also serves setup state to the frontend so it
// knows whether to show the login or the first-run form.
func (r *Router) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.currentUser(req) != "" {
			next(w, req)
			return
		}
		writeErr(w, http.StatusUnauthorized, errors.New("未登录"))
	}
}

// ---------- handlers ----------

// authState reports whether setup is needed and whether the caller is signed in.
// It is reachable without a session: the UI needs it to pick a screen.
func (r *Router) authState(w http.ResponseWriter, req *http.Request) {
	setup, err := r.needsSetup()
	if err != nil {
		// Reachable without a session, so the SQLite error stays in the log.
		writeAuthErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"needs_setup":   setup,
		"authenticated": !setup && r.currentUser(req) != "",
		"username":      r.currentUser(req),
	})
}

// setup creates the first WebUI admin. It is refused once an admin exists, so
// it cannot be used to take over an installed instance.
func (r *Router) setup(w http.ResponseWriter, req *http.Request) {
	if !r.admitAuthAttempt(w, req) {
		return
	}
	// The whole setup is serialised and the "is setup needed" check is repeated
	// inside the lock. Without this, a burst of concurrent requests all observe
	// "no admin yet" and each creates one, which lets an attacker plant an
	// account on a fresh (or freshly reachable) instance.
	r.setupMu.Lock()
	defer r.setupMu.Unlock()

	setup, err := r.needsSetup()
	if err != nil {
		writeAuthErr(w, 500, err)
		return
	}
	if !setup {
		writeErr(w, http.StatusConflict, errors.New("已完成初始化"))
		return
	}

	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" {
		writeErr(w, 400, errors.New("用户名不能为空"))
		return
	}
	if len(in.Password) < minPasswordLength {
		writeErr(w, 400, fmt.Errorf("密码至少 %d 位", minPasswordLength))
		return
	}
	if len(in.Password) > 72 {
		writeErr(w, 400, errors.New("密码最多 72 字节"))
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		writeAuthErr(w, 500, err)
		return
	}
	if err := r.st.CreateAdmin(in.Username, hash); err != nil {
		writeAuthErr(w, 500, err)
		return
	}

	key, err := r.loadSessionKey()
	if err != nil {
		writeAuthErr(w, 500, err)
		return
	}
	if err := r.setSessionCookie(w, req, key, in.Username); err != nil {
		writeAuthErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"username": in.Username})
}

func (r *Router) login(w http.ResponseWriter, req *http.Request) {
	if !r.admitAuthAttempt(w, req) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	// Verification and generation issuance are one critical section shared with
	// password changes, provisioning and logout. Ordinary API reads stay unlocked.
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	a, err := r.st.GetAdmin(strings.TrimSpace(in.Username))
	hash := ""
	if err == nil && a != nil {
		hash = a.PasswordHash
	}
	// Always run bcrypt, even for an unknown username. Otherwise a missing user
	// returns in microseconds while a real one takes ~tens of milliseconds, and
	// the difference enumerates valid usernames despite the identical message.
	ok := checkPassword(hash, in.Password)
	if err != nil || a == nil || !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("用户名或密码错误"))
		return
	}

	key, err := r.loadSessionKey()
	if err != nil {
		writeAuthErr(w, 500, err)
		return
	}
	if err := r.setSessionCookie(w, req, key, a.Username); err != nil {
		writeAuthErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"username": a.Username})
}

func (r *Router) logout(w http.ResponseWriter, req *http.Request) {
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	// Clearing the cookie only affects this browser; bumping the generation is
	// what actually revokes the token, so a copy captured elsewhere stops
	// working too.
	//
	// Only do that for a caller who actually holds a session: the route is
	// reachable without one, and an unauthenticated bump would let anyone sign
	// the operator out of every device at will.
	if r.currentUser(req) != "" {
		if err := r.bumpGeneration(); err != nil {
			writeAuthErr(w, 500, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: req.TLS != nil, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// changePassword updates the signed-in admin's password.
func (r *Router) changePassword(w http.ResponseWriter, req *http.Request) {
	var in struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	// Everything from the credential check to the generation bump is one
	// critical section: it shares the lock with login, so a request that
	// verified the OLD password can no longer acquire the generation issued
	// after this change and walk away with a valid session.
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	a, err := r.st.GetAdmin(r.currentUser(req))
	if err != nil {
		writeErr(w, 401, errors.New("未登录"))
		return
	}
	if !checkPassword(a.PasswordHash, in.Current) {
		writeErr(w, 400, errors.New("当前密码不正确"))
		return
	}
	if len(in.Next) < minPasswordLength {
		writeErr(w, 400, fmt.Errorf("新密码至少 %d 位", minPasswordLength))
		return
	}
	if len(in.Next) > 72 {
		writeErr(w, 400, errors.New("新密码最多 72 字节"))
		return
	}
	hash, err := hashPassword(in.Next)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := r.st.SetAdminPassword(a.Username, hash); err != nil {
		writeErr(w, 500, err)
		return
	}
	// A password change must sign out every existing session, otherwise a
	// stolen cookie survives the one action taken to stop it.
	if err := r.bumpGeneration(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
