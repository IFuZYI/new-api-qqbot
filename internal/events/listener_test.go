package events

import (
	"encoding/json"
	"errors"
	"testing"
)

type fakeSvc struct {
	monitored map[int64]bool
	bound     []int64
	bindErr   error
}

func (f *fakeSvc) IsMonitoredGroup(groupID int64) (bool, error) {
	return f.monitored[groupID], nil
}

func (f *fakeSvc) BindByCommand(qq int64) error {
	if f.bindErr != nil {
		return f.bindErr
	}
	f.bound = append(f.bound, qq)
	return nil
}

func msgEvent(groupID, userID int64, raw string) []byte {
	b, _ := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group",
		"group_id": groupID, "user_id": userID, "raw_message": raw,
	})
	return b
}

func TestBindCommandBindsMember(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1129535731: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle(msgEvent(1129535731, 54321, BindCommand))

	if len(svc.bound) != 1 || svc.bound[0] != 54321 {
		t.Fatalf("expected 54321 to be bound, got %v", svc.bound)
	}
}

// The command must not work in a group this service does not monitor.
func TestBindCommandIgnoredOutsideMonitoredGroups(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1129535731: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle(msgEvent(999999999, 54321, BindCommand))

	if len(svc.bound) != 0 {
		t.Errorf("must not bind from an unmonitored group: %v", svc.bound)
	}
}

func TestOnlyExactCommandTriggers(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	// Anything with extra content, or a truncated variant, must not trigger.
	for _, raw := range []string{"绑定", ">>绑定", "绑定<<", ">>绑定<< 你好", ">>绑定<<abc", "你好", ">> 绑定 <<"} {
		l.Handle(msgEvent(1, 54321, raw))
	}
	if len(svc.bound) != 0 {
		t.Errorf("only the exact command should bind, got %v", svc.bound)
	}
}

// QQ clients routinely append a space or newline, so surrounding whitespace is
// tolerated while extra content is not.
func TestCommandToleratesSurroundingWhitespace(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle(msgEvent(1, 54321, " >>绑定<< "))
	l.Handle(msgEvent(1, 54322, ">>绑定<<\n"))

	if len(svc.bound) != 2 {
		t.Errorf("surrounding whitespace should be tolerated, got %v", svc.bound)
	}
}

func TestNonMessageAndPrivateEventsIgnored(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	// a notice event
	l.Handle([]byte(`{"post_type":"notice","notice_type":"group_increase","group_id":1,"user_id":54321}`))
	// a private message (no group)
	l.Handle([]byte(`{"post_type":"message","message_type":"private","user_id":54321,"raw_message":">>绑定<<"}`))
	// malformed
	l.Handle([]byte(`not json`))

	if len(svc.bound) != 0 {
		t.Errorf("unexpected binds: %v", svc.bound)
	}
}

// A failed bind must not panic or retry-storm; it is logged and dropped.
func TestBindFailureIsTolerated(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1: true}, bindErr: errors.New("no such account")}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle(msgEvent(1, 54321, BindCommand))

	if len(svc.bound) != 0 {
		t.Errorf("a failed bind must not be recorded: %v", svc.bound)
	}
}
