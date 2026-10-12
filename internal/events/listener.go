// Package events listens on NapCat's OneBot WebSocket for push events and acts
// on them. The patrols remain the backstop (see ADR-0003); this listener is
// what lets the event-driven cases react immediately: the configured bind
// command a member sends, a member joining a group, and a message that counts
// as the day's check-in.
//
// Which of those are active, and what the commands are, comes from the service's
// settings: every path here has a switch, so an operator who does not use one
// pays nothing for it. There is deliberately no sign-in-board handling here:
// NapCat exposes no check-in notice event, so the board can only be read by
// polling.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Service is the subset of business logic the listener needs.
type Service interface {
	IsMonitoredGroup(groupID int64) (bool, error)

	// BindByCommand binds a member who sent a bind command.
	BindByCommand(groupID, qq int64) error
	// BindMember binds a member without a command (the join path).
	BindMember(groupID, qq int64) error
	// IsBindCommand reports whether a message is one of the configured bind
	// commands. The listener does not know the patterns itself, so the operator
	// can change them without a restart.
	IsBindCommand(message string) bool

	// CheckinByMessage records a message-driven check-in.
	CheckinByMessage(groupID, qq int64) (CheckinOutcome, error)
	// IsCheckinCommand reports whether a message is one of the configured
	// check-in commands.
	IsCheckinCommand(message string) bool
	// CheckinByAnyMessage reports whether every message counts as a check-in.
	CheckinByAnyMessage() bool

	// BindOnJoinEnabled reports whether a member is bound when they join.
	BindOnJoinEnabled() bool

	// EnsureMember records a member observed via an event, so the roster patrol
	// is not the only way a member becomes known.
	EnsureMember(groupID, qq int64, nickname string) error
}

// CheckinOutcome is what a message-driven check-in did. The listener only needs
// to know whether the day was recorded, which is what the log line says.
type CheckinOutcome struct {
	Recorded         bool
	AlreadyCheckedIn bool
	// Unbound means the member has no account bound yet, so the check-in was
	// not recorded: the reward is paid to the bound account (see
	// service.CheckinByMessage).
	Unbound bool
}

// MessageEvent is the OneBot message event the listener cares about.
type MessageEvent struct {
	Time        *int64 `json:"time"`
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
				slog.Info("未配置事件 WebSocket，仅依赖定期巡检")
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
		slog.Warn("事件 WebSocket 断开，稍后重连", "err", err, "retry_in", backoff.String())
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

// Probe opens the event stream just long enough to prove it accepts this
// service's credentials, then closes it. It exists so the connection test can
// verify the event WebSocket without leaving a second consumer attached (a
// second connection would receive its own copy of every event).
func Probe(url, token string) error {
	header := map[string][]string{}
	if token != "" {
		header["Authorization"] = []string{"Bearer " + token}
		// NapCat also accepts the token as a WebSocket subprotocol.
		header["Sec-WebSocket-Protocol"] = []string{"Bearer," + token}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			return &handshakeError{status: resp.Status, err: err}
		}
		return err
	}
	return conn.Close()
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

	slog.Info("事件 WebSocket 已连接", "url", url)

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
		// A panic while handling one event must not kill the process: recover,
		// log, and keep the stream alive.
		l.handleSafely(raw)
	}
}

type handshakeError struct {
	status string
	err    error
}

func (e *handshakeError) Error() string {
	return "websocket handshake failed: " + e.status + ": " + e.err.Error()
}

// handleSafely runs Handle with panic recovery.
func (l *Listener) handleSafely(raw []byte) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("处理事件时发生 panic", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	l.Handle(raw)
}

// Handle processes one raw event. It is exported so it can be driven directly
// from tests and from the WebUI's debug console.
func (l *Listener) Handle(raw []byte) {
	// Only the routing fields are decoded here, and the DEBUG trace below uses
	// the same struct: the raw event is never written to the log, because it
	// carries the member's message text and nickname and the daily file is kept
	// for the retention period and searchable from the panel.
	var head struct {
		PostType    string `json:"post_type"`
		NoticeType  string `json:"notice_type"`
		MessageType string `json:"message_type"`
		GroupID     int64  `json:"group_id"`
		UserID      int64  `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		slog.Debug("忽略无法解析的事件", "err", err, "bytes", len(raw))
		return
	}
	// Meta events (heartbeats) arrive every few seconds and say nothing.
	if head.PostType != "meta_event" {
		slog.Debug("收到事件",
			"post_type", head.PostType,
			"notice_type", head.NoticeType,
			"message_type", head.MessageType,
			"group_id", head.GroupID,
			"user_id", head.UserID,
			"bytes", len(raw),
		)
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

// handleMessage dispatches one group message: it may be a bind command and/or a
// check-in, and the two are independent (a bind command is also a message, so
// with message check-ins on it counts for both).
func (l *Listener) handleMessage(raw []byte) {
	var ev MessageEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	// Legacy producers omitted message_type; explicitly non-group messages
	// must never gain group privileges.
	if ev.GroupID <= 0 || ev.UserID <= 0 || (ev.MessageType != "" && ev.MessageType != "group") {
		return
	}
	if !l.monitored(ev.GroupID) {
		return
	}
	text := strings.TrimSpace(ev.RawMessage)
	if text == "" {
		return
	}

	if l.svc.IsBindCommand(text) {
		if err := l.svc.BindByCommand(ev.GroupID, ev.UserID); err != nil {
			// The member is not told: this service does not speak in groups.
			slog.Warn("绑定指令失败", "group_id", ev.GroupID, "qq", ev.UserID, "err", err)
		} else {
			slog.Info("已通过绑定指令绑定", "group_id", ev.GroupID, "qq", ev.UserID)
		}
	}

	// A check-in is evaluated separately from the bind command: the operator can
	// switch either path on without the other.
	// Missing timestamps retain legacy behaviour. A supplied timestamp needs the
	// service's configured calendar, not the listener host's timezone.
	if ev.Time != nil {
		calendar, ok := l.svc.(interface{ MessageIsToday(int64) bool })
		if !ok || *ev.Time <= 0 || !calendar.MessageIsToday(*ev.Time) {
			return
		}
	}
	if l.svc.CheckinByAnyMessage() || l.svc.IsCheckinCommand(text) {
		res, err := l.svc.CheckinByMessage(ev.GroupID, ev.UserID)
		switch {
		case err != nil:
			slog.Warn("消息签到失败", "group_id", ev.GroupID, "qq", ev.UserID, "err", err)
		case res.Recorded:
			slog.Info("已记录消息签到", "group_id", ev.GroupID, "qq", ev.UserID)
		case res.Unbound:
			// Not an error: the member simply has to bind first.
			slog.Debug("消息签到跳过：未绑定账号", "group_id", ev.GroupID, "qq", ev.UserID)
		}
	}
}

// handleGroupIncrease records a new member straight away, so they become known
// (and can be bound and converted) without waiting for a roster patrol, and
// binds them immediately when the operator has that path switched on.
//
// The event carries no nickname, so the member is stored with an empty one and
// the next roster patrol fills it in. That is complete enough to bind and to
// convert, which is what matters at join time.
func (l *Listener) handleGroupIncrease(raw []byte) {
	var ev NoticeEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if ev.GroupID <= 0 || ev.UserID <= 0 {
		return
	}
	if !l.monitored(ev.GroupID) {
		return
	}
	if err := l.svc.EnsureMember(ev.GroupID, ev.UserID, ""); err != nil {
		slog.Error("记录新成员失败", "group_id", ev.GroupID, "qq", ev.UserID, "err", err)
		return
	}
	slog.Info("已记录新成员", "group_id", ev.GroupID, "qq", ev.UserID)

	// Binding is a separate switch, and its failure must not undo the
	// recording above: the member is known either way, and the patrol retries.
	if !l.svc.BindOnJoinEnabled() {
		return
	}
	if err := l.svc.BindMember(ev.GroupID, ev.UserID); err != nil {
		slog.Warn("进群自动绑定失败", "group_id", ev.GroupID, "qq", ev.UserID, "err", err)
		return
	}
	slog.Info("进群自动绑定成功", "group_id", ev.GroupID, "qq", ev.UserID)
}

func (l *Listener) monitored(groupID int64) bool {
	ok, err := l.svc.IsMonitoredGroup(groupID)
	if err != nil {
		slog.Error("查询监测群失败", "group_id", groupID, "err", err)
		return false
	}
	return ok
}
