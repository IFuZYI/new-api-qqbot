package logging

import (
	"fmt"
	"testing"
)

// The ring must never hold more than its limit, and must keep the NEWEST
// entries. Its capacity is the bound on a query's memory, so an off-by-one here
// is a memory regression rather than a cosmetic one.
func TestEntryRingKeepsOnlyTheNewestLimit(t *testing.T) {
	r := newEntryRing(3)
	for i := 0; i < 10; i++ {
		evicted := r.add(Entry{Msg: fmt.Sprintf("r%d", i)})
		if i < 3 && evicted {
			t.Errorf("add(%d) reported an eviction before the ring was full", i)
		}
		if i >= 3 && !evicted {
			t.Errorf("add(%d) did not report the eviction", i)
		}
		if len(r.buf) > 3 {
			t.Fatalf("after add(%d) the ring holds %d entries, want at most 3", i, len(r.buf))
		}
		if cap(r.buf) != 3 {
			t.Errorf("after add(%d) capacity = %d, want 3 (the backing array must not grow)", i, cap(r.buf))
		}
	}
	got := r.newestFirst()
	if len(got) != 3 {
		t.Fatalf("newestFirst returned %d entries, want 3", len(got))
	}
	for i, want := range []string{"r9", "r8", "r7"} {
		if got[i].Msg != want {
			t.Errorf("entry %d = %q, want %q", i, got[i].Msg, want)
		}
	}
}

// A limit of zero or less must not panic or produce an unbounded ring: the
// caller's limit is clamped by Query, but the ring is safe on its own terms.
func TestEntryRingHandlesANonPositiveLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		r := newEntryRing(limit)
		r.add(Entry{Msg: "a"})
		r.add(Entry{Msg: "b"})
		if len(r.buf) > 1 {
			t.Errorf("limit %d: ring holds %d entries, want at most 1", limit, len(r.buf))
		}
		if got := r.newestFirst(); len(got) != 1 || got[0].Msg != "b" {
			t.Errorf("limit %d: newestFirst = %+v, want the newest only", limit, got)
		}
	}
}
