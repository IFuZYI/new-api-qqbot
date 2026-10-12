package service

import (
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// The already-bound guard must stop the binding path BEFORE the gateway is
// asked: no call at all, not just no write.
//
// The invariant "none of the binding paths may touch a member who already has a
// binding" is enforced twice — bindQQ short-circuits, and
// store.InsertBindingIfAbsent's ON CONFLICT DO NOTHING makes a concurrent loser
// a no-op — so the end state is identical with the service-level guard removed.
// An independent reviewer removed it and both binding tests stayed green.
//
// What distinguishes the two is the TRAFFIC: with the guard, a member who
// already has a binding costs zero gateway calls. That matters against a gateway
// that rate-limits and caps sessions, and it is the half of the invariant the
// state-only assertions cannot see.
func TestAnAlreadyBoundMemberCostsNoGatewayCall(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	if err := st.SetBinding(store.Binding{
		QQ: 54321, NewAPIUserID: 99, NewAPIUsername: "handpicked",
		NewAPIEmail: "someone@example.com", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	// An automatic match exists, so a search WOULD find something.
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}

	before := na.findCalls
	if err := svc.BindMember(1129535731, 54321); err != nil {
		t.Fatalf("BindMember: %v", err)
	}
	if got := na.findCalls - before; got != 0 {
		t.Errorf("an already-bound member cost %d gateway searches, want 0 (the guard must short-circuit before the call)", got)
	}

	// The command path and the patrol path share bindQQ, so one check per entry
	// point: the patrol must also skip.
	before = na.findCalls
	if _, err := svc.AutoBind(); err != nil {
		t.Fatalf("AutoBind: %v", err)
	}
	if got := na.findCalls - before; got != 0 {
		t.Errorf("the patrol searched for an already-bound member %d times, want 0", got)
	}
}
