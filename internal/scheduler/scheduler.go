// Package scheduler runs the periodic sweeps. Each sweep has its own interval,
// because their needs differ: the sign-in board is today-only and must be read
// often, while the roster changes slowly.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"qqbot/internal/service"
)

// patrolDisabledCheck is how often a switched-off patrol re-reads its setting.
const patrolDisabledCheck = 30 * time.Second

// Scheduler drives the sweeps on independent timers.
type Scheduler struct {
	svc *service.Service

	mu      sync.Mutex
	lastRun map[string]time.Time
	lastErr map[string]string
}

// New builds a Scheduler.
func New(svc *service.Service) *Scheduler {
	return &Scheduler{
		svc:     svc,
		lastRun: map[string]time.Time{},
		lastErr: map[string]string{},
	}
}

// Run starts the loops and blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	go s.loop(ctx, "members", s.memberInterval, func() error {
		return s.svc.SyncAllMembers()
	})

	// The bind patrol is separate from the roster patrol on purpose: binding is
	// cheap (one account lookup per unbound member) while reading a roster is
	// not, so an operator can retry bindings far more often than they re-read
	// the rosters. It only ever touches members with no binding.
	go s.loop(ctx, "bind", s.bindInterval, func() error {
		_, err := s.svc.AutoBind()
		return err
	})

	go s.loop(ctx, "rules", s.ruleInterval, func() error {
		_, err := s.svc.RunScheduledRules()
		return err
	})

	go s.loop(ctx, "checkins", s.checkinInterval, func() error {
		date, err := s.svc.Today()
		if err != nil {
			return err
		}
		// Remember a sync failure: the award step usually succeeds, and its nil
		// return would otherwise erase the error before the UI could read it,
		// making a dead board look like a healthy patrol.
		syncErr := s.svc.SyncAllCheckins(date)
		if _, err := s.svc.Award(date); err != nil {
			return err
		}
		return syncErr
	})

	<-ctx.Done()
}

// bindInterval returns 0 when the bind patrol is switched off or its interval
// is 0, which is what makes the loop idle instead of running.
func (s *Scheduler) bindInterval() time.Duration {
	cfg, err := s.svc.Settings()
	if err != nil {
		return time.Hour
	}
	if !cfg.BindPatrolEnabled || cfg.BindPatrolMinutes <= 0 {
		return 0
	}
	return time.Duration(cfg.BindPatrolMinutes) * time.Minute
}

// memberInterval returns 0 when the roster patrol is switched off.
func (s *Scheduler) memberInterval() time.Duration {
	cfg, err := s.svc.Settings()
	if err != nil {
		return 2 * time.Hour
	}
	if !cfg.MemberPatrolEnabled {
		return 0
	}
	return time.Duration(cfg.MemberSyncMinutes) * time.Minute
}

// ruleInterval is the shortest configured rule interval, so every rule is
// checked at least as often as it asks for. Rules are also cheap no-ops when
// nothing matches.
func (s *Scheduler) ruleInterval() time.Duration {
	rules, err := s.svc.Rules()
	if err != nil {
		return time.Hour
	}
	best := 0
	for _, r := range rules {
		if !r.Enabled || r.IntervalMinutes <= 0 {
			continue
		}
		if best == 0 || r.IntervalMinutes < best {
			best = r.IntervalMinutes
		}
	}
	if best == 0 {
		// No scheduled rules configured; re-check occasionally so a newly added
		// rule starts running without a restart.
		return 30 * time.Minute
	}
	return time.Duration(best) * time.Minute
}

// checkinInterval returns 0 when the check-in patrol is switched off.
func (s *Scheduler) checkinInterval() time.Duration {
	cfg, err := s.svc.Settings()
	if err != nil {
		return 30 * time.Minute
	}
	if !cfg.CheckinPatrolEnabled {
		return 0
	}
	return time.Duration(cfg.CheckinSyncMinutes) * time.Minute
}

// loop runs fn every interval, re-reading the interval each tick so a settings
// change takes effect without a restart.
func (s *Scheduler) loop(ctx context.Context, name string, every func() time.Duration, fn func() error) {
	// A short initial delay lets the process finish starting and avoids a
	// burst of traffic the moment it comes up.
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			// A zero interval means the patrol is switched off: idle and keep
			// re-checking, so re-enabling it takes effect without a restart.
			if d := every(); d <= 0 {
				s.record(name, nil)
				timer.Reset(patrolDisabledCheck)
				continue
			}
			if err := s.safely(name, fn); err != nil {
				s.record(name, err)
				log.Printf("[%s] sweep failed: %v", name, err)
			} else {
				s.record(name, nil)
			}
			d := every()
			if d <= 0 {
				d = patrolDisabledCheck
			}
			timer.Reset(d)
		}
	}
}

// safely runs one sweep, converting a panic into an error. A panic on this
// goroutine would otherwise terminate the process, taking the WebUI, the event
// listener and the other patrols with it.
func (s *Scheduler) safely(name string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
}

func (s *Scheduler) record(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRun[name] = time.Now()
	if err != nil {
		s.lastErr[name] = err.Error()
	} else {
		delete(s.lastErr, name)
	}
}

// Status reports when each sweep last ran and whether it failed, for the UI.
func (s *Scheduler) Status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := map[string]any{}
	for _, name := range []string{"members", "bind", "checkins", "rules"} {
		entry := map[string]any{}
		if t, ok := s.lastRun[name]; ok {
			entry["last_run"] = t.Unix()
		}
		if e, ok := s.lastErr[name]; ok {
			entry["last_error"] = e
		}
		out[name] = entry
	}
	return out
}
