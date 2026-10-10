// Package newapi is a thin client for a new-api gateway's admin API. It only
// ever calls HTTP endpoints: the service never touches new-api's database.
package newapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrAmbiguousEmail means more than one account matched an email exactly, so
// binding must not happen automatically.
var ErrAmbiguousEmail = errors.New("more than one new-api account matches this email")

// userAgent identifies this service. The gateway is fronted by Cloudflare,
// which rejects requests with Go's default User-Agent.
const userAgent = "qqbot-group-monitor/0.1 (+self-hosted)"

// User is the subset of a new-api account this service cares about.
type User struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Group       string `json:"group"`
	Role        int    `json:"role"`
	Status      int    `json:"status"`
	Remark      string `json:"remark"`
}

// Client talks to one new-api instance.
type Client struct {
	baseURL string
	http    *http.Client

	mu    sync.RWMutex
	token string
}

// New builds a client for baseURL (e.g. https://api.fuzy.site).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// SetToken installs a previously obtained access token.
func (c *Client) SetToken(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = tok
}

// Token returns the current access token.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// envelope is the standard new-api response wrapper.
type envelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if tok := c.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s %s: unexpected response (HTTP %d): %.200s", method, path, resp.StatusCode, raw)
	}
	if !env.Success {
		msg := env.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("%s %s: %s (HTTP %d)", method, path, msg, resp.StatusCode)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s %s: decode data: %w", method, path, err)
		}
	}
	return nil
}

// Login authenticates the admin account and stores the returned access token.
func (c *Client) Login(username, password string) error {
	var data struct {
		AccessToken string `json:"access_token"`
	}
	err := c.do(http.MethodPost, "/api/user/login",
		map[string]string{"username": username, "password": password}, &data)
	if err != nil {
		return err
	}
	if data.AccessToken == "" {
		return errors.New("login succeeded but returned no access token")
	}
	c.SetToken(data.AccessToken)
	return nil
}

// FindUserByEmail returns the account whose email equals addr, ignoring case.
// It returns (nil, nil) when nothing matches and ErrAmbiguousEmail when more
// than one does. /api/user/search is a LIKE across several columns, so the
// exact-match filter here is what makes the result trustworthy.
func (c *Client) FindUserByEmail(addr string) (*User, error) {
	want := strings.ToLower(strings.TrimSpace(addr))
	if want == "" {
		return nil, nil
	}

	var page struct {
		Items []User `json:"items"`
		Total int    `json:"total"`
	}
	if err := c.do(http.MethodGet, "/api/user/search?keyword="+queryEscape(addr), nil, &page); err != nil {
		return nil, err
	}

	var matches []User
	for _, u := range page.Items {
		if strings.ToLower(strings.TrimSpace(u.Email)) == want {
			matches = append(matches, u)
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrAmbiguousEmail, addr)
	}
}

// SetGroup moves an account into group. new-api's PUT /api/user/ takes the full
// user object and silently discards fields it does not accept, so everything it
// does persist is resent verbatim.
func (c *Client) SetGroup(u *User, group string) error {
	body := map[string]any{
		"id":           u.ID,
		"username":     u.Username,
		"display_name": u.DisplayName,
		"role":         u.Role,
		"group":        group,
		"remark":       u.Remark,
	}
	return c.do(http.MethodPut, "/api/user/", body, nil)
}

// AddQuota grants quota to an account. new-api records the change itself, so
// this service keeps no ledger of its own.
func (c *Client) AddQuota(userID, value int) error {
	if value <= 0 {
		return fmt.Errorf("quota value must be positive, got %d", value)
	}
	return c.do(http.MethodPost, "/api/user/manage", map[string]any{
		"id":     userID,
		"action": "add_quota",
		"mode":   "add",
		"value":  value,
	}, nil)
}

// GetUser loads one account by id.
func (c *Client) GetUser(id int) (*User, error) {
	var u User
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/user/%d", id), nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Groups returns the group names configured on the instance, which is how the
// service validates that its source/target groups really exist.
func (c *Client) Groups() (map[string]struct{}, error) {
	var data map[string]struct {
		Desc  string  `json:"desc"`
		Ratio float64 `json:"ratio"`
	}
	if err := c.do(http.MethodGet, "/api/user/groups", nil, &data); err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(data))
	for name := range data {
		out[name] = struct{}{}
	}
	return out, nil
}

func queryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~', ch == '@':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
