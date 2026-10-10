package store

import (
	"path/filepath"
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

func TestMarkRewardedIsOnceOnly(t *testing.T) {
	s := newTestStore(t)
	if err := s.RecordCheckin(Checkin{QQ: 54321, Date: "2026-10-10", GroupID: 1, RewardQuota: 150}); err != nil {
		t.Fatal(err)
	}

	ok, err := s.MarkRewarded(54321, "2026-10-10", time.Now())
	if err != nil {
		t.Fatalf("MarkRewarded: %v", err)
	}
	if !ok {
		t.Fatal("first MarkRewarded should report the award was claimed")
	}
	// a second attempt (retry, restart, duplicate poll) must not re-award
	ok, err = s.MarkRewarded(54321, "2026-10-10", time.Now())
	if err != nil {
		t.Fatalf("MarkRewarded 2: %v", err)
	}
	if ok {
		t.Error("second MarkRewarded must not claim the award again")
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
