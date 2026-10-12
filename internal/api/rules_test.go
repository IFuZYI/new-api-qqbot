package api

import (
	"net/http"
	"strings"
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

// A rule id that is not a number is the caller's mistake, and the panel must be
// told so rather than getting a store error dressed up as a 500.
func TestUpdateRuleRejectsABadPathID(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var out struct {
		Error string `json:"error"`
	}
	code := putJSONWith(t, cl, srv.URL+"/api/rules/not-a-number",
		service.Rule{Name: "x", OnBind: true, TargetGroup: "authorized"}, &out)
	if code != 400 {
		t.Fatalf("status %d, want 400", code)
	}
	if !strings.Contains(out.Error, "invalid id") {
		t.Errorf("unexpected message: %q", out.Error)
	}
}

func TestUpdateRuleRejectsAMalformedBody(t *testing.T) {
	srv, _, cl := newTestServer(t)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/rules/1", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("malformed body = %d, want 400", resp.StatusCode)
	}
}

// Updating a rule that does not exist is refused as a bad request: the operator
// edited a rule that was deleted in another tab, and the save must not create it
// back under a fresh id.
func TestUpdateRuleRejectsAnUnknownID(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var out struct {
		Error string `json:"error"`
	}
	code := putJSONWith(t, cl, srv.URL+"/api/rules/4242",
		service.Rule{Name: "ghost", Enabled: true, OnBind: true, TargetGroup: "authorized"}, &out)
	if code != 400 {
		t.Fatalf("status %d, want 400", code)
	}
	if out.Error == "" {
		t.Error("the failure must be reported in the body")
	}

	var list struct {
		Items []service.Rule `json:"items"`
	}
	getJSONWith(t, cl, srv.URL+"/api/rules", &list)
	if len(list.Items) != 0 {
		t.Errorf("a refused update must not create a rule: %+v", list.Items)
	}
}

// An invalid payload on update is refused for the same reason as on create: a
// rule with no trigger and no conditions matches nothing, so it must not be
// storable.
func TestUpdateRuleValidatesThePayload(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var created service.Rule
	postJSONWith(t, cl, srv.URL+"/api/rules", service.Rule{
		Name: "ok", Enabled: true, OnBind: true, TargetGroup: "authorized",
	}, &created)

	bad := service.Rule{ID: created.ID, Name: "", OnBind: true, TargetGroup: "authorized"}
	if code := putJSONWith(t, cl, srv.URL+"/api/rules/"+itoa(created.ID), bad, nil); code != 400 {
		t.Errorf("an empty name must be a 400, got %d", code)
	}

	// The stored rule must be untouched by the refused save.
	var list struct {
		Items []service.Rule `json:"items"`
	}
	getJSONWith(t, cl, srv.URL+"/api/rules", &list)
	if len(list.Items) != 1 || list.Items[0].Name != "ok" {
		t.Errorf("the stored rule changed: %+v", list.Items)
	}
}

func TestDeleteRuleRejectsABadPathID(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var out struct {
		Error string `json:"error"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/rules/not-a-number/delete", map[string]any{}, &out); code != 400 {
		t.Fatalf("status %d, want 400", code)
	}
	if !strings.Contains(out.Error, "invalid id") {
		t.Errorf("unexpected message: %q", out.Error)
	}
}

// Deleting a rule is idempotent, and a store failure is a 500 carrying the real
// error (this route is behind the session, so there is nothing to hide).
func TestDeleteRuleIsIdempotentAndMapsStoreFailure(t *testing.T) {
	srv, st, cl := newTestServer(t)

	var created service.Rule
	postJSONWith(t, cl, srv.URL+"/api/rules", service.Rule{
		Name: "gone", Enabled: true, OnBind: true, TargetGroup: "authorized",
	}, &created)

	if code := postJSONWith(t, cl, srv.URL+"/api/rules/"+itoa(created.ID)+"/delete", map[string]any{}, nil); code != 200 {
		t.Fatalf("delete status %d", code)
	}
	// Deleting it again must succeed: the row is already gone, which is the
	// state the caller asked for.
	if code := postJSONWith(t, cl, srv.URL+"/api/rules/"+itoa(created.ID)+"/delete", map[string]any{}, nil); code != 200 {
		t.Errorf("repeated delete = %d, want 200", code)
	}

	if err := st.DB().Exec("DROP TABLE promotion_rules").Error; err != nil {
		t.Fatal(err)
	}
	var out struct {
		Error string `json:"error"`
	}
	if code := postJSONWith(t, cl, srv.URL+"/api/rules/1/delete", map[string]any{}, &out); code != 500 {
		t.Fatalf("broken store = %d, want 500", code)
	}
	if !strings.Contains(out.Error, "promotion_rules") {
		t.Errorf("the real error must be reported: %q", out.Error)
	}
}

// The rule form offers the gateway's own group names, so the operator picks a
// name the instance actually has instead of typing one.
func TestNewAPIGroupsReturnsGatewayGroups(t *testing.T) {
	srv, _, cl, _ := newUserTestEnv(t)

	var got map[string]struct{}
	if code := getJSONWith(t, cl, srv.URL+"/api/newapi/groups", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"default", "authorized", "vip"} {
		if _, ok := got[want]; !ok {
			t.Errorf("group %q is missing from %+v", want, got)
		}
	}
}

// A gateway that cannot be read is the far side's problem, not this service's:
// the form says so as a 502 so the operator looks at the connection card.
func TestNewAPIGroupsMapsGatewayFailureTo502(t *testing.T) {
	env := newEnvWithStubs(t, failingGroupsGateway{}, fakeQQ{})

	var out struct {
		Error string `json:"error"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/newapi/groups", &out); code != 502 {
		t.Fatalf("status %d, want 502", code)
	}
	if !strings.Contains(out.Error, "gateway unreachable") {
		t.Errorf("the upstream failure must be surfaced: %q", out.Error)
	}
}
