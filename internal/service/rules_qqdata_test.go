package service

import (
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/store"
)

// A rule whose conditions only look at the gateway must not make the service
// read a QQ roster. Reading one roster per monitored group per binding is the
// expensive call in this system, and an operator running quota-only rules would
// pay it for nothing - which is exactly the "don't call NapCat when the feature
// does not need it" rule the modular split is for.
func TestOnBindRuleWithoutQQConditionsReadsNoRoster(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	enableTestGroup(t, st, 1)
	enableTestGroup(t, st, 2)
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	if _, err := svc.CreateRule(Rule{
		Name: "quota → vip", Enabled: true, OnBind: true,
		TargetGroup: "vip",
		Conditions:  []Condition{{ConditionType: CondQuota, Op: OpGTE, Value: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.BindMember(1, 54321); err != nil {
		t.Fatal(err)
	}

	if got := nc.memberListCalls(); got != 0 {
		t.Errorf("a gateway-only rule read %d rosters; it needs none", got)
	}
	if len(na.groupSets) != 1 || na.groupSets[0].group != "vip" {
		t.Errorf("the rule should still have fired: %+v", na.groupSets)
	}
}

// The same for the periodic sweep: rules that only look at the gateway must not
// trigger roster reads.
func TestScheduledRuleWithoutQQConditionsReadsNoRoster(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	enableTestGroup(t, st, 1)
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	if err := st.UpsertMembers(1, []store.Member{{
		QQ: 54321, Nickname: "a",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	na.groupSets = nil
	if _, err := svc.CreateRule(Rule{
		Name: "quota → vip", Enabled: true, IntervalMinutes: 30,
		TargetGroup: "vip",
		Conditions:  []Condition{{ConditionType: CondQuota, Op: OpGTE, Value: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.RunScheduledRules(); err != nil {
		t.Fatal(err)
	}
	if got := nc.memberListCalls(); got != 0 {
		t.Errorf("a gateway-only scheduled rule read %d rosters; it needs none", got)
	}
}

// A rule that DOES look at QQ data must still read the roster: the saving must
// not come from skipping work that is needed.
func TestRuleWithQQConditionReadsRoster(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	enableTestGroup(t, st, 1)
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	nc.members[1] = []napcat.GroupMember{{QQ: 54321, Nickname: "a", Level: 5}}
	if _, err := svc.CreateRule(Rule{
		Name: "level → vip", Enabled: true, OnBind: true,
		TargetGroup: "vip",
		Conditions:  []Condition{{ConditionType: CondGroupLevel, Op: OpGTE, Value: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.BindMember(1, 54321); err != nil {
		t.Fatal(err)
	}
	if got := nc.memberListCalls(); got == 0 {
		t.Error("a QQ-side condition must read the roster, or it cannot be evaluated")
	}
	if len(na.groupSets) != 1 {
		t.Errorf("the level rule should have fired: %+v", na.groupSets)
	}
}

// A rule with no conditions at all needs no roster either: "everyone bound" is
// decided from the binding alone.
func TestOnBindRuleWithoutConditionsReadsNoRoster(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	enableTestGroup(t, st, 1)
	na.byEmail["54321@qq.com"] = newTestUser(7, "default")
	if _, err := svc.CreateRule(Rule{
		Name: "everyone → vip", Enabled: true, OnBind: true,
		TargetGroup: "vip", Conditions: []Condition{},
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.BindMember(1, 54321); err != nil {
		t.Fatal(err)
	}
	if got := nc.memberListCalls(); got != 0 {
		t.Errorf("a condition-free rule read %d rosters; it needs none", got)
	}
	if len(na.groupSets) != 1 {
		t.Errorf("the rule should have fired: %+v", na.groupSets)
	}
}

// NeedsQQData is the predicate the saving rests on, so it is pinned directly.
func TestRuleNeedsQQData(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		want bool
	}{
		{"no conditions", Rule{Conditions: []Condition{}}, false},
		{"quota only", Rule{Conditions: []Condition{{ConditionType: CondQuota}}}, false},
		{"request count only", Rule{Conditions: []Condition{{ConditionType: CondRequestCount}}}, false},
		{"checkin days only", Rule{Conditions: []Condition{{ConditionType: CondCheckinDays}}}, false},
		{"group level", Rule{Conditions: []Condition{{ConditionType: CondGroupLevel}}}, true},
		{"qq level", Rule{Conditions: []Condition{{ConditionType: CondQQLevel}}}, true},
		{"join days", Rule{Conditions: []Condition{{ConditionType: CondJoinDays}}}, true},
		{"mixed", Rule{Conditions: []Condition{
			{ConditionType: CondQuota}, {ConditionType: CondQQLevel},
		}}, true},
	}
	for _, tc := range cases {
		if got := tc.rule.NeedsQQData(); got != tc.want {
			t.Errorf("%s: NeedsQQData = %v, want %v", tc.name, got, tc.want)
		}
	}
}
