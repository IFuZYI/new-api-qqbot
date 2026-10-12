package service

import (
	"errors"
	"testing"

	"qqbot/internal/store"
)

// groupsFailingGateway fails the group-name read, which is what the rule form
// depends on: the handler answers 502 when it errors, so a swallowed error
// would show an empty group list and the operator would type a name by hand.
type groupsFailingGateway struct {
	*fakeNewAPI
}

func (groupsFailingGateway) Groups() (map[string]struct{}, error) {
	return nil, errors.New("gateway unreachable")
}

// NewAPIGroups hands the rule form the gateway's own group names. They must be
// the gateway's, and a failure to read them must not read as "no groups
// configured".
func TestNewAPIGroupsReturnsGatewayGroups(t *testing.T) {
	svc, _, na, _ := newTestService(t)
	na.byEmail = nil

	groups, err := svc.NewAPIGroups()
	if err != nil {
		t.Fatalf("NewAPIGroups: %v", err)
	}
	want := map[string]struct{}{"default": {}, "authorized": {}, "vip": {}}
	if len(groups) != len(want) {
		t.Fatalf("groups = %v, want %v", groups, want)
	}
	for name := range want {
		if _, ok := groups[name]; !ok {
			t.Errorf("group %q missing from %v", name, groups)
		}
	}

	broken := New(mustStore(t), groupsFailingGateway{newFakeNewAPI()}, newFakeNapCat())
	if groups, err := broken.NewAPIGroups(); err == nil || groups != nil {
		t.Fatalf("a gateway failure must be reported, not shown as an empty list: %v %v", groups, err)
	}
}

// mustStore opens a throwaway store for a test.
func mustStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/gates.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// BindOnJoinEnabled is the switch that gates the whole join path: when it is
// off the listener returns before BindMember, so a false here is what keeps the
// bot from binding someone who merely walked into a group. It must follow the
// stored switch exactly, and fail closed if the settings cannot be read.
func TestBindOnJoinSwitchGatesTheJoinPath(t *testing.T) {
	svc, st, _, _ := newTestService(t)

	if !svc.BindOnJoinEnabled() {
		t.Fatal("the join path defaults to on")
	}

	cfg, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BindOnJoinEnabled = false
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if svc.BindOnJoinEnabled() {
		t.Fatal("the switch is off, so the join path must not bind")
	}

	cfg.BindOnJoinEnabled = true
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if !svc.BindOnJoinEnabled() {
		t.Fatal("the switch is on, so the join path must bind")
	}

	// An unreadable configuration must not enable binding.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if svc.BindOnJoinEnabled() {
		t.Fatal("an unreadable configuration must fail closed")
	}
}

// IsMonitoredGroup gates whether the bot acts on a group at all. A newly
// discovered group is stored disabled, so "unknown group" must not read as
// monitored - otherwise inviting the bot somewhere would start recording and
// paying that group's members.
func TestIsMonitoredGroupRejectsUnknownAndDisabled(t *testing.T) {
	svc, st, _, _ := newTestService(t)

	if err := st.UpsertGroups([]store.MonitoredGroup{
		{GroupID: 100, GroupName: "newly discovered", MemberCount: 3},
		{GroupID: 200, GroupName: "enabled", MemberCount: 5},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupEnabled(200, true); err != nil {
		t.Fatal(err)
	}

	if ok, err := svc.IsMonitoredGroup(200); err != nil || !ok {
		t.Errorf("an enabled group must be monitored: ok=%v err=%v", ok, err)
	}
	if ok, err := svc.IsMonitoredGroup(100); err != nil || ok {
		t.Errorf("a disabled group must not be monitored: ok=%v err=%v", ok, err)
	}
	if ok, err := svc.IsMonitoredGroup(999); err != nil || ok {
		t.Errorf("an unknown group must not be monitored: ok=%v err=%v", ok, err)
	}

	// Once the operator enables it, it is monitored; the switch is the only
	// thing that decides.
	if err := st.SetGroupEnabled(100, true); err != nil {
		t.Fatal(err)
	}
	if ok, err := svc.IsMonitoredGroup(100); err != nil || !ok {
		t.Errorf("a newly enabled group must be monitored: ok=%v err=%v", ok, err)
	}

	// A store that cannot answer must report the failure rather than the safe
	// default: the caller logs the error and treats the group as unmonitored,
	// but it must be able to tell the two apart.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if ok, err := svc.IsMonitoredGroup(100); err == nil || ok {
		t.Errorf("an unreadable store must be reported: ok=%v err=%v", ok, err)
	}
}
