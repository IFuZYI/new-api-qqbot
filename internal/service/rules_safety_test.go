package service

import (
	"errors"
	"qqbot/internal/napcat"
	"qqbot/internal/store"
	"strings"
	"testing"
)

type partialRuleQQ struct{ *fakeNapCat }

func (q partialRuleQQ) GetGroupMemberList(id int64) ([]napcat.GroupMember, error) {
	if id == 2 {
		return nil, errors.New("roster unavailable")
	}
	return q.fakeNapCat.GetGroupMemberList(id)
}

func TestRulePartialRosterFailsClosed(t *testing.T) {
	for _, bind := range []bool{false, true} {
		t.Run(map[bool]string{false: "sweep", true: "bind"}[bind], func(t *testing.T) {
			svc, st, na, nc := newTestService(t)
			setupMember(t, svc, st, 1, 1, "default")
			enableTestGroup(t, st, 2)
			nc.members[1] = []napcat.GroupMember{{QQ: 1, Level: 2}}
			svc.qq = partialRuleQQ{nc}
			na.users()[1] = newTestUser(1, "default")
			r, err := svc.CreateRule(Rule{Name: "partial", Enabled: true, OnBind: true, IntervalMinutes: 1, TargetGroup: "vip", Conditions: []Condition{{ConditionType: CondGroupLevel, Op: OpLTE, Value: 5}}})
			if err != nil {
				t.Fatal(err)
			}
			independent, err := svc.CreateRule(Rule{Name: "independent", Enabled: true, OnBind: true, IntervalMinutes: 1, TargetGroup: "safe", SourceGroups: []string{"default"}})
			if err != nil {
				t.Fatal(err)
			}
			var results []RuleRunResult
			if bind {
				results, err = svc.ApplyOnBindRules(1, nil)
			} else {
				results, err = svc.ApplyRules([]Rule{*r, *independent})
			}
			if na.users()[1].Group != "safe" || len(na.groupSets) != 1 {
				t.Errorf("QQ failure blocked independent rule or caused unsafe write: %+v, sets=%+v", results, na.groupSets)
			}
			if len(results) != 2 || results[0].Promoted != 0 || results[0].Failed != 1 || results[1].Promoted != 1 {
				t.Errorf("wrong per-rule outcomes: %+v", results)
			}
			stored, readErr := st.GetRule(r.ID)
			if readErr != nil || !strings.Contains(stored.LastError, "roster unavailable") {
				t.Errorf("failure not persisted: %+v %v", stored, readErr)
			}
			visible := err != nil && strings.Contains(err.Error(), "roster unavailable")
			for _, res := range results {
				visible = visible || strings.Contains(res.Error, "roster unavailable")
			}
			if !visible {
				t.Fatal("roster failure not visible")
			}
			cache := newQQDataCache(st, svc.qq)
			groups, _ := st.EnabledGroups()
			if err := cache.load(groups); err == nil {
				t.Error("initial load hid failure")
			}
			if _, err := cache.contextFor(1); err == nil {
				t.Error("cached failure was forgotten")
			}
		})
	}
}

var _ = store.ErrNotFound

func TestRuleUnknownJoinTimeNeverMatches(t *testing.T) {
	for _, op := range ConditionOps {
		t.Run(string(op), func(t *testing.T) {
			ctx := ContextFromMember(napcat.GroupMember{QQ: 1, Level: 3}, 100000)
			r := Rule{Enabled: true, IntervalMinutes: 1, Conditions: []Condition{{ConditionType: CondJoinDays, Op: op, Value: 1}}}
			if r.Matches(ctx) {
				t.Fatal("unknown join time matched")
			}
			r.Conditions[0].Value = 0
			if r.Matches(ctx) {
				t.Fatal("unknown join time matched zero")
			}
			r.Conditions = []Condition{{ConditionType: CondGroupLevel, Op: OpEq, Value: 3}}
			if !r.Matches(ctx) {
				t.Fatal("unknown join time blocked independent level")
			}
		})
	}
	ctx := ContextFromMember(napcat.GroupMember{QQ: 1, JoinTime: 100000}, 100000)
	r := Rule{Enabled: true, IntervalMinutes: 1, Conditions: []Condition{{ConditionType: CondJoinDays, Op: OpEq, Value: 0}}}
	if !r.Matches(ctx) {
		t.Fatal("known zero join days must match")
	}
}
