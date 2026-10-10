package api

import (
	"encoding/json"
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

// testEnv is an authenticated test server: the cookie jar carries the session
// created by first-run setup.
type testEnv struct {
	srv    *httptest.Server
	st     *store.Store
	client *http.Client
	cfg    *config.Manager
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
	srv := httptest.NewServer(NewRouter(st, svc, cfgMgr, t.TempDir()).Handler())
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

	return &testEnv{srv: srv, st: st, client: client, cfg: cfgMgr}
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
	var saved config.Config
	if code := postJSONWith(t, cl, srv.URL+"/api/config", in, &saved); code != 200 {
		t.Fatalf("save status %d", code)
	}
	if saved.NewAPI.Password != config.RedactedPassword {
		t.Errorf("password must not be echoed back: %q", saved.NewAPI.Password)
	}

	var got config.Config
	getJSONWith(t, cl, srv.URL+"/api/config", &got)
	if got.NewAPI.Password != config.RedactedPassword {
		t.Errorf("GET must redact the password: %q", got.NewAPI.Password)
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

	postJSONWith(t, cl, srv.URL+"/api/config", config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "real-pw"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "tok"},
	}, nil)

	var got config.Config
	getJSONWith(t, cl, srv.URL+"/api/config", &got)
	got.NewAPI.URL = "https://other.example.com" // operator edits only the URL
	postJSONWith(t, cl, srv.URL+"/api/config", got, nil)

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
	if code := postJSONWith(t, cl, srv.URL+"/api/config", bad, nil); code == 200 {
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
	// New groups default to monitored so the first sweep does something.
	if !groups[0].Enabled {
		t.Error("a freshly discovered group should be enabled by default")
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

	postJSONWith(t, cl, srv.URL+"/api/config", config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "pw-123"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "wt"},
	}, nil)

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
