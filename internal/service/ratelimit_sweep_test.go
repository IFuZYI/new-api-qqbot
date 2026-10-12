package service

import (
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// A gateway that rate-limits us must stop a sweep, not be retried once per
// member/group.
//
// The lockout covers logins only, so a 429 (or the 50-session cap) returned by a
// DATA endpoint used to be retried for every remaining item: measured, four
// ListUsers calls against a 429 produced four network attempts, and a roster
// sweep kept walking groups. Each attempt counts against the gateway's limiter,
// so the window never closes — the same failure the login lockout exists to
// prevent.
func TestRateLimitedSweepStopsInsteadOfRetryingEveryItem(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	// Three enabled groups, each with a member: without the guard the sweep
	// would call the gateway once per member.
	for _, id := range []int64{1, 2, 3} {
		if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: id, GroupName: "g"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetGroupEnabled(id, true); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertMembers(id, []store.Member{{QQ: 100 + id, Nickname: "m"}}); err != nil {
			t.Fatal(err)
		}
	}
	na.failFind = &newapi.RateLimitError{Method: "GET", Path: "/api/user/search"}

	if _, err := svc.AutoBind(); err != nil {
		t.Fatalf("AutoBind must report the rate limit, not fail the sweep: %v", err)
	}
	if na.findCalls != 1 {
		t.Errorf("the gateway was called %d times; a rate limit must stop the sweep at the first refusal", na.findCalls)
	}
	_ = nc
}

// A per-member rate limit stops the sweep even when the failure comes from a
// member further down the list: the first refusal is the signal.
func TestRateLimitOnALaterMemberStillStopsTheSweep(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	for _, id := range []int64{1, 2, 3} {
		if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: id, GroupName: "g"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetGroupEnabled(id, true); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertMembers(id, []store.Member{{QQ: 100 + id, Nickname: "m"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Fail only the SECOND lookup, so the guard cannot be a "fail fast on the
	// first member" accident.
	na.failFindAfter = 1
	na.failFind = &newapi.RateLimitError{Method: "GET", Path: "/api/user/search"}

	if _, err := svc.AutoBind(); err != nil {
		t.Fatalf("AutoBind must report it: %v", err)
	}
	if na.findCalls != 2 {
		t.Errorf("the gateway was called %d times, want 2 (the second refusal must stop the sweep)", na.findCalls)
	}
}

// The session cap is the same shape: it needs the operator, so retrying it once
// per item only spends the gateway's budget.
func TestSessionLimitStopsASweep(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	for _, id := range []int64{1, 2, 3} {
		if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: id, GroupName: "g"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetGroupEnabled(id, true); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertMembers(id, []store.Member{{QQ: 100 + id, Nickname: "m"}}); err != nil {
			t.Fatal(err)
		}
	}
	na.failFind = &newapi.SessionLimitError{Method: "POST", Path: "/api/user/login"}

	if _, err := svc.AutoBind(); err != nil {
		t.Fatalf("AutoBind must report it: %v", err)
	}
	if na.findCalls != 1 {
		t.Errorf("the gateway was called %d times; the session cap needs the operator, not retries", na.findCalls)
	}
}

// An ordinary per-member failure (no account yet) must NOT stop the sweep: the
// guard is only for the two conditions that get worse with every attempt.
func TestOrdinaryFailuresDoNotStopASweep(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	for _, id := range []int64{1, 2, 3} {
		if err := st.UpsertGroups([]store.MonitoredGroup{{GroupID: id, GroupName: "g"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetGroupEnabled(id, true); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertMembers(id, []store.Member{{QQ: 100 + id, Nickname: "m"}}); err != nil {
			t.Fatal(err)
		}
	}
	na.failFind = errString("gateway 500")

	if _, err := svc.AutoBind(); err != nil {
		t.Fatal(err)
	}
	if na.findCalls != 3 {
		t.Errorf("an ordinary failure stopped the sweep after %d of 3 members", na.findCalls)
	}
}
