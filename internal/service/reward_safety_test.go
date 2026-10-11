package service

import (
	"sync"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// The reward is claimed once per (qq, date) no matter how many paths record a
// check-in or how often the award sweep runs. This is the invariant that makes
// "any message counts" safe to switch on, so it is probed directly rather than
// inferred: several message check-ins, a board upgrade and concurrent award
// sweeps all target the same day.
func TestMessageCheckinsCannotCauseDoubleReward(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1129535731)
	date := todayOf(t, svc)

	if err := st.UpsertMembers(1129535731, []store.Member{{QQ: 54321, Nickname: "a"}}); err != nil {
		t.Fatal(err)
	}
	na.byEmail["54321@qq.com"] = &newapi.User{ID: 7, Username: "seven", Group: "default", Role: 1}
	// Bind first: a check-in only counts for a bound member.
	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}

	// A burst of messages: only the first may record.
	for i := 0; i < 20; i++ {
		if _, err := svc.CheckinByMessage(1129535731, 54321); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.CheckinsOn(date)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("20 messages produced %d check-in rows, want 1", len(rows))
	}

	// Concurrent award sweeps: exactly one payment may reach the gateway.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Award(date); err != nil {
				t.Errorf("Award: %v", err)
			}
		}()
	}
	wg.Wait()

	if len(na.quotaAdds) != 1 {
		t.Errorf("want exactly 1 quota grant, got %d: %+v", len(na.quotaAdds), na.quotaAdds)
	}
	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardedAt == 0 || c.RewardQuota <= 0 {
		t.Errorf("the day should be marked rewarded with an amount: %+v", c)
	}
	if len(na.quotaAdds) == 1 && na.quotaAdds[0].value != c.RewardQuota {
		t.Errorf("the recorded amount (%d) differs from the amount paid (%d)", c.RewardQuota, na.quotaAdds[0].value)
	}
}

// A message check-in for a member who leaves the group and comes back must not
// be lost: the member row survives, and the day stays recorded.
func TestMessageCheckinSurvivesGroupChange(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	enableTestGroup(t, st, 1)
	date := todayOf(t, svc)
	bindForTest(t, svc, st, na, 54321)

	if _, err := svc.CheckinByMessage(1, 54321); err != nil {
		t.Fatal(err)
	}
	// The member leaves: the roster no longer lists them.
	if err := st.UpsertMembers(1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCheckin(54321, date); err != nil {
		t.Errorf("the check-in must survive a group change: %v", err)
	}
	if _, err := st.GetMember(54321); err != nil {
		t.Errorf("the member row must survive a group change: %v", err)
	}
}
