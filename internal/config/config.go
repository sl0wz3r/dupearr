// Package config owns config.xml, the *arr-style host configuration file in the data directory
// (docs/ARCHITECTURE.md §10). Operational settings live in the database (models.Settings), not here.
//
// The file format follows Servarr (docs/research/arr-conventions.md §1): a flat <Config> root with
// one element per setting, no XML declaration, booleans written as "True"/"False" and enums in
// PascalCase (read case-insensitively). Unlike Servarr, unknown elements are preserved and
// duplicate elements are collapsed to one (the first non-blank value). A UTF-8 byte order mark is
// accepted and dropped on the next save.
//
// Precedence is DUPEARR__SECTION__KEY environment variable > config.xml > built-in default.
// Environment values only exist in memory and are never written to config.xml.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Authentication methods and requirements, and default ports.
const (
	AuthNone     = "None"
	AuthBasic    = "Basic" // legacy: migrated to AuthForms on load (docs/DECISIONS.md D1)
	AuthForms    = "Forms"
	AuthExternal = "External"

	AuthRequiredEnabled          = "Enabled"
	AuthRequiredDisabledForLocal = "DisabledForLocalAddresses"

	DefaultPort    = 3873
	DefaultSslPort = 9873
)

// FileName is the name of the configuration file inside the data directory.
const FileName = "config.xml"

// Defaults and limits of the remaining settings.
const (
	DefaultBindAddress  = "*"
	DefaultLogLevel     = "info"
	DefaultLogSizeLimit = 1 // MB
	DefaultInstanceName = "Dupearr"
	DefaultBranch       = "main"

	// MaxLogSizeLimit is the largest accepted LogSizeLimit in MB (Servarr's validator: 1–10).
	MaxLogSizeLimit = 10
)

// Config mirrors config.xml (<Config> root). Booleans serialize as "True"/"False" like *arr.
type Config struct {
	BindAddress            string // "*"
	Port                   int
	UrlBase                string // normalized: "" or "/something" (leading slash, no trailing)
	EnableSsl              bool
	SslPort                int
	SslCertPath            string
	SslKeyPath             string
	ApiKey                 string // 32 lowercase hex
	AuthenticationMethod   string // Auth*
	AuthenticationRequired string // AuthRequired*
	TrustedProxies         string // reverse proxies whose forwarding headers are believed: IPs / CIDR ranges, "a, b"
	AllowedHosts           string // host names Dupearr is addressed by through them ("*.example.com": sub-domains), "a, b"
	LogLevel               string // trace|debug|info|warn|error
	LogSizeLimit           int    // MB per log file
	InstanceName           string
	LaunchBrowser          bool
	Branch                 string
}

// ValidationError is one invalid property (serialized like *arr validation failures:
// {"propertyName": "...", "errorMessage": "..."}).
type ValidationError struct {
	PropertyName string `json:"propertyName"`
	ErrorMessage string `json:"errorMessage"`
}

// Error implements error.
func (e ValidationError) Error() string { return e.PropertyName + ": " + e.ErrorMessage }

// ValidationErrors is the error returned by Load and Update when the configuration is invalid.
// Use errors.As to obtain the individual failures (e.g. to answer HTTP 400).
type ValidationErrors []ValidationError

// Error implements error.
func (v ValidationErrors) Error() string {
	msgs := make([]string, len(v))
	for i, e := range v {
		msgs[i] = e.Error()
	}
	return "invalid configuration: " + strings.Join(msgs, "; ")
}

// Manager owns config.xml in dataDir. Load creates the file with defaults (new ApiKey) when
// missing, fills missing elements with defaults, then applies DUPEARR__SECTION__KEY env overrides
// in memory (overrides are NOT written back). Unknown XML elements are preserved on save.
// Env override names (docs/ARCHITECTURE.md §10): DUPEARR__SERVER__BINDADDRESS|PORT|URLBASE|
// ENABLESSL|SSLPORT|SSLCERTPATH|SSLKEYPATH, DUPEARR__AUTH__APIKEY|METHOD|REQUIRED|TRUSTEDPROXIES|
// ALLOWEDHOSTS, DUPEARR__LOG__LEVEL|SIZELIMIT, DUPEARR__APP__INSTANCENAME.
//
// A Manager is safe for concurrent use.
type Manager struct {
	dataDir string
	path    string
	doc     *document // immutable after Load
	env     envValues // immutable after Load

	updateMu sync.Mutex // serialises Update, including its OnChange callbacks

	mu        sync.RWMutex
	file      Config // what config.xml holds
	effective Config // file + env overrides
	subs      []func(old, new Config)
}

// Default returns the default configuration (without an ApiKey).
func Default() Config {
	return Config{
		BindAddress:            DefaultBindAddress,
		Port:                   DefaultPort,
		UrlBase:                "",
		EnableSsl:              false,
		SslPort:                DefaultSslPort,
		AuthenticationMethod:   AuthForms,
		AuthenticationRequired: AuthRequiredEnabled,
		LogLevel:               DefaultLogLevel,
		LogSizeLimit:           DefaultLogSizeLimit,
		InstanceName:           DefaultInstanceName,
		LaunchBrowser:          false,
		Branch:                 DefaultBranch,
	}
}

// GenerateAPIKey returns a new random 32-character lowercase hex API key.
func GenerateAPIKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error (Go ≥ 1.24)
	return hex.EncodeToString(b[:])
}

// Load reads (or creates) dataDir/config.xml. See Manager.
//
// A missing or empty file is created with Default() and a new API key. Missing (or blank)
// elements are filled with defaults, a blank ApiKey is regenerated, AuthenticationMethod "Basic"
// becomes "Forms" and values are canonicalised; the file is rewritten only when one of these
// changed something. A file that is not well-formed XML with a <Config> root, holds unparsable
// values, or yields an invalid configuration (after env overrides) is an error and is left
// untouched. Invalid DUPEARR__ environment values are errors too.
func Load(dataDir string) (*Manager, error) {
	return load(dataDir, os.Environ())
}

func load(dataDir string, environ []string) (*Manager, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("config: data directory is empty")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("config: resolve data directory %q: %w", dataDir, err)
	}
	// The data directory holds secrets (config.xml, the database, backups, logs): owner only.
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("config: create data directory: %w", err)
	}
	m := &Manager{dataDir: abs, path: filepath.Join(abs, FileName)}

	p, err := m.read()
	if err != nil {
		return nil, err
	}
	file, dirty, err := decode(p)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", m.path, err)
	}

	env, err := readEnv(environ)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	effective := Normalize(env.apply(file))
	if errs := Validate(effective); len(errs) > 0 {
		return nil, fmt.Errorf("config: %s (with DUPEARR__ environment overrides applied): %w", m.path, ValidationErrors(errs))
	}

	m.doc, m.env, m.file, m.effective = p.doc, env, file, effective
	if dirty {
		if err := m.save(&m.file); err != nil {
			return nil, fmt.Errorf("config: save %s: %w", m.path, err)
		}
	}
	return m, nil
}

// read parses config.xml; a missing or blank file yields an empty document. A UTF-8 byte order
// mark (added by some Windows editors) is accepted like .NET/Servarr do, and the file is then
// rewritten without it, because encoding/xml (e.g. the healthcheck probe) rejects it.
func (m *Manager) read() (*parsed, error) {
	data, err := readConfigFile(m.path)
	hadBOM := bytes.HasPrefix(data, utf8BOM)
	data = bytes.TrimPrefix(data, utf8BOM)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &parsed{doc: newDocument(), values: map[int]string{}, dirty: true}, nil
	case err != nil:
		return nil, fmt.Errorf("config: read %s: %w", m.path, err)
	case len(strings.TrimSpace(string(data))) == 0:
		return &parsed{doc: newDocument(), values: map[int]string{}, dirty: true}, nil
	}
	p, err := parseDocument(data)
	if err != nil {
		return nil, fmt.Errorf("config: %s is corrupt or invalid (%w); fix it, or delete it and Dupearr will recreate it with defaults and a new API key", m.path, err)
	}
	p.dirty = p.dirty || hadBOM
	return p, nil
}

// decode turns parsed values into a Config: defaults for missing or blank values, a new API key
// when there is none, then Normalize. It appends elements for missing fields to p.doc and reports
// whether the file needs rewriting.
func decode(p *parsed) (Config, bool, error) {
	c := Default()
	dirty := p.dirty
	var errs []error
	for i, f := range fields {
		raw, ok := p.values[i]
		if !ok {
			p.doc.nodes = append(p.doc.nodes, node{field: i})
			dirty = true
		}
		if raw == "" && !f.optional {
			if f.xml == "ApiKey" {
				c.ApiKey = GenerateAPIKey()
			}
			dirty = dirty || ok // a blank required element is rewritten with its default
			continue
		}
		if err := f.parse(&c, raw); err != nil {
			errs = append(errs, fmt.Errorf("<%s>: %w", f.xml, err))
		}
	}
	if len(errs) > 0 {
		return Config{}, false, errors.Join(errs...)
	}

	c = Normalize(c)
	for i, f := range fields {
		if raw, ok := p.values[i]; ok && raw != "" && f.format(&c) != raw {
			dirty = true // canonicalised ("forms" → "Forms", "Basic" → "Forms", "true" → "True", …)
		}
	}
	return c, dirty, nil
}

// save renders c into config.xml atomically. It refuses to write a file that Load would reject
// as too large, so a save can never leave Dupearr unable to start.
func (m *Manager) save(c *Config) error {
	data := m.doc.render(c)
	if len(data) > maxFileSize {
		return fmt.Errorf("rendered file is %d bytes, over the %d byte limit", len(data), maxFileSize)
	}
	return writeFileAtomic(m.path, data)
}

// Get returns a copy of the current config with env overrides applied.
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.effective
}

// Update applies fn to a copy of the stored config, validates it, persists it and notifies
// OnChange subscribers. Env-overridden fields keep their override values in memory.
//
// fn receives the file-backed values (what config.xml holds), not the env-overridden view.
// Changes fn makes to env-overridden fields are discarded, so override values can never be
// written to config.xml. The result is normalised (see Normalize) and the effective config
// (with overrides) must pass Validate; so must config.xml on its own, except for problems it
// already had, so that removing an env override later cannot leave Dupearr unable to start.
// Otherwise a ValidationErrors is returned and nothing is saved. On success the effective config
// is returned and every OnChange callback runs synchronously with the effective old and new
// configs.
//
// Updates are serialised; fn and OnChange callbacks must not call Update (they may call Get).
func (m *Manager) Update(fn func(c *Config)) (Config, error) {
	if fn == nil {
		return Config{}, errors.New("config: Update called with a nil function")
	}
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	m.mu.RLock()
	oldFile, oldEffective := m.file, m.effective
	m.mu.RUnlock()

	next := oldFile
	fn(&next)
	for i, f := range fields {
		if m.env.set[i] {
			f.copy(&next, &oldFile) // the environment owns this field
		}
	}
	next = Normalize(next)
	effective := Normalize(m.env.apply(next))
	if errs := Validate(effective); len(errs) > 0 {
		return Config{}, ValidationErrors(errs)
	}
	if errs := m.newFileProblems(oldFile, next); len(errs) > 0 {
		return Config{}, ValidationErrors(errs)
	}

	if err := m.save(&next); err != nil {
		return Config{}, fmt.Errorf("config: save %s: %w", m.path, err)
	}

	m.mu.Lock()
	m.file, m.effective = next, effective
	subs := slices.Clone(m.subs)
	m.mu.Unlock()

	for _, sub := range subs {
		sub(oldEffective, effective)
	}
	return effective, nil
}

// newFileProblems returns the validation problems of the file-backed config next that the env
// overrides hide and that oldFile did not already have. Saving such a config would stop Dupearr
// from starting once the DUPEARR__ variable is removed (e.g. SslPort set equal to the Port that
// config.xml holds while DUPEARR__SERVER__PORT is in effect). Pre-existing problems are
// tolerated so that an update is never blocked by something it did not cause.
func (m *Manager) newFileProblems(oldFile, next Config) []ValidationError {
	if !slices.Contains(m.env.set[:], true) {
		return nil // no overrides: next is the effective config, already validated
	}
	out := NewProblems(oldFile, next)
	for i := range out {
		out[i].ErrorMessage += fileOnlySuffix
	}
	return out
}

// Path returns the absolute path of config.xml.
func (m *Manager) Path() string { return m.path }

// DataDir returns the data directory (absolute).
func (m *Manager) DataDir() string { return m.dataDir }

// OnChange registers fn to be called (synchronously, after persisting) on every successful Update.
func (m *Manager) OnChange(fn func(old, new Config)) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, fn)
}

// EnvOverrides returns the names of keys currently overridden by env (UI shows them read-only),
// e.g. ["port", "urlBase"] (HostConfig JSON property names). Never nil.
func (m *Manager) EnvOverrides() []string {
	out := []string{}
	for i, f := range fields {
		if m.env.set[i] {
			out = append(out, f.json)
		}
	}
	return out
}

// Normalize returns c in canonical form: trimmed strings, BindAddress "" → "*", UrlBase as ""
// or "/x" (see NormalizeURLBase), AuthenticationMethod/AuthenticationRequired in PascalCase with
// the legacy "Basic" migrated to "Forms" (docs/DECISIONS.md D1), TrustedProxies/AllowedHosts as
// "a, b" (see NormalizeList), LogLevel lower-case ("warning" → "warn", "fatal" → "error") and
// Branch lower-case. Values it cannot canonicalise are left for Validate to reject.
func Normalize(c Config) Config {
	c.BindAddress = strings.TrimSpace(c.BindAddress)
	if c.BindAddress == "" {
		c.BindAddress = DefaultBindAddress
	}
	c.UrlBase = NormalizeURLBase(c.UrlBase)
	c.SslCertPath = strings.TrimSpace(c.SslCertPath)
	c.SslKeyPath = strings.TrimSpace(c.SslKeyPath)
	c.ApiKey = strings.TrimSpace(c.ApiKey)
	c.AuthenticationMethod = canonicalAuthMethod(c.AuthenticationMethod)
	c.AuthenticationRequired = canonicalAuthRequired(c.AuthenticationRequired)
	c.TrustedProxies = NormalizeList(c.TrustedProxies)
	c.AllowedHosts = NormalizeList(c.AllowedHosts)
	c.LogLevel = canonicalLogLevel(c.LogLevel)
	c.InstanceName = strings.TrimSpace(c.InstanceName)
	c.Branch = strings.ToLower(strings.TrimSpace(c.Branch))
	return c
}

// NormalizeList returns a list setting (TrustedProxies, AllowedHosts) with its entries (see
// SplitList) joined by ", ", so that the separator a user typed never counts as a change and
// Settings → General shows a tidy list. The entries themselves are kept as typed: whoever reads
// the list checks them (invalid entries in config.xml or the environment are skipped and logged,
// never a reason not to start).
func NormalizeList(s string) string {
	return strings.Join(SplitList(s), ", ")
}

// NormalizeURLBase returns "" or "/something": surrounding whitespace and leading/trailing
// slashes are removed and a single leading slash is added. It does not validate the result
// (Validate rejects "..", spaces, query characters, …).
func NormalizeURLBase(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "/")
	if s == "" {
		return ""
	}
	return "/" + s
}

func canonicalAuthMethod(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "none":
		return AuthNone
	case "forms", "basic": // Basic is no longer supported: migrate to Forms (D1)
		return AuthForms
	case "external":
		return AuthExternal
	}
	return s
}

func canonicalAuthRequired(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "enabled":
		return AuthRequiredEnabled
	case "disabledforlocaladdresses":
		return AuthRequiredDisabledForLocal
	}
	return s
}

func canonicalLogLevel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "warning":
		return "warn"
	case "fatal":
		return "error"
	}
	return s
}
