// Package store owns the project's SQLite database: schema, migrations and the
// queries the rest of the service uses. Everything the service knows about QQ
// members, bindings and check-ins lives here; new-api is never written to.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MonitoredGroup is a QQ group selected for monitoring.
type MonitoredGroup struct {
	GroupID     int64  `gorm:"primaryKey;autoIncrement:false" json:"group_id"`
	GroupName   string `json:"group_name"`
	MemberCount int    `json:"member_count"`
	Enabled     bool   `gorm:"default:true" json:"enabled"`
	UpdatedAt   int64  `json:"updated_at"`
}

// Member is a QQ user seen in a monitored group, keyed by QQ number.
type Member struct {
	QQ        int64  `gorm:"primaryKey;autoIncrement:false" json:"qq"`
	Nickname  string `json:"nickname"`
	FirstSeen int64  `json:"first_seen"`
	UpdatedAt int64  `json:"updated_at"`

	// GroupIDs is populated on read, not stored on this row.
	GroupIDs []int64 `gorm:"-" json:"group_ids,omitempty"`
}

// MemberGroup records that a member belongs to a monitored group. It is kept
// separate from Member so a member who leaves one group survives the sync.
type MemberGroup struct {
	QQ      int64 `gorm:"primaryKey;autoIncrement:false" json:"qq"`
	GroupID int64 `gorm:"primaryKey;autoIncrement:false" json:"group_id"`
	Joined  int64 `json:"joined"`
}

// Binding maps a QQ number to a new-api account. This project owns the mapping;
// new-api's own email field is only ever read, as a matching clue.
type Binding struct {
	QQ             int64  `gorm:"primaryKey;autoIncrement:false" json:"qq"`
	NewAPIUserID   int    `json:"newapi_user_id"`
	NewAPIUsername string `json:"newapi_username"`
	NewAPIEmail    string `json:"newapi_email"`
	Source         string `json:"source"` // auto | manual | command
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// Checkin is one member's sign-in on one day, as read from the group's
// sign-in board. (QQ, Date) is the identity: re-polling the same day is a no-op.
type Checkin struct {
	QQ          int64  `gorm:"primaryKey;autoIncrement:false" json:"qq"`
	Date        string `gorm:"primaryKey;autoIncrement:false;type:varchar(10)" json:"date"`
	GroupID     int64  `json:"group_id"`
	SignedAt    int64  `json:"signed_at"`
	Rank        int    `json:"rank"`
	RewardQuota int    `json:"reward_quota"`
	RewardedAt  int64  `json:"rewarded_at"`
}

// Setting is a key/value configuration row; Value holds JSON.
type Setting struct {
	Key       string `gorm:"primaryKey" json:"key"`
	Value     string `json:"value"`
	UpdatedAt int64  `json:"updated_at"`
}

// Admin is a WebUI login for this service. It is deliberately not tied to
// new-api: the WebUI must work even when the gateway is unreachable.
type Admin struct {
	ID           int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Username     string `gorm:"uniqueIndex" json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
}

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = gorm.ErrRecordNotFound

// Store is the database handle.
type Store struct{ db *gorm.DB }

// Open opens (creating if needed) the SQLite database at path and migrates it.
func Open(path string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := db.AutoMigrate(
		&MonitoredGroup{}, &Member{}, &MemberGroup{},
		&Binding{}, &Checkin{}, &Setting{}, &Admin{},
	); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// DB exposes the underlying handle for tests and ad-hoc queries.
func (s *Store) DB() *gorm.DB { return s.db }

// Close releases the database.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func now() int64 { return time.Now().Unix() }

// ---------- groups ----------

// UpsertGroups stores the monitored-group selection, preserving Enabled for
// groups that are already monitored.
func (s *Store) UpsertGroups(gs []MonitoredGroup) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, g := range gs {
			var existing MonitoredGroup
			err := tx.First(&existing, "group_id = ?", g.GroupID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				g.UpdatedAt = now()
				if err := tx.Create(&g).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			existing.GroupName = g.GroupName
			existing.MemberCount = g.MemberCount
			existing.UpdatedAt = now()
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListGroups returns monitored groups ordered by id.
func (s *Store) ListGroups() ([]MonitoredGroup, error) {
	var out []MonitoredGroup
	err := s.db.Order("group_id").Find(&out).Error
	return out, err
}

// EnabledGroups returns only the groups selected for monitoring.
func (s *Store) EnabledGroups() ([]MonitoredGroup, error) {
	var out []MonitoredGroup
	err := s.db.Where("enabled = ?", true).Order("group_id").Find(&out).Error
	return out, err
}

// ---------- members ----------

// UpsertMembers reconciles one group's roster: every member is created or
// updated, and memberships that are no longer present are removed from that
// group. Member rows are never deleted, so history (bindings, check-ins) survives.
func (s *Store) UpsertMembers(groupID int64, ms []Member) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		seen := make([]int64, 0, len(ms))
		for _, m := range ms {
			seen = append(seen, m.QQ)

			var existing Member
			err := tx.First(&existing, "qq = ?", m.QQ).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				rec := Member{QQ: m.QQ, Nickname: m.Nickname, FirstSeen: now(), UpdatedAt: now()}
				if err := tx.Create(&rec).Error; err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				existing.Nickname = m.Nickname
				existing.UpdatedAt = now()
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
			}

			var mg MemberGroup
			err = tx.First(&mg, "qq = ? AND group_id = ?", m.QQ, groupID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(&MemberGroup{QQ: m.QQ, GroupID: groupID, Joined: now()}).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}

		// Drop memberships this group no longer reports.
		q := tx.Where("group_id = ?", groupID)
		if len(seen) > 0 {
			q = q.Where("qq NOT IN ?", seen)
		}
		return q.Delete(&MemberGroup{}).Error
	})
}

// GetMember loads one member by QQ.
func (s *Store) GetMember(qq int64) (*Member, error) {
	var m Member
	if err := s.db.First(&m, "qq = ?", qq).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// ListMembers returns every known member with their group memberships attached.
func (s *Store) ListMembers() ([]Member, error) {
	var ms []Member
	if err := s.db.Order("qq").Find(&ms).Error; err != nil {
		return nil, err
	}
	return s.attachGroups(ms)
}

// MembersInGroup returns the members currently in one group.
func (s *Store) MembersInGroup(groupID int64) ([]Member, error) {
	var ms []Member
	err := s.db.
		Joins("JOIN member_groups ON member_groups.qq = members.qq").
		Where("member_groups.group_id = ?", groupID).
		Order("members.qq").Find(&ms).Error
	if err != nil {
		return nil, err
	}
	return s.attachGroups(ms)
}

func (s *Store) attachGroups(ms []Member) ([]Member, error) {
	if len(ms) == 0 {
		return ms, nil
	}
	var links []MemberGroup
	if err := s.db.Find(&links).Error; err != nil {
		return nil, err
	}
	byQQ := make(map[int64][]int64, len(links))
	for _, l := range links {
		byQQ[l.QQ] = append(byQQ[l.QQ], l.GroupID)
	}
	for i := range ms {
		ms[i].GroupIDs = byQQ[ms[i].QQ]
	}
	return ms, nil
}

// ---------- bindings ----------

// SetBinding creates or replaces the binding for a QQ number (换绑).
func (s *Store) SetBinding(b Binding) error {
	var existing Binding
	err := s.db.First(&existing, "qq = ?", b.QQ).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		b.CreatedAt = now()
		b.UpdatedAt = b.CreatedAt
		return s.db.Create(&b).Error
	}
	if err != nil {
		return err
	}
	existing.NewAPIUserID = b.NewAPIUserID
	existing.NewAPIUsername = b.NewAPIUsername
	existing.NewAPIEmail = b.NewAPIEmail
	existing.Source = b.Source
	existing.UpdatedAt = now()
	return s.db.Save(&existing).Error
}

// GetBinding loads one binding by QQ.
func (s *Store) GetBinding(qq int64) (*Binding, error) {
	var b Binding
	if err := s.db.First(&b, "qq = ?", qq).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

// DeleteBinding removes a binding.
func (s *Store) DeleteBinding(qq int64) error {
	return s.db.Delete(&Binding{}, "qq = ?", qq).Error
}

// ListBindings returns all bindings ordered by QQ.
func (s *Store) ListBindings() ([]Binding, error) {
	var out []Binding
	err := s.db.Order("qq").Find(&out).Error
	return out, err
}

// CountBindings counts bindings.
func (s *Store) CountBindings() (int64, error) {
	var n int64
	err := s.db.Model(&Binding{}).Count(&n).Error
	return n, err
}

// UnboundMembers returns members with no binding yet, which the auto-refresh
// pass retries and the UI shows as "pending".
func (s *Store) UnboundMembers() ([]Member, error) {
	var ms []Member
	err := s.db.
		Joins("LEFT JOIN bindings ON bindings.qq = members.qq").
		Where("bindings.qq IS NULL").
		Order("members.qq").Find(&ms).Error
	if err != nil {
		return nil, err
	}
	return s.attachGroups(ms)
}

// ---------- check-ins ----------

// RecordCheckin stores a sign-in seen on the board. Re-recording the same
// (qq, date) updates the observed rank/time but never touches RewardQuota or
// RewardedAt, so a repeated poll cannot re-randomise or re-award.
func (s *Store) RecordCheckin(c Checkin) error {
	var existing Checkin
	err := s.db.First(&existing, "qq = ? AND date = ?", c.QQ, c.Date).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(&c).Error
	}
	if err != nil {
		return err
	}
	existing.SignedAt = c.SignedAt
	existing.Rank = c.Rank
	existing.GroupID = c.GroupID
	return s.db.Save(&existing).Error
}

// GetCheckin loads one member's sign-in for a date.
func (s *Store) GetCheckin(qq int64, date string) (*Checkin, error) {
	var c Checkin
	if err := s.db.First(&c, "qq = ? AND date = ?", qq, date).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// CountCheckins returns how many distinct days a member has signed in on.
func (s *Store) CountCheckins(qq int64) (int, error) {
	var n int64
	err := s.db.Model(&Checkin{}).Where("qq = ?", qq).Count(&n).Error
	return int(n), err
}

// MarkRewarded claims the one-time reward for a (qq, date), returning false if
// it was already claimed. This is the guard that makes awarding idempotent.
func (s *Store) MarkRewarded(qq int64, date string, at time.Time) (bool, error) {
	res := s.db.Model(&Checkin{}).
		Where("qq = ? AND date = ? AND rewarded_at = 0", qq, date).
		Update("rewarded_at", at.Unix())
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// SetRewardAmount records the amount chosen for a (qq, date) without claiming
// it. Storing the value before the gateway call keeps it stable across a crash.
func (s *Store) SetRewardAmount(qq int64, date string, amount int) error {
	return s.db.Model(&Checkin{}).
		Where("qq = ? AND date = ?", qq, date).
		Update("reward_quota", amount).Error
}

// ReleaseReward clears the claim on a (qq, date) so a failed award is retried.
// It also resets the recorded amount, so the retry draws afresh.
func (s *Store) ReleaseReward(qq int64, date string) error {
	return s.db.Model(&Checkin{}).
		Where("qq = ? AND date = ?", qq, date).
		Updates(map[string]any{"rewarded_at": 0, "reward_quota": 0}).Error
}

// CheckinsOn returns every check-in recorded for a date.
func (s *Store) CheckinsOn(date string) ([]Checkin, error) {
	var out []Checkin
	err := s.db.Where("date = ?", date).Order("qq").Find(&out).Error
	return out, err
}

// ---------- settings ----------

// PutSetting stores a JSON-encoded setting.
func (s *Store) PutSetting(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal setting %s: %w", key, err)
	}
	var existing Setting
	err = s.db.First(&existing, "key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(&Setting{Key: key, Value: string(raw), UpdatedAt: now()}).Error
	}
	if err != nil {
		return err
	}
	existing.Value = string(raw)
	existing.UpdatedAt = now()
	return s.db.Save(&existing).Error
}

// GetSetting decodes a setting into out. A missing setting is not an error: it
// leaves out untouched so callers can supply their own defaults.
func (s *Store) GetSetting(key string, out any) error {
	var rec Setting
	err := s.db.First(&rec, "key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(rec.Value), out)
}

// ---------- admins ----------

// CreateAdmin stores a WebUI admin.
func (s *Store) CreateAdmin(username, passwordHash string) error {
	return s.db.Create(&Admin{Username: username, PasswordHash: passwordHash, CreatedAt: now()}).Error
}

// GetAdmin loads a WebUI admin by username.
func (s *Store) GetAdmin(username string) (*Admin, error) {
	var a Admin
	if err := s.db.First(&a, "username = ?", username).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// CountAdmins returns how many WebUI admins exist, so the first run can prompt
// for initial setup.
func (s *Store) CountAdmins() (int64, error) {
	var n int64
	err := s.db.Model(&Admin{}).Count(&n).Error
	return n, err
}
