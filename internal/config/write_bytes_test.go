package config

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeBytes is the atomic half of every config write: the file holds
// credentials, so it must land owner-only, must never be left behind when a
// step fails, and must be complete or absent - a truncated config would fail to
// parse on the next start.
func TestWriteBytesCreatesACompleteOwnerOnlyFile(t *testing.T) {
	m, _ := newTestManager(t)
	content := []byte("hello: world\n")

	if err := m.writeBytes(content); err != nil {
		t.Fatalf("writeBytes: %v", err)
	}
	got, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
	info, err := os.Stat(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600: the file holds credentials", perm)
	}

	// A second write replaces the file rather than appending to it.
	if err := m.writeBytes([]byte("second: 1\n")); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second: 1\n" {
		t.Errorf("content after rewrite = %q", got)
	}

	// The directory is created on demand and is owner-only too.
	if perm := dirPerm(t, filepath.Dir(m.Path())); perm != 0o700 {
		t.Errorf("dir mode = %o, want 700", perm)
	}
}

// The temp file is written beside the destination and removed on every failure
// path, so a failed write cannot leave a stray ".config-*.yaml" (with
// credentials in it) in the operator's data directory.
func TestWriteBytesLeavesNoTempFileBehind(t *testing.T) {
	m, _ := newTestManager(t)
	dir := filepath.Dir(m.Path())
	dest := m.Path()

	// A destination that cannot be created (the parent is a file) fails at
	// MkdirAll, and must not leave anything in the directory.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.path = filepath.Join(blocker, "sub", "config.yaml")
	if err := m.writeBytes([]byte("x")); err == nil {
		t.Fatal("expected a failure when the parent path is a file")
	}
	m.path = dest

	// A destination that cannot be renamed onto (it is a directory) fails at
	// the last step, after the temp file exists: it must be cleaned up.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.writeBytes([]byte("x")); err == nil {
		t.Fatal("expected a failure when the destination is a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Errorf("unexpected directory contents: %+v", entries)
	}

	// A write into a directory that does not exist yet must succeed: the data
	// directory is created on demand.
	m2, _ := newTestManager(t)
	m2.path = filepath.Join(t.TempDir(), "missing-dir", FileName)
	if err := m2.writeBytes([]byte("ok")); err != nil {
		t.Fatalf("a write into a new directory must succeed: %v", err)
	}
}

func dirPerm(t *testing.T, dir string) os.FileMode {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	return info.Mode().Perm()
}

// A write that runs out of room must leave the previous config in place and no
// temp file behind. The destination holds the operator's only copy of the
// credentials, so a half-written replacement would lock them out on the next
// start. A full filesystem is the portable way to reach that branch; where the
// test cannot create one it is skipped rather than silently passing.
func TestWriteBytesLeavesTheOldFileOnAFailedWrite(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs privileges to mount a full filesystem")
	}
	base := t.TempDir()
	mountPoint := filepath.Join(base, "tiny")
	if err := os.Mkdir(mountPoint, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mount", "-t", "tmpfs", "-o", "size=64k", "tmpfs", mountPoint).CombinedOutput(); err != nil {
		t.Skipf("cannot mount a tmpfs here: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("umount", mountPoint).Run() })

	m, _ := newTestManager(t)
	dest := filepath.Join(mountPoint, FileName)
	m.path = dest
	if err := m.writeBytes([]byte("original: true\n")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Fill the filesystem so the next temp file can be created but not written.
	filler, err := os.Create(filepath.Join(mountPoint, "filler.bin"))
	if err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 4096)
	for {
		if _, err := filler.Write(block); err != nil {
			break
		}
	}
	if err := filler.Close(); err != nil {
		t.Fatal(err)
	}

	big := make([]byte, 32*1024)
	if err := m.writeBytes(big); err == nil {
		t.Fatal("expected a write failure on a full filesystem")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("the previous config was lost: %v", err)
	}
	if string(got) != "original: true\n" {
		t.Errorf("the destination was partially overwritten: %q", got)
	}
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("a temp file was left behind on a full filesystem: %s", e.Name())
		}
	}

	// A data directory that cannot be written at all (a read-only mount) must
	// surface as an error from the temp-file step, not as a silent success that
	// leaves the operator believing the config was saved.
	roDir := filepath.Join(base, "readonly")
	if err := os.Mkdir(roDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mount", "-t", "tmpfs", "tmpfs", roDir).CombinedOutput(); err != nil {
		t.Skipf("cannot mount a second tmpfs: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("umount", roDir).Run() })
	if out, err := exec.Command("mount", "-o", "remount,ro", roDir).CombinedOutput(); err != nil {
		t.Skipf("cannot remount read-only: %v %s", err, out)
	}
	m2, _ := newTestManager(t)
	m2.path = filepath.Join(roDir, FileName)
	if err := m2.writeBytes([]byte("x")); err == nil {
		t.Error("a write into a read-only directory must fail loudly")
	}
}

// EnsureFile is the "give the operator something to edit" path, and it seeds
// from the database so a WebUI-configured instance does not come back empty.
func TestEnsureFileSeedsFromTheStoreAndLeavesContentAlone(t *testing.T) {
	m, st := newTestManager(t)
	in := sample()
	if err := m.saveToStore(in); err != nil {
		t.Fatal(err)
	}

	if err := m.EnsureFile(); err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}
	raw, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatalf("EnsureFile did not create the file: %v", err)
	}
	if !strings.Contains(string(raw), in.NewAPI.URL) {
		t.Errorf("the file was not seeded from the store:\n%s", raw)
	}
	if !strings.HasPrefix(string(raw), "#") {
		t.Errorf("the header is missing:\n%s", raw)
	}
	_ = st

	// Running it again must not touch what is there: the file is the operator's
	// to edit, and re-seeding would silently undo a hand edit.
	if err := os.WriteFile(m.Path(), []byte("hand: edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureFile(); err != nil {
		t.Fatalf("EnsureFile on an existing file: %v", err)
	}
	got, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hand: edited\n" {
		t.Errorf("EnsureFile overwrote an existing file: %q", got)
	}
}

// A path whose parent is a file cannot be stat'ed at all: the failure is not
// "no such file", so it must be reported rather than treated as "create it".
// The error must be the stat's own, not one from a write that should never have
// been attempted.
func TestEnsureFileReportsAnUnusablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := newTestManager(t)
	m.path = filepath.Join(blocker, "sub", FileName)

	err := m.EnsureFile()
	if err == nil {
		t.Fatal("EnsureFile must report a path it cannot even stat")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("a non-ENOENT failure must not be reported as a missing file: %v", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "stat" {
		t.Errorf("the stat failure itself must be returned, got %v", err)
	}
}
