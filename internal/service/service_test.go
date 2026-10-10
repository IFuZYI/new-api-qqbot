package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

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

// fakeNapCat serves canned rosters and group lists.
type fakeNapCat struct {
	groups  []napcat.Group
	members map[int64][]napcat.GroupMember
}

func newFakeNapCat() *fakeNapCat {
	return &fakeNapCat{
		members: map[int64][]napcat.GroupMember{},
	}
}

func (f *fakeNapCat) GetGroupList() ([]napcat.Group, error) { return f.groups, nil }

func (f *fakeNapCat) GetGroupMemberList(groupID int64) ([]napcat.GroupMember, error) {
	return f.members[groupID], nil
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
	if s.MemberSyncMinutes <= 0 {
		t.Errorf("sync interval must default to a positive value: %+v", s)
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

// A check-in is recorded under the date the message arrived on *in the
// configured timezone*, not the UTC date: 2026-10-09T20:00Z is already the
// 10th in Asia/Shanghai, and the day boundary must match Today().
func TestRecordCheckinByMessageUsesConfiguredTimezone(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	at := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)

	if err := svc.RecordCheckinByMessage(1129535731, 54321, at); err != nil {
		t.Fatalf("RecordCheckinByMessage: %v", err)
	}
	c, err := st.GetCheckin(54321, "2026-10-10")
	if err != nil {
		t.Fatalf("GetCheckin: %v", err)
	}
	if c.GroupID != 1129535731 || c.SignedAt != at.Unix() {
		t.Errorf("unexpected checkin: %+v", c)
	}

	// Change the timezone: the same instant must now land on the 9th. The
	// stored date follows the configuration, so the boundary is operator-set.
	s, _ := svc.Settings()
	s.Timezone = "UTC"
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCheckinByMessage(1129535731, 99999, at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCheckin(99999, "2026-10-09"); err != nil {
		t.Errorf("the configured timezone must decide the date: %v", err)
	}
}

// Repeat messages on the same day collapse into one row. The store's upsert
// refreshes the observed time (signed_at/group_id follow the latest message)
// but never touches the reward fields, so a day's chatter cannot re-randomise
// or re-award what was already paid.
func TestRecordCheckinByMessageIsIdempotentPerDay(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	first := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	second := time.Date(2026, 10, 10, 9, 30, 0, 0, time.UTC)

	if err := svc.RecordCheckinByMessage(1, 54321, first); err != nil {
		t.Fatal(err)
	}
	// Simulate the award having been paid for the day's first message.
	if _, err := st.ClaimReward(54321, "2026-10-10", 150, first); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCheckinByMessage(1, 54321, second); err != nil {
		t.Fatal(err)
	}

	all, err := st.CheckinsOn("2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("a second message the same day must not add a row, got %d", len(all))
	}
	if all[0].SignedAt != second.Unix() {
		t.Errorf("the upsert refreshes the observed time: signed_at = %d, want %d",
			all[0].SignedAt, second.Unix())
	}
	if all[0].RewardQuota != 150 || all[0].RewardedAt == 0 {
		t.Errorf("re-recording must not touch the reward: %+v", all[0])
	}
}

// A message on another day is a new check-in.
func TestRecordCheckinByMessageAcrossDays(t *testing.T) {
	svc, st, _, _ := newTestService(t)

	if err := svc.RecordCheckinByMessage(1, 54321, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCheckinByMessage(1, 54321, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	days, err := st.CountCheckins(54321)
	if err != nil {
		t.Fatal(err)
	}
	if days != 2 {
		t.Errorf("a message on a new day must record a second row, got %d", days)
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
	if err := svc.RecordCheckinByMessage(1, 54321, time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)); err != nil {
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
	if err := svc.RecordCheckinByMessage(1, 54321, time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)); err != nil {
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
	svc, _, na, _ := newTestService(t)
	if err := svc.RecordCheckinByMessage(1, 54321, time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)); err != nil {
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
	if err := svc.RecordCheckinByMessage(1, 54321, time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)); err != nil {
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
	if err := svc.RecordCheckinByMessage(1, 54321, time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)); err != nil {
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
	if !s.MemberPatrolEnabled {
		t.Errorf("the member patrol must default to on: %+v", s)
	}
}

// A stored settings blob from before check-ins became message-driven still
// carries checkin_patrol_enabled/checkin_sync_minutes. Go's json.Unmarshal
// ignores unknown fields, so loading it must not fail and must not resurrect
// any check-in setting: the struct has none.
func TestLegacySettingsBlobLoadsWithoutCheckinFields(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	legacy := json.RawMessage(`{"member_patrol_enabled":true,"member_sync_minutes":90,` +
		`"checkin_patrol_enabled":true,"checkin_sync_minutes":30,` +
		`"reward_min":10,"reward_max":20,"timezone":"Asia/Shanghai"}`)
	if err := st.PutSetting("settings", legacy); err != nil {
		t.Fatal(err)
	}

	s, err := svc.Settings()
	if err != nil {
		t.Fatalf("an old blob must still load: %v", err)
	}
	if s.MemberSyncMinutes != 90 || s.RewardMin != 10 || s.RewardMax != 20 {
		t.Errorf("known fields must survive: %+v", s)
	}
}
