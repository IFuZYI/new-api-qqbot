package logging

import "testing"

// scanLevel must read the record's own level field, not search the line for a
// level name. The distinction is invisible in a single sample: a scan that
// searches the text answers correctly whenever the first level name it happens
// to try (map order is random) is the right one. So the test drives many lines,
// each of which mentions all three other level names in its message and error
// text, and requires every answer to be the record's own level. One line would
// pass by luck about a quarter of the time; ten lines cannot.
func TestLevelProbeReadsTheTopLevelField(t *testing.T) {
	cases := []struct {
		level string
		line  string
	}{
		{"INFO", `{"time":"2026-10-12T10:00:00Z","level":"INFO","msg":"about a failure","err":"ERROR: WARN DEBUG gateway 502"}`},
		{"ERROR", `{"time":"2026-10-12T10:00:01Z","level":"ERROR","msg":"sweep failed","note":"INFO WARN DEBUG noise"}`},
		{"WARN", `{"time":"2026-10-12T10:00:02Z","level":"WARN","msg":"bind failed","err":"ERROR INFO DEBUG"}`},
		{"DEBUG", `{"time":"2026-10-12T10:00:03Z","level":"DEBUG","msg":"请求","detail":"ERROR INFO WARN"}`},
		{"INFO", `{"time":"2026-10-12T10:00:04Z","level":"INFO","msg":"ERROR WARN DEBUG all in the message"}`},
		{"ERROR", `{"time":"2026-10-12T10:00:05Z","level":"ERROR","msg":"INFO WARN DEBUG"}`},
		{"WARN", `{"time":"2026-10-12T10:00:06Z","level":"WARN","msg":"DEBUG ERROR INFO"}`},
		{"DEBUG", `{"time":"2026-10-12T10:00:07Z","level":"DEBUG","msg":"INFO ERROR WARN"}`},
		{"INFO", `{"time":"2026-10-12T10:00:08Z","level":"INFO","msg":"ERROR","err":"WARN","note":"DEBUG"}`},
		{"ERROR", `{"time":"2026-10-12T10:00:09Z","level":"ERROR","msg":"DEBUG","err":"INFO","note":"WARN"}`},
	}
	for i, c := range cases {
		got, ok := scanLevel([]byte(c.line))
		if !ok {
			t.Errorf("case %d: scanLevel rejected a valid record", i)
			continue
		}
		if got != c.level {
			t.Errorf("case %d: scanLevel = %q, want %q (the record's own level field)", i, got, c.level)
		}
	}
}

// A line whose level field is absent or unknown yields nothing, so the caller
// can skip it rather than guess.
func TestLevelProbeRejectsUnknownOrMissingLevel(t *testing.T) {
	for _, line := range []string{
		`{"time":"2026-10-12T10:00:00Z","msg":"no level field"}`,
		`{"time":"2026-10-12T10:00:00Z","level":"TRACE","msg":"unknown"}`,
		`not json`,
		``,
	} {
		if got, ok := scanLevel([]byte(line)); ok {
			t.Errorf("scanLevel(%q) = %q, want no answer", line, got)
		}
	}
}
