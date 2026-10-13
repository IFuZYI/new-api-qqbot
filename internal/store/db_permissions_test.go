package store

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// captureStoreLog installs a JSON handler as the process default logger and
// returns a reader for what it recorded. The store logs through the
// package-level slog, so this is the only way to assert on its records.
func captureStoreLog(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&lockedWriter{mu: &mu, buf: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		storeLogCapture = nil
	})
	storeLogCapture = func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// loggedWarn reports whether the captured log carries a WARN with this message.
func loggedWarn(t *testing.T, msg string) bool {
	t.Helper()
	if storeLogCapture == nil {
		t.Fatal("loggedWarn needs captureStoreLog to be installed first")
	}
	out := storeLogCapture()
	return strings.Contains(out, `"level":"WARN"`) && strings.Contains(out, msg)
}

// storeLogCapture is set by captureStoreLog so loggedWarn can read it without
// threading the reader through every call.
var storeLogCapture func() string

// The database file, its WAL/SHM siblings and the data directory must be
// owner-only.
//
// The database holds the session-cookie signing key (settings.auth.session_key),
// the plaintext gateway password and a live 30-day gateway session — strictly
// more than data/config.yaml, which this codebase already hardens to 0600. With
// SQLite's default (the process umask, 0644 on the documented bare-metal run)
// any local user could read the signing key and mint an admin cookie without
// the password. An independent reviewer measured 0644 on all three files.
func TestDatabaseFilesAreOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := Open(filepath.Join(dir, "qqbot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// A write makes SQLite materialise the WAL/SHM siblings.
	if err := st.PutSetting("mode-probe", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("data dir mode = %04o, want 0700", got)
	}

	for _, name := range []string{"qqbot.db", "qqbot.db-wal", "qqbot.db-shm"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			// SQLite may have checkpointed the WAL away; only the database
			// itself is mandatory.
			if name == "qqbot.db" {
				t.Fatalf("%s is missing: %v", name, err)
			}
			continue
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600 (it holds the cookie signing key and the gateway password)", name, got)
		}
	}
}

// Reopening an existing world-readable database must tighten it: an operator
// upgrading from a version that created it 0644 gets the fix without a manual
// chmod.
func TestReopeningTightensAnExistingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "qqbot.db")

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// Simulate the old, world-readable state.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("after reopening, mode = %04o, want 0600", got)
	}
}

// A data directory that ALREADY exists must be tightened too, not just one this
// code creates.
//
// MkdirAll applies its mode only to directories it creates, so an existing
// directory keeps whatever it had. That is the Docker case: a named volume's
// mount point is created by Docker as 0755 + setgid, and it stayed 2755 on every
// deployment while the log directory inside it was 0700 — the directory holding
// the database and the logs was world-traversable. Found by running the real
// container.
func TestAnExistingWorldReadableDataDirIsTightened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := Open(filepath.Join(dir, "qqbot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("an existing data dir stayed %04o, want 0700", got)
	}
}

// A data directory that cannot be tightened must not stop the service.
//
// The chmod can fail (a read-only mount, a directory owned by another user), and
// refusing to start over a permission bit would be worse than the bit itself:
// the database files carry their own modes.
//
// This test MUST inject the failure: the suite runs as root, and root's chmod on
// its own directory always succeeds, so a test that simply calls Open never
// reaches the tolerant branch. An independent reviewer proved that by making the
// failure fatal — the original version of this test still passed. The chmodDir
// hook is what makes the property observable.
func TestAnUntightenableDataDirStillOpens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	captureStoreLog(t)

	// Make the chmod fail, as a foreign-owned or read-only directory would.
	orig := chmodDir
	chmodDir = func(string, os.FileMode) error { return os.ErrPermission }
	t.Cleanup(func() { chmodDir = orig })

	st, err := Open(filepath.Join(dir, "qqbot.db"))
	if err != nil {
		t.Fatalf("Open must not fail over a directory mode: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.PutSetting("still-works", map[string]any{"ok": true}); err != nil {
		t.Errorf("the store is unusable: %v", err)
	}
	// The failure must be REPORTED, not silently swallowed: the operator has to
	// learn that the directory could not be tightened.
	if !loggedWarn(t, "数据目录权限收紧失败") {
		t.Error("the chmod failure was not reported")
	}
}

// The chmod must not be pointed at a shared parent: `-data /var` or `-data /`
// would remove group/other access from unrelated directories.
func TestSharedParentsAreNotTightened(t *testing.T) {
	for _, dir := range []string{"/", "/var", "/etc", "/tmp", "/usr", "/home", "/srv", "/opt", "/root"} {
		if isOwnedDataDir(dir) {
			t.Errorf("isOwnedDataDir(%q) = true; a shared parent must not be chmod'd", dir)
		}
	}
	// A real data directory — the documented ones, and a relative path — must
	// still be tightened.
	for _, dir := range []string{"/app/data", "/home/qqbot/data", "/srv/qqbot/data", "data", "./data", "/tmp/qqbot", "/opt/qqbot/data", "/var/lib/qqbot"} {
		if !isOwnedDataDir(dir) {
			t.Errorf("isOwnedDataDir(%q) = false; a real data directory must be tightened", dir)
		}
	}
}

// A shared parent must still be CREATED when missing — the gate is on the chmod,
// not on MkdirAll. /tmp/qqbot-<random> is exactly that case.
func TestASharedParentIsStillCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A path whose parent is a shared directory that does NOT yet exist.
	shared := filepath.Join(dir, "sub")
	st, err := Open(filepath.Join(shared, "qqbot.db"))
	if err != nil {
		t.Fatalf("Open must create a missing parent even when it will not chmod it: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("the parent directory was not created: %v", err)
	}
}

// Open must not TOUCH a shared parent's mode.
//
// The predicate test above pins isOwnedDataDir's ANSWERS, not that Open consults
// it: removing the gate from Open's call site leaves that test green. This one
// records which paths Open actually chmods, so it fails if the gate is removed.
func TestOpenDoesNotChmodASharedParent(t *testing.T) {
	var chmodded []string
	orig := chmodDir
	chmodDir = func(path string, mode os.FileMode) error {
		chmodded = append(chmodded, path)
		return orig(path, mode)
	}
	t.Cleanup(func() { chmodDir = orig })

	// The chmod target is filepath.Dir(path) — the directory HOLDING the
	// database. So the refused-parent case is `-data /tmp` (the db sits directly
	// in /tmp), not `-data /tmp/qqbot` (whose dir is /tmp/qqbot, which is fine).
	// Use a uniquely-named database so nothing else in /tmp is disturbed, and
	// assert /tmp itself is never chmod'd.
	probe := filepath.Join("/tmp", fmt.Sprintf("qqbot-shared-probe-%d.db", os.Getpid()))
	st, err := Open(probe)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(probe + suffix)
		}
	})

	for _, p := range chmodded {
		if p == "/tmp" || p == "/" {
			t.Errorf("Open chmod'd %q, a shared parent; the gate is not wired in", p)
		}
	}
	// And the positive control: a normal data directory IS chmod'd, so the
	// assertion above cannot pass by chmodding nothing at all.
	chmodded = nil
	dir := filepath.Join(t.TempDir(), "data")
	st2, err := Open(filepath.Join(dir, "qqbot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	found := false
	for _, p := range chmodded {
		if p == dir {
			found = true
		}
	}
	if !found {
		t.Errorf("a normal data directory was not chmod'd at all (recorded: %v); the control is vacuous", chmodded)
	}
}
