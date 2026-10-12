package logging

import (
	"context"
	"time"
)

// Policy is the operator's log configuration, read from the settings.
type Policy struct {
	Level         string
	RetentionDays int
}

// MaintainInterval is how often the stored policy is re-applied. The level is
// also applied immediately on every settings save (see Apply); the tick is what
// catches a day rolling over for retention, and a policy changed elsewhere.
const MaintainInterval = time.Hour

// Apply sets the level and runs the retention sweep once.
//
// A change of level is itself recorded: the operator flips the switch in the
// panel and then opens the log page, and without a line saying the new level is
// in force they cannot tell the save from a silent failure. An unchanged policy
// records nothing, because the maintenance loop re-applies it every hour.
func (m *Manager) Apply(p Policy) {
	changed := p.Level != "" && p.Level != m.Level()
	if err := m.SetLevel(p.Level); err != nil {
		m.logger.Warn("忽略无效的日志等级", "level", p.Level, "err", err)
	}
	if changed {
		m.logger.Info("日志等级已生效", "new_level", m.Level())
	}
	removed, err := m.Cleanup(p.RetentionDays)
	if err != nil {
		m.logger.Error("清理过期日志失败", "err", err)
	}
	if removed > 0 {
		m.logger.Info("已清理过期日志", "removed", removed, "retention_days", p.RetentionDays)
	}
}

// Maintain applies policy() at start and then every MaintainInterval, until ctx
// is done.
func (m *Manager) Maintain(ctx context.Context, policy func() Policy) {
	m.maintain(ctx, MaintainInterval, policy)
}

func (m *Manager) maintain(ctx context.Context, every time.Duration, policy func() Policy) {
	m.Apply(policy())
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Apply(policy())
		}
	}
}
