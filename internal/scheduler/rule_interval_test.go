package scheduler

import (
	"testing"
	"time"

	"qqbot/internal/service"
)

// addSchedRule stores one rule directly, so the interval tests are about the
// scheduling rule and not about the API's validation.
func addSchedRule(t *testing.T, svc *service.Service, r service.Rule) service.Rule {
	t.Helper()
	created, err := svc.CreateRule(r)
	if err != nil {
		t.Fatalf("CreateRule(%+v): %v", r, err)
	}
	return *created
}

// ruleInterval drives the rules patrol, so it must be the SMALLEST configured
// interval: every rule is then checked at least as often as it asked for. The
// boundaries that matter are the ones that must not be counted - a disabled
// rule, and one with no interval - because counting either would run the sweep
// more often than any enabled rule wants, and reading only the first rule would
// starve the rest.
func TestRuleIntervalTakesTheSmallestEnabledInterval(t *testing.T) {
	sched, _, svc := newScheduler(t)

	// No rules at all: the patrol still re-checks, so a rule added later starts
	// running without a restart.
	if got := sched.ruleInterval(); got != 30*time.Minute {
		t.Errorf("with no rules the interval = %s, want 30m", got)
	}

	// One enabled rule at 5 minutes is the boundary: the sweep follows it, not
	// the 30m idle cadence.
	addSchedRule(t, svc, service.Rule{
		Name: "five", Enabled: true, IntervalMinutes: 5, TargetGroup: "authorized",
	})
	if got := sched.ruleInterval(); got != 5*time.Minute {
		t.Errorf("one 5m rule = %s, want 5m", got)
	}

	// A disabled rule at 5 minutes must be ignored: turning a rule off must
	// remove its traffic, not just its effects.
	disabled := addSchedRule(t, svc, service.Rule{
		Name: "disabled", Enabled: false, IntervalMinutes: 5, TargetGroup: "authorized",
	})
	// The enabled 5m rule is still there, so the interval stays 5m; the point
	// is that the disabled one contributed nothing. Drop the enabled one to
	// prove it.
	for _, r := range mustRules(t, svc) {
		if r.Name == "five" {
			if err := svc.DeleteRule(r.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	rules := mustRules(t, svc)
	if len(rules) != 1 || rules[0].ID != disabled.ID {
		t.Fatalf("precondition: only the disabled rule should remain: %+v", rules)
	}
	if got := sched.ruleInterval(); got != 30*time.Minute {
		t.Errorf("a disabled rule must be ignored: %s, want 30m", got)
	}

	// A rule with interval 0 (an on-bind-only rule) must be ignored too: it is
	// not on the schedule, so it must not pull the sweep in.
	zero := addSchedRule(t, svc, service.Rule{
		Name: "onbind", Enabled: true, OnBind: true, TargetGroup: "authorized",
	})
	if zero.IntervalMinutes != 0 {
		t.Fatalf("precondition: an on-bind rule should have no interval, got %d", zero.IntervalMinutes)
	}
	if got := sched.ruleInterval(); got != 30*time.Minute {
		t.Errorf("a zero-interval rule must be ignored: %s, want 30m", got)
	}

	// Two scheduled rules: the smaller wins, whichever order they were added in.
	addSchedRule(t, svc, service.Rule{
		Name: "long", Enabled: true, IntervalMinutes: 90, TargetGroup: "authorized",
	})
	addSchedRule(t, svc, service.Rule{
		Name: "short", Enabled: true, IntervalMinutes: 7, TargetGroup: "authorized",
	})
	if got := sched.ruleInterval(); got != 7*time.Minute {
		t.Errorf("two rules = %s, want the smaller (7m)", got)
	}

	// And the clamp still applies to the winner, so an absurd stored value
	// cannot overflow into a negative period (which reads as "switched off").
	huge := addSchedRule(t, svc, service.Rule{
		Name: "huge", Enabled: true, IntervalMinutes: service.MaxIntervalMinutes,
		TargetGroup: "authorized",
	})
	if huge.IntervalMinutes != service.MaxIntervalMinutes {
		t.Fatalf("precondition: %d", huge.IntervalMinutes)
	}
	if got := sched.ruleInterval(); got != 7*time.Minute {
		t.Errorf("the smallest rule must still win: %s", got)
	}
}

// A store that cannot be read must fall back to a working interval, not to 0:
// a zero period reads as "switched off", so a broken read would silently stop
// every rule from running.
func TestRuleIntervalFallsBackWhenRulesUnreadable(t *testing.T) {
	sched, st, _ := newScheduler(t)

	if err := st.DB().Exec("DROP TABLE promotion_rules").Error; err != nil {
		t.Fatal(err)
	}
	got := sched.ruleInterval()
	if got != time.Hour {
		t.Errorf("ruleInterval with an unreadable store = %s, want 1h", got)
	}
	if got <= 0 {
		t.Error("a fallback must never be 0: that reads as a switched-off patrol")
	}
}

func mustRules(t *testing.T, svc *service.Service) []service.Rule {
	t.Helper()
	rules, err := svc.Rules()
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	return rules
}
