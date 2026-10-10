// Package napcat talks to a local NapCat instance. Two transports are
// supported and both are needed: the OneBot network adapter (HTTP or
// WebSocket) for routine action calls and event push, and the WebUI's debug
// bridge, which can drive OneBot actions and rewrite the OneBot config without
// restarting NapCat.
package napcat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const userAgent = "qqbot-group-monitor/0.1 (+self-hosted)"

// Group is a QQ group the bot account belongs to.
type Group struct {
	GroupID        int64  `json:"group_id"`
	GroupName      string `json:"group_name"`
	MemberCount    int    `json:"member_count"`
	MaxMemberCount int    `json:"max_member_count"`
}

// GroupMember is one member of a group.
//
// Level and QQLevel arrive as different JSON types from NapCat (`level` is a
// string, `qq_level` a number), hence flexInt.
type GroupMember struct {
	QQ       int64   `json:"user_id"`
	Nickname string  `json:"nickname"`
	Card     string  `json:"card"`
	Level    flexInt `json:"level"`    // 群等级
	QQLevel  flexInt `json:"qq_level"` // QQ 账号等级
	JoinTime int64   `json:"join_time"`
}

// DisplayName is what the operator sees for a member in this group.
func (m GroupMember) DisplayName() string {
	if strings.TrimSpace(m.Card) != "" {
		return m.Card
	}
	return m.Nickname
}

// SignedMember is one entry of a group's sign-in board. The board is today-only
// and paginated, so callers must treat repeated reads as a union.
type SignedMember struct {
	QQ   int64  `json:"user_id"`
	Nick string `json:"nick"`
	Time int64  `json:"time"`
	Rank int    `json:"rank"`
}

// Client calls OneBot actions over an HTTP endpoint.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a Client for an OneBot HTTP endpoint (e.g. http://127.0.0.1:3002).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// obEnvelope is NapCat's OneBot response envelope.
type obEnvelope struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
}

// Call runs one OneBot action and decodes its data into out.
func (c *Client) Call(action string, params map[string]any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/"+action, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: read response: %w", action, err)
	}
	return decodeAction(action, body, out)
}

// decodeAction unwraps an OneBot envelope. A non-zero retcode is an error even
// when the payload is well formed; retcode 0 with an empty list is a valid
// answer, not a failure.
func decodeAction(action string, body []byte, out any) error {
	var env obEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%s: unexpected response (HTTP body): %.200s", action, body)
	}
	if env.Retcode != 0 {
		msg := env.Message
		if msg == "" {
			msg = env.Wording
		}
		return fmt.Errorf("%s: retcode %d: %s", action, env.Retcode, msg)
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s: decode data: %w", action, err)
		}
	}
	return nil
}

// GetGroupList returns the groups the bot account is in. An empty slice is a
// valid result meaning the account has not been invited anywhere.
func (c *Client) GetGroupList() ([]Group, error) {
	var gs []Group
	if err := c.Call("get_group_list", nil, &gs); err != nil {
		return nil, err
	}
	return gs, nil
}

// GetGroupMemberList returns a group's roster.
func (c *Client) GetGroupMemberList(groupID int64) ([]GroupMember, error) {
	var ms []GroupMember
	err := c.Call("get_group_member_list", map[string]any{"group_id": groupID}, &ms)
	if err != nil {
		return nil, err
	}
	return ms, nil
}

// GetGroupSignedList returns today's sign-in board for a group.
func (c *Client) GetGroupSignedList(groupID int64) ([]SignedMember, error) {
	var ss []SignedMember
	err := c.Call("get_group_signed_list", map[string]any{"group_id": groupID}, &ss)
	if err != nil {
		return nil, err
	}
	return ss, nil
}

// LoginInfo identifies the bot account.
type LoginInfo struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
}

// GetLoginInfo returns the bot account's identity, used as a liveness probe.
func (c *Client) GetLoginInfo() (*LoginInfo, error) {
	var li LoginInfo
	if err := c.Call("get_login_info", nil, &li); err != nil {
		return nil, err
	}
	return &li, nil
}

// ---------- WebUI bridge ----------

// OneBotConfig is NapCat's OneBot configuration. Only the network adapter lists
// are modelled; unknown keys are preserved on write through Raw.
type OneBotConfig struct {
	Network map[string]json.RawMessage `json:"network"`
	Raw     map[string]json.RawMessage `json:"-"`
}

// WebUI drives a NapCat instance through its own HTTP API, including the debug
// bridge that forwards OneBot actions to the live adapter.
type WebUI struct {
	baseURL string
	token   string
	http    *http.Client

	mu         sync.RWMutex
	credential string
}

// NewWebUI builds a WebUI client for e.g. http://127.0.0.1:6099.
func NewWebUI(baseURL, token string) *WebUI {
	return &WebUI{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// SetCredential installs a previously obtained credential.
func (w *WebUI) SetCredential(c string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.credential = c
}

// Credential returns the current credential.
func (w *WebUI) Credential() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.credential
}

type webuiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (w *WebUI) do(path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(http.MethodPost, w.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if cred := w.Credential(); cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}

	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: read response: %w", path, err)
	}

	var env webuiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: unexpected response: %.200s", path, raw)
	}
	if env.Code != 0 {
		return fmt.Errorf("%s: %s (code %d)", path, env.Message, env.Code)
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s: decode data: %w", path, err)
		}
	}
	return nil
}

// Login exchanges the WebUI token for a one-hour credential. The token is sent
// as sha256(token + ".napcat"), matching NapCat's own hashing.
//
// The credential is signed with a per-process secret unless
// NAPCAT_WEBUI_JWT_SECRET_KEY is set, so it dies with the NapCat process and
// callers must re-login (and tolerate the login rate limit) after a restart.
func (w *WebUI) Login() error {
	sum := sha256.Sum256([]byte(w.token + ".napcat"))
	var data struct {
		Credential string `json:"Credential"`
		Require2FA bool   `json:"require2FA"`
	}
	err := w.do("/api/auth/login", map[string]string{"hash": hex.EncodeToString(sum[:])}, &data)
	if err != nil {
		return err
	}
	if data.Require2FA {
		return fmt.Errorf("NapCat WebUI requires 2FA, which this service cannot satisfy")
	}
	if data.Credential == "" {
		return fmt.Errorf("login succeeded but returned no credential")
	}
	w.SetCredential(data.Credential)
	return nil
}

// callAction forwards an OneBot action through the WebUI debug bridge, which
// needs no network adapter to be enabled.
func (w *WebUI) callAction(action string, params map[string]any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	var raw json.RawMessage
	if err := w.do("/api/Debug/call", map[string]any{"action": action, "params": params}, &raw); err != nil {
		return err
	}
	return decodeAction(action, raw, out)
}

// GetGroupList via the WebUI bridge.
func (w *WebUI) GetGroupList() ([]Group, error) {
	var gs []Group
	if err := w.callAction("get_group_list", nil, &gs); err != nil {
		return nil, err
	}
	return gs, nil
}

// GetGroupMemberList via the WebUI bridge.
func (w *WebUI) GetGroupMemberList(groupID int64) ([]GroupMember, error) {
	var ms []GroupMember
	if err := w.callAction("get_group_member_list", map[string]any{"group_id": groupID}, &ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// GetGroupSignedList via the WebUI bridge.
func (w *WebUI) GetGroupSignedList(groupID int64) ([]SignedMember, error) {
	var ss []SignedMember
	if err := w.callAction("get_group_signed_list", map[string]any{"group_id": groupID}, &ss); err != nil {
		return nil, err
	}
	return ss, nil
}

// GetOneBotConfig reads NapCat's current OneBot configuration.
func (w *WebUI) GetOneBotConfig() (*OneBotConfig, error) {
	var raw map[string]json.RawMessage
	if err := w.do("/api/OB11Config/GetConfig", map[string]any{}, &raw); err != nil {
		return nil, err
	}
	cfg := &OneBotConfig{Raw: raw, Network: map[string]json.RawMessage{}}
	if n, ok := raw["network"]; ok {
		_ = json.Unmarshal(n, &cfg.Network)
	}
	return cfg, nil
}

// SetOneBotConfig writes the OneBot configuration. NapCat saves it and reloads
// the network adapters in place, so no restart is needed.
func (w *WebUI) SetOneBotConfig(cfg *OneBotConfig) error {
	merged := map[string]json.RawMessage{}
	for k, v := range cfg.Raw {
		merged[k] = v
	}
	netRaw, err := json.Marshal(cfg.Network)
	if err != nil {
		return fmt.Errorf("marshal network: %w", err)
	}
	merged["network"] = netRaw

	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// NapCat expects the config as a JSON *string*, not an object.
	return w.do("/api/OB11Config/SetConfig", map[string]string{"config": string(encoded)}, nil)
}

// EnableWebSocketServer turns on an OneBot WebSocket server adapter, which is
// what delivers push events (and therefore binding commands) to this service.
func (w *WebUI) EnableWebSocketServer(name, host string, port int, token string) error {
	cfg, err := w.GetOneBotConfig()
	if err != nil {
		return err
	}
	adapters := []map[string]any{{
		"name": name, "enable": true, "host": host, "port": port,
		"messagePostFormat": "array", "reportSelfMessage": false,
		"token": token, "enableForcePushEvent": true, "debug": false,
		"heartInterval": 30000,
	}}
	raw, err := json.Marshal(adapters)
	if err != nil {
		return err
	}
	cfg.Network["websocketServers"] = raw
	return w.SetOneBotConfig(cfg)
}

// EnableHTTPServer turns on an OneBot HTTP server adapter for action calls.
func (w *WebUI) EnableHTTPServer(name, host string, port int, token string) error {
	cfg, err := w.GetOneBotConfig()
	if err != nil {
		return err
	}
	adapters := []map[string]any{{
		"name": name, "enable": true, "port": port, "host": host,
		"enableCors": true, "enableWebsocket": false,
		"messagePostFormat": "array", "token": token, "debug": false,
	}}
	raw, err := json.Marshal(adapters)
	if err != nil {
		return err
	}
	cfg.Network["httpServers"] = raw
	return w.SetOneBotConfig(cfg)
}

// ParseWSURL normalises a user-entered WebSocket URL.
func ParseWSURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return "", fmt.Errorf("websocket URL must use ws:// or wss://, got %q", u.Scheme)
	}
	return u.String(), nil
}
