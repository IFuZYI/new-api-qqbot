package service

import (
	"strings"
	"testing"

	"qqbot/internal/store"
)

// A Service built without a gateway must fail its calls with an error, not panic.
//
// `New` accepts nil interfaces (runtime_test.go and several other call sites do
// exactly that), and the bind path dereferences the gateway without a guard, so
// an event-driven bind on such a Service crashed the process with a nil-pointer
// panic instead of reporting "not configured". The panic surfaced while writing
// the listener→store integration test; production wires a real gateway, but the
// latent trap is one `service.New(st, nil, nil)` away from a live crash.
func TestNilGatewayFailsInsteadOfPanicking(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/nil.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st, nil, nil)

	// Every gateway-facing entry point must return an error, not panic.
	if _, _, err := svc.bindQQ(54321, "join", nil); err == nil {
		t.Error("bindQQ with no gateway returned no error")
	} else if !strings.Contains(err.Error(), "未配置") {
		t.Errorf("the error should say the gateway is not configured: %v", err)
	}

	if _, err := svc.AutoBind(); err != nil {
		t.Errorf("AutoBind with no gateway must report a per-member failure, not fail the sweep: %v", err)
	}

	// The nil QQ client is equally reachable (several tests pass nil). A group
	// must be ENABLED for the sweep to touch it at all, which is the invariant
	// that keeps an unrelated group out of the roster read.
	if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: 1, GroupName: "g"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(1, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncAllMembers(); err == nil {
		t.Error("SyncAllMembers with no QQ client returned no error")
	}
	// And with nothing enabled it is a no-op, not an error.
	if err := st.SetGroupEnabled(1, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncAllMembers(); err != nil {
		t.Errorf("a disabled group must make the sweep a no-op: %v", err)
	}
}

// A nil QQ client must not panic the roster or board reads either.
func TestNilQQClientFailsInsteadOfPanicking(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/nilqq.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st, nil, nil)

	if err := svc.SyncAllCheckins("2026-10-12"); err != nil {
		t.Errorf("SyncAllCheckins with no QQ client must not fail the whole sweep: %v", err)
	}
}
