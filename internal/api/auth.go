package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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

// signSession builds a cookie value of the form username.expiry.mac.
func signSession(key []byte, username string, expiry time.Time) string {
	payload := username + "." + strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifySession checks a cookie value and returns the username it carries.
func verifySession(key []byte, value string) (string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed session")
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[2])) != 1 {
		return "", errors.New("bad signature")
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", errors.New("bad expiry")
	}
	if time.Now().After(time.Unix(expiry, 0)) {
		return "", errors.New("expired")
	}
	return parts[0], nil
}

// hashPassword returns a bcrypt hash.
func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// checkPassword compares a password against its hash.
func checkPassword(hash, pw string) bool {
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

func (r *Router) setSessionCookie(w http.ResponseWriter, key []byte, username string) {
	expiry := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    signSession(key, username, expiry),
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
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
	name, err := verifySession(key, c.Value)
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
	r.setSessionCookie(w, key, in.Username)
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
	if err != nil || !checkPassword(a.PasswordHash, in.Password) {
		// Same message either way, so the response does not reveal whether the
		// username exists.
		writeErr(w, http.StatusUnauthorized, errors.New("用户名或密码错误"))
		return
	}

	key, err := r.loadSessionKey()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	r.setSessionCookie(w, key, a.Username)
	writeJSON(w, 200, map[string]any{"username": a.Username})
}

func (r *Router) logout(w http.ResponseWriter, _ *http.Request) {
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
	writeJSON(w, 200, map[string]any{"ok": true})
}
