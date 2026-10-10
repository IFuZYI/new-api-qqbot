// Package events listens on NapCat's OneBot WebSocket for push events and acts
// on them. The patrols remain the backstop (see ADR-0003); this listener is
// what lets the two event-driven cases react immediately: the ">>绑定<<" command
// a member sends, and a member joining a group.
//
// There is deliberately no check-in handling here: NapCat exposes no check-in
// notice event, so the sign-in board can only be read by polling.
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
	BindByCommand(groupID, qq int64) error
	// EnsureMember records a member observed via an event, so the roster patrol
	// is not the only way a member becomes known.
	EnsureMember(groupID, qq int64, nickname string) error
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

// NoticeEvent is the OneBot notice event the listener cares about.
type NoticeEvent struct {
	PostType   string `json:"post_type"`
	NoticeType string `json:"notice_type"`
	GroupID    int64  `json:"group_id"`
	UserID     int64  `json:"user_id"`
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
// because the patrols are the backstop.
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
				log.Print("[events] no event WebSocket configured; relying on the patrols only")
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
	var head struct {
		PostType   string `json:"post_type"`
		NoticeType string `json:"notice_type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return
	}

	switch head.PostType {
	case "message":
		l.handleMessage(raw)
	case "notice":
		if head.NoticeType == "group_increase" {
			l.handleGroupIncrease(raw)
		}
	}
}

func (l *Listener) handleMessage(raw []byte) {
	var ev MessageEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if ev.GroupID == 0 || ev.UserID == 0 {
		return
	}
	if strings.TrimSpace(ev.RawMessage) != BindCommand {
		return
	}
	if !l.monitored(ev.GroupID) {
		return
	}
	if err := l.svc.BindByCommand(ev.GroupID, ev.UserID); err != nil {
		// The member is not told: this service does not speak in groups.
		log.Printf("[events] bind command from %d failed: %v", ev.UserID, err)
		return
	}
	log.Printf("[events] bound %d via %s", ev.UserID, BindCommand)
}

// handleGroupIncrease records a new member straight away, so they become known
// (and can be bound and converted) without waiting for a roster patrol.
//
// The event carries no nickname, so the member is stored with an empty one and
// the next roster patrol fills it in. That is complete enough to bind and to
// convert, which is what matters at join time.
func (l *Listener) handleGroupIncrease(raw []byte) {
	var ev NoticeEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if ev.GroupID == 0 || ev.UserID == 0 {
		return
	}
	if !l.monitored(ev.GroupID) {
		return
	}
	if err := l.svc.EnsureMember(ev.GroupID, ev.UserID, ""); err != nil {
		log.Printf("[events] recording new member %d in %d failed: %v", ev.UserID, ev.GroupID, err)
		return
	}
	log.Printf("[events] new member %d recorded in group %d", ev.UserID, ev.GroupID)
}

func (l *Listener) monitored(groupID int64) bool {
	ok, err := l.svc.IsMonitoredGroup(groupID)
	if err != nil {
		log.Printf("[events] group check failed for %d: %v", groupID, err)
		return false
	}
	return ok
}
