package events

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// featureSvc is the listener's view of the service for the feature paths. It is
// separate from fakeSvc so the existing tests keep pinning the old contract.
type featureSvc struct {
	mu sync.Mutex

	monitored map[int64]bool

	bindCommands  map[string]bool // messages that count as a bind command
	checkinCmds   map[string]bool // messages that count as a check-in command
	anyMessage    bool            // "any message is a check-in"
	bindOnJoin    bool            // bind a member when they join
	checkinOnJoin bool            // check in a member when they join

	binds       []int64
	checkins    []checkinCall
	joinBinds   []int64
	ensured     []int64
	bindErr     error
	checkinErr  error
	joinBindErr error
}

type checkinCall struct {
	groupID int64
	qq      int64
}

func (f *featureSvc) EnsureMember(groupID, qq int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, qq)
	return nil
}

func (f *featureSvc) IsMonitoredGroup(groupID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.monitored[groupID], nil
}

func (f *featureSvc) BindByCommand(groupID, qq int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bindErr != nil {
		return f.bindErr
	}
	f.binds = append(f.binds, qq)
	return nil
}

func (f *featureSvc) IsBindCommand(message string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bindCommands[message]
}

func (f *featureSvc) IsCheckinCommand(message string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkinCmds[message]
}

func (f *featureSvc) CheckinByAnyMessage() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.anyMessage
}

func (f *featureSvc) BindOnJoinEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bindOnJoin
}

func (f *featureSvc) CheckinByMessage(groupID, qq int64) (CheckinOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkinErr != nil {
		return CheckinOutcome{}, f.checkinErr
	}
	f.checkins = append(f.checkins, checkinCall{groupID: groupID, qq: qq})
	return CheckinOutcome{Recorded: true}, nil
}

func (f *featureSvc) BindMember(groupID, qq int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.joinBindErr != nil {
		return f.joinBindErr
	}
	f.joinBinds = append(f.joinBinds, qq)
	return nil
}

func (f *featureSvc) recorded() (binds, joinBinds, ensured []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.binds...),
		append([]int64(nil), f.joinBinds...),
		append([]int64(nil), f.ensured...)
}

func (f *featureSvc) checkinCalls() []checkinCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]checkinCall(nil), f.checkins...)
}

func newFeatureListener(svc *featureSvc) *Listener {
	return New(func() (string, string) { return "", "" }, svc)
}

// ---------- bind command ----------

// The command is whatever the operator configured, not a hardcoded string.
func TestBindCommandFollowsConfiguredPatterns(t *testing.T) {
	svc := &featureSvc{
		monitored:    map[int64]bool{1: true},
		bindCommands: map[string]bool{"绑定我": true},
	}
	l := newFeatureListener(svc)

	l.Handle(msgEvent(1, 54321, "绑定我"))
	if b, _, _ := svc.recorded(); len(b) != 1 || b[0] != 54321 {
		t.Fatalf("the configured command did not bind: %v", b)
	}

	// The old hardcoded command must no longer work once it is not configured.
	l.Handle(msgEvent(1, 54322, ">>绑定<<"))
	if b, _, _ := svc.recorded(); len(b) != 1 {
		t.Errorf("an unconfigured command must not bind: %v", b)
	}
}

// A message that is not a bind command must not bind, however close it looks.
func TestNonCommandMessageDoesNotBind(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{1: true}, bindCommands: map[string]bool{"/绑定": true}}
	l := newFeatureListener(svc)

	for _, raw := range []string{"绑定", "你好", "/绑定 一下", ""} {
		l.Handle(msgEvent(1, 54321, raw))
	}
	if b, _, _ := svc.recorded(); len(b) != 0 {
		t.Errorf("only a configured command may bind, got %v", b)
	}
}

// A bind command is not a check-in command, and vice versa: the two lists are
// independent.
func TestBindAndCheckinCommandsAreIndependent(t *testing.T) {
	svc := &featureSvc{
		monitored:    map[int64]bool{1: true},
		bindCommands: map[string]bool{"/绑定": true},
		checkinCmds:  map[string]bool{"/签到": true},
	}
	l := newFeatureListener(svc)

	l.Handle(msgEvent(1, 54321, "/签到"))
	binds, _, _ := svc.recorded()
	if len(binds) != 0 {
		t.Errorf("a check-in command must not bind: %v", binds)
	}
	if calls := svc.checkinCalls(); len(calls) != 1 {
		t.Errorf("a check-in command should record a check-in: %+v", calls)
	}

	l.Handle(msgEvent(1, 54321, "/绑定"))
	binds, _, _ = svc.recorded()
	if len(binds) != 1 {
		t.Errorf("a bind command should bind: %v", binds)
	}
	if calls := svc.checkinCalls(); len(calls) != 1 {
		t.Errorf("a bind command must not check in: %+v", calls)
	}
}

// A bind command that fails is logged and dropped, as before.
func TestBindCommandFailureTolerated(t *testing.T) {
	svc := &featureSvc{
		monitored:    map[int64]bool{1: true},
		bindCommands: map[string]bool{"/绑定": true},
		bindErr:      errors.New("no such account"),
	}
	l := newFeatureListener(svc)

	l.Handle(msgEvent(1, 54321, "/绑定"))
	if b, _, _ := svc.recorded(); len(b) != 0 {
		t.Errorf("a failed bind must not be recorded: %v", b)
	}
}

// ---------- message check-ins ----------

// A qualifying message records a check-in.
func TestCheckinCommandRecordsCheckin(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{7: true}, checkinCmds: map[string]bool{"/签到": true}}
	l := newFeatureListener(svc)

	l.Handle(msgEvent(7, 54321, "/签到"))

	calls := svc.checkinCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 check-in, got %+v", calls)
	}
	if calls[0].groupID != 7 || calls[0].qq != 54321 {
		t.Errorf("wrong check-in recorded: %+v", calls[0])
	}
}

// With "any message is a check-in" on, an ordinary message counts.
func TestAnyMessageCountsAsCheckin(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{7: true}, anyMessage: true}
	l := newFeatureListener(svc)

	l.Handle(msgEvent(7, 54321, "大家早上好"))

	if calls := svc.checkinCalls(); len(calls) != 1 {
		t.Errorf("a plain message should check in: %+v", calls)
	}
}

// With both switched off, a message is just a message.
func TestNoCheckinWhenDisabled(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{7: true}}
	l := newFeatureListener(svc)

	l.Handle(msgEvent(7, 54321, "随便说说"))
	l.Handle(msgEvent(7, 54321, "/签到"))

	if calls := svc.checkinCalls(); len(calls) != 0 {
		t.Errorf("no check-in should be recorded: %+v", calls)
	}
}

// A message from an unmonitored group must not check in: the operator did not
// choose that group, and the bot can be invited anywhere.
func TestCheckinIgnoredOutsideMonitoredGroups(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{7: true}, anyMessage: true}
	l := newFeatureListener(svc)

	l.Handle(msgEvent(999, 54321, "你好"))

	if calls := svc.checkinCalls(); len(calls) != 0 {
		t.Errorf("must not check in from an unmonitored group: %+v", calls)
	}
}

// A private message is not a group check-in.
func TestPrivateMessageIsNotACheckin(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{7: true}, anyMessage: true}
	l := newFeatureListener(svc)

	raw, _ := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "private",
		"user_id": 54321, "raw_message": "你好",
	})
	l.Handle(raw)

	if calls := svc.checkinCalls(); len(calls) != 0 {
		t.Errorf("a private message must not check in: %+v", calls)
	}
}

// A check-in failure must not panic or take the stream down.
func TestCheckinFailureTolerated(t *testing.T) {
	svc := &featureSvc{
		monitored: map[int64]bool{7: true}, anyMessage: true,
		checkinErr: errors.New("store closed"),
	}
	l := newFeatureListener(svc)

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.handleSafely(msgEvent(7, 54321, "你好"))
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleSafely did not return")
	}
}

// ---------- bind on join ----------

// A member joining a monitored group is bound immediately when the switch is on.
func TestJoinBindsMemberWhenEnabled(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{1129535731: true}, bindOnJoin: true}
	l := newFeatureListener(svc)

	l.Handle([]byte(`{"post_type":"notice","notice_type":"group_increase","group_id":1129535731,"user_id":54321}`))

	if _, joinBinds, _ := svc.recorded(); len(joinBinds) != 1 || joinBinds[0] != 54321 {
		t.Fatalf("the new member should have been bound: %v", joinBinds)
	}
}

// With the switch off the member is still recorded, just not bound: recording
// is what makes the roster patrol optional.
func TestJoinRecordsMemberWithoutBindingWhenDisabled(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{1: true}}
	l := newFeatureListener(svc)

	l.Handle([]byte(`{"post_type":"notice","notice_type":"group_increase","group_id":1,"user_id":54321}`))

	_, joinBinds, ensured := svc.recorded()
	if len(joinBinds) != 0 {
		t.Errorf("must not bind with the switch off: %v", joinBinds)
	}
	if len(ensured) != 1 {
		t.Errorf("the member must still be recorded: %v", ensured)
	}
}

// A failing join-bind must not stop the member being recorded, and must not
// take the stream down.
func TestJoinBindFailureIsTolerated(t *testing.T) {
	svc := &featureSvc{
		monitored: map[int64]bool{1: true}, bindOnJoin: true,
		joinBindErr: errors.New("gateway down"),
	}
	l := newFeatureListener(svc)

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.handleSafely([]byte(`{"post_type":"notice","notice_type":"group_increase","group_id":1,"user_id":54321}`))
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleSafely did not return")
	}
	if _, _, ensured := svc.recorded(); len(ensured) != 1 {
		t.Errorf("the member should still be recorded: %v", ensured)
	}
}

// A join in an unmonitored group is ignored entirely.
func TestJoinIgnoredOutsideMonitoredGroups(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{1: true}, bindOnJoin: true}
	l := newFeatureListener(svc)

	l.Handle([]byte(`{"post_type":"notice","notice_type":"group_increase","group_id":999,"user_id":54321}`))

	binds, joinBinds, ensured := svc.recorded()
	if len(binds)+len(joinBinds)+len(ensured) != 0 {
		t.Errorf("an unmonitored group must be ignored: %v %v %v", binds, joinBinds, ensured)
	}
}

// A join event with no user id must be ignored, not recorded as QQ 0.
func TestJoinWithZeroUserIgnored(t *testing.T) {
	svc := &featureSvc{monitored: map[int64]bool{1: true}, bindOnJoin: true}
	l := newFeatureListener(svc)

	l.Handle([]byte(`{"post_type":"notice","notice_type":"group_increase","group_id":1,"user_id":0}`))

	if _, joinBinds, ensured := svc.recorded(); len(joinBinds)+len(ensured) != 0 {
		t.Errorf("a zero user id must be ignored: %v %v", joinBinds, ensured)
	}
}

// The bind command must also check the member in when that path is on: sending
// a command is sending a message.
func TestBindCommandAlsoChecksIn(t *testing.T) {
	svc := &featureSvc{
		monitored:    map[int64]bool{1: true},
		bindCommands: map[string]bool{"/绑定": true},
		anyMessage:   true,
	}
	l := newFeatureListener(svc)

	l.Handle(msgEvent(1, 54321, "/绑定"))

	binds, _, _ := svc.recorded()
	if len(binds) != 1 {
		t.Fatalf("the command should bind: %v", binds)
	}
	if calls := svc.checkinCalls(); len(calls) != 1 {
		t.Errorf("the command message should also check in: %+v", calls)
	}
}
