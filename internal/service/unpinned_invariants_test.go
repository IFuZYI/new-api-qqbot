package service

import (
	"errors"
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// This file pins four invariants that an independent audit found UNPINNED: the
// code enforced them, but removing the enforcement left the whole suite green.
// Each test here was verified to fail when its guard is removed.

// --- U2: SetUserGroup re-reads the account before writing ---------------------
//
// new-api's PUT replaces the fields it accepts, so a write from a SPARSE object
// blanks display_name. The shipped test (TestSetUserGroupReReadsBeforeWriting)
// looked like the pin but was not: the fake's SetGroup never modelled the
// payload, so the assertion held whatever the service sent.
//
// payloadGateway models the real client: SetGroup writes exactly the fields the
// CALLER's struct carries, so a sparse struct overwrites the stored account.
type payloadGateway struct {
	server map[int]*newapi.User
	gets   int
	writes int
}

func newPayloadGateway() *payloadGateway {
	return &payloadGateway{server: map[int]*newapi.User{}}
}

func (g *payloadGateway) FindUserByEmail(string) (*newapi.User, error) { return nil, nil }
func (g *payloadGateway) GetUser(id int) (*newapi.User, error) {
	g.gets++
	u, ok := g.server[id]
	if !ok {
		return nil, errors.New("no such account")
	}
	// A fresh copy, like a real HTTP round trip: the caller cannot mutate the
	// server's state by editing the returned struct.
	cp := *u
	return &cp, nil
}

// SetGroup replaces the account's fields with what the caller's struct carries —
// which is what the real PUT does.
func (g *payloadGateway) SetGroup(u *newapi.User, group string) error {
	g.writes++
	g.server[u.ID] = &newapi.User{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
		Role: u.Role, Remark: u.Remark, Group: group,
	}
	return nil
}
func (g *payloadGateway) AddQuota(int, int) error              { return nil }
func (g *payloadGateway) Groups() (map[string]struct{}, error) { return map[string]struct{}{}, nil }
func (g *payloadGateway) ListUsers() ([]newapi.User, error)    { return nil, nil }

func TestSetUserGroupDoesNotBlankFieldsItDoesNotOwn(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/payload.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gw := newPayloadGateway()
	gw.server[7] = &newapi.User{
		ID: 7, Username: "u7", DisplayName: "爱丽丝", Role: 1, Remark: "keep me", Group: "default",
	}
	svc := New(st, gw, nil)

	if err := svc.SetUserGroup(7, "vip"); err != nil {
		t.Fatalf("SetUserGroup: %v", err)
	}
	got := gw.server[7]
	if got.Group != "vip" {
		t.Errorf("group = %q, want vip", got.Group)
	}
	// The fields the service does not own must survive the write.
	if got.DisplayName != "爱丽丝" {
		t.Errorf("display_name was blanked: %q (the write did not re-read the account)", got.DisplayName)
	}
	if got.Remark != "keep me" {
		t.Errorf("remark was blanked: %q", got.Remark)
	}
	if got.Username != "u7" || got.Role != 1 {
		t.Errorf("username/role were clobbered: %+v", got)
	}
	if gw.gets == 0 {
		t.Error("the account was written without being re-read first")
	}
}

// --- U4: the account cache is only replaced after a COMPLETE read ------------
//
// newapi_users is a cache written with a full replace, so a partial or failed
// fetch would delete real accounts. The store layer pins the replace semantics;
// nothing pinned that the SERVICE refuses to call it after a bad read.
func TestSyncNewAPIUsersKeepsTheCacheWhenTheReadFails(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/cache.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Seed the cache with two accounts.
	if err := st.ReplaceNewAPIUsers([]store.NewAPIUser{
		{ID: 1, Username: "a", Group: "default"},
		{ID: 2, Username: "b", Group: "default"},
	}); err != nil {
		t.Fatal(err)
	}

	gw := &failingListGateway{err: errors.New("gateway 502")}
	svc := New(st, gw, nil)

	if _, err := svc.SyncNewAPIUsers(); err == nil {
		t.Fatal("a failed read must be reported")
	}
	// The cache must be untouched: a full replace after a failed read would
	// delete accounts that still exist.
	got, err := st.ListNewAPIUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("the cache holds %d accounts after a failed read, want the original 2", len(got))
	}
}

// A read that returns nothing is ambiguous — an empty instance and a broken
// fetch look identical — so the service must not clear the cache on it.
func TestSyncNewAPIUsersDoesNotClearTheCacheOnAnEmptyRead(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/cache2.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReplaceNewAPIUsers([]store.NewAPIUser{{ID: 1, Username: "a"}}); err != nil {
		t.Fatal(err)
	}

	gw := &failingListGateway{empty: true}
	svc := New(st, gw, nil)

	n, err := svc.SyncNewAPIUsers()
	if err != nil {
		t.Fatalf("an empty read is not an error: %v", err)
	}
	if n != 0 {
		t.Errorf("reported %d accounts, want 0", n)
	}
	got, _ := st.ListNewAPIUsers()
	if len(got) != 1 {
		t.Errorf("an empty read cleared the cache (%d rows left); a broken fetch and an empty instance look identical", len(got))
	}
}

type failingListGateway struct {
	err   error
	empty bool
}

func (g *failingListGateway) FindUserByEmail(string) (*newapi.User, error) { return nil, nil }
func (g *failingListGateway) GetUser(int) (*newapi.User, error)            { return nil, nil }
func (g *failingListGateway) SetGroup(*newapi.User, string) error          { return nil }
func (g *failingListGateway) AddQuota(int, int) error                      { return nil }
func (g *failingListGateway) Groups() (map[string]struct{}, error)         { return nil, nil }
func (g *failingListGateway) ListUsers() ([]newapi.User, error) {
	if g.err != nil {
		return nil, g.err
	}
	if g.empty {
		return []newapi.User{}, nil
	}
	return nil, nil
}

// --- U1: the roster sync never moves an account ------------------------------
//
// Group conversion has ONE decision point (the rules). ADR-0005 records that the
// old blanket sweep ran alongside the rules and moved accounts behind the
// operator's back; nothing asserted that the member patrol leaves groups alone,
// so re-adding such a sweep left the suite green.
func TestSyncMembersNeverMovesAnAccount(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	// A monitored group with a member who is bound to an account sitting in the
	// "default" group, plus a rule that would move them if the RULES ran.
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	na.users()[7] = &newapi.User{ID: 7, Username: "u7", Group: "default"}
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "m"}}

	// The roster patrol only.
	if err := svc.SyncAllMembers(); err != nil {
		t.Fatalf("SyncAllMembers: %v", err)
	}

	if len(na.groupSets) != 0 {
		t.Errorf("the member patrol wrote a group: %+v (conversion must be rules-only)", na.groupSets)
	}
	if got := na.users()[7].Group; got != "default" {
		t.Errorf("the account moved to %q during a roster sync", got)
	}
}
