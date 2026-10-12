package logging

import (
	"strings"
	"testing"
	"time"
)

// A record's envelope keys (time, level, msg) are reserved: slog writes them
// itself, and an attribute with the same name produces a JSON object with a
// duplicate key. Decoding keeps the last one, so such a record is silently
// misread (an attribute named "level" makes the whole record disappear).
func TestReservedAttributeNamesAreRejected(t *testing.T) {
	for _, key := range []string{"time", "level", "msg", "source"} {
		if !isReservedAttr(key) {
			t.Errorf("%q must be reserved", key)
		}
	}
	for _, key := range []string{"patrol", "qq", "err", "status"} {
		if isReservedAttr(key) {
			t.Errorf("%q must not be reserved", key)
		}
	}
}

// An attribute that would collide is written under a safe name instead of
// corrupting the record.
func TestCollidingAttrIsRenamed(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)

	m.Logger().Info("hello", "level", "debug", "qq", 1)

	body := readFile(t, dir+"/qqbot-2026-10-12.log")
	if strings.Count(body, `"level"`) != 1 {
		t.Errorf("duplicate reserved key in the record: %s", body)
	}
	if !strings.Contains(body, `"#level":"debug"`) {
		t.Errorf("the colliding attribute was not renamed: %s", body)
	}
	// The record is still readable, with both the envelope level and the attr.
	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Level != "INFO" {
		t.Fatalf("entries = %+v", res.Entries)
	}
	if res.Entries[0].Attrs["#level"] != "debug" {
		t.Errorf("attrs = %+v", res.Entries[0].Attrs)
	}
}
