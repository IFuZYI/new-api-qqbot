package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// fakeNewAPI records calls and returns canned answers.
type fakeNewAPI struct {
	byEmail   map[string]*newapi.User
	ambig     map[string]bool
	dir       map[int]*newapi.User
	groupSets []groupSet
	quotaAdds []quotaAdd
	failSet   error
	failQuota error
	failFind  error

	// findCalls counts FindUserByEmail invocations, and failFindAfter makes the
	// fake fail only from the Nth call on: a sweep's stop-on-rate-limit guard
	// must be visible without relying on the first call failing.
	findCalls     int
	failFindAfter int
	// getCalls counts GetUser invocations: a batch group change must read each
	// account once, not once for the username and again for the write.
	getCalls int

	// failSetFor fails SetGroup for specific account ids, so a batch can be
	// exercised with one protected account among successful ones.
	failSetFor map[int]error

	// mutateUser models whether SetGroup writes the new group back onto the
	// caller's struct. The real client does not; default true keeps the older
	// tests reading naturally.
	mutateUser *bool
}

// errString is a tiny error type so tests can name an error inline.
type errString string

func (e errString) Error() string { return string(e) }

type groupSet struct {
	userID int
	group  string
}

type quotaAdd struct {
	userID int
	value  int
}

func newFakeNewAPI() *fakeNewAPI {
	return &fakeNewAPI{byEmail: map[string]*newapi.User{}, ambig: map[string]bool{}}
}

func (f *fakeNewAPI) FindUserByEmail(addr string) (*newapi.User, error) {
	f.findCalls++
	if f.failFind != nil && (f.failFindAfter == 0 || f.findCalls > f.failFindAfter) {
		return nil, f.failFind
	}
	if f.ambig[addr] {
		return nil, newapi.ErrAmbiguousEmail
	}
	u := f.byEmail[addr]
	if u != nil {
		// The gateway knows this account, so it is addressable by id too.
		f.users()[u.ID] = u
	}
	return u, nil
}

// users is the account directory the fake mutates, so a promotion is visible on
// the next read (as it is on the real gateway).
func (f *fakeNewAPI) users() map[int]*newapi.User {
	if f.dir == nil {
		f.dir = map[int]*newapi.User{}
	}
	return f.dir
}

func (f *fakeNewAPI) GetUser(id int) (*newapi.User, error) {
	f.getCalls++
	if u, ok := f.users()[id]; ok {
		return u, nil
	}
	return nil, errors.New("user not found")
}

func (f *fakeNewAPI) SetGroup(u *newapi.User, group string) error {
	if err := f.failSetFor[u.ID]; err != nil {
		return err
	}
	if f.failSet != nil {
		return f.failSet
	}
	f.groupSets = append(f.groupSets, groupSet{userID: u.ID, group: group})
	if f.mutateUser == nil || *f.mutateUser {
		u.Group = group
	}
	// The gateway's own copy always changes, whatever the caller's struct does.
	server := f.users()[u.ID]
	if server == nil {
		server = &newapi.User{ID: u.ID}
	}
	server.Group = group
	f.users()[u.ID] = server
	return nil
}

func (f *fakeNewAPI) Groups() (map[string]struct{}, error) {
	return map[string]struct{}{"default": {}, "authorized": {}, "vip": {}}, nil
}

// ListUsers returns the fake's account directory, so a sync sees the same
// accounts a GetUser would.
func (f *fakeNewAPI) ListUsers() ([]newapi.User, error) {
	out := make([]newapi.User, 0, len(f.users()))
	for _, u := range f.users() {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeNewAPI) AddQuota(userID, value int) error {
	if f.failQuota != nil {
		return f.failQuota
	}
	f.quotaAdds = append(f.quotaAdds, quotaAdd{userID: userID, value: value})
	return nil
}

// fakeNapCat serves canned rosters, boards and group lists.
type fakeNapCat struct {
	groups []napcat.Group
	// groupListErr makes the group-list read fail, so a caller's error path is
	// exercised without a second fake.
	groupListErr error
	members      map[int64][]napcat.GroupMember
	board        map[int64][]napcat.SignedMember
	boardErr     map[int64]error

	// rosterCalls counts GetGroupMemberList invocations. Reading a roster is the
	// expensive call this system makes (one request per monitored group), so the
	// count is how a test proves a feature did not make it.
	rosterCalls int
}

// memberListCalls reports how many roster reads have happened.
func (f *fakeNapCat) memberListCalls() int { return f.rosterCalls }

// newTestUser builds an account fixture. It carries a non-zero quota so a
// gateway-side condition (余额/请求数) has something to compare against.
func newTestUser(id int, group string) *newapi.User {
	return &newapi.User{
		ID: id, Username: "u" + strconv.Itoa(id), Group: group, Role: 1,
		Quota: 1000, RequestCount: 10,
	}
}

func newFakeNapCat() *fakeNapCat {
	return &fakeNapCat{
		members:  map[int64][]napcat.GroupMember{},
		board:    map[int64][]napcat.SignedMember{},
		boardErr: map[int64]error{},
	}
}

func (f *fakeNapCat) GetGroupList() ([]napcat.Group, error) {
	if f.groupListErr != nil {
		return nil, f.groupListErr
	}
	return f.groups, nil
}

func (f *fakeNapCat) GetGroupMemberList(groupID int64) ([]napcat.GroupMember, error) {
	f.rosterCalls++
	return f.members[groupID], nil
}

func (f *fakeNapCat) GetGroupSignedList(groupID int64) ([]napcat.SignedMember, error) {
	if err := f.boardErr[groupID]; err != nil {
		return nil, err
	}
	return f.board[groupID], nil
}

func newTestService(t *testing.T) (*Service, *store.Store, *fakeNewAPI, *fakeNapCat) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	na := newFakeNewAPI()
	nc := newFakeNapCat()
	svc := New(st, na, nc)
	return svc, st, na, nc
}

func TestSettingsDefaultsAndRoundTrip(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	s, err := svc.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if s.RewardMin != 100 || s.RewardMax != 200 {
		t.Errorf("unexpected default reward range: %d-%d", s.RewardMin, s.RewardMax)
	}
	if s.MemberSyncMinutes <= 0 || s.CheckinSyncMinutes <= 0 {
		t.Errorf("sync intervals must default to positive values: %+v", s)
	}

	s.RewardMax = 500
	s.MemberSyncMinutes = 45
	if err := svc.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got.RewardMax != 500 || got.MemberSyncMinutes != 45 {
		t.Errorf("settings did not persist: %+v", got)
	}
}

func TestSaveSettingsRejectsBadRewardRange(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.RewardMin, s.RewardMax = 300, 100
	if err := svc.SaveSettings(s); err == nil {
		t.Fatal("expected min > max to be rejected")
	}
}

func TestSyncMembersStoresRosterAndGroups(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	nc.members[1129535731] = []napcat.GroupMember{
		{QQ: 54321, Nickname: "alice", Card: "爱丽丝"},
		{QQ: 99999, Nickname: "bob"},
	}

	if err := svc.SyncMembers(1129535731); err != nil {
		t.Fatalf("SyncMembers: %v", err)
	}

	m, err := st.GetMember(54321)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if m.Nickname != "爱丽丝" {
		t.Errorf("card should win as the stored nickname: %q", m.Nickname)
	}

	inGroup, err := st.MembersInGroup(1129535731)
	if err != nil {
		t.Fatal(err)
	}
	if len(inGroup) != 2 {
		t.Errorf("want 2 members in group, got %d", len(inGroup))
	}
}

func TestAutoBindMatchesQQEmail(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "alice"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}

	res, err := svc.AutoBind()
	if err != nil {
		t.Fatalf("AutoBind: %v", err)
	}
	if res.Bound != 1 {
		t.Errorf("want 1 binding, got %+v", res)
	}

	b, err := st.GetBinding(54321)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.NewAPIUserID != 7 || b.Source != "auto" {
		t.Errorf("unexpected binding: %+v", b)
	}
}

func TestAutoBindLeavesUnmatchedMembersPending(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "alice"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}

	res, err := svc.AutoBind()
	if err != nil {
		t.Fatalf("AutoBind: %v", err)
	}
	if res.Bound != 0 || res.Pending != 1 {
		t.Errorf("want 0 bound / 1 pending, got %+v", res)
	}
	if _, err := st.GetBinding(54321); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("no binding should have been created, got err=%v", err)
	}
}

func TestAutoBindRefusesAmbiguousMatch(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "alice"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.ambig["54321@qq.com"] = true

	res, err := svc.AutoBind()
	if err != nil {
		t.Fatalf("AutoBind: %v", err)
	}
	if res.Bound != 0 || res.Ambiguous != 1 {
		t.Errorf("ambiguous match must not bind: %+v", res)
	}
	if _, err := st.GetBinding(54321); !errors.Is(err, store.ErrNotFound) {
		t.Error("ambiguous match must not create a binding")
	}
}

func TestSyncCheckinsRecordsBoard(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	bindForTest(t, svc, st, na, 54321)
	bindForTest(t, svc, st, na, 99999)
	nc.board[1] = []napcat.SignedMember{
		{QQ: 54321, Nick: "爱丽丝", Time: 1791630000, Rank: 1},
		{QQ: 99999, Nick: "bob", Time: 1791630600, Rank: 2},
	}

	n, err := svc.SyncCheckins(1, "2026-10-10")
	if err != nil {
		t.Fatalf("SyncCheckins: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 recorded, got %d", n)
	}
	c, err := st.GetCheckin(54321, "2026-10-10")
	if err != nil {
		t.Fatalf("GetCheckin: %v", err)
	}
	if c.Rank != 1 || c.SignedAt != 1791630000 {
		t.Errorf("unexpected checkin: %+v", c)
	}
}

// The board is today-only and polled repeatedly; a later poll must merge, not
// replace, so members who signed in earlier are not dropped.
func TestSyncCheckinsIsAUnionAcrossPolls(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	bindForTest(t, svc, st, na, 54321)
	bindForTest(t, svc, st, na, 99999)
	nc.board[1] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Rank: 1}}
	if _, err := svc.SyncCheckins(1, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	nc.board[1] = []napcat.SignedMember{{QQ: 99999, Nick: "b", Rank: 1}}
	if _, err := svc.SyncCheckins(1, "2026-10-10"); err != nil {
		t.Fatal(err)
	}

	all, err := st.CheckinsOn("2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("want both members retained, got %d", len(all))
	}
}

// A group with no sign-in board must not be reported as a hard failure for the
// whole sweep.
func TestSyncCheckinsToleratesBoardError(t *testing.T) {
	svc, _, _, nc := newTestService(t)
	nc.boardErr[1] = errors.New("无法获取该群组打卡列表")
	if _, err := svc.SyncCheckins(1, "2026-10-10"); err == nil {
		t.Error("a board error should be reported so the caller can log it")
	}
}

func TestAwardPaysOnceWithinRange(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "a"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	nc.board[1] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Rank: 1}}
	if _, err := svc.SyncCheckins(1, "2026-10-10"); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Award("2026-10-10")
	if err != nil {
		t.Fatalf("Award: %v", err)
	}
	if res.Awarded != 1 {
		t.Fatalf("want 1 award, got %+v", res)
	}
	if len(na.quotaAdds) != 1 {
		t.Fatalf("want 1 quota call, got %+v", na.quotaAdds)
	}
	v := na.quotaAdds[0].value
	if v < 100 || v > 200 {
		t.Errorf("award %d outside configured range 100-200", v)
	}

	c, err := st.GetCheckin(54321, "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardQuota != v || c.RewardedAt == 0 {
		t.Errorf("checkin not marked rewarded: %+v", c)
	}
}

func TestAwardDoesNotPayTwice(t *testing.T) {
	svc, _, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "a"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	nc.board[1] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Rank: 1}}
	if _, err := svc.SyncCheckins(1, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Award("2026-10-10"); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Award("2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if res.Awarded != 0 {
		t.Errorf("second Award must not pay again: %+v", res)
	}
	if len(na.quotaAdds) != 1 {
		t.Errorf("want exactly 1 quota call total, got %d", len(na.quotaAdds))
	}
}

func TestAwardSkipsUnboundMembers(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	// A row with no binding behind it, as an older version would have written.
	if err := st.RecordCheckin(store.Checkin{QQ: 54321, Date: "2026-10-10", GroupID: 1}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Award("2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if res.Awarded != 0 || res.Unbound != 1 {
		t.Errorf("unbound member should be counted, not paid: %+v", res)
	}
	if len(na.quotaAdds) != 0 {
		t.Error("no quota should be granted to an unbound member")
	}
}

// A gateway failure must leave the reward unclaimed so the next sweep retries.
func TestAwardLeavesRewardUnclaimedOnGatewayFailure(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "a"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	nc.board[1] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Rank: 1}}
	if _, err := svc.SyncCheckins(1, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	na.failQuota = errors.New("gateway down")

	res, err := svc.Award("2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 {
		t.Errorf("want 1 failure, got %+v", res)
	}
	c, err := st.GetCheckin(54321, "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardedAt != 0 {
		t.Error("a failed award must stay unclaimed so it retries")
	}
}

func TestAwardFixedRangePaysExactly(t *testing.T) {
	svc, _, na, nc := newTestService(t)
	s, _ := svc.Settings()
	s.RewardMin, s.RewardMax = 77, 77
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "a"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	nc.board[1] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Rank: 1}}
	if _, err := svc.SyncCheckins(1, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Award("2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if len(na.quotaAdds) != 1 || na.quotaAdds[0].value != 77 {
		t.Errorf("min==max must pay exactly: %+v", na.quotaAdds)
	}
}

// The bind command must record the member, not only the binding: with the
// roster patrol switched off, this is the only thing that makes the member
// known to the UI and to the rules engine.
func TestBindByCommandRecordsMember(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}

	if _, err := svc.BindByCommand(1129535731, 54321); err != nil {
		t.Fatalf("BindByCommand: %v", err)
	}
	if _, err := st.GetMember(54321); err != nil {
		t.Errorf("the member who bound must be recorded: %v", err)
	}
	b, err := st.GetBinding(54321)
	if err != nil || b.NewAPIUserID != 7 {
		t.Errorf("binding not stored: %+v err=%v", b, err)
	}
}

func TestEnsureMemberIsIdempotent(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)

	if err := svc.EnsureMember(1129535731, 54321, ""); err != nil {
		t.Fatal(err)
	}
	// A second join event for the same member must not create a duplicate.
	if err := svc.EnsureMember(1129535731, 54321, ""); err != nil {
		t.Fatal(err)
	}
	ms, err := st.MembersInGroup(1129535731)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Errorf("want 1 member, got %d", len(ms))
	}
}

// A later patrol must fill in the nickname the join event could not provide.
func TestEnsureMemberNicknameFilledByLaterSync(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)

	if err := svc.EnsureMember(1129535731, 54321, ""); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetMember(54321)
	if m.Nickname != "" {
		t.Fatalf("precondition: nickname should be empty, got %q", m.Nickname)
	}

	// The roster patrol then sees the member with a real name.
	if err := st.UpsertMembers(1129535731, []store.Member{{QQ: 54321, Nickname: "爱丽丝"}}); err != nil {
		t.Fatal(err)
	}
	m, _ = st.GetMember(54321)
	if m.Nickname != "爱丽丝" {
		t.Errorf("nickname not filled in: %q", m.Nickname)
	}
}

func TestSettingsDefaultPatrolsEnabled(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if !s.MemberPatrolEnabled || !s.CheckinPatrolEnabled {
		t.Errorf("both patrols must default to on: %+v", s)
	}
}

// A group with no sign-in board is a normal condition, not a failure: the
// check-in patrol must not show a permanent error just because a monitored
// group lacks a board.
func TestBoardlessGroupIsNotAFailure(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	nc.boardErr[1] = fmt.Errorf("get_group_signed_list: %w", napcat.ErrNoSigninBoard)

	if err := svc.SyncAllCheckins("2026-10-10"); err != nil {
		t.Errorf("a boardless group must not be reported as a failure: %v", err)
	}
}

// A real failure must still surface, or the patrol would look healthy while
// silently recording nothing.
func TestRealCheckinFailureStillSurfaces(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	nc.boardErr[1] = errors.New("connection refused")

	if err := svc.SyncAllCheckins("2026-10-10"); err == nil {
		t.Error("a genuine failure must be reported")
	}
}
