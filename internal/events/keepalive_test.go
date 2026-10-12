package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A socket that stays open but sends nothing must not be trusted forever.
//
// Measured before this test: a server that completed the handshake and then went
// silent left the listener "connected" indefinitely — one connection, no
// reconnect, no record — because ReadMessage only returns on a read error and a
// half-open socket (NapCat alive but wedged, or a NAT drop with no FIN) produces
// none. Every event after that point is lost silently while the panel shows the
// stream as connected.
//
// The fix is a server-side pong requirement: the listener pings, and a peer that
// stops answering is disconnected so Run reconnects.
func TestSilentSocketIsReconnected(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	// The first connection goes silent after the handshake (it never reads, so
	// it never answers a ping). The second behaves.
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		conns++
		n := conns
		mu.Unlock()
		if n == 1 {
			// Hold the socket open and never read: a wedged peer. It stays
			// "connected" at the TCP level and answers nothing.
			time.Sleep(30 * time.Second)
			_ = c.Close()
			return
		}
		// The replacement connection sends one event so the test can prove the
		// listener recovered rather than merely reconnecting.
		ev, _ := json.Marshal(map[string]any{
			"post_type": "notice", "notice_type": "group_increase",
			"group_id": 1, "user_id": 54321,
		})
		_ = c.WriteMessage(websocket.TextMessage, ev)
		time.Sleep(2 * time.Second)
		_ = c.Close()
	}))
	defer srv.Close()

	// The listener needs the group to look monitored for the join to record.
	svc := &fakeSvc{monitored: map[int64]bool{1: true}}
	l := New(func() (string, string) { return "ws" + strings.TrimPrefix(srv.URL, "http"), "" }, svc)
	// The ping cadence is an implementation constant; the test's server goes
	// silent on connection 1 and answers nothing, so the listener must give up
	// on it and dial again within a bounded time.
	l.pingInterval = 300 * time.Millisecond
	l.pongWait = 900 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go l.Run(ctx)

	// The join from the SECOND connection is the proof: the listener abandoned
	// the silent socket and reconnected.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(svc.ensuredCalls()) > 0 {
			mu.Lock()
			n := conns
			mu.Unlock()
			if n < 2 {
				t.Errorf("the join was recorded but only %d connection(s) were made", n)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	n := conns
	mu.Unlock()
	t.Fatalf("the silent socket was never abandoned: connections = %d, joins recorded = %d",
		n, len(svc.ensuredCalls()))
}

// The ping cadence must be short enough to notice a wedged peer, and the read
// limit must be set so one huge payload cannot be read whole into memory
// (gorilla's default of 0 means unlimited).
func TestSessionSetsAReadLimit(t *testing.T) {
	if got := readLimit(); got <= 0 {
		t.Fatalf("read limit = %d; gorilla's 0 means unlimited", got)
	}
	// A few MiB: large enough for a real event, small enough that a rogue peer
	// cannot exhaust memory.
	if got := readLimit(); got > 32<<20 {
		t.Errorf("read limit = %d bytes, too large to be a bound", got)
	}
}
