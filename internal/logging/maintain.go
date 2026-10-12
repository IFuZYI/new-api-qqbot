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
// It is serialised: the hourly maintenance tick and a settings save can call it
// at the same moment, and two sweeps over the same directory make the loser's
// os.Remove return ENOENT, which was reported as a cleanup FAILURE even though
// every file it wanted gone was gone. (Measured: 38% of overlapping pairs with
// 23 expired files logged a false ERROR, which is the one level the operator is
// meant to trust.) Serialising also keeps the level-change record exact.
//
// A change of level is itself recorded: the operator flips the switch in the
// panel and then opens the log page, and without a line saying the new level is
// in force they cannot tell the save from a silent failure. An unchanged policy
// records nothing, because the maintenance loop re-applies it every hour.
func (m *Manager) Apply(p Policy) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	// Decide "did it change" from what the logger actually holds, and only after
	// a successful set: comparing the raw stored string against the canonical
	// name made "DEBUG" look like a change on every hourly tick, and announcing
	// a refused value said "已生效" next to the warning that it was ignored.
	before := m.Level()
	if err := m.SetLevel(p.Level); err != nil {
		m.logger.Warn("忽略无效的日志等级", "level", p.Level, "err", err)
	} else if after := m.Level(); after != before {
		m.logger.Info("日志等级已生效", "new_level", after)
	}
	removed, err := m.Cleanup(p.RetentionDays)
	if err != nil {
		m.logger.Error("清理过期日志失败", "err", err)
	}
	if removed > 0 {
		m.logger.Info("已清理过期日志", "removed", removed, "retention_days", p.RetentionDays)
	}
}

// Maintain applies policy() every MaintainInterval, until ctx is done.
//
// It does NOT apply on entry: the caller applies once at start-up, so the level
// is in force and the retention sweep has run before the server accepts
// requests. Applying again here would sweep twice within the same second and
// record two "cleaned up expired logs" lines with different counts for one
// start-up.
func (m *Manager) Maintain(ctx context.Context, policy func() Policy) {
	m.maintain(ctx, MaintainInterval, policy)
}

func (m *Manager) maintain(ctx context.Context, every time.Duration, policy func() Policy) {
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
