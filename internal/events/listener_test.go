package events

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSvc is driven from the listener goroutine and read from the test
// goroutine, so its recorders are guarded.
type fakeSvc struct {
	mu        sync.Mutex
	monitored map[int64]bool
	bound     []int64
	ensured   []ensureCall
	bindErr   error
	bindPanic bool
}

// boundQQs returns a copy of the bound QQs.
func (f *fakeSvc) boundQQs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.bound...)
}

// ensuredCalls returns a copy of the recorded EnsureMember calls.
func (f *fakeSvc) ensuredCalls() []ensureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ensureCall(nil), f.ensured...)
}

type ensureCall struct {
	groupID int64
	qq      int64
}

func (f *fakeSvc) EnsureMember(groupID, qq int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, ensureCall{groupID: groupID, qq: qq})
	return nil
}

func (f *fakeSvc) IsMonitoredGroup(groupID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.monitored[groupID], nil
}

func (f *fakeSvc) BindByCommand(groupID, qq int64) error {
	if f.bindPanic {
		panic("simulated failure deep in the bind path")
	}
	if f.bindErr != nil {
		return f.bindErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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

	if b := svc.boundQQs(); len(b) != 1 || b[0] != 54321 {
		t.Fatalf("expected 54321 to be bound, got %v", b)
	}
}

// The command must not work in a group this service does not monitor.
func TestBindCommandIgnoredOutsideMonitoredGroups(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1129535731: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle(msgEvent(999999999, 54321, BindCommand))

	if b := svc.boundQQs(); len(b) != 0 {
		t.Errorf("must not bind from an unmonitored group: %v", b)
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

// A new member must become known from the join event, without waiting for a
// roster patrol: that is what makes the member patrol optional.
func TestGroupIncreaseRecordsMember(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1129535731: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle([]byte(`{"time":1791634898,"self_id":3688665216,"post_type":"notice","notice_type":"group_increase","sub_type":"approve","group_id":1129535731,"user_id":54321,"operator_id":1}`))

	if len(svc.ensured) != 1 {
		t.Fatalf("new member not recorded: %+v", svc.ensured)
	}
	if svc.ensured[0].groupID != 1129535731 || svc.ensured[0].qq != 54321 {
		t.Errorf("wrong member recorded: %+v", svc.ensured[0])
	}
}

func TestGroupIncreaseIgnoredOutsideMonitoredGroups(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1129535731: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle([]byte(`{"post_type":"notice","notice_type":"group_increase","group_id":999999,"user_id":54321}`))

	if len(svc.ensured) != 0 {
		t.Errorf("must not record from an unmonitored group: %+v", svc.ensured)
	}
}

// Other notice types must stay untouched.
func TestOtherNoticesIgnored(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	for _, raw := range []string{
		`{"post_type":"notice","notice_type":"group_decrease","group_id":1,"user_id":54321}`,
		`{"post_type":"notice","notice_type":"group_card","group_id":1,"user_id":54321}`,
		`{"post_type":"notice","notice_type":"notify","sub_type":"gray_tip","group_id":1,"user_id":54321}`,
		`{"post_type":"meta_event","meta_event_type":"heartbeat"}`,
	} {
		l.Handle([]byte(raw))
	}
	if len(svc.ensured) != 0 || len(svc.bound) != 0 {
		t.Errorf("unexpected side effects: ensured=%+v bound=%+v", svc.ensured, svc.bound)
	}
}

// A join event with no user id (malformed) must be ignored, not recorded as QQ 0.
func TestGroupIncreaseWithZeroUserIgnored(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle([]byte(`{"post_type":"notice","notice_type":"group_increase","group_id":1,"user_id":0}`))

	if len(svc.ensured) != 0 {
		t.Errorf("a zero user id must be ignored: %+v", svc.ensured)
	}
}

// A panic while handling an event must not kill the process.
func TestHandlePanicIsContained(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1: true}, bindPanic: true}
	l := New(func() (string, string) { return "", "" }, svc)

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.handleSafely([]byte(`{"post_type":"message","group_id":1,"user_id":5,"raw_message":">>绑定<<"}`))
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleSafely did not return; a panic escaped")
	}
}
