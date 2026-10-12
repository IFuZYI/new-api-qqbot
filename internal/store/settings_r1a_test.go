package store

import (
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestR1PutSettingConcurrentFirstWrite(t *testing.T) {
	s := newTestStore(t)
	const n = 16
	// Hold old read-before-create implementations after every caller observed
	// absence. Atomic INSERT ON CONFLICT implementations do not read first.
	var reads sync.WaitGroup
	reads.Add(n)
	if err := s.db.Callback().Query().After("gorm:query").Register("r1a:settings_barrier", func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" {
			reads.Done()
			reads.Wait()
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(v int) { <-start; done <- s.PutSetting("r1a-first", map[string]int{"value": v}) }(i)
	}
	close(start)
	for i := 0; i < n; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("concurrent upsert: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("upserts did not finish")
		}
	}
	if err := s.db.Callback().Query().Remove("r1a:settings_barrier"); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := s.GetSetting("r1a-first", &got); err != nil {
		t.Fatal(err)
	}
	if got["value"] < 0 || got["value"] >= n {
		t.Fatalf("invalid final value: %v", got)
	}
	if err := s.PutSetting("r1a-first", map[string]int{"value": 99}); err != nil {
		t.Fatal(err)
	}
	if err := s.GetSetting("r1a-first", &got); err != nil || got["value"] != 99 {
		t.Fatalf("replacement: %v %v", got, err)
	}
	var rows int64
	if err := s.db.Model(&Setting{}).Where("key = ?", "r1a-first").Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("row count %d: %v", rows, err)
	}
}
