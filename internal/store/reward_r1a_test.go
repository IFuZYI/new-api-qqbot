package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestR1MigrateLegacyRewardWithoutClaimingConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the old table, before reward_drawn/state columns existed.
	if err := s.db.Exec("DROP TABLE checkins").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(`CREATE TABLE checkins (qq integer, date text, group_id integer, signed_at integer, rank integer, reward_quota integer, rewarded_at integer, source text, PRIMARY KEY (qq,date))`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(`INSERT INTO checkins VALUES (1,'2026-10-09',1,0,0,77,123,'board'),(2,'2026-10-09',1,0,0,0,0,'message'),(3,'2026-10-09',1,0,0,0,123,'board')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, qq := range []int64{1, 3} {
			c, err := s.GetCheckin(qq, "2026-10-09")
			if err != nil {
				t.Fatal(err)
			}
			if !c.RewardDrawn || c.RewardState != "legacy" || c.RewardedAt != 123 {
				t.Errorf("legacy claim misrepresented: %+v", c)
			}
			if _, ok, err := s.ClaimPendingReward(qq, c.Date, 999, time.Now()); err != nil || ok {
				t.Fatalf("legacy row reclaimed: %v %v", ok, err)
			}
		}
		c, err := s.GetCheckin(2, "2026-10-09")
		if err != nil {
			t.Fatal(err)
		}
		if c.RewardDrawn || c.RewardState != "pending" {
			t.Errorf("legacy pending changed: %+v", c)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestR1PendingClaimRetainsZeroAndHasOneWinner(t *testing.T) {
	s := newTestStore(t)
	date := "2026-10-09"
	if err := s.RecordCheckin(Checkin{QQ: 1, Date: date}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.ClaimPendingReward(1, date, 0, time.Now())
	if err != nil || !ok || !c.RewardDrawn {
		t.Fatalf("zero draw: %+v %v %v", c, ok, err)
	}
	if err := s.FinishReward(1, date, "pending", "rejected", time.Now()); err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	winners := make(chan *Checkin, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			c, ok, err := s.ClaimPendingReward(1, date, 999, time.Now())
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners <- c
			}
		}()
	}
	close(start)
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("winners: %d", len(winners))
	}
	for c := range winners {
		if c.RewardQuota != 0 || !c.RewardDrawn {
			t.Fatalf("zero was redrawn: %+v", c)
		}
	}
	if err := s.FinishReward(1, date, "paid", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimPendingReward(1, date, 999, time.Now()); err != nil || ok {
		t.Fatalf("paid reclaimed: %v %v", ok, err)
	}
}
