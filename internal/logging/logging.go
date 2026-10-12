// Package logging owns the service's log: where records go, at what level, and
// for how long they are kept.
//
// Records are written twice. The data directory gets one JSON-lines file per
// day (logs/qqbot-YYYY-MM-DD.log), which is what the panel reads back to show
// errors and what the retention sweep deletes; stderr gets the same records as
// text, so `docker logs` and journald keep working as before.
//
// The level is shared by both outputs and can be changed while running: an
// operator chasing a live problem switches to DEBUG from the panel, and that
// must not need a restart (a restart would lose the very state being debugged).
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DirName is the log directory inside the data directory, so one directory is
// still the whole instance to back up or move.
const DirName = "logs"

// Options configures Open. Zero values mean the production defaults.
type Options struct {
	// Dir holds the daily files. It is created when missing.
	Dir string
	// Stderr receives the text copy; nil means os.Stderr.
	Stderr io.Writer
	// Now is the clock that picks the day's file; nil means time.Now.
	Now func() time.Time
	// Location decides which calendar "today" means. nil means time.Local.
	// It is the operator's timezone from the settings, not the host's: the
	// check-ins and the rewards already use that calendar, so the log must
	// agree with them or a record written at 00:30 Beijing lands in the
	// previous day's file.
	Location *time.Location
}

// Manager is the running log.
type Manager struct {
	dir   string
	now   func() time.Time
	level slog.LevelVar

	// locMu guards loc, which SetLocation changes while the service is running.
	locMu sync.RWMutex
	loc   *time.Location

	// applyMu serialises Apply, which the maintenance tick and a settings save
	// can both reach: two sweeps over one directory make the loser's os.Remove
	// return ENOENT and log a false cleanup failure.
	applyMu sync.Mutex

	file   *dayFile
	logger *slog.Logger
}

// Open creates the log directory and returns a Manager at INFO.
func Open(o Options) (*Manager, error) {
	if o.Dir == "" {
		return nil, fmt.Errorf("log directory is required")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	m := &Manager{dir: o.Dir, now: o.Now, loc: o.Location}
	m.level.Set(slog.LevelInfo)
	m.file = &dayFile{dir: o.Dir, now: o.Now, loc: o.Location, stderr: o.Stderr}
	opts := &slog.HandlerOptions{Level: &m.level}
	m.logger = slog.New(slog.NewMultiHandler(
		attrRenamer{slog.NewJSONHandler(m.file, opts)},
		attrRenamer{slog.NewTextHandler(o.Stderr, opts)},
	))
	return m, nil
}

// reservedAttrs are the keys slog writes itself. An attribute with one of these
// names produces a JSON object with a duplicate key, and decoding keeps only the
// last — so a record carrying a "level" attribute reads back as if it had no
// level at all and vanishes from the panel. Renaming keeps the attribute
// readable and the record intact.
var reservedAttrs = map[string]bool{"time": true, "level": true, "msg": true, "source": true}

func isReservedAttr(key string) bool { return reservedAttrs[key] }

// attrRenamer renames a record's colliding attributes before the handler writes
// the record.
//
// It wraps the handler rather than using slog's ReplaceAttr hook, because that
// hook cannot tell the envelope's own level from an attribute named "level":
// both arrive with the same key. The wrapper sees only the record's attributes,
// so the envelope is never in question.
type attrRenamer struct{ slog.Handler }

func (h attrRenamer) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(sanitizeAttr(a))
		return true
	})
	return h.Handler.Handle(ctx, out)
}

func (h attrRenamer) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = sanitizeAttr(a)
	}
	return attrRenamer{h.Handler.WithAttrs(out)}
}

func (h attrRenamer) WithGroup(name string) slog.Handler {
	return attrRenamer{h.Handler.WithGroup(sanitizeKey(name))}
}

// sanitizeAttr prefixes a colliding key, recursing into groups.
//
// A GROUP's own key matters as much as a leaf's: slog.Group("level", …) emits
// `"level":{…}` as a top-level key, so an unsanitised group key produces the
// same duplicate-key record a leaf would.
func sanitizeAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		out := make([]slog.Attr, len(group))
		for i, g := range group {
			out[i] = sanitizeAttr(g)
		}
		a.Key = sanitizeKey(a.Key)
		a.Value = slog.GroupValue(out...)
		return a
	}
	a.Key = sanitizeKey(a.Key)
	return a
}

// sanitizeKey prefixes a key that would collide with the record envelope.
func sanitizeKey(key string) string {
	if isReservedAttr(key) {
		return "#" + key
	}
	return key
}

// Logger returns the logger writing to both outputs.
func (m *Manager) Logger() *slog.Logger { return m.logger }

// Dir returns the directory holding the daily files.
func (m *Manager) Dir() string { return m.dir }

// SetLocation changes the calendar the day boundaries follow. It is called once
// at start-up with the operator's timezone; a record already written stays in
// the file it was written to, because the day is part of the file's name.
//
// Both the day file and the readers (Location, and through it Cleanup and Query)
// are updated under their own locks, so a caller may invoke it while records are
// being written and while the panel is querying.
func (m *Manager) SetLocation(loc *time.Location) {
	if loc == nil {
		return
	}
	m.file.mu.Lock()
	m.file.loc = loc
	m.file.mu.Unlock()
	m.locMu.Lock()
	m.loc = loc
	m.locMu.Unlock()
}

// Location returns the calendar the day boundaries follow.
func (m *Manager) Location() *time.Location {
	m.locMu.RLock()
	defer m.locMu.RUnlock()
	return m.loc
}

// Close releases the current file.
func (m *Manager) Close() error { return m.file.Close() }

// Levels the panel offers. WARN and ERROR records are always written; the
// setting only decides whether the chatty DEBUG records are kept too.
const (
	LevelInfo  = "info"
	LevelDebug = "debug"
)

// ParseLevel validates a configured level name.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case LevelInfo:
		return slog.LevelInfo, nil
	case LevelDebug:
		return slog.LevelDebug, nil
	}
	return 0, fmt.Errorf("日志等级必须是 info 或 debug，收到 %q", name)
}

// LevelName is the canonical lowercase name for a level, so a value read back
// from the settings compares equal to the name the logger reports.
func LevelName(lv slog.Level) string {
	if lv <= slog.LevelDebug {
		return LevelDebug
	}
	return LevelInfo
}

// SetLevel changes the level of the running logger.
func (m *Manager) SetLevel(name string) error {
	lv, err := ParseLevel(name)
	if err != nil {
		return err
	}
	m.level.Set(lv)
	return nil
}

// Level reports the current level name.
func (m *Manager) Level() string { return LevelName(m.level.Level()) }

// dayFile is an io.Writer that appends to the file for the clock's current day,
// switching files at midnight. slog hands it one complete record per Write, so
// switching between writes never splits a record across two days' files.
type dayFile struct {
	dir string
	now func() time.Time
	loc *time.Location
	// stderr is where a file failure is reported. It cannot go through the
	// logger: the logger is what is broken.
	stderr io.Writer

	mu   sync.Mutex
	day  string
	file *os.File
	// lastErr is the last distinct failure already reported, so a broken file
	// cannot write one line per record (a DEBUG day is tens of thousands).
	lastErr string
}

func fileName(day string) string { return "qqbot-" + day + ".log" }

func (d *dayFile) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	day := d.now().In(d.loc).Format(time.DateOnly)
	if d.file == nil || day != d.day {
		if d.file != nil {
			_ = d.file.Close()
			d.file = nil
		}
		f, err := os.OpenFile(filepath.Join(d.dir, fileName(day)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			d.reportOpenFailure(err, day)
			return 0, err
		}
		d.file, d.day = f, day
		d.lastErr = ""
	}
	n, err := d.file.Write(p)
	if err != nil {
		d.reportWriteFailure(err)
	}
	return n, err
}

// reportOpenFailure tells the operator, once per distinct cause, that the day's
// file cannot be opened.
//
// The record itself still reaches stderr (the multi-handler runs both), but the
// panel reads the FILES: without this, a broken log directory looked like a day
// with no records at all. Reported on the stderr stream directly, because the
// logger is the thing that is broken.
func (d *dayFile) reportOpenFailure(err error, day string) {
	key := day + ": " + err.Error()
	if d.lastErr == key {
		return
	}
	d.lastErr = key
	fmt.Fprintf(d.stderr, "qqbot: 日志文件 %s 写入失败，记录只输出到控制台: %v\n", fileName(day), err)
}

// reportWriteFailure is the same for a write that fails after the file was
// opened (a full disk).
func (d *dayFile) reportWriteFailure(err error) {
	key := "write: " + err.Error()
	if d.lastErr == key {
		return
	}
	d.lastErr = key
	fmt.Fprintf(d.stderr, "qqbot: 日志文件 %s 写入失败，记录只输出到控制台: %v\n", fileName(d.day), err)
}

func (d *dayFile) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil {
		return nil
	}
	err := d.file.Close()
	d.file = nil
	return err
}

// Cleanup deletes daily files older than keepDays, counting today as the first
// kept day. keepDays <= 0 keeps everything.
//
// Only names this package writes (qqbot-YYYY-MM-DD.log, with a date that
// parses) are considered, so anything else an operator puts in the directory
// survives. The date comes from the name, not the modification time: copying
// or restoring the directory resets mtimes, and the name is what says which day
// a file holds.
func (m *Manager) Cleanup(keepDays int) (int, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	today := m.now().In(m.Location())
	cutoff := time.Date(today.Year(), today.Month(), today.Day()-(keepDays-1), 0, 0, 0, 0, time.UTC).Format(time.DateOnly)

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	var firstErr error
	for _, e := range entries {
		day, ok := dayOf(e.Name())
		if !ok || e.IsDir() || day >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(m.dir, e.Name())); err != nil {
			// Already gone is success: the sweep wanted the file deleted, and a
			// concurrent sweep (or an operator tidying the directory by hand)
			// got there first. Reporting it as a failure would put a false ERROR
			// in the one view the operator is meant to trust.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// dayOf extracts the date from a daily file's name.
func dayOf(name string) (string, bool) {
	day, ok := strings.CutPrefix(name, "qqbot-")
	if !ok {
		return "", false
	}
	day, ok = strings.CutSuffix(day, ".log")
	if !ok {
		return "", false
	}
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return "", false
	}
	return day, true
}

// InstallDefault makes this logger the process default. slog.SetDefault also
// redirects the standard library's log package (at INFO), so the call sites
// that still use log.Printf, and any dependency that logs, land in the same
// daily file instead of only on stderr.
func (m *Manager) InstallDefault() { slog.SetDefault(m.logger) }
