package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvReadErrorsPreserveProcessValues(t *testing.T) {
	for _, kind := range []string{"directory", "scanner"} {
		t.Run(kind, func(t *testing.T) {
			path := t.TempDir()
			if kind == "scanner" {
				path = filepath.Join(path, ".env")
				if err := os.WriteFile(path, []byte("QQBOT_ADMIN_USERNAME=partial\n"+strings.Repeat("x", 1024*1024+1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(EnvFilePath, path)
			t.Setenv(EnvAdminPassword, "process-secret")
			env, err := LoadEnv(t.TempDir())
			if err == nil {
				t.Error("read failure was silently accepted")
			}
			if env[EnvAdminPassword] != "process-secret" {
				t.Error("process credential lost on file error")
			}
			if env[EnvAdminUsername] == "partial" {
				t.Error("partial file values must not be used")
			}
		})
	}
}
