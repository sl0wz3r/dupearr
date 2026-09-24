package config

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLoadCreatesDataDirOwnerOnly: the data directory holds config.xml, the database, backups
// and logs, so a new one must not be readable by other users (SEC-016).
func TestLoadCreatesDataDirOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "new", "Dupearr")
	mustLoad(t, dir)
	if got := fileMode(t, dir); got != 0o700 {
		t.Fatalf("data directory mode = %v, want 0700", got)
	}
}

func TestRewriteFileKeepsUnknownContentAndAppliesFn(t *testing.T) {
	t.Parallel()
	c := defaultWithKey()
	c.InstanceName = "Backup"
	in := strings.Replace(canonical(c), "</Config>", "  <!-- note -->\n  <Custom>x</Custom>\n</Config>", 1)
	out, got, err := RewriteFile([]byte("\xEF\xBB\xBF"+in), func(c *Config) { c.Port = 8080; c.UrlBase = "dupe/" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 8080 || got.UrlBase != "/dupe" || got.InstanceName != "Backup" || got.ApiKey != testKey {
		t.Errorf("config = %+v", got)
	}
	s := string(out)
	for _, want := range []string{"<Port>8080</Port>", "<UrlBase>/dupe</UrlBase>", "<!-- note -->", "<Custom>x</Custom>"} {
		if !strings.Contains(s, want) {
			t.Errorf("rewritten file lacks %s:\n%s", want, s)
		}
	}
	if strings.HasPrefix(s, "\xEF\xBB\xBF") {
		t.Error("byte order mark kept")
	}
	// Parsing the result yields the same config.
	if again, err := ParseFile(out); err != nil || again != got {
		t.Errorf("ParseFile(rewritten) = %+v, %v", again, err)
	}

	for _, bad := range []string{"{json}", "<Other/>", "<Config><Port>abc</Port></Config>", strings.Repeat(" ", maxFileSize+1)} {
		if _, err := ParseFile([]byte(bad)); err == nil {
			t.Errorf("ParseFile(%.20q) accepted", bad)
		}
	}
}

// TestParseFileIgnoresEnv: environment overrides are never applied (not parallel: t.Setenv).
func TestParseFileIgnoresEnv(t *testing.T) {
	t.Setenv("DUPEARR__SERVER__PORT", "9999")
	got, err := ParseFile([]byte("<Config><Port>1234</Port></Config>"))
	if err != nil || got.Port != 1234 {
		t.Errorf("ParseFile = %d, %v; want the file's port", got.Port, err)
	}
}

// TestCheckFileValidatesWithoutEnvOverrides: a config that is only valid because an environment
// variable masks one of its values must be refused (SEC-008), except for problems the current
// config.xml already has.
func TestCheckFileValidatesWithoutEnvOverrides(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, canonical(defaultWithKey()), 0o600)
	m, err := load(dir, []string{"DUPEARR__SERVER__PORT=8080"})
	if err != nil {
		t.Fatal(err)
	}
	next := m.File()
	if err := m.CheckFile(next); err != nil {
		t.Fatalf("current file refused: %v", err)
	}
	next.Port = 99999 // masked by DUPEARR__SERVER__PORT
	var verrs ValidationErrors
	if err := m.CheckFile(next); !errors.As(err, &verrs) || verrs[0].PropertyName != "port" {
		t.Fatalf("env-masked invalid port: err = %v, want a port validation error", err)
	}
	next = m.File()
	next.LogLevel = "loud" // not overridden: invalid either way
	if err := m.CheckFile(next); !errors.As(err, &verrs) {
		t.Fatalf("invalid log level accepted: %v", err)
	}
	if m.File().Port != DefaultPort || m.Get().Port != 8080 {
		t.Errorf("File() = %d, Get() = %d", m.File().Port, m.Get().Port)
	}
}
