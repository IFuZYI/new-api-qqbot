package config

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

// record is one captured slog record.
type record struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// captureHandler collects records so a test can assert on what was logged.
type captureHandler struct {
	mu   sync.Mutex
	recs []record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := record{level: r.Level, msg: r.Message, attrs: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, rec)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]record(nil), h.recs...)
}

// captureLogs routes the default logger into a captureHandler for one test.
func captureLogs(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// A malformed config file must be REPORTED, not silently ignored.
//
// syncFromFile correctly refuses to adopt a file that does not parse (adopting
// it would wipe the stored connection settings), but the refusal was invisible:
// every production caller uses MustLoad, which discards the error, so the panel
// showed the stored configuration with nothing saying the file was broken —
// only a later 保存 surfaced it, as a 500.
func TestMalformedFileIsReported(t *testing.T) {
	h := captureLogs(t)
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Path(), []byte("newapi: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Load(); err == nil {
		t.Fatal("a malformed file must be reported to the caller")
	}

	recs := h.snapshot()
	if len(recs) == 0 {
		t.Fatal("the parse failure was not logged")
	}
	// It must be WARN or above: it is the operator's only signal, and the
	// panel's level filter is where they would look.
	if recs[0].level < slog.LevelWarn {
		t.Errorf("parse failure logged at %v; it must be WARN or above", recs[0].level)
	}
	if !strings.Contains(recs[0].msg, "配置") {
		t.Errorf("message = %q: it must name the config file as the problem", recs[0].msg)
	}
	if _, ok := recs[0].attrs["err"]; !ok {
		t.Errorf("the record carries no err attribute: %v", recs[0].attrs)
	}
}

// One broken file must not fill the day's log: syncFromFile runs on every load
// and on every save.
func TestMalformedFileIsReportedOnce(t *testing.T) {
	h := captureLogs(t)
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Path(), []byte("newapi: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		_, _ = m.Load()
	}
	if n := len(h.snapshot()); n != 1 {
		t.Errorf("the failure was logged %d times for one unchanged file", n)
	}
}

// A re-break of the SAME bytes must be reported too.
//
// The original test re-broke with DIFFERENT contents, which is the one case a
// content-keyed guard handles; leaving reportedHash set after a successful parse
// meant break -> fix -> break-again-with-the-same-typo stayed silent on the
// MustLoad path — the exact gap the record exists to close. Found by an
// independent reviewer.
func TestARepeatedBreakOfTheSameBytesIsReportedAgain(t *testing.T) {
	h := captureLogs(t)
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}
	broken := []byte("newapi: [unclosed\n")

	if err := os.WriteFile(m.Path(), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Load()

	// Fix it.
	if err := os.WriteFile(m.Path(), []byte("newapi:\n  url: https://gateway.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(); err != nil {
		t.Fatalf("a valid file must be adopted: %v", err)
	}
	if n := len(h.snapshot()); n != 1 {
		t.Fatalf("records after the first breakage = %d, want 1", n)
	}

	// Break it again with the SAME bytes.
	if err := os.WriteFile(m.Path(), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Load()

	if n := len(h.snapshot()); n != 2 {
		t.Errorf("records = %d, want 2: a repeated break of the same bytes was not reported", n)
	}
}

// A fixed file clears the state, so a later breakage is reported again.
func TestAFixedFileClearsTheReport(t *testing.T) {
	h := captureLogs(t)
	m, _ := newTestManager(t)
	if err := m.Save(sample()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Path(), []byte("newapi: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Load()

	// Fix it: the file parses and carries a value.
	if err := os.WriteFile(m.Path(), []byte("newapi:\n  url: https://gateway.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(); err != nil {
		t.Fatalf("a valid file must be adopted: %v", err)
	}
	// Break it again, with different contents.
	if err := os.WriteFile(m.Path(), []byte("newapi: [unclosed again\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Load()

	if n := len(h.snapshot()); n != 2 {
		t.Errorf("records = %d, want 2 (the second breakage must be reported)", n)
	}
}
