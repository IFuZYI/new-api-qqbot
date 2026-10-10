package api

import (
	"net/http"
	"net/http/cookiejar"
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

// userGateway is a fake gateway with a small account directory, so the user
// directory endpoint can be exercised end to end.
type userGateway struct {
	users map[int]*newapi.User
	fail  map[int]error
}

func (g *userGateway) FindUserByEmail(addr string) (*newapi.User, error) { return nil, nil }
func (g *userGateway) GetUser(id int) (*newapi.User, error) {
	if err := g.fail[id]; err != nil {
		return nil, err
	}
	u, ok := g.users[id]
	if !ok {
		return nil, errString("user not found")
	}
	return u, nil
}
func (g *userGateway) SetGroup(u *newapi.User, group string) error {
	if err := g.fail[u.ID]; err != nil {
		return err
	}
	if srv, ok := g.users[u.ID]; ok {
		srv.Group = group
	}
	return nil
}
func (g *userGateway) AddQuota(int, int) error { return nil }
func (g *userGateway) Groups() (map[string]struct{}, error) {
	return map[string]struct{}{"default": {}, "authorized": {}, "vip": {}}, nil
}
func (g *userGateway) ListUsers() ([]newapi.User, error) {
	out := make([]newapi.User, 0, len(g.users))
	for _, u := range g.users {
		out = append(out, *u)
	}
	return out, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func newUserTestEnv(t *testing.T) (*httptest.Server, *store.Store, *http.Client, *userGateway) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gw := &userGateway{
		users: map[int]*newapi.User{
			1: {ID: 1, Username: "alice", Email: "111@qq.com", Group: "default", Status: 1},
			2: {ID: 2, Username: "stranger", Email: "999@qq.com", Group: "authorized", Status: 1},
		},
		fail: map[int]error{},
	}
	svc := service.New(st, gw, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	srv := httptest.NewServer(NewRouter(st, svc, cfgMgr, t.TempDir()).Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.Post(srv.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"username":"admin","password":"test-password"}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("setup failed with %d", resp.StatusCode)
	}

	// Seed a member bound to account 1, and an account-only row (2).
	if err := st.UpsertMembers(100, []store.Member{{QQ: 111, Nickname: "alice"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBinding(store.Binding{
		QQ: 111, NewAPIUserID: 1, NewAPIUsername: "alice", NewAPIEmail: "111@qq.com", Source: "auto",
	}); err != nil {
		t.Fatal(err)
	}
	return srv, st, client, gw
}

func TestListUsersUnionsMembersAndAccounts(t *testing.T) {
	srv, _, cl, _ := newUserTestEnv(t)

	// Sync first so the account cache is populated.
	var syncRes struct {
		Accounts int `json:"accounts"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/users/sync", map[string]any{}, &syncRes); code != 200 {
		t.Fatalf("sync status %d", code)
	}
	if syncRes.Accounts != 2 {
		t.Errorf("synced %d accounts, want 2", syncRes.Accounts)
	}

	var got usersResponse
	if code := getJSONWith(t, cl, srv.URL+"/api/users", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Total != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", got.Total, got.Items)
	}
	kinds := map[string]bool{}
	for _, r := range got.Items {
		kinds[r.Kind] = true
	}
	if !kinds["member"] || !kinds["account"] {
		t.Errorf("the union must include a member row and an account row: %+v", got.Items)
	}
	if got.Summary.NotJoined != 1 {
		t.Errorf("summary.not_joined = %d, want 1", got.Summary.NotJoined)
	}
}

func TestListUsersFilters(t *testing.T) {
	srv, _, cl, _ := newUserTestEnv(t)
	postJSONWith(t, cl, srv.URL+"/api/users/sync", map[string]any{}, nil)

	var got usersResponse
	if code := getJSONWith(t, cl, srv.URL+"/api/users?kind=account", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Items) != 1 || got.Items[0].NewAPIUserID != 2 {
		t.Fatalf("kind=account should return only account 2: %+v", got.Items)
	}

	if code := getJSONWith(t, cl, srv.URL+"/api/users?group=authorized", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Items) != 1 || got.Items[0].NewAPIUserID != 2 {
		t.Fatalf("group=authorized should return only account 2: %+v", got.Items)
	}

	if code := getJSONWith(t, cl, srv.URL+"/api/users?bound=unbound", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, r := range got.Items {
		if r.Bound {
			t.Errorf("bound=unbound returned a bound row: %+v", r)
		}
	}
}

func TestSetUserGroupEndpoint(t *testing.T) {
	srv, _, cl, gw := newUserTestEnv(t)
	postJSONWith(t, cl, srv.URL+"/api/users/sync", map[string]any{}, nil)

	if code := postJSONWith(t, cl, srv.URL+"/api/users/1/group",
		map[string]any{"group": "vip"}, nil); code != 200 {
		t.Fatalf("status %d", code)
	}
	if gw.users[1].Group != "vip" {
		t.Errorf("gateway group = %q, want vip", gw.users[1].Group)
	}
}

func TestSetUserGroupsEndpointReportsFailures(t *testing.T) {
	srv, _, cl, gw := newUserTestEnv(t)
	postJSONWith(t, cl, srv.URL+"/api/users/sync", map[string]any{}, nil)

	// Account 2 is protected; account 1 must still move.
	gw.fail[2] = errString("No permission to access users of same or higher level")

	var got struct {
		Results   []service.GroupChangeResult `json:"results"`
		Succeeded int                         `json:"succeeded"`
		Failed    int                         `json:"failed"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/users/groups",
		map[string]any{"user_ids": []int{1, 2}, "group": "vip"}, &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Succeeded != 1 || got.Failed != 1 {
		t.Errorf("want 1 succeeded and 1 failed, got %d/%d", got.Succeeded, got.Failed)
	}
	if gw.users[1].Group != "vip" {
		t.Errorf("the unprotected account did not move: %+v", gw.users[1])
	}
	if gw.users[2].Group == "vip" {
		t.Error("the protected account must not have moved")
	}
}

func TestSetUserGroupsEndpointRejectsEmptySelection(t *testing.T) {
	srv, _, cl, _ := newUserTestEnv(t)

	code := postJSONWith(t, cl, srv.URL+"/api/users/groups",
		map[string]any{"user_ids": []int{}, "group": "vip"}, nil)
	if code != 400 {
		t.Errorf("an empty selection must be a 400, got %d", code)
	}
}

// The user directory must be behind auth like everything else.
func TestUsersRequiresAuth(t *testing.T) {
	srv, _, _, _ := newUserTestEnv(t)

	resp, err := http.Get(srv.URL + "/api/users")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated /api/users returned %d, want 401", resp.StatusCode)
	}
}

// usersResponse mirrors the endpoint's JSON, kept local so the test does not
// depend on the service package's field names.
type usersResponse struct {
	Items []struct {
		Kind         string `json:"kind"`
		QQ           int64  `json:"qq"`
		Bound        bool   `json:"bound"`
		NewAPIUserID int    `json:"newapi_user_id"`
		NewAPIGroup  string `json:"newapi_group"`
	} `json:"items"`
	Total   int `json:"total"`
	Summary struct {
		Total     int `json:"total"`
		Bound     int `json:"bound"`
		Unbound   int `json:"unbound"`
		InGroup   int `json:"in_group"`
		Left      int `json:"left"`
		Accounts  int `json:"accounts"`
		NotJoined int `json:"not_joined"`
	} `json:"summary"`
}

var _ = napcat.Group{}
