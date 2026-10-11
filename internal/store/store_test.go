package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenMigratesAllTables(t *testing.T) {
	s := newTestStore(t)
	for _, tbl := range []string{
		"monitored_groups", "members", "member_groups",
		"bindings", "checkins", "settings", "admins",
	} {
		if !s.DB().Migrator().HasTable(tbl) {
			t.Errorf("table %q was not created", tbl)
		}
	}
}

// Members are keyed by QQ and upserted, so re-syncing the same group twice
// must not duplicate rows or lose the group membership.
func TestUpsertMembersIsIdempotent(t *testing.T) {
	s := newTestStore(t)

	ms := []Member{
		{QQ: 54321, Nickname: "alice"},
		{QQ: 99999, Nickname: "bob"},
	}
	if err := s.UpsertMembers(1129535731, ms); err != nil {
		t.Fatalf("UpsertMembers: %v", err)
	}
	// second sync: nickname changed, one member left the group
	if err := s.UpsertMembers(1129535731, []Member{{QQ: 54321, Nickname: "alice2"}}); err != nil {
		t.Fatalf("UpsertMembers 2: %v", err)
	}

	all, err := s.ListMembers()
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 members, got %d", len(all))
	}

	m, err := s.GetMember(54321)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if m.Nickname != "alice2" {
		t.Errorf("nickname not updated: %q", m.Nickname)
	}

	// QQ 99999 is no longer in the group, but the member row must survive.
	inGroup, err := s.MembersInGroup(1129535731)
	if err != nil {
		t.Fatalf("MembersInGroup: %v", err)
	}
	if len(inGroup) != 1 || inGroup[0].QQ != 54321 {
		t.Errorf("group membership not reconciled: %+v", inGroup)
	}
}

func TestBindingIsOnePerQQAndReplaceable(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertMembers(1, []Member{{QQ: 54321, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetBinding(Binding{
		QQ: 54321, NewAPIUserID: 7, NewAPIUsername: "seven",
		NewAPIEmail: "54321@qq.com", Source: "auto",
	}); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	// Re-binding (换绑) replaces the target rather than adding a row.
	if err := s.SetBinding(Binding{
		QQ: 54321, NewAPIUserID: 8, NewAPIUsername: "eight",
		NewAPIEmail: "other@qq.com", Source: "manual",
	}); err != nil {
		t.Fatalf("SetBinding replace: %v", err)
	}

	b, err := s.GetBinding(54321)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.NewAPIUserID != 8 || b.Source != "manual" {
		t.Errorf("binding not replaced: %+v", b)
	}

	n, err := s.CountBindings()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("want 1 binding, got %d", n)
	}
}

// The sign-in board is today-only and polled repeatedly, so recording the same
// (qq, date) twice must be a no-op that preserves the first award decision.
func TestCheckinIsIdempotentPerQQPerDate(t *testing.T) {
	s := newTestStore(t)

	c := Checkin{QQ: 54321, Date: "2026-10-10", GroupID: 1, SignedAt: 100, Rank: 3, RewardQuota: 150}
	if err := s.RecordCheckin(c); err != nil {
		t.Fatalf("RecordCheckin: %v", err)
	}
	// a later poll reports the same member again, possibly with a new rank
	c.Rank = 1
	c.RewardQuota = 999
	if err := s.RecordCheckin(c); err != nil {
		t.Fatalf("RecordCheckin 2: %v", err)
	}

	got, err := s.GetCheckin(54321, "2026-10-10")
	if err != nil {
		t.Fatalf("GetCheckin: %v", err)
	}
	if got.RewardQuota != 150 {
		t.Errorf("reward was recomputed on re-record: %d", got.RewardQuota)
	}

	n, err := s.CountCheckins(54321)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("want 1 checkin row, got %d", n)
	}
}

func TestCheckinDaysCountsDistinctDates(t *testing.T) {
	s := newTestStore(t)
	for _, d := range []string{"2026-10-08", "2026-10-09", "2026-10-10"} {
		if err := s.RecordCheckin(Checkin{QQ: 54321, Date: d, GroupID: 1, RewardQuota: 100}); err != nil {
			t.Fatal(err)
		}
	}
	// re-poll on the same day must not inflate the day count
	if err := s.RecordCheckin(Checkin{QQ: 54321, Date: "2026-10-10", GroupID: 1, RewardQuota: 100}); err != nil {
		t.Fatal(err)
	}

	days, err := s.CountCheckins(54321)
	if err != nil {
		t.Fatal(err)
	}
	if days != 3 {
		t.Errorf("want 3 checkin days, got %d", days)
	}
}

// The reward is claimed once per (qq, date): the sign-in board is polled
// repeatedly, so a second claim (retry, restart, duplicate poll) must not pay
// again. ClaimReward does the claim and the amount in one statement, so this
// also pins that the amount survives the losing attempt rather than being
// overwritten by a second draw.
func TestClaimRewardIsOnceOnly(t *testing.T) {
	s := newTestStore(t)
	if err := s.RecordCheckin(Checkin{QQ: 54321, Date: "2026-10-10", GroupID: 1}); err != nil {
		t.Fatal(err)
	}

	ok, err := s.ClaimReward(54321, "2026-10-10", 150, time.Now())
	if err != nil {
		t.Fatalf("ClaimReward: %v", err)
	}
	if !ok {
		t.Fatal("first ClaimReward should report the award was claimed")
	}

	// A second attempt must lose, and must not touch the recorded amount.
	ok, err = s.ClaimReward(54321, "2026-10-10", 999, time.Now())
	if err != nil {
		t.Fatalf("ClaimReward 2: %v", err)
	}
	if ok {
		t.Error("second ClaimReward must not claim the award again")
	}
	c, err := s.GetCheckin(54321, "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardQuota != 150 {
		t.Errorf("the losing claim overwrote the amount: got %d, want 150", c.RewardQuota)
	}
	if c.RewardedAt == 0 {
		t.Error("the paid claim should be recorded")
	}
}

// A released claim is claimable again, and the release clears the amount so the
// retry draws afresh — this is the path a failed gateway call takes.
func TestReleaseRewardMakesItClaimableAgain(t *testing.T) {
	s := newTestStore(t)
	if err := s.RecordCheckin(Checkin{QQ: 54321, Date: "2026-10-10", GroupID: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimReward(54321, "2026-10-10", 150, time.Now()); err != nil || !ok {
		t.Fatalf("precondition: ClaimReward = %v, %v", ok, err)
	}
	if err := s.ReleaseReward(54321, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCheckin(54321, "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardedAt != 0 || c.RewardQuota != 0 {
		t.Errorf("release left the claim in place: %+v", c)
	}
	ok, err := s.ClaimReward(54321, "2026-10-10", 200, time.Now())
	if err != nil || !ok {
		t.Fatalf("the retry must be able to claim: ok=%v err=%v", ok, err)
	}
	c, err = s.GetCheckin(54321, "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardQuota != 200 {
		t.Errorf("the retry did not draw afresh: got %d, want 200", c.RewardQuota)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s := newTestStore(t)

	var out struct {
		Min int `json:"min"`
		Max int `json:"max"`
	}
	if err := s.GetSetting("reward", &out); err != nil {
		t.Fatalf("GetSetting missing: %v", err)
	}
	if out.Min != 0 || out.Max != 0 {
		t.Errorf("missing setting should leave the target zeroed, got %+v", out)
	}

	if err := s.PutSetting("reward", map[string]int{"min": 100, "max": 200}); err != nil {
		t.Fatalf("PutSetting: %v", err)
	}
	if err := s.GetSetting("reward", &out); err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if out.Min != 100 || out.Max != 200 {
		t.Errorf("round trip failed: %+v", out)
	}
}

// The member and check-in patrols write from separate goroutines. Without a
// busy timeout the second writer fails immediately with SQLITE_BUSY and its
// entire sweep is lost, so concurrent writers must queue instead.
func TestConcurrentWritersDoNotFailWithBusy(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertGroups([]MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				qq := int64(i*1000 + j)
				if err := s.EnsureMember(1, qq, "n"); err != nil {
					errs <- err
					return
				}
				if err := s.RecordCheckin(Checkin{QQ: qq, Date: "2026-10-10", GroupID: 1}); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent writer failed: %v", err)
	}

	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 96 {
		t.Errorf("member count = %d, want 96 (writes were lost)", len(ms))
	}
}

// A newly discovered group must be stored disabled: the bot can be invited into
// unrelated groups, and monitoring one means recording its members and paying
// them, so it takes an explicit choice. UpsertGroups must also preserve a
// choice already made, or every refresh would silently re-enable a group the
// operator turned off.
func TestNewGroupIsDisabledAndSelectionIsPreserved(t *testing.T) {
	s := newTestStore(t)

	if err := s.UpsertGroups([]MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	gs, err := s.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 {
		t.Fatalf("want 1 group, got %d", len(gs))
	}
	if gs[0].Enabled {
		t.Error("a newly discovered group must default to disabled")
	}

	// Opt in, then refresh the list: the choice must survive.
	if err := s.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGroups([]MonitoredGroup{{GroupID: 1, GroupName: "g-renamed", MemberCount: 5}}); err != nil {
		t.Fatal(err)
	}
	gs, err = s.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if !gs[0].Enabled {
		t.Error("a refresh reverted the operator's enabled choice")
	}
	if gs[0].GroupName != "g-renamed" || gs[0].MemberCount != 5 {
		t.Errorf("refresh did not update the metadata: %+v", gs[0])
	}

	// And a group turned off must stay off across a refresh.
	if err := s.SetGroupEnabled(1, false); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGroups([]MonitoredGroup{{GroupID: 1, GroupName: "g-renamed", MemberCount: 6}}); err != nil {
		t.Fatal(err)
	}
	gs, _ = s.ListGroups()
	if gs[0].Enabled {
		t.Error("a refresh re-enabled a group the operator turned off")
	}
}
