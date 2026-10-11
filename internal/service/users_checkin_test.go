package service

import (
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/store"
)

// The directory tells the operator how each day's check-in was learned. Without
// it, "已签到" cannot be told apart from "signed the board", and the two have
// different meanings when the board patrol is what is being debugged.
func TestUserDirectoryReportsCheckinSource(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)

	// One member checks in by message, one by the board. Both must be in the
	// roster and bound: the directory iterates members, and a check-in only
	// counts for a bound member.
	if err := st.UpsertMembers(1129535731, []store.Member{
		{QQ: 54321, Nickname: "a"}, {QQ: 99999, Nickname: "b"},
	}); err != nil {
		t.Fatal(err)
	}
	bindForTest(t, svc, st, na, 54321)
	bindForTest(t, svc, st, na, 99999)
	if _, err := svc.CheckinByMessage(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	nc.board[1129535731] = []napcat.SignedMember{{QQ: 99999, Nick: "b", Rank: 1}}
	if _, err := svc.SyncCheckins(1129535731, date); err != nil {
		t.Fatal(err)
	}

	rows, err := svc.UserDirectory(UserFilter{}, date)
	if err != nil {
		t.Fatal(err)
	}
	byQQ := map[int64]UserRow{}
	for _, r := range rows {
		byQQ[r.QQ] = r
	}
	if got := byQQ[54321].CheckinSource; got != store.CheckinSourceMessage {
		t.Errorf("message check-in source = %q, want %q", got, store.CheckinSourceMessage)
	}
	if got := byQQ[99999].CheckinSource; got != store.CheckinSourceBoard {
		t.Errorf("board check-in source = %q, want %q", got, store.CheckinSourceBoard)
	}
	if !byQQ[54321].CheckedToday || !byQQ[99999].CheckedToday {
		t.Error("both members should read as checked in today")
	}
}

// A member who has not checked in reports no source at all, so the UI can leave
// the cell empty instead of printing a bogus one.
func TestUserDirectoryHasNoCheckinSourceWhenNotCheckedIn(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	if err := st.UpsertMembers(1129535731, []store.Member{{QQ: 54321, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}

	rows, err := svc.UserDirectory(UserFilter{}, todayOf(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].CheckedToday || rows[0].CheckinSource != "" {
		t.Errorf("a member who has not checked in must report no source: %+v", rows[0])
	}
}
