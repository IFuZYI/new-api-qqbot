package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A DEBUG trace of an incoming event must describe the event, not quote it.
// The raw event carries the member's message text and nickname; writing it to
// the daily file would put group chat content on disk, where it is kept for the
// retention period and searchable from the panel.
func TestEventTraceDoesNotLogMessageContent(t *testing.T) {
	buf := captureLog(t)
	svc := &fakeSvc{monitored: map[int64]bool{}}
	l := New(func() (string, string) { return "", "" }, svc)

	raw, _ := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group",
		"group_id": 1129535731, "user_id": 54321,
		"raw_message": "我的手机号是 13800138000，晚上一起吃饭",
		"sender":      map[string]any{"nickname": "爱丽丝", "card": "小爱"},
	})
	l.handleSafely(raw)

	out := buf.String()
	for _, secret := range []string{"13800138000", "晚上一起吃饭", "爱丽丝", "小爱"} {
		if strings.Contains(out, secret) {
			t.Errorf("DEBUG trace leaked %q: %s", secret, out)
		}
	}
	// The structural facts are still there, so the trace is useful.
	for _, want := range []string{`"post_type":"message"`, `"group_id":1129535731`, `"user_id":54321`} {
		if !strings.Contains(out, want) {
			t.Errorf("trace is missing %s: %s", want, out)
		}
	}
}

// The trace is still written at DEBUG for a notice event.
func TestNoticeTraceKeepsItsShape(t *testing.T) {
	buf := captureLog(t)
	svc := &fakeSvc{monitored: map[int64]bool{1: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	raw, _ := json.Marshal(map[string]any{
		"post_type": "notice", "notice_type": "group_increase",
		"group_id": 1, "user_id": 2,
	})
	l.handleSafely(raw)

	out := buf.String()
	if !strings.Contains(out, `"notice_type":"group_increase"`) {
		t.Errorf("notice trace = %s", out)
	}
	if bytes.Contains([]byte(out), []byte("raw_message")) {
		t.Errorf("trace mentions raw_message: %s", out)
	}
}
