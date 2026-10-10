package service

import (
	"testing"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
)

// ---------- condition matching ----------

func conds(cs ...Condition) []Condition { return cs }

func TestRuleOnBindMatchesOnlyBoundMembers(t *testing.T) {
	r := Rule{ID: 1, Enabled: true, Name: "绑定即提升", OnBind: true, TargetGroup: "authorized"}

	bound := MemberContext{QQ: 1, Bound: true, User: &newapi.User{ID: 10, Group: "default"}}
	if !r.Matches(bound) {
		t.Error("a bound member should match an on-bind rule")
	}

	unbound := MemberContext{QQ: 2, Bound: false}
	if r.Matches(unbound) {
		t.Error("an unbound member must not match")
	}
}

// An unfinished rule (no trigger, no conditions) must not sweep everyone into
// a group. A rule that does have a trigger may legitimately mean "everyone".
func TestRuleWithoutTriggerOrConditionsNeverMatches(t *testing.T) {
	r := Rule{ID: 1, Enabled: true, TargetGroup: "authorized"}
	if r.Matches(MemberContext{QQ: 1, Bound: true, User: &newapi.User{ID: 1}}) {
		t.Error("a rule with no trigger and no conditions must not match")
	}
}

func TestScheduledRuleWithoutConditionsMatchesEveryoneBound(t *testing.T) {
	r := Rule{ID: 1, Enabled: true, IntervalMinutes: 60, TargetGroup: "authorized"}
	if !r.Matches(MemberContext{QQ: 1, Bound: true, User: &newapi.User{ID: 1}}) {
		t.Error("a scheduled rule with no conditions should match any bound member")
	}
}

func TestRuleDisabledNeverMatches(t *testing.T) {
	r := Rule{ID: 1, Enabled: false, OnBind: true, TargetGroup: "authorized"}
	if r.Matches(MemberContext{QQ: 1, Bound: true, User: &newapi.User{ID: 1}}) {
		t.Error("a disabled rule must not match")
	}
}

// Every listed condition must hold: they are ANDed, not ORed.
func TestRuleConditionsAreANDed(t *testing.T) {
	r := Rule{
		ID: 1, Enabled: true, TargetGroup: "authorized",
		Conditions: conds(
			Condition{ConditionType: CondGroupLevel, Op: OpGTE, Value: 10},
			Condition{ConditionType: CondQQLevel, Op: OpGTE, Value: 20},
		),
	}

	both := MemberContext{QQ: 1, Level: 10, QQLevel: 20}
	if !r.Matches(both) {
		t.Error("both conditions satisfied should match")
	}
	oneShort := MemberContext{QQ: 1, Level: 10, QQLevel: 19}
	if r.Matches(oneShort) {
		t.Error("one failing condition must fail the rule")
	}
}

func TestRuleConditionOperators(t *testing.T) {
	user := &newapi.User{ID: 5, Quota: 1000, RequestCount: 50}

	cases := []struct {
		name string
		cond Condition
		ctx  MemberContext
		want bool
	}{
		{"group level >=", Condition{ConditionType: CondGroupLevel, Op: OpGTE, Value: 5}, MemberContext{Level: 5}, true},
		{"group level > fails", Condition{ConditionType: CondGroupLevel, Op: OpGTE, Value: 6}, MemberContext{Level: 5}, false},
		{"group level ==", Condition{ConditionType: CondGroupLevel, Op: OpEq, Value: 5}, MemberContext{Level: 5}, true},
		{"group level <", Condition{ConditionType: CondGroupLevel, Op: OpLT, Value: 9}, MemberContext{Level: 5}, true},
		{"qq level >", Condition{ConditionType: CondQQLevel, Op: OpGT, Value: 9}, MemberContext{QQLevel: 10}, true},
		{"join days >=", Condition{ConditionType: CondJoinDays, Op: OpGTE, Value: 7}, MemberContext{JoinDays: 7}, true},
		{"checkin days >=", Condition{ConditionType: CondCheckinDays, Op: OpGTE, Value: 3}, MemberContext{CheckinDays: 3}, true},
		{"newapi quota >=", Condition{ConditionType: CondQuota, Op: OpGTE, Value: 1000}, MemberContext{User: user}, true},
		{"newapi quota > fails", Condition{ConditionType: CondQuota, Op: OpGT, Value: 1000}, MemberContext{User: user}, false},
		{"newapi requests >=", Condition{ConditionType: CondRequestCount, Op: OpGTE, Value: 50}, MemberContext{User: user}, true},
	}

	for _, tc := range cases {
		r := Rule{ID: 1, Enabled: true, TargetGroup: "authorized", Conditions: conds(tc.cond)}
		if got := r.Matches(tc.ctx); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A condition about the gateway account cannot be satisfied without one.
func TestNewAPIConditionsRequireAnAccount(t *testing.T) {
	r := Rule{ID: 1, Enabled: true, TargetGroup: "authorized",
		Conditions: conds(Condition{ConditionType: CondQuota, Op: OpGTE, Value: 0})}
	if r.Matches(MemberContext{QQ: 1, Bound: false, User: nil}) {
		t.Error("a gateway condition must not match without an account")
	}
}

func TestRuleSkipsMembersAlreadyInTarget(t *testing.T) {
	r := Rule{ID: 1, Enabled: true, OnBind: true, TargetGroup: "authorized"}
	ctx := MemberContext{QQ: 1, Bound: true, User: &newapi.User{ID: 1, Group: "authorized"}}
	if !r.Matches(ctx) {
		t.Error("matching is about conditions; the target check happens at apply time")
	}
}

func TestRuleSourcesGate(t *testing.T) {
	r := Rule{ID: 1, Enabled: true, OnBind: true, TargetGroup: "authorized",
		SourceGroups: []string{"default", "trial"}}

	inDefault := MemberContext{QQ: 1, Bound: true, User: &newapi.User{ID: 1, Group: "default"}}
	if !r.Matches(inDefault) {
		t.Error("an account in a listed source group should match")
	}

	// Empty source list means "from anywhere".
	any := Rule{ID: 2, Enabled: true, OnBind: true, TargetGroup: "authorized"}
	if !any.Matches(inDefault) {
		t.Error("an empty source list should match any group")
	}
}

func TestRuleValidate(t *testing.T) {
	valid := Rule{Name: "x", TargetGroup: "authorized", OnBind: true}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid rule rejected: %v", err)
	}

	cases := []struct {
		name string
		rule Rule
	}{
		{"no name", Rule{TargetGroup: "authorized", OnBind: true}},
		{"no target", Rule{Name: "x", OnBind: true}},
		{"no trigger", Rule{Name: "x", TargetGroup: "authorized"}},
		{"bad op", Rule{Name: "x", TargetGroup: "a", OnBind: true,
			Conditions: conds(Condition{ConditionType: CondGroupLevel, Op: "~", Value: 1})}},
		{"bad condition type", Rule{Name: "x", TargetGroup: "a", OnBind: true,
			Conditions: conds(Condition{ConditionType: "nonsense", Op: OpGTE, Value: 1})}},
		{"negative value", Rule{Name: "x", TargetGroup: "a", OnBind: true,
			Conditions: conds(Condition{ConditionType: CondGroupLevel, Op: OpGTE, Value: -1})}},
	}
	for _, tc := range cases {
		if err := tc.rule.Validate(); err == nil {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}
}

// Interval is what makes a rule run on the sweep instead of on the bind event.
func TestRuleIntervalOnlyWhenPositive(t *testing.T) {
	if (Rule{IntervalMinutes: 0}).RunsOnSchedule() {
		t.Error("0 minutes means no scheduled run")
	}
	if !(Rule{IntervalMinutes: 60}).RunsOnSchedule() {
		t.Error("a positive interval means a scheduled run")
	}
}

func TestLevelOfAndMemberContextFromNapCat(t *testing.T) {
	m := napcat.GroupMember{QQ: 5, Nickname: "a", Level: 9, QQLevel: 30, JoinTime: 1791632992}
	ctx := ContextFromMember(m, 1791632992+3*86400)
	if ctx.Level != 9 || ctx.QQLevel != 30 {
		t.Errorf("levels not carried over: %+v", ctx)
	}
	if ctx.JoinDays != 3 {
		t.Errorf("join days = %d, want 3", ctx.JoinDays)
	}
}

// A member who joined in the future (clock skew) must not report negative days.
func TestJoinDaysNeverNegative(t *testing.T) {
	m := napcat.GroupMember{QQ: 5, JoinTime: 1791632992 + 1000}
	ctx := ContextFromMember(m, 1791632992)
	if ctx.JoinDays != 0 {
		t.Errorf("join days = %d, want 0", ctx.JoinDays)
	}
}

// A member with no join time (0) must not look like they joined in 1970.
func TestJoinDaysZeroWhenUnknown(t *testing.T) {
	m := napcat.GroupMember{QQ: 5, JoinTime: 0}
	ctx := ContextFromMember(m, 1791632992)
	if ctx.JoinDays != 0 {
		t.Errorf("join days = %d, want 0 when the join time is unknown", ctx.JoinDays)
	}
}
