package service

import (
	"errors"
	"strings"
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// The nil-dependency stubs must fail every gateway and QQ call with the sentinel
// errors, so a caller can tell "this instance has no connection configured" from
// an upstream failure. They are the replacement for the nil-pointer panic that
// used to crash the process when a Service was built without a gateway.
func TestUnconfiguredStubsReportTheSentinel(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/stub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st, nil, nil)

	// Gateway side.
	if _, err := svc.gw.GetUser(1); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("GetUser: %v", err)
	}
	if err := svc.gw.SetGroup(&newapi.User{ID: 1}, "vip"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("SetGroup: %v", err)
	}
	if err := svc.gw.AddQuota(1, 10); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("AddQuota: %v", err)
	}
	if _, err := svc.gw.Groups(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Groups: %v", err)
	}
	if _, err := svc.gw.ListUsers(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("ListUsers: %v", err)
	}
	if _, err := svc.gw.FindUserByEmail("a@b.c"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("FindUserByEmail: %v", err)
	}

	// QQ side.
	if _, err := svc.qq.GetGroupList(); !errors.Is(err, ErrQQNotConfigured) {
		t.Errorf("GetGroupList: %v", err)
	}
	if _, err := svc.qq.GetGroupMemberList(1); !errors.Is(err, ErrQQNotConfigured) {
		t.Errorf("GetGroupMemberList: %v", err)
	}
	if _, err := svc.qq.GetGroupSignedList(1); !errors.Is(err, ErrQQNotConfigured) {
		t.Errorf("GetGroupSignedList: %v", err)
	}

	// The messages must be the operator-facing Chinese ones, since they are
	// rendered in the panel.
	if err := svc.gw.SetGroup(&newapi.User{ID: 1}, "vip"); !strings.Contains(err.Error(), "未配置 new-api 连接") {
		t.Errorf("gateway message = %q", err)
	}
	if _, err := svc.qq.GetGroupList(); !strings.Contains(err.Error(), "未配置 NapCat 连接") {
		t.Errorf("QQ message = %q", err)
	}
}

// SyncGroups reads the group list from QQ and keeps the operator's enabled
// choices across a refresh (the invariant that stops a refresh re-enabling a
// group the operator turned off).
func TestSyncGroupsKeepsTheEnabledChoice(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	// A group the operator has disabled, already stored.
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g", MemberCount: 1}}); err != nil {
		t.Fatal(err)
	}
	// QQ now reports the same group with a new name and a second group.
	nc.groups = []napcat.Group{
		{GroupID: 1, GroupName: "g 改名", MemberCount: 5},
		{GroupID: 2, GroupName: "新群", MemberCount: 2},
	}

	gs, err := svc.SyncGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 2 {
		t.Fatalf("groups = %+v, want 2", gs)
	}
	for _, g := range gs {
		if g.GroupID == 1 {
			if g.GroupName != "g 改名" {
				t.Errorf("metadata was not refreshed: %+v", g)
			}
			if g.Enabled {
				t.Error("the refresh re-enabled a group the operator had disabled")
			}
		}
		if g.GroupID == 2 && g.Enabled {
			t.Error("a newly discovered group must be stored disabled")
		}
	}

	// The ENABLE direction is the dangerous one: SyncGroups builds
	// store.MonitoredGroup without an Enabled field, so a refresh that took the
	// caller's zero value would silently DISABLE every monitored group — and the
	// operator's members would stop being recorded and paid. Only the disable
	// side was pinned before, so `existing.Enabled = g.Enabled` survived.
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	gs, err = svc.SyncGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.GroupID == 1 && !g.Enabled {
			t.Error("the refresh DISABLED a group the operator had enabled")
		}
	}
	// And it must still be enabled in the store, not just in the return value.
	rows, err := st.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range rows {
		if g.GroupID == 1 && !g.Enabled {
			t.Error("the store lost the operator's enabled choice")
		}
	}

	// A QQ failure is reported.
	nc.groupListErr = errors.New("napcat down")
	if _, err := svc.SyncGroups(); err == nil {
		t.Error("a QQ failure returned no error")
	}
}
