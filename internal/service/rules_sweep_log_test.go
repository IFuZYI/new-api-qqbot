package service

import (
	"strings"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// The scheduled sweep and the on-bind path are the same operation reached two
// ways, so a failure must be recorded the same way: naming the rule, the member
// and the reason. The sweep used to bump res.Failed and say nothing, which left
// the operator with a count and no way to find out which account failed.
func TestSweepRuleFailureIsLogged(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	na.users()[7] = &newapi.User{ID: 7, Username: "u", Email: "54321@qq.com", Group: "default"}
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	rule := Rule{Name: "sweep promote", Enabled: true, IntervalMinutes: 60, TargetGroup: "vip"}
	if _, err := svc.CreateRule(rule); err != nil {
		t.Fatal(err)
	}
	na.failSet = errString("no permission")
	buf := captureLog(t)

	results, err := svc.RunScheduledRules()
	if err != nil {
		t.Fatal(err)
	}

	var failed int
	for _, r := range results {
		failed += r.Failed
	}
	if failed != 1 {
		t.Fatalf("failed = %d, want 1 (%+v)", failed, results)
	}
	out := buf.String()
	for _, want := range []string{`"level":"WARN"`, "sweep promote", `"qq":54321`, "no permission"} {
		if !strings.Contains(out, want) {
			t.Errorf("the sweep's failure record is missing %s: %s", want, out)
		}
	}
	// The per-rule result must carry the reason too, so the panel's rule row can
	// show it without reading the log.
	for _, r := range results {
		if r.Failed > 0 && r.Error == "" {
			t.Errorf("a failed rule run left no error: %+v", r)
		}
	}
}
