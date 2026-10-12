package service

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"qqbot/internal/store"
)

// UserRow is one row of the operator's user table. It is keyed by both a QQ and
// a new-api account, because the two sides can be out of step: an account whose
// owner never joined the group has no QQ, and a member whose account was never
// created has no account. Showing the union is what makes "谁没进群" visible.
type UserRow struct {
	// Kind tells the UI which side the row came from:
	//   member      - a QQ seen in a monitored group
	//   left        - a known QQ no longer in any monitored group
	//   account     - a new-api account with no bound QQ
	Kind string `json:"kind"`

	// QQ side. QQ is 0 for an account nobody has joined with.
	QQ       int64   `json:"qq"`
	Nickname string  `json:"nickname"`
	GroupIDs []int64 `json:"group_ids"`
	InGroup  bool    `json:"in_group"`

	// new-api side. NewAPIUserID is 0 for an unbound member.
	Bound        bool   `json:"bound"`
	NewAPIUserID int    `json:"newapi_user_id,omitempty"`
	NewAPIUser   string `json:"newapi_username,omitempty"`
	NewAPIEmail  string `json:"newapi_email,omitempty"`
	NewAPIGroup  string `json:"newapi_group,omitempty"`
	NewAPIStatus int    `json:"newapi_status,omitempty"`
	BindSource   string `json:"bind_source,omitempty"`
	CheckedToday bool   `json:"checked_today"`
	// CheckinSource says how today's check-in was learned: board or message.
	// Empty when there is no check-in today.
	CheckinSource string `json:"checkin_source,omitempty"`
	CheckinDays   int    `json:"checkin_days"`
	RewardToday   int    `json:"reward_today"`
	// RewardState mirrors the check-in's reward state so the directory can
	// show an uncertain outcome instead of presenting it as paid.
	RewardState string `json:"reward_state,omitempty"`
	RewardError string `json:"reward_error,omitempty"`
}

// UserFilter narrows the directory. Every field is optional; the zero value
// matches everything.
type UserFilter struct {
	// Bound selects by binding state: "" (any), "bound", "unbound".
	Bound string
	// InGroup selects by QQ-group membership: "" (any), "in", "out".
	InGroup string
	// Group matches the account's new-api group name exactly.
	Group string
	// Keyword matches QQ, nickname, new-api username or email (substring,
	// case-insensitive).
	Keyword string
	// Kind selects a row kind: "" (any), "member", "left", "account".
	Kind string
}

// UserDirectory builds the operator-facing table for date.
//
// It is a union of members, bindings (even without member rows), and cached
// new-api accounts not represented by either. The account list is the cache
// refreshed by SyncNewAPIUsers;
// when it has never been synced the directory still works, it just cannot show
// accounts nobody has joined with.
func (s *Service) UserDirectory(filter UserFilter, date string) ([]UserRow, error) {
	members, err := s.st.ListMembers()
	if err != nil {
		return nil, err
	}
	// One query for every binding, rather than one per member.
	bindings, err := s.st.ListBindings()
	if err != nil {
		return nil, err
	}
	byQQ := make(map[int64]store.Binding, len(bindings))
	for _, b := range bindings {
		byQQ[b.QQ] = b
	}
	accounts, err := s.st.ListNewAPIUsers()
	if err != nil {
		return nil, err
	}
	byID := make(map[int]store.NewAPIUser, len(accounts))
	for _, a := range accounts {
		byID[a.ID] = a
	}
	// One read for today's sign-ins and one grouped read for lifetime totals,
	// rather than a GetCheckin + CountCheckins pair per member. At 200 members
	// the per-member form issued 400 queries to answer two questions that these
	// two set-wise reads already answer, and this runs on every filter change.
	todayCheckins, err := s.st.CheckinsOn(date)
	if err != nil {
		return nil, err
	}
	checkinByQQ := make(map[int64]store.Checkin, len(todayCheckins))
	for _, c := range todayCheckins {
		checkinByQQ[c.QQ] = c
	}
	totals, err := s.st.CheckinTotalsByQQ()
	if err != nil {
		return nil, err
	}

	rows := make([]UserRow, 0, len(members)+len(accounts))
	// Accounts a member row already represents, so the account loop below does
	// not emit a duplicate.
	shown := make(map[int]bool, len(members))
	shownQQ := make(map[int64]bool, len(members))
	for _, m := range members {
		shownQQ[m.QQ] = true
		row := UserRow{
			Kind:     "member",
			QQ:       m.QQ,
			Nickname: m.Nickname,
			GroupIDs: m.GroupIDs,
			InGroup:  len(m.GroupIDs) > 0,
		}
		if !row.InGroup {
			row.Kind = "left"
		}
		if b, ok := byQQ[m.QQ]; ok {
			row.Bound = true
			row.NewAPIUserID = b.NewAPIUserID
			row.NewAPIUser = b.NewAPIUsername
			row.NewAPIEmail = b.NewAPIEmail
			row.BindSource = b.Source
			shown[b.NewAPIUserID] = true
			if a, ok := byID[b.NewAPIUserID]; ok {
				row.NewAPIUser = a.Username
				row.NewAPIEmail = a.Email
				row.NewAPIGroup = a.Group
				row.NewAPIStatus = a.Status
			}
		}
		if c, ok := checkinByQQ[m.QQ]; ok {
			row.CheckedToday = true
			row.CheckinSource = c.Source
			row.RewardToday = c.RewardQuota
			row.RewardState, row.RewardError = c.RewardState, c.RewardError
		}
		row.CheckinDays = totals[m.QQ]
		rows = append(rows, row)
	}

	// Bindings are keyed by QQ, not account: several orphan QQs may point to
	// one account, including an account already represented by a member row.
	for _, b := range bindings {
		if shownQQ[b.QQ] {
			continue
		}
		row := UserRow{
			Kind: "account", QQ: b.QQ, GroupIDs: []int64{}, Bound: true,
			NewAPIUserID: b.NewAPIUserID, NewAPIUser: b.NewAPIUsername,
			NewAPIEmail: b.NewAPIEmail, BindSource: b.Source,
			CheckinDays: totals[b.QQ],
		}
		if a, ok := byID[b.NewAPIUserID]; ok {
			row.NewAPIUser, row.NewAPIEmail = a.Username, a.Email
			row.NewAPIGroup, row.NewAPIStatus = a.Group, a.Status
			row.Nickname = a.DisplayName
		}
		if c, ok := checkinByQQ[b.QQ]; ok {
			row.CheckedToday = true
			row.CheckinSource, row.RewardToday = c.Source, c.RewardQuota
			row.RewardState, row.RewardError = c.RewardState, c.RewardError
		}
		shown[b.NewAPIUserID] = true
		rows = append(rows, row)
	}

	// Every remaining account has no represented binding.
	for _, a := range accounts {
		if shown[a.ID] {
			continue
		}
		row := UserRow{
			Kind:         "account",
			NewAPIUserID: a.ID,
			NewAPIUser:   a.Username,
			NewAPIEmail:  a.Email,
			NewAPIGroup:  a.Group,
			NewAPIStatus: a.Status,
			Nickname:     a.DisplayName,
		}
		rows = append(rows, row)
	}

	out := make([]UserRow, 0, len(rows))
	for _, r := range rows {
		if r.matches(filter) {
			out = append(out, r)
		}
	}
	// Stable order: QQ rows by QQ, then account-only rows by id. Keeping the
	// QQ rows first matches the way the operator reads the table.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].QQ == 0) != (out[j].QQ == 0) {
			return out[j].QQ == 0
		}
		if out[i].QQ != out[j].QQ {
			return out[i].QQ < out[j].QQ
		}
		return out[i].NewAPIUserID < out[j].NewAPIUserID
	})
	return out, nil
}

func (r UserRow) matches(f UserFilter) bool {
	switch f.Bound {
	case "bound":
		if !r.Bound {
			return false
		}
	case "unbound":
		if r.Bound {
			return false
		}
	}
	switch f.InGroup {
	case "in":
		if !r.InGroup {
			return false
		}
	case "out":
		if r.InGroup {
			return false
		}
	}
	if f.Group != "" && r.NewAPIGroup != f.Group {
		return false
	}
	if f.Kind != "" && r.Kind != f.Kind {
		return false
	}
	if kw := strings.ToLower(strings.TrimSpace(f.Keyword)); kw != "" {
		hay := strings.ToLower(strings.Join([]string{
			r.Nickname, r.NewAPIUser, r.NewAPIEmail,
		}, "\n"))
		if !strings.Contains(hay, kw) && !strings.Contains(formatQQ(r.QQ), kw) {
			return false
		}
	}
	return true
}

func formatQQ(qq int64) string {
	if qq == 0 {
		return ""
	}
	return strconv.FormatInt(qq, 10)
}

// SyncNewAPIUsers refreshes the cached account list from the gateway. It
// replaces the cache only after a complete read, so a partial fetch cannot make
// real accounts disappear from the directory.
func (s *Service) SyncNewAPIUsers() (int, error) {
	us, err := s.gw.ListUsers()
	if err != nil {
		// The cache is written with a FULL replace, so it must only be written
		// after a complete read: caching a failed fetch would delete real
		// accounts. The error is returned and the cache is left alone.
		return 0, err
	}
	if len(us) == 0 {
		// An empty answer is ambiguous — a broken fetch and an instance with no
		// accounts look identical from here — and a full replace with an empty
		// list would wipe the cache. The read is reported as "0 accounts" and
		// the cache is left as it was; the operator can still clear it
		// deliberately from the panel.
		return 0, nil
	}
	out := make([]store.NewAPIUser, 0, len(us))
	for _, u := range us {
		out = append(out, store.NewAPIUser{
			ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
			Email: u.Email, Group: u.Group, Role: u.Role, Status: u.Status,
			Quota: u.Quota, RequestCount: u.RequestCount,
		})
	}
	if err := s.st.ReplaceNewAPIUsers(out); err != nil {
		return 0, err
	}
	return len(out), nil
}

// SetUserGroup moves one new-api account into group, re-reading the account
// first.
//
// The re-read is required, not defensive: new-api's PUT replaces the fields it
// accepts, so sending a sparse object blanks display_name. It also keeps the
// cached group honest, since the gateway is the source of truth.
func (s *Service) SetUserGroup(userID int, group string) error {
	// Serialise against the rule sweeps and the bulk path: the read, the
	// decision and the write must not interleave with another writer for the
	// same account, or the final group depends on completion order.
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	return s.setUserGroupLocked(userID, group)
}

// setUserGroupLocked is the body of SetUserGroup for callers that already hold
// groupMu (the bulk path holds it across the whole selection).
func (s *Service) setUserGroupLocked(userID int, group string) error {
	group = strings.TrimSpace(group)
	if group == "" {
		return errors.New("目标分组不能为空")
	}
	if userID == 0 {
		return errors.New("new-api 账号 ID 不能为空")
	}
	u, err := s.gw.GetUser(userID)
	if err != nil {
		return err
	}
	if u.Group == group {
		// Already there: writing anyway would be a no-op on the gateway but
		// still a needless write, and it would blank fields if the read above
		// returned a sparse object.
		if err := s.st.SetNewAPIUserGroup(userID, group); err != nil {
			return err
		}
		return nil
	}
	if err := s.gw.SetGroup(u, group); err != nil {
		return err
	}
	// Reflect it locally so the UI updates without a full re-sync.
	return s.st.SetNewAPIUserGroup(userID, group)
}

// GroupChangeResult reports one account's group change in a batch.
type GroupChangeResult struct {
	UserID   int    `json:"user_id"`
	Username string `json:"username,omitempty"`
	QQ       int64  `json:"qq,omitempty"`
	Group    string `json:"group"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// SetUserGroups applies a group to many accounts, reporting each outcome.
//
// One account failing must not stop the rest, so errors are collected per row
// rather than returned: a single role-protected account should not block a
// batch the operator selected.
func (s *Service) SetUserGroups(userIDs []int, group string) ([]GroupChangeResult, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return nil, errors.New("目标分组不能为空")
	}
	if len(userIDs) == 0 {
		return nil, errors.New("请先选择要调整的账号")
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	bindings, err := s.st.BindingsByNewAPIUser()
	if err != nil {
		return nil, err
	}
	out := make([]GroupChangeResult, 0, len(userIDs))
	seen := make(map[int]bool, len(userIDs))
	for _, id := range userIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		res := GroupChangeResult{UserID: id, Group: group, QQ: bindings[id]}
		if u, err := s.gw.GetUser(id); err == nil {
			res.Username = u.Username
		}
		if err := s.setUserGroupLocked(id, group); err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
		out = append(out, res)
	}
	return out, nil
}
