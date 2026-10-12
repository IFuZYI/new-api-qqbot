package events

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a bytes.Buffer that a goroutine can write while the test reads.
//
// The plain bytes.Buffer capture made TestTheConnectedRecordHidesTheToken a data
// race under -race: the listener goroutine wrote the connect record while the
// test polled buf.String(). It passed without -race and failed with it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// A bind command that failed is a WARN naming the member, so the operator can
// answer "I sent the command and nothing happened" from the log page.
func TestFailedBindCommandIsWarn(t *testing.T) {
	buf := captureLog(t)
	svc := &fakeSvc{monitored: map[int64]bool{1: true}, bindCmd: ">>绑定<<", bindErr: errors.New("no such account")}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle(msgEvent(1, 54321, ">>绑定<<"))

	out := buf.String()
	for _, want := range []string{`"level":"WARN"`, `"qq":54321`, `"group_id":1`, "no such account"} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %s: %s", want, out)
		}
	}
}

// A successful bind is an INFO record: it changes state, and the operator wants
// to see when and by which path a member was bound.
func TestSuccessfulBindCommandIsInfo(t *testing.T) {
	buf := captureLog(t)
	svc := &fakeSvc{monitored: map[int64]bool{1: true}, bindCmd: ">>绑定<<"}
	l := New(func() (string, string) { return "", "" }, svc)

	l.Handle(msgEvent(1, 54321, ">>绑定<<"))

	out := buf.String()
	if !strings.Contains(out, `"level":"INFO"`) || !strings.Contains(out, `"qq":54321`) {
		t.Errorf("no INFO record for the bind: %s", out)
	}
}

// A panic in an event handler is an ERROR with its stack.
func TestEventPanicIsError(t *testing.T) {
	buf := captureLog(t)
	svc := &fakeSvc{monitored: map[int64]bool{1: true}, bindCmd: ">>绑定<<", bindPanic: true}
	l := New(func() (string, string) { return "", "" }, svc)

	l.handleSafely(msgEvent(1, 5, ">>绑定<<"))

	out := buf.String()
	if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, "simulated failure") || !strings.Contains(out, "goroutine") {
		t.Errorf("panic log = %s", out)
	}
}

// Every event received is traced at DEBUG, so with DEBUG on the operator can
// see exactly what NapCat sent.
func TestEventReceivedIsDebug(t *testing.T) {
	buf := captureLog(t)
	svc := &fakeSvc{monitored: map[int64]bool{}}
	l := New(func() (string, string) { return "", "" }, svc)

	l.handleSafely(msgEvent(9, 5, "hello"))

	out := buf.String()
	if !strings.Contains(out, `"level":"DEBUG"`) || !strings.Contains(out, `"post_type":"message"`) {
		t.Errorf("no DEBUG trace of the event: %s", out)
	}
}
