package service

import (
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/store"
)

// A check-in only counts for a member who has an account bound: the reward is
// paid to the bound new-api account, so an unbound member's check-in can never
// be paid, and recording it would put a "已签到" row in front of the operator
// that no reward can ever clear.
func TestMessageCheckinRequiresABinding(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)

	res, err := svc.CheckinByMessage(1129535731, 54321)
	if err != nil {
		t.Fatalf("CheckinByMessage: %v", err)
	}
	if res.Recorded {
		t.Errorf("an unbound member must not be recorded: %+v", res)
	}
	if !res.Unbound {
		t.Errorf("the result must say why it was not recorded: %+v", res)
	}
	if _, err := st.GetCheckin(54321, todayOf(t, svc)); err == nil {
		t.Error("no check-in row may exist for an unbound member")
	}
}

// The member is still recorded as a member: the message proves they are in the
// group, and the patrol needs to see them to bind them later.
func TestMessageCheckinStillRecordsTheMember(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)

	if _, err := svc.CheckinByMessage(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetMember(54321); err != nil {
		t.Errorf("the member must still be recorded: %v", err)
	}
}

// Once the member is bound, the same message records the check-in.
func TestMessageCheckinWorksAfterBinding(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatal(err)
	}

	res, err := svc.CheckinByMessage(1129535731, 54321)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Recorded {
		t.Errorf("a bound member's check-in must be recorded: %+v", res)
	}
	if _, err := st.GetCheckin(54321, todayOf(t, svc)); err != nil {
		t.Errorf("the check-in should be in the store: %v", err)
	}
}

// A binding that is deleted (the operator unbinds) must stop new check-ins.
func TestMessageCheckinStopsWhenTheBindingIsRemoved(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBinding(54321); err != nil {
		t.Fatal(err)
	}

	res, err := svc.CheckinByMessage(1129535731, 54321)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recorded || !res.Unbound {
		t.Errorf("an unbound member must not be recorded: %+v", res)
	}
}

// The sign-in board path has the same rule: a board entry for an unbound member
// is not a check-in here, because it cannot be paid.
func TestBoardCheckinRequiresABinding(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)

	nc.board[1129535731] = []napcat.SignedMember{
		{QQ: 54321, Nick: "a", Time: 1791630000, Rank: 1}, // unbound
	}
	if _, err := svc.SyncCheckins(1129535731, date); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCheckin(54321, date); err == nil {
		t.Error("an unbound member's board entry must not be recorded as a check-in")
	}
}

// And a bound member's board entry is recorded, so the gate is not suppressing
// the path entirely.
func TestBoardCheckinRecordsBoundMember(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatal(err)
	}

	nc.board[1129535731] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Time: 1791630000, Rank: 3}}
	if _, err := svc.SyncCheckins(1129535731, date); err != nil {
		t.Fatal(err)
	}
	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatalf("a bound member's board entry must be recorded: %v", err)
	}
	if c.Source != store.CheckinSourceBoard || c.Rank != 3 {
		t.Errorf("unexpected row: %+v", c)
	}
}

// A member who was bound, checked in, and is then unbound keeps the day they
// already earned: the gate stops new check-ins, it does not retract history.
func TestUnbindingDoesNotEraseAnExistingCheckin(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckinByMessage(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBinding(54321); err != nil {
		t.Fatal(err)
	}

	if _, err := st.GetCheckin(54321, date); err != nil {
		t.Errorf("the recorded check-in must survive an unbind: %v", err)
	}
}

// The award sweep must not pay an unbound member whose row predates the rule
// (a row from before this change, or one the operator unbound afterwards).
func TestAwardSkipsUnboundCheckinRows(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)

	// A row with no binding behind it, as an older version would have written.
	if err := st.RecordCheckin(store.Checkin{QQ: 54321, Date: date, GroupID: 1129535731}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Award(date)
	if err != nil {
		t.Fatal(err)
	}
	if res.Awarded != 0 || res.Unbound != 1 {
		t.Errorf("an unbound check-in must not be paid: %+v", res)
	}
	if len(na.quotaAdds) != 0 {
		t.Errorf("no quota may be granted: %+v", na.quotaAdds)
	}
}

// The operator's workflow: an unbound member's board entry is skipped, the bind
// patrol binds them, and the next poll of the same day records it. Nothing about
// the earlier skip is remembered.
func TestBoardCheckinIsPickedUpAfterBinding(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)
	nc.board[1129535731] = []napcat.SignedMember{{QQ: 54321, Nick: "a", Time: 1791630000, Rank: 2}}

	// Before binding: not a check-in, but the member is now known.
	if _, err := svc.SyncCheckins(1129535731, date); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCheckin(54321, date); err == nil {
		t.Fatal("precondition: an unbound member's board entry must not be recorded")
	}
	if _, err := st.GetMember(54321); err != nil {
		t.Fatalf("the board should still make the member known: %v", err)
	}

	// Bind, then poll again: the same day's entry is now recorded.
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncCheckins(1129535731, date); err != nil {
		t.Fatal(err)
	}
	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatalf("after binding, the board entry must be recorded: %v", err)
	}
	if c.Source != store.CheckinSourceBoard || c.Rank != 2 {
		t.Errorf("unexpected row: %+v", c)
	}
}
