package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

type fakeGateway struct{}

func (fakeGateway) FindUserByEmail(addr string) (*newapi.User, error) { return nil, nil }
func (fakeGateway) GetUser(id int) (*newapi.User, error) {
	return &newapi.User{ID: id, Username: "u", Group: "default"}, nil
}
func (fakeGateway) SetGroup(u *newapi.User, group string) error { return nil }
func (fakeGateway) AddQuota(userID, value int) error            { return nil }
func (fakeGateway) Groups() (map[string]struct{}, error) {
	return map[string]struct{}{"default": {}, "authorized": {}, "vip": {}}, nil
}
func (fakeGateway) ListUsers() ([]newapi.User, error) { return nil, nil }

type fakeQQ struct{}

func (fakeQQ) GetGroupList() ([]napcat.Group, error) {
	return []napcat.Group{{GroupID: 1129535731, GroupName: "FuZY、ラッキー、fuzy", MemberCount: 3}}, nil
}
func (fakeQQ) GetGroupMemberList(groupID int64) ([]napcat.GroupMember, error) {
	return []napcat.GroupMember{{QQ: 54321, Nickname: "alice", Card: "爱丽丝"}}, nil
}
func (fakeQQ) GetGroupSignedList(groupID int64) ([]napcat.SignedMember, error) {
	return []napcat.SignedMember{{QQ: 54321, Nick: "爱丽丝", Rank: 1, Time: 1791630000}}, nil
}

// stubPatrols is the scheduler's status shape, without a scheduler.
type stubPatrols struct{}

func (stubPatrols) Status() map[string]any {
	out := map[string]any{}
	for _, name := range []string{"members", "bind", "checkins", "rules"} {
		out[name] = map[string]any{}
	}
	return out
}

// testEnv is an authenticated test server: the cookie jar carries the session
// created by first-run setup.
type testEnv struct {
	srv    *httptest.Server
	st     *store.Store
	client *http.Client
	cfg    *config.Manager
	router *Router
}

func newTestServer(t *testing.T) (*httptest.Server, *store.Store, *http.Client) {
	t.Helper()
	env := newTestEnv(t)
	return env.srv, env.st, env.client
}

// newTestServerWithConfig also returns the config manager, for tests that check
// what landed on disk.
func newTestServerWithConfig(t *testing.T) (*httptest.Server, *store.Store, *http.Client, *config.Manager) {
	t.Helper()
	env := newTestEnv(t)
	return env.srv, env.st, env.client, env.cfg
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	router := NewRouter(st, svc, cfgMgr, t.TempDir())
	// The scheduler is not started in tests, so its status is injected: the
	// endpoint must work without one, and must report the patrol names.
	router.SetPatrolStatus(stubPatrols{})
	srv := httptest.NewServer(router.Handler())
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}

	// First-run setup also signs in, so the jar holds a usable session.
	resp, err := client.Post(srv.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"username":"admin","password":"test-password"}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("setup failed with %d", resp.StatusCode)
	}

	return &testEnv{srv: srv, st: st, client: client, cfg: cfgMgr, router: router}
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	return getJSONWith(t, http.DefaultClient, url, out)
}

func getJSONWith(t *testing.T, c *http.Client, url string, out any) int {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func postJSON(t *testing.T, url string, body any, out any) int {
	t.Helper()
	return postJSONWith(t, http.DefaultClient, url, body, out)
}

func putJSONWith(t *testing.T, c *http.Client, url string, body any, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func postJSONWith(t *testing.T, c *http.Client, url string, body any, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := c.Post(url, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

// The UI drives everything off these two calls, so they must work before
// anything is configured.
func TestBootstrapReportsUnconfigured(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var got struct {
		Configured bool             `json:"configured"`
		Settings   service.Settings `json:"settings"`
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/bootstrap", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Configured {
		t.Error("a fresh install must report itself as unconfigured")
	}
	if got.Settings.RewardMin != 100 || got.Settings.RewardMax != 200 {
		t.Errorf("settings should be returned with defaults: %+v", got.Settings)
	}
}

func TestConfigRoundTripHidesSecrets(t *testing.T) {
	srv, _, cl, cfgMgr := newTestServerWithConfig(t)

	in := config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "secret-pw"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "webui-tok"},
	}
	var saved cfgResponse
	if code := postJSONWith(t, cl, srv.URL+"/api/config", cfgRequest{Config: in}, &saved); code != 200 {
		t.Fatalf("save status %d", code)
	}
	if saved.Config.NewAPI.Password != config.RedactedPassword {
		t.Errorf("password must not be echoed back: %q", saved.Config.NewAPI.Password)
	}

	var got cfgResponse
	getJSONWith(t, cl, srv.URL+"/api/config", &got)
	if got.Config.NewAPI.Password != config.RedactedPassword {
		t.Errorf("GET must redact the password: %q", got.Config.NewAPI.Password)
	}
	if got.Revision == "" {
		t.Error("GET must return a revision so a stale save can be refused")
	}

	// The real secret must still be on disk, unchanged.
	stored := cfgMgr.MustLoad()
	if stored.NewAPI.Password != "secret-pw" {
		t.Errorf("stored password was clobbered: %q", stored.NewAPI.Password)
	}
}

// Saving the form without retyping the password must not erase it.
func TestConfigSaveKeepsPasswordWhenRedactedPlaceholderSent(t *testing.T) {
	srv, _, cl, cfgMgr := newTestServerWithConfig(t)

	postJSONWith(t, cl, srv.URL+"/api/config", cfgRequest{Config: config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "real-pw"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "tok"},
	}}, nil)

	var got cfgResponse
	getJSONWith(t, cl, srv.URL+"/api/config", &got)
	edited := got.Config
	edited.NewAPI.URL = "https://other.example.com" // operator edits only the URL
	postJSONWith(t, cl, srv.URL+"/api/config", cfgRequest{Config: edited, Revision: got.Revision}, nil)

	stored := cfgMgr.MustLoad()
	if stored.NewAPI.Password != "real-pw" {
		t.Errorf("password was wiped by a redacted round trip: %q", stored.NewAPI.Password)
	}
	if stored.NewAPI.URL != "https://other.example.com" {
		t.Errorf("URL edit was lost: %q", stored.NewAPI.URL)
	}
}

func TestConfigRejectsInvalidInput(t *testing.T) {
	srv, _, cl := newTestServer(t)
	bad := config.Config{
		NewAPI: config.NewAPI{URL: "not-a-url", Username: "u", Password: "p"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "t"},
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/config", cfgRequest{Config: bad}, nil); code == 200 {
		t.Error("an invalid URL must be rejected")
	}
}

func TestGroupsRefreshStoresSelection(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var groups []store.MonitoredGroup
	if code := postJSONWith(t, cl, srv.URL+"/api/groups/refresh", map[string]any{}, &groups); code != 200 {
		t.Fatalf("refresh status %d", code)
	}
	if len(groups) != 1 || groups[0].GroupID != 1129535731 {
		t.Fatalf("unexpected groups: %+v", groups)
	}
	// A newly discovered group is stored disabled: the bot can be invited into
	// unrelated groups, and monitoring one means recording its members and
	// paying them, so the operator opts in explicitly.
	if groups[0].Enabled {
		t.Error("a freshly discovered group must not be monitored by default")
	}
}

func TestGroupSelectionPersists(t *testing.T) {
	srv, _, cl := newTestServer(t)
	postJSONWith(t, cl, srv.URL+"/api/groups/refresh", map[string]any{}, nil)

	var groups []store.MonitoredGroup
	if code := postJSONWith(t, cl, srv.URL+"/api/groups", map[string]any{
		"group_ids": []int64{},
	}, &groups); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(groups) != 1 || groups[0].Enabled {
		t.Errorf("deselecting should disable the group: %+v", groups)
	}
}

func TestMembersEndpointIncludesBindingState(t *testing.T) {
	srv, st, cl := newTestServer(t)
	postJSONWith(t, cl, srv.URL+"/api/groups/refresh", map[string]any{}, nil)
	if err := st.UpsertMembers(1129535731, []store.Member{{QQ: 54321, Nickname: "爱丽丝"}}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Items []struct {
			QQ       int64  `json:"qq"`
			Nickname string `json:"nickname"`
			Bound    bool   `json:"bound"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/members", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Total != 1 || got.Items[0].QQ != 54321 || got.Items[0].Bound {
		t.Errorf("unexpected members payload: %+v", got)
	}

	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, NewAPIUsername: "seven"}); err != nil {
		t.Fatal(err)
	}
	getJSONWith(t, cl, srv.URL+"/api/members", &got)
	if !got.Items[0].Bound {
		t.Error("binding state not reflected in the member list")
	}
}

func TestManualBindAndUnbind(t *testing.T) {
	srv, st, cl := newTestServer(t)
	if err := st.UpsertMembers(1, []store.Member{{QQ: 54321, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}

	var resp struct {
		Binding store.Binding `json:"binding"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/bindings", map[string]any{
		"qq": 54321, "newapi_user_id": 42, "newapi_username": "forty-two",
	}, &resp); code != 200 {
		t.Fatalf("bind status %d", code)
	}
	if resp.Binding.NewAPIUserID != 42 || resp.Binding.Source != "manual" {
		t.Errorf("unexpected binding: %+v", resp.Binding)
	}

	if code := postJSONWith(t, cl, srv.URL+"/api/bindings/54321/delete", map[string]any{}, nil); code != 200 {
		t.Fatalf("unbind status %d", code)
	}
	if _, err := st.GetBinding(54321); err == nil {
		t.Error("binding should be gone after unbind")
	}
}

func TestSettingsEndpointValidates(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var s service.Settings
	getJSONWith(t, cl, srv.URL+"/api/settings", &s)
	if s.RewardMin != 100 || s.RewardMax != 200 {
		t.Fatalf("unexpected defaults: %+v", s)
	}

	s.RewardMin, s.RewardMax = 500, 100
	if code := postJSONWith(t, cl, srv.URL+"/api/settings", s, nil); code == 200 {
		t.Error("min > max must be rejected")
	}
	s.RewardMin, s.RewardMax = 100, 500
	if code := postJSONWith(t, cl, srv.URL+"/api/settings", s, nil); code != 200 {
		t.Errorf("valid settings rejected, status %d", code)
	}
}

func TestActionsRunTheSweeps(t *testing.T) {
	srv, _, cl := newTestServer(t)
	postJSONWith(t, cl, srv.URL+"/api/groups/refresh", map[string]any{}, nil)
	// A freshly discovered group is disabled, so opt it in: this test is about
	// the sweeps, not about the default.
	postJSONWith(t, cl, srv.URL+"/api/groups", map[string]any{
		"group_ids": []int64{1129535731},
	}, nil)

	var res struct {
		Members int `json:"members"`
		Bound   int `json:"bound"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/actions/sync", map[string]any{}, &res); code != 200 {
		t.Fatalf("sync status %d", code)
	}
	if res.Members != 1 {
		t.Errorf("sync should have stored the roster: %+v", res)
	}

	if code := postJSONWith(t, cl, srv.URL+"/api/actions/bind", map[string]any{}, nil); code != 200 {
		t.Errorf("bind action status %d", code)
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/actions/promote", map[string]any{}, nil); code != 200 {
		t.Errorf("promote action status %d", code)
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/actions/award", map[string]any{}, nil); code != 200 {
		t.Errorf("award action status %d", code)
	}
}

func TestUnknownActionIsNotFound(t *testing.T) {
	srv, _, cl := newTestServer(t)
	if code := postJSONWith(t, cl, srv.URL+"/api/actions/nope", map[string]any{}, nil); code != 404 {
		t.Errorf("want 404, got %d", code)
	}
}

func TestCheckinsEndpoint(t *testing.T) {
	srv, st, cl := newTestServer(t)
	if err := st.RecordCheckin(store.Checkin{QQ: 54321, Date: "2026-10-10", RewardQuota: 150}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items []store.Checkin `json:"items"`
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/checkins?date=2026-10-10", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Items) != 1 || got.Items[0].RewardQuota != 150 {
		t.Errorf("unexpected: %+v", got.Items)
	}
}

// Saving through the WebUI must also write the config file, so the two copies
// never drift and the file is usable as a provisioning source.
func TestConfigSaveWritesTheFile(t *testing.T) {
	srv, _, cl, cfgMgr := newTestServerWithConfig(t)

	postJSONWith(t, cl, srv.URL+"/api/config", cfgRequest{Config: config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "pw-123"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "wt"},
	}}, nil)

	raw, err := os.ReadFile(cfgMgr.Path())
	if err != nil {
		t.Fatalf("config file was not written by the WebUI save: %v", err)
	}
	if !strings.Contains(string(raw), "api.fuzy.site") {
		t.Errorf("file does not hold the saved config:\n%s", raw)
	}

	// And the file must load back to the same values.
	got := cfgMgr.MustLoad()
	if got.NewAPI.Username != "napcat" || got.NewAPI.Password != "pw-123" {
		t.Errorf("round trip through the file failed: %+v", got.NewAPI)
	}
}

// cfgRequest/cfgResponse mirror the connection-config envelope, which carries a
// revision so a stale form cannot clobber a concurrent change.
type cfgRequest struct {
	Config   config.Config `json:"config"`
	Revision string        `json:"revision"`
}

type cfgResponse struct {
	Config   config.Config `json:"config"`
	Revision string        `json:"revision"`
}

// A save based on a stale view must be refused, so a long-lived tab cannot
// overwrite a hand edit (or another tab's save) with values it never touched.
func TestStaleConfigSaveIsRefused(t *testing.T) {
	srv, _, cl, cfgMgr := newTestServerWithConfig(t)

	postJSONWith(t, cl, srv.URL+"/api/config", cfgRequest{Config: config.Config{
		NewAPI: config.NewAPI{URL: "https://one.example.com", Username: "u", Password: "p"},
		NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3002"},
	}}, nil)

	var first cfgResponse
	getJSONWith(t, cl, srv.URL+"/api/config", &first)
	if first.Revision == "" {
		t.Fatal("no revision returned")
	}

	// Somebody else changes the config (a hand edit to the file).
	if err := cfgMgr.Save(config.Config{
		NewAPI: config.NewAPI{URL: "https://two.example.com", Username: "u", Password: "p"},
		NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3002"},
	}); err != nil {
		t.Fatal(err)
	}

	// The stale tab now saves, having edited nothing relevant.
	stale := first.Config
	code := postJSONWith(t, cl, srv.URL+"/api/config",
		cfgRequest{Config: stale, Revision: first.Revision}, nil)
	if code != http.StatusConflict {
		t.Errorf("a stale save must be refused with 409, got %d", code)
	}
	if got := cfgMgr.MustLoad().NewAPI.URL; got != "https://two.example.com" {
		t.Errorf("the concurrent change was overwritten: %q", got)
	}

	// Reloading and saving again must work.
	var fresh cfgResponse
	getJSONWith(t, cl, srv.URL+"/api/config", &fresh)
	edited := fresh.Config
	edited.NewAPI.Username = "edited"
	if code := postJSONWith(t, cl, srv.URL+"/api/config",
		cfgRequest{Config: edited, Revision: fresh.Revision}, nil); code != 200 {
		t.Errorf("a save with the current revision must succeed, got %d", code)
	}
}

// cfgSectionRequest/cfgSectionResponse mirror the section-scoped save envelope.
type cfgSectionRequest struct {
	Config   config.Config `json:"config"`
	Revision string        `json:"revision"`
}

type cfgSectionResponse struct {
	Config    config.Config     `json:"config"`
	Revision  string            `json:"revision"`
	Revisions map[string]string `json:"revisions"`
}

// Saving the gateway must not touch the NapCat half, and vice versa: the two
// connection cards are independent so a half-filled form cannot clobber the
// other side.
func TestConfigSectionSaveIsIndependent(t *testing.T) {
	srv, _, cl, cfgMgr := newTestServerWithConfig(t)

	postJSONWith(t, cl, srv.URL+"/api/config", cfgRequest{Config: config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "pw"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "wt"},
	}}, nil)

	// Save only the gateway (as the new-api card does).
	if code := postJSONWith(t, cl, srv.URL+"/api/config/newapi", cfgSectionRequest{
		Config: config.Config{NewAPI: config.NewAPI{
			URL: "https://new.example.com", Username: "u2", Password: "pw2",
		}},
	}, nil); code != 200 {
		t.Fatalf("section save status %d", code)
	}

	got := cfgMgr.MustLoad()
	if got.NewAPI.URL != "https://new.example.com" {
		t.Errorf("gateway not saved: %q", got.NewAPI.URL)
	}
	if got.NapCat.WebUIURL != "http://127.0.0.1:6099" || got.NapCat.WebUIToken != "wt" {
		t.Errorf("NapCat half clobbered by a gateway save: %+v", got.NapCat)
	}
}

// GET /api/config must return a revision per section, so each card can detect a
// change to its own half without being disturbed by the other.
func TestConfigReturnsSectionRevisions(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var got cfgSectionResponse
	if code := getJSONWith(t, cl, srv.URL+"/api/config", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Revisions[config.SectionNewAPI] == "" || got.Revisions[config.SectionNapCat] == "" {
		t.Errorf("both section revisions must be present: %+v", got.Revisions)
	}
	if got.Revisions[config.SectionNewAPI] == got.Revisions[config.SectionNapCat] {
		t.Error("the two section revisions must differ")
	}
}

// An empty revision means "no guard". That is deliberate: a client that does
// not track revisions (or an operator's script) may still save. Pin it so a
// future change that made the revision mandatory cannot land silently — the
// WebUI always echoes one, so only a non-browser caller is affected.
func TestSectionSaveWithoutRevisionIsAllowed(t *testing.T) {
	srv, _, cl := newTestServer(t)

	// No revision field at all.
	if code := postJSONWith(t, cl, srv.URL+"/api/config/napcat", cfgSectionRequest{
		Config: config.Config{NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3002"}},
	}, nil); code != 200 {
		t.Errorf("a section save with no revision must be accepted, got %d", code)
	}

	// Explicitly empty is the same thing.
	if code := postJSONWith(t, cl, srv.URL+"/api/config/napcat", cfgSectionRequest{
		Config:   config.Config{NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3003"}},
		Revision: "",
	}, nil); code != 200 {
		t.Errorf("a section save with an empty revision must be accepted, got %d", code)
	}

	// A wrong (non-empty) revision is still refused.
	if code := postJSONWith(t, cl, srv.URL+"/api/config/napcat", cfgSectionRequest{
		Config:   config.Config{NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3004"}},
		Revision: "not-the-current-revision",
	}, nil); code != http.StatusConflict {
		t.Errorf("a stale revision must be refused with 409, got %d", code)
	}
}

// A section save guarded by a stale revision of that section must be refused,
// but a stale revision of the *other* section must not matter.
func TestSectionSaveGuardsItsOwnRevision(t *testing.T) {
	srv, _, cl := newTestServer(t)

	postJSONWith(t, cl, srv.URL+"/api/config", cfgRequest{Config: config.Config{
		NewAPI: config.NewAPI{URL: "https://one.example.com", Username: "u", Password: "p"},
		NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3002"},
	}}, nil)

	var first cfgSectionResponse
	getJSONWith(t, cl, srv.URL+"/api/config", &first)

	// Somebody changes the NapCat half.
	if code := postJSONWith(t, cl, srv.URL+"/api/config/napcat", cfgSectionRequest{
		Config:   config.Config{NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3999"}},
		Revision: first.Revisions[config.SectionNapCat],
	}, nil); code != 200 {
		t.Fatalf("napcat save status %d", code)
	}

	// A gateway save that carries the pre-change gateway revision must still
	// succeed: the gateway half did not move.
	if code := postJSONWith(t, cl, srv.URL+"/api/config/newapi", cfgSectionRequest{
		Config:   config.Config{NewAPI: config.NewAPI{URL: "https://two.example.com", Username: "u", Password: "p"}},
		Revision: first.Revisions[config.SectionNewAPI],
	}, nil); code != 200 {
		t.Errorf("a gateway save must not be blocked by a NapCat change, got %d", code)
	}

	// But a stale NapCat revision must be refused.
	if code := postJSONWith(t, cl, srv.URL+"/api/config/napcat", cfgSectionRequest{
		Config:   config.Config{NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3002"}},
		Revision: first.Revisions[config.SectionNapCat],
	}, nil); code != http.StatusConflict {
		t.Errorf("a stale NapCat revision must be refused with 409, got %d", code)
	}
}

// fakeNapCatAdmin records what auto-configure was asked to do and returns a
// canned result, so the endpoint is testable without a live NapCat.
type fakeNapCatAdmin struct {
	gotURL   string
	gotToken string
	gotOpts  napcat.AdapterOptions
	err      error
}

func (f *fakeNapCatAdmin) EnsureAdapters(url, token string, opts napcat.AdapterOptions) (*napcat.AdapterResult, error) {
	f.gotURL, f.gotToken, f.gotOpts = url, token, opts
	if f.err != nil {
		return nil, f.err
	}
	return &napcat.AdapterResult{
		Token:      "aabbccddeeff00112233445566778899",
		OneBotURL:  "http://127.0.0.1:3002",
		EventWSURL: "ws://127.0.0.1:3001",
	}, nil
}

// The auto-configure endpoint logs in with the supplied WebUI credential, asks
// for the adapters, and stores the resulting OneBot/event settings.
func TestAutoConfigureNapCatStoresAdapters(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "auto.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	fake := &fakeNapCatAdmin{}
	r := NewRouter(st, svc, cfgMgr, t.TempDir())
	r.SetNapCatAdmin(fake)
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Post(srv.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"username":"admin","password":"test-password"}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	resp.Body.Close()

	if code := postJSONWith(t, cl, srv.URL+"/api/config/napcat/autoconfigure", map[string]any{
		"webui_url":   "http://127.0.0.1:6099/webui/",
		"webui_token": "webui-secret",
	}, nil); code != 200 {
		t.Fatalf("autoconfigure status %d", code)
	}

	if fake.gotURL != "http://127.0.0.1:6099/webui/" {
		t.Errorf("webui url passed through wrong: %q", fake.gotURL)
	}
	if fake.gotToken != "webui-secret" {
		t.Errorf("webui token not passed: %q", fake.gotToken)
	}
	if fake.gotOpts.ConnectHost != "127.0.0.1" {
		t.Errorf("connect host not derived from the webui url: %q", fake.gotOpts.ConnectHost)
	}

	stored := cfgMgr.MustLoad()
	if stored.NapCat.OneBotURL != "http://127.0.0.1:3002" {
		t.Errorf("onebot url not stored: %q", stored.NapCat.OneBotURL)
	}
	if stored.NapCat.EventWSURL != "ws://127.0.0.1:3001" {
		t.Errorf("event ws url not stored: %q", stored.NapCat.EventWSURL)
	}
	if stored.NapCat.OneBotToken != "aabbccddeeff00112233445566778899" {
		t.Errorf("adapter token not stored: %q", stored.NapCat.OneBotToken)
	}
	if stored.NapCat.EventWSToken != stored.NapCat.OneBotToken {
		t.Errorf("event token should match the adapter token")
	}
	if stored.NapCat.WebUIToken != "webui-secret" {
		t.Errorf("webui token not stored: %q", stored.NapCat.WebUIToken)
	}
}

// When auto-configure is not wired up, the endpoint says so instead of 404ing
// or pretending to have done something.
func TestAutoConfigureNapCatWithoutAdminIsNotImplemented(t *testing.T) {
	srv, _, cl := newTestServer(t)
	code := postJSONWith(t, cl, srv.URL+"/api/config/napcat/autoconfigure", map[string]any{
		"webui_url": "http://127.0.0.1:6099", "webui_token": "t",
	}, nil)
	if code != http.StatusNotImplemented {
		t.Errorf("expected 501 when auto-configure is unavailable, got %d", code)
	}
}

// Auto-configure with neither a URL nor a stored one is a client error, not a
// failed gateway call.
func TestAutoConfigureNapCatRequiresURL(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "auto2.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	r := NewRouter(st, svc, cfgMgr, t.TempDir())
	r.SetNapCatAdmin(&fakeNapCatAdmin{})
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Post(srv.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"username":"admin","password":"test-password"}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	resp.Body.Close()

	code := postJSONWith(t, cl, srv.URL+"/api/config/napcat/autoconfigure", map[string]any{}, nil)
	if code != 400 {
		t.Errorf("expected 400 when no WebUI URL is known, got %d", code)
	}
}

// Binding from the account side must record the account's real username and
// email, not the blanks the form sends. The operator picks an account from a
// list, so the authoritative values are the ones the gateway reported: a row
// stored with an empty email can never be matched against a member's
// qq@qq.com again, and the account would look unbound in the directory.
func TestManualBindFillsAccountDetailsFromCache(t *testing.T) {
	srv, st, cl := newTestServer(t)
	if err := st.UpsertMembers(1, []store.Member{{QQ: 2470777872, Nickname: "FuZY"}}); err != nil {
		t.Fatal(err)
	}
	// The account cache as SyncNewAPIUsers would have written it.
	if err := st.ReplaceNewAPIUsers([]store.NewAPIUser{
		{ID: 4, Username: "fuzyQQ", Email: "2470777872@qq.com", Group: "default"},
	}); err != nil {
		t.Fatal(err)
	}

	var resp struct {
		Binding store.Binding `json:"binding"`
	}
	// The form sends only the two ids, as the account-side dialog does.
	if code := postJSONWith(t, cl, srv.URL+"/api/bindings", map[string]any{
		"qq": 2470777872, "newapi_user_id": 4,
	}, &resp); code != 200 {
		t.Fatalf("bind status %d", code)
	}
	if resp.Binding.NewAPIUsername != "fuzyQQ" {
		t.Errorf("username not filled from the cache: %q", resp.Binding.NewAPIUsername)
	}
	if resp.Binding.NewAPIEmail != "2470777872@qq.com" {
		t.Errorf("email not filled from the cache: %q", resp.Binding.NewAPIEmail)
	}

	// And the directory must now show the two sides as one bound row.
	var got struct {
		Items []service.UserRow `json:"items"`
	}
	getJSONWith(t, cl, srv.URL+"/api/users", &got)
	merged := 0
	for _, r := range got.Items {
		if r.QQ == 2470777872 && r.NewAPIUserID == 4 {
			merged++
			if !r.Bound {
				t.Error("the merged row must be marked bound")
			}
		}
	}
	if merged != 1 {
		t.Errorf("the QQ and the account must appear as exactly one row, got %d: %+v", merged, got.Items)
	}
}

// An account-side bind must work when the row carries no QQ: the operator picks
// the QQ in the dialog. (The old dialog sent qq=0 and the request failed.)
func TestManualBindFromAccountSideWithPickedQQ(t *testing.T) {
	srv, st, cl := newTestServer(t)
	if err := st.ReplaceNewAPIUsers([]store.NewAPIUser{
		{ID: 4, Username: "fuzyQQ", Email: "2470777872@qq.com"},
	}); err != nil {
		t.Fatal(err)
	}

	var resp struct {
		Binding store.Binding `json:"binding"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/bindings", map[string]any{
		"qq": 2470777872, "newapi_user_id": 4,
	}, &resp); code != 200 {
		t.Fatalf("bind from the account side failed with %d", code)
	}
	if resp.Binding.QQ != 2470777872 || resp.Binding.NewAPIUserID != 4 {
		t.Errorf("unexpected binding: %+v", resp.Binding)
	}

	// A bind with no QQ is still refused: the dialog must supply one.
	if code := postJSONWith(t, cl, srv.URL+"/api/bindings", map[string]any{
		"qq": 0, "newapi_user_id": 4,
	}, nil); code != 400 {
		t.Errorf("a missing QQ must be a 400, got %d", code)
	}
}

// The cache fill must only supply what the caller left out. An operator who
// types a username (or an API client that knows better) must not have it
// silently replaced by whatever the account cache happens to hold - the form's
// value is the more recent statement of intent.
func TestManualBindKeepsExplicitlySuppliedDetails(t *testing.T) {
	srv, st, cl := newTestServer(t)
	if err := st.ReplaceNewAPIUsers([]store.NewAPIUser{
		{ID: 4, Username: "cached-name", Email: "cached@qq.com"},
	}); err != nil {
		t.Fatal(err)
	}

	var resp struct {
		Binding store.Binding `json:"binding"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/bindings", map[string]any{
		"qq": 2470777872, "newapi_user_id": 4,
		"newapi_username": "typed-by-operator",
		"newapi_email":    "typed@example.com",
	}, &resp); code != 200 {
		t.Fatalf("bind status %d", code)
	}
	if resp.Binding.NewAPIUsername != "typed-by-operator" {
		t.Errorf("the caller's username was overwritten by the cache: %q", resp.Binding.NewAPIUsername)
	}
	if resp.Binding.NewAPIEmail != "typed@example.com" {
		t.Errorf("the caller's email was overwritten by the cache: %q", resp.Binding.NewAPIEmail)
	}
}

// An account the cache has never seen must still bind: the cache is a
// convenience, not a prerequisite. The row simply carries what the caller sent.
func TestManualBindWorksWithoutACachedAccount(t *testing.T) {
	srv, _, cl := newTestServer(t)
	// Deliberately no ReplaceNewAPIUsers: the cache is empty.

	var resp struct {
		Binding store.Binding `json:"binding"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/bindings", map[string]any{
		"qq": 2470777872, "newapi_user_id": 99,
		"newapi_username": "uncached",
	}, &resp); code != 200 {
		t.Fatalf("a bind must not require the account cache, got %d", code)
	}
	if resp.Binding.NewAPIUserID != 99 || resp.Binding.NewAPIUsername != "uncached" {
		t.Errorf("unexpected binding: %+v", resp.Binding)
	}
	if resp.Binding.NewAPIEmail != "" {
		t.Errorf("no cached email to fill, want empty: %q", resp.Binding.NewAPIEmail)
	}
}

// Autoconfigure must persist nothing when the NapCat write fails: storing URLs
// and a token for adapters that were never created would leave the panel
// claiming a working setup while every sweep fails.
func TestAutoConfigureNapCatPersistsNothingOnFailure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "autofail.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)

	// A known-good starting config, so we can prove it is left untouched.
	before := config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "pw"},
		NapCat: config.NapCat{
			WebUIURL: "http://127.0.0.1:6099", WebUIToken: "wt",
			OneBotURL: "http://127.0.0.1:3002", OneBotToken: "old-ob-token",
		},
	}
	if err := cfgMgr.Save(before); err != nil {
		t.Fatal(err)
	}

	r := NewRouter(st, svc, cfgMgr, t.TempDir())
	r.SetNapCatAdmin(&fakeNapCatAdmin{err: errors.New("login rate limit")})
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Post(srv.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"username":"admin","password":"test-password"}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	resp.Body.Close()

	code := postJSONWith(t, cl, srv.URL+"/api/config/napcat/autoconfigure", map[string]any{
		"webui_url": "http://127.0.0.1:6099", "webui_token": "wt",
	}, nil)
	if code != http.StatusBadGateway {
		t.Errorf("a failed NapCat write must surface as 502, got %d", code)
	}

	// The stored config must be exactly what it was.
	got := cfgMgr.MustLoad()
	if got.NapCat.OneBotURL != before.NapCat.OneBotURL {
		t.Errorf("onebot url was written despite the failure: %q", got.NapCat.OneBotURL)
	}
	if got.NapCat.OneBotToken != before.NapCat.OneBotToken {
		t.Errorf("adapter token was written despite the failure: %q", got.NapCat.OneBotToken)
	}
	if got.NapCat.WebUIToken != before.NapCat.WebUIToken {
		t.Errorf("webui token changed: %q", got.NapCat.WebUIToken)
	}
}

// The SPA's index.html must be revalidated while the content-hashed bundle may
// be cached hard. Without the distinction the browser applies heuristic
// freshness to index.html, serves it from disk after a deploy, and the operator
// keeps seeing the previous UI until they hard-refresh — a bug that is
// invisible until the next release.
func TestStaticCachingDistinguishesIndexFromHashedAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte(`<!doctype html><script src="/assets/index-abc123.js"></script>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "index-abc123.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(staticHandler(dir))
	t.Cleanup(srv.Close)

	get := func(path string) (string, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Type")
	}

	cc, _ := get("/")
	if cc != indexCacheControl {
		t.Errorf("index.html Cache-Control = %q, want %q", cc, indexCacheControl)
	}

	// The SPA fallback (a client-side route) serves index.html too, so it must
	// carry index.html's rule rather than inheriting the route's own shape.
	cc, ct := get("/users/42")
	if cc != indexCacheControl {
		t.Errorf("SPA fallback Cache-Control = %q, want %q", cc, indexCacheControl)
	}
	if !strings.Contains(ct, "text/html") {
		t.Errorf("SPA fallback Content-Type = %q, want html", ct)
	}

	cc, _ = get("/assets/index-abc123.js")
	if cc != hashedAssetCacheControl {
		t.Errorf("hashed asset Cache-Control = %q, want %q", cc, hashedAssetCacheControl)
	}

	// A missing file under /assets/ falls back to index.html, so it must carry
	// index.html's rule. Without this case a mutation that hoists the asset
	// header above the os.Stat branch keeps every other assertion green while
	// caching that HTML for a year — and a later deploy that ships a file of
	// that name would then serve a stale page from cache.
	cc, ct = get("/assets/does-not-exist.js")
	if cc != indexCacheControl {
		t.Errorf("missing asset fallback Cache-Control = %q, want %q", cc, indexCacheControl)
	}
	if !strings.Contains(ct, "text/html") {
		t.Errorf("missing asset fallback Content-Type = %q, want html", ct)
	}

	// A file served from the root is not content-hashed, so it must revalidate
	// like index.html: an operator can replace it under the same name.
	if err := os.WriteFile(filepath.Join(dir, "favicon.ico"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cc, _ = get("/favicon.ico")
	if cc != indexCacheControl {
		t.Errorf("root file Cache-Control = %q, want %q", cc, indexCacheControl)
	}
}
