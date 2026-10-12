package service

import (
	"sync"
	"testing"
	"time"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// gateGateway forces both writers to have read the account before either can
// write, which is the interleaving the lock exists to prevent. A bounded wait
// keeps the serialised case from hanging: the first reader proceeds alone once
// no second reader can be coming (it cannot, because it holds the lock).
type gateGateway struct {
	*fakeNewAPI
	target     int
	mu         sync.Mutex
	waiting    int
	maxWaiting int
	release    chan struct{}
}

func newGateGateway(inner *fakeNewAPI, target int) *gateGateway {
	return &gateGateway{fakeNewAPI: inner, target: target, release: make(chan struct{})}
}

func (g *gateGateway) GetUser(id int) (*newapi.User, error) {
	u, err := g.fakeNewAPI.GetUser(id)
	if id != g.target {
		return u, err
	}
	// waiting is the number of readers inside this gate right now; maxWaiting
	// records the high-water mark. Serialised callers keep it at 1.
	g.mu.Lock()
	g.waiting++
	if g.waiting > g.maxWaiting {
		g.maxWaiting = g.waiting
	}
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-time.After(150 * time.Millisecond):
	}
	g.mu.Lock()
	g.waiting--
	g.mu.Unlock()
	return u, err
}

// Two writers that both read the account before either writes would each pass
// their own source-group gate and the final group would be whichever wrote
// last. The read-decide-write must be serialised, and the second writer must
// re-read the account and refuse because it is no longer in the source group.
func TestR2ConcurrentRuleRunsRespectSourceGate(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	// Mimic the real gateway client: SetGroup does not mutate the caller's
	// struct, so a stale copy would otherwise look unchanged.
	no := false
	na.mutateUser = &no

	if err := st.SetBinding(store.Binding{QQ: 777, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	na.users()[7] = &newapi.User{ID: 7, Username: "u", Group: "default"}

	rule := Rule{ID: 1, Name: "to-vip", Enabled: true, IntervalMinutes: 5,
		SourceGroups: []string{"default"}, TargetGroup: "vip"}

	gate := newGateGateway(na, 7)
	svc.gw = gate

	const n = 2
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			if _, err := svc.ApplyRules([]Rule{rule}); err != nil {
				t.Errorf("ApplyRules: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := na.users()[7].Group; got != "vip" {
		t.Fatalf("final group = %q, want vip", got)
	}
	if len(na.groupSets) != 1 {
		t.Fatalf("the source gate was passed twice: %d writes %+v", len(na.groupSets), na.groupSets)
	}
	// With the read-decide-write serialised, two readers are never inside the
	// gate at once.
	gate.mu.Lock()
	peak := gate.maxWaiting
	gate.mu.Unlock()
	if peak >= 2 {
		t.Fatalf("two rule runs read the account concurrently (peak %d): the critical section is not serialised", peak)
	}
}

// A manual group change must not interleave with a rule sweep: the operator's
// write and the rule's write both read the account first.
func TestR2ManualChangeSerialisesWithRules(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	no := false
	na.mutateUser = &no

	if err := st.SetBinding(store.Binding{QQ: 778, NewAPIUserID: 8, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	na.users()[8] = &newapi.User{ID: 8, Username: "v", Group: "default"}

	rule := Rule{ID: 1, Name: "to-vip", Enabled: true, IntervalMinutes: 5,
		SourceGroups: []string{"default"}, TargetGroup: "vip"}

	gate := newGateGateway(na, 8)
	svc.gw = gate

	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	go func() {
		defer wg.Done()
		<-start
		_, _ = svc.ApplyRules([]Rule{rule})
	}()
	go func() {
		defer wg.Done()
		<-start
		_ = svc.SetUserGroup(8, "gold")
	}()
	close(start)
	wg.Wait()

	// Both paths are legitimate writers, so the final group depends on which
	// ran first (the rule only fires from the source group; the operator's
	// explicit change always applies). What must hold is mutual exclusion:
	// the two read-decide-write sequences never overlap.
	gate.mu.Lock()
	peak := gate.maxWaiting
	gate.mu.Unlock()
	if peak >= 2 {
		t.Fatalf("a rule run and a manual change read the account concurrently (peak %d)", peak)
	}
	final := na.users()[8].Group
	if final != "vip" && final != "gold" {
		t.Fatalf("final group = %q, want vip or gold", final)
	}
}
