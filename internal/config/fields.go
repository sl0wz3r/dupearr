package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// EnvPrefix prefixes every environment override (DUPEARR__SECTION__KEY).
const EnvPrefix = "DUPEARR__"

// field describes one config.xml element: its XML element name, HostConfig JSON property name,
// optional DUPEARR__ environment variable, and an accessor for exactly one of the value kinds.
type field struct {
	xml      string
	json     string
	env      string
	optional bool // may legitimately be empty (a blank element is not replaced by the default)

	str  func(*Config) *string
	num  func(*Config) *int
	flag func(*Config) *bool
}

// fields lists every setting in canonical order (the element order of a new config.xml).
var fields = [...]field{
	{xml: "BindAddress", json: "bindAddress", env: "DUPEARR__SERVER__BINDADDRESS", str: func(c *Config) *string { return &c.BindAddress }},
	{xml: "Port", json: "port", env: "DUPEARR__SERVER__PORT", num: func(c *Config) *int { return &c.Port }},
	{xml: "UrlBase", json: "urlBase", env: "DUPEARR__SERVER__URLBASE", optional: true, str: func(c *Config) *string { return &c.UrlBase }},
	{xml: "EnableSsl", json: "enableSsl", env: "DUPEARR__SERVER__ENABLESSL", flag: func(c *Config) *bool { return &c.EnableSsl }},
	{xml: "SslPort", json: "sslPort", env: "DUPEARR__SERVER__SSLPORT", num: func(c *Config) *int { return &c.SslPort }},
	{xml: "SslCertPath", json: "sslCertPath", env: "DUPEARR__SERVER__SSLCERTPATH", optional: true, str: func(c *Config) *string { return &c.SslCertPath }},
	{xml: "SslKeyPath", json: "sslKeyPath", env: "DUPEARR__SERVER__SSLKEYPATH", optional: true, str: func(c *Config) *string { return &c.SslKeyPath }},
	{xml: "ApiKey", json: "apiKey", env: "DUPEARR__AUTH__APIKEY", str: func(c *Config) *string { return &c.ApiKey }},
	{xml: "AuthenticationMethod", json: "authenticationMethod", env: "DUPEARR__AUTH__METHOD", str: func(c *Config) *string { return &c.AuthenticationMethod }},
	{xml: "AuthenticationRequired", json: "authenticationRequired", env: "DUPEARR__AUTH__REQUIRED", str: func(c *Config) *string { return &c.AuthenticationRequired }},
	{xml: "LogLevel", json: "logLevel", env: "DUPEARR__LOG__LEVEL", str: func(c *Config) *string { return &c.LogLevel }},
	{xml: "LogSizeLimit", json: "logSizeLimit", env: "DUPEARR__LOG__SIZELIMIT", num: func(c *Config) *int { return &c.LogSizeLimit }},
	{xml: "InstanceName", json: "instanceName", env: "DUPEARR__APP__INSTANCENAME", str: func(c *Config) *string { return &c.InstanceName }},
	{xml: "LaunchBrowser", json: "launchBrowser", flag: func(c *Config) *bool { return &c.LaunchBrowser }},
	{xml: "Branch", json: "branch", str: func(c *Config) *string { return &c.Branch }},
}

// fieldIndexByXML returns the index of the field whose element name matches name
// case-insensitively, or -1.
func fieldIndexByXML(name string) int {
	for i, f := range fields {
		if strings.EqualFold(f.xml, name) {
			return i
		}
	}
	return -1
}

// format renders the field's value of c as stored in config.xml.
func (f field) format(c *Config) string {
	switch {
	case f.str != nil:
		return *f.str(c)
	case f.num != nil:
		return strconv.Itoa(*f.num(c))
	default:
		if *f.flag(c) {
			return "True"
		}
		return "False"
	}
}

// parse stores the (trimmed) text raw into the field of c.
func (f field) parse(c *Config, raw string) error {
	raw = strings.TrimSpace(raw)
	switch {
	case f.str != nil:
		*f.str(c) = raw
	case f.num != nil:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", truncate(raw, 40))
		}
		*f.num(c) = n
	default:
		b, ok := parseBool(raw)
		if !ok {
			return fmt.Errorf("%q is not True or False", truncate(raw, 40))
		}
		*f.flag(c) = b
	}
	return nil
}

// copy copies the field's value from src to dst.
func (f field) copy(dst, src *Config) {
	switch {
	case f.str != nil:
		*f.str(dst) = *f.str(src)
	case f.num != nil:
		*f.num(dst) = *f.num(src)
	default:
		*f.flag(dst) = *f.flag(src)
	}
}

// parseBool accepts True/False (any case) as written by *arr, plus 1/0 and yes/no.
func parseBool(s string) (value, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	}
	return false, false
}

// envValues holds the DUPEARR__ overrides read at Load.
type envValues struct {
	set    [len(fields)]bool
	values Config
}

// readEnv extracts the supported DUPEARR__ variables from environ ("KEY=value" entries); see
// lookupEnv for how names match. Unparsable values are errors.
func readEnv(environ []string) (envValues, error) {
	var e envValues
	var errs []error
	for i, f := range fields {
		if f.env == "" {
			continue
		}
		name, value, ok := lookupEnv(environ, f.env)
		if !ok {
			continue
		}
		if err := f.parse(&e.values, value); err != nil {
			errs = append(errs, fmt.Errorf("environment variable %s: %w", name, err))
			continue
		}
		e.set[i] = true
	}
	return e, errors.Join(errs...)
}

// lookupEnv finds canonical (an upper-case variable name) in environ. Names match
// case-insensitively (like .NET's configuration binder, docs/research/arr-conventions.md §1.5);
// when several spellings are present the exact upper-case one wins, else the lexicographically
// smallest. Empty or blank values count as unset.
func lookupEnv(environ []string, canonical string) (name, value string, ok bool) {
	for _, kv := range environ {
		n, v, found := strings.Cut(kv, "=")
		if !found || !strings.EqualFold(n, canonical) || strings.TrimSpace(v) == "" {
			continue
		}
		if ok && !preferEnvName(n, name, canonical) {
			continue
		}
		name, value, ok = n, v, true
	}
	return name, value, ok
}

// LookupEnv returns the value of an environment variable such as "DUPEARR__SERVER__PORT" exactly
// as Load resolves overrides: the name matches case-insensitively and blank values count as unset.
// Tools that must not create config.xml (e.g. the healthcheck probe) use it to agree with Load.
func LookupEnv(name string) (string, bool) {
	_, value, ok := lookupEnv(os.Environ(), name)
	return value, ok
}

// preferEnvName reports whether candidate should replace current as the spelling of canonical.
func preferEnvName(candidate, current, canonical string) bool {
	if current == canonical {
		return false
	}
	return candidate == canonical || candidate < current
}

// apply returns c with the overridden fields replaced by their environment values.
func (e envValues) apply(c Config) Config {
	for i, f := range fields {
		if e.set[i] {
			f.copy(&c, &e.values)
		}
	}
	return c
}
