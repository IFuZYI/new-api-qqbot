package logging

import (
	"testing"
	"time"
)

// Apply records the level change. The operator flips the switch and then opens
// the log page to see it take effect; without a record they cannot tell the save
// from a silent failure.
func TestApplyRecordsTheLevelChange(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)

	m.Apply(Policy{Level: LevelDebug, RetentionDays: 0})

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range res.Entries {
		if e.Level == "INFO" && e.Attrs["new_level"] == LevelDebug {
			found = true
		}
	}
	if !found {
		body := readFile(t, dir+"/qqbot-2026-10-12.log")
		t.Errorf("no record of the level change: %s", body)
	}
}

// Applying the same policy twice must not repeat the record: the maintenance
// loop runs hourly, and an unchanged policy is not an event.
func TestApplyDoesNotRepeatAnUnchangedPolicy(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)

	m.Apply(Policy{Level: LevelInfo, RetentionDays: 7})
	m.Apply(Policy{Level: LevelInfo, RetentionDays: 7})
	m.Apply(Policy{Level: LevelInfo, RetentionDays: 7})

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range res.Entries {
		if _, ok := e.Attrs["new_level"]; ok && e.Msg == "日志等级已生效" {
			n++
		}
	}
	if n > 1 {
		t.Errorf("the level change was recorded %d times", n)
	}
}
