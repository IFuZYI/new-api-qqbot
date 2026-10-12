// Package napcat talks to a local NapCat instance. Two transports are
// supported and both are needed: the OneBot network adapter (HTTP or
// WebSocket) for routine action calls and event push, and the WebUI's debug
// bridge, which can drive OneBot actions and rewrite the OneBot config without
// restarting NapCat.
package napcat

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
//
// Rank is fractional by construction (see flexFloat); it is display-only and
// must not be treated as a stable identifier.
type SignedMember struct {
	QQ   int64     `json:"user_id"`
	Nick string    `json:"nick"`
	Time int64     `json:"time"`
	Rank flexFloat `json:"rank"`
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
		http:    &http.Client{Timeout: 30 * time.Second, CheckRedirect: sameOriginRedirect},
	}
}

// maxResponseBody bounds how much of an upstream response is read into memory.
//
// A NapCat answer is a few tens of kilobytes, so 8 MiB is far above any real
// payload while still bounding a runaway one: a misdirected URL or a hijacked
// response (a proxy's error page, the SPA's HTML, an endless stream) would
// otherwise be buffered whole by io.ReadAll, which grows until the process dies.
const maxResponseBody = 8 << 20

// ErrResponseTooLarge means the upstream sent more than maxResponseBody. The
// read is abandoned at the limit rather than truncated, so a caller gets a
// named cause instead of a decode failure on half a JSON document.
var ErrResponseTooLarge = errors.New("response body is too large")

// readBody reads a response body up to maxResponseBody and refuses anything
// larger.
//
// The limit is enforced by reading one byte past it: io.LimitReader reports EOF
// at the cap either way, so a body cut off mid-JSON would surface as an
// unexplained decode error. The extra byte makes "the body ended" and "the cap
// was hit" distinguishable.
func readBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBody {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrResponseTooLarge, maxResponseBody)
	}
	return body, nil
}

// Failover runs primary and, only when it fails, runs fallback; both must do
// the same thing, and the first success is returned.
//
// A QQ client can be built and still be useless. Building one only composes a
// URL, so the failure this service actually meets is a *call* failure: NapCat's
// QQ login expires ("登录态已失效，请重新登录", docs/implementation.md), and
// every QQ-side read then fails while the OneBot port is still listening and
// answering — an error envelope, not a refused connection. A caller that falls
// back only when the client cannot be *built* never reaches the WebUI bridge in
// that state, even though the bridge is exactly what still works: NapCat starts
// the WebUI before login so the operator can drive the login through it
// ("提前启动 WebUI，使其在登录前可用", reference/NapCatQQ napcat.ts), and the
// debug bridge drives the same actions without any network adapter being
// enabled. Falling back on the error is what keeps both paths usable.
//
// When both sides fail, the returned error carries both causes (errors.Join):
// an operator has to learn that the OneBot path and the WebUI path are both
// broken, not just the one that happened to be tried last. The join keeps
// errors.Is working for sentinels, so a caller can still recognise a condition
// that is normal rather than a failure (napcat.ErrNoSigninBoard). The value is
// unspecified when an error is returned.
func Failover[T any](primary, fallback func() (T, error)) (T, error) {
	v, err := primary()
	if err == nil {
		return v, nil
	}
	primaryErr := err
	if v, err = fallback(); err == nil {
		return v, nil
	}
	var zero T
	return zero, errors.Join(primaryErr, err)
}

// obEnvelope is NapCat's OneBot response envelope.
type obEnvelope struct {
	Status  string          `json:"status"`
	Retcode *int            `json:"retcode"`
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", action, resp.StatusCode)
	}

	body, err := readBody(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return decodeAction(action, body, out)
}

// ErrNoSigninBoard means the group simply has no sign-in board. That is a
// normal condition (most groups do not have one), not a failure, so callers
// must not treat it as an error to report.
var ErrNoSigninBoard = errors.New("group has no sign-in board")

// noSigninBoardMarkers are the upstream messages NapCat raises for a group
// without a board. GetGroupSignedList throws when the response carries no page.
var noSigninBoardMarkers = []string{
	"无法获取该群组打卡列表",
	"no sign-in board",
}

// decodeAction unwraps an OneBot envelope. A non-zero retcode is an error even
// when the payload is well formed; retcode 0 with an empty list is a valid
// answer, not a failure.
func decodeAction(action string, body []byte, out any) error {
	var env obEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%s: invalid OneBot response", action)
	}
	if env.Retcode == nil || env.Status == "" {
		return fmt.Errorf("%s: incomplete OneBot envelope", action)
	}
	if *env.Retcode != 0 || env.Status != "ok" {
		msg := env.Message
		if msg == "" {
			msg = env.Wording
		}
		for _, marker := range noSigninBoardMarkers {
			if strings.Contains(msg, marker) {
				return fmt.Errorf("%s: %w", action, ErrNoSigninBoard)
			}
		}
		return fmt.Errorf("%s: retcode %d: %s", action, *env.Retcode, msg)
	}
	if len(env.Data) == 0 || (out != nil && string(env.Data) == "null") {
		return fmt.Errorf("%s: missing OneBot data", action)
	}
	if out != nil {
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
//
// The address is normalised to its origin first. Operators paste the address
// they see in the browser - which is the panel at .../webui/ - but NapCat's API
// lives at the root (/api/...), so a path is stripped rather than carried into
// every request. Without this, every call would hit .../webui/api/... and get
// the SPA's HTML back instead of JSON.
func NewWebUI(baseURL, token string) *WebUI {
	return &WebUI{
		baseURL: NormalizeWebUIURL(baseURL),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second, CheckRedirect: sameOriginRedirect},
	}
}

// NormalizeWebUIURL reduces a NapCat WebUI address to its origin
// (scheme://host[:port]). A value without a scheme gets http://, since the
// panel is normally plain HTTP on the loopback interface.
func NormalizeWebUIURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	return u.Scheme + "://" + u.Host
}

// WebUIHost returns the host to use when reaching NapCat's OneBot adapters,
// derived from the WebUI address the operator gave.
//
// "localhost" is deliberately mapped to 127.0.0.1. The adapters bind on IPv4
// loopback, but localhost commonly resolves to ::1 first (this host does), and
// a listener on 127.0.0.1 is not reachable through ::1 - so a URL built from
// localhost fails with "connection refused" even though everything is running.
// 127.0.0.1 is unambiguous.
func WebUIHost(raw string) string {
	u, err := url.Parse(NormalizeWebUIURL(raw))
	if err != nil || u.Hostname() == "" {
		return "127.0.0.1"
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return "127.0.0.1"
	}
	return host
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
	Code    *int            `json:"code"`
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyWebUIError(&httpStatusError{status: resp.StatusCode, path: path})
	}
	raw, err := readBody(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	var env webuiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: invalid WebUI response", path)
	}
	if env.Code == nil {
		return fmt.Errorf("%s: missing WebUI code", path)
	}
	if *env.Code != 0 {
		return classifyWebUIError(fmt.Errorf("%s: %s (code %d)", path, env.Message, *env.Code))
	}
	if out != nil {
		if len(env.Data) == 0 || bytes.Equal(bytes.TrimSpace(env.Data), []byte("null")) {
			return fmt.Errorf("%s: missing WebUI data", path)
		}
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

// GetLoginInfo via the WebUI bridge.
func (w *WebUI) GetLoginInfo() (*LoginInfo, error) {
	var li LoginInfo
	if err := w.callAction("get_login_info", nil, &li); err != nil {
		return nil, err
	}
	return &li, nil
}

// GetOneBotConfig reads NapCat's current OneBot configuration.
func (w *WebUI) GetOneBotConfig() (*OneBotConfig, error) {
	var raw map[string]json.RawMessage
	if err := w.do("/api/OB11Config/GetConfig", map[string]any{}, &raw); err != nil {
		return nil, err
	}
	cfg := &OneBotConfig{Raw: raw, Network: map[string]json.RawMessage{}}
	n, ok := raw["network"]
	if !ok || json.Unmarshal(n, &cfg.Network) != nil || cfg.Network == nil {
		return nil, fmt.Errorf("invalid OneBot network: expected object")
	}
	if err := validateNetwork(cfg.Network); err != nil {
		return nil, err
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
	adapters, err := decodeAdapters(cfg.Network["websocketServers"])
	if err != nil {
		return err
	}
	adapters = upsertAdapter(adapters, name, map[string]any{
		"name": name, "enable": true, "host": host, "port": port,
		"messagePostFormat": "array", "reportSelfMessage": false,
		"token": token, "enableForcePushEvent": true, "debug": false,
		"heartInterval": 30000,
	})
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
	adapters, err := decodeAdapters(cfg.Network["httpServers"])
	if err != nil {
		return err
	}
	adapters = upsertAdapter(adapters, name, map[string]any{
		"name": name, "enable": true, "port": port, "host": host,
		"enableCors": true, "enableWebsocket": false,
		"messagePostFormat": "array", "token": token, "debug": false,
	})
	raw, err := json.Marshal(adapters)
	if err != nil {
		return err
	}
	cfg.Network["httpServers"] = raw
	return w.SetOneBotConfig(cfg)
}

// Adapter names this service owns. They are stable so a re-run replaces its own
// adapters in place instead of piling up duplicates, and so an unrelated
// adapter the operator configured is left alone.
const (
	HTTPAdapterName = "qqbot-http"
	WSAdapterName   = "qqbot-ws"
)

// Default adapter ports. They match the ones the deployment notes record.
const (
	DefaultHTTPPort = 3002
	DefaultWSPort   = 3001
)

// AdapterOptions configures EnsureAdapters.
type AdapterOptions struct {
	// Token authenticates the adapters. When empty, the token already on this
	// service's adapters is reused (so a re-run does not rotate the secret out
	// from under a live client); when there is none either, a fresh one is
	// generated.
	Token string
	// ConnectHost is the host this service uses to reach NapCat, taken from the
	// WebUI URL the operator pasted (e.g. "localhost"). It only shapes the URLs
	// returned; the adapters themselves bind on BindHost.
	ConnectHost string
	// BindHost is the interface the adapters listen on. Defaults to 127.0.0.1:
	// the OneBot adapters must not be exposed beyond the host.
	BindHost string
	HTTPPort int
	WSPort   int
}

// AdapterResult is what the caller stores as its own NapCat connection settings.
type AdapterResult struct {
	Token      string `json:"token"`
	OneBotURL  string `json:"onebot_url"`
	EventWSURL string `json:"event_ws_url"`
}

// EnsureAdapters turns on the OneBot HTTP and WebSocket server adapters this
// service needs, in one hot write, and reports the URLs and token to store.
//
// This is what makes "give me the WebUI URL and I'll set up the rest" true:
// the operator supplies only the WebUI address and its token, and NapCat's
// OneBot config is written for them (no manual editing, no restart - NapCat
// saves and reloads the network adapters in place). Adapters this service does
// not own are preserved, and the existing token is reused on a re-run so a
// running client is not locked out.
func (w *WebUI) EnsureAdapters(opts AdapterOptions) (*AdapterResult, error) {
	if opts.BindHost == "" {
		opts.BindHost = "127.0.0.1"
		// Keep the default private, but match an IPv6 connection address:
		// an IPv4-only listener cannot be reached through ::1.
		if ip := net.ParseIP(strings.Trim(opts.ConnectHost, "[]")); ip != nil && ip.To4() == nil {
			opts.BindHost = "::1"
		}
	}
	if opts.ConnectHost == "" {
		// The bind host is not always usable as a connect host: 0.0.0.0 means
		// "every interface" and cannot be dialled. Fall back to loopback, which
		// is what a same-host deployment (the default) wants.
		if isWildcardHost(opts.BindHost) {
			opts.ConnectHost = "127.0.0.1"
			if opts.BindHost == "::" || opts.BindHost == "[::]" {
				opts.ConnectHost = "::1"
			}
		} else {
			opts.ConnectHost = opts.BindHost
		}
	}
	if opts.HTTPPort == 0 {
		opts.HTTPPort = DefaultHTTPPort
	}
	if opts.WSPort == 0 {
		opts.WSPort = DefaultWSPort
	}

	cfg, err := w.GetOneBotConfig()
	if err != nil {
		return nil, err
	}

	token := strings.TrimSpace(opts.Token)
	if token == "" {
		token = adapterToken(cfg, "httpServers", HTTPAdapterName)
	}
	if token == "" {
		token = adapterToken(cfg, "websocketServers", WSAdapterName)
	}
	if token == "" {
		token, err = generateAdapterToken()
		if err != nil {
			return nil, err
		}
	}

	httpAdapters, err := decodeAdapters(cfg.Network["httpServers"])
	if err != nil {
		return nil, err
	}
	httpAdapters = upsertAdapter(httpAdapters, HTTPAdapterName, map[string]any{
		"name": HTTPAdapterName, "enable": true, "port": opts.HTTPPort, "host": opts.BindHost,
		"enableCors": true, "enableWebsocket": false,
		"messagePostFormat": "array", "token": token, "debug": false,
	})
	if cfg.Network == nil {
		cfg.Network = map[string]json.RawMessage{}
	}
	cfg.Network["httpServers"] = mustJSON(httpAdapters)

	wsAdapters, err := decodeAdapters(cfg.Network["websocketServers"])
	if err != nil {
		return nil, err
	}
	wsAdapters = upsertAdapter(wsAdapters, WSAdapterName, map[string]any{
		"name": WSAdapterName, "enable": true, "host": opts.BindHost, "port": opts.WSPort,
		"messagePostFormat": "array", "reportSelfMessage": false,
		"token": token, "enableForcePushEvent": true, "debug": false,
		"heartInterval": 30000,
	})
	cfg.Network["websocketServers"] = mustJSON(wsAdapters)

	if err := w.SetOneBotConfig(cfg); err != nil {
		return nil, err
	}
	return &AdapterResult{
		Token:      token,
		OneBotURL:  "http://" + net.JoinHostPort(opts.ConnectHost, strconv.Itoa(opts.HTTPPort)),
		EventWSURL: "ws://" + net.JoinHostPort(opts.ConnectHost, strconv.Itoa(opts.WSPort)),
	}, nil
}

// decodeAdapters reads one adapter list. A missing or empty list is not an
// error: the group simply has no adapters of that kind yet.
func decodeAdapters(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber() // Preserve unknown integers beyond float64 precision.
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode adapters: %w", err)
	}
	if out == nil {
		return nil, fmt.Errorf("decode adapters: expected array, not null")
	}
	for _, adapter := range out {
		if adapter == nil {
			return nil, fmt.Errorf("decode adapters: expected object, not null")
		}
	}
	return out, nil
}

// upsertAdapter replaces the adapter with this name (keeping its position) or
// appends it, so a re-run is idempotent and unrelated adapters survive.
func upsertAdapter(list []map[string]any, name string, want map[string]any) []map[string]any {
	for i, a := range list {
		if s, _ := a["name"].(string); s == name {
			for key, value := range want {
				a[key] = value
			}
			list[i] = a
			return list
		}
	}
	return append(list, want)
}

// adapterToken returns the token on the named adapter, or "" when there is none.
func adapterToken(cfg *OneBotConfig, list, name string) string {
	adapters, err := decodeAdapters(cfg.Network[list])
	if err != nil {
		return ""
	}
	for _, a := range adapters {
		if s, _ := a["name"].(string); s == name {
			if t, _ := a["token"].(string); t != "" {
				return t
			}
		}
	}
	return ""
}

// mustJSON marshals a value that is known to be JSON-encodable (maps of strings
// and numbers), returning a null literal rather than panicking if it is not.
func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

// isWildcardHost reports whether host means "every interface" rather than a
// dialable address. 0.0.0.0 and :: are bind-only values; connecting to them is
// meaningless, so callers must not reuse them as a destination.
func isWildcardHost(host string) bool {
	switch strings.TrimSpace(host) {
	case "0.0.0.0", "::", "[::]", "":
		return true
	}
	return false
}

// generateAdapterToken returns a fresh 32-hex-character token, matching the
// shape NapCat itself generates.
func generateAdapterToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate adapter token: %w", err)
	}
	return hex.EncodeToString(b), nil
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
