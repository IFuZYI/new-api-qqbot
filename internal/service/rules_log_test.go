package service

import (
	"strings"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// A rule that fails for one account says so in the log, naming the rule and the
// account. The per-rule result carries the error only until the panel is
// refreshed; the log keeps it.
func TestRuleFailureIsLogged(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	// The account exists and is addressable, so the failure comes from the
	// group write rather than a missing user.
	na.users()[7] = &newapi.User{ID: 7, Username: "u", Email: "54321@qq.com", Group: "default"}
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	rule := Rule{
		Name: "promote all", Enabled: true,
		OnBind: true, TargetGroup: "vip",
	}
	if _, err := svc.CreateRule(rule); err != nil {
		t.Fatal(err)
	}
	na.failSet = errString("no permission")
	buf := captureLog(t)

	if _, err := svc.ApplyOnBindRules(54321, nil); err != nil {
		// The rule error is reported alongside the binding, not as a failure of
		// this call, so either shape is acceptable here.
		t.Logf("ApplyOnBindRules returned %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"level":"WARN"`) && !strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("no failure record for the rule: %s", out)
	}
	if !strings.Contains(out, "promote all") {
		t.Errorf("the record does not name the rule: %s", out)
	}
}
