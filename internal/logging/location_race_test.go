package logging

import (
	"sync"
	"testing"
	"time"
)

// The location is written once at start-up while the service is already running:
// the HTTP handlers (Query), the maintenance loop (Cleanup) and the writer can
// all touch it concurrently. Every read must go through the same lock as the
// write, or `go test -race` reports a data race on a real deployment's startup.
//
// This test drives all four paths at once; run it with -race.
func TestLocationIsRaceFree(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// The writer: records go out continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.Logger().Info("record")
			}
		}
	}()

	// The panel: queries the day.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := m.Query(Query{}); err != nil {
					t.Errorf("Query: %v", err)
					return
				}
			}
		}
	}()

	// The maintenance loop: sweeps retention.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := m.Cleanup(7); err != nil {
					t.Errorf("Cleanup: %v", err)
					return
				}
			}
		}
	}()

	// The start-up path: installs the operator's timezone.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			m.SetLocation(loc)
		}
	}()

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
	_ = dir
}

// A nil location is ignored rather than clearing the calendar: a failed settings
// read must not make the day boundary undefined.
func TestSetLocationIgnoresNil(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)
	before := m.Location()
	m.SetLocation(nil)
	if m.Location() != before {
		t.Error("SetLocation(nil) replaced the calendar")
	}
}
