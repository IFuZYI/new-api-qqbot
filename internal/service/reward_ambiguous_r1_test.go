package service

import (
	"errors"
	"testing"

	"qqbot/internal/newapi"
	"qqbot/internal/store"
)

// An uncertain gateway outcome (timeout / unreadable acknowledgement) may
// already have credited the account upstream. The claim must stay held so the
// next sweep does not pay again, and the row must be marked for reconciliation
// rather than looking like an ordinary retryable failure.
func TestR1AmbiguousRewardStopsAutomaticRetry(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	date := "2026-10-10"
	r1SeedReward(t, svc, date, 77)

	na.failQuota = &newapi.AmbiguousWriteError{Err: errors.New("read response: unexpected EOF")}
	first, err := svc.Award(date)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if first.Ambiguous != 1 || first.Failed != 0 {
		t.Fatalf("first sweep: %+v", first)
	}
	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardState != "ambiguous" {
		t.Fatalf("uncertain outcome not flagged for reconciliation: %+v", c)
	}
	if c.RewardedAt != 0 {
		t.Fatalf("uncertain outcome must not read as paid: %+v", c)
	}
	if c.RewardQuota != 77 {
		t.Fatalf("first draw lost: %+v", c)
	}

	// A later sweep must not retry it, even after the gateway recovers.
	na.failQuota = nil
	for i := 0; i < 3; i++ {
		again, err := svc.Award(date)
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if again.Awarded != 0 || again.Failed != 0 {
			t.Fatalf("ambiguous reward was retried: %+v", again)
		}
	}
	if len(na.quotaAdds) != 0 {
		t.Fatalf("gateway was called for an ambiguous claim: %+v", na.quotaAdds)
	}
}

// A definite refusal is not an uncertain outcome: it stays retryable, and the
// retry keeps the originally drawn amount.
func TestR1DefiniteRefusalStillRetries(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	date := "2026-10-10"
	r1SeedReward(t, svc, date, 77)

	na.failQuota = errors.New("insufficient permission")
	if got, err := svc.Award(date); err != nil || got.Failed != 1 {
		t.Fatalf("first: %+v %v", got, err)
	}
	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardState != "pending" {
		t.Fatalf("definite refusal must stay retryable: %+v", c)
	}
	na.failQuota = nil
	got, err := svc.Award(date)
	if err != nil || got.Awarded != 1 {
		t.Fatalf("retry: %+v %v", got, err)
	}
	if len(na.quotaAdds) != 1 || na.quotaAdds[0].value != 77 {
		t.Fatalf("retry redrew or double-paid: %+v", na.quotaAdds)
	}
	_ = store.Checkin{}
}
