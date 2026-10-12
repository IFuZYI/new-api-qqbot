package service

import (
	"testing"

	"qqbot/internal/newapi"
)

// A batch group change must read each account ONCE, not twice.
//
// SetUserGroups read every id with gw.GetUser for the username and then again
// inside setUserGroupLocked, so a batch of N accounts spent 2N gateway reads —
// against a gateway that rate-limits and caps sessions, in the very file that is
// otherwise careful about both. Found by an independent reviewer.
func TestABatchGroupChangeReadsEachAccountOnce(t *testing.T) {
	svc, _, na, _ := newTestService(t)
	na.users()[1] = &newapi.User{ID: 1, Username: "alpha", Group: "default"}
	na.users()[2] = &newapi.User{ID: 2, Username: "beta", Group: "default"}

	before := na.getCalls
	if _, err := svc.SetUserGroups([]int{1, 2}, "vip"); err != nil {
		t.Fatal(err)
	}
	reads := na.getCalls - before
	if reads != 2 {
		t.Errorf("a 2-account batch made %d account reads, want 2 (one per account; two per account means the read is duplicated)", reads)
	}
	// And the write still landed, so the optimisation did not skip it.
	if got := na.users()[1].Group; got != "vip" {
		t.Errorf("account 1 group = %q, want vip", got)
	}
	if got := na.users()[2].Group; got != "vip" {
		t.Errorf("account 2 group = %q, want vip", got)
	}
}

// The single-change path still reads the account itself: it has no caller that
// read it already, and the re-read is what stops a sparse PUT blanking
// display_name.
func TestASingleGroupChangeStillReadsTheAccount(t *testing.T) {
	svc, _, na, _ := newTestService(t)
	na.users()[1] = &newapi.User{ID: 1, Username: "alpha", DisplayName: "爱丽丝", Group: "default"}

	before := na.getCalls
	if err := svc.SetUserGroup(1, "vip"); err != nil {
		t.Fatal(err)
	}
	if reads := na.getCalls - before; reads != 1 {
		t.Errorf("a single change made %d reads, want 1", reads)
	}
	if got := na.users()[1].DisplayName; got != "爱丽丝" {
		t.Errorf("display_name was blanked: %q", got)
	}
}
