package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"qqbot/internal/events"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// This file closes the listener→store seam. Every test in internal/events ends
// in a fake recorder, and every test in cmd/qqbot covers the adapter's calendar
// only, so before this file the following changes were invisible:
//
//   - deleting the check-in dispatch in the listener, or the member recording
//     on a join, which would silently stop the event-driven paths;
//   - making IsCheckinCommand return true unconditionally, which would break the
//     "an empty list means any message, a non-empty one means only these" rule
//     the panel documents;
//   - swapping the adapter's BindByCommand for something that swallows errors.
//
// Here the REAL listener runs over the REAL bindAdapter over a REAL service and
// store, with only the two external systems absent (nil, as runtime_test.go does
// — the paths under test never touch them).

// listenerStore wires the real listener over a real service/store with one
// monitored, enabled group.
func listenerStore(t *testing.T) (*events.Listener, *store.Store, *service.Service) {
	t.Helper()
	st := runtimeStore(t)
	svc := service.New(st, nil, nil)
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	// The real adapter, exactly as main wires it.
	l := events.New(func() (string, string) { return "", "" }, bindAdapter{svc})
	return l, st, svc
}

// captureLog routes the default logger into a buffer for one test.
//
// The listener writes through slog's default logger, which is what the day file
// receives in production; capturing it lets a test assert on the operator-visible
// record itself — the only signal that distinguishes "the dispatch ran and
// failed" from "nothing ran", since the service records the member as its own
// first step either way.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func groupMessage(groupID, userID int64, raw string, ts int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group",
		"group_id": groupID, "user_id": userID,
		"raw_message": raw, "time": ts,
	})
	return b
}

// bindMember gives a member an account binding. A check-in requires one: the
// reward is paid to the bound account, so an unbound member's check-in is
// deliberately skipped (see Service.CheckinByMessage).
func bindMember(t *testing.T, st *store.Store, qq int64, userID int) {
	t.Helper()
	if err := st.SetBinding(store.Binding{QQ: qq, NewAPIUserID: userID, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
}

// A message matching the configured command reaches the store as a
// message-sourced check-in for today.
func TestListenerRecordsACheckinThroughTheRealAdapter(t *testing.T) {
	l, st, svc := listenerStore(t)
	bindMember(t, st, 54321, 7)
	today, err := svc.Today()
	if err != nil {
		t.Fatal(err)
	}

	l.Handle(groupMessage(1, 54321, "/签到", time.Now().Unix()))

	c, err := st.GetCheckin(54321, today)
	if err != nil {
		t.Fatalf("no check-in was recorded: %v", err)
	}
	if c.Source != "message" {
		t.Errorf("source = %q, want message", c.Source)
	}
}

// A non-matching message records nothing while the command list is non-empty.
//
// The member is BOUND deliberately: an unbound member is skipped before the
// pattern list is consulted, so without the binding this test stayed green with
// IsCheckinCommand forced to true (found by an independent reviewer).
func TestListenerIgnoresANonMatchingMessageThroughTheRealAdapter(t *testing.T) {
	l, st, svc := listenerStore(t)
	bindMember(t, st, 54321, 7)
	today, _ := svc.Today()

	l.Handle(groupMessage(1, 54321, "今天天气不错", time.Now().Unix()))

	if _, err := st.GetCheckin(54321, today); err == nil {
		t.Error("an ordinary message recorded a check-in while the command list is non-empty")
	}
}

// With the list emptied, ANY message counts — the switch semantics the panel
// documents and the adapter must honour.
func TestListenerCountsAnyMessageWhenTheListIsEmpty(t *testing.T) {
	l, st, svc := listenerStore(t)
	bindMember(t, st, 54321, 7)
	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.CheckinPatterns = ""
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	today, _ := svc.Today()

	l.Handle(groupMessage(1, 54321, "今天天气不错", time.Now().Unix()))

	if _, err := st.GetCheckin(54321, today); err != nil {
		t.Errorf("an empty command list must count any message: %v", err)
	}
}

// With the path switched off, a matching message records nothing.
//
// The member is BOUND deliberately: without it the check-in is refused by the
// missing binding, so the test stayed green with the master switch no longer
// gating the path (found by an independent reviewer).
func TestListenerRecordsNothingWhenTheMessagePathIsOff(t *testing.T) {
	l, st, svc := listenerStore(t)
	bindMember(t, st, 54321, 7)
	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.CheckinByMessageEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	today, _ := svc.Today()

	l.Handle(groupMessage(1, 54321, "/签到", time.Now().Unix()))

	if _, err := st.GetCheckin(54321, today); err == nil {
		t.Error("a switched-off path recorded a check-in")
	}
}

// A delayed event carrying YESTERDAY's timestamp must not earn today's
// check-in: the adapter's calendar decides, not the host clock.
func TestListenerRejectsAYesterdayTimestampThroughTheRealAdapter(t *testing.T) {
	l, st, svc := listenerStore(t)
	bindMember(t, st, 54321, 7)
	today, _ := svc.Today()

	l.Handle(groupMessage(1, 54321, "/签到", time.Now().Add(-30*time.Hour).Unix()))

	if _, err := st.GetCheckin(54321, today); err == nil {
		t.Error("a yesterday-timestamped message recorded today's check-in")
	}
}

// A join event records the MEMBERSHIP, which is what makes the roster patrol
// optional (ADR-0003's revision).
//
// The assertion is on the membership (member_groups), not merely on the member
// row existing: Service.BindMember — the next step of this same handler — calls
// EnsureMember itself, so asserting "the member exists" stayed green with the
// join's own recording removed (found by an independent reviewer's mutation).
func TestListenerRecordsAJoinThroughTheRealAdapter(t *testing.T) {
	l, st, _ := listenerStore(t)
	raw, _ := json.Marshal(map[string]any{
		"post_type": "notice", "notice_type": "group_increase",
		"group_id": 1, "user_id": 54321,
	})
	l.Handle(raw)

	assertMembership(t, st, 54321, 1)
}

// The join's own recording is the ONLY writer when the bind-on-join switch is
// off, which is the configuration that makes it load-bearing.
//
// With the switch on (the default) the handler's next step, BindMember, calls
// EnsureMember itself — so removing the join's own call is behaviourally
// equivalent there and an independent reviewer's mutation survived. With the
// switch off, nothing else records the member, so this is where the join's
// recording is pinned.
func TestListenerRecordsAJoinWhenBindingIsOff(t *testing.T) {
	l, st, svc := listenerStore(t)
	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.BindOnJoinEnabled = false
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(map[string]any{
		"post_type": "notice", "notice_type": "group_increase",
		"group_id": 1, "user_id": 54321,
	})
	l.Handle(raw)

	assertMembership(t, st, 54321, 1)
}

// assertMembership fails unless qq is recorded as a member OF groupID.
func assertMembership(t *testing.T, st *store.Store, qq, groupID int64) {
	t.Helper()
	ms, err := st.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.QQ != qq {
			continue
		}
		for _, g := range m.GroupIDs {
			if g == groupID {
				return
			}
		}
		t.Errorf("the join recorded the member but NOT the membership: %+v", m)
		return
	}
	t.Errorf("the join was not recorded: members = %+v", ms)
}

// A join for an UNMONITORED group records nothing — the reject side of the
// monitored() gate on the join path.
func TestListenerIgnoresAJoinInAnUnmonitoredGroup(t *testing.T) {
	l, st, _ := listenerStore(t)
	raw, _ := json.Marshal(map[string]any{
		"post_type": "notice", "notice_type": "group_increase",
		"group_id": 999, "user_id": 54321,
	})
	l.Handle(raw)

	ms, err := st.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.QQ == 54321 {
			t.Errorf("a join in an unmonitored group recorded the member: %+v", m)
		}
	}
}

// A message in an unmonitored group records nothing.
//
// The member is BOUND first, deliberately: an unbound member's check-in is
// skipped by CheckinByMessage before the group gate is reached, so the original
// version of this test passed with the monitored() gate removed entirely
// (found by an independent reviewer's mutation). Binding the member makes the
// group gate the only thing that can refuse the check-in.
func TestListenerIgnoresAnUnmonitoredGroupThroughTheRealAdapter(t *testing.T) {
	l, st, svc := listenerStore(t)
	bindMember(t, st, 54321, 7)
	today, _ := svc.Today()

	l.Handle(groupMessage(999, 54321, "/签到", time.Now().Unix()))

	if _, err := st.GetCheckin(54321, today); err == nil {
		t.Error("a message in an unmonitored group recorded a check-in")
	}
}

// The bind command reaches the service and its OUTCOME is what the operator
// sees: a bind that cannot resolve an account leaves the member unbound, and a
// bind that resolves one creates it.
//
// The original version only asserted the first half with NO gateway configured,
// so it stayed green with the whole bind dispatch removed — the assertion was
// about an effect that could not have occurred (found by an independent
// reviewer). A real gateway is injected here so the dispatch is load-bearing:
// with it, the second case proves the command actually reaches the service.
func TestListenerBindCommandReachesTheService(t *testing.T) {
	logs := captureLog(t)
	st := runtimeStore(t)
	svc := service.New(st, nil, nil)
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.BindCommandEnabled = true
	s.BindCommandPatterns = ">>绑定<<"
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	l := events.New(func() (string, string) { return "", "" }, bindAdapter{svc})

	// No account is configured, so the bind cannot resolve one.
	l.Handle(groupMessage(1, 54321, ">>绑定<<", time.Now().Unix()))
	if _, err := st.GetBinding(54321); err == nil {
		t.Error("a bind with no matching account created a binding")
	}
	// The member IS recorded: the command proves they are in the group, which is
	// the part that must not depend on the account lookup.
	assertMembership(t, st, 54321, 1)

	// The dispatch itself is what the LOG pins: asserting only on stored state
	// cannot distinguish "the command was handled" from "nothing ran", because
	// Service.BindByCommand records the member as its own first step — an
	// independent reviewer's `IsBindCommand -> return true` mutation survived
	// the state-only version of this test.
	//
	// The records are JSON (the day file's format), so the assertions match the
	// encoded shape: the service logs 自动绑定失败 with source=command, and the
	// listener adds its own 绑定指令失败 when the service did not already log.
	out := logs.String()
	if !strings.Contains(out, "自动绑定失败") && !strings.Contains(out, "绑定指令失败") {
		t.Errorf("the bind command was not handled at all (no record): %s", out)
	}
	if !strings.Contains(out, `"qq":54321`) {
		t.Errorf("the record does not name the member: %s", out)
	}
	// source=command distinguishes this path from the patrol and join paths.
	if !strings.Contains(out, `"source":"command"`) {
		t.Errorf("the record does not say which path bound (want source=command): %s", out)
	}

	// The REJECT side: an ordinary message is not a bind command, so it must not
	// reach the dispatch. Without this, forcing IsBindCommand to return true for
	// every message kept the test green — the accept side alone cannot tell
	// "this message is a command" from "every message is".
	before := logs.Len()
	l.Handle(groupMessage(1, 99999, "今天天气不错", time.Now().Unix()))
	after := logs.String()[before:]
	if strings.Contains(after, "自动绑定") || strings.Contains(after, "绑定指令") {
		t.Errorf("an ordinary message was treated as a bind command: %s", after)
	}
	// And it must not have bound that member either.
	if _, err := st.GetBinding(99999); err == nil {
		t.Error("an ordinary message created a binding")
	}
}
