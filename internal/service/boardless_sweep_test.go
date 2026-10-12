package service

import (
	"errors"
	"fmt"
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/store"
)

// One boardless group must not stop the others from being recorded.
//
// Every existing test of this uses a SINGLE group, so the property the sentinel
// exists for — a group without a sign-in board is normal, skip it and carry on —
// was never exercised. A sweep that aborted on the first boardless group would
// silently stop recording every later group's board, and the reward loop would
// then pay only the groups that happened to sort first.
func TestABoardlessGroupDoesNotStopTheSweep(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	// Three groups: boardless, then a real failure, then a healthy one. The
	// healthy one is deliberately LAST, so an abort anywhere earlier would hide
	// its board.
	for _, id := range []int64{1, 2, 3} {
		if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: id, GroupName: fmt.Sprintf("g%d", id)}}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetGroupEnabled(id, true); err != nil {
			t.Fatal(err)
		}
	}
	// Group 1 has no board at all.
	nc.boardErr[1] = fmt.Errorf("get_group_signed_list: %w", napcat.ErrNoSigninBoard)
	// Group 2 fails for a real reason.
	nc.boardErr[2] = errors.New("napcat 500")
	// Group 3 answers normally, with a member who has an account: a check-in is
	// only recorded for a bound member (the reward needs an account to pay).
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	nc.board[3] = []napcat.SignedMember{{QQ: 54321, Time: 1}}

	err := svc.SyncAllCheckins("2026-10-12")
	// The boardless group is not a failure, the real one is.
	if err == nil {
		t.Error("group 2's real failure must be reported")
	}
	if err != nil && errors.Is(err, napcat.ErrNoSigninBoard) {
		t.Errorf("the boardless group was reported as a failure: %v", err)
	}

	// The healthy group's board must have been recorded despite the earlier
	// problems.
	c, err := st.GetCheckin(54321, "2026-10-12")
	if err != nil {
		t.Fatalf("the healthy group's board was never recorded: %v", err)
	}
	if c.Source != "board" {
		t.Errorf("source = %q, want board", c.Source)
	}
}

// The sentinel must survive the failover join, or a boardless group reads as a
// transport failure (the WebUI fallback answers the same way).
func TestTheBoardlessSentinelSurvivesFailover(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	// Both transports answer "no board".
	nc.boardErr[1] = fmt.Errorf("get_group_signed_list: %w", napcat.ErrNoSigninBoard)

	// A sweep over one boardless group is a clean no-op.
	if err := svc.SyncAllCheckins("2026-10-12"); err != nil {
		t.Errorf("a boardless group must be a clean no-op, got: %v", err)
	}
}
