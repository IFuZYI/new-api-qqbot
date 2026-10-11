package service

import (
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// todayOf returns the service's notion of today, so a test can assert against
// the same date the service recorded.
func todayOf(t *testing.T, svc *Service) string {
	t.Helper()
	d, err := svc.Today()
	if err != nil {
		t.Fatalf("Today: %v", err)
	}
	return d
}

// A message is a check-in in its own right: it carries no rank and no sign-in
// time, and it must still be recorded.
func TestCheckinByMessageRecords(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)

	res, err := svc.CheckinByMessage(1129535731, 54321)
	if err != nil {
		t.Fatalf("CheckinByMessage: %v", err)
	}
	if !res.Recorded {
		t.Fatalf("the first message of the day must be recorded: %+v", res)
	}

	c, err := st.GetCheckin(54321, todayOf(t, svc))
	if err != nil {
		t.Fatalf("GetCheckin: %v", err)
	}
	if c.Source != store.CheckinSourceMessage {
		t.Errorf("check-in source = %q, want %q", c.Source, store.CheckinSourceMessage)
	}
	// The member is recorded too: the message proves they are in the group.
	if _, err := st.GetMember(54321); err != nil {
		t.Errorf("the member who checked in must be recorded: %v", err)
	}
}

// A day is checked in once. Every further message that day is a no-op, which is
// what stops a chatty member from farming rewards.
func TestCheckinByMessageIsIdempotentPerDay(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)

	first, err := svc.CheckinByMessage(1129535731, 54321)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Recorded {
		t.Fatalf("precondition: first check-in should be recorded: %+v", first)
	}
	for i := 0; i < 3; i++ {
		again, err := svc.CheckinByMessage(1129535731, 54321)
		if err != nil {
			t.Fatal(err)
		}
		if again.Recorded {
			t.Errorf("call %d re-recorded the same day: %+v", i+2, again)
		}
	}
	all, err := st.CheckinsOn(todayOf(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("want exactly 1 check-in row for the day, got %d", len(all))
	}
}

// The board is the richer record: when it arrives after a message-driven
// check-in, it must upgrade the row (rank, sign-in time, source) rather than be
// ignored.
func TestBoardUpgradesMessageCheckin(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)

	if _, err := svc.CheckinByMessage(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	nc.board[1129535731] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Time: 1791630000, Rank: 3}}
	if _, err := svc.SyncCheckins(1129535731, date); err != nil {
		t.Fatal(err)
	}

	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != store.CheckinSourceBoard {
		t.Errorf("board data should win: source = %q, want %q", c.Source, store.CheckinSourceBoard)
	}
	if c.Rank != 3 || c.SignedAt != 1791630000 {
		t.Errorf("board details not stored: %+v", c)
	}
}

// The reverse order must not lose anything either: a message after the board
// must not blank the rank or the sign-in time the board reported.
func TestMessageCheckinDoesNotClobberBoardData(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)

	nc.board[1129535731] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Time: 1791630000, Rank: 7}}
	if _, err := svc.SyncCheckins(1129535731, date); err != nil {
		t.Fatal(err)
	}
	res, err := svc.CheckinByMessage(1129535731, 54321)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recorded {
		t.Error("a message must not re-record a day the board already recorded")
	}

	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rank != 7 || c.SignedAt != 1791630000 || c.Source != store.CheckinSourceBoard {
		t.Errorf("board data was clobbered by a later message: %+v", c)
	}
}

// A message check-in must not reset a reward that was already paid.
func TestMessageCheckinKeepsPaidReward(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)

	if err := st.UpsertMembers(1129535731, []store.Member{{QQ: 54321, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckinByMessage(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Award(date); err != nil {
		t.Fatal(err)
	}
	paid, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if paid.RewardedAt == 0 {
		t.Fatal("precondition: the reward should have been paid")
	}

	if _, err := svc.CheckinByMessage(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if after.RewardedAt != paid.RewardedAt || after.RewardQuota != paid.RewardQuota {
		t.Errorf("a later message disturbed the paid reward: before=%+v after=%+v", paid, after)
	}
}

// A message-driven check-in is paid like any other: that is the whole point of
// counting messages.
func TestAwardPaysMessageCheckin(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)

	if err := st.UpsertMembers(1129535731, []store.Member{{QQ: 54321, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckinByMessage(1129535731, 54321); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Award(date)
	if err != nil {
		t.Fatal(err)
	}
	if res.Awarded != 1 {
		t.Errorf("a message-driven check-in must be paid: %+v", res)
	}
	if len(na.quotaAdds) != 1 {
		t.Errorf("want 1 quota call, got %+v", na.quotaAdds)
	}
}

// A zero QQ or group (a malformed event) must be refused rather than recorded
// under a meaningless key.
func TestCheckinByMessageRejectsZeroIdentity(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	if _, err := svc.CheckinByMessage(0, 54321); err == nil {
		t.Error("a zero group id must be refused")
	}
	if _, err := svc.CheckinByMessage(1, 0); err == nil {
		t.Error("a zero QQ must be refused")
	}
}

// Recording a check-in for a member must not fail just because the group is not
// monitored: the member may have been recorded from a monitored group and be
// seen in another. Filtering groups is the listener's job.
func TestCheckinByMessageWorksForUnknownGroup(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	if _, err := svc.CheckinByMessage(999, 54321); err != nil {
		t.Fatalf("CheckinByMessage: %v", err)
	}
	if _, err := st.GetCheckin(54321, todayOf(t, svc)); err != nil {
		t.Errorf("the check-in should have been recorded: %v", err)
	}
}

// A store failure must surface, not be swallowed into a silent "not recorded".
func TestCheckinByMessageReportsStoreFailure(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckinByMessage(1, 54321); err == nil {
		t.Error("a closed store must surface as an error")
	}
}
