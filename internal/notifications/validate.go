package notifications

import (
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

const maxNameLength = 128

var (
	reTelegramToken  = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	reTelegramChatID = regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z][A-Za-z0-9_]{3,})$`)
	reAlnum          = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	rePushoverName   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,25}$`)
	rePushoverSound  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reNtfyTopic      = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
	reAppriseKey     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	reHostname       = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?)*\.?$`)
)

// ValidationFailedError is returned by Service.Test (and delivery) when a connection's
// configuration is invalid. Errors holds the individual failures (propertyName = field name).
type ValidationFailedError struct {
	Errors []config.ValidationError
}

// Error implements error.
func (e *ValidationFailedError) Error() string {
	msgs := make([]string, 0, len(e.Errors))
	for _, v := range e.Errors {
		msgs = append(msgs, v.ErrorMessage)
	}
	return "invalid notification settings: " + strings.Join(msgs, "; ")
}

// ValidateConfig checks a connection's settings against its provider schema.
//
// It checks the name, the kind, the triggers (⊆ models.On*), value types, required fields, URL
// formats, select options and provider-specific rules, and rejects secrets that still hold the
// "********" mask (call MergeSecrets first on updates). PropertyName is "name", "kind",
// "triggers", "settings" or the provider field name (e.g. "webhookUrl"). Returns nil when valid.
func ValidateConfig(cfg models.NotificationConfig) []config.ValidationError {
	var errs []config.ValidationError
	add := func(prop, format string, args ...any) {
		errs = append(errs, config.ValidationError{PropertyName: prop, ErrorMessage: fmt.Sprintf(format, args...)})
	}

	name := strings.TrimSpace(cfg.Name)
	switch {
	case name == "":
		add("name", "Name is required")
	case utf8.RuneCountInString(name) > maxNameLength:
		add("name", "Name must be at most %d characters", maxNameLength)
	case strings.ContainsFunc(name, isControl):
		add("name", "Name must not contain control characters")
	}

	for _, t := range cfg.Triggers {
		if !isKnownTrigger(t) {
			add("triggers", "Unknown trigger %q", t)
		}
	}

	schema, known := providerSchema(cfg.Kind)
	switch {
	case strings.TrimSpace(cfg.Kind) == "":
		add("kind", "Kind is required")
		return errs
	case !known:
		add("kind", "Unknown notification kind %q (expected one of %s)", cfg.Kind, strings.Join(knownKinds(), ", "))
		return errs
	}

	set, err := parseSettings(schema, cfg.Settings)
	if err != nil {
		add("settings", "Settings must be a JSON object")
		return errs
	}
	return append(errs, validateSettings(set)...)
}

// validateSettings runs the generic field checks and the provider-specific rules.
func validateSettings(set *settings) []config.ValidationError {
	var errs []config.ValidationError
	add := func(prop, format string, args ...any) {
		errs = append(errs, config.ValidationError{PropertyName: prop, ErrorMessage: fmt.Sprintf(format, args...)})
	}
	bad := map[string]bool{} // fields that already have an error

	for _, f := range set.schema.Fields {
		raw, k := decodeScalar(set.raw[f.Name])
		fail := func(format string, args ...any) {
			add(f.Name, format, args...)
			bad[f.Name] = true
		}
		// Type checks on the raw value.
		switch f.Type {
		case fieldNumber:
			if k == valueOther || k == valueBool {
				fail("%s must be a whole number", f.Label)
				continue
			}
			if v := strings.TrimSpace(raw); v != "" {
				if _, err := strconv.ParseInt(v, 10, 64); err != nil {
					fail("%s must be a whole number", f.Label)
					continue
				}
			}
		case fieldCheckbox:
			if k == valueOther || k == valueNumber {
				fail("%s must be true or false", f.Label)
				continue
			}
			if v := strings.TrimSpace(raw); k == valueString && v != "" && !strings.EqualFold(v, "true") && !strings.EqualFold(v, "false") {
				fail("%s must be true or false", f.Label)
				continue
			}
		default:
			if k == valueOther {
				fail("%s must be a string", f.Label)
				continue
			}
		}
		if f.Secret && k == valueString && raw == MaskedValue {
			// MergeSecrets leaves the mask when there is no stored value or the destination
			// (server URL / SMTP host) changed.
			fail("%s is masked; re-enter the value (a stored secret is only kept while the server address is unchanged)", f.Label)
			continue
		}

		v := set.str(f.Name)
		if v == "" {
			if f.Required {
				fail("%s is required", f.Label)
			}
			continue
		}
		// Control characters are never valid (and could smuggle CR/LF into headers or SMTP).
		if strings.ContainsFunc(v, isControlExceptNewline(f.Type == fieldTextarea)) {
			fail("%s must not contain control characters", f.Label)
			continue
		}
		switch f.Type {
		case fieldURL:
			if strings.Contains(v, MaskedValue) {
				// A partly masked URL (see maskURL) that MergeSecrets could not restore: it was
				// edited, or the connection is new.
				fail("%s contains a hidden part (%s); re-enter the full URL", f.Label, MaskedValue)
			} else if msg := checkHTTPURL(v); msg != "" {
				fail("%s %s", f.Label, msg)
			}
		case fieldSelect:
			if !slices.Contains(f.Options, v) {
				fail("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
			}
		}
	}

	check := func(prop string) bool { return !bad[prop] }
	switch set.schema.Kind {
	case KindTelegram:
		if v := set.str("botToken"); v != "" && check("botToken") && !reTelegramToken.MatchString(v) {
			add("botToken", "Bot Token must look like 123456789:ABCdef… (from @BotFather)")
		}
		if v := set.str("chatId"); v != "" && check("chatId") && !reTelegramChatID.MatchString(v) {
			add("chatId", "Chat ID must be a numeric id (e.g. -1001234567890) or @channelusername")
		}
		if n, ok := set.num("topicId"); ok && check("topicId") && n < 0 {
			add("topicId", "Topic ID must not be negative")
		}
	case KindPushover:
		for _, name := range []string{"appToken", "userKey"} {
			if v := set.str(name); v != "" && check(name) && !reAlnum.MatchString(v) {
				add(name, "%s must contain only letters and digits", set.fields[name].Label)
			}
		}
		if check("devices") {
			for _, d := range set.list("devices") {
				if !rePushoverName.MatchString(d) {
					add("devices", "Device %q is invalid (letters, digits, - and _; max 25)", d)
					break
				}
			}
		}
		if v := set.str("sound"); v != "" && check("sound") && !rePushoverSound.MatchString(v) {
			add("sound", "Sound must contain only letters, digits, - and _")
		}
		if n, ok := set.num("retry"); ok && check("retry") && n < 30 {
			add("retry", "Retry must be at least 30 seconds")
		}
		if n, ok := set.num("expire"); ok && check("expire") && (n < 1 || n > 10800) {
			add("expire", "Expire must be between 1 and 10800 seconds")
		}
	case KindGotify:
		if n, ok := set.num("priority"); ok && check("priority") && (n < 0 || n > 10) {
			add("priority", "Priority must be between 0 and 10")
		}
	case KindNtfy:
		if v := set.str("topic"); v != "" && check("topic") && !reNtfyTopic.MatchString(v) {
			add("topic", "Topic may contain only letters, digits, - and _ (max 64)")
		}
		token, user, pass := set.str("accessToken"), set.str("username"), set.str("password")
		if token != "" && (user != "" || pass != "") {
			add("accessToken", "Use either an access token or username/password, not both")
		}
		if user != "" && pass == "" && check("password") {
			add("password", "Password is required when a username is set")
		}
		if pass != "" && user == "" && check("username") {
			add("username", "Username is required when a password is set")
		}
		if check("tags") {
			for _, t := range set.list("tags") {
				if strings.ContainsAny(t, " \t") || utf8.RuneCountInString(t) > 64 {
					add("tags", "Tag %q is invalid (no spaces, max 64 characters)", t)
					break
				}
			}
		}
	case KindApprise:
		key, urls := set.str("configKey"), set.str("urls")
		switch {
		case key == "" && urls == "" && check("configKey") && check("urls"):
			add("configKey", "Set either a Config Key or Apprise URLs")
		case key != "" && urls != "":
			add("configKey", "Set either a Config Key or Apprise URLs, not both")
		case key != "" && check("configKey") && !reAppriseKey.MatchString(key):
			add("configKey", "Config Key may contain only letters, digits, - and _ (max 128)")
		}
		if set.str("password") != "" && set.str("username") == "" && check("username") {
			add("username", "Username is required when a password is set")
		}
	case KindWebhook:
		hdr, err := parseHeaders(set.str("headers"))
		if err != nil && check("headers") {
			add("headers", "%s", err.Error())
		}
		if set.str("password") != "" && set.str("username") == "" && check("username") {
			add("username", "Username is required when a password is set")
		}
		if hdr.Get("Authorization") != "" && set.str("username") != "" {
			add("headers", "An Authorization header conflicts with username/password; use one of them")
		}
	case KindEmail:
		if v := set.str("host"); v != "" && check("host") && !isValidHost(v) {
			add("host", "Server must be a host name or IP address (without scheme or port)")
		}
		if n, ok := set.num("port"); ok && check("port") && (n < 1 || n > 65535) {
			add("port", "Port must be between 1 and 65535")
		} else if msg := smtpPortMismatch(set.str("encryption"), smtpPort(set)); msg != "" && check("port") && check("encryption") {
			add("port", "%s", msg)
		}
		if v := set.str("from"); v != "" && check("from") {
			if _, err := mail.ParseAddress(v); err != nil {
				add("from", "From Address is not a valid email address")
			}
		}
		for _, name := range []string{"to", "cc", "bcc"} {
			if v := set.str(name); v != "" && check(name) {
				if _, err := mail.ParseAddressList(v); err != nil {
					add(name, "%s must be a comma-separated list of email addresses", set.fields[name].Label)
				}
			}
		}
		user, pass := set.str("username"), set.str("password")
		if pass != "" && user == "" && check("username") {
			add("username", "Username is required when a password is set")
		}
		if user != "" && set.str("encryption") == EncryptionNone && !isLocalhost(smtpHost(set)) {
			add("encryption", "Credentials are only sent over an encrypted connection; choose starttls or tls")
		}
	}
	return errs
}

// checkHTTPURL returns "" when v is an absolute http(s) URL with a host and no credentials, else a
// message fragment ("must …").
func checkHTTPURL(v string) string {
	if strings.ContainsAny(v, " \t\r\n") {
		return "must be a valid URL (no spaces)"
	}
	u, err := url.Parse(v)
	if err != nil {
		return "must be a valid URL"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "must start with http:// or https://"
	}
	if u.Hostname() == "" {
		return "must include a host"
	}
	if u.User != nil {
		return "must not contain credentials; use the username/password fields"
	}
	return ""
}

// isValidHost reports whether v is a bare DNS name or IP address.
func isValidHost(v string) bool {
	if net.ParseIP(strings.Trim(v, "[]")) != nil {
		return true
	}
	return len(v) <= 253 && reHostname.MatchString(v)
}

// isLocalhost mirrors net/smtp's notion of a local server (credentials allowed without TLS).
func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// isControl reports C0/C1 control characters (including newlines).
func isControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r < 0xa0)
}

// isControlExceptNewline returns a predicate for disallowed characters; textareas may contain
// newlines and tabs.
func isControlExceptNewline(multiline bool) func(rune) bool {
	return func(r rune) bool {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			return false
		}
		return isControl(r)
	}
}

// forbiddenHeaders are managed by the HTTP client and may not be set by users.
var forbiddenHeaders = []string{
	"Host", "Content-Length", "Transfer-Encoding", "Connection", "Upgrade", "Te", "Trailer",
	"Keep-Alive", "Proxy-Connection", "Proxy-Authorization",
}

// parseHeaders parses the webhook "headers" textarea: one "Key: Value" per line; blank lines and
// lines starting with # are ignored.
func parseHeaders(text string) (http.Header, error) {
	h := http.Header{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == MaskedValue {
			return nil, fmt.Errorf("line %d is hidden (%s); re-enter it", i+1, MaskedValue)
		}
		key, value, ok := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d: expected \"Key: Value\"", i+1)
		}
		if !isHeaderToken(key) {
			return nil, fmt.Errorf("line %d: invalid header name %q", i+1, key)
		}
		if value == MaskedValue {
			// A masked value MergeSecrets could not restore (new header name, or the URL changed).
			return nil, fmt.Errorf("line %d: the value of %q is hidden; re-enter it", i+1, key)
		}
		if strings.ContainsFunc(value, func(r rune) bool { return r != '\t' && isControl(r) }) {
			return nil, fmt.Errorf("line %d: header value contains control characters", i+1)
		}
		canon := http.CanonicalHeaderKey(key)
		if slices.Contains(forbiddenHeaders, canon) {
			return nil, fmt.Errorf("line %d: header %q cannot be overridden", i+1, canon)
		}
		h.Add(canon, value)
	}
	return h, nil
}

// isHeaderToken reports whether s is a valid RFC 9110 field name (token).
func isHeaderToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}
