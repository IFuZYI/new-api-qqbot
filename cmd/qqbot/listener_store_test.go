package main

import (
	"encoding/json"
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
func TestListenerIgnoresANonMatchingMessageThroughTheRealAdapter(t *testing.T) {
	l, st, svc := listenerStore(t)
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
func TestListenerRecordsNothingWhenTheMessagePathIsOff(t *testing.T) {
	l, st, svc := listenerStore(t)
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

// A join event records the membership, which is what makes the roster patrol
// optional (ADR-0003's revision).
func TestListenerRecordsAJoinThroughTheRealAdapter(t *testing.T) {
	l, st, _ := listenerStore(t)
	// The join path records the member only for a monitored group; the helper
	// registers and enables group 1. A member with no nickname is stored with an
	// empty one and filled in by the next roster patrol (the event carries none).
	raw, _ := json.Marshal(map[string]any{
		"post_type": "notice", "notice_type": "group_increase",
		"group_id": 1, "user_id": 54321,
	})
	l.Handle(raw)

	ms, err := st.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.QQ == 54321 {
			return
		}
	}
	t.Errorf("the join was not recorded: members = %+v", ms)
}

// A message in an unmonitored group records nothing.
func TestListenerIgnoresAnUnmonitoredGroupThroughTheRealAdapter(t *testing.T) {
	l, st, svc := listenerStore(t)
	today, _ := svc.Today()

	l.Handle(groupMessage(999, 54321, "/签到", time.Now().Unix()))

	if _, err := st.GetCheckin(54321, today); err == nil {
		t.Error("a message in an unmonitored group recorded a check-in")
	}
}

// The bind command reaches the service: with no account configured the bind
// fails, and the failure must not create a binding.
func TestListenerBindCommandDoesNotCreateABindingWithoutAnAccount(t *testing.T) {
	l, st, _ := listenerStore(t)

	l.Handle(groupMessage(1, 54321, ">>绑定<<", time.Now().Unix()))

	if _, err := st.GetBinding(54321); err == nil {
		t.Error("a bind with no matching account created a binding")
	}
}
