package service

import (
	"qqbot/internal/napcat"
	"testing"
	"time"
)

func TestUnknownJoinTimeAcrossGroups(t *testing.T) {
	for _, bind := range []bool{false, true} {
		for _, unknownFirst := range []bool{false, true} {
			t.Run(map[bool]string{false: "sweep", true: "bind"}[bind]+map[bool]string{false: "known-first", true: "unknown-first"}[unknownFirst], func(t *testing.T) {
				svc, st, na, nc := newTestService(t)
				setupMember(t, svc, st, 1, 1, "default")
				enableTestGroup(t, st, 2)
				na.users()[1] = newTestUser(1, "default")
				known := napcat.GroupMember{QQ: 1, JoinTime: time.Now().Unix() - 10*86400}
				unknown := napcat.GroupMember{QQ: 1}
				if unknownFirst {
					known, unknown = unknown, known
				}
				nc.members[1] = []napcat.GroupMember{known}
				nc.members[2] = []napcat.GroupMember{unknown}
				r, err := svc.CreateRule(Rule{Name: "join", Enabled: true, OnBind: true, IntervalMinutes: 1, TargetGroup: "vip", Conditions: []Condition{{ConditionType: CondJoinDays, Op: OpGTE, Value: 1}}})
				if err != nil {
					t.Fatal(err)
				}
				var results []RuleRunResult
				if bind {
					results, err = svc.ApplyOnBindRules(1, nil)
				} else {
					results, err = svc.ApplyRules([]Rule{*r})
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(na.groupSets) != 0 || results[0].Skipped != 1 {
					t.Fatalf("unknown merged join time matched: %+v", results)
				}
			})
		}
	}
}
