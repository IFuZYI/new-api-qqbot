package store

import (
	"testing"
)

// An empty roster must not reconcile a group down to nothing.
//
// UpsertMembers' reconciliation is deliberate — a member the group no longer
// reports has left, and their membership row goes — but the EMPTY case is
// different in kind: the bot is itself a member of every group it can read, so
// an empty list means a partial read (NapCat's member cache can be transiently
// empty and still answer retcode 0), not that everyone left. Reconciling it
// blanks the whole group in the panel until the next poll.
//
// The non-empty direction is pinned too, so the guard cannot be mistaken for
// "membership is never removed".
func TestAnEmptyRosterDoesNotClearTheGroup(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertMembers(1, []Member{{QQ: 54321, Nickname: "a"}, {QQ: 99999, Nickname: "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertMembers(1, nil); err != nil {
		t.Fatalf("an empty roster returned an error: %v", err)
	}
	inGroup, err := s.MembersInGroup(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(inGroup) != 2 {
		t.Errorf("an empty roster left %d memberships, want the 2 that were there (a partial read must not blank the group)", len(inGroup))
	}

	// And the reconciliation still works for a real departure.
	if err := s.UpsertMembers(1, []Member{{QQ: 54321, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}
	inGroup, err = s.MembersInGroup(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(inGroup) != 1 || inGroup[0].QQ != 54321 {
		t.Errorf("the reconciliation no longer drops a departed member: %+v", inGroup)
	}
}
