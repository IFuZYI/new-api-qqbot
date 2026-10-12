package service

import (
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// A rule's own interval must be honoured, not just the loop's tick.
//
// The rules patrol is a single loop whose period is the SHORTEST enabled rule
// interval, and RunScheduledRules returned every enabled rule with a positive
// interval — nothing compared now against last_run_at + interval_minutes. So a
// rule configured at 1440 minutes ran at a 5-minute rule's cadence: 288x the
// gateway traffic the operator budgeted (one GetUser per binding per tick), and
// accounts converted as soon as they first matched rather than on the schedule
// the panel shows (按巡检周期 1440min). Found by an independent reviewer.
func TestAScheduledRuleWaitsForItsOwnInterval(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	// A member bound to an account in the source group, so the rule has
	// something to move.
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	na.users()[7] = &newapi.User{ID: 7, Username: "u7", Group: "default"}
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}

	// One rule with a long interval, never run yet.
	slow := Rule{
		Name: "慢规则", Enabled: true, IntervalMinutes: 1440,
		SourceGroups: []string{"default"}, TargetGroup: "vip",
	}
	if _, err := svc.CreateRule(slow); err != nil {
		t.Fatal(err)
	}

	// First run: it has never run, so it must run now.
	res, err := svc.RunScheduledRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("the never-run rule did not run: %+v", res)
	}
	if got := na.users()[7].Group; got != "vip" {
		t.Fatalf("the rule did not move the account: group = %q", got)
	}

	// Put the account back so a second run WOULD move it again if it ran.
	na.users()[7] = &newapi.User{ID: 7, Username: "u7", Group: "default"}

	// Second run, immediately: the rule's interval is 1440 minutes, so it must
	// be skipped — its last run was seconds ago.
	res2, err := svc.RunScheduledRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(res2) != 0 {
		t.Errorf("a rule ran again %s after its last run despite a 1440-minute interval: %+v", "seconds", res2)
	}
	if got := na.users()[7].Group; got != "default" {
		t.Errorf("the account was moved again outside the rule's interval: group = %q", got)
	}
}
