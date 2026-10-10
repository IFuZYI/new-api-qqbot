package store

import (
	"gorm.io/gorm"
)

// NewAPIUser is a cached new-api account.
//
// The gateway stays the source of truth; this row exists so the WebUI can list
// every account - including the ones no QQ member has joined with (谁没进群) -
// and show each account's group without calling the gateway on every page load.
// The service never writes new-api's own database: it reads this list over the
// admin API and writes a group back through the same API.
type NewAPIUser struct {
	ID           int    `gorm:"primaryKey;autoIncrement:false" json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Group        string `json:"group"`
	Role         int    `json:"role"`
	Status       int    `json:"status"`
	Quota        int    `json:"quota"`
	RequestCount int    `json:"request_count"`
	SyncedAt     int64  `json:"synced_at"`
}

// TableName pins the table name. GORM's default naming would derive something
// from the acronym-heavy Go name; the raw DELETE in ReplaceNewAPIUsers needs a
// name that cannot drift.
func (NewAPIUser) TableName() string { return "newapi_users" }

// ReplaceNewAPIUsers refreshes the cached account list.
//
// It is a full replace rather than an upsert: the gateway's list is
// authoritative, so an account deleted there must not linger here and keep
// showing up as "未进群". Callers must therefore only call it after a *complete*
// fetch - a partial list would delete the accounts it failed to read.
func (s *Store) ReplaceNewAPIUsers(users []NewAPIUser) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// GORM refuses an unfiltered DELETE, so clear the table explicitly.
		if err := tx.Exec("DELETE FROM newapi_users").Error; err != nil {
			return err
		}
		if len(users) == 0 {
			return nil
		}
		at := now()
		for i := range users {
			users[i].SyncedAt = at
		}
		return tx.CreateInBatches(users, 200).Error
	})
}

// ListNewAPIUsers returns the cached accounts ordered by id.
func (s *Store) ListNewAPIUsers() ([]NewAPIUser, error) {
	var out []NewAPIUser
	err := s.db.Order("id").Find(&out).Error
	return out, err
}

// GetNewAPIUser loads one cached account.
func (s *Store) GetNewAPIUser(id int) (*NewAPIUser, error) {
	var u NewAPIUser
	if err := s.db.First(&u, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// SetNewAPIUserGroup updates the cached group after the gateway accepted the
// change, so the UI reflects it without waiting for the next full re-sync.
func (s *Store) SetNewAPIUserGroup(id int, group string) error {
	return s.db.Model(&NewAPIUser{}).Where("id = ?", id).
		Updates(map[string]any{"group": group, "synced_at": now()}).Error
}

// BindingsByNewAPIUser maps a new-api account id to the QQ bound to it, so the
// UI can tell an account with a member from one nobody has joined with.
func (s *Store) BindingsByNewAPIUser() (map[int]int64, error) {
	var bs []Binding
	if err := s.db.Find(&bs).Error; err != nil {
		return nil, err
	}
	out := make(map[int]int64, len(bs))
	for _, b := range bs {
		out[b.NewAPIUserID] = b.QQ
	}
	return out, nil
}
