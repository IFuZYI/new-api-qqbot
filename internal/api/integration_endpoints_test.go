package api

import (
	"testing"

	"qqbot/internal/store"
)

// These endpoints are called by the panel but had no success-path test: only an
// auth-required check and the cross-origin matrix. A handler that returns the
// wrong shape (or panics on an empty result) would therefore ship unnoticed —
// which matters most for the two that render a table straight from the body.

// GET /api/groups feeds the 群管理 table. It must report the operator's enabled
// choice alongside the roster QQ reported.
func TestIntegrationListGroupsReportsEnabledState(t *testing.T) {
	e := newIntegrationEnv(t)
	e.monitorGroup(t, 1129535731, "测试群")
	if err := e.st.UpsertGroups([]store.MonitoredGroup{{GroupID: 999, GroupName: "未选群"}}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Items []struct {
			GroupID     int64  `json:"group_id"`
			GroupName   string `json:"group_name"`
			Enabled     bool   `json:"enabled"`
			MemberCount int    `json:"member_count"`
		} `json:"items"`
	}
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/groups", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v, want both stored groups", got.Items)
	}
	byID := map[int64]bool{}
	for _, g := range got.Items {
		byID[g.GroupID] = g.Enabled
	}
	if !byID[1129535731] {
		t.Error("the monitored group must report enabled")
	}
	if byID[999] {
		t.Error("a newly discovered group must report disabled")
	}
}

// An empty roster is an empty list, not null: the table maps over the field.
func TestIntegrationListGroupsEmptyIsAnArray(t *testing.T) {
	e := newIntegrationEnv(t)
	var raw map[string]any
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/groups", &raw); code != 200 {
		t.Fatalf("status %d", code)
	}
	if raw["items"] == nil {
		t.Error("items is null; the panel calls .map on it and would crash")
	}
}

// GET /api/bindings is the binding list; a bound member must appear with the
// account it points at.
func TestIntegrationListBindingsReportsTheAccount(t *testing.T) {
	e := newIntegrationEnv(t)
	if err := e.st.SetBinding(store.Binding{
		QQ: 54321, NewAPIUserID: 7, NewAPIUsername: "u7", NewAPIEmail: "54321@qq.com", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Items []struct {
			QQ             int64  `json:"qq"`
			NewAPIUserID   int    `json:"newapi_user_id"`
			NewAPIUsername string `json:"newapi_username"`
			Source         string `json:"source"`
		} `json:"items"`
	}
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/bindings", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %+v", got.Items)
	}
	b := got.Items[0]
	if b.QQ != 54321 || b.NewAPIUserID != 7 || b.NewAPIUsername != "u7" || b.Source != "manual" {
		t.Errorf("binding row = %+v", b)
	}
}

// GET /api/newapi/groups backs the rule editor's group pickers; it reports the
// names configured on the instance.
func TestIntegrationNewAPIGroupsReportsTheInstancesGroups(t *testing.T) {
	e := newIntegrationEnv(t)
	e.gw.groups = []string{"default", "vip"}

	// The handler writes the map itself (a group name to its descriptor), not a
	// wrapper object: the panel reads the keys.
	var got map[string]struct {
		Desc  string `json:"desc"`
		Ratio string `json:"ratio"`
	}
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/newapi/groups", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got) != 2 {
		t.Fatalf("groups = %+v, want both names", got)
	}
	if _, ok := got["default"]; !ok {
		t.Errorf("groups = %+v, missing default", got)
	}
	if _, ok := got["vip"]; !ok {
		t.Errorf("groups = %+v, missing vip", got)
	}
}

// POST /api/actions/checkin-member records a message-driven check-in for one
// member, which is how the message path is verified without waiting for someone
// to chat. (The bare `checkin` action runs the BOARD sync instead — a different
// path, and the reason this one is easy to confuse with it.)
func TestIntegrationManualCheckinRecordsTheMember(t *testing.T) {
	e := newIntegrationEnv(t)
	e.monitorGroup(t, 1, "g")
	if err := e.st.UpsertMembers(1, []store.Member{{QQ: 54321, Nickname: "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}

	var out map[string]any
	code := postJSONWith(t, e.client, e.srv.URL+"/api/actions/checkin-member",
		map[string]any{"qq": 54321, "group_id": 1}, &out)
	if code != 200 {
		t.Fatalf("status %d: %+v", code, out)
	}

	// The row must exist, sourced from the message path (not the board).
	today, err := e.svc.Today()
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.st.GetCheckin(54321, today)
	if err != nil {
		t.Fatalf("no check-in recorded: %v", err)
	}
	if c.Source != "message" {
		t.Errorf("source = %q, want message", c.Source)
	}
}

// A second manual check-in for the same day must not create a second row: the
// path is idempotent per (qq, date) like every other check-in path.
func TestIntegrationManualCheckinIsIdempotent(t *testing.T) {
	e := newIntegrationEnv(t)
	e.monitorGroup(t, 1, "g")
	if err := e.st.UpsertMembers(1, []store.Member{{QQ: 54321, Nickname: "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		var out map[string]any
		if code := postJSONWith(t, e.client, e.srv.URL+"/api/actions/checkin-member",
			map[string]any{"qq": 54321, "group_id": 1}, &out); code != 200 {
			t.Fatalf("run %d: status %d: %+v", i, code, out)
		}
	}

	today, _ := e.svc.Today()
	var listed struct {
		Items []struct {
			QQ int64 `json:"qq"`
		} `json:"items"`
	}
	if code := getJSONWith(t, e.client, e.srv.URL+"/api/checkins?date="+today, &listed); code != 200 {
		t.Fatalf("checkins status %d", code)
	}
	n := 0
	for _, it := range listed.Items {
		if it.QQ == 54321 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the member has %d rows for one day, want exactly 1", n)
	}
}
