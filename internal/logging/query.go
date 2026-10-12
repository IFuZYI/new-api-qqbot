package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one decoded record.
type Entry struct {
	Time  time.Time      `json:"time"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Query selects records from one day's file.
type Query struct {
	// Day is YYYY-MM-DD; empty means today.
	Day string
	// MinLevel is debug/info/warn/error; empty means everything.
	MinLevel string
	// Search is a case-insensitive substring of the message or any attribute.
	Search string
	// Limit caps the result, keeping the newest; <= 0 means DefaultLimit.
	Limit int
}

// DefaultLimit and MaxLimit bound a query: a DEBUG day can run to many
// megabytes, and the panel only needs what fits on a screen or two.
const (
	DefaultLimit = 200
	MaxLimit     = 2000
)

// Result is a query's answer.
type Result struct {
	Day       string         `json:"day"`
	Entries   []Entry        `json:"entries"`
	Counts    map[string]int `json:"counts"`
	Truncated bool           `json:"truncated"`
}

// QueryError is a malformed query (the caller's mistake), as opposed to a
// failure reading the file.
type QueryError struct{ msg string }

func (e *QueryError) Error() string { return e.msg }

// DayFile describes one daily file on disk.
type DayFile struct {
	Day  string `json:"day"`
	Size int64  `json:"size"`
}

var levelRank = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

// maxLine bounds one record. A panic stack is the longest legitimate record
// (tens of KB); a longer line ends the read, and what was decoded before it is
// still returned rather than failing the whole day.
const maxLine = 1 << 20

// Query reads one day's records.
func (m *Manager) Query(q Query) (Result, error) {
	day := q.Day
	if day == "" {
		day = m.now().In(m.loc).Format(time.DateOnly)
	}
	// The day reaches the filesystem, so it must be a date and nothing else.
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return Result{}, &QueryError{fmt.Sprintf("日期格式应为 YYYY-MM-DD：%q", day)}
	}
	min := -1
	if q.MinLevel != "" {
		r, ok := levelRank[strings.ToUpper(q.MinLevel)]
		if !ok {
			return Result{}, &QueryError{fmt.Sprintf("未知的日志等级 %q", q.MinLevel)}
		}
		min = r
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	search := strings.ToLower(q.Search)

	res := Result{Day: day, Entries: []Entry{}, Counts: map[string]int{}}
	f, err := os.Open(filepath.Join(m.dir, fileName(day)))
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	var matched []Entry
	for sc.Scan() {
		line := sc.Bytes()
		// The level is read from the raw bytes first: counting the day's
		// records needs every line's level, but decoding the whole object for a
		// line that will not be reported is what made a 20MB day cost ~0.9s per
		// panel open (measured; see BenchmarkQueryBigDay).
		level, ok := scanLevel(line)
		if !ok {
			continue
		}
		res.Counts[level]++
		if levelRank[level] < min {
			continue
		}
		e, ok := decodeEntry(line)
		if !ok {
			continue
		}
		if search != "" && !matchesSearch(e, search) {
			continue
		}
		matched = append(matched, e)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return res, err
	}
	if len(matched) > limit {
		matched = matched[len(matched)-limit:]
		res.Truncated = true
	}
	for i := len(matched) - 1; i >= 0; i-- {
		res.Entries = append(res.Entries, matched[i])
	}
	return res, nil
}

// scanLevel reads a record's own level field out of the raw line.
//
// It is a byte scan rather than a decode, and it looks only for the top-level
// "level" key written by slog's JSON handler: the value must be one of the four
// known names, so a record whose *text* mentions "ERROR" (an error string in an
// attribute) is not miscounted.
func scanLevel(line []byte) (string, bool) {
	const key = `"level":"`
	i := bytes.Index(line, []byte(key))
	if i < 0 {
		return "", false
	}
	rest := line[i+len(key):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return "", false
	}
	lv := string(rest[:j])
	if _, ok := levelRank[lv]; !ok {
		return "", false
	}
	return lv, true
}

// matchesSearch reports whether a record mentions term (already lowercased) in
// its message or one of its attribute values.
//
// The raw line is deliberately NOT searched: it also carries the timestamp and
// the level, so a search for "502" matched every record whose nanosecond field
// contained those digits, and a search for "error" matched the whole day.
func matchesSearch(e Entry, term string) bool {
	if strings.Contains(strings.ToLower(e.Msg), term) {
		return true
	}
	for k, v := range e.Attrs {
		if strings.Contains(strings.ToLower(k), term) {
			return true
		}
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), term) {
			return true
		}
		if s := fmt.Sprint(v); s != "" && strings.Contains(strings.ToLower(s), term) {
			return true
		}
	}
	return false
}

// decodeEntry parses one JSON line written by slog's JSON handler.
func decodeEntry(line []byte) (Entry, bool) {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return Entry{}, false
	}
	var e Entry
	if s, ok := raw["time"].(string); ok {
		e.Time, _ = time.Parse(time.RFC3339Nano, s)
	}
	e.Level, _ = raw["level"].(string)
	e.Msg, _ = raw["msg"].(string)
	if _, ok := levelRank[e.Level]; !ok {
		return Entry{}, false
	}
	delete(raw, "time")
	delete(raw, "level")
	delete(raw, "msg")
	if len(raw) > 0 {
		e.Attrs = raw
	}
	return e, true
}

// Days lists the daily files, newest first.
func (m *Manager) Days() ([]DayFile, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	out := []DayFile{}
	for _, e := range entries {
		day, ok := dayOf(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, DayFile{Day: day, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day > out[j].Day })
	return out, nil
}
