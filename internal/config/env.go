package config

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment variables read at startup. The connection settings are NOT read
// from here: those live in config.yaml / the WebUI, which is where they can be
// edited without a restart. The admin credential is different - it is the thing
// that gates the panel, so it is provisioned before the panel is reachable.
const (
	// EnvAdminUsername overrides the admin account name. Defaults to
	// DefaultAdminUsername.
	EnvAdminUsername = "QQBOT_ADMIN_USERNAME"
	// EnvAdminPassword sets the admin password. When set (and non-blank) the
	// admin account is created or its password reset at startup.
	EnvAdminPassword = "QQBOT_ADMIN_PASSWORD"
	// EnvPort sets the WebUI's listen address. A bare port ("4096") is
	// accepted as well as a full address (":4096", "127.0.0.1:4096"), so a
	// provisioning template that only knows the port does not have to know
	// Go's address syntax.
	EnvPort = "QQBOT_PORT"
	// EnvFilePath points at an env file other than <data>/.env.
	EnvFilePath = "QQBOT_ENV_FILE"

	// EnvFileName is the env file looked for inside the data directory.
	EnvFileName = ".env"

	// DefaultAdminUsername is used when only a password is configured.
	DefaultAdminUsername = "admin"
)

// AdminCreds is the admin credential supplied through the environment.
type AdminCreds struct {
	Set      bool
	Username string
	Password string
}

// NormalizeAddr turns a configured listen value into a Go listen address.
//
// A bare port ("4096") becomes ":4096"; anything that already looks like an
// address (":4096", "127.0.0.1:4096", "[::1]:4096") is returned unchanged. It
// returns "" for a blank value, meaning "no override".
//
// It is deliberately permissive about what it *accepts* (an operator typing
// "4096" is the obvious case) but strict about what it returns: a value it
// cannot make sense of is passed through so net.Listen reports it, rather than
// being silently rewritten into something the operator did not ask for.
func NormalizeAddr(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// All digits: a bare port. Prefix the colon so net.Listen binds on every
	// interface, matching the -addr flag's ":4096" default.
	if _, err := strconv.Atoi(v); err == nil {
		return ":" + v
	}
	return v
}

// EnvPortFromEnv returns the normalized listen address from the environment,
// or "" when it is not configured.
func EnvPortFromEnv(env map[string]string) string {
	return NormalizeAddr(env[EnvPort])
}

// AdminCredsFromEnv builds the credential from an already-merged environment.
//
// A blank password means "leave the stored admin alone", so an empty
// QQBOT_ADMIN_PASSWORD (a common accident in a provisioning template) cannot
// reset the panel's password to nothing.
func AdminCredsFromEnv(env map[string]string) AdminCreds {
	pw := strings.TrimSpace(env[EnvAdminPassword])
	if pw == "" {
		return AdminCreds{}
	}
	user := strings.TrimSpace(env[EnvAdminUsername])
	if user == "" {
		user = DefaultAdminUsername
	}
	return AdminCreds{Set: true, Username: user, Password: pw}
}

// ResolveEnvFilePath returns the env file to read: QQBOT_ENV_FILE when set,
// otherwise .env inside the data directory. Putting it in the data directory
// keeps the whole instance (database, config.yaml, .env) in one place.
func ResolveEnvFilePath(dataDir string) string {
	if p := strings.TrimSpace(os.Getenv(EnvFilePath)); p != "" {
		return p
	}
	return filepath.Join(dataDir, EnvFileName)
}

// EnvFileCandidates lists the env files to try, in precedence order. The
// data directory comes first because that is the documented location; the
// working directory is accepted as a fallback so a checkout that keeps its .env
// beside the code still works.
func EnvFileCandidates(dataDir string) []string {
	if p := strings.TrimSpace(os.Getenv(EnvFilePath)); p != "" {
		return []string{p}
	}
	return []string{filepath.Join(dataDir, EnvFileName), EnvFileName}
}

// LoadEnv merges the process environment with the env file.
//
// The process environment wins, so a container or systemd unit can override
// what the file says without editing it. A missing file is normal, not an
// error: the service must still start when everything is configured in the
// WebUI.
func LoadEnv(dataDir string) (map[string]string, error) {
	out := map[string]string{}
	fileVals, err := loadFirstEnvFile(EnvFileCandidates(dataDir))
	if err != nil {
		return nil, err
	}
	for k, v := range fileVals {
		out[k] = v
	}
	for _, k := range []string{EnvAdminUsername, EnvAdminPassword, EnvPort} {
		if v, ok := os.LookupEnv(k); ok {
			out[k] = v
		}
	}
	return out, nil
}

// loadFirstEnvFile reads the first candidate that exists.
func loadFirstEnvFile(paths []string) (map[string]string, error) {
	for _, p := range paths {
		vals, err := LoadEnvFile(p)
		if err != nil {
			return nil, err
		}
		if len(vals) > 0 {
			return vals, nil
		}
		if _, err := os.Stat(p); err == nil {
			// The file exists but carries nothing; stop rather than silently
			// falling through to a different file the operator did not mean.
			return vals, nil
		}
	}
	return map[string]string{}, nil
}

// LoadEnvFile parses a .env file. A missing file yields an empty map.
func LoadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseEnv(f), nil
}

// ParseEnv reads KEY=VALUE lines.
//
// This is deliberately small rather than a full shell parser: the file only
// ever holds a handful of simple assignments. It supports comments, a leading
// "export", surrounding whitespace and single/double quotes. A '#' only starts
// a comment at the start of a line, so a password containing '#' survives.
func ParseEnv(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	// A long password or token should not be truncated at the default 64K.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = unquoteEnvValue(strings.TrimSpace(value))
	}
	return out
}

// unquoteEnvValue strips a matching pair of surrounding quotes and, inside
// double quotes, the usual backslash escapes.
func unquoteEnvValue(v string) string {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		inner := v[1 : len(v)-1]
		inner = strings.ReplaceAll(inner, `\n`, "\n")
		inner = strings.ReplaceAll(inner, `\t`, "\t")
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner
	}
	return v
}
