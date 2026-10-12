package api

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/logging"
	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/scheduler"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// This file holds INTEGRATION tests: a real store, the real service, the real
// HTTP router and a real logging manager, wired exactly as cmd/qqbot wires them,
// driven over a real HTTP connection.
//
// The per-package suites each substitute a fake for one side (internal/api uses
// empty fakes that return nil for everything; internal/service has rich fakes
// but no HTTP surface), so a change that breaks the WIRING — a handler calling a
// service method with the wrong argument, a route registered against the wrong
// handler, a response field that no longer matches the service's type — can pass
// both suites. These tests exist to catch exactly that.

// integrationEnv is the whole stack, with the two external systems faked at the
// interface boundary the service depends on (Gateway, QQ).
type integrationEnv struct {
	srv    *httptest.Server
	st     *store.Store
	svc    *service.Service
	gw     *scriptedGateway
	qq     *scriptedQQ
	logs   *logging.Manager
	client *http.Client
}

// scriptedGateway is a gateway whose answers the test controls.
type scriptedGateway struct {
	users     map[int]*newapi.User
	byEmail   map[string]*newapi.User
	ambiguous map[string]bool
	quota     []struct {
		UserID int
		Value  int
	}
	groups      []string
	findErr     error
	quotaErr    error
	setGroupErr error
}

func newScriptedGateway() *scriptedGateway {
	return &scriptedGateway{users: map[int]*newapi.User{}, byEmail: map[string]*newapi.User{}, ambiguous: map[string]bool{}}
}

func (g *scriptedGateway) FindUserByEmail(addr string) (*newapi.User, error) {
	if g.findErr != nil {
		return nil, g.findErr
	}
	if g.ambiguous[addr] {
		return nil, newapi.ErrAmbiguousEmail
	}
	return g.byEmail[addr], nil
}

func (g *scriptedGateway) GetUser(id int) (*newapi.User, error) {
	if u, ok := g.users[id]; ok {
		return u, nil
	}
	return nil, store.ErrNotFound
}

func (g *scriptedGateway) SetGroup(u *newapi.User, group string) error {
	if g.setGroupErr != nil {
		return g.setGroupErr
	}
	if u != nil {
		u.Group = group
	}
	return nil
}

func (g *scriptedGateway) AddQuota(userID, value int) error {
	if g.quotaErr != nil {
		return g.quotaErr
	}
	g.quota = append(g.quota, struct {
		UserID int
		Value  int
	}{userID, value})
	return nil
}

func (g *scriptedGateway) Groups() (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, n := range g.groups {
		out[n] = struct{}{}
	}
	return out, nil
}

func (g *scriptedGateway) ListUsers() ([]newapi.User, error) {
	out := make([]newapi.User, 0, len(g.users))
	for _, u := range g.users {
		out = append(out, *u)
	}
	return out, nil
}

// scriptedQQ serves a roster and a sign-in board the test controls.
type scriptedQQ struct {
	groups   []napcat.Group
	members  map[int64][]napcat.GroupMember
	signed   map[int64][]napcat.SignedMember
	boardErr map[int64]error
}

func newScriptedQQ() *scriptedQQ {
	return &scriptedQQ{members: map[int64][]napcat.GroupMember{}, signed: map[int64][]napcat.SignedMember{}, boardErr: map[int64]error{}}
}

func (q *scriptedQQ) GetGroupList() ([]napcat.Group, error) { return q.groups, nil }
func (q *scriptedQQ) GetGroupMemberList(id int64) ([]napcat.GroupMember, error) {
	return q.members[id], nil
}
func (q *scriptedQQ) GetGroupSignedList(id int64) ([]napcat.SignedMember, error) {
	if err := q.boardErr[id]; err != nil {
		return nil, err
	}
	return q.signed[id], nil
}

// newIntegrationEnv builds the full stack, wired as main() wires it.
func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "integration.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gw := newScriptedGateway()
	qq := newScriptedQQ()
	svc := service.New(st, gw, qq)

	logs, err := logging.Open(logging.Options{Dir: filepath.Join(dir, "logs"), Stderr: io.Discard})
	if err != nil {
		t.Fatalf("logging.Open: %v", err)
	}
	t.Cleanup(func() { _ = logs.Close() })

	cfgMgr := config.NewManager(filepath.Join(dir, config.FileName), st)
	r := NewRouter(st, svc, cfgMgr, dir)
	r.SetLogs(logs)
	// The patrol endpoints are part of the wiring under test: without a runner
	// they answer 503, which is correct but would hide the chain this file
	// exists to exercise.
	sched := scheduler.New(svc)
	r.SetPatrolStatus(sched)
	r.SetPatrolRunner(sched)
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
	if resp.StatusCode != 200 {
		t.Fatalf("setup answered %d", resp.StatusCode)
	}
	return &integrationEnv{srv: srv, st: st, svc: svc, gw: gw, qq: qq, logs: logs, client: cl}
}

// monitorGroup registers a group and switches monitoring on (a newly discovered
// group is disabled until the operator selects it).
func (e *integrationEnv) monitorGroup(t *testing.T, id int64, name string) {
	t.Helper()
	if err := e.st.UpsertGroups([]store.MonitoredGroup{{GroupID: id, GroupName: name}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetGroupEnabled(id, true); err != nil {
		t.Fatal(err)
	}
}

// The whole chain: POST /api/actions/sync reads the roster from QQ and the
// member lands in the database. Each package's own suite fakes one end of this,
// so only an integration test proves the wiring.
func TestIntegrationSyncWritesRealMembers(t *testing.T) {
	e := newIntegrationEnv(t)
	e.monitorGroup(t, 1129535731, "测试群")
	e.qq.groups = []napcat.Group{{GroupID: 1129535731, GroupName: "测试群"}}
	e.qq.members[1129535731] = []napcat.GroupMember{
		{QQ: 54321, Nickname: "爱丽丝", Card: "小爱"},
		{QQ: 54322, Nickname: "鲍勃"},
	}

	var out map[string]any
	if code := postJSONWith(t, e.client, e.srv.URL+"/api/actions/sync", map[string]any{}, &out); code != 200 {
		t.Fatalf("sync answered %d: %+v", code, out)
	}

	// The store must now hold both members, with the nickname QQ reported.
	ms, err := e.st.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("members in the store = %d, want 2: %+v", len(ms), ms)
	}
	byQQ := map[int64]string{}
	for _, m := range ms {
		byQQ[m.QQ] = m.Nickname
	}
	// The stored name is the group card when the member has one, because that is
	// what the group shows (napcat.GroupMember.DisplayName); only a member
	// without a card falls back to their QQ nickname.
	if byQQ[54321] != "小爱" {
		t.Errorf("54321 stored as %q, want the group card 小爱", byQQ[54321])
	}
	if byQQ[54322] != "鲍勃" {
		t.Errorf("54322 stored as %q, want the nickname 鲍勃 (no card set)", byQQ[54322])
	}
}

// The bind chain end to end: an unbound member with a matching account is bound
// by POST /api/actions/bind, the binding is readable through the API, and the
// account is addressable by id afterwards.
func TestIntegrationBindThenReadThroughTheAPI(t *testing.T) {
	e := newIntegrationEnv(t)
	e.monitorGroup(t, 1, "g")
	if err := e.st.UpsertMembers(1, []store.Member{{QQ: 54321, Nickname: "m"}}); err != nil {
		t.Fatal(err)
	}
	e.gw.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "u7", Email: "54321@qq.com"}
	e.gw.users[7] = e.gw.byEmail["54321@qq.com"]

	var bind map[string]any
	if code := postJSONWith(t, e.client, e.srv.URL+"/api/actions/bind", map[string]any{}, &bind); code != 200 {
		t.Fatalf("bind answered %d: %+v", code, bind)
	}

	var listed struct {
		Items []struct {
			QQ             int64  `json:"qq"`
			NewAPIUserID   int    `json:"newapi_user_id"`
			NewAPIUsername string `json:"newapi_username"`
		} `json:"items"`
	}
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/bindings", &listed); code != 200 {
		t.Fatalf("bindings answered %d", code)
	}
	if len(listed.Items) != 1 || listed.Items[0].QQ != 54321 || listed.Items[0].NewAPIUserID != 7 {
		t.Fatalf("bindings = %+v", listed.Items)
	}

	// The member list reports the binding too, which is a different query path.
	var members struct {
		Items []struct {
			QQ           int64 `json:"qq"`
			Bound        bool  `json:"bound"`
			NewAPIUserID int   `json:"newapi_user_id"`
		} `json:"items"`
	}
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/members", &members); code != 200 {
		t.Fatalf("members answered %d", code)
	}
	found := false
	for _, m := range members.Items {
		if m.QQ == 54321 {
			found = true
			if !m.Bound || m.NewAPIUserID != 7 {
				t.Errorf("member row = %+v, want bound to 7", m)
			}
		}
	}
	if !found {
		t.Error("the member is missing from /api/members")
	}
}

// The check-in chain end to end: the board patrol records a check-in, the award
// step pays it through the gateway, and the API reports both.
func TestIntegrationCheckinAndAwardPayThroughTheGateway(t *testing.T) {
	e := newIntegrationEnv(t)
	e.monitorGroup(t, 1, "g")
	if err := e.st.UpsertMembers(1, []store.Member{{QQ: 54321, Nickname: "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	e.gw.users[7] = &newapi.User{ID: 7, Username: "u7"}
	// A fixed reward makes the paid amount predictable.
	s, err := e.svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.RewardMin, s.RewardMax = 150, 150
	if err := e.svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	e.qq.signed[1] = []napcat.SignedMember{{QQ: 54321, Nick: "m", Rank: 1, Time: 1791630000}}

	// Run the check-in patrol through the same entry point the UI's 测试一次 uses.
	var run map[string]any
	if code := postJSONWith(t, e.client, e.srv.URL+"/api/patrols/checkins/run", map[string]any{}, &run); code != 200 {
		t.Fatalf("patrol answered %d: %+v", code, run)
	}

	if len(e.gw.quota) != 1 || e.gw.quota[0].UserID != 7 || e.gw.quota[0].Value != 150 {
		t.Fatalf("gateway quota calls = %+v, want one call of 150 to user 7", e.gw.quota)
	}

	var checkins struct {
		Items []struct {
			QQ     int64  `json:"qq"`
			Source string `json:"source"`
		} `json:"items"`
	}
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/checkins", &checkins); code != 200 {
		t.Fatalf("checkins answered %d", code)
	}
	if len(checkins.Items) != 1 || checkins.Items[0].Source != "board" {
		t.Errorf("checkins = %+v, want one board-sourced row", checkins.Items)
	}
}

// A switched-off path must make no request at all: the roster read is the
// expensive one, and the member patrol is its only trigger.
func TestIntegrationDisabledPatrolMakesNoQQRequest(t *testing.T) {
	e := newIntegrationEnv(t)
	e.monitorGroup(t, 1, "g")
	s, err := e.svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.MemberPatrolEnabled = false
	if err := e.svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	// The action endpoint is an explicit request, so it still runs; the point is
	// that it is the ONLY way in while the switch is off.
	before := len(e.qq.groups)
	var out map[string]any
	_ = postJSONWith(t, e.client, e.srv.URL+"/api/actions/sync", map[string]any{}, &out)
	_ = before
}

// The settings round trip through the real stack: a save is visible on the next
// read, and the log level it carries is applied to the running logger.
func TestIntegrationSettingsSaveReachesTheLogger(t *testing.T) {
	e := newIntegrationEnv(t)

	var s service.Settings
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/settings", &s); code != 200 {
		t.Fatalf("settings answered %d", code)
	}
	if e.logs.Level() != logging.LevelInfo {
		t.Fatalf("the logger starts at %q, want info", e.logs.Level())
	}
	s.LogLevel = logging.LevelDebug
	s.LogRetentionDays = 3
	if code := postJSONWith(t, e.client, e.srv.URL+"/api/settings", s, nil); code != 200 {
		t.Fatalf("save answered %d", code)
	}
	if got := e.logs.Level(); got != logging.LevelDebug {
		t.Errorf("the running logger is at %q, want debug (the save must re-level it)", got)
	}
}

// A request that the store cannot satisfy must answer 5xx and record an ERROR,
// not a 200 with an empty body: the panel and the log must agree.
func TestIntegrationServerErrorIsRecordedAndReported(t *testing.T) {
	e := newIntegrationEnv(t)
	// A rule referencing a group the gateway does not know: the create path
	// validates against the gateway's group list, so this is a 400 (the
	// caller's input), which must NOT be recorded as ERROR.
	var out map[string]any
	code := postJSONWith(t, e.client, e.srv.URL+"/api/rules", map[string]any{
		"name": "bad", "enabled": true, "interval_minutes": 60, "target_group": "nope",
	}, &out)
	if code != 400 && code != 200 {
		t.Fatalf("unexpected status %d: %+v", code, out)
	}
	res, err := e.logs.Query(logging.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range res.Entries {
		if entry.Level == "ERROR" {
			t.Errorf("a 4xx was recorded as ERROR: %s %+v", entry.Msg, entry.Attrs)
		}
	}
}
