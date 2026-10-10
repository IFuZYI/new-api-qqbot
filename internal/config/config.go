// Package config holds the operator-supplied connection settings for the two
// external systems.
//
// The settings live in two places on purpose. The YAML file under the data
// directory is what an operator reads, hand-edits and provisions from a
// deployment; the database is the running copy the WebUI edits. A change to the
// file wins on the next read, and a WebUI save writes both, so the two stay in
// step and either one is a complete answer to "how is this instance
// configured".
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"qqbot/internal/store"
)

const (
	newAPIKey = "config.newapi"
	napCatKey = "config.napcat"
)

// FileName is the config file's name inside the data directory.
const FileName = "config.yaml"

// NewAPI is the gateway admin account this service drives.
type NewAPI struct {
	URL      string `yaml:"url" json:"url"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

// NapCat describes how to reach the local NapCat instance. Two transports are
// configured independently: the WebUI (used for reading the OneBot config and
// as a fallback action bridge) and the OneBot HTTP endpoint (used for routine
// action calls).
type NapCat struct {
	WebUIURL   string `yaml:"webui_url" json:"webui_url"`
	WebUIToken string `yaml:"webui_token" json:"webui_token"`

	OneBotURL   string `yaml:"onebot_url" json:"onebot_url"`
	OneBotToken string `yaml:"onebot_token" json:"onebot_token"`

	// EventWSURL receives push events (used for the binding command). Optional:
	// without it the service still works, only with slower reaction.
	EventWSURL   string `yaml:"event_ws_url" json:"event_ws_url"`
	EventWSToken string `yaml:"event_ws_token" json:"event_ws_token"`
}

// Config is the full external-systems configuration.
type Config struct {
	NewAPI NewAPI `yaml:"newapi" json:"newapi"`
	NapCat NapCat `yaml:"napcat" json:"napcat"`
}

const fileHeader = `# QQBot 群监测 — 连接配置
#
# 这个文件与 WebUI 的「连接配置」是同一份配置：
#   - 在这里手改并保存，服务会在下一次读取时采用（文件优先）；
#   - 在 WebUI 里保存，会同时写入数据库和本文件。
# 两种方式不会互相覆盖。密码与 token 以明文保存在此，请限制文件权限。

`

// Manager owns the config file and the database mirror.
type Manager struct {
	path string
	st   *store.Store

	mu       sync.Mutex
	lastHash string // content hash of the file as last read or written
}

// NewManager builds a Manager for the config file at path, mirroring into st.
func NewManager(path string, st *store.Store) *Manager {
	return &Manager{path: path, st: st}
}

// Path returns the config file's location.
func (m *Manager) Path() string { return m.path }

// Load returns the current configuration.
//
// When the file changed since it was last read, its contents are adopted into
// the database first, so a hand edit takes effect without a restart. A missing
// file is not an error. A malformed one is reported and the database copy is
// returned unchanged, so a typo cannot wipe a working configuration.
func (m *Manager) Load() (Config, error) {
	if err := m.syncFromFile(); err != nil {
		return m.fromStore(), err
	}
	return m.fromStore(), nil
}

// MustLoad returns the configuration, ignoring a file error. It is for call
// sites where a broken config file should not break the service.
func (m *Manager) MustLoad() Config {
	c, _ := m.Load()
	return c
}

// syncFromFile adopts the file into the database when it changed on disk.
func (m *Manager) syncFromFile() error {
	raw, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	sum := hashBytes(raw)
	m.mu.Lock()
	unchanged := sum == m.lastHash
	m.mu.Unlock()
	if unchanged {
		return nil
	}

	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("parse %s: %w", m.path, err)
	}

	// An empty or comment-only file parses cleanly into the zero Config, which
	// is indistinguishable from "the operator cleared everything". Adopting it
	// would destroy the stored connection settings (an editor that truncates
	// before writing is enough to trigger it), so a file that carries no values
	// at all is ignored. Clearing the configuration is done from the WebUI.
	if c.isZero() {
		m.mu.Lock()
		m.lastHash = sum
		m.mu.Unlock()
		return nil
	}

	// Record the hash before writing, so Save's own file write is not later
	// mistaken for an external edit.
	m.mu.Lock()
	m.lastHash = sum
	m.mu.Unlock()

	return m.saveToStore(c)
}

// isZero reports whether every field is empty, i.e. the file carried no values.
func (c Config) isZero() bool {
	return c.NewAPI.URL == "" && c.NewAPI.Username == "" && c.NewAPI.Password == "" &&
		c.NapCat.WebUIURL == "" && c.NapCat.WebUIToken == "" &&
		c.NapCat.OneBotURL == "" && c.NapCat.OneBotToken == "" &&
		c.NapCat.EventWSURL == "" && c.NapCat.EventWSToken == ""
}

// Revision identifies the current configuration so a writer can detect that it
// changed underneath them. It is a hash of the file's content when the file is
// present, and of the stored copy otherwise.
func (m *Manager) Revision() string {
	if raw, err := os.ReadFile(m.path); err == nil {
		return hashBytes(raw)
	}
	c := m.fromStore()
	raw, err := yaml.Marshal(c)
	if err != nil {
		return ""
	}
	return hashBytes(raw)
}

// fromStore reads the database copy.
func (m *Manager) fromStore() Config {
	var c Config
	_ = m.st.GetSetting(newAPIKey, &c.NewAPI)
	_ = m.st.GetSetting(napCatKey, &c.NapCat)
	return c
}

// Save stores the configuration in both the database and the file.
func (m *Manager) Save(c Config) error {
	if err := m.saveToStore(c); err != nil {
		return err
	}
	return m.writeFile(c)
}

func (m *Manager) saveToStore(c Config) error {
	if err := m.st.PutSetting(newAPIKey, c.NewAPI); err != nil {
		return err
	}
	return m.st.PutSetting(napCatKey, c.NapCat)
}

// EnsureFile creates the config file when it is missing, so an operator has
// something to edit. Existing content is never touched.
func (m *Manager) EnsureFile() error {
	if _, err := os.Stat(m.path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Seed from the database, so an instance configured through the WebUI
	// before the file existed does not come back empty.
	return m.writeFile(m.fromStore())
}

// writeFile writes the config atomically: a crash mid-write cannot leave a
// truncated file that would then fail to parse.
func (m *Manager) writeFile(c Config) error {
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	content := append([]byte(fileHeader), body...)

	m.mu.Lock()
	m.lastHash = hashBytes(content)
	m.mu.Unlock()

	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	// The file holds credentials, so keep it owner-only.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, m.path)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ValidateNewAPI checks the gateway settings.
func (n NewAPI) Validate() error {
	if strings.TrimSpace(n.URL) == "" {
		return errors.New("new-api URL is required")
	}
	if err := validateURL(n.URL, "http", "https"); err != nil {
		return fmt.Errorf("new-api URL: %w", err)
	}
	if strings.TrimSpace(n.Username) == "" {
		return errors.New("new-api username is required")
	}
	if n.Password == "" {
		return errors.New("new-api password is required")
	}
	return nil
}

// Validate checks the NapCat settings. At least one transport must be
// configured, otherwise the service has no way to reach QQ.
func (n NapCat) Validate() error {
	if strings.TrimSpace(n.WebUIURL) == "" && strings.TrimSpace(n.OneBotURL) == "" {
		return errors.New("configure either the NapCat WebUI URL or the OneBot URL")
	}
	if n.WebUIURL != "" {
		if err := validateURL(n.WebUIURL, "http", "https"); err != nil {
			return fmt.Errorf("NapCat WebUI URL: %w", err)
		}
		if n.WebUIToken == "" {
			return errors.New("NapCat WebUI token is required when the WebUI URL is set")
		}
	}
	if n.OneBotURL != "" {
		if err := validateURL(n.OneBotURL, "http", "https"); err != nil {
			return fmt.Errorf("OneBot URL: %w", err)
		}
	}
	if n.EventWSURL != "" {
		if err := validateURL(n.EventWSURL, "ws", "wss"); err != nil {
			return fmt.Errorf("event WebSocket URL: %w", err)
		}
	}
	return nil
}

func validateURL(raw string, schemes ...string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	for _, s := range schemes {
		if u.Scheme == s {
			if u.Host == "" {
				return errors.New("missing host")
			}
			return nil
		}
	}
	return fmt.Errorf("scheme must be one of %s", strings.Join(schemes, ", "))
}

// RedactedPassword is the placeholder the WebUI sends back unchanged when the
// operator did not edit the field.
const RedactedPassword = "********"

// Redacted returns a copy with secrets replaced, for display in the WebUI.
func (c Config) Redacted() Config {
	out := c
	if out.NewAPI.Password != "" {
		out.NewAPI.Password = RedactedPassword
	}
	if out.NapCat.WebUIToken != "" {
		out.NapCat.WebUIToken = RedactedPassword
	}
	if out.NapCat.OneBotToken != "" {
		out.NapCat.OneBotToken = RedactedPassword
	}
	if out.NapCat.EventWSToken != "" {
		out.NapCat.EventWSToken = RedactedPassword
	}
	return out
}

// Merge keeps stored secrets when the incoming value is the redaction
// placeholder, so saving the form without retyping a password does not wipe it.
func (c Config) Merge(stored Config) Config {
	out := c
	if out.NewAPI.Password == RedactedPassword || out.NewAPI.Password == "" {
		out.NewAPI.Password = stored.NewAPI.Password
	}
	if out.NapCat.WebUIToken == RedactedPassword || out.NapCat.WebUIToken == "" {
		out.NapCat.WebUIToken = stored.NapCat.WebUIToken
	}
	if out.NapCat.OneBotToken == RedactedPassword || out.NapCat.OneBotToken == "" {
		out.NapCat.OneBotToken = stored.NapCat.OneBotToken
	}
	if out.NapCat.EventWSToken == RedactedPassword || out.NapCat.EventWSToken == "" {
		out.NapCat.EventWSToken = stored.NapCat.EventWSToken
	}
	return out
}
