package service

import (
	"errors"
	"path/filepath"
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
}

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
	if u, ok := f.users()[id]; ok {
		return u, nil
	}
	return nil, errors.New("user not found")
}

func (f *fakeNewAPI) SetGroup(u *newapi.User, group string) error {
	if f.failSet != nil {
		return f.failSet
	}
	f.groupSets = append(f.groupSets, groupSet{userID: u.ID, group: group})
	u.Group = group
	f.users()[u.ID] = u
	return nil
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
	groups   []napcat.Group
	members  map[int64][]napcat.GroupMember
	board    map[int64][]napcat.SignedMember
	boardErr map[int64]error
}

func newFakeNapCat() *fakeNapCat {
	return &fakeNapCat{
		members:  map[int64][]napcat.GroupMember{},
		board:    map[int64][]napcat.SignedMember{},
		boardErr: map[int64]error{},
	}
}

func (f *fakeNapCat) GetGroupList() ([]napcat.Group, error) { return f.groups, nil }

func (f *fakeNapCat) GetGroupMemberList(groupID int64) ([]napcat.GroupMember, error) {
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
	if s.SourceGroup != "default" || s.TargetGroup != "authorized" {
		t.Errorf("unexpected default groups: %+v", s)
	}
	if s.RewardMin != 100 || s.RewardMax != 200 {
		t.Errorf("unexpected default reward range: %d-%d", s.RewardMin, s.RewardMax)
	}
	if s.MemberSyncMinutes <= 0 || s.CheckinSyncMinutes <= 0 {
		t.Errorf("sync intervals must default to positive values: %+v", s)
	}

	s.TargetGroup = "vip"
	s.RewardMax = 500
	if err := svc.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetGroup != "vip" || got.RewardMax != 500 {
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

func TestPromoteOnlyMovesSourceGroup(t *testing.T) {
	svc, _, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{
		{QQ: 111, Nickname: "in-default"},
		{QQ: 222, Nickname: "already-authorized"},
		{QQ: 333, Nickname: "vip"},
	}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.byEmail["111@qq.com"] = &newapi.User{ID: 11, Username: "u11", Group: "default", Role: 1}
	na.byEmail["222@qq.com"] = &newapi.User{ID: 22, Username: "u22", Group: "authorized", Role: 1}
	na.byEmail["333@qq.com"] = &newapi.User{ID: 33, Username: "u33", Group: "vip", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Promote()
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.Promoted != 1 {
		t.Errorf("want exactly 1 promotion, got %+v", res)
	}
	if len(na.groupSets) != 1 || na.groupSets[0].userID != 11 || na.groupSets[0].group != "authorized" {
		t.Errorf("wrong promotion: %+v", na.groupSets)
	}
}

// Re-running promotion must not issue redundant writes, or a 2-hour sweep would
// hammer the gateway.
func TestPromoteIsIdempotent(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.byEmail["111@qq.com"] = &newapi.User{ID: 11, Username: "u11", Group: "default", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Promote(); err != nil {
		t.Fatal(err)
	}
	na.groupSets = nil

	// The fake updates the user in place, mirroring the gateway.
	res, err := svc.Promote()
	if err != nil {
		t.Fatal(err)
	}
	if res.Promoted != 0 || len(na.groupSets) != 0 {
		t.Errorf("second pass should be a no-op, got %+v sets=%+v", res, na.groupSets)
	}
	_ = st
}

func TestPromoteSkipsUnboundMembers(t *testing.T) {
	svc, _, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "nobody"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Promote()
	if err != nil {
		t.Fatal(err)
	}
	if res.Promoted != 0 || len(na.groupSets) != 0 {
		t.Errorf("unbound members must not be promoted: %+v", res)
	}
}

func TestPromoteReportsFailureWithoutAborting(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}, {QQ: 222, Nickname: "b"}}
	if err := svc.SyncMembers(1); err != nil {
		t.Fatal(err)
	}
	na.byEmail["111@qq.com"] = &newapi.User{ID: 11, Username: "u11", Group: "default", Role: 1}
	na.byEmail["222@qq.com"] = &newapi.User{ID: 22, Username: "u22", Group: "default", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	na.failSet = errors.New("boom")

	res, err := svc.Promote()
	if err != nil {
		t.Fatalf("Promote should not abort on one failure: %v", err)
	}
	if res.Failed != 2 {
		t.Errorf("want 2 failures recorded, got %+v", res)
	}
	_ = st
}

func TestSyncCheckinsRecordsBoard(t *testing.T) {
	svc, st, _, nc := newTestService(t)
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
	svc, st, _, nc := newTestService(t)
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
	svc, _, na, nc := newTestService(t)
	nc.board[1] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Rank: 1}}
	if _, err := svc.SyncCheckins(1, "2026-10-10"); err != nil {
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
