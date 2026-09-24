package logging

import (
	"regexp"
	"strings"
)

// Removed replaces every secret masked by Redact (the literal Servarr's CleanseLogMessage uses).
const Removed = "(removed)"

// redactRule replaces the secret part of every match of re with repl (which keeps the
// surrounding captures and inserts Removed).
type redactRule struct {
	re   *regexp.Regexp
	repl string
}

// secretName matches parameter/field names that carry secrets: anything containing apikey,
// api_key, api-key, token, password, passwd, secret, passkey or authkey (e.g. X-Plex-Token,
// access_token, authToken, plexToken, passwordConfirmation).
const secretName = `(?:api[_-]?key|token|password|passwd|secret|passkey|authkey)`

// redactRules run in order. Each rule is idempotent on its own output, so Redact(Redact(s)) ==
// Redact(s).
var redactRules = []redactRule{
	// URL user info: scheme://user:password@host (SMTP, Apprise, proxies, …).
	{regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.\-]*://[^/\s:@?#]*:)[^/\s@?#]+@`), "${1}" + Removed + "@"},
	// Discord webhooks: https://discord.com/api/webhooks/<id>/<token>.
	{regexp.MustCompile(`(?i)(discord(?:app)?\.com/api/(?:v\d+/)?webhooks/\d+/)[A-Za-z0-9_\-]+`), "${1}" + Removed},
	// Slack webhooks: https://hooks.slack.com/services/T…/B…/<secret> (and workflows/triggers).
	{regexp.MustCompile(`(?i)(hooks\.slack\.com/(?:services|workflows|triggers)/)[^\s"'<>?#]+`), "${1}" + Removed},
	// Telegram bot tokens: bot<digits>:<secret> (api.telegram.org/bot123:ABC/sendMessage) and
	// Apprise tgram://<digits>:<secret>/….
	{regexp.MustCompile(`(?i)(\bbot\d{3,}:|tgram://\d{3,}:)[A-Za-z0-9_\-]+`), "${1}" + Removed},
	// Secret headers in "Name: value", "Name=value", JSON and Go http.Header dumps
	// (map[X-Plex-Token:[abc]]): X-Api-Key, X-Plex-Token, X-Gotify-Key, X-Emby-Token, … — any
	// X- header whose name ends in key, token, secret or password.
	{regexp.MustCompile(`(?i)(\bx-[a-z0-9\-]*(?:key|token|secret|password)"?\s*[:=]\s*"?\[?\s*)[^\s"'\],;&<>]+`), "${1}" + Removed},
	// Authorization headers, keeping the scheme: "Authorization: Bearer (removed)".
	{regexp.MustCompile(`(?i)(\b(?:proxy-)?authorization"?\s*[:=]\s*"?\[?\s*(?:(?:bearer|basic|token|digest|apikey)\s+)?)[^\s"'\],;<>]+`), "${1}" + Removed},
	// Cookies: everything up to the end of the header value.
	{regexp.MustCompile(`(?i)(\b(?:set-)?cookie"?\s*[:=]\s*"?\[?\s*)[^\r\n"'\]]+`), "${1}" + Removed},
	// JSON string fields whose name looks secret: "password":"…", "apiKey":"…", "authToken":"…".
	{regexp.MustCompile(`(?i)("[^"\s]*` + secretName + `[^"\s]*"\s*:\s*")(?:[^"\\]|\\.)+(")`), "${1}" + Removed + "${2}"},
	// The same inside an already-escaped string, e.g. an error quoting a response body with %q:
	// "{\"authToken\":\"…\"}".
	{regexp.MustCompile(`(?i)(\\"[^"\s\\]*` + secretName + `[^"\s\\]*\\"\s*:\s*\\")[^"\\]+(\\")`), "${1}" + Removed + "${2}"},
	// Go %+v dumps of structs and maps: {Name:plex Token:abc}, map[apiKey:abc]. The value must
	// follow the colon directly (prose such as "token: expired" is left alone).
	{regexp.MustCompile(`(?i)((?:^|[\s{(\[,;&"'])[a-z0-9_\-]*` + secretName + `[a-z0-9_\-]*:)[^\s,;&{}()\[\]"'<>]+`), "${1}" + Removed},
	// XML elements whose name looks secret: <ApiKey>…</ApiKey>, <Password>…</Password>.
	{regexp.MustCompile(`(?i)(<[a-z_\-]*` + secretName + `[a-z_\-]*>)[^<]+(</)`), "${1}" + Removed + "${2}"},
	// Query-string and key=value pairs: ?apikey=…, &api_key=…, &X-Plex-Token=…, token=…,
	// access_token=…, password=…, auth=….
	{regexp.MustCompile(`(?i)((?:^|[?&#;:\s,{(\["'])(?:[a-z0-9_.\-]*` + secretName + `|auth|pass|pwd)=)("?)[^&\s"'<>#]+`), "${1}${2}" + Removed},
}

// redactTriggers are lower-case substrings at least one of which occurs in any string a rule can
// match; strings without them skip the regular expressions entirely.
var redactTriggers = []string{"key", "token", "pass", "pwd", "secret", "auth", "cookie", "webhooks", "hooks.slack", "bot", "tgram", "@"}

// Redact masks secrets (api keys, tokens, passwords) in URLs and strings before logging.
//
// It covers query parameters (apikey=, api_key=, X-Plex-Token=, token=, access_token=,
// password=, …), headers (X-Api-Key, X-Plex-Token, Authorization, Cookie) in "Name: value",
// JSON and Go http.Header form, JSON/XML fields whose names contain password, apiKey, token,
// secret, …, URL user-info passwords, Discord and Slack webhook URL secrets and Telegram bot
// tokens. Each secret is replaced with "(removed)", like the *arr apps. The log handler applies it
// to every message and attribute automatically.
func Redact(s string) string {
	if s == "" || !mayContainSecret(s) {
		return s
	}
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

func mayContainSecret(s string) bool {
	lower := strings.ToLower(s)
	for _, t := range redactTriggers {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// sensitiveKey reports whether an attribute key, struct field or map key names a secret, so its
// whole value is masked (e.g. log.Info("…", "apiKey", k)). Keys are compared case-insensitively
// ignoring '-', '_', '.' and ' ' separators.
func sensitiveKey(key string) bool {
	k := strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '.', ' ':
			return -1
		}
		return r
	}, strings.ToLower(key))
	if k == "" {
		return false
	}
	for _, s := range []string{"apikey", "token", "password", "passwd", "secret", "passkey", "authkey", "authorization", "cookie", "credential", "privatekey", "userkey", "webhookurl"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	if lower := strings.ToLower(key); strings.HasPrefix(lower, "x-") && strings.HasSuffix(lower, "-key") {
		return true // secret headers such as X-Gotify-Key
	}
	return k == "auth" || k == "pass" || k == "pwd"
}
