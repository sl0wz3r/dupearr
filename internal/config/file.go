package config

import (
	"bytes"
	"fmt"
	"slices"
)

// RewriteFile decodes the contents of a config.xml the way Load does (defaults for missing or
// blank elements, a new API key when there is none, canonical values), applies fn (when not nil)
// and renders the file again, keeping unknown elements, comments and the element order like a
// save by Update. It never touches the disk and never applies DUPEARR__ environment overrides:
// the returned Config is exactly what the returned file holds. It does not validate the result
// (see Validate and Manager.CheckFile).
//
// Backup restores use it to inspect and adjust a config.xml taken from an archive before it
// replaces the live one.
func RewriteFile(data []byte, fn func(c *Config)) ([]byte, Config, error) {
	if len(data) > maxFileSize {
		return nil, Config{}, fmt.Errorf("%s is larger than %d bytes", FileName, maxFileSize)
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	p := &parsed{doc: newDocument(), values: map[int]string{}}
	if len(bytes.TrimSpace(data)) > 0 {
		var err error
		if p, err = parseDocument(data); err != nil {
			return nil, Config{}, fmt.Errorf("%s is corrupt or invalid: %w", FileName, err)
		}
	}
	c, _, err := decode(p)
	if err != nil {
		return nil, Config{}, fmt.Errorf("%s: %w", FileName, err)
	}
	if fn != nil {
		fn(&c)
		c = Normalize(c)
	}
	out := p.doc.render(&c)
	if len(out) > maxFileSize {
		return nil, Config{}, fmt.Errorf("rendered %s is %d bytes, over the %d byte limit", FileName, len(out), maxFileSize)
	}
	return out, c, nil
}

// RenderOnto renders c into the document of the config.xml contents base: base's element order,
// unknown elements and comments are kept, every known setting takes its value from c (elements
// base lacks are appended). An empty base renders a new file. Like RewriteFile it never touches the
// disk, never applies environment overrides and does not validate c.
//
// Backup restores use it to stage a restored configuration without adopting anything but the known
// settings from the archive: an element this build does not know could be one a later build reads
// (a security setting, say) and would otherwise be adopted unreviewed. (The known security
// settings have their own restore rules; the reverse-proxy trust lists, for example, are never
// restored.)
func RenderOnto(base []byte, c Config) ([]byte, error) {
	if len(base) > maxFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", FileName, maxFileSize)
	}
	base = bytes.TrimPrefix(base, utf8BOM)
	p := &parsed{doc: newDocument(), values: map[int]string{}}
	if len(bytes.TrimSpace(base)) > 0 {
		var err error
		if p, err = parseDocument(base); err != nil {
			return nil, fmt.Errorf("%s is corrupt or invalid: %w", FileName, err)
		}
	}
	if _, _, err := decode(p); err != nil { // appends the known elements base lacks
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	c = Normalize(c)
	out := p.doc.render(&c)
	if len(out) > maxFileSize {
		return nil, fmt.Errorf("rendered %s is %d bytes, over the %d byte limit", FileName, len(out), maxFileSize)
	}
	return out, nil
}

// ParseFile decodes the contents of a config.xml like RewriteFile, without environment overrides
// and without validating it.
func ParseFile(data []byte) (Config, error) {
	_, c, err := RewriteFile(data, nil)
	return c, err
}

// File returns what config.xml holds: the configuration without DUPEARR__ environment overrides.
func (m *Manager) File() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.file
}

// CheckFile reports whether c could replace config.xml, applying the rules Update applies before
// saving: with this Manager's DUPEARR__ environment overrides applied, c must be valid (Dupearr
// can start with it), and on its own it must not have problems the current config.xml does not
// already have (removing an override later cannot leave Dupearr unable to start). The error is a
// ValidationErrors. Nothing is saved.
func (m *Manager) CheckFile(c Config) error {
	m.mu.RLock()
	current := m.file
	m.mu.RUnlock()
	c = Normalize(c)
	if errs := Validate(m.env.apply(c)); len(errs) > 0 {
		return ValidationErrors(errs)
	}
	if errs := NewProblems(current, c); len(errs) > 0 {
		for i := range errs {
			errs[i].ErrorMessage += fileOnlySuffix
		}
		return ValidationErrors(errs)
	}
	return nil
}

// fileOnlySuffix marks problems of config.xml that the DUPEARR__ environment overrides hide.
const fileOnlySuffix = " (in config.xml, i.e. without the DUPEARR__ environment overrides)"

// NewProblems returns the validation problems of next (validated on its own, without environment
// overrides) that current does not have too. Pre-existing problems are tolerated, so a change is
// never blocked by something it did not cause.
func NewProblems(current, next Config) []ValidationError {
	had := Validate(current)
	var out []ValidationError
	for _, e := range Validate(next) {
		if !slices.Contains(had, e) {
			out = append(out, e)
		}
	}
	return out
}
