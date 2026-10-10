package service

import (
	"strings"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// seedUserDirectory sets up a store with two members (one bound, one not) and
// three new-api accounts (one bound, two nobody joined with).
func seedUserDirectory(t *testing.T) (*Service, *store.Store, *fakeNewAPI) {
	t.Helper()
	svc, st, na, _ := newTestService(t)

	if err := st.UpsertMembers(100, []store.Member{
		{QQ: 111, Nickname: "alice"},
		{QQ: 222, Nickname: "bob"},
	}); err != nil {
		t.Fatalf("UpsertMembers: %v", err)
	}
	// 333 was in a group before and is now gone, but the member row survives.
	if err := st.EnsureMember(100, 333, "carol"); err != nil {
		t.Fatalf("EnsureMember: %v", err)
	}
	// Reconcile group 100 to only 111 and 222: 333 drops out of the group but
	// keeps their member row, which is the "left" case.
	if err := st.UpsertMembers(100, []store.Member{
		{QQ: 111, Nickname: "alice"},
		{QQ: 222, Nickname: "bob"},
	}); err != nil {
		t.Fatalf("UpsertMembers (2): %v", err)
	}

	// Accounts: 1 is bound to 111, 2 and 3 are nobody's.
	na.users()[1] = &newapi.User{ID: 1, Username: "alice", Email: "111@qq.com", Group: "default", Status: 1}
	na.users()[2] = &newapi.User{ID: 2, Username: "stranger", Email: "999@qq.com", Group: "authorized", Status: 1}
	na.users()[3] = &newapi.User{ID: 3, Username: "someone", Email: "888@qq.com", Group: "vip", Status: 1}

	if err := st.SetBinding(store.Binding{
		QQ: 111, NewAPIUserID: 1, NewAPIUsername: "alice",
		NewAPIEmail: "111@qq.com", Source: "auto",
	}); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	if _, err := svc.SyncNewAPIUsers(); err != nil {
		t.Fatalf("SyncNewAPIUsers: %v", err)
	}
	return svc, st, na
}

func TestUserDirectoryUnionsMembersAndAccounts(t *testing.T) {
	svc, _, _ := seedUserDirectory(t)

	rows, err := svc.UserDirectory(UserFilter{}, "2026-01-01")
	if err != nil {
		t.Fatalf("UserDirectory: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("want 5 rows (3 members + 2 account-only), got %d: %+v", len(rows), rows)
	}

	byQQ := map[int64]UserRow{}
	for _, r := range rows {
		byQQ[r.QQ] = r
	}

	if r := byQQ[111]; !r.Bound || r.NewAPIGroup != "default" || !r.InGroup {
		t.Errorf("111 should be a bound, in-group member on 'default': %+v", r)
	}
	if r := byQQ[222]; r.Bound || r.Kind != "member" {
		t.Errorf("222 should be an unbound member: %+v", r)
	}
	if r := byQQ[333]; r.InGroup || r.Kind != "left" {
		t.Errorf("333 left the group but must survive as a 'left' row: %+v", r)
	}

	// The accounts nobody joined with are the point of the union.
	var notJoined []int
	for _, r := range rows {
		if r.Kind == "account" {
			notJoined = append(notJoined, r.NewAPIUserID)
		}
	}
	if len(notJoined) != 2 || notJoined[0] != 2 || notJoined[1] != 3 {
		t.Errorf("want accounts 2 and 3 as 'account' rows, got %v", notJoined)
	}
}

func TestUserDirectoryFilters(t *testing.T) {
	svc, _, _ := seedUserDirectory(t)

	cases := []struct {
		name   string
		filter UserFilter
		want   []int64 // QQ numbers expected, in any order
	}{
		{"bound only", UserFilter{Bound: "bound"}, []int64{111}},
		{"unbound only", UserFilter{Bound: "unbound"}, []int64{222, 333}},
		{"in group", UserFilter{InGroup: "in"}, []int64{111, 222}},
		{"left or not joined", UserFilter{InGroup: "out"}, []int64{333}},
		{"by group name", UserFilter{Group: "authorized"}, nil},
		{"kind account", UserFilter{Kind: "account"}, nil},
		{"kind left", UserFilter{Kind: "left"}, []int64{333}},
		{"keyword by qq", UserFilter{Keyword: "111"}, []int64{111}},
		{"keyword by nickname", UserFilter{Keyword: "ali"}, []int64{111}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := svc.UserDirectory(tc.filter, "2026-01-01")
			if err != nil {
				t.Fatalf("UserDirectory: %v", err)
			}
			if tc.want == nil {
				return
			}
			got := map[int64]bool{}
			for _, r := range rows {
				if r.QQ != 0 {
					got[r.QQ] = true
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %+v", tc.want, rows)
			}
			for _, q := range tc.want {
				if !got[q] {
					t.Errorf("expected QQ %d in %+v", q, rows)
				}
			}
		})
	}
}

// An account-only row must be reachable by the group filter and the keyword
// filter, since those are how the operator finds "who has not joined".
func TestUserDirectoryFindsAccountsByGroupAndEmail(t *testing.T) {
	svc, _, _ := seedUserDirectory(t)

	rows, err := svc.UserDirectory(UserFilter{Group: "authorized"}, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].NewAPIUserID != 2 || rows[0].Kind != "account" {
		t.Fatalf("group filter should find the account-only row #2: %+v", rows)
	}

	rows, err = svc.UserDirectory(UserFilter{Keyword: "999@qq.com"}, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].NewAPIUserID != 2 {
		t.Fatalf("keyword should match the account email: %+v", rows)
	}
}

func TestSyncNewAPIUsersReplacesTheCache(t *testing.T) {
	svc, st, na := seedUserDirectory(t)

	// The gateway loses account 3 and gains 4.
	delete(na.users(), 3)
	na.users()[4] = &newapi.User{ID: 4, Username: "newbie", Email: "777@qq.com", Group: "default"}

	n, err := svc.SyncNewAPIUsers()
	if err != nil {
		t.Fatalf("SyncNewAPIUsers: %v", err)
	}
	if n != 3 {
		t.Errorf("synced %d accounts, want 3", n)
	}
	got, err := st.ListNewAPIUsers()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int]bool{}
	for _, u := range got {
		ids[u.ID] = true
	}
	if ids[3] {
		t.Error("a deleted account must not linger in the cache")
	}
	if !ids[4] {
		t.Error("a new account must appear in the cache")
	}
}

func TestSetUserGroupReReadsBeforeWriting(t *testing.T) {
	svc, st, na := seedUserDirectory(t)

	// Give the account a display name the sparse-object bug would blank.
	na.users()[1].DisplayName = "Alice A"

	if err := svc.SetUserGroup(1, "vip"); err != nil {
		t.Fatalf("SetUserGroup: %v", err)
	}

	if got := na.users()[1].Group; got != "vip" {
		t.Errorf("gateway group = %q, want vip", got)
	}
	if got := na.users()[1].DisplayName; got != "Alice A" {
		t.Errorf("display_name was blanked by the group write: %q", got)
	}
	// The cache reflects the change without a full re-sync.
	cached, err := st.GetNewAPIUser(1)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Group != "vip" {
		t.Errorf("cached group = %q, want vip", cached.Group)
	}
}

func TestSetUserGroupIsANoOpWhenAlreadyThere(t *testing.T) {
	svc, _, na := seedUserDirectory(t)

	if err := svc.SetUserGroup(1, "default"); err != nil {
		t.Fatalf("SetUserGroup: %v", err)
	}
	if len(na.groupSets) != 0 {
		t.Errorf("writing the same group must not call the gateway: %+v", na.groupSets)
	}
}

func TestSetUserGroupsReportsPerAccountOutcomes(t *testing.T) {
	svc, _, na := seedUserDirectory(t)

	// Account 2 fails; 1 and 3 succeed. One failure must not stop the batch.
	na.failSetFor = map[int]error{2: errString("No permission to access users of same or higher level")}

	res, err := svc.SetUserGroups([]int{1, 2, 3}, "vip")
	if err != nil {
		t.Fatalf("SetUserGroups: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("want 3 results, got %d", len(res))
	}
	ok, failed := 0, 0
	for _, r := range res {
		if r.OK {
			ok++
		} else {
			failed++
			if r.UserID != 2 || r.Error == "" {
				t.Errorf("unexpected failure row: %+v", r)
			}
		}
	}
	if ok != 2 || failed != 1 {
		t.Errorf("want 2 ok and 1 failed, got %d/%d", ok, failed)
	}
	// The successful ones really moved.
	if na.users()[1].Group != "vip" || na.users()[3].Group != "vip" {
		t.Errorf("successful accounts did not move: %+v", na.users())
	}
	if na.users()[2].Group == "vip" {
		t.Error("the failing account must not have moved")
	}
}

func TestSetUserGroupsRejectsEmptyInput(t *testing.T) {
	svc, _, _ := seedUserDirectory(t)

	if _, err := svc.SetUserGroups(nil, "vip"); err == nil {
		t.Error("an empty selection must be refused")
	}
	if _, err := svc.SetUserGroups([]int{1}, "  "); err == nil {
		t.Error("a blank group must be refused")
	}
}

func TestSetUserGroupsDeduplicates(t *testing.T) {
	svc, _, _ := seedUserDirectory(t)

	res, err := svc.SetUserGroups([]int{1, 1, 0, 1}, "vip")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Errorf("duplicates and zero ids must collapse to one result, got %+v", res)
	}
}

func TestUserDirectoryKeywordIsCaseInsensitive(t *testing.T) {
	svc, _, _ := seedUserDirectory(t)

	rows, err := svc.UserDirectory(UserFilter{Keyword: "STRANGER"}, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !strings.EqualFold(rows[0].NewAPIUser, "stranger") {
		t.Fatalf("keyword matching must ignore case: %+v", rows)
	}
}

// A binding can outlive its member row (the manual bind path takes any QQ).
// Such an account must still appear, marked bound - it must not vanish because
// no member row exists to carry it.
func TestUserDirectoryKeepsABoundAccountWithNoMemberRow(t *testing.T) {
	svc, st, _ := seedUserDirectory(t)

	// Bind account 2 to a QQ that has no member row at all.
	if err := st.SetBinding(store.Binding{
		QQ: 777, NewAPIUserID: 2, NewAPIUsername: "stranger",
		NewAPIEmail: "999@qq.com", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := svc.UserDirectory(UserFilter{}, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	var found *UserRow
	for i := range rows {
		if rows[i].NewAPIUserID == 2 {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatalf("the bound account 2 vanished from the directory: %+v", rows)
	}
	if !found.Bound || found.QQ != 777 {
		t.Errorf("account 2 should be shown as bound to 777: %+v", *found)
	}
}

// The directory must not list the same account twice: once via a member row and
// once as a lone account.
func TestUserDirectoryDoesNotDuplicateAnAccount(t *testing.T) {
	svc, _, _ := seedUserDirectory(t)

	rows, err := svc.UserDirectory(UserFilter{}, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]int{}
	for _, r := range rows {
		if r.NewAPIUserID != 0 {
			seen[r.NewAPIUserID]++
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("account #%d appears %d times, want once", id, n)
		}
	}
}
