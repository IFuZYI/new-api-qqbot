package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

// ruleFromStore decodes the JSON list fields. A malformed blob yields an empty
// list rather than failing the whole listing.
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
		_ = json.Unmarshal([]byte(row.SourceGroups), &r.SourceGroups)
	}
	if r.SourceGroups == nil {
		r.SourceGroups = []string{}
	}
	if row.Conditions != "" {
		_ = json.Unmarshal([]byte(row.Conditions), &r.Conditions)
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

// ApplyRules runs the given rules over every member. A rule that cannot read
// the gateway records its error and the other rules still run, so one bad rule
// does not stall the sweep.
func (s *Service) ApplyRules(rules []Rule) ([]RuleRunResult, error) {
	results := make([]RuleRunResult, 0, len(rules))
	if len(rules) == 0 {
		return results, nil
	}

	members, err := s.st.ListMembers()
	if err != nil {
		return nil, err
	}
	// Read each enabled group's roster once, for the QQ-side condition values.
	groupMembers, err := s.rosterByQQ()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	accounts := map[int]*newapi.User{} // cache by bound user id

	for _, rule := range rules {
		res := RuleRunResult{RuleID: rule.ID, RuleName: rule.Name}
		for _, m := range members {
			b, err := s.st.GetBinding(m.QQ)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					res.Skipped++
					continue
				}
				res.Error = err.Error()
				break
			}

			u, ok := accounts[b.NewAPIUserID]
			if !ok {
				u, err = s.gw.GetUser(b.NewAPIUserID)
				if err != nil {
					res.Failed++
					continue
				}
				accounts[b.NewAPIUserID] = u
			}

			ctx := groupMembers[m.QQ]
			ctx.QQ = m.QQ
			ctx.Bound = true
			ctx.User = u
			if days, err := s.st.CountCheckins(m.QQ); err == nil {
				ctx.CheckinDays = days
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
func (s *Service) ApplyOnBindRules(qq int64) ([]RuleRunResult, error) {
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

	ctx, err := s.memberContext(qq, u)
	if err != nil {
		return nil, err
	}

	results := make([]RuleRunResult, 0, len(rows))
	now := time.Now().Unix()
	for _, row := range rows {
		rule := ruleFromStore(row)
		res := RuleRunResult{RuleID: rule.ID, RuleName: rule.Name}

		if !rule.Matches(*ctx) || u.Group == rule.TargetGroup {
			res.Skipped++
		} else if err := s.gw.SetGroup(u, rule.TargetGroup); err != nil {
			res.Failed++
			res.Error = err.Error()
		} else {
			res.Promoted++
		}

		if err := s.st.MarkRuleRun(rule.ID, now, res.Error); err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// memberContext assembles one member's rule context from QQ and the gateway.
func (s *Service) memberContext(qq int64, u *newapi.User) (*MemberContext, error) {
	ctx := MemberContext{QQ: qq, Bound: u != nil, User: u}
	if days, err := s.st.CountCheckins(qq); err == nil {
		ctx.CheckinDays = days
	}

	groups, err := s.st.EnabledGroups()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	for _, g := range groups {
		ms, err := s.qq.GetGroupMemberList(g.GroupID)
		if err != nil {
			continue
		}
		for _, m := range ms {
			if m.QQ != qq {
				continue
			}
			c := ContextFromMember(m, now)
			ctx.Level = c.Level
			ctx.QQLevel = c.QQLevel
			ctx.JoinDays = c.JoinDays
			return &ctx, nil
		}
	}
	return &ctx, nil
}

// rosterByQQ builds per-member QQ-side context for every monitored group.
func (s *Service) rosterByQQ() (map[int64]MemberContext, error) {
	groups, err := s.st.EnabledGroups()
	if err != nil {
		return nil, err
	}
	out := map[int64]MemberContext{}
	now := time.Now().Unix()
	for _, g := range groups {
		ms, err := s.qq.GetGroupMemberList(g.GroupID)
		if err != nil {
			// A group that cannot be read is skipped; the rest still apply.
			continue
		}
		for _, m := range ms {
			c := ContextFromMember(m, now)
			// Keep the highest level seen, in case a member is in several
			// monitored groups.
			if prev, ok := out[m.QQ]; ok && prev.Level > c.Level {
				c.Level = prev.Level
			}
			out[m.QQ] = c
		}
	}
	return out, nil
}
