package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A file whose mode cannot be tightened must not be reported as a CLEANUP
// failure.
//
// The sweep's job is deleting the expired files; tightening the mode of the ones
// it keeps is secondary. Folding a chmod failure into the returned error made
// Apply log 清理过期日志失败 on every hourly tick — while the file it named had in
// fact been removed. Found by running the package as a non-root user with a
// root-owned file inside the window (the reviewer's probe), which is the only way
// to make chmod fail: the suite itself runs as root, where chmod always succeeds.
//
// The chmod is injected here for the same reason: a real failure needs an
// identity the test does not have.
func TestAnUntightenableFileIsNotACleanupFailure(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	// One expired file (must be removed and COUNTED) and one kept file whose
	// chmod will fail.
	touch(t, dir, fileName("2026-10-08"))
	touch(t, dir, fileName("2026-10-12"))

	orig := chmodFile
	chmodFile = func(path string, mode os.FileMode) error {
		if strings.HasSuffix(path, fileName("2026-10-12")) {
			return os.ErrPermission
		}
		return orig(path, mode)
	}
	t.Cleanup(func() { chmodFile = orig })

	removed, err := m.Cleanup(3)
	if err != nil {
		t.Errorf("a chmod failure was reported as a cleanup failure: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (the expired file must still be counted)", removed)
	}
	// The expired file is gone; the kept one is still there.
	if _, err := os.Stat(filepath.Join(dir, fileName("2026-10-08"))); err == nil {
		t.Error("the expired file was not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, fileName("2026-10-12"))); err != nil {
		t.Errorf("the kept file was removed: %v", err)
	}
}

// The chmod failure must still be REPORTED — at WARN, naming the file — so the
// operator learns the mode could not be tightened without being told the cleanup
// failed.
func TestAnUntightenableFileIsReportedAtWarn(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2026-10-12"))

	orig := chmodFile
	chmodFile = func(string, os.FileMode) error { return os.ErrPermission }
	t.Cleanup(func() { chmodFile = orig })

	if _, err := m.Cleanup(3); err != nil {
		t.Fatal(err)
	}
	res, err := m.Query(Query{Day: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range res.Entries {
		if e.Msg == "日志文件权限收紧失败" {
			found = true
			if e.Level != "WARN" {
				t.Errorf("the record is at %s, want WARN (it is not a cleanup failure)", e.Level)
			}
			if e.Attrs["file"] != fileName("2026-10-12") {
				t.Errorf("the record does not name the file: %+v", e.Attrs)
			}
		}
		if e.Msg == "清理过期日志失败" {
			t.Errorf("the cleanup was reported as failed although it succeeded: %+v", e.Attrs)
		}
	}
	if !found {
		t.Error("the chmod failure was not reported at all")
	}
}

// A real REMOVAL failure is still a cleanup failure: the distinction must not
// swallow the error that matters.
//
// The failure is injected at removeExpired rather than provoked with a fixture:
// a non-empty directory named like a day file is SKIPPED by the e.IsDir() guard
// (deliberately — it is not this package's file), and as root nothing else in a
// temp dir resists deletion. The property under test is the error PROPAGATION
// from a removal, which is what the hook exercises.
func TestARemovalFailureIsStillACleanupFailure(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2026-10-01"))

	orig := removeFn
	removeFn = func(string) error { return os.ErrPermission }
	t.Cleanup(func() { removeFn = orig })

	_, err := m.Cleanup(3)
	if err == nil {
		t.Fatal("a real removal failure was swallowed")
	}
	// And the file is still there, because the removal really did fail.
	if _, statErr := os.Stat(filepath.Join(dir, fileName("2026-10-01"))); statErr != nil {
		t.Errorf("the file vanished although the removal reported failure: %v", statErr)
	}
}

// A removal failure and a chmod failure in the same sweep must be told apart:
// the removal error is returned, the chmod one is only logged.
func TestARemovalFailureIsReturnedEvenWhenAChmodAlsoFails(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	m, dir := openTest(t, clock)
	touch(t, dir, fileName("2026-10-01")) // expired: removal will fail
	touch(t, dir, fileName("2026-10-12")) // kept: chmod will fail

	origRemove, origChmod := removeFn, chmodFile
	removeFn = func(string) error { return os.ErrPermission }
	chmodFile = func(string, os.FileMode) error { return os.ErrPermission }
	t.Cleanup(func() { removeFn, chmodFile = origRemove, origChmod })

	_, err := m.Cleanup(3)
	if err == nil {
		t.Fatal("the removal failure was swallowed")
	}
	if !strings.Contains(err.Error(), "permission") {
		t.Errorf("the returned error does not describe the removal: %v", err)
	}
}
