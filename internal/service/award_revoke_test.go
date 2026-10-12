package service

import (
	"errors"
	"net/http"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// authFailure builds the error the gateway client produces for a revoked
// session (an HTTPError, not an AmbiguousWriteError), so the test exercises the
// real classification: a definite refusal, which releases the claim.
func authFailure() error {
	return &newapi.HTTPError{Method: "POST", Path: "/api/user/manage",
		StatusCode: http.StatusUnauthorized, Code: "AUTH_SESSION_REVOKED", Message: "登录状态已失效"}
}

// A revoked session in the middle of a payment sweep must leave the claim held
// with its drawn amount, and the NEXT sweep must pay it.
//
// The claim is persisted before the gateway call (so a crash cannot double-pay)
// and FinishReward only runs after a definite answer, so the row survives a
// failed call. What was never pinned is the behaviour the panel documents: the
// amount must not be re-drawn, and the next sweep — which finds the row still
// pending — must complete the payment rather than skipping it as "already
// claimed".
//
// This is the deliberate trade recorded for the reward path: never retry inside
// the same sweep (a retry could double-pay when the first call actually landed),
// but do pay on the next one, because the claim is only a reservation.
func TestRevokeMidAwardKeepsTheClaimAndTheNextSweepPays(t *testing.T) {
	svc, st, na, nc := newTestService(t)
	today, err := svc.Today()
	if err != nil {
		t.Fatal(err)
	}
	// A bound member who has checked in, waiting for the reward.
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordMessageCheckin(store.Checkin{QQ: 54321, Date: today, GroupID: 1}); err != nil {
		t.Fatal(err)
	}
	_ = nc

	// The gateway refuses the payment: the session was revoked mid-sweep. An
	// auth failure is NOT an ambiguous write, so the claim returns to pending
	// for the next sweep (rather than being held for reconciliation).
	na.failQuota = authFailure()
	res, err := svc.Award(today)
	if err != nil {
		t.Fatalf("Award: %v", err)
	}
	if res.Failed != 1 || res.Awarded != 0 {
		t.Errorf("result = %+v, want one failure and no award", res)
	}

	// The row must still be pending, with the amount it first drew.
	row, err := st.GetCheckin(54321, today)
	if err != nil {
		t.Fatal(err)
	}
	if row.RewardState != "pending" {
		t.Errorf("reward_state = %q, want pending (a definite refusal releases the claim)", row.RewardState)
	}
	if row.RewardQuota == 0 {
		t.Fatal("the drawn amount was lost; the next sweep would re-draw and could pay a different one")
	}
	first := row.RewardQuota

	// The session recovers; the next sweep must pay the SAME amount.
	na.failQuota = nil
	res2, err := svc.Award(today)
	if err != nil {
		t.Fatalf("second Award: %v", err)
	}
	if res2.Awarded != 1 {
		t.Errorf("the second sweep did not pay the held claim: %+v", res2)
	}
	row2, err := st.GetCheckin(54321, today)
	if err != nil {
		t.Fatal(err)
	}
	if row2.RewardState != "paid" {
		t.Errorf("reward_state = %q, want paid", row2.RewardState)
	}
	if row2.RewardQuota != first {
		t.Errorf("the amount changed across the retry: %d -> %d (it must be drawn once)", first, row2.RewardQuota)
	}
	// And it must be paid exactly once: a third sweep finds nothing pending.
	res3, err := svc.Award(today)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Awarded != 0 {
		t.Errorf("a paid reward was paid again: %+v", res3)
	}
}

// An unreadable acknowledgement is the opposite case: the write may have landed,
// so the claim must NOT be released — it is held for reconciliation and never
// retried automatically.
func TestAmbiguousWriteHoldsTheClaimForReconciliation(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	today, _ := svc.Today()
	if err := st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordMessageCheckin(store.Checkin{QQ: 54321, Date: today, GroupID: 1}); err != nil {
		t.Fatal(err)
	}

	na.failQuota = &newapi.AmbiguousWriteError{Err: errors.New("read response: unexpected EOF")}
	res, err := svc.Award(today)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ambiguous != 1 {
		t.Errorf("result = %+v, want one ambiguous", res)
	}

	row, err := st.GetCheckin(54321, today)
	if err != nil {
		t.Fatal(err)
	}
	if row.RewardState != "ambiguous" {
		t.Errorf("reward_state = %q, want ambiguous", row.RewardState)
	}
	if row.RewardQuota == 0 {
		t.Error("the amount was not preserved for reconciliation")
	}

	// A later sweep must not pay it: the money may already have moved.
	res2, err := svc.Award(today)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Awarded != 0 {
		t.Errorf("an ambiguous reward was paid automatically: %+v", res2)
	}
}
