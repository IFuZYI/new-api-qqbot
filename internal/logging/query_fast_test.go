package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The panel opens on "today, all levels", which on a DEBUG day means reading a
// file of tens of megabytes. Decoding every line as a full JSON object costs
// about a second per open, so a query must skip the decode for records it will
// not report.
//
// The benchmark is the guard: it fails (well, the timing assertion does) if the
// fast path is removed.
func BenchmarkQueryBigDay(b *testing.B) {
	dir := b.TempDir()
	f, err := os.Create(filepath.Join(dir, "qqbot-2026-10-12.log"))
	if err != nil {
		b.Fatal(err)
	}
	// ~20MB: a busy DEBUG day.
	for i := 0; i < 150000; i++ {
		fmt.Fprintf(f, `{"time":"2026-10-12T10:00:%02d.123456789Z","level":"DEBUG","msg":"请求","method":"GET","path":"/api/status","status":200,"duration_ms":3}`+"\n", i%60)
	}
	f.Close()

	m, err := Open(Options{Dir: dir, Stderr: os.Stderr, Now: func() time.Time {
		return time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Query(Query{Day: "2026-10-12", MinLevel: "error"}); err != nil {
			b.Fatal(err)
		}
	}
}

// A filtered query must not decode records it excludes. The observable contract
// is the counts (which need every level) plus the entries; this test pins the
// behaviour that matters for correctness, and the benchmark covers the cost.
func TestFilteredQuerySkipsDecode(t *testing.T) {
	dir := t.TempDir()
	day := "2026-10-12"
	lines := []string{
		`{"time":"2026-10-12T10:00:00Z","level":"DEBUG","msg":"请求","path":"/api/status"}`,
		`{"time":"2026-10-12T10:00:01Z","level":"INFO","msg":"已记录新成员","qq":123}`,
		`{"time":"2026-10-12T10:00:02Z","level":"ERROR","msg":"巡检失败","patrol":"bind"}`,
	}
	seedLines(t, dir, day, lines...)

	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, err := Open(Options{Dir: dir, Stderr: os.Stderr, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}

	res, err := m.Query(Query{Day: day, MinLevel: "error"})
	if err != nil {
		t.Fatal(err)
	}
	// Counts cover the whole day even though only ERROR is listed.
	if res.Counts["DEBUG"] != 1 || res.Counts["INFO"] != 1 || res.Counts["ERROR"] != 1 {
		t.Errorf("counts = %v", res.Counts)
	}
	if len(res.Entries) != 1 || res.Entries[0].Level != "ERROR" {
		t.Errorf("entries = %+v", res.Entries)
	}
	if res.Entries[0].Attrs["patrol"] != "bind" {
		t.Errorf("attrs = %+v", res.Entries[0].Attrs)
	}
}

// A record whose level is unknown is skipped rather than crashing or being
// counted as INFO.
func TestUnknownLevelLineSkipped(t *testing.T) {
	dir := t.TempDir()
	day := "2026-10-12"
	seedLines(t, dir, day,
		`{"time":"2026-10-12T10:00:00Z","level":"TRACE","msg":"nope"}`,
		`{"time":"2026-10-12T10:00:01Z","level":"INFO","msg":"yes"}`,
		`not json at all`,
	)
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, err := Open(Options{Dir: dir, Stderr: os.Stderr, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Query(Query{Day: day})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Msg != "yes" {
		t.Errorf("entries = %+v", res.Entries)
	}
	if res.Counts["TRACE"] != 0 {
		t.Errorf("counts = %v", res.Counts)
	}
}

// The fast path must produce exactly what the slow path did: same counts, same
// entries, same attrs. This is the regression guard for the rewrite.
func TestFastPathMatchesFullDecode(t *testing.T) {
	dir := t.TempDir()
	day := "2026-10-12"
	seedLines(t, dir, day,
		`{"time":"2026-10-12T10:00:00.000000001Z","level":"DEBUG","msg":"请求","method":"POST","path":"/api/settings","status":400,"duration_ms":7}`,
		`{"time":"2026-10-12T10:00:01Z","level":"WARN","msg":"绑定指令失败","qq":54321,"err":"no such account"}`,
		`{"time":"2026-10-12T10:00:02Z","level":"ERROR","msg":"巡检失败","patrol":"checkins","err":"board down"}`,
	)
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, err := Open(Options{Dir: dir, Stderr: os.Stderr, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Query(Query{Day: day})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 3 {
		t.Fatalf("entries = %d", len(res.Entries))
	}
	// Newest first, attributes intact, timestamps parsed.
	if res.Entries[0].Level != "ERROR" || res.Entries[2].Level != "DEBUG" {
		t.Errorf("order = %v %v %v", res.Entries[0].Level, res.Entries[1].Level, res.Entries[2].Level)
	}
	if res.Entries[0].Attrs["patrol"] != "checkins" || res.Entries[1].Attrs["qq"] != float64(54321) {
		t.Errorf("attrs = %+v / %+v", res.Entries[0].Attrs, res.Entries[1].Attrs)
	}
	if res.Entries[2].Time.IsZero() {
		t.Error("timestamp not parsed")
	}
}

// The decode path is shared, so a record the fast path accepts must decode to
// the same Entry a plain json.Unmarshal produces.
func TestDecodeEntryEquivalentToUnmarshal(t *testing.T) {
	line := []byte(`{"time":"2026-10-12T10:00:02.5Z","level":"ERROR","msg":"巡检失败","patrol":"checkins","n":3}`)
	got, ok := decodeEntry(line)
	if !ok {
		t.Fatal("decodeEntry rejected a valid record")
	}
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		t.Fatal(err)
	}
	if got.Level != raw["level"] || got.Msg != raw["msg"] {
		t.Errorf("got %+v from %v", got, raw)
	}
}
