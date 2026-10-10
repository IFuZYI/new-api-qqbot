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
)

// sessionCookie is the WebUI's session cookie.
const sessionCookie = "qqbot_session"

// sessionTTL is how long a WebUI login stays valid.
const sessionTTL = 7 * 24 * time.Hour

// minPasswordLength is enforced at setup and when changing a password.
const minPasswordLength = 8

// setupMu serialises first-run setup, which is otherwise a check-then-create
// race that concurrent requests can win several times.
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

// generation reads the current session generation, creating it on first use.
func (r *Router) generation() (int64, error) {
	var g int64
	if err := r.st.GetSetting(authGenerationSetting, &g); err != nil {
		return 0, err
	}
	if g == 0 {
		g = 1
		if err := r.st.PutSetting(authGenerationSetting, g); err != nil {
			return 0, err
		}
	}
	return g, nil
}

// bumpGeneration invalidates every outstanding session.
func (r *Router) bumpGeneration() error {
	g, err := r.generation()
	if err != nil {
		return err
	}
	return r.st.PutSetting(authGenerationSetting, g+1)
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

func (r *Router) setSessionCookie(w http.ResponseWriter, key []byte, username string) error {
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
		writeErr(w, 500, err)
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
	// The whole setup is serialised and the "is setup needed" check is repeated
	// inside the lock. Without this, a burst of concurrent requests all observe
	// "no admin yet" and each creates one, which lets an attacker plant an
	// account on a fresh (or freshly reachable) instance.
	r.setupMu.Lock()
	defer r.setupMu.Unlock()

	setup, err := r.needsSetup()
	if err != nil {
		writeErr(w, 500, err)
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
	hash, err := hashPassword(in.Password)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := r.st.CreateAdmin(in.Username, hash); err != nil {
		writeErr(w, 500, err)
		return
	}

	key, err := r.loadSessionKey()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := r.setSessionCookie(w, key, in.Username); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"username": in.Username})
}

func (r *Router) login(w http.ResponseWriter, req *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}

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
		writeErr(w, 500, err)
		return
	}
	if err := r.setSessionCookie(w, key, a.Username); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"username": a.Username})
}

func (r *Router) logout(w http.ResponseWriter, _ *http.Request) {
	// Clearing the cookie only affects this browser; bumping the generation is
	// what actually revokes the token, so a copy captured elsewhere stops
	// working too.
	if err := r.bumpGeneration(); err != nil {
		writeErr(w, 500, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// changePassword updates the signed-in admin's password.
func (r *Router) changePassword(w http.ResponseWriter, req *http.Request) {
	username := r.currentUser(req)
	a, err := r.st.GetAdmin(username)
	if err != nil {
		writeErr(w, 401, errors.New("未登录"))
		return
	}
	var in struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
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
