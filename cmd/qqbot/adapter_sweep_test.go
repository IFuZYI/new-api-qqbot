package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// The adapter must reach the store through the real failover path.
//
// Every other test of these methods uses a fake QQ client, so replacing the
// napcat.Failover wrapper in lazyQQ.GetGroupMemberList with a direct OneBot call
// would fail no test — and a degraded NapCat (expired QQ login, OneBot answering
// error envelopes) would then fail every sweep instead of falling back to the
// WebUI bridge, which is the entire point of the fallback.
func TestRosterSweepFallsBackToTheWebUIBridge(t *testing.T) {
	var onebotCalls int32

	// OneBot is up but refuses the action, as it does when the QQ login expired:
	// NapCat answers with a non-zero retcode rather than a transport error.
	onebot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&onebotCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "failed", "retcode": 1404, "message": "机器人未登录",
		})
	}))
	defer onebot.Close()

	// The WebUI bridge serves the roster.
	webui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{"Credential": "cred-1", "require2FA": false},
			})
		case "/api/Debug/call":
			// The bridge forwards a full OneBot envelope inside data.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "ok",
				"data": map[string]any{
					"status": "ok", "retcode": 0,
					"data": []map[string]any{
						{"user_id": 54321, "nickname": "甲", "card": ""},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer webui.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "adapter.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := config.NewManager(filepath.Join(dir, config.FileName), st)
	if err := mgr.SaveSection(config.SectionNapCat, config.Config{
		NapCat: config.NapCat{
			OneBotURL: onebot.URL, WebUIURL: webui.URL, WebUIToken: "tok",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// One monitored, enabled group, so the sweep actually reads a roster.
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}

	// The REAL adapter over the REAL service and store.
	svc := service.New(st, nil, &lazyQQ{cfg: mgr})
	if err := svc.SyncAllMembers(); err != nil {
		t.Fatalf("SyncAllMembers: %v", err)
	}

	// The member must have reached the store through the fallback.
	ms, err := st.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range ms {
		if m.QQ == 54321 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the roster never reached the store; members = %+v", ms)
	}
	if atomic.LoadInt32(&onebotCalls) == 0 {
		t.Error("the OneBot path was never attempted; the fallback is being bypassed")
	}
}

// When BOTH paths fail, the sweep reports the failure (it must not look like an
// empty group).
func TestRosterSweepReportsWhenBothPathsFail(t *testing.T) {
	onebot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "failed", "retcode": 1404, "message": "机器人未登录",
		})
	}))
	defer onebot.Close()
	// The WebUI is unreachable: no listener at all.
	webuiURL := "http://127.0.0.1:1"

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "adapter2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := config.NewManager(filepath.Join(dir, config.FileName), st)
	if err := mgr.SaveSection(config.SectionNapCat, config.Config{
		NapCat: config.NapCat{OneBotURL: onebot.URL, WebUIURL: webuiURL, WebUIToken: "tok"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}

	svc := service.New(st, nil, &lazyQQ{cfg: mgr})
	err = svc.SyncAllMembers()
	if err == nil {
		t.Fatal("both paths failing must be reported, not treated as an empty group")
	}
	if !strings.Contains(err.Error(), "未登录") && !strings.Contains(err.Error(), "refused") &&
		!strings.Contains(err.Error(), "connection") {
		t.Logf("error text: %v", err)
	}
}
