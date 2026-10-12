package store

import (
	"path/filepath"
	"testing"
	"time"
)

// A deferred read-then-write transaction must not be killable by
// SQLITE_BUSY_SNAPSHOT, which `busy_timeout` does not cover.
//
// The store's own transactions have this shape (BumpSettingInt,
// ClaimPendingReward): BEGIN, SELECT a row, then UPDATE it. SQLite's default
// BEGIN is DEFERRED, so the SELECT takes a read snapshot and the UPDATE tries to
// upgrade it. If another connection commits in between, the snapshot is stale and
// SQLite returns SQLITE_BUSY_SNAPSHOT *immediately* — it is not waiting for a
// lock, it is being told to roll back, so the 10s busy_timeout never applies. An
// independent reviewer measured the second writer failing after 0.40s.
//
// `_txlock=immediate` makes BEGIN take the write lock up front, so the two
// writers queue on busy_timeout instead of one being killed by a stale snapshot.
//
// The interleaving is forced, not raced: B opens its transaction and reads
// (taking the snapshot), then A writes and commits. Without the fix A commits
// while B holds only a read snapshot, and B's own write is refused; with it, B
// holds the write lock, A waits, and both succeed.
func TestAStaleSnapshotCannotKillTheSecondWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })

	if err := a.PutSetting("counter", map[string]any{"n": 0}); err != nil {
		t.Fatal(err)
	}

	// B: BEGIN and read, taking its snapshot.
	tx := b.db.Begin()
	if tx.Error != nil {
		t.Fatalf("B begin: %v", tx.Error)
	}
	var rec Setting
	if err := tx.First(&rec, "key = ?", "counter").Error; err != nil {
		t.Fatalf("B read: %v", err)
	}

	// A: write and commit. With _txlock=immediate this WAITS for B (B holds the
	// write lock); without it, A commits straight away and invalidates B's
	// snapshot.
	aDone := make(chan error, 1)
	go func() {
		aDone <- a.PutSetting("counter", map[string]any{"n": 5})
	}()

	// Let A get as far as it can before B writes: either it committed (no fix)
	// or it is queued behind B's write lock (fix).
	select {
	case err := <-aDone:
		if err != nil {
			t.Fatalf("A failed: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		// Still waiting: B holds the lock, which is the fixed behaviour.
	}

	// B: write on the snapshot it read.
	writeErr := tx.Model(&Setting{}).Where("key = ?", "counter").
		Updates(map[string]any{"value": "1"}).Error
	if writeErr != nil {
		t.Errorf("the second writer was killed by its stale snapshot: %v (want it to queue, not fail)", writeErr)
	}
	if err := tx.Commit().Error; err != nil {
		t.Errorf("B commit: %v", err)
	}
	// A must still land.
	select {
	case err := <-aDone:
		if err != nil {
			t.Errorf("A failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("A never completed after B committed")
	}
}
