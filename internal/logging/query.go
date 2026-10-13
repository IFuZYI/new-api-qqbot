package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// MaxRetentionDays bounds Cleanup's own input. The settings boundary
// (service.MaxLogRetentionDays) enforces the same ceiling on the operator's
// value; this one keeps Cleanup safe for any caller, because
// `today.Day() - (keepDays-1)` overflows for an absurd value and MaxInt64 wraps
// the cutoff FORWARD, deleting today's file.
const MaxRetentionDays = 3650

// Result is a query's answer.
type Result struct {
	Day     string         `json:"day"`
	Entries []Entry        `json:"entries"`
	Counts  map[string]int `json:"counts"`
	// Truncated means the limit cut the list short: there are more records than
	// the caller asked for.
	Truncated bool `json:"truncated"`
	// Unparsed counts lines whose level could be read but which do not decode
	// (a partial write, a record from another producer). They are in Counts but
	// can never appear in Entries, so the panel has to say so or its totals and
	// its list disagree with no explanation.
	Unparsed int `json:"unparsed"`
	// Incomplete means the scan stopped early (a record longer than the read
	// limit). Everything after that point is missing from both Counts and
	// Entries, so the day's numbers are a lower bound.
	Incomplete bool `json:"incomplete"`
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
		day = m.now().In(m.Location()).Format(time.DateOnly)
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
	// A ring of at most `limit` entries, newest last: only the newest `limit`
	// records are ever returned, so retaining every match made the panel's
	// unfiltered whole-day open allocate with the day's size — measured 122 MB
	// heap on a 27 MB / 200k-line day inside one handler. Everything older than
	// the ring's oldest entry is dropped as it arrives, so peak memory is the
	// limit, not the file.
	ring := newEntryRing(limit)
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
			res.Unparsed++
			continue
		}
		if search != "" && !matchesSearch(e, search) {
			continue
		}
		if ring.add(e) {
			res.Truncated = true
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			// A line longer than maxLine stops the scan: the records after it
			// are silently absent, so the caller is told the answer is partial.
			res.Incomplete = true
		} else {
			return res, err
		}
	}
	res.Entries = ring.newestFirst()
	return res, nil
}

// entryRing keeps the newest n entries with a fixed capacity.
//
// It exists so a query's memory is bounded by its limit rather than by the size
// of the day it reads: the panel asks for 200 records out of a file that can
// hold hundreds of thousands, and an unbounded slice retained every match.
type entryRing struct {
	buf   []Entry
	limit int
	// dropped records that at least one entry was evicted, i.e. the result is a
	// truncation of a longer match set.
	dropped bool
}

func newEntryRing(limit int) *entryRing {
	if limit < 1 {
		limit = 1
	}
	return &entryRing{buf: make([]Entry, 0, limit), limit: limit}
}

// add appends e, evicting the oldest entry if the ring is full. It reports
// whether an eviction happened, which is what makes the result Truncated.
func (r *entryRing) add(e Entry) bool {
	if len(r.buf) == r.limit {
		// Shift rather than reslice: reslicing would keep the backing array
		// growing, and the evicted Entry must be released so its attribute map
		// can be collected.
		copy(r.buf, r.buf[1:])
		r.buf = r.buf[:r.limit-1]
		r.dropped = true
	}
	r.buf = append(r.buf, e)
	return r.dropped
}

// newestFirst returns the retained entries, newest first. It never allocates
// more than the ring's capacity.
func (r *entryRing) newestFirst() []Entry {
	out := make([]Entry, 0, len(r.buf))
	for i := len(r.buf) - 1; i >= 0; i-- {
		out = append(out, r.buf[i])
	}
	return out
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

// attrText renders one attribute value for searching.
//
// Numbers are the reason this exists: JSON decodes them to float64, and fmt's
// default verb turns a QQ number into "1.23456789e+08", so an operator searching
// the digits they see in the panel found nothing. An integral value is rendered
// as an integer (matching how it was written and how it is displayed); anything
// else keeps fmt's formatting.
func attrText(v any) string {
	switch n := v.(type) {
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < 1e15 {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	case float32:
		return attrText(float64(n))
	default:
		return fmt.Sprint(v)
	}
}

// matchesSearch reports whether a record mentions term (already lowercased) in
// its message, one of its attribute names, or one of its attribute values.
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
		if s := attrText(v); s != "" && strings.Contains(strings.ToLower(s), term) {
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
