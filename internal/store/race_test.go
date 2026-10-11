package store

import (
	"path/filepath"
	"sync"
	"testing"
)

// Exactly one concurrent caller may claim a (qq, date): two messages racing on
// the same day must not both report themselves as the one that recorded it.
func TestRecordMessageCheckinClaimsOnceUnderConcurrency(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const n = 200
	var wg sync.WaitGroup
	claims := make([]bool, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := s.RecordMessageCheckin(Checkin{QQ: 54321, Date: "2026-10-10", GroupID: 1})
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			claims[i] = ok
		}(i)
	}
	close(start)
	wg.Wait()

	got := 0
	for _, ok := range claims {
		if ok {
			got++
		}
	}
	if got != 1 {
		t.Errorf("want exactly 1 claim, got %d (%v)", got, claims)
	}
}
