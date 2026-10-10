package store

import (
	"testing"
)

func TestReplaceNewAPIUsersIsACompleteReplace(t *testing.T) {
	st := newTestStore(t)

	if err := st.ReplaceNewAPIUsers([]NewAPIUser{
		{ID: 1, Username: "a", Group: "default"},
		{ID: 2, Username: "b", Group: "vip"},
	}); err != nil {
		t.Fatalf("ReplaceNewAPIUsers: %v", err)
	}
	got, err := st.ListNewAPIUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SyncedAt == 0 {
		t.Fatalf("unexpected rows: %+v", got)
	}

	// A second sync that no longer lists account 1 must drop it: the gateway's
	// list is authoritative, so a deleted account must not linger and keep
	// showing up as "未进群".
	if err := st.ReplaceNewAPIUsers([]NewAPIUser{{ID: 2, Username: "b", Group: "authorized"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListNewAPIUsers()
	if len(got) != 1 || got[0].ID != 2 || got[0].Group != "authorized" {
		t.Fatalf("want only account 2 with the new group, got %+v", got)
	}
}

func TestReplaceNewAPIUsersWithEmptyClears(t *testing.T) {
	st := newTestStore(t)

	if err := st.ReplaceNewAPIUsers([]NewAPIUser{{ID: 1, Username: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceNewAPIUsers(nil); err != nil {
		t.Fatalf("an empty replace must be allowed: %v", err)
	}
	got, _ := st.ListNewAPIUsers()
	if len(got) != 0 {
		t.Errorf("want an empty cache, got %+v", got)
	}
}

func TestSetNewAPIUserGroup(t *testing.T) {
	st := newTestStore(t)

	if err := st.ReplaceNewAPIUsers([]NewAPIUser{{ID: 7, Username: "x", Group: "default"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNewAPIUserGroup(7, "vip"); err != nil {
		t.Fatalf("SetNewAPIUserGroup: %v", err)
	}
	u, err := st.GetNewAPIUser(7)
	if err != nil {
		t.Fatal(err)
	}
	if u.Group != "vip" {
		t.Errorf("group = %q, want vip", u.Group)
	}
}

func TestBindingsByNewAPIUser(t *testing.T) {
	st := newTestStore(t)

	if err := st.SetBinding(Binding{QQ: 111, NewAPIUserID: 1, Source: "auto"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBinding(Binding{QQ: 222, NewAPIUserID: 2, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	m, err := st.BindingsByNewAPIUser()
	if err != nil {
		t.Fatal(err)
	}
	if m[1] != 111 || m[2] != 222 {
		t.Errorf("unexpected map: %+v", m)
	}
}
