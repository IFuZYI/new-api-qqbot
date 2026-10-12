package logging

import (
	"strings"
	"testing"
	"time"
)

// A line whose level can be read but which does not decode (a partial write, a
// truncated record) is counted by the fast path but can never be listed. The
// panel must say so rather than showing "3 errors" above two rows.
func TestUnparsedLinesAreReported(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	seedLines(t, dir, "2026-10-12",
		`{"time":"2026-10-12T10:00:00Z","level":"ERROR","msg":"complete"}`,
		`{"time":"2026-10-12T10:00:01Z","level":"ERROR","msg":"cut off mid-wri`, // invalid JSON
	)

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(res.Entries))
	}
	if res.Unparsed != 1 {
		t.Errorf("Unparsed = %d, want 1 (the count and the list must agree)", res.Unparsed)
	}
	// The counted level is still reported, so the operator sees the record exists.
	if res.Counts["ERROR"] != 2 {
		t.Errorf("counts = %v", res.Counts)
	}
}

// A record longer than the read limit ends the scan. Everything after it is
// neither counted nor listed, so the result must say it is incomplete instead of
// silently under-reporting.
func TestOverlongLineMarksTheResultIncomplete(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	huge := `{"time":"2026-10-12T10:00:00Z","level":"INFO","msg":"` + strings.Repeat("x", maxLine+16) + `"}`
	seedLines(t, dir, "2026-10-12",
		`{"time":"2026-10-12T09:59:59Z","level":"INFO","msg":"before"}`,
		huge,
		`{"time":"2026-10-12T10:00:01Z","level":"ERROR","msg":"after"}`,
	)

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Incomplete {
		t.Error("Incomplete = false: the records after the over-long line are missing with no signal")
	}
	// What was read before the break is still returned.
	if len(res.Entries) != 1 || res.Entries[0].Msg != "before" {
		t.Errorf("entries = %+v", res.Entries)
	}
}

// A search matches attribute NAMES as well as values, and the docs say so: the
// operator searching "patrol" wants every record carrying that field.
func TestSearchMatchesAttributeNames(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	seedLines(t, dir, "2026-10-12",
		`{"time":"2026-10-12T10:00:00Z","level":"ERROR","msg":"巡检失败","patrol":"checkins"}`,
		`{"time":"2026-10-12T10:00:01Z","level":"INFO","msg":"无关记录","other":1}`,
	)

	res, err := m.Query(Query{Day: "2026-10-12", Search: "patrol"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Msg != "巡检失败" {
		t.Errorf("search patrol = %+v", res.Entries)
	}
}
