package service

import (
	"errors"
	"fmt"
	"strings"

	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
)

// ConditionType is what a condition looks at.
type ConditionType string

const (
	CondGroupLevel   ConditionType = "group_level"   // 群等级
	CondQQLevel      ConditionType = "qq_level"      // QQ 账号等级
	CondJoinDays     ConditionType = "join_days"     // 入群天数
	CondCheckinDays  ConditionType = "checkin_days"  // 累计签到天数
	CondQuota        ConditionType = "quota"         // new-api 余额
	CondRequestCount ConditionType = "request_count" // new-api 请求数
)

// ConditionTypes lists every supported condition, for the UI.
var ConditionTypes = []ConditionType{
	CondGroupLevel, CondQQLevel, CondJoinDays, CondCheckinDays, CondQuota, CondRequestCount,
}

// ConditionOp is a comparison operator.
type ConditionOp string

const (
	OpGTE ConditionOp = ">="
	OpGT  ConditionOp = ">"
	OpLTE ConditionOp = "<="
	OpLT  ConditionOp = "<"
	OpEq  ConditionOp = "=="
)

// ConditionOps lists every supported operator, for the UI.
var ConditionOps = []ConditionOp{OpGTE, OpGT, OpLTE, OpLT, OpEq}

// Condition is one threshold a member must meet.
type Condition struct {
	ConditionType ConditionType `json:"type"`
	Op            ConditionOp   `json:"op"`
	Value         int           `json:"value"`
}

// Validate checks a single condition.
func (c Condition) Validate() error {
	if !c.ConditionType.valid() {
		return fmt.Errorf("unknown condition type %q", c.ConditionType)
	}
	if !c.Op.valid() {
		return fmt.Errorf("unknown operator %q", c.Op)
	}
	if c.Value < 0 {
		return fmt.Errorf("condition value must not be negative (got %d)", c.Value)
	}
	return nil
}

func (t ConditionType) valid() bool {
	for _, x := range ConditionTypes {
		if x == t {
			return true
		}
	}
	return false
}

func (o ConditionOp) valid() bool {
	for _, x := range ConditionOps {
		if x == o {
			return true
		}
	}
	return false
}

// Label is a human-readable name for the condition type, used by the UI.
func (t ConditionType) Label() string {
	switch t {
	case CondGroupLevel:
		return "群等级"
	case CondQQLevel:
		return "QQ 等级"
	case CondJoinDays:
		return "入群天数"
	case CondCheckinDays:
		return "累计签到天数"
	case CondQuota:
		return "new-api 余额"
	case CondRequestCount:
		return "new-api 请求数"
	}
	return string(t)
}

// Rule moves a matching member's account into TargetGroup.
//
// A rule fires either on the binding event (OnBind) or on a schedule
// (IntervalMinutes > 0), and a member must satisfy every Condition. A rule with
// no trigger and no conditions is invalid rather than "match everything", so a
// half-finished rule cannot sweep an entire group into another group.
type Rule struct {
	ID              int64       `json:"id"`
	Name            string      `json:"name"`
	Enabled         bool        `json:"enabled"`
	OnBind          bool        `json:"on_bind"`
	IntervalMinutes int         `json:"interval_minutes"`
	SourceGroups    []string    `json:"source_groups"`
	TargetGroup     string      `json:"target_group"`
	Conditions      []Condition `json:"conditions"`

	LastRunAt int64  `json:"last_run_at"`
	LastError string `json:"last_error,omitempty"`

	// DecodeError is set when the stored rule could not be read back. A rule
	// with a decode error never matches, so a corrupt row cannot widen into a
	// match-everyone rule.
	DecodeError string `json:"decode_error,omitempty"`
}

// Validate checks a rule is usable.
func (r Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("规则名称不能为空")
	}
	if strings.TrimSpace(r.TargetGroup) == "" {
		return errors.New("目标分组不能为空")
	}
	if !r.OnBind && r.IntervalMinutes <= 0 {
		return errors.New("至少选择一种触发方式：绑定触发或设置巡检周期")
	}
	if r.IntervalMinutes < 0 {
		return errors.New("巡检周期不能为负")
	}
	for _, g := range r.SourceGroups {
		if strings.TrimSpace(g) == "" {
			return errors.New("源分组不能为空")
		}
		if g == r.TargetGroup {
			return fmt.Errorf("源分组与目标分组不能相同（%q）", g)
		}
	}
	for _, c := range r.Conditions {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// RunsOnSchedule reports whether the rule takes part in the periodic sweep.
func (r Rule) RunsOnSchedule() bool { return r.IntervalMinutes > 0 }

// NeedsQQData reports whether evaluating this rule requires a QQ roster read.
//
// Reading a roster is this system's expensive call - one request per monitored
// group, and the call NapCat serves from an in-process cache that still costs a
// round trip - so a rule that can be decided from the gateway alone must not
// cause one. Conditions that read QQ data (群等级 / QQ 等级 / 入群天数) need it;
// the gateway-side ones (余额 / 请求数) and the check-in count do not.
//
// A rule whose conditions could not be decoded reports true: it fails closed
// anyway (see Matches), and guessing "no QQ data needed" for a rule that might
// have had a level condition would silently change its meaning.
func (r Rule) NeedsQQData() bool {
	if r.DecodeError != "" {
		return true
	}
	for _, c := range r.Conditions {
		if c.ConditionType.needsQQData() {
			return true
		}
	}
	return false
}

// needsQQData reports whether a condition is evaluated from QQ-side data.
func (t ConditionType) needsQQData() bool {
	switch t {
	case CondGroupLevel, CondQQLevel, CondJoinDays:
		return true
	}
	return false
}

// MemberContext is everything a rule can look at for one member.
type MemberContext struct {
	QQ    int64
	Bound bool
	User  *newapi.User // nil when unbound

	// HasQQData reports whether the QQ-side fields below were actually read.
	// Without it a member whose roster could not be read (or who is in no
	// monitored group) would present all-zero levels and join days, and a
	// "<"/"<=" condition would match them - a mass-mis-promotion path. A
	// QQ-side condition therefore does not match when this is false.
	HasQQData bool

	Level       int // 群等级
	QQLevel     int // QQ 账号等级
	JoinDays    int
	CheckinDays int
}

// ContextFromMember builds the QQ-side part of a context. `now` is a Unix time,
// passed in so the caller controls the clock.
func ContextFromMember(m napcat.GroupMember, now int64) MemberContext {
	ctx := MemberContext{
		QQ:        m.QQ,
		HasQQData: true,
		Level:     int(m.Level),
		QQLevel:   int(m.QQLevel),
	}
	// 0 means NapCat did not report a join time; do not report decades of
	// membership for it.
	if m.JoinTime > 0 && now > m.JoinTime {
		ctx.JoinDays = int((now - m.JoinTime) / 86400)
	}
	return ctx
}

// Matches reports whether this rule applies to the member.
func (r Rule) Matches(ctx MemberContext) bool {
	if !r.Enabled {
		return false
	}
	// A rule whose stored form could not be read is unusable: failing closed is
	// the only safe reading, because the alternative (an empty condition list)
	// means "everyone".
	if r.DecodeError != "" {
		return false
	}
	if r.OnBind && !ctx.Bound {
		return false
	}
	// A rule with no trigger and no conditions would silently mean "everyone",
	// so it is treated as unfinished and matches nothing. A rule with a trigger
	// but no conditions is a deliberate "everyone bound" and is allowed.
	if !r.OnBind && r.IntervalMinutes <= 0 && len(r.Conditions) == 0 {
		return false
	}

	if len(r.SourceGroups) > 0 {
		if ctx.User == nil {
			return false
		}
		found := false
		for _, g := range r.SourceGroups {
			if g == ctx.User.Group {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	for _, c := range r.Conditions {
		if !c.satisfiedBy(ctx) {
			return false
		}
	}
	return true
}

func (c Condition) satisfiedBy(ctx MemberContext) bool {
	var actual int
	switch c.ConditionType {
	case CondGroupLevel, CondQQLevel, CondJoinDays:
		// These come from QQ. If the roster could not be read, the zero values
		// are unknown, not zero, so the condition must not be treated as met.
		if !ctx.HasQQData {
			return false
		}
	}
	switch c.ConditionType {
	case CondGroupLevel:
		actual = ctx.Level
	case CondQQLevel:
		actual = ctx.QQLevel
	case CondJoinDays:
		actual = ctx.JoinDays
	case CondCheckinDays:
		actual = ctx.CheckinDays
	case CondQuota:
		if ctx.User == nil {
			return false
		}
		actual = ctx.User.Quota
	case CondRequestCount:
		if ctx.User == nil {
			return false
		}
		actual = ctx.User.RequestCount
	default:
		return false
	}

	switch c.Op {
	case OpGTE:
		return actual >= c.Value
	case OpGT:
		return actual > c.Value
	case OpLTE:
		return actual <= c.Value
	case OpLT:
		return actual < c.Value
	case OpEq:
		return actual == c.Value
	}
	return false
}
