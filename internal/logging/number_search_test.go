package logging

import (
	"testing"
	"time"
)

// A QQ number is the thing an operator searches for most, and it must match the
// digits they type.
//
// JSON numbers decode to float64, and fmt's default verb renders a large one in
// scientific notation: 123456789 became "1.23456789e+08", so a search for the
// number found nothing. The search must compare numbers the way an operator
// writes them.
func TestSearchMatchesQQNumbers(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	seedLines(t, dir, "2026-10-12",
		`{"time":"2026-10-12T10:00:00Z","level":"INFO","msg":"已绑定账号","qq":123456789,"user_id":7}`,
		`{"time":"2026-10-12T10:00:01Z","level":"ERROR","msg":"巡检失败","patrol":"members","group_id":1129535731}`,
	)

	for _, tc := range []struct {
		term string
		want int
	}{
		{"123456789", 1},  // a member's QQ
		{"1129535731", 1}, // a group id
		{"1234567890", 0}, // a number that is not there
		{"已绑定账号", 1},      // the message
		{"members", 1},    // a string attribute
	} {
		res, err := m.Query(Query{Day: "2026-10-12", Search: tc.term})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != tc.want {
			t.Errorf("search %q = %d hits, want %d", tc.term, len(res.Entries), tc.want)
		}
	}
}

// The exact number formatting still applies to non-integral values: a duration
// in milliseconds must be findable, and a float must not be turned into a
// misleading integer.
func TestSearchFormatsNumbersFaithfully(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	seedLines(t, dir, "2026-10-12",
		`{"time":"2026-10-12T10:00:00Z","level":"DEBUG","msg":"请求","duration_ms":12.5,"status":200}`,
	)

	if res, _ := m.Query(Query{Day: "2026-10-12", Search: "12.5"}); len(res.Entries) != 1 {
		t.Errorf("search 12.5 = %d hits, want 1", len(res.Entries))
	}
	if res, _ := m.Query(Query{Day: "2026-10-12", Search: "12"}); len(res.Entries) != 1 {
		t.Errorf("search 12 = %d hits, want 1 (a substring of 12.5)", len(res.Entries))
	}
	if res, _ := m.Query(Query{Day: "2026-10-12", Search: "200"}); len(res.Entries) != 1 {
		t.Errorf("search 200 = %d hits, want 1", len(res.Entries))
	}
}
