package service

import (
	"errors"
	"gorm.io/gorm"
	"qqbot/internal/store"
	"strings"
	"testing"
)

func TestRuleCheckinReadFailureFailsClosed(t *testing.T) {
	for _, bind := range []bool{false, true} {
		t.Run(map[bool]string{false: "sweep", true: "bind"}[bind], func(t *testing.T) {
			svc, st, na, _ := newTestService(t)
			setupMember(t, svc, st, 1, 1, "default")
			na.users()[1] = newTestUser(1, "default")
			if err := st.RecordCheckin(store.Checkin{QQ: 1, Date: "2026-01-01"}); err != nil {
				t.Fatal(err)
			}
			guarded, err := svc.CreateRule(Rule{Name: "checkins", Enabled: true, OnBind: true, IntervalMinutes: 1, TargetGroup: "unsafe", Conditions: []Condition{{ConditionType: CondCheckinDays, Op: OpEq, Value: 0}}})
			if err != nil {
				t.Fatal(err)
			}
			independent, err := svc.CreateRule(Rule{Name: "independent", Enabled: true, OnBind: true, IntervalMinutes: 1, TargetGroup: "safe"})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.DB().Callback().Query().Before("gorm:query").Register("checkin_fault", func(tx *gorm.DB) {
				if tx.Statement.Table == "checkins" {
					tx.AddError(errors.New("checkin count unavailable"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.CountCheckins(1); err == nil {
				t.Fatal("fault not injected")
			}
			var results []RuleRunResult
			if bind {
				results, err = svc.ApplyOnBindRules(1, nil)
			} else {
				results, err = svc.ApplyRules([]Rule{*guarded, *independent})
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 2 || results[0].Promoted != 0 || results[0].Failed != 1 || !strings.Contains(results[0].Error, "checkin count unavailable") || results[1].Promoted != 1 {
				t.Fatalf("wrong outcomes: %+v", results)
			}
			if len(na.groupSets) != 1 || na.users()[1].Group != "safe" {
				t.Fatalf("unsafe writes: %+v", na.groupSets)
			}
			row, err := st.GetRule(guarded.ID)
			if err != nil || !strings.Contains(row.LastError, "checkin count unavailable") {
				t.Fatalf("error not persisted: %+v %v", row, err)
			}
		})
	}
}
