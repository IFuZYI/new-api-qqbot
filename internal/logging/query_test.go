package logging

import (
	"errors"
	"testing"
	"time"
)

func seed(t *testing.T) *Manager {
	t.Helper()
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)
	if err := m.SetLevel(LevelDebug); err != nil {
		t.Fatal(err)
	}
	l := m.Logger()
	l.Debug("debug one")
	clock.t = clock.t.Add(time.Second)
	l.Info("info one", "qq", 1)
	clock.t = clock.t.Add(time.Second)
	l.Warn("warn one")
	clock.t = clock.t.Add(time.Second)
	l.Error("sweep failed", "patrol", "bind", "err", errors.New("gateway 502"))
	return m
}

// The panel lists newest first: the operator opens it to see what just broke.
func TestQueryReturnsNewestFirstWithAttrs(t *testing.T) {
	m := seed(t)
	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(res.Entries), res.Entries)
	}
	top := res.Entries[0]
	if top.Level != "ERROR" || top.Msg != "sweep failed" {
		t.Errorf("newest entry = %+v", top)
	}
	if top.Attrs["patrol"] != "bind" || top.Attrs["err"] != "gateway 502" {
		t.Errorf("attrs = %+v", top.Attrs)
	}
	if top.Time.IsZero() {
		t.Error("entry time not decoded")
	}
}

// A minimum level filters out everything below it, which is how "show me only
// the errors" works.
func TestQueryMinLevelFilters(t *testing.T) {
	m := seed(t)
	res, err := m.Query(Query{Day: "2026-10-12", MinLevel: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 2 || res.Entries[0].Level != "ERROR" || res.Entries[1].Level != "WARN" {
		t.Errorf("warn+ = %+v", res.Entries)
	}
}

// The counts cover the whole day regardless of the filter, so the panel can
// show "3 errors today" next to a list filtered to something else.
func TestQueryCountsEveryLevel(t *testing.T) {
	m := seed(t)
	res, err := m.Query(Query{Day: "2026-10-12", MinLevel: "error"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"DEBUG": 1, "INFO": 1, "WARN": 1, "ERROR": 1}
	for k, v := range want {
		if res.Counts[k] != v {
			t.Errorf("Counts[%s] = %d, want %d (all: %v)", k, res.Counts[k], v, res.Counts)
		}
	}
}

// A keyword matches the message and the attribute values, so searching a QQ
// number finds every record about that member.
func TestQuerySearchMatchesMessageAndAttrs(t *testing.T) {
	m := seed(t)
	res, err := m.Query(Query{Day: "2026-10-12", Search: "502"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Msg != "sweep failed" {
		t.Errorf("search 502 = %+v", res.Entries)
	}
}

// Limit keeps the newest records, and Truncated tells the panel there is more.
func TestQueryLimitKeepsNewest(t *testing.T) {
	m := seed(t)
	res, err := m.Query(Query{Day: "2026-10-12", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 2 || res.Entries[0].Level != "ERROR" || !res.Truncated {
		t.Errorf("limit 2 = %+v truncated=%v", res.Entries, res.Truncated)
	}
}

// A day with no file is an empty result, not an error: a quiet day is normal.
func TestQueryMissingDayIsEmpty(t *testing.T) {
	m := seed(t)
	res, err := m.Query(Query{Day: "2026-01-01"})
	if err != nil || len(res.Entries) != 0 {
		t.Errorf("missing day = %+v, %v", res, err)
	}
}

// The day is a date, never a path: it is the one value in the query a caller
// controls that reaches the filesystem.
func TestQueryRejectsNonDateDay(t *testing.T) {
	m := seed(t)
	for _, day := range []string{"../../etc/passwd", "2026-10-12/../x", "x"} {
		if _, err := m.Query(Query{Day: day}); err == nil {
			t.Errorf("Query(day=%q) succeeded", day)
		}
	}
}

// Days lists the files on disk, newest first, for the panel's day picker.
func TestDaysListsFilesNewestFirst(t *testing.T) {
	m := seed(t)
	touch(t, m.Dir(), fileName("2026-10-10"))
	touch(t, m.Dir(), "notes.txt")
	days, err := m.Days()
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Day != "2026-10-12" || days[1].Day != "2026-10-10" || days[0].Size == 0 {
		t.Errorf("days = %+v", days)
	}
}
