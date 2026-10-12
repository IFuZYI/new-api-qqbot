// Package scheduler runs the periodic sweeps. Each sweep has its own interval,
// because their needs differ: the sign-in board is today-only and must be read
// often, while the roster changes slowly.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"qqbot/internal/service"
)

// patrolDisabledCheck is how often a switched-off patrol re-reads its setting.
const patrolDisabledCheck = 30 * time.Second

// ErrUnknownPatrol is returned by RunNow for a name that is not a patrol. It is
// a sentinel so a caller can tell "you asked for something that does not exist"
// (a bad request) from "the sweep ran and failed" (an upstream problem) without
// matching on the message text.
var ErrUnknownPatrol = errors.New("未知的巡检")

// Scheduler drives the sweeps on independent timers.
type Scheduler struct {
	svc          *service.Service
	initialDelay time.Duration

	mu      sync.Mutex
	lastRun map[string]time.Time
	lastErr map[string]string
}

// New builds a Scheduler.
func New(svc *service.Service) *Scheduler {
	return &Scheduler{
		svc:          svc,
		initialDelay: 20 * time.Second,
		lastRun:      map[string]time.Time{},
		lastErr:      map[string]string{},
	}
}

// sweep is one patrol: what it does, how often it should run, and the name the
// WebUI and the status map use for it.
//
// The loops and the manual "test once" button both run through this table, so a
// patrol cannot have one behaviour on its timer and a different one when the
// operator clicks it.
type sweep struct {
	name string
	// every returns the interval to wait after this sweep, or 0 when the patrol
	// is switched off (which makes the loop idle).
	every func() time.Duration
	// body is the sweep itself. Its value is reported to a manual caller; the
	// loops ignore it.
	body func() (any, error)
}

// sweeps returns the patrol table. It is built per call because the interval
// functions read live settings.
func (s *Scheduler) sweeps() []sweep {
	return []sweep{
		{
			name:  "members",
			every: s.memberInterval,
			body: func() (any, error) {
				if err := s.svc.SyncAllMembers(); err != nil {
					return nil, err
				}
				return nil, nil
			},
		},
		{
			// The bind patrol is separate from the roster patrol on purpose:
			// binding is cheap (one account lookup per unbound member) while
			// reading a roster is not, so an operator can retry bindings far
			// more often than they re-read the rosters. It only ever touches
			// members with no binding.
			name:  "bind",
			every: s.bindInterval,
			body: func() (any, error) {
				return s.svc.AutoBind()
			},
		},
		{
			name:  "rules",
			every: s.ruleInterval,
			body: func() (any, error) {
				return s.svc.RunScheduledRules()
			},
		},
		{
			name:  "checkins",
			every: s.checkinInterval,
			body:  s.runCheckinSweep,
		},
	}
}

// Run blocks until cancellation and all owned sweeps have finished. Their
// callbacks may still use the database after cancellation, so callers must join.
func (s *Scheduler) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for _, sw := range s.sweeps() {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.loop(ctx, sw.name, sw.every, func() error {
				_, err := sw.body()
				return err
			})
		}()
	}
	workers.Wait()
}

// RunNow runs one patrol's sweep immediately and records the outcome, so the
// WebUI can offer a "test once" button per patrol.
//
// It runs the same body the loop runs, and through the same recording, because
// the two must not diverge: a test that reports success while the timer would
// fail (or the reverse) is worse than no test at all. Recording it is also what
// stops the row saying 尚未运行 afterwards, which would otherwise leave the
// operator unsure the click did anything.
//
// A switched-off patrol is runnable on purpose: "a disabled feature makes no
// request" is a rule about the automatic loops, and clicking 测试一次 is an
// explicit request for one run — it is how an operator decides whether to turn
// the feature on. The timer is untouched either way.
func (s *Scheduler) RunNow(name string) (any, error) {
	for _, sw := range s.sweeps() {
		if sw.name != name {
			continue
		}
		var res any
		// A panic must not take the process down (see safely), and it must be
		// recorded as a failure rather than reported as a success.
		err := s.safely(name, func() error {
			var runErr error
			res, runErr = sw.body()
			return runErr
		})
		s.record(name, err)
		if err != nil {
			return nil, err
		}
		return res, nil
	}
	return nil, fmt.Errorf("%w：%s", ErrUnknownPatrol, name)
}

// runCheckinSweep is the check-in patrol's body: read the sign-in board when that
// path is on, then pay the day's rewards.
//
// The two are separable on purpose. Reading a board costs one QQ request per
// monitored group and errors on every poll for a group without a board, so
// "message check-ins only" is a normal configuration; rewards, however, must
// still be paid, because a check-in that is never paid is the failure where
// people sign in and get nothing. The loop therefore runs while either path is
// on, and only the board read is skipped.
func (s *Scheduler) runCheckinSweep() (any, error) {
	cfg, err := s.svc.Settings()
	if err != nil {
		return nil, err
	}
	date, err := s.svc.Today()
	if err != nil {
		return nil, err
	}

	// Remember a sync failure: the award step usually succeeds, and its nil
	// return would otherwise erase the error before the UI could read it,
	// making a dead board look like a healthy patrol.
	var syncErr error
	if cfg.CheckinPatrolEnabled {
		syncErr = s.svc.SyncAllCheckins(date)
	}
	res, err := s.svc.Award(date)
	if err != nil {
		return nil, err
	}
	return res, syncErr
}

// intervalMinutes converts a configured minute count to a Duration, clamping
// out-of-range values. A value that would overflow is treated as "very long"
// rather than wrapping negative — a negative period reads as a disabled patrol,
// so an overflow would silently switch the feature off.
func intervalMinutes(minutes int) time.Duration {
	if minutes <= 0 {
		return 0
	}
	if minutes > service.MaxIntervalMinutes {
		return time.Duration(service.MaxIntervalMinutes) * time.Minute
	}
	return time.Duration(minutes) * time.Minute
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
	return intervalMinutes(cfg.BindPatrolMinutes)
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
	return intervalMinutes(cfg.MemberSyncMinutes)
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
	return intervalMinutes(best)
}

// checkinInterval returns 0 when there is nothing for the check-in loop to do.
//
// The loop reads the board AND pays rewards, so it must run while either is
// wanted: switching the board patrol off (the expensive path) must not stop
// rewards for message check-ins. Only when both paths are off is there nothing
// to do - and then no request is made at all.
func (s *Scheduler) checkinInterval() time.Duration {
	cfg, err := s.svc.Settings()
	if err != nil {
		return 30 * time.Minute
	}
	if !cfg.CheckinPatrolEnabled && !cfg.CheckinByMessageEnabled {
		return 0
	}
	return intervalMinutes(cfg.CheckinSyncMinutes)
}

// loop runs fn every interval, re-reading the interval each tick so a settings
// change takes effect without a restart.
func (s *Scheduler) loop(ctx context.Context, name string, every func() time.Duration, fn func() error) {
	// A short initial delay lets the process finish starting and avoids a
	// burst of traffic the moment it comes up.
	timer := time.NewTimer(s.initialDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			// A zero interval means the patrol is switched off: idle and keep
			// re-checking, so re-enabling it takes effect without a restart.
			if d := every(); d <= 0 {
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
