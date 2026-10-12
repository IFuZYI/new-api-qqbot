package logging

import (
	"log"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Installed as the process default, both slog's package functions and the
// standard library's log.Printf reach the daily file. That is what lets a call
// site anywhere in the binary (or in a dependency) be found from the panel.
func TestInstallDefaultCapturesStdlibLog(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	prevSlog, prevFlags, prevWriter := slog.Default(), log.Flags(), log.Writer()
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetFlags(prevFlags)
		log.SetOutput(prevWriter)
	})

	m.InstallDefault()
	log.Printf("legacy line %d", 7)
	slog.Error("slog line", "err", "boom")

	body := readFile(t, filepath.Join(dir, "qqbot-2026-10-12.log"))
	if !strings.Contains(body, "legacy line 7") {
		t.Errorf("log.Printf did not reach the file: %q", body)
	}
	if !strings.Contains(body, `"level":"ERROR","msg":"slog line"`) {
		t.Errorf("slog.Error did not reach the file: %q", body)
	}
}
