package service

import (
	"testing"
	"time"

	"qqbot/internal/napcat"
	"qqbot/internal/store"
)

// The bind trigger and the patrol must resolve the same member to the same
// QQ-side context, or a rule can fire on bind and then disagree on the next
// sweep (or vice versa). The comment on memberContext has always claimed this;
// these pin it.
//
// A member in several monitored groups is the case that breaks: each group
// reports its own 群等级 / QQ 等级 / 入群天数, and the two paths have to pick
// them the same way.
// newTestCache builds a loaded roster cache, as a sweep would.
func newTestCache(t *testing.T, svc *Service, st *store.Store) *qqDataCache {
	t.Helper()
	groups, err := st.EnabledGroups()
	if err != nil {
		t.Fatal(err)
	}
	c := newQQDataCache(st, svc.qq)
	if err := c.load(groups); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBindAndPatrolResolveMemberContextIdentically(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	enableTestGroup(t, st, 1)
	enableTestGroup(t, st, 2)

	now := time.Now().Unix()
	// Group 1: high QQ level, joined long ago, low group level.
	nc.members[1] = []napcat.GroupMember{
		{QQ: 54321, Nickname: "a", Level: 2, QQLevel: 10, JoinTime: now - 30*86400},
	}
	// Group 2: low QQ level, joined yesterday, high group level.
	nc.members[2] = []napcat.GroupMember{
		{QQ: 54321, Nickname: "a", Level: 7, QQLevel: 3, JoinTime: now - 1*86400},
	}

	byPatrol, err := svc.rosterByQQ()
	if err != nil {
		t.Fatalf("rosterByQQ: %v", err)
	}
	fromPatrol, ok := byPatrol[54321]
	if !ok {
		t.Fatal("the patrol found no context for the member")
	}

	fromBind, err := svc.memberContext(54321, newTestUser(7, "default"), true, newTestCache(t, svc, st))
	if err != nil {
		t.Fatalf("memberContext: %v", err)
	}

	if fromPatrol.HasQQData != fromBind.HasQQData {
		t.Errorf("HasQQData differs: patrol=%v bind=%v", fromPatrol.HasQQData, fromBind.HasQQData)
	}
	if fromPatrol.Level != fromBind.Level {
		t.Errorf("群等级 differs: patrol=%d bind=%d", fromPatrol.Level, fromBind.Level)
	}
	if fromPatrol.QQLevel != fromBind.QQLevel {
		t.Errorf("QQ 等级 differs: patrol=%d bind=%d", fromPatrol.QQLevel, fromBind.QQLevel)
	}
	if fromPatrol.JoinDays != fromBind.JoinDays {
		t.Errorf("入群天数 differs: patrol=%d bind=%d", fromPatrol.JoinDays, fromBind.JoinDays)
	}
}

// The resolution itself: the highest value seen wins for every field, so a rule
// does not depend on the order the groups happen to be read in.
func TestMultiGroupContextTakesTheHighestPerField(t *testing.T) {
	svc, st, _, nc := newTestService(t)
	enableTestGroup(t, st, 1)
	enableTestGroup(t, st, 2)

	now := time.Now().Unix()
	nc.members[1] = []napcat.GroupMember{
		{QQ: 54321, Nickname: "a", Level: 2, QQLevel: 10, JoinTime: now - 30*86400},
	}
	nc.members[2] = []napcat.GroupMember{
		{QQ: 54321, Nickname: "a", Level: 7, QQLevel: 3, JoinTime: now - 1*86400},
	}

	byPatrol, err := svc.rosterByQQ()
	if err != nil {
		t.Fatal(err)
	}
	got := byPatrol[54321]
	if got.Level != 7 {
		t.Errorf("群等级 = %d, want the highest (7)", got.Level)
	}
	if got.QQLevel != 10 {
		t.Errorf("QQ 等级 = %d, want the highest (10)", got.QQLevel)
	}
	if got.JoinDays != 30 {
		t.Errorf("入群天数 = %d, want the longest (30)", got.JoinDays)
	}
}

// Binding a batch of members must not read the rosters once per member: the
// roster is the same for all of them within one sweep, so it is read once per
// monitored group. This is the traffic half of the modular split.
func TestBindSweepReadsEachRosterOnce(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	enableTestGroup(t, st, 1)
	enableTestGroup(t, st, 2)

	// Three members, each with an account, all in both groups.
	members := []store.Member{{QQ: 111, Nickname: "a"}, {QQ: 222, Nickname: "b"}, {QQ: 333, Nickname: "c"}}
	if err := st.UpsertMembers(1, members); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMembers(2, members); err != nil {
		t.Fatal(err)
	}
	for i, qq := range []string{"111", "222", "333"} {
		// A distinct account each: sharing one id would make the first bind move
		// it to the target group and the rest skip as "already there".
		na.byEmail[qq+"@qq.com"] = newTestUser(7+i, "default")
	}
	nc.members[1] = []napcat.GroupMember{{QQ: 111, Level: 5}, {QQ: 222, Level: 5}, {QQ: 333, Level: 5}}
	nc.members[2] = []napcat.GroupMember{{QQ: 111, Level: 5}, {QQ: 222, Level: 5}, {QQ: 333, Level: 5}}

	// A rule with a QQ-side condition, so the roster is genuinely needed.
	if _, err := svc.CreateRule(Rule{
		Name: "level → vip", Enabled: true, OnBind: true,
		TargetGroup: "vip",
		Conditions:  []Condition{{ConditionType: CondGroupLevel, Op: OpGTE, Value: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.AutoBind()
	if err != nil {
		t.Fatal(err)
	}
	if res.Bound != 3 {
		t.Fatalf("want 3 bindings, got %+v", res)
	}

	// Two groups, so at most two reads - not three members times two groups.
	if got := nc.memberListCalls(); got > 2 {
		t.Errorf("the bind sweep read rosters %d times for 3 members in 2 groups; want at most 2", got)
	}
	// And the rule still fired for every member.
	if len(na.groupSets) != 3 {
		t.Errorf("want 3 promotions, got %+v", na.groupSets)
	}
}
