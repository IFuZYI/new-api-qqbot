package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	raw := `# a comment
QQBOT_ADMIN_USERNAME=admin

export QQBOT_ADMIN_PASSWORD=secret-pw
  QQBOT_OTHER = spaced 
EMPTY=
QUOTED="has spaces"
SINGLE='single quoted'
HASH=pa#ss#word
NOEQUALS
`
	got := ParseEnv(strings.NewReader(raw))

	want := map[string]string{
		"QQBOT_ADMIN_USERNAME": "admin",
		"QQBOT_ADMIN_PASSWORD": "secret-pw",
		"QQBOT_OTHER":          "spaced",
		"EMPTY":                "",
		"QUOTED":               "has spaces",
		"SINGLE":               "single quoted",
		// A password may contain '#': only a leading '#' starts a comment, so
		// the rest of the value must survive untouched.
		"HASH": "pa#ss#word",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["NOEQUALS"]; ok {
		t.Error("a line without '=' should be ignored")
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d keys, want %d: %v", len(got), len(want), got)
	}
}

// A Windows-style CRLF file must not leave a stray \r in the password.
func TestParseEnvStripsCarriageReturn(t *testing.T) {
	got := ParseEnv(strings.NewReader("QQBOT_ADMIN_PASSWORD=secret\r\nQQBOT_ADMIN_USERNAME=admin\r\n"))
	if got["QQBOT_ADMIN_PASSWORD"] != "secret" {
		t.Errorf("password = %q, want %q", got["QQBOT_ADMIN_PASSWORD"], "secret")
	}
	if got["QQBOT_ADMIN_USERNAME"] != "admin" {
		t.Errorf("username = %q", got["QQBOT_ADMIN_USERNAME"])
	}
}

func TestLoadEnvFileMissingIsNotAnError(t *testing.T) {
	got, err := LoadEnvFile(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatalf("a missing .env must not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want an empty map, got %v", got)
	}
}

func TestLoadEnvPrefersProcessEnvAndReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("QQBOT_ADMIN_PASSWORD=from-file\nQQBOT_ONLY_IN_FILE=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("QQBOT_ADMIN_PASSWORD", "from-process")
	got, err := LoadEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got["QQBOT_ADMIN_PASSWORD"] != "from-process" {
		t.Errorf("the process environment must win: %q", got["QQBOT_ADMIN_PASSWORD"])
	}
	if got["QQBOT_ONLY_IN_FILE"] != "x" {
		t.Errorf("the file's other keys must survive: %v", got)
	}
}

func TestAdminCredsFromEnv(t *testing.T) {
	t.Run("unset means do not touch the stored admin", func(t *testing.T) {
		c := AdminCredsFromEnv(map[string]string{})
		if c.Set {
			t.Error("no password configured must leave Set false")
		}
	})

	t.Run("a whitespace-only password counts as unset", func(t *testing.T) {
		if AdminCredsFromEnv(map[string]string{EnvAdminPassword: "   "}).Set {
			t.Error("a blank password must not be adopted")
		}
	})

	t.Run("the username defaults to admin", func(t *testing.T) {
		c := AdminCredsFromEnv(map[string]string{EnvAdminPassword: "hunter2hunter2"})
		if !c.Set || c.Username != DefaultAdminUsername {
			t.Errorf("got %+v, want username %q", c, DefaultAdminUsername)
		}
	})

	t.Run("both values are honoured", func(t *testing.T) {
		c := AdminCredsFromEnv(map[string]string{
			EnvAdminUsername: "root",
			EnvAdminPassword: "hunter2hunter2",
		})
		if !c.Set || c.Username != "root" || c.Password != "hunter2hunter2" {
			t.Errorf("unexpected: %+v", c)
		}
	})
}

// The .env file is the documented way to set the admin, so the default path is
// the data directory, and QQBOT_ENV_FILE overrides it.
func TestResolveEnvFilePath(t *testing.T) {
	t.Setenv(EnvFilePath, "")
	if got := ResolveEnvFilePath("/data"); got != filepath.Join("/data", ".env") {
		t.Errorf("default path = %q", got)
	}
	t.Setenv(EnvFilePath, "/etc/qqbot.env")
	if got := ResolveEnvFilePath("/data"); got != "/etc/qqbot.env" {
		t.Errorf("override path = %q", got)
	}
}

// The data directory's .env is the documented location; a checkout that keeps
// its .env beside the code still works as a fallback.
func TestLoadEnvFallsBackToTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir() // empty data dir, no .env in it
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("QQBOT_ADMIN_PASSWORD=from-cwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	got, err := LoadEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got["QQBOT_ADMIN_PASSWORD"] != "from-cwd" {
		t.Errorf("the working-directory .env was not used: %v", got)
	}
}

// The data directory wins when both exist.
func TestLoadEnvPrefersTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("QQBOT_ADMIN_PASSWORD=from-data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("QQBOT_ADMIN_PASSWORD=from-cwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	got, err := LoadEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got["QQBOT_ADMIN_PASSWORD"] != "from-data" {
		t.Errorf("want the data-directory value, got %v", got)
	}
}

// A file that exists but is empty must not silently fall through to another
// file the operator did not mean.
func TestLoadEnvStopsAtAnExistingEmptyFile(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("# intentionally empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("QQBOT_ADMIN_PASSWORD=from-cwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	got, err := LoadEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got["QQBOT_ADMIN_PASSWORD"] != "" {
		t.Errorf("an existing but empty file should stop the search, got %v", got)
	}
}
