package logging

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// The exported Maintain is what main actually calls, and its contract differs
// from the tick helper's: it must NOT apply on entry. Applying on entry would
// sweep twice within the same second at start-up and record two "cleaned up
// expired logs" lines with different counts for one start.
func TestMaintainWrapperDoesNotApplyOnEntry(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2026-10-01"))
	touch(t, dir, fileName("2026-10-12"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Maintain(ctx, func() Policy { return Policy{Level: LevelDebug, RetentionDays: 1} })
	}()
	// MaintainInterval is an hour, so nothing can tick inside this window: any
	// change on disk here would have to come from an apply-on-entry.
	time.Sleep(150 * time.Millisecond)
	files := listDir(t, dir)
	if len(files) != 2 {
		t.Errorf("files = %v: Maintain applied on entry (the caller applies at start-up)", files)
	}
	if m.Level() == LevelDebug {
		t.Error("Maintain changed the level on entry; the start-up apply belongs to the caller")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Maintain did not return on ctx cancellation")
	}
}

// WithAttrs must rename reserved keys on the attributes it carries, exactly as
// it does for record-time attributes: a wrapper that forwards them unchanged
// reintroduces the duplicate-JSON-key defect for every logger built with
// .With(...), where a bound `level` makes the record vanish from the panel.
func TestWithAttrsRenamesReservedKeys(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, _ := openTest(t, clock)
	// The default level admits INFO; set it explicitly so the test does not
	// depend on the default.
	m.Apply(Policy{Level: LevelInfo, RetentionDays: 0})

	l := m.Logger().With(slog.String("level", "custom"), slog.String("msg", "bound"))
	l.Info("hello", "level", "record")

	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	// The level attribute must have been renamed, so the record still appears
	// at INFO: a bound `level` reaching the JSON would make the panel's level
	// scan read "custom" and drop the record from every filter.
	var found *Entry
	for i := range res.Entries {
		if res.Entries[i].Msg == "hello" {
			found = &res.Entries[i]
		}
	}
	if found == nil {
		t.Fatalf("the record is missing from a default query: %+v", res.Entries)
	}
	if found.Level != "INFO" {
		t.Errorf("level = %q, want INFO (a bound attribute changed the record's level)", found.Level)
	}
	for _, key := range []string{"#level", "#msg"} {
		if _, ok := found.Attrs[key]; !ok {
			t.Errorf("bound attribute %q missing from %v", key, found.Attrs)
		}
	}
}
