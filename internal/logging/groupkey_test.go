package logging

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A GROUP's key collides with the record envelope exactly like a leaf's:
// slog.Group("level", …) writes `"level":{…}` at the top level, so an
// unsanitised group key makes the record carry two `level` keys and decode to
// the wrong shape (the panel then cannot read the record's own level).
func TestGroupKeyCollisionIsSanitized(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)

	m.Logger().Info("with a group", slog.Group("level", "a", 1))
	m.Logger().Info("nested", slog.Group("outer", slog.Group("level", "b", 2)))
	m.Logger().WithGroup("msg").Info("grouped logger")

	body := readFile(t, dir+"/qqbot-2026-10-12.log")
	for i, line := range strings.Split(strings.TrimSpace(body), "\n") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("line %d is not a JSON object: %v (%s)", i, err, line)
		}
		// Decoding into a map collapses duplicates, so count the raw keys too:
		// a duplicate means the envelope's level was overwritten by the group.
		if n := strings.Count(line, `"level":`); n != 1 {
			t.Errorf("line %d has %d `level` keys: %s", i, n, line)
		}
		if _, ok := raw["level"]; !ok {
			t.Errorf("line %d lost its own level: %s", i, line)
		}
	}
}

// A grouped logger's records still decode, with the attribute reachable under
// its sanitised name.
func TestGroupedRecordStillDecodes(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)

	m.Logger().Info("x", slog.Group("level", "a", 1))
	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("entries = %+v", res.Entries)
	}
	if res.Entries[0].Level != "INFO" {
		t.Errorf("the record's own level was lost: %+v", res.Entries[0])
	}
	if res.Counts["INFO"] != 1 {
		t.Errorf("counts = %v", res.Counts)
	}
}
