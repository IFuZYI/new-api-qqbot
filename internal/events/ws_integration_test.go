package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestListenerBindsOverRealWebSocket drives the listener through an actual
// WebSocket connection, using the event shape confirmed against live NapCat:
//
//	{"time":…,"self_id":…,"post_type":"message","message_type":"group",
//	 "group_id":…,"user_id":…,"raw_message":">>绑定<<"}
func TestListenerBindsOverRealWebSocket(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1129535731: true}}

	var gotAuth string
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		// A meta event first, as NapCat sends on connect, then the command.
		_ = conn.WriteJSON(map[string]any{
			"time": 1791634871, "self_id": 3688665216,
			"post_type": "meta_event", "meta_event_type": "lifecycle",
		})
		_ = conn.WriteJSON(map[string]any{
			"time": 1791634898, "self_id": 3688665216,
			"post_type": "message", "message_type": "group",
			"group_id": 1129535731, "user_id": 54321, "raw_message": ">>绑定<<",
		})
		// Keep the socket open until the test finishes.
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	l := New(func() (string, string) { return url, "ws-tok" }, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go l.Run(ctx)

	deadline := time.After(4 * time.Second)
	for {
		if len(svc.bound) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the bind command was never dispatched over the WebSocket")
		case <-time.After(50 * time.Millisecond):
		}
	}

	if svc.bound[0] != 54321 {
		t.Errorf("bound the wrong member: %v", svc.bound)
	}
	if gotAuth != "Bearer ws-tok" {
		t.Errorf("token not attached to the handshake: %q", gotAuth)
	}
}

// Run must not block forever or panic when nothing is configured: the service
// has to keep working on polling alone.
func TestRunWithoutEndpointIsInert(t *testing.T) {
	l := New(func() (string, string) { return "", "" }, &fakeSvc{})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// A refused handshake must be retried, not fatal.
func TestRunRetriesAfterHandshakeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	l := New(func() (string, string) { return url, "bad" }, &fakeSvc{})

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()

	select {
	case <-done:
		// Returned on cancellation rather than dying on the 401.
	case <-time.After(6 * time.Second):
		t.Fatal("Run gave up instead of retrying")
	}
}

// A raw payload of the exact shape NapCat sends must dispatch the bind, which
// pins the field names against drift.
func TestHandleMatchesNapCatWireFormat(t *testing.T) {
	svc := &fakeSvc{monitored: map[int64]bool{1129535731: true}}
	l := New(func() (string, string) { return "", "" }, svc)

	raw, _ := json.Marshal(map[string]any{
		"time": 1791634898, "self_id": 3688665216,
		"post_type": "message", "message_type": "group", "sub_type": "normal",
		"message_id": 123, "group_id": 1129535731, "user_id": 54321,
		"raw_message": ">>绑定<<",
		"message":     []any{map[string]any{"type": "text", "data": map[string]any{"text": ">>绑定<<"}}},
	})
	l.Handle(raw)

	if len(svc.bound) != 1 || svc.bound[0] != 54321 {
		t.Fatalf("wire-format event did not bind: %v", svc.bound)
	}
}
