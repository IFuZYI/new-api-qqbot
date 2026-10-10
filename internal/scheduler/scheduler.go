// Package scheduler runs the periodic sweeps. Each sweep has its own interval,
// because their needs differ: the sign-in board is today-only and must be read
// often, while the roster changes slowly.
package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"qqbot/internal/service"
)

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
		if err := s.svc.SyncAllMembers(); err != nil {
			return err
		}
		// New members appear here, so follow up immediately: bind them. Group
		// conversion is the rules' job (each rule opts in via its own interval
		// or the bind trigger), not a blanket sweep.
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
		if err := s.svc.SyncAllCheckins(date); err != nil {
			// A group with no sign-in board is normal, so record and carry on.
			s.record("checkins", err)
		}
		_, err = s.svc.Award(date)
		return err
	})

	<-ctx.Done()
}

func (s *Scheduler) memberInterval() time.Duration {
	cfg, err := s.svc.Settings()
	if err != nil {
		return 2 * time.Hour
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

func (s *Scheduler) checkinInterval() time.Duration {
	cfg, err := s.svc.Settings()
	if err != nil {
		return 30 * time.Minute
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
			if err := fn(); err != nil {
				s.record(name, err)
				log.Printf("[%s] sweep failed: %v", name, err)
			} else {
				s.record(name, nil)
			}
			d := every()
			if d <= 0 {
				d = time.Hour
			}
			timer.Reset(d)
		}
	}
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
	for _, name := range []string{"members", "checkins", "rules"} {
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
