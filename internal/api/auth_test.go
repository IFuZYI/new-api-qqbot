package api

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// newBareServer returns a server with no admin yet, so setup behaviour can be
// exercised from scratch.
func newBareServer(t *testing.T) (*httptest.Server, *store.Store, *http.Client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	srv := httptest.NewServer(NewRouter(st, svc, cfgMgr, t.TempDir()).Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	return srv, st, &http.Client{Jar: jar}
}

func post(t *testing.T, c *http.Client, url, body string) (int, string) {
	t.Helper()
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// Every data endpoint must be closed before setup, or the panel is open to the
// world until someone happens to configure it.
func TestEndpointsRequireAuth(t *testing.T) {
	srv, _, cl := newBareServer(t)

	for _, path := range []string{
		"/api/bootstrap", "/api/config", "/api/groups", "/api/members",
		"/api/bindings", "/api/settings", "/api/checkins",
	} {
		if code := getJSONWith(t, cl, srv.URL+path, nil); code != http.StatusUnauthorized {
			t.Errorf("%s should require auth, got %d", path, code)
		}
	}
	if code, _ := post(t, cl, srv.URL+"/api/actions/award", `{}`); code != http.StatusUnauthorized {
		t.Errorf("actions should require auth, got %d", code)
	}
}

func TestAuthStateReportsSetupNeeded(t *testing.T) {
	srv, _, cl := newBareServer(t)

	var got struct {
		NeedsSetup    bool `json:"needs_setup"`
		Authenticated bool `json:"authenticated"`
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/auth/state", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if !got.NeedsSetup || got.Authenticated {
		t.Errorf("unexpected state: %+v", got)
	}
}

func TestSetupCreatesAdminAndSignsIn(t *testing.T) {
	srv, st, cl := newBareServer(t)

	code, _ := post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`)
	if code != 200 {
		t.Fatalf("setup status %d", code)
	}

	// The password must be stored hashed, never in the clear.
	a, err := st.GetAdmin("admin")
	if err != nil {
		t.Fatalf("GetAdmin: %v", err)
	}
	if a.PasswordHash == "hunter2hunter2" || a.PasswordHash == "" {
		t.Error("password was not hashed")
	}
	if !strings.HasPrefix(a.PasswordHash, "$2") {
		t.Errorf("expected a bcrypt hash, got %q", a.PasswordHash)
	}

	// The response set a session, so the data endpoints now work.
	if code := getJSONWith(t, cl, srv.URL+"/api/bootstrap", nil); code != 200 {
		t.Errorf("bootstrap after setup: %d", code)
	}
}

// Setup must not be a way to take over an instance that already has an admin.
func TestSetupRefusedOnceAdminExists(t *testing.T) {
	srv, _, cl := newBareServer(t)
	if code, _ := post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`); code != 200 {
		t.Fatalf("initial setup failed: %d", code)
	}

	// A fresh client, as an attacker would be.
	jar, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar}
	code, _ := post(t, other, srv.URL+"/api/auth/setup", `{"username":"attacker","password":"attacker12345"}`)
	if code != http.StatusConflict {
		t.Errorf("second setup should be refused, got %d", code)
	}
	if code := getJSONWith(t, other, srv.URL+"/api/bootstrap", nil); code != http.StatusUnauthorized {
		t.Errorf("attacker should not be authenticated, got %d", code)
	}
}

func TestSetupRejectsShortPassword(t *testing.T) {
	srv, _, cl := newBareServer(t)
	if code, _ := post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"short"}`); code != 400 {
		t.Errorf("short password should be rejected, got %d", code)
	}
}

func TestLoginAndLogout(t *testing.T) {
	srv, _, _ := newBareServer(t)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`)

	// A wrong password must fail with the same message as an unknown user.
	jar2, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar2}
	code, body := post(t, other, srv.URL+"/api/auth/login", `{"username":"admin","password":"wrong-password"}`)
	if code != http.StatusUnauthorized {
		t.Errorf("wrong password should be 401, got %d", code)
	}
	code2, body2 := post(t, other, srv.URL+"/api/auth/login", `{"username":"nobody","password":"wrong-password"}`)
	if code2 != http.StatusUnauthorized || body != body2 {
		t.Errorf("responses should not distinguish unknown user from wrong password: %d/%q vs %d/%q", code, body, code2, body2)
	}

	// The correct password signs in.
	code3, _ := post(t, other, srv.URL+"/api/auth/login", `{"username":"admin","password":"hunter2hunter2"}`)
	if code3 != 200 {
		t.Fatalf("login failed: %d", code3)
	}
	if code := getJSONWith(t, other, srv.URL+"/api/bootstrap", nil); code != 200 {
		t.Errorf("should be authenticated after login: %d", code)
	}

	// Logging out invalidates the session.
	post(t, other, srv.URL+"/api/auth/logout", `{}`)
	if code := getJSONWith(t, other, srv.URL+"/api/bootstrap", nil); code != http.StatusUnauthorized {
		t.Errorf("should be signed out: %d", code)
	}
}

// A tampered cookie must not authenticate.
func TestForgedSessionRejected(t *testing.T) {
	srv, _, _ := newBareServer(t)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`)

	u, _ := http.NewRequest("GET", srv.URL+"/api/bootstrap", nil)
	u.AddCookie(&http.Cookie{Name: sessionCookie, Value: "admin.9999999999.deadbeef"})
	resp, err := http.DefaultClient.Do(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a forged cookie must not authenticate, got %d", resp.StatusCode)
	}
}

func TestChangePassword(t *testing.T) {
	srv, _, _ := newBareServer(t)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`)

	if code, _ := post(t, cl, srv.URL+"/api/auth/password", `{"current":"wrong","next":"newpassword1"}`); code != 400 {
		t.Errorf("wrong current password should be rejected, got %d", code)
	}
	if code, _ := post(t, cl, srv.URL+"/api/auth/password", `{"current":"hunter2hunter2","next":"newpassword1"}`); code != 200 {
		t.Errorf("password change failed: %d", code)
	}

	jar2, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar2}
	if code, _ := post(t, other, srv.URL+"/api/auth/login", `{"username":"admin","password":"hunter2hunter2"}`); code != http.StatusUnauthorized {
		t.Error("the old password should no longer work")
	}
	if code, _ := post(t, other, srv.URL+"/api/auth/login", `{"username":"admin","password":"newpassword1"}`); code != 200 {
		t.Error("the new password should work")
	}
}

// A captured session must stop working after logout: clearing the cookie only
// affects the caller's browser.
func TestLogoutRevokesTheSessionServerSide(t *testing.T) {
	srv, _, _ := newBareServer(t)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`)

	// Capture the cookie, as an attacker who stole it would have.
	var stolen string
	for _, c := range jar.Cookies(mustParse(t, srv.URL)) {
		if c.Name == sessionCookie {
			stolen = c.Value
		}
	}
	if stolen == "" {
		t.Fatal("no session cookie issued")
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/bootstrap", nil); code != 200 {
		t.Fatal("session should work before logout")
	}

	post(t, cl, srv.URL+"/api/auth/logout", `{}`)

	// Replay the captured value on a fresh client.
	req, _ := http.NewRequest("GET", srv.URL+"/api/bootstrap", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: stolen})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a captured cookie still authenticates after logout (%d)", resp.StatusCode)
	}
}

// Changing the password must sign out existing sessions, otherwise a stolen
// cookie survives the one action taken to stop it.
func TestPasswordChangeRevokesSessions(t *testing.T) {
	srv, _, _ := newBareServer(t)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`)

	var stolen string
	for _, c := range jar.Cookies(mustParse(t, srv.URL)) {
		if c.Name == sessionCookie {
			stolen = c.Value
		}
	}

	if code, _ := post(t, cl, srv.URL+"/api/auth/password", `{"current":"hunter2hunter2","next":"newpassword1"}`); code != 200 {
		t.Fatalf("password change failed: %d", code)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/bootstrap", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: stolen})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a pre-change cookie still authenticates (%d)", resp.StatusCode)
	}
}

// A username containing a dot used to produce a cookie that never verified,
// locking the operator out silently.
func TestUsernameWithDotCanLogIn(t *testing.T) {
	for _, name := range []string{"admin.ops", "first.last", "a.b.c"} {
		srv, _, _ := newBareServer(t)
		jar, _ := cookiejar.New(nil)
		cl := &http.Client{Jar: jar}

		code, _ := post(t, cl, srv.URL+"/api/auth/setup",
			`{"username":"`+name+`","password":"hunter2hunter2"}`)
		if code != 200 {
			t.Fatalf("%s: setup %d", name, code)
		}
		if code := getJSONWith(t, cl, srv.URL+"/api/bootstrap", nil); code != 200 {
			t.Errorf("%s: the session issued by setup was rejected (%d)", name, code)
		}

		// And a fresh login works too.
		jar2, _ := cookiejar.New(nil)
		cl2 := &http.Client{Jar: jar2}
		if code, _ := post(t, cl2, srv.URL+"/api/auth/login",
			`{"username":"`+name+`","password":"hunter2hunter2"}`); code != 200 {
			t.Fatalf("%s: login %d", name, code)
		}
		if code := getJSONWith(t, cl2, srv.URL+"/api/bootstrap", nil); code != 200 {
			t.Errorf("%s: login returned 200 but the session is rejected (%d)", name, code)
		}
	}
}

// Concurrent setup requests must not each create an admin.
func TestConcurrentSetupCreatesOneAdmin(t *testing.T) {
	srv, st, _ := newBareServer(t)

	var wg sync.WaitGroup
	var ok int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			jar, _ := cookiejar.New(nil)
			cl := &http.Client{Jar: jar}
			code, _ := post(t, cl, srv.URL+"/api/auth/setup",
				`{"username":"admin`+strconv.Itoa(i)+`","password":"hunter2hunter2"}`)
			if code == 200 {
				atomic.AddInt32(&ok, 1)
			}
		}(i)
	}
	wg.Wait()

	n, err := st.CountAdmins()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("concurrent setup created %d admins (want 1)", n)
	}
	if ok != 1 {
		t.Errorf("%d setup requests reported success (want 1)", ok)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// An unauthenticated caller must not be able to sign the operator out: the
// logout route is reachable without a session, so bumping the generation there
// would be a credential-free denial of service.
func TestUnauthenticatedLogoutDoesNotRevokeTheOperator(t *testing.T) {
	srv, _, _ := newBareServer(t)

	// The operator signs in.
	jar, _ := cookiejar.New(nil)
	op := &http.Client{Jar: jar}
	post(t, op, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`)
	if code := getJSONWith(t, op, srv.URL+"/api/bootstrap", nil); code != 200 {
		t.Fatal("operator should be signed in")
	}

	// An attacker with no session calls logout.
	anon, _ := cookiejar.New(nil)
	if code, _ := post(t, &http.Client{Jar: anon}, srv.URL+"/api/auth/logout", `{}`); code != 200 {
		t.Fatalf("logout should still answer, got %d", code)
	}

	if code := getJSONWith(t, op, srv.URL+"/api/bootstrap", nil); code != 200 {
		t.Errorf("an unauthenticated logout revoked the operator's session (%d)", code)
	}
}

// The generation must move forward exactly once per authenticated logout, even
// when the row does not exist yet: a lost initialiser would let a cookie issued
// in that window survive the logout it raced with.
func TestGenerationBumpIsNotLostOnInit(t *testing.T) {
	srv, st, _ := newBareServer(t)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = st.BumpSettingInt("auth.generation")
		}()
	}
	wg.Wait()

	var g int64
	if err := st.GetSetting("auth.generation", &g); err != nil {
		t.Fatal(err)
	}
	if g != 16 {
		t.Errorf("generation = %d after 16 concurrent bumps, want 16 (an increment was lost)", g)
	}
	_ = srv
}

// A cookie signed before any generation existed must stop working once the
// generation is first bumped.
func TestFirstBumpInvalidatesPreExistingCookies(t *testing.T) {
	srv, st, _ := newBareServer(t)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"hunter2hunter2"}`)

	var stolen string
	for _, c := range jar.Cookies(mustParse(t, srv.URL)) {
		if c.Name == sessionCookie {
			stolen = c.Value
		}
	}
	if stolen == "" {
		t.Fatal("no cookie issued")
	}
	// Force the row into existence via a bump, as an authenticated logout would.
	if _, err := st.BumpSettingInt("auth.generation"); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/bootstrap", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: stolen})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a cookie from before the first bump still works (%d)", resp.StatusCode)
	}
}

// ---------- admin provisioned from the environment ----------

// newBareRouter is newBareServer plus the Router itself, so startup behaviour
// (which runs before any HTTP request) can be exercised directly.
func newBareRouter(t *testing.T) (*Router, *store.Store, *httptest.Server, *http.Client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	r := NewRouter(st, svc, cfgMgr, t.TempDir())
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	return r, st, srv, &http.Client{Jar: jar}
}

func TestEnsureAdminFromEnvCreatesTheAdmin(t *testing.T) {
	r, st, srv, cl := newBareRouter(t)

	if err := r.EnsureAdminFromEnv(config.AdminCreds{
		Set: true, Username: "admin", Password: "hunter2hunter2",
	}); err != nil {
		t.Fatal(err)
	}

	a, err := st.GetAdmin("admin")
	if err != nil {
		t.Fatalf("the admin was not created: %v", err)
	}
	if !checkPassword(a.PasswordHash, "hunter2hunter2") {
		t.Error("the stored hash does not match the configured password")
	}

	// The panel must not ask for first-run setup any more.
	var got struct {
		NeedsSetup bool `json:"needs_setup"`
	}
	getJSONWith(t, cl, srv.URL+"/api/auth/state", &got)
	if got.NeedsSetup {
		t.Error("setup should not be needed once the env admin exists")
	}

	// And the configured credential must actually sign in.
	if code, _ := post(t, cl, srv.URL+"/api/auth/login",
		`{"username":"admin","password":"hunter2hunter2"}`); code != 200 {
		t.Errorf("login with the configured credential failed: %d", code)
	}
}

// The env value is authoritative: changing it and restarting must reset the
// password, which is the documented recovery path when it is lost.
func TestEnsureAdminFromEnvResetsAnExistingPassword(t *testing.T) {
	r, st, _, _ := newBareRouter(t)

	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "oldpassword1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "newpassword2"}); err != nil {
		t.Fatal(err)
	}

	a, _ := st.GetAdmin("admin")
	if !checkPassword(a.PasswordHash, "newpassword2") {
		t.Error("the password was not reset to the configured value")
	}
	if checkPassword(a.PasswordHash, "oldpassword1") {
		t.Error("the old password still works")
	}
	if n, _ := st.CountAdmins(); n != 1 {
		t.Errorf("admin count = %d, want 1 (it must not create a second account)", n)
	}
}

// Re-applying the same credential must be a no-op: every restart runs this, and
// rewriting the hash each time would revoke every session on every restart.
func TestEnsureAdminFromEnvIsIdempotent(t *testing.T) {
	r, st, srv, cl := newBareRouter(t)

	creds := config.AdminCreds{Set: true, Username: "admin", Password: "hunter2hunter2"}
	if err := r.EnsureAdminFromEnv(creds); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetAdmin("admin")

	if code, _ := post(t, cl, srv.URL+"/api/auth/login",
		`{"username":"admin","password":"hunter2hunter2"}`); code != 200 {
		t.Fatal("login should work")
	}
	if err := r.EnsureAdminFromEnv(creds); err != nil {
		t.Fatal(err)
	}

	after, _ := st.GetAdmin("admin")
	if before.PasswordHash != after.PasswordHash {
		t.Error("an unchanged credential rewrote the hash, which would revoke sessions")
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/bootstrap", nil); code != 200 {
		t.Errorf("the session was invalidated by an unchanged env credential (%d)", code)
	}
}

// A different configured username must not silently lock the existing account
// out: it is added, and both keep working.
func TestEnsureAdminFromEnvWithADifferentUsernameKeepsTheOldOne(t *testing.T) {
	r, st, _, _ := newBareRouter(t)

	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "hunter2hunter2"}); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "root", Password: "hunter2hunter2"}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.GetAdmin("admin"); err != nil {
		t.Error("the original admin was removed")
	}
	if _, err := st.GetAdmin("root"); err != nil {
		t.Error("the newly configured admin was not created")
	}
}

// An unset credential must leave whatever the WebUI set up alone.
func TestEnsureAdminFromEnvUnsetIsANoOp(t *testing.T) {
	r, st, _, _ := newBareRouter(t)

	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "hunter2hunter2"}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetAdmin("admin")

	if err := r.EnsureAdminFromEnv(config.AdminCreds{}); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetAdmin("admin")
	if err != nil {
		t.Fatal(err)
	}
	if before.PasswordHash != after.PasswordHash {
		t.Error("an unset credential changed the stored password")
	}
}

// A too-short configured password is refused rather than quietly weakening the
// panel's only gate.
func TestEnsureAdminFromEnvRejectsAShortPassword(t *testing.T) {
	r, st, _, _ := newBareRouter(t)

	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "short"}); err == nil {
		t.Error("a password below the minimum length must be refused")
	}
	if n, _ := st.CountAdmins(); n != 0 {
		t.Errorf("a rejected credential created %d admins", n)
	}
}

// Resetting the password must invalidate sessions issued before it, which is
// how a leaked cookie stops working.
func TestEnsureAdminFromEnvResetRevokesSessions(t *testing.T) {
	r, _, srv, cl := newBareRouter(t)

	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "hunter2hunter2"}); err != nil {
		t.Fatal(err)
	}
	post(t, cl, srv.URL+"/api/auth/login", `{"username":"admin","password":"hunter2hunter2"}`)

	var stolen string
	for _, c := range cl.Jar.(*cookiejar.Jar).Cookies(mustParse(t, srv.URL)) {
		if c.Name == sessionCookie {
			stolen = c.Value
		}
	}
	if stolen == "" {
		t.Fatal("no session cookie issued")
	}

	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "rotated-pw-9"}); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/bootstrap", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: stolen})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a cookie from before the reset still works (%d)", resp.StatusCode)
	}
}
