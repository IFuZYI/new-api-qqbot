package events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A token carried in the event-WebSocket URL's query string must not reach the
// log.
//
// NapCat accepts the OneBot token as ?access_token=, and this service stores
// such URLs (see napcat.ParseWSURL). The day file is written 0644 and kept for
// the whole retention period, and the panel can search it — so an unredacted URL
// is a token at rest, readable by every local user. An independent reviewer
// measured REALLOGTOKEN123 landing in the real day file.
func TestTheEventURLIsRedactedBeforeLogging(t *testing.T) {
	for _, c := range []struct {
		in       string
		mustHide []string
		mustKeep string
	}{
		{"wss://napcat.invalid:3001/onebot?access_token=SECRET123&x=1", []string{"SECRET123", "access_token", "x=1"}, "napcat.invalid:3001"},
		{"ws://user:pass@napcat.invalid:3001/", []string{"user", "pass@"}, "napcat.invalid:3001"},
		{"wss://napcat.invalid:3001/onebot#frag-SECRET", []string{"frag-SECRET"}, "napcat.invalid:3001"},
	} {
		got := redactURL(c.in)
		for _, secret := range c.mustHide {
			if strings.Contains(got, secret) {
				t.Errorf("redactURL(%q) = %q, which still carries %q", c.in, got, secret)
			}
		}
		if c.mustKeep != "" && !strings.Contains(got, c.mustKeep) {
			t.Errorf("redactURL(%q) = %q, which lost the host the operator needs", c.in, got)
		}
	}
}

// An unparsable URL is reported as a placeholder, never echoed: the value cannot
// be trusted to be secret-free.
func TestAnUnparsableURLIsNotEchoed(t *testing.T) {
	got := redactURL("://not a url with SECRET in it")
	if strings.Contains(got, "SECRET") {
		t.Errorf("redactURL echoed the raw value: %q", got)
	}
}

// End to end through the REAL connect path: the record the listener writes on a
// successful connection must not carry the URL's token.
func TestTheConnectedRecordHidesTheToken(t *testing.T) {
	buf := captureLog(t)

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		// Hold the socket briefly so the connect record is written.
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/onebot?access_token=SECRET123"
	svc := &fakeSvc{monitored: map[int64]bool{}}
	l := New(func() (string, string) { return url, "" }, svc)
	l.pingInterval = 50 * time.Millisecond
	l.pongWait = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go l.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "已连接") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, "已连接") {
		t.Fatalf("the connect record was never written; the test would pass vacuously: %q", out)
	}
	if strings.Contains(out, "SECRET123") {
		t.Errorf("the token from the URL reached the log: %q", out)
	}
	if !strings.Contains(out, "napcat.invalid") && !strings.Contains(out, srv.Listener.Addr().String()) {
		t.Errorf("the record lost the host, which is the only part the operator needs: %q", out)
	}
}
