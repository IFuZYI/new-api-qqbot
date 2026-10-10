// Package events listens on NapCat's OneBot WebSocket for push events and acts
// on them. Polling remains the source of truth (see ADR-0003); this listener
// only shortens the reaction time for things that need it, currently the
// ">>绑定<<" command a member sends in a monitored group.
package events

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// BindCommand is the message a member sends to link their QQ to the new-api
// account registered under their QQ email.
const BindCommand = ">>绑定<<"

// Service is the subset of business logic the listener needs.
type Service interface {
	IsMonitoredGroup(groupID int64) (bool, error)
	BindByCommand(qq int64) error
}

// MessageEvent is the OneBot message event the listener cares about.
type MessageEvent struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"`
	GroupID     int64  `json:"group_id"`
	UserID      int64  `json:"user_id"`
	RawMessage  string `json:"raw_message"`
	Message     any    `json:"message"`
}

// Endpoint supplies the current WebSocket URL and token. It is read on every
// reconnect so an edit in the WebUI takes effect without a restart.
type Endpoint func() (url, token string)

// Listener maintains a WebSocket connection to NapCat and dispatches events.
type Listener struct {
	endpoint Endpoint
	svc      Service
}

// New builds a Listener. endpoint is called on each connection attempt.
func New(endpoint Endpoint, svc Service) *Listener {
	return &Listener{endpoint: endpoint, svc: svc}
}

// Run connects and keeps reconnecting until ctx is cancelled. It never returns
// an error: a missing or broken event stream must not take the service down,
// because polling alone is sufficient for correctness.
func (l *Listener) Run(ctx context.Context) {
	backoff := 2 * time.Second
	const maxBackoff = 2 * time.Minute
	loggedIdle := false

	for {
		if ctx.Err() != nil {
			return
		}
		url, token := l.endpoint()
		if strings.TrimSpace(url) == "" {
			if !loggedIdle {
				log.Print("[events] no event WebSocket configured; relying on polling only")
				loggedIdle = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			continue
		}
		loggedIdle = false

		err := l.session(ctx, url, token)
		if ctx.Err() != nil {
			return
		}
		log.Printf("[events] disconnected: %v; retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// session runs one connection until it fails.
func (l *Listener) session(ctx context.Context, url, token string) error {
	header := map[string][]string{}
	if token != "" {
		header["Authorization"] = []string{"Bearer " + token}
		// NapCat also accepts the token as a WebSocket subprotocol.
		header["Sec-WebSocket-Protocol"] = []string{"Bearer," + token}
	}

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			return &handshakeError{status: resp.Status, err: err}
		}
		return err
	}
	defer conn.Close()

	log.Printf("[events] connected to %s", url)

	// Closing the connection on shutdown unblocks ReadMessage.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		l.Handle(raw)
	}
}

type handshakeError struct {
	status string
	err    error
}

func (e *handshakeError) Error() string {
	return "websocket handshake failed: " + e.status + ": " + e.err.Error()
}

// Handle processes one raw event. It is exported so it can be driven directly
// from tests and from the WebUI's debug console.
func (l *Listener) Handle(raw []byte) {
	var ev MessageEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if ev.PostType != "message" || ev.GroupID == 0 || ev.UserID == 0 {
		return
	}
	if strings.TrimSpace(ev.RawMessage) != BindCommand {
		return
	}

	// Only react inside groups this service actually monitors.
	ok, err := l.svc.IsMonitoredGroup(ev.GroupID)
	if err != nil {
		log.Printf("[events] group check failed for %d: %v", ev.GroupID, err)
		return
	}
	if !ok {
		return
	}

	if err := l.svc.BindByCommand(ev.UserID); err != nil {
		// The member is not told: this service does not speak in groups.
		log.Printf("[events] bind command from %d failed: %v", ev.UserID, err)
		return
	}
	log.Printf("[events] bound %d via %s", ev.UserID, BindCommand)
}
