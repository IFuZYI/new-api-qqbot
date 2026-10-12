package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qqbot/internal/logging"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// The start-up logging sequence must install the operator's calendar BEFORE the
// first Apply.
//
// Apply writes the level record and runs the retention sweep, and both are
// day-relative. Reversing the two was invisible to every test in
// internal/logging (they call SetLocation/Apply themselves), while on a UTC host
// with an Asia/Shanghai operator the start-up record landed in the previous
// day's file and the retention boundary was evaluated against the wrong day.
//
// The host here is UTC and the operator is Pacific/Kiritimati (UTC+14), so the
// two calendars are on different DATES: the assertion cannot pass by accident.
func TestStartupLoggingUsesTheOperatorsCalendar(t *testing.T) {
	// A host clock of 2026-10-12 19:00 UTC is 2026-10-13 09:00 in UTC+14.
	host := time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	st, err := store.Open(filepath.Join(dir, "startup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := service.New(st, nil, nil)

	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.Timezone = "Pacific/Kiritimati"
	// A level that differs from the logger's default, so Apply has something to
	// record (it only logs when the level actually changes).
	s.LogLevel = logging.LevelDebug
	if err := svc.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	logDir := filepath.Join(dir, "logs")
	logs, err := logging.Open(logging.Options{
		Dir: logDir, Stderr: io.Discard, Now: func() time.Time { return host },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	// main installs the manager as the process logger before this point, which is
	// what routes the 数据目录 record into the day file.
	logs.InstallDefault()

	applyStartupLogging(logs, svc, "test-data")

	// The start-up record must be in the OPERATOR's day file.
	if _, err := os.Stat(filepath.Join(logDir, "qqbot-2026-10-13.log")); err != nil {
		t.Errorf("the start-up record is not in the operator's day file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "qqbot-2026-10-12.log")); err == nil {
		t.Error("a file was written under the HOST's day: the calendar was installed after the first Apply")
	}
}

// A broken timezone in the settings must not stop start-up: the logger keeps the
// host's calendar and the reason is recorded.
func TestStartupLoggingSurvivesABadTimezone(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "startup2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := service.New(st, nil, nil)
	s, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.Timezone = "Not/AZone"
	s.LogLevel = logging.LevelDebug
	// Written through the STORE, not SaveSettings: the service validates the
	// timezone on save, so an unusable value can only reach the reader from a
	// hand-edited or legacy row. The reader must still survive it.
	if err := st.PutSetting("settings", s); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	host := time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC)
	logs, err := logging.Open(logging.Options{
		Dir: filepath.Join(dir, "logs"), Stderr: &stderr, Now: func() time.Time { return host },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	logs.InstallDefault()

	applyStartupLogging(logs, svc, "test-data")

	// It must not panic and must still log somewhere: the host's day, since the
	// operator's zone is unusable.
	if _, err := os.Stat(filepath.Join(dir, "logs", "qqbot-2026-10-12.log")); err != nil {
		t.Errorf("a bad timezone broke start-up logging entirely: %v", err)
	}
	_ = slog.LevelInfo
}
