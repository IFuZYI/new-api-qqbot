package service

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// seedBindCandidate registers a monitored group and one unbound member in it.
func seedBindCandidate(t *testing.T, svc *Service, qq int64) {
	t.Helper()
	if err := svc.st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.UpsertMembers(1, []store.Member{{QQ: qq, Nickname: "m"}}); err != nil {
		t.Fatal(err)
	}
}

// A failed bind is recorded per member with the reason. The counters say "1
// failed"; only the log says which member and why, which is what the operator
// needs to fix it.
func TestFailedBindIsLoggedWithMemberAndReason(t *testing.T) {
	svc, _, na, _ := newTestService(t)
	seedBindCandidate(t, svc, 54321)
	na.failFind = errString("gateway 502")
	buf := captureLog(t)

	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{`"level":"WARN"`, `"qq":54321`, "gateway 502"} {
		if !strings.Contains(out, want) {
			t.Errorf("bind failure log is missing %s: %s", want, out)
		}
	}
}

// A successful bind is an INFO record naming the member and the path, so the
// operator can reconstruct who was bound and by which route.
func TestSuccessfulBindIsLogged(t *testing.T) {
	svc, _, na, _ := newTestService(t)
	seedBindCandidate(t, svc, 54321)
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "u", Email: "54321@qq.com"}
	buf := captureLog(t)

	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{`"level":"INFO"`, `"qq":54321`, "auto"} {
		if !strings.Contains(out, want) {
			t.Errorf("bind log is missing %s: %s", want, out)
		}
	}
}

// An ambiguous match needs a human, so it is a WARN naming the member rather
// than a silent counter bump.
func TestAmbiguousBindIsLoggedAsWarn(t *testing.T) {
	svc, _, na, _ := newTestService(t)
	seedBindCandidate(t, svc, 54321)
	na.ambig["54321@qq.com"] = true
	buf := captureLog(t)

	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"qq":54321`) {
		t.Errorf("ambiguous bind log = %s", out)
	}
}

// A reward that did not move is an ERROR naming the member: it is the one
// record the operator must be able to find when someone says "我没收到奖励".
func TestFailedRewardIsLoggedAsError(t *testing.T) {
	svc, _, na, _ := newTestService(t)
	date := "2026-10-10"
	cfg := DefaultSettings()
	cfg.RewardMin, cfg.RewardMax = 100, 100
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.RecordCheckin(store.Checkin{QQ: 54321, Date: date, GroupID: 1}); err != nil {
		t.Fatal(err)
	}
	na.failQuota = errString("gateway rejected the quota")
	buf := captureLog(t)

	if _, err := svc.Award(date); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{`"level":"ERROR"`, `"qq":54321`, "gateway rejected the quota"} {
		if !strings.Contains(out, want) {
			t.Errorf("reward failure log is missing %s: %s", want, out)
		}
	}
}

// A paid reward is INFO: the money moved, and the amount belongs in the record.
func TestPaidRewardIsLogged(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	date := "2026-10-10"
	cfg := DefaultSettings()
	cfg.RewardMin, cfg.RewardMax = 150, 150
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.RecordCheckin(store.Checkin{QQ: 54321, Date: date, GroupID: 1}); err != nil {
		t.Fatal(err)
	}
	buf := captureLog(t)

	if _, err := svc.Award(date); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`"level":"INFO"`, `"qq":54321`, "150"} {
		if !strings.Contains(out, want) {
			t.Errorf("paid reward log is missing %s: %s", want, out)
		}
	}
}
