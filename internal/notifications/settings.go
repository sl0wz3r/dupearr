package notifications

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// MaskedValue replaces secret settings in API responses. Sending it back on update keeps the stored
// value (MergeSecrets); it is never accepted as a real secret (ValidateConfig, delivery).
const MaskedValue = "********"

// valueKind classifies a raw JSON settings value.
type valueKind int

const (
	valueAbsent valueKind = iota // missing or JSON null
	valueString
	valueNumber
	valueBool
	valueOther // object, array or malformed
)

// decodeScalar decodes a raw JSON value into its textual form.
func decodeScalar(raw json.RawMessage) (string, valueKind) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return "", valueAbsent
	}
	switch t[0] {
	case '"':
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return "", valueOther
		}
		return s, valueString
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(t, &b); err != nil {
			return "", valueOther
		}
		return strconv.FormatBool(b), valueBool
	case '{', '[':
		return "", valueOther
	default:
		var n json.Number
		if err := json.Unmarshal(t, &n); err != nil {
			return "", valueOther
		}
		return n.String(), valueNumber
	}
}

// decodeObject decodes a settings document. Empty input and JSON null yield an empty object;
// anything that is not a JSON object reports ok=false.
func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return map[string]json.RawMessage{}, true
	}
	if t[0] != '{' {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(t, &obj); err != nil {
		return nil, false
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	return obj, true
}

// encodeObject marshals a settings object (keys sorted by encoding/json).
func encodeObject(obj map[string]json.RawMessage) json.RawMessage {
	if len(obj) == 0 {
		return json.RawMessage("{}")
	}
	b, err := json.Marshal(obj)
	if err != nil {
		// Values are json.RawMessage produced by json.Unmarshal (always valid) or our own
		// marshalled strings, so this cannot happen; never leak the input on failure.
		return json.RawMessage("{}")
	}
	return b
}

// isEmptyValue reports whether a raw value is absent, null or an empty string.
func isEmptyValue(raw json.RawMessage) bool {
	s, k := decodeScalar(raw)
	return k == valueAbsent || (k == valueString && s == "")
}

// isMaskValue reports whether a raw value is exactly the MaskedValue string.
func isMaskValue(raw json.RawMessage) bool {
	s, k := decodeScalar(raw)
	return k == valueString && s == MaskedValue
}

// defaultString renders a FieldSchema default as text ("" when none).
func defaultString(f FieldSchema) string {
	switch d := f.Default.(type) {
	case nil:
		return ""
	case string:
		return d
	case bool:
		return strconv.FormatBool(d)
	case int:
		return strconv.Itoa(d)
	case int64:
		return strconv.FormatInt(d, 10)
	case float64:
		return strconv.FormatFloat(d, 'f', -1, 64)
	default:
		return fmt.Sprint(d)
	}
}

// settings is a typed view over a connection's settings object for one provider schema.
type settings struct {
	schema ProviderSchema
	fields map[string]FieldSchema
	raw    map[string]json.RawMessage
}

// errSettingsNotObject is returned when Settings is not a JSON object.
var errSettingsNotObject = errors.New("settings must be a JSON object")

// parseSettings builds a settings view; raw must be a JSON object (or empty/null).
func parseSettings(schema ProviderSchema, raw json.RawMessage) (*settings, error) {
	obj, ok := decodeObject(raw)
	if !ok {
		return nil, errSettingsNotObject
	}
	return settingsFor(schema, obj), nil
}

// settingsFor builds a settings view over an already decoded settings object.
func settingsFor(schema ProviderSchema, obj map[string]json.RawMessage) *settings {
	fields := make(map[string]FieldSchema, len(schema.Fields))
	for _, f := range schema.Fields {
		fields[f.Name] = f
	}
	return &settings{schema: schema, fields: fields, raw: obj}
}

// str returns the textual value of a field, falling back to its default when absent or empty.
// Values are trimmed, except passwords, tokens and multi-line secrets, which are returned verbatim
// (surrounding whitespace can be part of a password). Secret URLs and names (a webhook URL, an
// ntfy topic) are trimmed like any URL or name.
func (s *settings) str(name string) string {
	f := s.fields[name]
	v, k := decodeScalar(s.raw[name])
	if k == valueAbsent || k == valueOther {
		return defaultString(f)
	}
	if !f.Secret || f.Type == fieldURL || f.Type == fieldText {
		v = strings.TrimSpace(v)
	}
	if v == "" {
		return defaultString(f)
	}
	return v
}

// num returns an integer field; ok=false when empty or not an integer.
func (s *settings) num(name string) (int64, bool) {
	v := strings.TrimSpace(s.str(name))
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// boolean returns a checkbox field (true/false, case-insensitive; default when absent).
func (s *settings) boolean(name string) bool {
	return strings.EqualFold(strings.TrimSpace(s.str(name)), "true")
}

// list splits a comma/newline separated field into trimmed, non-empty items.
func (s *settings) list(name string) []string {
	return splitList(s.str(name))
}

// splitList splits on commas and newlines, trimming and dropping empty items.
func splitList(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sensitiveValues lists values that must never appear in logs or returned errors: every secret
// setting plus the token-bearing parts (path, query) of the provider URLs.
func (s *settings) sensitiveValues() []string {
	var out []string
	for _, f := range s.schema.Fields {
		v, k := decodeScalar(s.raw[f.Name])
		if k != valueString || v == "" {
			continue
		}
		if f.Secret {
			out = append(out, v)
			if t := strings.TrimSpace(v); t != v {
				out = append(out, t)
			}
			if f.Type == fieldTextarea {
				out = append(out, splitList(v)...)
			}
		}
		switch {
		case isHeaderListField(f):
			// Custom webhook headers often carry tokens (e.g. Authorization: Bearer …). Scanned
			// leniently so even a malformed list never reaches a log unredacted.
			out = append(out, headerSecrets(v)...)
		case f.Type == fieldURL:
			// Webhook URLs (Discord, Slack, generic) carry their credential in the path/query.
			if u, err := url.Parse(strings.TrimSpace(v)); err == nil {
				if len(u.EscapedPath()) > 1 {
					out = append(out, u.EscapedPath(), u.Path)
				}
				if u.RawQuery != "" {
					out = append(out, u.RawQuery)
				}
				if u.User != nil {
					out = append(out, u.User.String())
				}
			}
		}
	}
	return out
}

// isSecretKey reports whether key of a settings object is masked for the given schema: schema
// secrets, and — defensively — keys the schema does not know (or every key of an unknown kind).
func isSecretKey(schema ProviderSchema, known bool, key string) bool {
	if !known {
		return true
	}
	for _, f := range schema.Fields {
		if f.Name == key {
			return f.Secret
		}
	}
	return true
}

// cloneTriggers returns a non-nil copy of triggers (JSON emits [] rather than null).
func cloneTriggers(t []string) []string {
	if t == nil {
		return []string{}
	}
	return slices.Clone(t)
}

var maskedJSON = json.RawMessage(strconv.Quote(MaskedValue))

// isPartlyMaskedURLKey reports whether key is a URL setting shown with its credential-bearing parts
// masked (maskURL) rather than whole: the generic webhook URL, whose host and path prefix identify
// the receiver while a token often hides in its last path segment or query.
func isPartlyMaskedURLKey(schema ProviderSchema, key string) bool {
	return schema.Kind == KindWebhook && key == "url"
}

// maskURL hides the parts of a URL that commonly carry a credential: userinfo, the last non-empty
// path segment (Home Assistant / n8n webhook ids, …), earlier path segments that look like ids or
// tokens (tokenLikeSegment), every query value (valueless parameters whole) and the fragment.
// Scheme, host, port, route names in the path and query keys stay readable. An unparsable or
// opaque URL is masked whole.
func maskURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return MaskedValue
	}
	var b strings.Builder
	b.WriteString(u.Scheme + "://")
	if u.User != nil {
		b.WriteString(MaskedValue + "@")
	}
	b.WriteString(u.Host)
	segs := strings.Split(u.EscapedPath(), "/")
	last := true
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] == "" {
			continue
		}
		if last || tokenLikeSegment(segs[i]) {
			segs[i] = MaskedValue
		}
		last = false
	}
	b.WriteString(strings.Join(segs, "/"))
	if u.RawQuery != "" || u.ForceQuery {
		params := strings.Split(u.RawQuery, "&")
		for i, p := range params {
			if k, _, ok := strings.Cut(p, "="); ok {
				params[i] = k + "=" + MaskedValue
			} else if p != "" {
				params[i] = MaskedValue
			}
		}
		b.WriteString("?" + strings.Join(params, "&"))
	}
	if u.Fragment != "" || strings.HasSuffix(raw, "#") {
		b.WriteString("#" + MaskedValue)
	}
	return b.String()
}

// tokenLikeSegment reports an (escaped) path segment before the last one that may be a credential
// rather than a route name: long ones, ids and tokens with digits (Discord's /webhooks/<id>/<token>/slack,
// Telegram's /bot<id>:<token>/sendMessage, Slack's /T…/B…/…) and segments carrying parameters or
// escapes (";token=…", "%2B"). Route names such as "api", "v1", "webhook" or "services" stay readable.
func tokenLikeSegment(seg string) bool {
	return len(seg) >= 16 || strings.ContainsAny(seg, ";=:@%") ||
		(len(seg) >= 6 && strings.ContainsAny(seg, "0123456789"))
}

// maskURLJSON masks a raw URL setting (maskURL). A non-string value is never echoed.
func maskURLJSON(raw json.RawMessage) json.RawMessage {
	s, k := decodeScalar(raw)
	switch {
	case k == valueAbsent || (k == valueString && strings.TrimSpace(s) == ""):
		return raw
	case k == valueString:
		return jsonString(maskURL(s))
	default:
		return maskedJSON
	}
}

// restoreMaskedURLs puts back stored partly masked URLs (isPartlyMaskedURLKey) that incoming sends
// exactly as MaskSecrets rendered them. An edited masked URL is left as is (and rejected by
// ValidateConfig, since it still contains the mask).
func restoreMaskedURLs(schema ProviderSchema, inObj, stObj map[string]json.RawMessage) {
	for k, v := range inObj {
		if !isPartlyMaskedURLKey(schema, k) {
			continue
		}
		in, ik := decodeScalar(v)
		st, sk := decodeScalar(stObj[k])
		if ik != valueString || sk != valueString || !strings.Contains(in, MaskedValue) {
			continue
		}
		if masked := maskURL(st); masked != strings.TrimSpace(st) && strings.TrimSpace(in) == masked {
			inObj[k] = stObj[k]
		}
	}
}

// MaskSecrets returns a copy of cfg with secret settings replaced by "********".
//
// Empty secrets stay empty (so the UI can tell "not set" from "set"). Keys the provider schema does
// not define — and every key of an unknown kind — are treated as secrets. In the webhook "headers"
// list every header value except well-known non-credential headers (Accept, Content-Type, …) is
// masked line by line ("Authorization: ********"), commented-out headers included. The generic
// webhook URL keeps its scheme, host and path prefix but has its credential-bearing parts masked
// (see maskURL). Settings that are not a JSON object are replaced by {} rather than echoed.
// Triggers is never nil.
func MaskSecrets(cfg models.NotificationConfig) models.NotificationConfig {
	out := cfg
	out.Triggers = cloneTriggers(cfg.Triggers)
	obj, ok := decodeObject(cfg.Settings)
	if !ok {
		out.Settings = json.RawMessage("{}")
		return out
	}
	schema, known := providerSchema(cfg.Kind)
	for k, v := range obj {
		if known && isHeaderListKey(schema, k) {
			obj[k] = maskHeadersJSON(v)
			continue
		}
		if known && isPartlyMaskedURLKey(schema, k) {
			obj[k] = maskURLJSON(v)
			continue
		}
		if !isSecretKey(schema, known, k) || isEmptyValue(v) {
			continue
		}
		if !known {
			// Unknown kind: keep non-string scalars (numbers, booleans) readable.
			if _, kind := decodeScalar(v); kind == valueNumber || kind == valueBool {
				continue
			}
		}
		obj[k] = maskedJSON
	}
	out.Settings = encodeObject(obj)
	return out
}

// MergeSecrets restores masked values from the stored config on update.
//
// Every setting of incoming that still holds "********" (and that MaskSecrets would have masked)
// takes the stored value of the same key, masked webhook header values take the stored value
// of the same header (matched by name and occurrence), and a partly masked webhook URL sent back
// exactly as MaskSecrets rendered it takes the stored URL. Secrets are only restored when both
// configs are of the same Kind and the destination they are sent to is unchanged (server URL,
// webhook URL, SMTP host and port — see destinationKey): otherwise anyone able to edit a
// connection could point it at their own server and have Test deliver the stored secret there.
// Unresolvable masks are left in place; ValidateConfig then reports them ("re-enter the value")
// so the placeholder is never saved or sent. Call MergeSecrets before ValidateConfig.
func MergeSecrets(incoming, stored models.NotificationConfig) models.NotificationConfig {
	out := incoming
	out.Triggers = cloneTriggers(incoming.Triggers)
	inObj, ok := decodeObject(incoming.Settings)
	if !ok {
		return out
	}
	schema, known := providerSchema(incoming.Kind)
	var stObj map[string]json.RawMessage
	if stored.Kind == incoming.Kind {
		stObj, _ = decodeObject(stored.Settings)
	}
	if known && stObj != nil {
		// Before the destination check: an unchanged masked URL is the stored destination.
		restoreMaskedURLs(schema, inObj, stObj)
	}
	if known && stObj != nil &&
		destinationKey(settingsFor(schema, inObj)) != destinationKey(settingsFor(schema, stObj)) {
		stObj = nil // entered for another destination: never forward them to the new one
	}
	for k, v := range inObj {
		if known && isHeaderListKey(schema, k) {
			if sv, ok := stObj[k]; ok {
				inObj[k] = mergeHeadersJSON(v, sv)
			}
			continue
		}
		if !isMaskValue(v) || !isSecretKey(schema, known, k) {
			continue
		}
		if sv, ok := stObj[k]; ok && !isEmptyValue(sv) && !isMaskValue(sv) {
			inObj[k] = sv
		}
	}
	out.Settings = encodeObject(inObj)
	return out
}

// destinationKey identifies where a connection sends its secrets: the server URL (Gotify, ntfy,
// Apprise), the webhook URL, or the SMTP host and port. MergeSecrets only restores stored secrets
// while it is unchanged. Providers with a fixed endpoint (Telegram, Pushover) or whose only secret
// is the destination itself (the Discord/Slack webhook URL) return "". URLs are compared verbatim
// (trimmed, defaults applied): the path matters too, since multi-tenant receivers (webhook.site, …)
// hand out one path per tenant.
func destinationKey(set *settings) string {
	switch set.schema.Kind {
	case KindGotify, KindNtfy, KindApprise:
		return set.str("serverUrl")
	case KindWebhook:
		return set.str("url")
	case KindEmail:
		return strings.ToLower(smtpHost(set)) + "|" + strconv.Itoa(smtpPort(set))
	default:
		return ""
	}
}
