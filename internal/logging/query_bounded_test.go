package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A query must not hold every matching record in memory.
//
// `matched` accumulated every line that passed the level and search filters, so
// peak memory was bounded by the day's size rather than by `Limit`. An
// independent reviewer measured the unfiltered whole-day open (the panel's
// initial load sends no level) at 1.71 s and a 122 MB heap delta on a 27 MB /
// 200k-line day, inside one HTTP handler. Only `Limit` entries are ever returned,
// so the rest was retained for nothing.
//
// This asserts the RESULT is unchanged while the retained set is bounded: the
// newest `limit` records, newest first, with Truncated set.
func TestAQueryKeepsOnlyTheNewestLimitRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := Open(Options{Dir: dir, Stderr: os.Stderr, Now: func() time.Time {
		return time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	const total = 5000
	f, err := os.Create(filepath.Join(dir, fileName("2026-10-12")))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		// Distinct messages so the order is checkable.
		fmt.Fprintf(f, "{\"time\":\"2026-10-12T09:00:00Z\",\"level\":\"INFO\",\"msg\":\"record-%05d\"}\n", i)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := m.Query(Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false with %d records and limit 10", total)
	}
	if len(res.Entries) != 10 {
		t.Fatalf("returned %d entries, want 10", len(res.Entries))
	}
	// Newest first: the last record written comes first.
	if res.Entries[0].Msg != fmt.Sprintf("record-%05d", total-1) {
		t.Errorf("first entry = %q, want the newest record", res.Entries[0].Msg)
	}
	if res.Entries[9].Msg != fmt.Sprintf("record-%05d", total-10) {
		t.Errorf("last entry = %q, want the 10th-newest record", res.Entries[9].Msg)
	}
	// Every level is still counted, including records outside the limit.
	if res.Counts["INFO"] != total {
		t.Errorf("Counts[INFO] = %d, want %d (counting must not depend on the limit)", res.Counts["INFO"], total)
	}

	// The retained set is bounded by the LIMIT, not the file. TotalAlloc cannot
	// prove this — decoding every line allocates transiently whatever the
	// retention policy is — so the bound is asserted where it lives: the ring
	// that holds the matches. See entry_ring_test.go for the ring's own
	// capacity tests.
	ring := newEntryRing(10)
	for i := 0; i < total; i++ {
		ring.add(Entry{Msg: "x"})
		if cap(ring.buf) != 10 {
			t.Fatalf("the ring's capacity grew to %d after %d adds; the matched set is not bounded by the limit", cap(ring.buf), i)
		}
	}
}
