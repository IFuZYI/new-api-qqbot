package scheduler

import (
	"context"
	"errors"
	"qqbot/internal/napcat"
	"qqbot/internal/service"
	"reflect"
	"testing"
	"time"
)

type runtimeBlockingQQ struct {
	stubQQ
	entered, release chan struct{}
}

func (q runtimeBlockingQQ) GetGroupMemberList(int64) ([]napcat.GroupMember, error) {
	close(q.entered)
	<-q.release
	return nil, nil
}

func TestRuntimeRunWaitsForOwnedSweep(t *testing.T) {
	_, st, _ := newScheduler(t)
	enableSchedGroup(t, st, 1)
	q := runtimeBlockingQQ{entered: make(chan struct{}), release: make(chan struct{})}
	svc := service.New(st, stubGateway{}, q)
	cfg, _ := svc.Settings()
	cfg.BindPatrolEnabled = false
	cfg.CheckinPatrolEnabled = false
	cfg.CheckinByMessageEnabled = false
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	s := New(svc)
	s.initialDelay = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-q.entered:
	case <-time.After(time.Second):
		close(q.release)
		t.Fatal("sweep did not enter")
	}
	cancel()
	select {
	case <-done:
		close(q.release)
		t.Fatal("Run returned with an owned sweep in flight")
	case <-time.After(30 * time.Millisecond):
	}
	close(q.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not join completed sweep")
	}
	if _, ok := s.Status()["members"].(map[string]any)["last_run"]; !ok {
		t.Fatal("Run returned before sweep recorded")
	}
}

func TestRuntimeDisabledTickPreservesStatus(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "never ran", true: "previous failure"}[prior], func(t *testing.T) {
			s, _, _ := newScheduler(t)
			s.initialDelay = 0
			if prior {
				s.record("members", errors.New("previous failure"))
			}
			before := s.Status()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tick := make(chan struct{})
			done := make(chan struct{})
			go func() {
				s.loop(ctx, "members", func() time.Duration { close(tick); return 0 }, func() error { t.Error("disabled body ran"); return nil })
				close(done)
			}()
			select {
			case <-tick:
			case <-time.After(time.Second):
				t.Fatal("no tick")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("no exit")
			}
			if after := s.Status(); !reflect.DeepEqual(before, after) {
				t.Fatalf("disabled tick changed status: before=%v after=%v", before, after)
			}
		})
	}
}
