package service

import (
	"errors"
	"qqbot/internal/store"
	"testing"
)

func r1SeedReward(t *testing.T, svc *Service, date string, amount int) {
	t.Helper()
	cfg := DefaultSettings()
	cfg.RewardMin = amount
	cfg.RewardMax = amount
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.SetBinding(store.Binding{QQ: 54321, NewAPIUserID: 7, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.RecordCheckin(store.Checkin{QQ: 54321, Date: date, GroupID: 1}); err != nil {
		t.Fatal(err)
	}
}
func TestR1ZeroRewardIsPaidWithoutGateway(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	date := "2026-10-10"
	r1SeedReward(t, svc, date, 0)
	na.failQuota = errors.New("zero must not reach gateway")
	for i := 0; i < 20; i++ {
		got, err := svc.Award(date)
		if err != nil || got.Failed != 0 {
			t.Fatalf("sweep: %+v %v", got, err)
		}
		if (i == 0 && got.Awarded != 1) || (i > 0 && got.Awarded != 0) {
			t.Fatalf("duplicate effect: %+v", got)
		}
	}
	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if !c.RewardDrawn || c.RewardQuota != 0 || c.RewardedAt == 0 || c.RewardState != "paid" {
		t.Fatalf("zero not paid: %+v", c)
	}
	if len(na.quotaAdds) != 0 {
		t.Fatalf("unexpected gateway calls: %+v", na.quotaAdds)
	}
}

func TestR1RewardHistoricalPendingUsesOriginalDate(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	old := "2026-10-09"
	today := "2026-10-10"
	future := "2026-10-11"
	r1SeedReward(t, svc, old, 77)
	na.failQuota = errors.New("rejected")
	if got, err := svc.Award(old); err != nil || got.Failed != 1 {
		t.Fatalf("first: %+v %v", got, err)
	}
	cfg := DefaultSettings()
	cfg.RewardMin = 999
	cfg.RewardMax = 999
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{today, future} {
		if err := st.RecordCheckin(store.Checkin{QQ: 54321, Date: d}); err != nil {
			t.Fatal(err)
		}
	}
	na.failQuota = nil
	got, err := svc.Award(today)
	if err != nil || got.Awarded != 2 {
		t.Fatalf("historical sweep: %+v %v", got, err)
	}
	for _, d := range []string{old, today, future} {
		c, err := st.GetCheckin(54321, d)
		if err != nil {
			t.Fatal(err)
		}
		if d == future {
			if c.RewardDrawn || c.RewardState != "pending" {
				t.Fatalf("future touched: %+v", c)
			}
			continue
		}
		want := 999
		if d == old {
			want = 77
		}
		if c.RewardState != "paid" || c.RewardedAt == 0 || c.RewardQuota != want {
			t.Fatalf("wrong reward date: %+v", c)
		}
	}
	if len(na.quotaAdds) != 2 || na.quotaAdds[0].value != 77 || na.quotaAdds[1].value != 999 {
		t.Fatalf("credits: %+v", na.quotaAdds)
	}
	got, err = svc.Award(today)
	if err != nil || got.Awarded != 0 {
		t.Fatalf("repeat: %+v %v", got, err)
	}
}

func TestR1RewardRetryKeepsFirstDraw(t *testing.T) {
	svc, st, na, _ := newTestService(t)
	date := "2026-10-10"
	r1SeedReward(t, svc, date, 77)
	na.failQuota = errors.New("explicit rejection")
	first, err := svc.Award(date)
	if err != nil || first.Failed != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	c, err := st.GetCheckin(54321, date)
	if err != nil {
		t.Fatal(err)
	}
	if c.RewardQuota != 77 {
		t.Fatalf("failed attempt lost first draw: %+v", c)
	}
	cfg := DefaultSettings()
	cfg.RewardMin = 999
	cfg.RewardMax = 999
	if err := svc.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	na.failQuota = nil
	second, err := svc.Award(date)
	if err != nil || second.Awarded != 1 {
		t.Fatalf("retry: %+v %v", second, err)
	}
	if len(na.quotaAdds) != 1 || na.quotaAdds[0].value != 77 {
		t.Fatalf("retry redrew: %+v", na.quotaAdds)
	}
}
