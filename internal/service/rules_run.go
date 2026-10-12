package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// NewAPIGroups returns the gateway's configured group names, for the rule form.
func (s *Service) NewAPIGroups() (map[string]struct{}, error) {
	return s.gw.Groups()
}

// Rules loads every configured rule.
func (s *Service) Rules() ([]Rule, error) {
	rows, err := s.st.ListRules()
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, ruleFromStore(r))
	}
	return out, nil
}

// CreateRule validates and stores a rule.
func (s *Service) CreateRule(r Rule) (*Rule, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	row, err := ruleToStore(r)
	if err != nil {
		return nil, err
	}
	created, err := s.st.CreateRule(row)
	if err != nil {
		return nil, err
	}
	out := ruleFromStore(*created)
	return &out, nil
}

// UpdateRule validates and stores changes to a rule.
func (s *Service) UpdateRule(r Rule) (*Rule, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.st.GetRule(r.ID); err != nil {
		return nil, err
	}
	row, err := ruleToStore(r)
	if err != nil {
		return nil, err
	}
	row.ID = r.ID
	if err := s.st.UpdateRule(row); err != nil {
		return nil, err
	}
	got, err := s.st.GetRule(r.ID)
	if err != nil {
		return nil, err
	}
	out := ruleFromStore(*got)
	return &out, nil
}

// DeleteRule removes a rule.
func (s *Service) DeleteRule(id int64) error { return s.st.DeleteRule(id) }

// ruleToStore encodes the list fields as JSON.
func ruleToStore(r Rule) (store.PromotionRule, error) {
	groups := r.SourceGroups
	if groups == nil {
		groups = []string{}
	}
	conds := r.Conditions
	if conds == nil {
		conds = []Condition{}
	}
	gj, err := json.Marshal(groups)
	if err != nil {
		return store.PromotionRule{}, fmt.Errorf("encode source groups: %w", err)
	}
	cj, err := json.Marshal(conds)
	if err != nil {
		return store.PromotionRule{}, fmt.Errorf("encode conditions: %w", err)
	}
	return store.PromotionRule{
		Name:            r.Name,
		Enabled:         r.Enabled,
		OnBind:          r.OnBind,
		IntervalMinutes: r.IntervalMinutes,
		SourceGroups:    string(gj),
		TargetGroup:     r.TargetGroup,
		Conditions:      string(cj),
	}, nil
}

// ruleFromStore decodes the JSON list fields, marking the rule unusable when a
// stored blob cannot be read.
//
// Both list fields fail CLOSED, not open. An empty list is meaningful on both
// sides — source_groups empty means 任意分组 (every group), conditions empty
// means "no extra restriction" — so letting a corrupt blob decode into one
// silently widens the rule. That is the dangerous direction: a targeted rule
// becomes a blanket conversion that moves every bound member's account into the
// target group. A rule whose stored JSON cannot be read is therefore marked
// unusable (DecodeError) instead, and Matches refuses it.
func ruleFromStore(row store.PromotionRule) Rule {
	r := Rule{
		ID:              row.ID,
		Name:            row.Name,
		Enabled:         row.Enabled,
		OnBind:          row.OnBind,
		IntervalMinutes: row.IntervalMinutes,
		TargetGroup:     row.TargetGroup,
		LastRunAt:       row.LastRunAt,
		LastError:       row.LastError,
	}
	if row.SourceGroups != "" {
		if err := json.Unmarshal([]byte(row.SourceGroups), &r.SourceGroups); err != nil {
			// Marked unusable below; the slice is reset to empty rather than
			// left nil so the JSON stays an array. A nil slice marshals to
			// `null`, and the panel reads `.length` off it — one corrupt row
			// would take the whole page down with a TypeError instead of
			// showing the one broken rule. `DecodeError` is what carries
			// "unusable"; the field itself does not need to be null to say so.
			r.DecodeError = fmt.Sprintf("source_groups unreadable: %v", err)
		} else if r.SourceGroups == nil {
			r.DecodeError = "source_groups unreadable: expected array, got null"
		}
	}
	if r.SourceGroups == nil {
		r.SourceGroups = []string{}
	}
	if row.Conditions != "" {
		if err := json.Unmarshal([]byte(row.Conditions), &r.Conditions); err != nil {
			// Same reasoning as source_groups: an empty condition list means
			// "match everyone bound", so a corrupt blob must not be allowed to
			// decode into one. The rule is refused via DecodeError, and the
			// slice stays an array for the same wire-format reason.
			r.DecodeError = fmt.Sprintf("conditions unreadable: %v", err)
		} else if r.Conditions == nil {
			r.DecodeError = "conditions unreadable: expected array, got null"
		}
	}
	if r.Conditions == nil {
		r.Conditions = []Condition{}
	}
	return r
}

// RuleRunResult summarizes one rule's pass.
type RuleRunResult struct {
	RuleID   int64  `json:"rule_id"`
	RuleName string `json:"rule_name"`
	Promoted int    `json:"promoted"`
	Skipped  int    `json:"skipped"`
	Failed   int    `json:"failed"`
	Error    string `json:"error,omitempty"`
}

// ApplyRules runs the given rules over every binding, including manually bound
// QQs with no member row. A rule that cannot read
// the gateway records its error and the other rules still run, so one bad rule
// does not stall the sweep.
func (s *Service) ApplyRules(rules []Rule) ([]RuleRunResult, error) {
	results := make([]RuleRunResult, 0, len(rules))
	if len(rules) == 0 {
		return results, nil
	}
	// Serialise the read-decide-write against the manual group-change paths:
	// two rules (or a rule and an operator edit) that both read the account
	// before either writes would each pass their own source-group gate and the
	// final group would be whichever wrote last.
	s.groupMu.Lock()
	defer s.groupMu.Unlock()

	bindings, err := s.st.ListBindings()
	if err != nil {
		return nil, err
	}
	// Read each enabled group's roster once, for the QQ-side condition values -
	// but only when a rule in this batch actually looks at them. A set of
	// gateway-only rules then costs no QQ request at all, which matters because
	// this sweep runs on its own timer.
	needQQData := false
	for _, rule := range rules {
		if rule.NeedsQQData() {
			needQQData = true
			break
		}
	}
	groupMembers := map[int64]MemberContext{}
	var qqErr error
	if needQQData {
		groupMembers, qqErr = s.rosterByQQ()
	}

	now := time.Now().Unix()
	accounts := map[int]*newapi.User{} // cache by bound user id

	for _, rule := range rules {
		res := RuleRunResult{RuleID: rule.ID, RuleName: rule.Name}
		if rule.NeedsQQData() && qqErr != nil {
			res.Error = qqErr.Error()
			slog.Warn("分组转换：读取 QQ 数据失败", "rule", rule.Name, "err", qqErr)
		}
		for _, b := range bindings {
			if rule.NeedsQQData() && qqErr != nil {
				res.Failed++
				continue
			}
			u, ok := accounts[b.NewAPIUserID]
			if !ok {
				u, err = s.gw.GetUser(b.NewAPIUserID)
				if err != nil {
					res.Failed++
					slog.Warn("分组转换：读取账号失败", "rule", rule.Name, "qq", b.QQ, "user_id", b.NewAPIUserID, "err", err)
					continue
				}
				accounts[b.NewAPIUserID] = u
			}

			ctx := groupMembers[b.QQ]
			ctx.QQ = b.QQ
			ctx.Bound = true
			ctx.User = u
			ctx.CheckinDays, ctx.CheckinError = s.st.CountCheckins(b.QQ)
			if err := rule.checkinError(ctx); err != nil {
				res.Failed++
				res.Error = err.Error()
				slog.Warn("分组转换：条件求值失败", "rule", rule.Name, "qq", b.QQ, "err", err)
				continue
			}

			if !rule.Matches(ctx) {
				res.Skipped++
				continue
			}
			if u.Group == rule.TargetGroup {
				res.Skipped++
				continue
			}
			if err := s.gw.SetGroup(u, rule.TargetGroup); err != nil {
				res.Failed++
				continue
			}
			// The gateway client does not mutate u, so record the new group
			// locally: otherwise the next rule in this sweep still sees the old
			// group, passes its own source-group gate, and moves the account
			// again.
			u.Group = rule.TargetGroup
			res.Promoted++
		}

		if err := s.st.MarkRuleRun(rule.ID, now, res.Error); err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// RunScheduledRules applies the rules that opt into the periodic sweep.
func (s *Service) RunScheduledRules() ([]RuleRunResult, error) {
	rows, err := s.st.ScheduledRules()
	if err != nil {
		return nil, err
	}
	rules := make([]Rule, 0, len(rows))
	for _, r := range rows {
		rules = append(rules, ruleFromStore(r))
	}
	return s.ApplyRules(rules)
}

// ApplyOnBindRules runs the rules triggered by a binding, for one member. It is
// called right after a bind so the new member's group changes immediately
// instead of waiting for the next sweep.
//
// cache carries rosters already read by the caller's sweep (see qqDataCache).
// AutoBind binds member after member, and the roster of a monitored group is the
// same for all of them, so passing the sweep's cache is what keeps this at one
// read per group instead of one per member. A nil cache reads its own, which is
// what a one-off caller (the in-group command) wants.
func (s *Service) ApplyOnBindRules(qq int64, cache *qqDataCache) ([]RuleRunResult, error) {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	rows, err := s.st.OnBindRules()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	b, err := s.st.GetBinding(qq)
	if err != nil {
		return nil, err
	}
	u, err := s.gw.GetUser(b.NewAPIUserID)
	if err != nil {
		return nil, err
	}

	// Only read QQ data if one of these rules actually looks at it. On the bind
	// path this matters most: AutoBind calls this per member, so a
	// gateway-only rule set would otherwise read every monitored group's roster
	// once per newly bound member.
	needQQData := false
	for _, row := range rows {
		if ruleFromStore(row).NeedsQQData() {
			needQQData = true
			break
		}
	}

	if needQQData && cache == nil {
		cache = newQQDataCache(s.st, s.qq)
	}

	ctx, err := s.memberContext(qq, u, false, nil)
	if err != nil {
		return nil, err
	}
	var qqErr error
	if needQQData {
		var qqCtx MemberContext
		qqCtx, qqErr = cache.contextFor(qq)
		qqCtx.Bound, qqCtx.User, qqCtx.QQ = ctx.Bound, ctx.User, qq
		qqCtx.CheckinDays = ctx.CheckinDays
		qqCtx.CheckinError = ctx.CheckinError
		ctx = &qqCtx
	}

	results := make([]RuleRunResult, 0, len(rows))
	now := time.Now().Unix()
	for _, row := range rows {
		rule := ruleFromStore(row)
		res := RuleRunResult{RuleID: rule.ID, RuleName: rule.Name}

		if rule.NeedsQQData() && qqErr != nil {
			res.Failed++
			res.Error = qqErr.Error()
			slog.Warn("分组转换：读取 QQ 数据失败", "rule", rule.Name, "qq", qq, "err", qqErr)
		} else if err := rule.checkinError(*ctx); err != nil {
			res.Failed++
			res.Error = err.Error()
			slog.Warn("分组转换：条件求值失败", "rule", rule.Name, "qq", qq, "err", err)
		} else if !rule.Matches(*ctx) || u.Group == rule.TargetGroup {
			res.Skipped++
		} else if err := s.gw.SetGroup(u, rule.TargetGroup); err != nil {
			res.Failed++
			res.Error = err.Error()
			slog.Warn("分组转换失败", "rule", rule.Name, "qq", qq, "user_id", u.ID, "target", rule.TargetGroup, "err", err)
		} else {
			u.Group = rule.TargetGroup
			res.Promoted++
			slog.Info("分组转换完成", "rule", rule.Name, "qq", qq, "user_id", u.ID, "target", rule.TargetGroup)
		}

		if err := s.st.MarkRuleRun(rule.ID, now, res.Error); err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// memberContext assembles one member's rule context from QQ and the gateway.
//
// The roster is only read when a rule actually needs QQ-side data (see
// Rule.NeedsQQData): the caller passes that in, so a set of gateway-only rules
// costs no QQ request at all. When it is not needed, HasQQData stays false, and
// a QQ-side condition would fail closed rather than read a zero level as a real
// one - which is why the flag, not just the values, is what callers rely on.
//
// It resolves through the same cache as the sweep (see qqDataCache), so the bind
// trigger and the patrol cannot disagree about a member in several groups.
func (s *Service) memberContext(qq int64, u *newapi.User, needQQData bool, cache *qqDataCache) (*MemberContext, error) {
	ctx := MemberContext{QQ: qq, Bound: u != nil, User: u}
	ctx.CheckinDays, ctx.CheckinError = s.st.CountCheckins(qq)
	if !needQQData {
		return &ctx, nil
	}

	qqCtx, err := cache.contextFor(qq)
	if err != nil {
		return nil, err
	}
	ctx.HasQQData = qqCtx.HasQQData
	ctx.Level = qqCtx.Level
	ctx.QQLevel = qqCtx.QQLevel
	ctx.JoinDays = qqCtx.JoinDays
	ctx.UnknownJoinTime = qqCtx.UnknownJoinTime
	return &ctx, nil
}

// A local check-in outage must not block unrelated gateway/QQ conditions.
func (r Rule) checkinError(ctx MemberContext) error {
	for _, c := range r.Conditions {
		if c.ConditionType == CondCheckinDays && ctx.CheckinError != nil {
			return fmt.Errorf("QQ %d check-in count: %w", ctx.QQ, ctx.CheckinError)
		}
	}
	return nil
}

// qqDataCache reads each monitored group's roster at most once, and resolves
// members from it.
//
// One roster read covers every member of that group, so a sweep that binds N
// members must not read the same roster N times: that is the difference between
// one request per group and one per member, and it is the whole reason this
// exists. The cache lives for a single sweep; a long-lived one would serve stale
// levels to the next run.
//
// A cache that was never loaded resolves nothing, which would make every QQ-side
// condition fail closed - a silent "no rule matches" that looks like a rules
// problem. contextFor therefore loads on demand, so the only way to get an empty
// answer is for the rosters to genuinely be empty.
type qqDataCache struct {
	qq QQ
	// st reads the monitored-group list when the caller did not supply it.
	st *store.Store

	// loaded records that the group list has been read, so an empty result is
	// not re-fetched on every lookup.
	loaded  bool
	loadErr error
	byGroup map[int64][]napcat.GroupMember
	// merged is the per-member context, resolved on first use.
	merged map[int64]MemberContext
}

// newQQDataCache builds a cache over the monitored groups.
func newQQDataCache(st *store.Store, qq QQ) *qqDataCache {
	return &qqDataCache{st: st, qq: qq, byGroup: map[int64][]napcat.GroupMember{}}
}

// load reads every monitored group's roster once. Calling it with the groups the
// caller already has avoids a second lookup; passing nil reads them here.
func (c *qqDataCache) load(enabled []store.MonitoredGroup) error {
	if c.loaded {
		return c.loadErr
	}
	if enabled == nil {
		var err error
		enabled, err = c.st.EnabledGroups()
		if err != nil {
			return err
		}
	}
	c.loaded = true
	for _, g := range enabled {
		ms, err := c.qq.GetGroupMemberList(g.GroupID)
		if err != nil {
			// A partial maximum can wrongly satisfy an upper-bound condition.
			c.loadErr = errors.Join(c.loadErr, fmt.Errorf("QQ group %d: %w", g.GroupID, err))
			continue
		}
		c.byGroup[g.GroupID] = ms
	}
	return c.loadErr
}

// contextFor resolves one member's QQ-side context from the cached rosters. It
// loads on first use, so a caller that forgot to load gets the rosters rather
// than an empty (fail-closed) answer.
func (c *qqDataCache) contextFor(qq int64) (MemberContext, error) {
	if err := c.load(nil); err != nil {
		return MemberContext{}, err
	}
	if c.merged == nil {
		merged := map[int64]MemberContext{}
		now := time.Now().Unix()
		for _, ms := range c.byGroup {
			for _, m := range ms {
				ctx := ContextFromMember(m, now)
				merged[m.QQ] = mergeContext(merged[m.QQ], ctx)
			}
		}
		c.merged = merged
	}
	return c.merged[qq], nil
}

// all returns the resolved context for every member seen in the rosters.
func (c *qqDataCache) all() (map[int64]MemberContext, error) {
	if _, err := c.contextFor(0); err != nil {
		return nil, err
	}
	return c.merged, nil
}

// mergeContext folds one group's view of a member into the accumulated one.
//
// Every field takes the highest value seen, so the result does not depend on the
// order the groups happen to be read in. Only merging some fields (as this did)
// makes a member's context depend on which group was read last, and lets the
// bind trigger and the patrol disagree about the same member.
func mergeContext(acc, add MemberContext) MemberContext {
	if !add.HasQQData {
		return acc
	}
	if !acc.HasQQData {
		return add
	}
	if add.Level > acc.Level {
		acc.Level = add.Level
	}
	if add.QQLevel > acc.QQLevel {
		acc.QQLevel = add.QQLevel
	}
	if add.JoinDays > acc.JoinDays {
		acc.JoinDays = add.JoinDays
	}
	acc.UnknownJoinTime = acc.UnknownJoinTime || add.UnknownJoinTime
	return acc
}

// rosterByQQ builds per-member QQ-side context for every monitored group.
func (s *Service) rosterByQQ() (map[int64]MemberContext, error) {
	groups, err := s.st.EnabledGroups()
	if err != nil {
		return nil, err
	}
	cache := newQQDataCache(s.st, s.qq)
	if err := cache.load(groups); err != nil {
		return nil, err
	}
	return cache.all()
}
