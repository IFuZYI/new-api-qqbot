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
	"encoding/json"
	"log/slog"
	"time"

	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

	// EventWSURL receives push events: the bind command, group joins and
	// message check-ins. Optional: without it the service still works, only
	// with slower reaction.
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
	// reportedHash is the hash of the last MALFORMED file already reported.
	// syncFromFile runs on every load and every save, so without this the same
	// broken file would write one WARN per call and bury the day's log. A file
	// that changes (or is fixed and broken again) is reported afresh.
	reportedHash string
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

// Locked helpers never acquire mu again. All read/merge/write sequences use it.
func (m *Manager) loadLocked() (Config, error) {
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
	unchanged := sum == m.lastHash
	if unchanged {
		return nil
	}

	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		// The file is refused (adopting it would wipe the stored settings), and
		// the refusal is REPORTED: every production caller uses MustLoad, which
		// discards the error, so without this record the operator saw a working
		// panel and a broken file with nothing connecting the two — only a later
		// 保存 surfaced it, as a 500. Reported once per distinct broken content.
		if sum != m.reportedHash {
			m.reportedHash = sum
			slog.Warn("配置文件无法解析，已继续使用数据库中的配置",
				"path", m.path, "err", err)
		}
		return fmt.Errorf("parse %s: %w", m.path, err)
	}

	// An empty or comment-only file parses cleanly into the zero Config, which
	// is indistinguishable from "the operator cleared everything". Adopting it
	// would destroy the stored connection settings (an editor that truncates
	// before writing is enough to trigger it), so a file that carries no values
	// at all is ignored. Clearing the configuration is done from the WebUI.
	if c.isZero() {
		return nil
	}

	// Merge over the stored copy rather than adopting the file wholesale: an
	// operator who adds or fixes a single line must not silently lose the
	// password and the tokens the file does not mention. Only a field the file
	// carries a non-empty value for is adopted, so a hand edit can add or
	// correct a value but cannot blank a stored one.
	merged := c.MergeFields(m.fromStore())
	if err := m.saveToStore(merged); err != nil {
		return err
	}
	// The file parses again, so a LATER breakage must be reported afresh — even
	// of the same bytes. Leaving reportedHash set meant break/fix/break-again
	// stayed silent, the exact gap the record exists to close.
	m.reportedHash = ""
	m.lastHash = sum
	return nil
}

// MergeFields overlays c onto base, but only with the non-empty fields c
// carries. It is the file-side counterpart of Merge, which has a redaction
// placeholder to reason about; here there is none, because a file cannot say
// "clear this": YAML decodes an empty string and an absent key to the same
// zero value, and both leave the stored field alone.
func (c Config) MergeFields(base Config) Config {
	out := base
	if c.NewAPI.URL != "" {
		out.NewAPI.URL = c.NewAPI.URL
	}
	if c.NewAPI.Username != "" {
		out.NewAPI.Username = c.NewAPI.Username
	}
	if c.NewAPI.Password != "" {
		out.NewAPI.Password = c.NewAPI.Password
	}
	if c.NapCat.WebUIURL != "" {
		out.NapCat.WebUIURL = c.NapCat.WebUIURL
	}
	if c.NapCat.WebUIToken != "" {
		out.NapCat.WebUIToken = c.NapCat.WebUIToken
	}
	if c.NapCat.OneBotURL != "" {
		out.NapCat.OneBotURL = c.NapCat.OneBotURL
	}
	if c.NapCat.OneBotToken != "" {
		out.NapCat.OneBotToken = c.NapCat.OneBotToken
	}
	if c.NapCat.EventWSURL != "" {
		out.NapCat.EventWSURL = c.NapCat.EventWSURL
	}
	if c.NapCat.EventWSToken != "" {
		out.NapCat.EventWSToken = c.NapCat.EventWSToken
	}
	return out
}

// isZero reports whether every field is empty, i.e. the file carried no values.
func (c Config) isZero() bool {
	return c.NewAPI.URL == "" && c.NewAPI.Username == "" && c.NewAPI.Password == "" &&
		c.NapCat.WebUIURL == "" && c.NapCat.WebUIToken == "" &&
		c.NapCat.OneBotURL == "" && c.NapCat.OneBotToken == "" &&
		c.NapCat.EventWSURL == "" && c.NapCat.EventWSToken == ""
}

// Revision identifies the effective configuration, not its YAML formatting.
// Hashing the same snapshot returned to a reader keeps stale content from being
// paired with a newer writer's revision.
func (m *Manager) Revision() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revisionLocked()
}

func (m *Manager) revisionLocked() string {
	c, _ := m.loadLocked()
	return c.Revision()
}

// Revision is computed before redaction, so secret changes also invalidate forms.
func (c Config) Revision() string {
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

// Save serializes a full replacement. File errors roll back the SQLite write;
// a commit error attempts to restore the previous file. This is NOT a crash-
// atomic transaction across SQLite and the filesystem.
func (m *Manager) Save(c Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked(c)
}

func (m *Manager) saveLocked(c Config) error {
	before, err := os.ReadFile(m.path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	body, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	content := append([]byte(fileHeader), body...)
	err = m.st.DB().Connection(func(conn *gorm.DB) error {
		renamed := false
		err := conn.Transaction(func(tx *gorm.DB) error {
			if err := saveSettings(tx, c); err != nil {
				return err
			}
			if err := m.writeBytes(content); err != nil {
				return err
			}
			renamed = true
			return nil
		})
		if err == nil || !renamed {
			return err
		}
		// database/sql marks a failed Commit done, but SQLite can still have
		// an open transaction (deferred FK failure). Roll back on the SAME
		// pinned connection before returning it to the pool.
		rollbackErr := conn.Exec("ROLLBACK").Error
		var restoreErr error
		if missing {
			restoreErr = os.Remove(m.path)
		} else {
			restoreErr = m.writeBytes(before)
		}
		if restoreErr != nil {
			// Never cache the rejected file; Load must retry/reconcile it.
			m.lastHash = ""
			return fmt.Errorf("config commit failed: %w; file restoration failed: %v; rollback: %v", err, restoreErr, rollbackErr)
		}
		return fmt.Errorf("config commit failed (previous file restored): %w; rollback: %v", err, rollbackErr)
	})
	if err != nil {
		return err
	}
	m.lastHash = hashBytes(content)
	return nil
}

// SectionNewAPI and SectionNapCat identify the two independently-editable
// halves of the configuration. The WebUI saves them separately so a change to
// one cannot write the other back from a stale form (which is how a hand edit
// used to be lost), and so a broken NapCat setting never blocks saving the
// gateway.
const (
	SectionNewAPI = "newapi"
	SectionNapCat = "napcat"
)

// SectionRevision returns a revision scoped to one section.
//
// A single whole-config revision would be wrong here: saving the gateway would
// change it and make an open NapCat form look stale (and vice versa), producing
// spurious conflicts. Hashing just the section means each form is only told
// "no" when its own half actually moved.
func (m *Manager) SectionRevision(section string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, _ := m.loadLocked()
	return sectionRevision(c, section)
}

// SectionRevision identifies one section of this exact snapshot.
func (c Config) SectionRevision(section string) string {
	return sectionRevision(c, section)
}

func sectionRevision(c Config, section string) string {
	var raw []byte
	var err error
	switch section {
	case SectionNewAPI:
		raw, err = yaml.Marshal(c.NewAPI)
	case SectionNapCat:
		raw, err = yaml.Marshal(c.NapCat)
	default:
		return ""
	}
	if err != nil {
		return ""
	}
	return hashBytes(raw)
}

// SaveSection replaces one section, leaving the other untouched.
//
// It re-reads the effective configuration first, so a hand edit to the other
// section (or to this one, caught earlier by the revision guard) is preserved
// rather than overwritten with the form's stale copy. The redaction placeholder
// keeps the stored secret when the operator did not retype it.
func (m *Manager) SaveSection(section string, in Config) error {
	_, err := m.SaveSectionCAS(section, in, "")
	return err
}

// ErrConflict means the caller must reload before attempting another write.
var ErrConflict = errors.New("配置已被修改，请重新载入后再保存。")

// ValidationError distinguishes bad form input from persistence failures.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// SaveCAS checks the whole revision and merges redacted secrets under one lock.
// An empty revision retains compatibility with unconditional saves.
func (m *Manager) SaveCAS(in Config, revision string) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := m.loadLocked()
	if err != nil {
		return Config{}, err
	}
	if revision != "" && revision != cur.Revision() {
		return Config{}, ErrConflict
	}
	in = in.Merge(cur)
	if err := in.NewAPI.Validate(); err != nil {
		return Config{}, &ValidationError{err}
	}
	if err := in.NapCat.Validate(); err != nil {
		return Config{}, &ValidationError{err}
	}
	return in, m.saveLocked(in)
}

// SaveSectionCAS compares, merges, validates and saves without an unlocked gap.
func (m *Manager) SaveSectionCAS(section string, in Config, revision string) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := m.loadLocked()
	if err != nil {
		return Config{}, err
	}
	if revision != "" && revision != sectionRevision(cur, section) {
		return Config{}, ErrConflict
	}
	return m.saveSectionLocked(section, in, cur)
}

// UpdateSectionCAS holds the Manager lock through a revision-guarded external
// operation and persistence. The callback receives current secrets and MUST NOT
// call Manager methods. A stale waiter never enters the callback. External side
// effects cannot be rolled back if local persistence subsequently fails.
func (m *Manager) UpdateSectionCAS(section, revision string, update func(Config) (Config, error)) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := m.loadLocked()
	if err != nil {
		return Config{}, err
	}
	if section != SectionNewAPI && section != SectionNapCat {
		return Config{}, &ValidationError{fmt.Errorf("unknown config section %q", section)}
	}
	if revision != "" && revision != sectionRevision(cur, section) {
		return Config{}, ErrConflict
	}
	in, err := update(cur)
	if err != nil {
		return Config{}, err
	}
	return m.saveSectionLocked(section, in, cur)
}

func (m *Manager) saveSectionLocked(section string, in, cur Config) (Config, error) {
	in = in.Merge(cur)
	switch section {
	case SectionNewAPI:
		if err := in.NewAPI.Validate(); err != nil {
			return Config{}, &ValidationError{err}
		}
		cur.NewAPI = in.NewAPI
	case SectionNapCat:
		if err := in.NapCat.Validate(); err != nil {
			return Config{}, &ValidationError{err}
		}
		cur.NapCat = in.NapCat
	default:
		return Config{}, &ValidationError{fmt.Errorf("unknown config section %q", section)}
	}
	return cur, m.saveLocked(cur)
}

func (m *Manager) saveToStore(c Config) error {
	return m.st.DB().Transaction(func(tx *gorm.DB) error { return saveSettings(tx, c) })
}

func saveSettings(tx *gorm.DB, c Config) error {
	for _, entry := range []struct {
		key   string
		value any
	}{{newAPIKey, c.NewAPI}, {napCatKey, c.NapCat}} {
		raw, err := json.Marshal(entry.value)
		if err != nil {
			return err
		}
		row := store.Setting{Key: entry.key, Value: string(raw), UpdatedAt: time.Now().Unix()}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"})}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// EnsureFile creates the config file when it is missing, so an operator has
// something to edit. Existing content is never touched.
func (m *Manager) EnsureFile() error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	body, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	content := append([]byte(fileHeader), body...)
	if err := m.writeBytes(content); err != nil {
		return err
	}
	m.lastHash = hashBytes(content)
	return nil
}

func (m *Manager) writeBytes(content []byte) error {
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
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
	if err := os.Rename(tmpName, m.path); err != nil {
		return err
	}
	return nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Validate checks the gateway settings. The URL must parse as http or https,
// and URL, username and password must all be present, so an incomplete form is
// refused when it is saved rather than failing later on the first gateway call.
//
// The messages are operator-facing (the WebUI shows them in a toast), so they
// are Simplified Chinese like the rest of the panel's text.
func (n NewAPI) Validate() error {
	if strings.TrimSpace(n.URL) == "" {
		return errors.New("请填写 new-api 地址")
	}
	if err := validateURL(n.URL, "http", "https"); err != nil {
		return fmt.Errorf("new-api 地址：%w", err)
	}
	if strings.TrimSpace(n.Username) == "" {
		return errors.New("请填写 new-api 用户名")
	}
	if n.Password == "" {
		return errors.New("请填写 new-api 密码")
	}
	return nil
}

// Validate checks the NapCat settings. At least one transport must be
// configured, otherwise the service has no way to reach QQ.
func (n NapCat) Validate() error {
	if strings.TrimSpace(n.WebUIURL) == "" && strings.TrimSpace(n.OneBotURL) == "" {
		return errors.New("请填写 NapCat 的 WebUI 地址或 OneBot 地址（二选一）")
	}
	if n.WebUIURL != "" {
		if err := validateURL(n.WebUIURL, "http", "https"); err != nil {
			return fmt.Errorf("NapCat WebUI 地址：%w", err)
		}
		if n.WebUIToken == "" {
			return errors.New("填了 WebUI 地址时必须同时填写 WebUI Token")
		}
	}
	if n.OneBotURL != "" {
		if err := validateURL(n.OneBotURL, "http", "https"); err != nil {
			return fmt.Errorf("OneBot 地址：%w", err)
		}
	}
	if n.EventWSURL != "" {
		if err := validateURL(n.EventWSURL, "ws", "wss"); err != nil {
			return fmt.Errorf("事件 WebSocket 地址：%w", err)
		}
	}
	return nil
}

// validateURL checks that raw parses and uses one of schemes with a non-empty
// host. Its messages are wrapped by the callers above and reach the operator in
// a toast, so they are Simplified Chinese too.
func validateURL(raw string, schemes ...string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	for _, s := range schemes {
		if u.Scheme == s {
			if u.Host == "" {
				return errors.New("缺少主机名")
			}
			return nil
		}
	}
	return fmt.Errorf("协议必须是 %s 之一", strings.Join(schemes, "、"))
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

// Merge keeps the stored secret when the incoming value is the redaction
// placeholder or blank, so saving the form without retyping a password does
// not wipe it: the WebUI form omits a secret it is not resending, and the two
// cases are indistinguishable from the value alone.
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
