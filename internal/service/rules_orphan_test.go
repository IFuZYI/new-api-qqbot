package service

import (
	"qqbot/internal/store"
	"testing"
)

func TestRulesSweepIncludesOrphanBindings(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	if err := st.SetBinding(store.Binding{QQ: 77, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	na.users()[7] = newTestUser(7, "default")
	r, err := svc.CreateRule(Rule{Name: "all bindings", Enabled: true, IntervalMinutes: 1, SourceGroups: []string{"default"}, TargetGroup: "vip"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.ApplyRules([]Rule{*r})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Promoted != 1 || na.users()[7].Group != "vip" {
		t.Fatalf("orphan skipped: %+v", got)
	}
	got, err = svc.ApplyRules([]Rule{*r})
	if err != nil || got[0].Promoted != 0 || len(na.groupSets) != 1 {
		t.Fatalf("second sweep not idempotent: %+v %v", got, err)
	}
}
