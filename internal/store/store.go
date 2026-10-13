// Package store owns the project's SQLite database: schema, migrations and the
// queries the rest of the service uses. Everything the service knows about QQ
// members, bindings and check-ins lives here; new-api is never written to.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// MonitoredGroup is a QQ group the bot account is in.
//
// Enabled deliberately has no gorm default. A default of true would make every
// newly discovered group monitored the moment the list is refreshed, so a bot
// invited into an unrelated group would start recording its members and paying
// them without the operator ever choosing it. New groups are stored disabled
// and the operator opts in; UpsertGroups keeps whatever was chosen before.
type MonitoredGroup struct {
	GroupID     int64  `gorm:"primaryKey;autoIncrement:false" json:"group_id"`
	GroupName   string `json:"group_name"`
	MemberCount int    `json:"member_count"`
	Enabled     bool   `json:"enabled"`
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

// CheckinSourceBoard and CheckinSourceMessage are Checkin.Source's two values:
// how the row was learned. A message is a first-hand statement by the member,
// the board is QQ's own record of them. Both count as a check-in for the day;
// the board is the richer row, so it upgrades a message-sourced one (see
// RecordCheckin).
const (
	CheckinSourceBoard   = "board"
	CheckinSourceMessage = "message"
)

// Checkin is one member's sign-in on one day. (QQ, Date) is the identity:
// re-polling the same day is a no-op, whether the row came from the sign-in
// board or from a message the member sent.
type Checkin struct {
	QQ          int64  `gorm:"primaryKey;autoIncrement:false" json:"qq"`
	Date        string `gorm:"primaryKey;autoIncrement:false;type:varchar(10)" json:"date"`
	GroupID     int64  `json:"group_id"`
	SignedAt    int64  `json:"signed_at"`
	Rank        int    `json:"rank"`
	RewardQuota int    `json:"reward_quota"`
	RewardedAt  int64  `json:"rewarded_at"`
	// RewardDrawn distinguishes a persisted zero award from an undrawn amount.
	RewardDrawn bool `gorm:"not null;default:false" json:"reward_drawn"`
	// RewardState is pending, claimed, ambiguous, paid, or legacy.
	// Only confirmed new payments set RewardedAt; legacy timestamps are preserved.
	RewardState     string `gorm:"not null;default:pending;index" json:"reward_state"`
	RewardError     string `json:"reward_error"`
	RewardClaimedAt int64  `json:"reward_claimed_at"`

	// Source records how the check-in was learned: board or message. The
	// default keeps rows written before this column existed reading as board
	// rows, which is what they were.
	Source string `gorm:"default:board" json:"source"`
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

// PromotionRule is one group-conversion rule. SourceGroups and Conditions are
// JSON, so adding a condition kind needs no migration.
type PromotionRule struct {
	ID              int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	OnBind          bool   `json:"on_bind"`
	IntervalMinutes int    `json:"interval_minutes"`
	SourceGroups    string `gorm:"type:text" json:"-"` // JSON array
	TargetGroup     string `json:"target_group"`
	Conditions      string `gorm:"type:text" json:"-"` // JSON array

	LastRunAt int64  `json:"last_run_at"`
	LastError string `json:"last_error"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = gorm.ErrRecordNotFound

// Store is the database handle.
type Store struct{ db *gorm.DB }

// Open opens (creating if needed) the SQLite database at path and migrates it.
//
// The pragmas matter: the member and check-in patrols run on separate
// goroutines and both write, so without a busy timeout the second writer fails
// the instant the first holds the write lock (SQLITE_BUSY) and its whole sweep
// is lost. WAL plus a generous busy timeout lets the writers queue instead.
func Open(path string) (*Store, error) {
	// The database holds the session-cookie signing key, the gateway password
	// and a live gateway session — strictly more than data/config.yaml, which
	// this codebase already hardens to 0600. SQLite creates the file (and its
	// -wal/-shm siblings) with the process umask, which on the documented
	// bare-metal run is world-readable 0644, so every local user could read the
	// signing key and mint an admin cookie. Create the directory owner-only and
	// tighten the file as soon as it exists.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir %s: %w", dir, err)
		}
		// MkdirAll applies its mode only to directories it CREATES, so an
		// existing data directory keeps whatever mode it already had. That is
		// not hypothetical: Docker creates a named volume's mount point as
		// 0755 + setgid, and it stayed 2755 on every deployment — the log
		// directory inside it was 0700 while the directory holding it was
		// world-traversable. Tighten it explicitly, tolerating a failure (the
		// store still works, and the file modes are what carry the secrets).
		if err := os.Chmod(dir, 0o700); err != nil {
			// Only report; do not fail the open. A read-only mount or a
			// directory owned by another user would otherwise make the service
			// refuse to start over a permission bit.
			slog.Warn("数据目录权限收紧失败", "dir", dir, "err", err)
		}
	}
	// _txlock=immediate makes every transaction take the write lock at BEGIN
	// rather than upgrading a read snapshot on its first write. The store's own
	// transactions read a row and then write it (BumpSettingInt,
	// ClaimPendingReward); with the default deferred BEGIN, another connection
	// committing in between makes that snapshot stale and SQLite refuses the
	// upgrade instantly with SQLITE_BUSY_SNAPSHOT — which busy_timeout does NOT
	// cover, because the transaction is being told to roll back, not waiting for
	// a lock. Measured before the fix: the second writer failed with
	// "database is locked (517)" while the first waited on it. With the write
	// lock held from BEGIN, the two queue on busy_timeout instead.
	dsn := path + "?_txlock=immediate" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		// foreign_keys is ON for future constraints: today no table declares a
		// FOREIGN KEY (verified: pragma_foreign_key_list is empty for every
		// table), so it currently enforces nothing and an orphan member_groups
		// row still inserts. It is kept because SQLite defaults it OFF per
		// connection — a constraint added later would otherwise be silently
		// unenforced, which is the failure mode that is hardest to notice.
		"&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Owner-only, for the file SQLite just created. The WAL/SHM siblings are
	// created later by SQLite itself, so they are tightened after the first
	// migration below.
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("tighten %s: %w", path, err)
	}
	if sqlDB, err := db.DB(); err == nil {
		// One connection is the standard SQLite configuration and it is what
		// makes concurrent writers safe: with two connections, two sweeps can
		// each hold a read lock and then both try to upgrade, which deadlocks
		// and the busy timeout does not help. Serialising at the pool removes
		// that path outright. The workload is small and bursty, so the lost
		// read parallelism costs nothing.
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetConnMaxLifetime(0)
	}
	if err := db.AutoMigrate(
		&MonitoredGroup{}, &Member{}, &MemberGroup{},
		&Binding{}, &Checkin{}, &Setting{}, &Admin{}, &PromotionRule{},
		&NewAPIUser{},
	); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Old rewarded_at markers were written BEFORE the gateway request. They
	// cannot prove payment, but must remain excluded from automatic retries.
	// Keep their original timestamp and amount under an explicit legacy state.
	if err := db.Model(&Checkin{}).
		Where("rewarded_at <> 0 AND reward_state = ?", "pending").
		Updates(map[string]any{"reward_state": "legacy", "reward_drawn": true,
			"reward_error": "历史领取记录，付款结果未经确认；请核对网关记录，勿直接重发"}).Error; err != nil {
		return nil, fmt.Errorf("migrate reward state: %w", err)
	}
	// The WAL and SHM files carry the same pages as the database and are created
	// by SQLite on the first write (the migration above), so they are tightened
	// here rather than at open. A missing sibling is not an error: SQLite may
	// have checkpointed it away.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("tighten %s: %w", path+suffix, err)
		}
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

// SetGroupEnabled turns monitoring on or off for one group.
func (s *Store) SetGroupEnabled(groupID int64, enabled bool) error {
	return s.db.Model(&MonitoredGroup{}).
		Where("group_id = ?", groupID).
		Updates(map[string]any{"enabled": enabled, "updated_at": now()}).Error
}

// ---------- members ----------

// UpsertMembers reconciles one group's roster: every member is created or
// updated, and memberships that are no longer present are removed from that
// group. Member rows are never deleted, so history (bindings, check-ins) survives.
func (s *Store) UpsertMembers(groupID int64, ms []Member) error {
	// An EMPTY roster is refused rather than reconciled to nothing.
	//
	// The reconciliation below is deliberate: a member the group no longer
	// reports has left, and their membership row must go. But "the group has no
	// members" is impossible for a roster this service can read — the bot is
	// itself a member of every group it polls — so an empty list means the read
	// was partial (NapCat's member cache can be transiently empty and still
	// answer retcode 0), not that everyone left. Reconciling it would blank the
	// whole group in the panel until the next poll, which is the same class of
	// damage as replacing the new-api user cache from a partial fetch.
	if len(ms) == 0 {
		return nil
	}
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

// EnsureMember records one member and their membership without touching any
// other row. Unlike UpsertMembers it does NOT treat the argument as the group's
// authoritative roster, so it is safe to call for a single observation (a join
// event, a bind command).
//
// A blank nickname leaves an existing one alone, so a caller that has no name
// (the join event carries none) cannot wipe the name a roster sync stored.
func (s *Store) EnsureMember(groupID, qq int64, nickname string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing Member
		err := tx.First(&existing, "qq = ?", qq).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			rec := Member{QQ: qq, Nickname: nickname, FirstSeen: now(), UpdatedAt: now()}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if strings.TrimSpace(nickname) != "" {
				existing.Nickname = nickname
			}
			existing.UpdatedAt = now()
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
		}

		var mg MemberGroup
		err = tx.First(&mg, "qq = ? AND group_id = ?", qq, groupID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&MemberGroup{QQ: qq, GroupID: groupID, Joined: now()}).Error
		}
		return err
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
	// Only the memberships of the members being returned. The previous version
	// loaded every row of member_groups and filtered in Go, so a one-group
	// listing (or the unbound list) read the whole table on every panel request
	// — the same class of issue CheckinTotalsByQQ was introduced to fix, one
	// function away.
	qqs := make([]int64, 0, len(ms))
	for _, m := range ms {
		qqs = append(qqs, m.QQ)
	}
	var links []MemberGroup
	if err := s.db.Where("qq IN ?", qqs).Find(&links).Error; err != nil {
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

// InsertBindingIfAbsent atomically creates an automatic binding. A loser reads
// the winner in the same transaction and must not run on-bind side effects.
func (s *Store) InsertBindingIfAbsent(b Binding) (*Binding, bool, error) {
	inserted := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		b.CreatedAt = now()
		b.UpdatedAt = b.CreatedAt
		res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "qq"}}, DoNothing: true}).Create(&b)
		if res.Error != nil {
			return res.Error
		}
		inserted = res.RowsAffected == 1
		if !inserted {
			return tx.First(&b, "qq = ?", b.QQ).Error
		}
		return nil
	})
	return &b, inserted, err
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

// RecordCheckin stores a check-in seen on the sign-in board. Re-recording the
// same (qq, date) updates the observed rank/time but never touches RewardQuota
// or RewardedAt, so a repeated poll cannot re-randomise or re-award.
//
// A board row supersedes a message-sourced one: the board is QQ's own record,
// so it takes over the row and its source. The reverse cannot happen, because
// RecordMessageCheckin never writes over an existing row.
func (s *Store) RecordCheckin(c Checkin) error {
	if c.Source == "" {
		c.Source = CheckinSourceBoard
	}
	// Upsert on the (qq, date) primary key: two concurrent polls of the same
	// board must not race between a read and an insert and trip the unique
	// index, which would abort the rest of that group's sync.
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "qq"}, {Name: "date"}},
		DoUpdates: clause.Assignments(map[string]any{
			"signed_at": c.SignedAt,
			"rank":      c.Rank,
			"group_id":  c.GroupID,
			"source":    c.Source,
		}),
	}).Create(&c).Error
}

// RecordMessageCheckin stores a check-in inferred from a message the member
// sent. Unlike RecordCheckin it is strictly additive: a day already recorded
// (from the board, or from an earlier message) is left exactly as it is, and
// the call reports that it did not record anything. The board row is never
// downgraded, because this never writes over an existing row.
//
// This is the (qq, date) idempotency guard for the message path, and it is why
// a member cannot farm rewards by chatting: the row can only be created once.
//
// The insert is a single conditional statement rather than a read followed by a
// write, so two messages racing on the same day cannot both report themselves
// as the one that recorded it - which is the count the operator sees.
func (s *Store) RecordMessageCheckin(c Checkin) (bool, error) {
	c.Source = CheckinSourceMessage
	res := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "qq"}, {Name: "date"}},
		DoNothing: true,
	}).Create(&c)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
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

// CheckinTotalsByQQ returns every member's lifetime check-in count in one query,
// keyed by QQ.
//
// This exists for the user directory, which used to call CountCheckins once per
// member. That is an N+1: with 200 members the directory issued 200 extra
// queries for a number that one GROUP BY already has, and it did so on every
// filter keystroke (the query is debounced, not cached). One grouped read is
// O(1) queries regardless of roster size.
//
// Members with no check-ins are simply absent from the map; a caller reads a
// missing key as 0.
func (s *Store) CheckinTotalsByQQ() (map[int64]int, error) {
	type row struct {
		QQ    int64
		Total int
	}
	var rows []row
	err := s.db.Model(&Checkin{}).
		Select("qq, COUNT(*) AS total").
		Group("qq").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.QQ] = r.Total
	}
	return out, nil
}

// ClaimPendingReward atomically claims a pending row and returns its persisted
// first draw. A retry never changes that draw, including an explicit zero.
// Claimed rows are not automatically retried: a crash may follow a remote credit.
func (s *Store) ClaimPendingReward(qq int64, date string, amount int, at time.Time) (*Checkin, bool, error) {
	var c Checkin
	claimed := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Checkin{}).Where("qq = ? AND date = ? AND rewarded_at = 0 AND reward_state = ?", qq, date, "pending").Updates(map[string]any{
			"reward_quota": gorm.Expr("CASE WHEN reward_drawn THEN reward_quota ELSE ? END", amount),
			"reward_drawn": true, "reward_state": "claimed", "reward_claimed_at": at.Unix(), "reward_error": "",
		})
		if res.Error != nil {
			return res.Error
		}
		claimed = res.RowsAffected == 1
		if !claimed {
			return nil
		}
		return tx.First(&c, "qq = ? AND date = ?", qq, date).Error
	})
	return &c, claimed, err
}

// FinishReward transitions only a held claim. A failed database write leaves it
// held, so neither a credit nor an uncertain result can cause automatic repayment.
// pending means a definite rejection; ambiguous requires operator reconciliation.
func (s *Store) FinishReward(qq int64, date, state, message string, at time.Time) error {
	if state != "paid" && state != "pending" && state != "ambiguous" {
		return fmt.Errorf("invalid reward state %q", state)
	}
	paidAt := int64(0)
	if state == "paid" {
		paidAt = at.Unix()
	}
	res := s.db.Model(&Checkin{}).Where("qq = ? AND date = ? AND reward_state = ?", qq, date, "claimed").Updates(map[string]any{
		"reward_state": state, "reward_error": message, "rewarded_at": paidAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("reward claim missing for %d on %s", qq, date)
	}
	return nil
}

// PendingRewardsThrough includes missed days, but never future or held claims.
func (s *Store) PendingRewardsThrough(date string) ([]Checkin, error) {
	var out []Checkin
	err := s.db.Where("date <= ? AND rewarded_at = 0 AND reward_state = ?", date, "pending").Order("date, qq").Find(&out).Error
	return out, err
}

// CheckinsOn returns every check-in recorded for a date.
func (s *Store) CheckinsOn(date string) ([]Checkin, error) {
	var out []Checkin
	err := s.db.Where("date = ?", date).Order("qq").Find(&out).Error
	return out, err
}

// ---------- settings ----------

// BumpSettingInt atomically increments an integer setting and returns the new
// value, creating it at 1 on first use. A read-then-write would let two
// concurrent callers (a logout racing the first-ever read) both observe the
// same value and lose one increment, which is exactly what makes a session
// revocation vanish.
func (s *Store) BumpSettingInt(key string) (int64, error) {
	var out int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var rec Setting
		err := tx.First(&rec, "key = ?", key).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			out = 1
			return tx.Create(&Setting{Key: key, Value: "1", UpdatedAt: now()}).Error
		case err != nil:
			return err
		default:
			var cur int64
			if rec.Value != "" {
				if err := json.Unmarshal([]byte(rec.Value), &cur); err != nil {
					cur = 0
				}
			}
			out = cur + 1
			raw, err := json.Marshal(out)
			if err != nil {
				return err
			}
			rec.Value = string(raw)
			rec.UpdatedAt = now()
			return tx.Save(&rec).Error
		}
	})
	return out, err
}

// PutSetting stores a JSON-encoded setting, creating the row when it does not
// exist yet. The value is replaced wholesale, so a read-modify-write here races
// two concurrent callers and can lose one of the updates; a counter that must
// not lose an increment goes through BumpSettingInt instead.
func (s *Store) PutSetting(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal setting %s: %w", key, err)
	}
	// A single upsert also makes simultaneous first writes safe.
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&Setting{Key: key, Value: string(raw), UpdatedAt: now()}).Error
}

// GetSettingRaw returns a setting's stored JSON without decoding it. Callers
// that must tell "the field was never written" from "the field was cleared"
// need the raw text, since both decode to the zero value.
func (s *Store) GetSettingRaw(key string) (string, error) {
	var rec Setting
	err := s.db.First(&rec, "key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return rec.Value, nil
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

// SetAdminPassword replaces an admin's password hash.
func (s *Store) SetAdminPassword(username, passwordHash string) error {
	return s.db.Model(&Admin{}).
		Where("username = ?", username).
		Update("password_hash", passwordHash).Error
}

// CountAdmins returns how many WebUI admins exist, so the first run can prompt
// for initial setup.
func (s *Store) CountAdmins() (int64, error) {
	var n int64
	err := s.db.Model(&Admin{}).Count(&n).Error
	return n, err
}

// ---------- promotion rules ----------

// ListRules returns every rule ordered by id.
func (s *Store) ListRules() ([]PromotionRule, error) {
	var out []PromotionRule
	err := s.db.Order("id").Find(&out).Error
	return out, err
}

// GetRule loads one rule.
func (s *Store) GetRule(id int64) (*PromotionRule, error) {
	var r PromotionRule
	if err := s.db.First(&r, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateRule stores a new rule.
func (s *Store) CreateRule(r PromotionRule) (*PromotionRule, error) {
	r.CreatedAt = now()
	r.UpdatedAt = r.CreatedAt
	if err := s.db.Create(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateRule replaces a rule's configuration.
func (s *Store) UpdateRule(r PromotionRule) error {
	r.UpdatedAt = now()
	return s.db.Model(&PromotionRule{}).Where("id = ?", r.ID).Updates(map[string]any{
		"name":             r.Name,
		"enabled":          r.Enabled,
		"on_bind":          r.OnBind,
		"interval_minutes": r.IntervalMinutes,
		"source_groups":    r.SourceGroups,
		"target_group":     r.TargetGroup,
		"conditions":       r.Conditions,
		"updated_at":       r.UpdatedAt,
	}).Error
}

// DeleteRule removes a rule.
func (s *Store) DeleteRule(id int64) error {
	return s.db.Delete(&PromotionRule{}, "id = ?", id).Error
}

// MarkRuleRun records a rule's outcome.
// MarkRuleRun records a rule's outcome.
//
// last_error is the RULE-level reason (the sweep could not run), not a
// per-member one: a per-member failure is counted in the result and logged with
// its qq/user_id, and an existing test pins that it must not mark the rule
// itself broken. A clean run clears the field, so the panel's row stops
// showing a reason that no longer applies.
func (s *Store) MarkRuleRun(id int64, at int64, errMsg string) error {
	return s.db.Model(&PromotionRule{}).Where("id = ?", id).Updates(map[string]any{
		"last_run_at": at,
		"last_error":  errMsg,
	}).Error
}

// ScheduledRules returns the enabled rules that take part in the periodic sweep.
func (s *Store) ScheduledRules() ([]PromotionRule, error) {
	var out []PromotionRule
	err := s.db.Where("enabled = ? AND interval_minutes > 0", true).
		Order("id").Find(&out).Error
	return out, err
}

// OnBindRules returns the enabled rules that fire when a member is bound.
func (s *Store) OnBindRules() ([]PromotionRule, error) {
	var out []PromotionRule
	err := s.db.Where("enabled = ? AND on_bind = ?", true, true).
		Order("id").Find(&out).Error
	return out, err
}
