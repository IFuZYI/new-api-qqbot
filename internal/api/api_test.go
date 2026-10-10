package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
func (fakeGateway) SetGroup(u *newapi.User, group string) error       { return nil }
func (fakeGateway) AddQuota(userID, value int) error                  { return nil }

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

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := service.New(st, fakeGateway{}, fakeQQ{})
	srv := httptest.NewServer(NewRouter(st, svc).Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
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
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(raw)))
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
	srv, _ := newTestServer(t)

	var got struct {
		Configured bool             `json:"configured"`
		Settings   service.Settings `json:"settings"`
	}
	if code := getJSON(t, srv.URL+"/api/bootstrap", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Configured {
		t.Error("a fresh install must report itself as unconfigured")
	}
	if got.Settings.TargetGroup != "authorized" {
		t.Errorf("settings should be returned with defaults: %+v", got.Settings)
	}
}

func TestConfigRoundTripHidesSecrets(t *testing.T) {
	srv, st := newTestServer(t)

	in := config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "secret-pw"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "webui-tok"},
	}
	var saved config.Config
	if code := postJSON(t, srv.URL+"/api/config", in, &saved); code != 200 {
		t.Fatalf("save status %d", code)
	}
	if saved.NewAPI.Password != config.RedactedPassword {
		t.Errorf("password must not be echoed back: %q", saved.NewAPI.Password)
	}

	var got config.Config
	getJSON(t, srv.URL+"/api/config", &got)
	if got.NewAPI.Password != config.RedactedPassword {
		t.Errorf("GET must redact the password: %q", got.NewAPI.Password)
	}

	// The real secret must still be on disk, unchanged.
	stored, err := config.Load(st)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NewAPI.Password != "secret-pw" {
		t.Errorf("stored password was clobbered: %q", stored.NewAPI.Password)
	}
}

// Saving the form without retyping the password must not erase it.
func TestConfigSaveKeepsPasswordWhenRedactedPlaceholderSent(t *testing.T) {
	srv, st := newTestServer(t)

	postJSON(t, srv.URL+"/api/config", config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "real-pw"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "tok"},
	}, nil)

	var got config.Config
	getJSON(t, srv.URL+"/api/config", &got)
	got.NewAPI.URL = "https://other.example.com" // operator edits only the URL
	postJSON(t, srv.URL+"/api/config", got, nil)

	stored, _ := config.Load(st)
	if stored.NewAPI.Password != "real-pw" {
		t.Errorf("password was wiped by a redacted round trip: %q", stored.NewAPI.Password)
	}
	if stored.NewAPI.URL != "https://other.example.com" {
		t.Errorf("URL edit was lost: %q", stored.NewAPI.URL)
	}
}

func TestConfigRejectsInvalidInput(t *testing.T) {
	srv, _ := newTestServer(t)
	bad := config.Config{
		NewAPI: config.NewAPI{URL: "not-a-url", Username: "u", Password: "p"},
		NapCat: config.NapCat{WebUIURL: "http://127.0.0.1:6099", WebUIToken: "t"},
	}
	if code := postJSON(t, srv.URL+"/api/config", bad, nil); code == 200 {
		t.Error("an invalid URL must be rejected")
	}
}

func TestGroupsRefreshStoresSelection(t *testing.T) {
	srv, _ := newTestServer(t)

	var groups []store.MonitoredGroup
	if code := postJSON(t, srv.URL+"/api/groups/refresh", map[string]any{}, &groups); code != 200 {
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
	srv, _ := newTestServer(t)
	postJSON(t, srv.URL+"/api/groups/refresh", map[string]any{}, nil)

	var groups []store.MonitoredGroup
	if code := postJSON(t, srv.URL+"/api/groups", map[string]any{
		"group_ids": []int64{},
	}, &groups); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(groups) != 1 || groups[0].Enabled {
		t.Errorf("deselecting should disable the group: %+v", groups)
	}
}

func TestMembersEndpointIncludesBindingState(t *testing.T) {
	srv, st := newTestServer(t)
	postJSON(t, srv.URL+"/api/groups/refresh", map[string]any{}, nil)
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
	if code := getJSON(t, srv.URL+"/api/members", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Total != 1 || got.Items[0].QQ != 54321 || got.Items[0].Bound {
		t.Errorf("unexpected members payload: %+v", got)
	}

	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, NewAPIUsername: "seven"}); err != nil {
		t.Fatal(err)
	}
	getJSON(t, srv.URL+"/api/members", &got)
	if !got.Items[0].Bound {
		t.Error("binding state not reflected in the member list")
	}
}

func TestManualBindAndUnbind(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.UpsertMembers(1, []store.Member{{QQ: 54321, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}

	var b store.Binding
	if code := postJSON(t, srv.URL+"/api/bindings", map[string]any{
		"qq": 54321, "newapi_user_id": 42, "newapi_username": "forty-two",
	}, &b); code != 200 {
		t.Fatalf("bind status %d", code)
	}
	if b.NewAPIUserID != 42 || b.Source != "manual" {
		t.Errorf("unexpected binding: %+v", b)
	}

	if code := postJSON(t, srv.URL+"/api/bindings/54321/delete", map[string]any{}, nil); code != 200 {
		t.Fatalf("unbind status %d", code)
	}
	if _, err := st.GetBinding(54321); err == nil {
		t.Error("binding should be gone after unbind")
	}
}

func TestSettingsEndpointValidates(t *testing.T) {
	srv, _ := newTestServer(t)

	var s service.Settings
	getJSON(t, srv.URL+"/api/settings", &s)
	if s.TargetGroup != "authorized" {
		t.Fatalf("unexpected defaults: %+v", s)
	}

	s.RewardMin, s.RewardMax = 500, 100
	if code := postJSON(t, srv.URL+"/api/settings", s, nil); code == 200 {
		t.Error("min > max must be rejected")
	}
	s.RewardMin, s.RewardMax = 100, 500
	if code := postJSON(t, srv.URL+"/api/settings", s, nil); code != 200 {
		t.Errorf("valid settings rejected, status %d", code)
	}
}

func TestActionsRunTheSweeps(t *testing.T) {
	srv, _ := newTestServer(t)
	postJSON(t, srv.URL+"/api/groups/refresh", map[string]any{}, nil)

	var res struct {
		Members int `json:"members"`
		Bound   int `json:"bound"`
	}
	if code := postJSON(t, srv.URL+"/api/actions/sync", map[string]any{}, &res); code != 200 {
		t.Fatalf("sync status %d", code)
	}
	if res.Members != 1 {
		t.Errorf("sync should have stored the roster: %+v", res)
	}

	if code := postJSON(t, srv.URL+"/api/actions/bind", map[string]any{}, nil); code != 200 {
		t.Errorf("bind action status %d", code)
	}
	if code := postJSON(t, srv.URL+"/api/actions/promote", map[string]any{}, nil); code != 200 {
		t.Errorf("promote action status %d", code)
	}
	if code := postJSON(t, srv.URL+"/api/actions/award", map[string]any{}, nil); code != 200 {
		t.Errorf("award action status %d", code)
	}
}

func TestUnknownActionIsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	if code := postJSON(t, srv.URL+"/api/actions/nope", map[string]any{}, nil); code != 404 {
		t.Errorf("want 404, got %d", code)
	}
}

func TestCheckinsEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.RecordCheckin(store.Checkin{QQ: 54321, Date: "2026-10-10", RewardQuota: 150}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items []store.Checkin `json:"items"`
	}
	if code := getJSON(t, srv.URL+"/api/checkins?date=2026-10-10", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Items) != 1 || got.Items[0].RewardQuota != 150 {
		t.Errorf("unexpected: %+v", got.Items)
	}
}
