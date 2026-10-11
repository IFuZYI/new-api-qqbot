package service

import (
	"errors"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// The join path binds a member with no command: it is the same match as the
// command path (qq@qq.com), just triggered by the join.
func TestBindMemberMatchesQQEmail(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}

	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatalf("BindMember: %v", err)
	}
	b, err := st.GetBinding(54321)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.NewAPIUserID != 7 || b.Source != "join" {
		t.Errorf("unexpected binding: %+v", b)
	}
}

// No account yet is a normal state, not an error: the member joined before
// registering, and the patrol retries later.
func TestBindMemberWithoutAccountIsNotAnError(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)

	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatalf("a missing account must not be an error: %v", err)
	}
	if _, err := st.GetBinding(54321); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("no binding should exist yet, got err=%v", err)
	}
}

// An ambiguous match must never be guessed at.
func TestBindMemberRefusesAmbiguousMatch(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	na.ambig["54321@qq.com"] = true

	err := svc.BindMember(1129535731, 54321)
	if err == nil {
		t.Error("an ambiguous match must be reported")
	}
	if _, err := st.GetBinding(54321); !errors.Is(err, store.ErrNotFound) {
		t.Error("an ambiguous match must not create a binding")
	}
}

// An existing binding must never be re-pointed by the join path: the operator's
// manual 换绑 (or a previous match) wins.
func TestBindMemberNeverChangesAnExistingBinding(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	// The operator bound this member to a specific account by hand.
	if err := st.SetBinding(store.Binding{
		QQ: 54321, NewAPIUserID: 99, NewAPIUsername: "handpicked",
		NewAPIEmail: "someone@example.com", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	// And an automatic match for their QQ email also exists.
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}

	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatalf("BindMember: %v", err)
	}
	b, err := st.GetBinding(54321)
	if err != nil {
		t.Fatal(err)
	}
	if b.NewAPIUserID != 99 || b.Source != "manual" {
		t.Errorf("an existing binding must not be replaced: %+v", b)
	}
}

// The same rule applies to the command path: a member who already has a binding
// keeps it.
func TestBindByCommandNeverChangesAnExistingBinding(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	if err := st.SetBinding(store.Binding{
		QQ: 54321, NewAPIUserID: 99, NewAPIUsername: "handpicked", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}

	if _, err := svc.BindByCommand(1129535731, 54321); err != nil {
		t.Fatalf("BindByCommand: %v", err)
	}
	b, _ := st.GetBinding(54321)
	if b.NewAPIUserID != 99 {
		t.Errorf("an existing binding must not be replaced by a command: %+v", b)
	}
}

// The join path records the member, so a member who joins while the roster
// patrol is off is still known.
func TestBindMemberRecordsMember(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}

	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetMember(54321); err != nil {
		t.Errorf("the joining member must be recorded: %v", err)
	}
	ms, err := st.MembersInGroup(1129535731)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Errorf("want the membership recorded, got %d rows", len(ms))
	}
}

// The join path applies the on-bind rules, exactly as the command and the
// automatic match do: "on bind" means on bind, whatever caused it.
func TestBindMemberAppliesOnBindRules(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	if _, err := svc.CreateRule(Rule{
		Name: "join → vip", Enabled: true, OnBind: true,
		TargetGroup: "vip", Conditions: []Condition{},
	}); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}

	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatal(err)
	}
	if len(na.groupSets) != 1 || na.groupSets[0].group != "vip" {
		t.Errorf("the on-bind rule should have fired: %+v", na.groupSets)
	}
}

// A zero identity is refused rather than recorded under a meaningless key.
func TestBindMemberRejectsZeroIdentity(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	if err := svc.BindMember(0, 54321); err == nil {
		t.Error("a zero group id must be refused")
	}
	if err := svc.BindMember(1, 0); err == nil {
		t.Error("a zero QQ must be refused")
	}
}

// A gateway failure must surface, so the caller can log it; the member is still
// recorded, so the patrol can retry the binding later.
func TestBindMemberSurfacesGatewayFailure(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	na.failFind = errors.New("gateway down")

	if err := svc.BindMember(1129535731, 54321); err == nil {
		t.Error("a gateway failure must be reported")
	}
	if _, err := st.GetMember(54321); err != nil {
		t.Errorf("the member must be recorded even when the lookup fails: %v", err)
	}
}
