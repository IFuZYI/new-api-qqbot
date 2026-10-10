package service

import (
	"errors"
	"sync"
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// setupMember registers a monitored group, adds the member to it and binds them.
// The group matters: QQ-side condition values (level, join days) are read from
// the rosters of enabled groups, so without one they would all read zero.
func setupMember(t *testing.T, svc *Service, st *store.Store, qq int64, userID int, group string) {
	t.Helper()
	enableTestGroup(t, st, 1)
	if err := st.UpsertMembers(1, []store.Member{{QQ: qq, Nickname: "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBinding(store.Binding{
		QQ: qq, NewAPIUserID: userID, NewAPIUsername: "u", Source: "auto",
	}); err != nil {
		t.Fatal(err)
	}
	_ = svc
}

// enableTestGroup registers one enabled monitored group. A group is stored
// disabled by default, so monitoring is turned on explicitly.
func enableTestGroup(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: id, GroupName: "g", MemberCount: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(id, true); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRuleValidatesAndPersists(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	created, err := svc.CreateRule(Rule{
		Name: "绑定即提升", Enabled: true, OnBind: true, TargetGroup: "authorized",
		SourceGroups: []string{"default"},
		Conditions:   []Condition{{ConditionType: CondGroupLevel, Op: OpGTE, Value: 5}},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("no id assigned")
	}

	// The list fields must survive the JSON round trip through the database.
	rules, err := svc.Rules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rules))
	}
	got := rules[0]
	if len(got.SourceGroups) != 1 || got.SourceGroups[0] != "default" {
		t.Errorf("source groups lost: %+v", got.SourceGroups)
	}
	if len(got.Conditions) != 1 || got.Conditions[0].Value != 5 {
		t.Errorf("conditions lost: %+v", got.Conditions)
	}
	if !got.OnBind || got.TargetGroup != "authorized" {
		t.Errorf("unexpected rule: %+v", got)
	}
}

func TestCreateRuleRejectsInvalid(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	if _, err := svc.CreateRule(Rule{Name: "", TargetGroup: "x", OnBind: true}); err == nil {
		t.Error("a nameless rule must be rejected")
	}
}

func TestUpdateAndDeleteRule(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	r, err := svc.CreateRule(Rule{Name: "a", Enabled: true, OnBind: true, TargetGroup: "authorized"})
	if err != nil {
		t.Fatal(err)
	}

	r.Name = "b"
	r.IntervalMinutes = 60
	updated, err := svc.UpdateRule(*r)
	if err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if updated.Name != "b" || updated.IntervalMinutes != 60 {
		t.Errorf("update not applied: %+v", updated)
	}

	if err := svc.DeleteRule(r.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	rules, _ := svc.Rules()
	if len(rules) != 0 {
		t.Errorf("rule not deleted: %+v", rules)
	}
}

func TestApplyRulesPromotesMatchingMember(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a", Level: 12}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Username: "u11", Group: "default", Role: 1}}

	r := Rule{ID: 1, Name: "等级达标", Enabled: true, TargetGroup: "authorized",
		Conditions: []Condition{{ConditionType: CondGroupLevel, Op: OpGTE, Value: 10}}}

	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if len(res) != 1 || res[0].Promoted != 1 {
		t.Fatalf("want 1 promotion, got %+v", res)
	}
	if na.dir[11].Group != "authorized" {
		t.Errorf("group not changed: %+v", na.dir[11])
	}
}

func TestApplyRulesLeavesNonMatchingAlone(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a", Level: 3}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "default", Role: 1}}

	r := Rule{ID: 1, Name: "等级达标", Enabled: true, TargetGroup: "authorized",
		Conditions: []Condition{{ConditionType: CondGroupLevel, Op: OpGTE, Value: 10}}}

	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Promoted != 0 || na.dir[11].Group != "default" {
		t.Errorf("a below-threshold member must not move: %+v / %+v", res, na.dir[11])
	}
}

// The source-group gate must protect accounts that are already elsewhere.
func TestApplyRulesRespectsSourceGroups(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	setupMember(t, svc, st, 111, 11, "vip")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "vip", Role: 1}}

	r := Rule{ID: 1, Name: "x", Enabled: true, TargetGroup: "authorized",
		OnBind: true, SourceGroups: []string{"default"}}

	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Promoted != 0 || na.dir[11].Group != "vip" {
		t.Errorf("an account outside the source groups must not move: %+v", res)
	}
}

// Running the same rule twice must not re-issue the write.
func TestApplyRulesIsIdempotent(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "default", Role: 1}}

	r := Rule{ID: 1, Name: "x", Enabled: true, OnBind: true, TargetGroup: "authorized"}
	if _, err := svc.ApplyRules([]Rule{r}); err != nil {
		t.Fatal(err)
	}
	na.groupSets = nil

	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Promoted != 0 || len(na.groupSets) != 0 {
		t.Errorf("second pass must be a no-op: %+v sets=%+v", res, na.groupSets)
	}
}

// One account failing must not stop the others.
func TestApplyRulesContinuesAfterFailure(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}, {QQ: 222, Nickname: "b"}}
	setupMember(t, svc, st, 111, 11, "default")
	setupMember(t, svc, st, 222, 22, "default")
	na.dir = map[int]*newapi.User{
		11: {ID: 11, Group: "default", Role: 1},
		22: {ID: 22, Group: "default", Role: 1},
	}
	na.failSet = errors.New("boom")

	r := Rule{ID: 1, Name: "x", Enabled: true, OnBind: true, TargetGroup: "authorized"}
	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatalf("a failure must not abort the pass: %v", err)
	}
	if res[0].Failed != 2 {
		t.Errorf("want 2 failures, got %+v", res)
	}
}

// A rule that cannot read the gateway records the error on itself instead of
// failing the whole sweep.
func TestApplyRulesRecordsGatewayError(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{} // GetUser will fail

	r := Rule{ID: 1, Name: "x", Enabled: true, OnBind: true, TargetGroup: "authorized"}
	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if res[0].Failed != 1 {
		t.Errorf("want 1 failure, got %+v", res)
	}
	if res[0].Error != "" {
		t.Errorf("a per-member failure should not mark the rule broken: %q", res[0].Error)
	}
}

// A missing binding is normal, not a failure.
func TestApplyRulesSkipsUnbound(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	if err := st.UpsertMembers(1, []store.Member{{QQ: 111, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}

	r := Rule{ID: 1, Name: "x", Enabled: true, OnBind: true, TargetGroup: "authorized"}
	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Promoted != 0 || res[0].Skipped != 1 {
		t.Errorf("unbound member should be skipped: %+v", res[0])
	}
}

func TestApplyOnBindRulesPromotesImmediately(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a", Level: 8}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "default", Role: 1}}

	if _, err := svc.CreateRule(Rule{
		Name: "绑定即提升", Enabled: true, OnBind: true, TargetGroup: "authorized",
		Conditions: []Condition{{ConditionType: CondGroupLevel, Op: OpGTE, Value: 5}},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ApplyOnBindRules(111)
	if err != nil {
		t.Fatalf("ApplyOnBindRules: %v", err)
	}
	if len(res) != 1 || res[0].Promoted != 1 {
		t.Fatalf("bind-time rule did not fire: %+v", res)
	}
	if na.dir[11].Group != "authorized" {
		t.Errorf("group not changed at bind time: %+v", na.dir[11])
	}
}

// A condition on the gateway account must see the account's real values.
func TestApplyRulesUsesGatewayAccountValues(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "default", Role: 1, Quota: 5000, RequestCount: 100}}

	r := Rule{ID: 1, Name: "余额达标", Enabled: true, TargetGroup: "authorized",
		Conditions: []Condition{{ConditionType: CondQuota, Op: OpGTE, Value: 1000}}}

	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Promoted != 1 {
		t.Errorf("quota condition did not fire: %+v", res)
	}
}

func TestRunScheduledRulesOnlyUsesScheduled(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "default", Role: 1}}

	// on_bind only: must not take part in the sweep
	if _, err := svc.CreateRule(Rule{Name: "bind-only", Enabled: true, OnBind: true, TargetGroup: "authorized"}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.RunScheduledRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("bind-only rule should not run on the sweep: %+v", res)
	}

	// a scheduled rule does
	if _, err := svc.CreateRule(Rule{Name: "sched", Enabled: true, IntervalMinutes: 60, TargetGroup: "authorized"}); err != nil {
		t.Fatal(err)
	}
	res, err = svc.RunScheduledRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Promoted != 1 {
		t.Errorf("scheduled rule should have run: %+v", res)
	}
}

// A disabled rule must be inert even if it is passed in.
func TestDisabledRuleDoesNothing(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "default", Role: 1}}

	r := Rule{ID: 1, Name: "off", Enabled: false, OnBind: true, TargetGroup: "authorized"}
	res, err := svc.ApplyRules([]Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Promoted != 0 || na.dir[11].Group != "default" {
		t.Errorf("disabled rule fired: %+v", res)
	}
}

// A join event or a bind command records one member; it must not be treated as
// the group's whole roster (which would delete everyone else's membership).
func TestEnsureMemberKeepsOtherMemberships(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)

	if err := st.UpsertMembers(1129535731, []store.Member{
		{QQ: 1, Nickname: "a"}, {QQ: 2, Nickname: "b"}, {QQ: 3, Nickname: "c"},
	}); err != nil {
		t.Fatal(err)
	}

	// One member joins / binds.
	if err := svc.EnsureMember(1129535731, 4, ""); err != nil {
		t.Fatal(err)
	}

	ms, err := st.MembersInGroup(1129535731)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 4 {
		t.Errorf("EnsureMember dropped other memberships: got %d, want 4", len(ms))
	}
}

// A bind command carries no nickname and must not blank the one a roster sync
// already stored.
func TestEnsureMemberDoesNotBlankNickname(t *testing.T) {
	svc, st, _, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	if err := st.UpsertMembers(1129535731, []store.Member{{QQ: 54321, Nickname: "爱丽丝"}}); err != nil {
		t.Fatal(err)
	}

	if err := svc.EnsureMember(1129535731, 54321, ""); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetMember(54321)
	if err != nil {
		t.Fatal(err)
	}
	if m.Nickname != "爱丽丝" {
		t.Errorf("nickname was blanked: %q", m.Nickname)
	}
}

// Two rules must not move the same account in one sweep: the first move makes
// the account leave its old group, so the second rule's source gate must fail.
func TestTwoRulesDoNotMoveTheSameAccountTwice(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "default", Role: 1}}
	// The fake records the move but, like the real client, must not be relied
	// upon to mutate the caller's struct; reset it to model that.
	noMutate := false
	na.mutateUser = &noMutate

	rules := []Rule{
		{ID: 1, Name: "first", Enabled: true, OnBind: true, SourceGroups: []string{"default"}, TargetGroup: "authorized"},
		{ID: 2, Name: "second", Enabled: true, OnBind: true, SourceGroups: []string{"default"}, TargetGroup: "vip"},
	}
	res, err := svc.ApplyRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, r := range res {
		total += r.Promoted
	}
	if total != 1 {
		t.Errorf("want exactly 1 move across both rules, got %d (%+v)", total, res)
	}
	if got := na.dir[11].Group; got != "authorized" {
		t.Errorf("final group = %q, want authorized (the first rule's target)", got)
	}
}

// A rule created disabled must stay disabled: a gorm default:true tag used to
// flip an explicit false back to true, so the rule ran despite the operator
// choosing 停用.
func TestCreateRuleKeepsDisabledState(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	created, err := svc.CreateRule(Rule{
		Name: "停用的规则", Enabled: false, OnBind: true, TargetGroup: "authorized",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if created.Enabled {
		t.Error("a rule created with enabled=false was stored enabled")
	}
	rules, _ := svc.Rules()
	if len(rules) != 1 || rules[0].Enabled {
		t.Errorf("stored rule is enabled: %+v", rules)
	}
}

// A zero reward range is a deliberate "no reward" and must survive a save.
func TestZeroRewardRangeIsHonoured(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	s, _ := svc.Settings()
	s.RewardMin, s.RewardMax = 0, 0
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got.RewardMin != 0 || got.RewardMax != 0 {
		t.Errorf("0..0 read back as %d..%d", got.RewardMin, got.RewardMax)
	}
}

// A concurrent award must record exactly the amount it paid.
func TestConcurrentAwardRecordsThePaidAmount(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Nickname: "a"}}
	setupMember(t, svc, st, 111, 11, "default")
	na.dir = map[int]*newapi.User{11: {ID: 11, Group: "default", Role: 1}}
	if err := st.RecordCheckin(store.Checkin{QQ: 111, Date: "2026-10-10", GroupID: 1}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Award("2026-10-10")
		}()
	}
	wg.Wait()

	if len(na.quotaAdds) != 1 {
		t.Fatalf("want exactly 1 payment, got %d", len(na.quotaAdds))
	}
	paid := na.quotaAdds[0].value
	c, err := st.GetCheckin(111, "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardQuota != paid {
		t.Errorf("recorded %d but paid %d", c.RewardQuota, paid)
	}
}
