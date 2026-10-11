// Package service holds the business logic: keeping the member roster in sync
// with the QQ groups, matching members to new-api accounts, promoting accounts
// between groups, and paying the daily check-in reward.
package service

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"qqbot/internal/command"
	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// Settings is the operator-configurable state. It is stored as one JSON blob
// under the "settings" key, so adding a field needs no migration.
//
// Every automated path has its own switch, because they cost different things:
// the patrols and the two event-driven paths each have to be switchable off
// without disturbing the others.
type Settings struct {
	// Patrol cadence, in minutes. Each is independent: the sign-in board is
	// today-only and needs a short cycle, the roster does not.
	//
	// Both patrols can be switched off. They are the backstop, not the only
	// path: the group-increase event records a new member immediately, the bind
	// command records the member who used it, and a bind settles the group. The
	// check-in board, however, has no event at all (NapCat exposes no check-in
	// notice), so turning the check-in patrol off stops check-ins being
	// recorded and therefore stops rewards - the UI says so.
	MemberPatrolEnabled  bool `json:"member_patrol_enabled"`
	MemberSyncMinutes    int  `json:"member_sync_minutes"`
	CheckinPatrolEnabled bool `json:"checkin_patrol_enabled"`
	CheckinSyncMinutes   int  `json:"checkin_sync_minutes"`

	// Binding paths. Three independent ways to bind an unbound member, plus the
	// command patterns for the one a member triggers.
	//
	// Only unbound members are ever touched: a member who already has a binding
	// keeps it, so switching these on can never re-point an existing binding.
	BindCommandEnabled bool `json:"bind_command_enabled"`
	// BindCommandPatterns is the command list, one pattern per line. A
	// /slash-delimited/ line is a regular expression.
	BindCommandPatterns string `json:"bind_command_patterns"`
	// BindOnJoinEnabled binds a member the moment they join a monitored group.
	BindOnJoinEnabled bool `json:"bind_on_join_enabled"`
	// BindPatrolEnabled is the periodic sweep that retries every unbound
	// member, which is what catches accounts created after the member joined.
	BindPatrolEnabled bool `json:"bind_patrol_enabled"`
	// BindPatrolMinutes is that sweep's interval. 0 means the timer is off even
	// when the switch is on, so a schedule can be paused without losing the
	// switch state.
	BindPatrolMinutes int `json:"bind_patrol_minutes"`

	// Check-in paths. The board patrol is QQ's own sign-in board; the message
	// path counts a qualifying message as the day's check-in. Both record the
	// same (qq, date) row, so enabling both cannot double-count a day.
	CheckinByMessageEnabled bool `json:"checkin_by_message_enabled"`
	// CheckinPatterns is the message command list, one pattern per line.
	CheckinPatterns string `json:"checkin_patterns"`

	// Daily reward, drawn uniformly from [RewardMin, RewardMax] per member per
	// day. Equal values mean a fixed award.
	RewardMin int `json:"reward_min"`
	RewardMax int `json:"reward_max"`

	// Timezone used to decide what "today" means for check-ins.
	Timezone string `json:"timezone"`
}

// DefaultSettings returns the shipped defaults.
func DefaultSettings() Settings {
	return Settings{
		MemberPatrolEnabled:  true,
		MemberSyncMinutes:    120,
		CheckinPatrolEnabled: true,
		CheckinSyncMinutes:   30,

		BindCommandEnabled:  true,
		BindCommandPatterns: command.DefaultBindPatterns,
		BindOnJoinEnabled:   true,
		BindPatrolEnabled:   true,
		BindPatrolMinutes:   60,

		CheckinByMessageEnabled: true,
		CheckinPatterns:         command.DefaultCheckinPatterns,

		RewardMin: 100,
		RewardMax: 200,
		Timezone:  "Asia/Shanghai",
	}
}

const settingsKey = "settings"

// Gateway is the subset of the new-api client the service needs.
type Gateway interface {
	FindUserByEmail(addr string) (*newapi.User, error)
	GetUser(id int) (*newapi.User, error)
	SetGroup(u *newapi.User, group string) error
	AddQuota(userID, value int) error
	Groups() (map[string]struct{}, error)
	// ListUsers returns every account on the instance, so the WebUI can show
	// accounts no member has joined with.
	ListUsers() ([]newapi.User, error)
}

// QQ is the subset of the NapCat client the service needs.
type QQ interface {
	GetGroupList() ([]napcat.Group, error)
	GetGroupMemberList(groupID int64) ([]napcat.GroupMember, error)
	GetGroupSignedList(groupID int64) ([]napcat.SignedMember, error)
}

// Service coordinates the store, the gateway and QQ.
type Service struct {
	st *store.Store
	gw Gateway
	qq QQ

	// rndMu guards rnd: Award runs from both the scheduler goroutine and HTTP
	// handler goroutines, and *rand.Rand is not safe for concurrent use.
	rndMu sync.Mutex
	rnd   *rand.Rand
}

// New builds a Service.
func New(st *store.Store, gw Gateway, qq QQ) *Service {
	return &Service{
		st:  st,
		gw:  gw,
		qq:  qq,
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Settings loads the current settings, falling back to defaults field by field
// so a partially written blob still yields a usable configuration.
//
// Decoding into the defaults is what makes both directions work with no extra
// bookkeeping: a field the stored blob does not mention keeps its default (so a
// settings row written before a feature existed still gets a working pattern
// list), while a field the blob states as empty is empty (so an operator who
// deliberately clears the command list gets an empty one).
func (s *Service) Settings() (Settings, error) {
	out := DefaultSettings()
	if err := s.st.GetSetting(settingsKey, &out); err != nil {
		return out, err
	}
	return withDefaults(out), nil
}

// withDefaults repairs zero values left by an older or partial settings blob.
//
// It covers only the fields where zero is not a legitimate value: an interval of
// 0 minutes would be an infinite loop, a negative one nonsense. A switch or a
// pattern list is deliberately NOT repaired here, because off and empty are
// values the operator can choose on purpose.
func withDefaults(in Settings) Settings {
	d := DefaultSettings()
	if in.MemberSyncMinutes <= 0 {
		in.MemberSyncMinutes = d.MemberSyncMinutes
	}
	if in.CheckinSyncMinutes <= 0 {
		in.CheckinSyncMinutes = d.CheckinSyncMinutes
	}
	if in.Timezone == "" {
		in.Timezone = d.Timezone
	}
	if in.BindPatrolMinutes < 0 {
		in.BindPatrolMinutes = d.BindPatrolMinutes
	}
	// A stored range of 0..0 is a deliberate "no reward" and must be honoured;
	// only a missing (negative) range falls back to the default.
	if in.RewardMin < 0 || in.RewardMax < 0 {
		in.RewardMin, in.RewardMax = d.RewardMin, d.RewardMax
	}
	return in
}

// SaveSettings validates and stores settings.
func (s *Service) SaveSettings(in Settings) error {
	in = withDefaults(in)
	if in.RewardMin < 0 || in.RewardMax < 0 {
		return errors.New("reward values must not be negative")
	}
	if in.RewardMin > in.RewardMax {
		return fmt.Errorf("reward min (%d) must not exceed max (%d)", in.RewardMin, in.RewardMax)
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return fmt.Errorf("unknown timezone %q: %w", in.Timezone, err)
	}
	// A broken expression must be refused here, by the operator who typed it,
	// rather than silently becoming a pattern that never matches - the failure
	// mode this whole feature is easiest to get wrong.
	if _, err := command.Parse(in.BindCommandPatterns); err != nil {
		return fmt.Errorf("绑定指令正则无效：%w", err)
	}
	if _, err := command.Parse(in.CheckinPatterns); err != nil {
		return fmt.Errorf("签到指令正则无效：%w", err)
	}
	return s.st.PutSetting(settingsKey, in)
}

// bindPatterns returns the compiled bind-command list, or an empty set when the
// command path is switched off. A set that fails to compile yields an empty set
// too: matching nothing is the only safe reading of an unusable pattern list,
// and SaveSettings refuses to store one in the first place.
func (s *Service) bindPatterns() command.Set {
	cfg, err := s.Settings()
	if err != nil || !cfg.BindCommandEnabled {
		return command.Set{}
	}
	set, err := command.Parse(cfg.BindCommandPatterns)
	if err != nil {
		return command.Set{}
	}
	return set
}

// checkinPatterns is bindPatterns for the check-in command list.
func (s *Service) checkinPatterns() command.Set {
	cfg, err := s.Settings()
	if err != nil || !cfg.CheckinByMessageEnabled {
		return command.Set{}
	}
	set, err := command.Parse(cfg.CheckinPatterns)
	if err != nil {
		return command.Set{}
	}
	return set
}

// IsBindCommand reports whether a message is one of the configured bind
// commands.
func (s *Service) IsBindCommand(message string) bool {
	return s.bindPatterns().Matches(message)
}

// IsCheckinCommand reports whether a message is one of the configured check-in
// commands.
func (s *Service) IsCheckinCommand(message string) bool {
	return s.checkinPatterns().Matches(message)
}

// CheckinByAnyMessage reports whether "any message counts as a check-in" is on,
// which is the setting that makes a plain message a check-in.
func (s *Service) CheckinByAnyMessage() bool {
	cfg, err := s.Settings()
	if err != nil {
		return false
	}
	return cfg.CheckinByMessageEnabled
}

// BindOnJoinEnabled reports whether a member is bound the moment they join.
func (s *Service) BindOnJoinEnabled() bool {
	cfg, err := s.Settings()
	if err != nil {
		return false
	}
	return cfg.BindOnJoinEnabled
}

// Today returns the current date in the configured timezone, which is the key
// check-ins are recorded under.
func (s *Service) Today() (string, error) {
	st, err := s.Settings()
	if err != nil {
		return "", err
	}
	loc, err := time.LoadLocation(st.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format("2006-01-02"), nil
}

// SyncGroups refreshes the stored group list from QQ, keeping the operator's
// enable/disable choices.
func (s *Service) SyncGroups() ([]store.MonitoredGroup, error) {
	gs, err := s.qq.GetGroupList()
	if err != nil {
		return nil, err
	}
	out := make([]store.MonitoredGroup, 0, len(gs))
	for _, g := range gs {
		out = append(out, store.MonitoredGroup{
			GroupID:     g.GroupID,
			GroupName:   g.GroupName,
			MemberCount: g.MemberCount,
		})
	}
	if err := s.st.UpsertGroups(out); err != nil {
		return nil, err
	}
	return s.st.ListGroups()
}

// SyncMembers pulls one group's roster into the store.
func (s *Service) SyncMembers(groupID int64) error {
	ms, err := s.qq.GetGroupMemberList(groupID)
	if err != nil {
		return err
	}
	out := make([]store.Member, 0, len(ms))
	for _, m := range ms {
		out = append(out, store.Member{QQ: m.QQ, Nickname: m.DisplayName()})
	}
	return s.st.UpsertMembers(groupID, out)
}

// SyncAllMembers sweeps every enabled group.
func (s *Service) SyncAllMembers() error {
	gs, err := s.st.EnabledGroups()
	if err != nil {
		return err
	}
	var errs []error
	for _, g := range gs {
		if err := s.SyncMembers(g.GroupID); err != nil {
			errs = append(errs, fmt.Errorf("group %d: %w", g.GroupID, err))
		}
	}
	return errors.Join(errs...)
}

// SyncCheckins reads one group's sign-in board for date and records it. The
// board is today-only and paginated, so repeated calls merge into a union.
func (s *Service) SyncCheckins(groupID int64, date string) (int, error) {
	ss, err := s.qq.GetGroupSignedList(groupID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range ss {
		if err := s.st.RecordCheckin(store.Checkin{
			QQ: m.QQ, Date: date, GroupID: groupID, SignedAt: m.Time,
			// NapCat's rank is fractional (see flexFloat); it is display-only,
			// so round it for storage rather than rejecting the row.
			Rank: int(math.Round(float64(m.Rank))),
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// SyncAllCheckins sweeps the sign-in boards of every enabled group.
func (s *Service) SyncAllCheckins(date string) error {
	gs, err := s.st.EnabledGroups()
	if err != nil {
		return err
	}
	var errs []error
	for _, g := range gs {
		if _, err := s.SyncCheckins(g.GroupID, date); err != nil {
			// A group with no sign-in board is normal, not a failure: reporting
			// it would leave the check-in patrol permanently red whenever any
			// monitored group lacks a board.
			if errors.Is(err, napcat.ErrNoSigninBoard) {
				continue
			}
			errs = append(errs, fmt.Errorf("group %d: %w", g.GroupID, err))
		}
	}
	return errors.Join(errs...)
}

// BindResult summarizes one auto-binding pass.
type BindResult struct {
	Bound     int `json:"bound"`
	Pending   int `json:"pending"`
	Ambiguous int `json:"ambiguous"`
	Failed    int `json:"failed"`
}

// AutoBind matches every unbound member to a new-api account by their QQ email
// (qq@qq.com). An exact, unique match binds; anything else is left pending.
func (s *Service) AutoBind() (BindResult, error) {
	var res BindResult
	ms, err := s.st.UnboundMembers()
	if err != nil {
		return res, err
	}
	for _, m := range ms {
		u, err := s.gw.FindUserByEmail(qqEmail(m.QQ))
		switch {
		case errors.Is(err, newapi.ErrAmbiguousEmail):
			res.Ambiguous++
			continue
		case err != nil:
			res.Failed++
			continue
		}
		if u == nil {
			res.Pending++
			continue
		}
		if err := s.st.SetBinding(store.Binding{
			QQ: m.QQ, NewAPIUserID: u.ID, NewAPIUsername: u.Username,
			NewAPIEmail: u.Email, Source: "auto",
		}); err != nil {
			return res, err
		}
		res.Bound++
		// Apply the binding-triggered rules now, so a new member's group
		// changes immediately instead of at the next sweep. A rule failure
		// must not fail the binding that already succeeded.
		if _, err := s.ApplyOnBindRules(m.QQ); err != nil {
			res.Failed++
		}
	}
	return res, nil
}

// EnsureMember records a member row (and group membership) from a QQ-side
// observation, so a member is known without waiting for a roster patrol. Used
// by the group-increase event and by the bind command, which both know the
// member's identity directly.
func (s *Service) EnsureMember(groupID, qq int64, nickname string) error {
	return s.st.EnsureMember(groupID, qq, nickname)
}

// BindByCommand binds a QQ to the new-api account matching qq@qq.com, which is
// what a member triggers with ">>绑定<<". The member cannot name another email.
//
// The member is recorded first, so the command works with the roster patrol
// switched off: the message itself is proof the member is in that group.
func (s *Service) BindByCommand(groupID, qq int64) (*store.Binding, error) {
	if groupID != 0 {
		if err := s.EnsureMember(groupID, qq, ""); err != nil {
			return nil, err
		}
	}
	u, err := s.gw.FindUserByEmail(qqEmail(qq))
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("no new-api account uses %s", qqEmail(qq))
	}
	b := store.Binding{
		QQ: qq, NewAPIUserID: u.ID, NewAPIUsername: u.Username,
		NewAPIEmail: u.Email, Source: "command",
	}
	if err := s.st.SetBinding(b); err != nil {
		return nil, err
	}
	// A member who binds by command gets the same immediate group change.
	if _, err := s.ApplyOnBindRules(qq); err != nil {
		return nil, err
	}
	return s.st.GetBinding(qq)
}

// CheckinResult reports what a message-driven check-in did.
type CheckinResult struct {
	// Recorded is true when this call is what recorded the day. A day is
	// recorded once, so every later message that day reports false.
	Recorded bool `json:"recorded"`
	// AlreadyCheckedIn distinguishes "you already checked in today" from a
	// genuine store failure, which is returned as an error instead.
	AlreadyCheckedIn bool `json:"already_checked_in"`
}

// CheckinByMessage records a check-in for a member who sent a message.
//
// The day is the unit: the first qualifying message of the day records the
// check-in and everything after it is a no-op, so chatting cannot farm rewards
// (the reward is claimed once per (qq, date) by Award). The member row is
// recorded as a side effect, because the message itself proves they are in the
// group - which is what makes the check-in path work with the roster patrol
// switched off.
//
// A check-in recorded this way is upgraded if the sign-in board later reports
// the same day (see store.RecordCheckin): the board knows the rank and the
// actual sign-in time.
func (s *Service) CheckinByMessage(groupID, qq int64) (CheckinResult, error) {
	var res CheckinResult
	if qq == 0 || groupID == 0 {
		return res, errors.New("check-in needs both a group and a QQ")
	}
	if err := s.EnsureMember(groupID, qq, ""); err != nil {
		return res, err
	}
	date, err := s.Today()
	if err != nil {
		return res, err
	}
	recorded, err := s.st.RecordMessageCheckin(store.Checkin{
		QQ: qq, Date: date, GroupID: groupID,
	})
	if err != nil {
		return res, err
	}
	res.Recorded = recorded
	res.AlreadyCheckedIn = !recorded
	return res, nil
}

// IsMonitoredGroup reports whether a group is selected for monitoring, so
// commands from unrelated groups are ignored.
func (s *Service) IsMonitoredGroup(groupID int64) (bool, error) {
	gs, err := s.st.EnabledGroups()
	if err != nil {
		return false, err
	}
	for _, g := range gs {
		if g.GroupID == groupID {
			return true, nil
		}
	}
	return false, nil
}

// qqEmail derives a QQ number's conventional email address.
func qqEmail(qq int64) string { return fmt.Sprintf("%d@qq.com", qq) }

// AwardResult summarizes one reward sweep.
type AwardResult struct {
	Awarded int `json:"awarded"`
	Unbound int `json:"unbound"`
	Failed  int `json:"failed"`
}

// Award pays the daily check-in reward for date. Each (qq, date) is paid at
// most once: the amount is drawn from the configured range and the claim is
// recorded before the gateway call, so a crash mid-sweep cannot double-pay,
// and a gateway failure releases the claim for the next sweep to retry.
func (s *Service) Award(date string) (AwardResult, error) {
	var res AwardResult
	cfg, err := s.Settings()
	if err != nil {
		return res, err
	}

	checkins, err := s.st.CheckinsOn(date)
	if err != nil {
		return res, err
	}
	for _, c := range checkins {
		if c.RewardedAt != 0 {
			continue
		}
		b, err := s.st.GetBinding(c.QQ)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				res.Unbound++
				continue
			}
			return res, err
		}

		amount := s.draw(cfg)
		// Claim the slot and record the amount in one statement. Doing these
		// separately lets a losing concurrent sweep overwrite reward_quota with
		// an amount it never paid (the claim still prevents double payment).
		claimed, err := s.st.ClaimReward(c.QQ, date, amount, time.Now())
		if err != nil {
			return res, err
		}
		if !claimed {
			continue
		}

		if err := s.gw.AddQuota(b.NewAPIUserID, amount); err != nil {
			// Release the claim so the next sweep retries.
			if err := s.st.ReleaseReward(c.QQ, date); err != nil {
				return res, err
			}
			res.Failed++
			continue
		}
		res.Awarded++
	}
	return res, nil
}

// draw picks the day's award from the configured range, inclusive.
func (s *Service) draw(cfg Settings) int {
	if cfg.RewardMax <= cfg.RewardMin {
		return cfg.RewardMin
	}
	s.rndMu.Lock()
	defer s.rndMu.Unlock()
	return cfg.RewardMin + s.rnd.Intn(cfg.RewardMax-cfg.RewardMin+1)
}
