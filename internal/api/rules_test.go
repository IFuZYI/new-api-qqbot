package api

import (
	"net/http"
	"testing"

	"qqbot/internal/service"
)

func TestRulesRequireAuth(t *testing.T) {
	srv, _, cl := newBareServer(t)
	for _, path := range []string{"/api/rules", "/api/rules/meta"} {
		if code := getJSONWith(t, cl, srv.URL+path, nil); code != http.StatusUnauthorized {
			t.Errorf("%s should require auth, got %d", path, code)
		}
	}
	if code, _ := post(t, cl, srv.URL+"/api/rules", `{}`); code != http.StatusUnauthorized {
		t.Errorf("creating a rule should require auth, got %d", code)
	}
}

func TestRuleMetaListsConditionsAndOperators(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var got struct {
		ConditionTypes []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"condition_types"`
		Operators []string `json:"operators"`
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/rules/meta", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.ConditionTypes) != len(service.ConditionTypes) {
		t.Errorf("want %d condition types, got %d", len(service.ConditionTypes), len(got.ConditionTypes))
	}
	// Labels are what the operator reads, so they must not be blank.
	for _, c := range got.ConditionTypes {
		if c.Value == "" || c.Label == "" {
			t.Errorf("incomplete condition meta: %+v", c)
		}
	}
	if len(got.Operators) != len(service.ConditionOps) {
		t.Errorf("want %d operators, got %d", len(service.ConditionOps), len(got.Operators))
	}
}

func TestCreateListUpdateDeleteRule(t *testing.T) {
	srv, _, cl := newTestServer(t)

	in := service.Rule{
		Name: "绑定即提升", Enabled: true, OnBind: true,
		SourceGroups: []string{"default"}, TargetGroup: "authorized",
		Conditions: []service.Condition{
			{ConditionType: service.CondGroupLevel, Op: service.OpGTE, Value: 5},
		},
	}
	var created service.Rule
	if code := postJSONWith(t, cl, srv.URL+"/api/rules", in, &created); code != 200 {
		t.Fatalf("create status %d", code)
	}
	if created.ID == 0 {
		t.Fatal("no id returned")
	}

	var list struct {
		Items []service.Rule `json:"items"`
	}
	getJSONWith(t, cl, srv.URL+"/api/rules", &list)
	if len(list.Items) != 1 {
		t.Fatalf("want 1 rule, got %d", len(list.Items))
	}
	if len(list.Items[0].Conditions) != 1 || list.Items[0].Conditions[0].Value != 5 {
		t.Errorf("conditions not round-tripped: %+v", list.Items[0])
	}
	if len(list.Items[0].SourceGroups) != 1 {
		t.Errorf("source groups not round-tripped: %+v", list.Items[0])
	}

	// Update
	created.Name = "改名了"
	created.IntervalMinutes = 120
	var updated service.Rule
	if code := putJSONWith(t, cl, srv.URL+"/api/rules/"+itoa(created.ID), created, &updated); code != 200 {
		t.Fatalf("update status %d", code)
	}
	if updated.Name != "改名了" || updated.IntervalMinutes != 120 {
		t.Errorf("update not applied: %+v", updated)
	}

	// Delete
	if code := postJSONWith(t, cl, srv.URL+"/api/rules/"+itoa(created.ID)+"/delete", map[string]any{}, nil); code != 200 {
		t.Fatalf("delete status %d", code)
	}
	getJSONWith(t, cl, srv.URL+"/api/rules", &list)
	if len(list.Items) != 0 {
		t.Errorf("rule not deleted: %+v", list.Items)
	}
}

func TestCreateRuleRejectsInvalidPayload(t *testing.T) {
	srv, _, cl := newTestServer(t)

	cases := []service.Rule{
		{Name: "", TargetGroup: "authorized", OnBind: true},
		{Name: "x", TargetGroup: "", OnBind: true},
		{Name: "x", TargetGroup: "authorized"}, // no trigger
		{Name: "x", TargetGroup: "authorized", OnBind: true,
			Conditions: []service.Condition{{ConditionType: "nonsense", Op: service.OpGTE, Value: 1}}},
		{Name: "x", TargetGroup: "authorized", OnBind: true, SourceGroups: []string{"authorized"}},
	}
	for i, in := range cases {
		if code := postJSONWith(t, cl, srv.URL+"/api/rules", in, nil); code == 200 {
			t.Errorf("case %d should have been rejected", i)
		}
	}
}

// A rule must be created with a fresh id even if the payload carries one, so a
// copy-paste cannot overwrite an existing rule.
func TestCreateRuleIgnoresSuppliedID(t *testing.T) {
	srv, _, cl := newTestServer(t)

	first := service.Rule{Name: "a", Enabled: true, OnBind: true, TargetGroup: "authorized"}
	var created service.Rule
	postJSONWith(t, cl, srv.URL+"/api/rules", first, &created)

	second := service.Rule{ID: created.ID, Name: "b", Enabled: true, OnBind: true, TargetGroup: "authorized"}
	var created2 service.Rule
	if code := postJSONWith(t, cl, srv.URL+"/api/rules", second, &created2); code != 200 {
		t.Fatalf("status %d", code)
	}
	if created2.ID == created.ID {
		t.Error("the supplied id must not be reused")
	}

	var list struct {
		Items []service.Rule `json:"items"`
	}
	getJSONWith(t, cl, srv.URL+"/api/rules", &list)
	if len(list.Items) != 2 {
		t.Errorf("want 2 rules, got %d", len(list.Items))
	}
}

func TestRulesActionRunsScheduledRules(t *testing.T) {
	srv, _, cl := newTestServer(t)
	postJSONWith(t, cl, srv.URL+"/api/rules", service.Rule{
		Name: "sched", Enabled: true, IntervalMinutes: 60, TargetGroup: "authorized",
	}, nil)

	if code := postJSONWith(t, cl, srv.URL+"/api/actions/rules", map[string]any{}, nil); code != 200 {
		t.Errorf("rules action status %d", code)
	}
}
