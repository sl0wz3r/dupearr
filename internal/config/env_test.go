package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These tests use t.Setenv and therefore must not run in parallel.

func TestEnvOverridesAllKeys(t *testing.T) {
	env := map[string]string{
		"DUPEARR__SERVER__BINDADDRESS": "127.0.0.1",
		"DUPEARR__SERVER__PORT":        "8181",
		"DUPEARR__SERVER__URLBASE":     "/dupe/",
		"DUPEARR__SERVER__ENABLESSL":   "true",
		"DUPEARR__SERVER__SSLPORT":     "8443",
		"DUPEARR__SERVER__SSLCERTPATH": "/certs/tls.crt",
		"DUPEARR__SERVER__SSLKEYPATH":  "/certs/tls.key",
		"DUPEARR__AUTH__APIKEY":        "ffffffffffffffffffffffffffffffff",
		"DUPEARR__AUTH__METHOD":        "External",
		"DUPEARR__AUTH__REQUIRED":      "DisabledForLocalAddresses",
		"DUPEARR__LOG__LEVEL":          "Debug",
		"DUPEARR__LOG__SIZELIMIT":      "5",
		"DUPEARR__APP__INSTANCENAME":   "Dupearr Env",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := m.Get()
	want := Config{
		BindAddress: "127.0.0.1", Port: 8181, UrlBase: "/dupe", EnableSsl: true, SslPort: 8443,
		SslCertPath: "/certs/tls.crt", SslKeyPath: "/certs/tls.key", ApiKey: "ffffffffffffffffffffffffffffffff",
		AuthenticationMethod: AuthExternal, AuthenticationRequired: AuthRequiredDisabledForLocal,
		LogLevel: "debug", LogSizeLimit: 5, InstanceName: "Dupearr Env", LaunchBrowser: false, Branch: "main",
	}
	if got != want {
		t.Fatalf("Get() = %+v\nwant %+v", got, want)
	}
	wantNames := []string{"bindAddress", "port", "urlBase", "enableSsl", "sslPort", "sslCertPath", "sslKeyPath",
		"apiKey", "authenticationMethod", "authenticationRequired", "logLevel", "logSizeLimit", "instanceName"}
	if names := m.EnvOverrides(); !slices.Equal(names, wantNames) {
		t.Fatalf("EnvOverrides() = %v, want %v", names, wantNames)
	}

	// None of the environment values reached config.xml: it holds defaults and its own key.
	file := readFile(t, m.Path())
	for _, v := range env {
		if strings.Contains(file, ">"+strings.TrimRight(v, "/")+"<") {
			t.Errorf("env value %q written to config.xml:\n%s", v, file)
		}
	}
	fromFile := mustLoad(t, dir).Get() // no environment
	if fromFile.ApiKey == want.ApiKey || !hexKey.MatchString(fromFile.ApiKey) {
		t.Fatalf("file ApiKey = %q", fromFile.ApiKey)
	}
	wantFile := Default()
	wantFile.ApiKey = fromFile.ApiKey
	if fromFile != wantFile {
		t.Fatalf("file config = %+v, want defaults", fromFile)
	}
}

func TestEnvOverrideNamesAreCaseInsensitive(t *testing.T) {
	t.Setenv("dupearr__server__port", "8282")
	t.Setenv("Dupearr__Log__Level", "trace")
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Get(); c.Port != 8282 || c.LogLevel != "trace" {
		t.Fatalf("Get() = %+v", c)
	}
	if names := m.EnvOverrides(); !slices.Equal(names, []string{"port", "logLevel"}) {
		t.Fatalf("EnvOverrides() = %v", names)
	}
}

func TestEnvOverridesAreNeverWrittenBack(t *testing.T) {
	t.Setenv("DUPEARR__SERVER__PORT", "9000")
	t.Setenv("DUPEARR__AUTH__APIKEY", "envkeyenvkeyenvkeyenvkey")
	dir := t.TempDir()
	c := defaultWithKey()
	c.Port = 1234
	p := writeFile(t, dir, canonical(c), 0o600)

	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var seenOld, seenNew Config
	m.OnChange(func(o, n Config) { seenOld, seenNew = o, n })

	var fnSaw Config
	got, err := m.Update(func(c *Config) {
		fnSaw = *c
		c.Port = 9000 // e.g. the UI echoing the effective value back
		c.ApiKey = GenerateAPIKey()
		c.InstanceName = "Changed"
	})
	if err != nil {
		t.Fatal(err)
	}
	if fnSaw.Port != 1234 || fnSaw.ApiKey != testKey {
		t.Fatalf("fn saw the overridden view, not the file: %+v", fnSaw)
	}
	if got.Port != 9000 || got.ApiKey != "envkeyenvkeyenvkeyenvkey" || got.InstanceName != "Changed" {
		t.Fatalf("Update() = %+v", got)
	}
	if seenOld.Port != 9000 || seenNew.Port != 9000 || seenOld.InstanceName != "Dupearr" || seenNew.InstanceName != "Changed" {
		t.Fatalf("OnChange got old=%+v new=%+v, want effective configs", seenOld, seenNew)
	}

	file := readFile(t, p)
	want := c
	want.InstanceName = "Changed"
	if file != canonical(want) {
		t.Fatalf("config.xml:\n%s\nwant:\n%s", file, canonical(want))
	}
	if strings.Contains(file, "9000") || strings.Contains(file, "envkey") {
		t.Fatalf("override written back:\n%s", file)
	}
}

func TestEnvAuthMethodNormalised(t *testing.T) {
	tests := map[string]string{"basic": AuthForms, "BASIC": AuthForms, "forms": AuthForms, "none": AuthNone, "External": AuthExternal}
	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			t.Setenv("DUPEARR__AUTH__METHOD", in)
			t.Setenv("DUPEARR__AUTH__REQUIRED", "disabledforlocaladdresses")
			m, err := Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if c := m.Get(); c.AuthenticationMethod != want || c.AuthenticationRequired != AuthRequiredDisabledForLocal {
				t.Fatalf("Get() = %+v", c)
			}
		})
	}
}

func TestEnvEmptyValueIsIgnored(t *testing.T) {
	t.Setenv("DUPEARR__SERVER__PORT", "")
	t.Setenv("DUPEARR__SERVER__URLBASE", "  ")
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Get(); c.Port != DefaultPort || c.UrlBase != "" {
		t.Fatalf("Get() = %+v", c)
	}
	if names := m.EnvOverrides(); len(names) != 0 {
		t.Fatalf("EnvOverrides() = %v", names)
	}
}

func TestEnvInvalidValues(t *testing.T) {
	tests := []struct{ name, key, value, wantErr string }{
		{"port not a number", "DUPEARR__SERVER__PORT", "http", "DUPEARR__SERVER__PORT"},
		{"port out of range", "DUPEARR__SERVER__PORT", "70000", "port: Must be between"},
		{"bool", "DUPEARR__SERVER__ENABLESSL", "maybe", "DUPEARR__SERVER__ENABLESSL"},
		{"ssl without cert", "DUPEARR__SERVER__ENABLESSL", "True", "sslCertPath"},
		{"size limit", "DUPEARR__LOG__SIZELIMIT", "big", "DUPEARR__LOG__SIZELIMIT"},
		{"size limit range", "DUPEARR__LOG__SIZELIMIT", "0", "logSizeLimit"},
		{"auth method", "DUPEARR__AUTH__METHOD", "Kerberos", "authenticationMethod"},
		{"auth required", "DUPEARR__AUTH__REQUIRED", "Never", "authenticationRequired"},
		{"log level", "DUPEARR__LOG__LEVEL", "verbose", "logLevel"},
		{"url base traversal", "DUPEARR__SERVER__URLBASE", "/a/../b", "urlBase"},
		{"url base query", "DUPEARR__SERVER__URLBASE", "/a?x=1", "urlBase"},
		{"bind address", "DUPEARR__SERVER__BINDADDRESS", "example.com", "bindAddress"},
		{"api key", "DUPEARR__AUTH__APIKEY", "short", "apiKey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			dir := t.TempDir()
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() err = %v, want containing %q", err, tt.wantErr)
			}
			if _, statErr := os.Stat(filepath.Join(dir, FileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("config.xml written despite invalid environment (stat: %v)", statErr)
			}
		})
	}
}

func TestEnvUpdateValidatesEffectiveConfig(t *testing.T) {
	t.Setenv("DUPEARR__SERVER__SSLPORT", "8080")
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := readFile(t, m.Path())
	_, err = m.Update(func(c *Config) {
		c.Port = 8080
		c.EnableSsl, c.SslCertPath, c.SslKeyPath = true, "/c", "/k"
	})
	var verrs ValidationErrors
	if !errors.As(err, &verrs) || len(verrs) != 1 || verrs[0].PropertyName != "sslPort" {
		t.Fatalf("err = %v, want sslPort validation error", err)
	}
	if readFile(t, m.Path()) != before {
		t.Fatal("file changed after failed update")
	}
}

func TestEnvUpdateKeepsConfigFileLoadable(t *testing.T) {
	t.Setenv("DUPEARR__SERVER__PORT", "8080")
	dir := t.TempDir()
	writeFile(t, dir, canonical(defaultWithKey()), 0o600) // Port 3873 in the file
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := readFile(t, m.Path())

	// Valid with the override (8080 ≠ 3873), but config.xml alone would have SslPort == Port and
	// Dupearr would refuse to start once DUPEARR__SERVER__PORT is removed.
	_, err = m.Update(func(c *Config) {
		c.EnableSsl, c.SslPort, c.SslCertPath, c.SslKeyPath = true, DefaultPort, "/c", "/k"
	})
	var verrs ValidationErrors
	if !errors.As(err, &verrs) || len(verrs) != 1 || verrs[0].PropertyName != "sslPort" ||
		!strings.Contains(verrs[0].ErrorMessage, "config.xml") {
		t.Fatalf("err = %v, want an sslPort error about config.xml", err)
	}
	if readFile(t, m.Path()) != before {
		t.Fatal("file changed after failed update")
	}
	if got := m.Get(); got.EnableSsl {
		t.Fatalf("state changed after failed update: %+v", got)
	}

	// Without the override the saved file loads again.
	if _, err := m.Update(func(c *Config) { c.EnableSsl, c.SslPort, c.SslCertPath, c.SslKeyPath = true, 9443, "/c", "/k" }); err != nil {
		t.Fatal(err)
	}
	if _, err := load(dir, nil); err != nil {
		t.Fatalf("config.xml does not load without the override: %v", err)
	}
}

func TestEnvUpdateToleratesPreexistingFileProblems(t *testing.T) {
	// config.xml is only valid thanks to the override (LogLevel "loud" is replaced by env);
	// unrelated updates must still work.
	t.Setenv("DUPEARR__LOG__LEVEL", "debug")
	dir := t.TempDir()
	c := defaultWithKey()
	c.LogLevel = "loud"
	writeFile(t, dir, canonical(c), 0o600)
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Update(func(c *Config) { c.InstanceName = "Other" })
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.LogLevel != "debug" || got.InstanceName != "Other" {
		t.Fatalf("Update() = %+v", got)
	}
	if file := readFile(t, m.Path()); !strings.Contains(file, "<LogLevel>loud</LogLevel>") {
		t.Fatalf("file-backed value changed:\n%s", file)
	}
}

func TestLookupEnv(t *testing.T) {
	t.Setenv("dupearr__server__urlbase", "/lower")
	t.Setenv("DUPEARR__SERVER__PORT", " ")
	if v, ok := LookupEnv("DUPEARR__SERVER__URLBASE"); !ok || v != "/lower" {
		t.Fatalf("LookupEnv(URLBASE) = %q, %v", v, ok)
	}
	if v, ok := LookupEnv("DUPEARR__SERVER__PORT"); ok {
		t.Fatalf("blank value must count as unset, got %q", v)
	}
	if _, ok := LookupEnv("DUPEARR__SERVER__NOPE"); ok {
		t.Fatal("unset variable reported as set")
	}
}
