package store

import "testing"

// attachGroups must attach every membership of the members it returns, and only
// those members' memberships.
//
// It previously read the whole member_groups table and filtered in Go, so every
// panel request read rows for members it was not returning. Scoping the query
// is only safe if the attachment is still complete: a member in several groups
// must still show all of them.
func TestAttachGroupsIsCompleteAndScoped(t *testing.T) {
	s := newTestStore(t)
	// One member in three groups, another in one, and a third member in a group
	// of its own that must not leak into either listing.
	for _, g := range []int64{1, 2, 3} {
		if err := s.UpsertMembers(g, []Member{{QQ: 111, Nickname: "multi"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertMembers(4, []Member{{QQ: 222, Nickname: "single"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertMembers(5, []Member{{QQ: 333, Nickname: "other"}}); err != nil {
		t.Fatal(err)
	}

	// The whole list: every member keeps all its memberships.
	all, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	byQQ := map[int64][]int64{}
	for _, m := range all {
		byQQ[m.QQ] = m.GroupIDs
	}
	if got := len(byQQ[111]); got != 3 {
		t.Errorf("member 111 has %d memberships, want 3: %+v", got, byQQ[111])
	}
	if got := len(byQQ[222]); got != 1 {
		t.Errorf("member 222 has %d memberships, want 1: %+v", got, byQQ[222])
	}

	// A scoped listing: the members of group 4 are just 222, and 222 must still
	// carry its own membership.
	inGroup, err := s.MembersInGroup(4)
	if err != nil {
		t.Fatal(err)
	}
	if len(inGroup) != 1 || inGroup[0].QQ != 222 {
		t.Fatalf("MembersInGroup(4) = %+v, want just 222", inGroup)
	}
	if len(inGroup[0].GroupIDs) != 1 || inGroup[0].GroupIDs[0] != 4 {
		t.Errorf("member 222's memberships = %+v, want [4]", inGroup[0].GroupIDs)
	}
}
