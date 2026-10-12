package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qqbot/internal/napcat"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// getRawBody reads a response without decoding it, for the tests that assert on
// what a body must NOT contain.
func getRawBody(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(raw)
}

// dropTable removes one table so a single store call fails while the rest of
// the request keeps working. Closing the whole store would fail the session
// check first (401), which is a different branch than the one under test.
func dropTable(t *testing.T, st *store.Store, name string) {
	t.Helper()
	if err := st.DB().Exec("DROP TABLE " + name).Error; err != nil {
		t.Fatalf("drop %s: %v", name, err)
	}
}

// failingQQ fails the roster read, so a sweep that talks to QQ can be made to
// fail while the store and the gateway stay healthy.
type failingQQ struct{ fakeQQ }

func (failingQQ) GetGroupMemberList(int64) ([]napcat.GroupMember, error) {
	return nil, errText("QQ 连接失败")
}

// failingGroupsGateway fails the group-name read the rule form depends on.
type failingGroupsGateway struct{ fakeGateway }

func (failingGroupsGateway) Groups() (map[string]struct{}, error) {
	return nil, errText("gateway unreachable")
}

// writeAuthErr guards the routes reachable without a session. A 5xx there must
// not carry internal detail - a SQLite message names the file and the statement
// - while a 4xx must still report the real reason, because hiding "密码至少 8 位"
// would make the panel unusable.
func TestWriteAuthErrHidesInternalDetailOn5xx(t *testing.T) {
	// The function itself, at the boundary it owns.
	rec := httptest.NewRecorder()
	writeAuthErr(rec, http.StatusInternalServerError, errors.New("sqlite3: no such table: admins"))
	body := rec.Body.String()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(body, "服务器内部错误，请查看服务端日志") {
		t.Errorf("the generic line is missing: %s", body)
	}
	if strings.Contains(body, "sqlite3") || strings.Contains(body, "no such table") {
		t.Errorf("internal detail leaked into a body reachable without a session: %s", body)
	}

	// A 4xx is the caller's mistake and is passed through verbatim.
	rec = httptest.NewRecorder()
	writeAuthErr(rec, http.StatusBadRequest, errors.New("密码至少 8 位"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "密码至少 8 位") {
		t.Errorf("a 4xx must report the real message: %d %s", rec.Code, rec.Body.String())
	}

	// And end to end: an unauthenticated route whose store call fails answers
	// 500 generically, while the real error still reaches the server log (the
	// ERROR record traceRequests writes for the 5xx).
	srv, st, cl := newBareServer(t)
	buf := captureDefaultLog(t)
	dropTable(t, st, "admins")

	code, body := getRawBody(t, cl, srv.URL+"/api/auth/state")
	if code != http.StatusInternalServerError {
		t.Fatalf("GET /api/auth/state = %d, want 500", code)
	}
	if !strings.Contains(body, "服务器内部错误，请查看服务端日志") {
		t.Errorf("the generic line is missing: %s", body)
	}
	for _, leak := range []string{"admins", "sqlite", "no such table"} {
		if strings.Contains(body, leak) {
			t.Errorf("internal detail %q leaked to an unauthenticated caller: %s", leak, body)
		}
	}
	if out := buf.String(); !strings.Contains(out, "no such table") {
		t.Errorf("the hidden detail must still be in the request's log record: %s", out)
	}
}

// The other half of the same rule: a client mistake on an unauthenticated route
// is reported exactly as it was written, with no generic line in its place.
func TestWriteAuthErrReportsClientMistakesVerbatim(t *testing.T) {
	srv, _, cl := newBareServer(t)

	code, body := post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"short"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("short password = %d, want 400", code)
	}
	if !strings.Contains(body, "密码至少 8 位") {
		t.Errorf("the real message was hidden: %s", body)
	}
	if strings.Contains(body, "服务器内部错误") {
		t.Errorf("a 400 must not be replaced by the generic line: %s", body)
	}

	// A wrong password is a 401 and equally specific.
	code, body = post(t, cl, srv.URL+"/api/auth/login", `{"username":"admin","password":"wrong-password"}`)
	if code != http.StatusUnauthorized || !strings.Contains(body, "用户名或密码错误") {
		t.Errorf("login failure = %d %s, want 401 with the real message", code, body)
	}
}

func TestListGroupsReturnsRowsAndMapsStoreFailure(t *testing.T) {
	env := newTestEnv(t)
	if err := env.st.UpsertGroups([]store.MonitoredGroup{
		{GroupID: 11, GroupName: "one"}, {GroupID: 22, GroupName: "two"},
	}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Items []store.MonitoredGroup `json:"items"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/groups", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Items) != 2 || got.Items[0].GroupID != 11 || got.Items[1].GroupName != "two" {
		t.Fatalf("unexpected groups: %+v", got.Items)
	}

	// A store failure is a 500 carrying the real error: this route sits behind
	// the session, so there is no internal detail to hide here.
	dropTable(t, env.st, "monitored_groups")
	var errBody struct {
		Error string `json:"error"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/groups", &errBody); code != 500 {
		t.Fatalf("broken store = %d, want 500", code)
	}
	if !strings.Contains(errBody.Error, "monitored_groups") {
		t.Errorf("the real error must be reported: %q", errBody.Error)
	}
}

func TestListBindingsReturnsRowsAndMapsStoreFailure(t *testing.T) {
	env := newTestEnv(t)
	if err := env.st.SetBinding(store.Binding{
		QQ: 54321, NewAPIUserID: 7, NewAPIUsername: "seven", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Items []store.Binding `json:"items"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/bindings", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Items) != 1 || got.Items[0].QQ != 54321 || got.Items[0].NewAPIUserID != 7 {
		t.Fatalf("unexpected bindings: %+v", got.Items)
	}

	dropTable(t, env.st, "bindings")
	var errBody struct {
		Error string `json:"error"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/bindings", &errBody); code != 500 {
		t.Fatalf("broken store = %d, want 500", code)
	}
	if !strings.Contains(errBody.Error, "bindings") {
		t.Errorf("the real error must be reported: %q", errBody.Error)
	}
}

func TestDeleteBindingRejectsBadQQAndMapsStoreFailure(t *testing.T) {
	env := newTestEnv(t)

	// A path segment that is not a number is the caller's mistake.
	var errBody struct {
		Error string `json:"error"`
	}
	if code := postJSONWith(t, env.client, env.srv.URL+"/api/bindings/not-a-number/delete",
		map[string]any{}, &errBody); code != 400 {
		t.Fatalf("bad qq = %d, want 400", code)
	}
	if !strings.Contains(errBody.Error, "invalid qq") {
		t.Errorf("unexpected message: %q", errBody.Error)
	}

	// Deleting a binding that is not there succeeds: unbind is idempotent, and
	// the panel's button must not fail on a row another tab already removed.
	if err := env.st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if code := postJSONWith(t, env.client, env.srv.URL+"/api/bindings/54321/delete", map[string]any{}, nil); code != 200 {
		t.Fatalf("first unbind = %d, want 200", code)
	}
	if code := postJSONWith(t, env.client, env.srv.URL+"/api/bindings/54321/delete", map[string]any{}, nil); code != 200 {
		t.Errorf("repeated unbind = %d, want 200", code)
	}
	if _, err := env.st.GetBinding(54321); err == nil {
		t.Error("the binding should be gone")
	}

	dropTable(t, env.st, "bindings")
	if code := postJSONWith(t, env.client, env.srv.URL+"/api/bindings/54321/delete",
		map[string]any{}, &errBody); code != 500 {
		t.Fatalf("broken store = %d, want 500", code)
	}
	if !strings.Contains(errBody.Error, "bindings") {
		t.Errorf("the real error must be reported: %q", errBody.Error)
	}
}

// The check-ins page is today by default and explicit by query: the default is
// what the panel loads on open, and a missing `date` must mean "today in the
// operator's timezone", not "all days" or an error.
func TestListCheckinsDefaultsToToday(t *testing.T) {
	env := newTestEnv(t)

	var today struct {
		Items []store.Checkin `json:"items"`
		Date  string          `json:"date"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/checkins", &today); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(today.Date) != len("2006-01-02") {
		t.Fatalf("the default day must be a date, got %q", today.Date)
	}
	if len(today.Items) != 0 {
		t.Fatalf("a fresh store has no check-ins: %+v", today.Items)
	}

	// A row recorded under the day the default path reported must come back
	// from that same path, which is what makes the page show today's board.
	if err := env.st.RecordCheckin(store.Checkin{QQ: 54321, Date: today.Date, RewardQuota: 150}); err != nil {
		t.Fatal(err)
	}
	getJSONWith(t, env.client, env.srv.URL+"/api/checkins", &today)
	if len(today.Items) != 1 || today.Items[0].QQ != 54321 || today.Items[0].RewardQuota != 150 {
		t.Fatalf("today's row is missing: %+v", today.Items)
	}

	// An explicit day with no rows is an empty list, not a 404.
	var other struct {
		Items []store.Checkin `json:"items"`
		Date  string          `json:"date"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/checkins?date=2001-02-03", &other); code != 200 {
		t.Fatalf("explicit day = %d, want 200", code)
	}
	if other.Date != "2001-02-03" || len(other.Items) != 0 {
		t.Fatalf("unexpected payload: %+v", other)
	}

	dropTable(t, env.st, "checkins")
	var errBody struct {
		Error string `json:"error"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/checkins", &errBody); code != 500 {
		t.Fatalf("broken store = %d, want 500", code)
	}
	if !strings.Contains(errBody.Error, "checkins") {
		t.Errorf("the real error must be reported: %q", errBody.Error)
	}
}

// The default day comes from the service's settings, so a settings blob that
// cannot be read fails the request rather than silently answering for the
// host's date (which may be the wrong day entirely).
func TestListCheckinsFailsWhenTheDayCannotBeResolved(t *testing.T) {
	env := newTestEnv(t)
	if err := env.st.PutSetting("settings", "{not json"); err != nil {
		t.Fatal(err)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if code := getJSONWith(t, env.client, env.srv.URL+"/api/checkins", &errBody); code != 500 {
		t.Fatalf("status %d, want 500", code)
	}
	if errBody.Error == "" {
		t.Error("the failure must be reported in the body")
	}
}

// Every action names a sweep, and each branch maps its failure to the status the
// operator should read: 502 when the far side (QQ or the gateway) is the broken
// hop, 500 when this service's own store is. Getting that wrong sends the
// operator to the wrong logs.
func TestActionMapsEveryFailureToItsStatus(t *testing.T) {
	t.Run("unknown name is a 404", func(t *testing.T) {
		env := newTestEnv(t)
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/nope", map[string]any{}, nil); code != 404 {
			t.Errorf("status %d, want 404", code)
		}
	})

	t.Run("unreadable settings are a 500", func(t *testing.T) {
		env := newTestEnv(t)
		if err := env.st.PutSetting("settings", "{not json"); err != nil {
			t.Fatal(err)
		}
		// The date is resolved before the switch, so a broken setting fails
		// every action rather than running one against a wrong day.
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/sync", map[string]any{}, nil); code != 500 {
			t.Errorf("status %d, want 500", code)
		}
	})

	t.Run("sync upstream failure is a 502", func(t *testing.T) {
		env := newEnvWithStubs(t, fakeGateway{}, failingQQ{})
		if err := env.st.UpsertGroups([]store.MonitoredGroup{{GroupID: 7, GroupName: "g"}}); err != nil {
			t.Fatal(err)
		}
		if err := env.st.SetGroupEnabled(7, true); err != nil {
			t.Fatal(err)
		}
		var out struct {
			Error string `json:"error"`
		}
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/sync", map[string]any{}, &out); code != 502 {
			t.Fatalf("status %d, want 502", code)
		}
		if !strings.Contains(out.Error, "QQ 连接失败") {
			t.Errorf("the upstream failure must be surfaced: %q", out.Error)
		}
	})

	t.Run("sync store failure is a 500", func(t *testing.T) {
		env := newTestEnv(t)
		// No enabled groups, so the sweep itself is a no-op and the failure
		// comes from reading back what it stored.
		dropTable(t, env.st, "members")
		var out struct {
			Error string `json:"error"`
		}
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/sync", map[string]any{}, &out); code != 500 {
			t.Fatalf("status %d, want 500", code)
		}
		if !strings.Contains(out.Error, "members") {
			t.Errorf("the real error must be reported: %q", out.Error)
		}
	})

	t.Run("bind store failure is a 502", func(t *testing.T) {
		env := newTestEnv(t)
		// AutoBind reads the unbound members through the bindings table; losing
		// it is an upstream-of-the-handler failure for this sweep.
		dropTable(t, env.st, "bindings")
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/bind", map[string]any{}, nil); code != 502 {
			t.Errorf("status %d, want 502", code)
		}
	})

	for _, name := range []string{"promote", "rules"} {
		t.Run(name+" store failure is a 502", func(t *testing.T) {
			env := newTestEnv(t)
			dropTable(t, env.st, "promotion_rules")
			if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/"+name, map[string]any{}, nil); code != 502 {
				t.Errorf("status %d, want 502", code)
			}
		})
	}

	t.Run("checkin upstream failure is a 502", func(t *testing.T) {
		env := newTestEnv(t)
		// The board sweep starts by listing the enabled groups.
		dropTable(t, env.st, "monitored_groups")
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/checkin", map[string]any{}, nil); code != 502 {
			t.Errorf("status %d, want 502", code)
		}
	})

	t.Run("checkin store failure is a 500", func(t *testing.T) {
		env := newTestEnv(t)
		// No enabled groups, so the board read is a no-op and the failure comes
		// from reading back the day's rows.
		dropTable(t, env.st, "checkins")
		var out struct {
			Error string `json:"error"`
		}
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/checkin", map[string]any{}, &out); code != 500 {
			t.Fatalf("status %d, want 500", code)
		}
		if !strings.Contains(out.Error, "checkins") {
			t.Errorf("the real error must be reported: %q", out.Error)
		}
	})

	// The board sweep's success path: the response names the day it read and
	// how many check-ins that day now holds, which is what the button reports.
	t.Run("checkin reports the day it read", func(t *testing.T) {
		env := newTestEnv(t)
		var out struct {
			Date    string `json:"date"`
			Checked int    `json:"checked"`
		}
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/checkin", map[string]any{}, &out); code != 200 {
			t.Fatalf("status %d", code)
		}
		// The VALUE matters, not its length: `len(out.Date) != len("2006-01-02")`
		// passed for any 10-character string, so a constant wrong day shipped.
		// Compute the expected day the way the service does — its own Today() —
		// rather than from a literal. The value matters, not its length:
		// `len(out.Date) != len("2006-01-02")` passed for any 10-character
		// string, so a constant wrong day shipped.
		want, err := env.router.svc.Today()
		if err != nil {
			t.Fatal(err)
		}
		if out.Date != want {
			t.Errorf("date = %q, want the day the service reports (%q)", out.Date, want)
		}
		if _, err := time.Parse(time.DateOnly, out.Date); err != nil {
			t.Errorf("date %q is not a calendar day: %v", out.Date, err)
		}
		if out.Checked != 0 {
			t.Errorf("checked = %d, want 0", out.Checked)
		}
	})

	// The alias the promote action is documented as: the rules sweep must be
	// what runs, so the response carries the rules' own results.
	t.Run("promote and rules return the rules' results", func(t *testing.T) {
		env := newTestEnv(t)
		if _, err := env.router.svc.CreateRule(service.Rule{
			Name: "sched", Enabled: true, IntervalMinutes: 60, TargetGroup: "authorized",
		}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"promote", "rules"} {
			var out struct {
				Rules []service.RuleRunResult `json:"rules"`
			}
			if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/"+name, map[string]any{}, &out); code != 200 {
				t.Fatalf("%s status %d", name, code)
			}
			if len(out.Rules) != 1 || out.Rules[0].RuleName != "sched" {
				t.Errorf("%s returned %+v, want the rule's result", name, out.Rules)
			}
		}
	})

	t.Run("checkin-member rejects a bad body", func(t *testing.T) {
		env := newTestEnv(t)
		code, _ := post(t, env.client, env.srv.URL+"/api/actions/checkin-member", "{not json")
		if code != 400 {
			t.Errorf("malformed body = %d, want 400", code)
		}
	})

	t.Run("checkin-member store failure is a 502", func(t *testing.T) {
		env := newTestEnv(t)
		// The member row is written first (the message is proof they are in the
		// group), so the failure comes from the binding lookup.
		dropTable(t, env.st, "bindings")
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/checkin-member",
			map[string]any{"qq": 54321, "group_id": 1129535731}, nil); code != 502 {
			t.Errorf("status %d, want 502", code)
		}
	})

	t.Run("award store failure is a 502", func(t *testing.T) {
		env := newTestEnv(t)
		dropTable(t, env.st, "checkins")
		if code := postJSONWith(t, env.client, env.srv.URL+"/api/actions/award", map[string]any{}, nil); code != 502 {
			t.Errorf("status %d, want 502", code)
		}
	})
}
