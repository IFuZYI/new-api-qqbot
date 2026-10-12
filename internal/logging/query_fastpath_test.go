package logging

import (
	"testing"
	"time"
)

// A log query must never decode a line it will not return.
//
// The fast path exists so a filtered query does not pay for records it excludes.
// Removing it left the suite green, because the observable counts stay the same:
// the tell is that an undecodable line is then counted as `unparsed` even when
// the level filter excludes it.
//
// The fast path exists so a filtered query does not pay for records it excludes.
// Removing it left the suite green because the observable counts stay the same;
// the tell is that an undecodable line is then counted as `unparsed` even when
// the filter excludes its level.
func TestQueryDoesNotDecodeLinesItExcludes(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	seedLines(t, dir, "2026-10-12",
		// A DEBUG line that does NOT decode, and an INFO line that does.
		`{"time":"2026-10-12T10:00:00Z","level":"DEBUG","msg":"cut off`,
		`{"time":"2026-10-12T10:00:01Z","level":"INFO","msg":"fine"}`,
	)

	// Asking for INFO and above: the broken DEBUG line is excluded by the level
	// filter, so it must never be decoded — and therefore never counted as
	// unparsed. If the query decodes every line before filtering, this is 1.
	res, err := m.Query(Query{Day: "2026-10-12", MinLevel: "info"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Unparsed != 0 {
		t.Errorf("Unparsed = %d: the query decoded a line the level filter excluded", res.Unparsed)
	}
	if len(res.Entries) != 1 || res.Entries[0].Msg != "fine" {
		t.Errorf("entries = %+v", res.Entries)
	}
	// Without the filter the same line IS decoded and counted, which is the
	// behaviour the panel relies on.
	all, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	if all.Unparsed != 1 {
		t.Errorf("Unparsed = %d without a filter, want 1", all.Unparsed)
	}
}
