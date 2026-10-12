package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedLines writes a day file verbatim, so a test controls every byte of the
// records (including the timestamp, which is what a naive search matches).
func seedLines(t *testing.T, dir, day string, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, fileName(day)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The search must match the message and the attribute values, not the whole
// serialised record: the timestamp contains arbitrary digits, so a search for
// "502" (or any number an operator is chasing) would otherwise match every
// record written in that nanosecond.
func TestSearchIgnoresTimestampAndLevel(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	seedLines(t, dir, "2026-10-12",
		`{"time":"2026-10-12T15:40:00.502324502Z","level":"INFO","msg":"hello","qq":1}`,
		`{"time":"2026-10-12T15:40:01.000000000Z","level":"ERROR","msg":"gateway said 502"}`,
	)

	res, err := m.Query(Query{Day: "2026-10-12", Search: "502"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Msg != "gateway said 502" {
		t.Errorf("search 502 = %+v (the timestamp matched too)", res.Entries)
	}
}

// Searching a level name must not return records merely because their own level
// is spelled that way — otherwise "error" matches the whole day.
func TestSearchIgnoresTheLevelField(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	seedLines(t, dir, "2026-10-12",
		`{"time":"2026-10-12T10:00:00Z","level":"ERROR","msg":"one"}`,
		`{"time":"2026-10-12T10:00:01Z","level":"INFO","msg":"two"}`,
	)

	res, err := m.Query(Query{Day: "2026-10-12", Search: "error"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("search error = %+v, want none (only the level field mentions it)", res.Entries)
	}
}

// Attribute values are still searchable: that is how an operator finds a QQ
// number or a patrol name.
func TestSearchStillMatchesAttrValues(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	seedLines(t, dir, "2026-10-12",
		`{"time":"2026-10-12T10:00:00Z","level":"ERROR","msg":"巡检失败","patrol":"checkins","err":"board down"}`,
		`{"time":"2026-10-12T10:00:01Z","level":"INFO","msg":"巡检完成","patrol":"bind"}`,
	)

	res, err := m.Query(Query{Day: "2026-10-12", Search: "checkins"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Msg != "巡检失败" {
		t.Errorf("search checkins = %+v", res.Entries)
	}
}
