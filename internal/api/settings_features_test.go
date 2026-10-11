package api

import (
	"net/http"
	"testing"

	"qqbot/internal/command"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// The new switches and pattern lists must survive a save/load through HTTP: the
// settings form is the only way an operator can set them.
func TestSettingsRoundTripThroughHTTP(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var s service.Settings
	getJSONWith(t, cl, srv.URL+"/api/settings", &s)
	if s.BindCommandPatterns != command.DefaultBindPatterns {
		t.Fatalf("defaults not returned: %+v", s)
	}

	s.BindCommandEnabled = false
	s.BindCommandPatterns = "/绑定啦\n<绑定>"
	s.BindOnJoinEnabled = false
	s.BindPatrolEnabled = false
	s.BindPatrolMinutes = 20
	s.CheckinByMessageEnabled = false
	s.CheckinPatterns = "/打卡"
	if code := postJSONWith(t, cl, srv.URL+"/api/settings", s, nil); code != 200 {
		t.Fatalf("save status %d", code)
	}

	var got service.Settings
	getJSONWith(t, cl, srv.URL+"/api/settings", &got)
	if got.BindCommandEnabled || got.BindOnJoinEnabled || got.BindPatrolEnabled || got.CheckinByMessageEnabled {
		t.Errorf("switches did not persist: %+v", got)
	}
	if got.BindPatrolMinutes != 20 {
		t.Errorf("bind patrol interval = %d, want 20", got.BindPatrolMinutes)
	}
	if got.BindCommandPatterns != "/绑定啦\n<绑定>" || got.CheckinPatterns != "/打卡" {
		t.Errorf("patterns did not persist: %q / %q", got.BindCommandPatterns, got.CheckinPatterns)
	}
}

// An invalid regular expression must be refused with a 400, not stored: a
// pattern that can never match is the failure mode that is hardest to notice.
func TestSettingsEndpointRejectsInvalidPattern(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var s service.Settings
	getJSONWith(t, cl, srv.URL+"/api/settings", &s)
	s.CheckinPatterns = "/([unclosed/"
	if code := postJSONWith(t, cl, srv.URL+"/api/settings", s, nil); code != http.StatusBadRequest {
		t.Errorf("an invalid pattern must be rejected with 400, got %d", code)
	}
}

// The bootstrap payload carries the settings, so the panel can render the
// switches without a second request.
func TestBootstrapCarriesFeatureSettings(t *testing.T) {
	srv, _, cl := newTestServer(t)

	var got struct {
		Settings service.Settings `json:"settings"`
	}
	if code := getJSONWith(t, cl, srv.URL+"/api/bootstrap", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if !got.Settings.BindCommandEnabled || !got.Settings.CheckinByMessageEnabled {
		t.Errorf("bootstrap must carry the feature switches: %+v", got.Settings)
	}
}

// The manual check-in action records a message-driven check-in for one member,
// which is how an operator verifies the path without waiting for someone to
// chat.
func TestActionCheckinMemberRecords(t *testing.T) {
	srv, st, cl := newTestServer(t)
	postJSONWith(t, cl, srv.URL+"/api/groups/refresh", map[string]any{}, nil)
	postJSONWith(t, cl, srv.URL+"/api/groups", map[string]any{"group_ids": []int64{1129535731}}, nil)
	// A check-in only counts for a bound member.
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, NewAPIEmail: "54321@qq.com"}); err != nil {
		t.Fatal(err)
	}

	var res struct {
		Recorded bool   `json:"recorded"`
		Date     string `json:"date"`
	}
	body := map[string]any{"qq": 54321, "group_id": 1129535731}
	if code := postJSONWith(t, cl, srv.URL+"/api/actions/checkin-member", body, &res); code != 200 {
		t.Fatalf("status %d", code)
	}
	if !res.Recorded {
		t.Errorf("the check-in should have been recorded: %+v", res)
	}
	if res.Date == "" {
		t.Fatal("the response should say which date was recorded")
	}
	if _, err := st.GetCheckin(54321, res.Date); err != nil {
		t.Errorf("the check-in is not in the store: %v", err)
	}

	// A second call the same day is a no-op, which is what the UI reports.
	var again struct {
		Recorded bool `json:"recorded"`
	}
	postJSONWith(t, cl, srv.URL+"/api/actions/checkin-member", body, &again)
	if again.Recorded {
		t.Error("a second check-in the same day must not record again")
	}
}

// The action needs both a QQ and a group, and says so.
func TestActionCheckinMemberValidatesInput(t *testing.T) {
	srv, _, cl := newTestServer(t)

	for _, body := range []map[string]any{
		{"group_id": 1},
		{"qq": 54321},
		{"qq": 0, "group_id": 0},
	} {
		if code := postJSONWith(t, cl, srv.URL+"/api/actions/checkin-member", body, nil); code != http.StatusBadRequest {
			t.Errorf("body %v: want 400, got %d", body, code)
		}
	}
}

// The manual bind action must not re-point a binding the operator made by hand,
// and reports it as a skip.
func TestActionBindLeavesAnExistingBindingAlone(t *testing.T) {
	srv, st, cl := newTestServer(t)
	postJSONWith(t, cl, srv.URL+"/api/groups/refresh", map[string]any{}, nil)
	postJSONWith(t, cl, srv.URL+"/api/groups", map[string]any{"group_ids": []int64{1129535731}}, nil)
	postJSONWith(t, cl, srv.URL+"/api/actions/sync", map[string]any{}, nil)
	if err := st.SetBinding(store.Binding{
		QQ: 54321, NewAPIUserID: 99, NewAPIUsername: "handpicked", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}

	var res service.BindResult
	if code := postJSONWith(t, cl, srv.URL+"/api/actions/bind", map[string]any{}, &res); code != 200 {
		t.Fatalf("status %d", code)
	}
	if res.Bound != 0 {
		t.Errorf("an already-bound member must not be re-bound: %+v", res)
	}
	b, err := st.GetBinding(54321)
	if err != nil {
		t.Fatal(err)
	}
	if b.NewAPIUserID != 99 {
		t.Errorf("the hand-made binding was replaced: %+v", b)
	}
}
