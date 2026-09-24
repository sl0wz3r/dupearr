package config

import (
	"fmt"
	"net"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits used by Validate.
const (
	minAPIKeyLen       = 20
	maxAPIKeyLen       = 128
	maxInstanceNameLen = 100
	maxURLBaseLen      = 200
	maxBranchLen       = 100
	maxPathLen         = 4096
)

// Validate checks a config and returns every problem found (nil when valid).
//
// It validates Normalize(c), so values that normalisation fixes (case of enums, slashes around
// UrlBase, the legacy "Basic" method) are accepted — exactly what Update would store. Property
// names are the HostConfig JSON names (docs/API.md). File existence (e.g. of the SSL
// certificate) is not checked here.
func Validate(c Config) []ValidationError {
	c = Normalize(c)
	var errs []ValidationError
	add := func(prop, format string, args ...any) {
		errs = append(errs, ValidationError{PropertyName: prop, ErrorMessage: fmt.Sprintf(format, args...)})
	}

	if c.BindAddress != "*" && net.ParseIP(c.BindAddress) == nil {
		add("bindAddress", "Must be '*' or a valid IP address")
	}
	if !validPort(c.Port) {
		add("port", "Must be between 1 and 65535")
	}
	if msg := urlBaseProblem(c.UrlBase); msg != "" {
		add("urlBase", "%s", msg)
	}
	if !validPort(c.SslPort) {
		add("sslPort", "Must be between 1 and 65535")
	} else if c.EnableSsl && c.SslPort == c.Port {
		add("sslPort", "Must differ from Port when SSL is enabled")
	}
	for _, p := range []struct{ prop, path string }{{"sslCertPath", c.SslCertPath}, {"sslKeyPath", c.SslKeyPath}} {
		switch {
		case c.EnableSsl && p.path == "":
			add(p.prop, "Required when SSL is enabled")
		case len(p.path) > maxPathLen:
			add(p.prop, "Must be at most %d characters", maxPathLen)
		case strings.IndexFunc(p.path, unicode.IsControl) >= 0:
			add(p.prop, "Must not contain control characters")
		}
	}
	if msg := apiKeyProblem(c.ApiKey); msg != "" {
		add("apiKey", "%s", msg)
	}
	switch c.AuthenticationMethod {
	case AuthNone, AuthForms, AuthExternal:
	default:
		add("authenticationMethod", "Must be one of: %s, %s, %s", AuthNone, AuthForms, AuthExternal)
	}
	switch c.AuthenticationRequired {
	case AuthRequiredEnabled, AuthRequiredDisabledForLocal:
	default:
		add("authenticationRequired", "Must be one of: %s, %s", AuthRequiredEnabled, AuthRequiredDisabledForLocal)
	}
	switch c.LogLevel {
	case "trace", "debug", "info", "warn", "error":
	default:
		add("logLevel", "Must be one of: trace, debug, info, warn, error")
	}
	if c.LogSizeLimit < 1 || c.LogSizeLimit > MaxLogSizeLimit {
		add("logSizeLimit", "Must be between 1 and %d (MB)", MaxLogSizeLimit)
	}
	switch {
	case c.InstanceName == "":
		add("instanceName", "Required")
	case utf8.RuneCountInString(c.InstanceName) > maxInstanceNameLen:
		add("instanceName", "Must be at most %d characters", maxInstanceNameLen)
	case strings.IndexFunc(c.InstanceName, unicode.IsControl) >= 0:
		add("instanceName", "Must not contain control characters")
	}
	switch {
	case c.Branch == "":
		add("branch", "Required ('main' is the default)")
	case utf8.RuneCountInString(c.Branch) > maxBranchLen:
		add("branch", "Must be at most %d characters", maxBranchLen)
	case strings.IndexFunc(c.Branch, unicode.IsControl) >= 0:
		add("branch", "Must not contain control characters")
	}
	return errs
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

// urlBaseProblem describes why a normalised UrlBase is invalid ("" when valid). Segments may
// only contain unreserved URL characters, so "..", spaces, query/fragment characters,
// percent-escapes and backslashes are all rejected.
func urlBaseProblem(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > maxURLBaseLen {
		return fmt.Sprintf("Must be at most %d characters", maxURLBaseLen)
	}
	for _, seg := range strings.Split(strings.TrimPrefix(s, "/"), "/") {
		switch seg {
		case "":
			return "Must not contain empty path segments ('//')"
		case ".", "..":
			return "Must not contain '.' or '..' path segments"
		}
		for _, r := range seg {
			if !isUnreserved(r) {
				return fmt.Sprintf("Contains invalid character %q; use only letters, digits, '-', '_', '.', '~' and '/'", r)
			}
		}
	}
	return ""
}

// isUnreserved reports whether r is an RFC 3986 unreserved character.
func isUnreserved(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '-' || r == '_' || r == '.' || r == '~'
}

// apiKeyProblem describes why an API key is unacceptable ("" when valid). Generated keys are 32
// lower-case hex characters; keys supplied by the user (config.xml or DUPEARR__AUTH__APIKEY) only
// need to be long enough and header/URL safe.
func apiKeyProblem(k string) string {
	switch {
	case k == "":
		return "Required"
	case len(k) < minAPIKeyLen || len(k) > maxAPIKeyLen:
		return fmt.Sprintf("Must be between %d and %d characters", minAPIKeyLen, maxAPIKeyLen)
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "Must contain only letters, digits, '-' and '_'"
		}
	}
	return ""
}
