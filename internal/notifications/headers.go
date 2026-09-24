package notifications

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

// Masking of the webhook "headers" textarea ("Key: Value" per line).
//
// Custom headers routinely carry credentials (Authorization: Bearer …, X-Api-Key: …), but the
// list is edited as one multi-line text, so masking it whole would force users to retype every
// header on each edit. Instead each credential-bearing value is masked on its own line
// ("Authorization: ********") and restored by header name on update (MergeSecrets).

// benignHeaders are standard request headers that never carry credentials; their values stay
// visible in API responses. Every other header value is treated as a secret.
var benignHeaders = []string{
	"Accept", "Accept-Charset", "Accept-Encoding", "Accept-Language", "Cache-Control",
	"Content-Language", "Content-Type", "Pragma", "User-Agent", "X-Requested-With",
}

// isHeaderListField reports whether f is a header-list textarea (the webhook "headers").
func isHeaderListField(f FieldSchema) bool {
	return f.Name == "headers" && f.Type == fieldTextarea
}

// isHeaderListKey reports whether key names a header-list field of schema.
func isHeaderListKey(schema ProviderSchema, key string) bool {
	for _, f := range schema.Fields {
		if f.Name == key {
			return isHeaderListField(f)
		}
	}
	return false
}

// headerLine is one "Key: Value" line of a header list, possibly commented out ("# Key: Value").
type headerLine struct {
	comment bool
	name    string // as written
	value   string // trimmed
}

// splitHeaderLine parses one line leniently; ok=false for blank lines, prose comments and
// malformed lines.
func splitHeaderLine(line string) (headerLine, bool) {
	t := strings.TrimSpace(line)
	var hl headerLine
	if rest, found := strings.CutPrefix(t, "#"); found {
		hl.comment, t = true, strings.TrimSpace(rest)
	}
	name, value, ok := strings.Cut(t, ":")
	name = strings.TrimSpace(name)
	if !ok || !isHeaderToken(name) {
		return headerLine{}, false
	}
	hl.name, hl.value = name, strings.TrimSpace(value)
	return hl, true
}

// slot keys a header for matching masked and stored lines; commented-out headers have their own
// namespace so a masked comment can never restore an active header (or vice versa).
func (hl headerLine) slot() string {
	if hl.comment {
		return "#" + http.CanonicalHeaderKey(hl.name)
	}
	return http.CanonicalHeaderKey(hl.name)
}

// render formats the line with value.
func (hl headerLine) render(value string) string {
	if hl.comment {
		return "# " + hl.name + ": " + value
	}
	return hl.name + ": " + value
}

// isBenignHeader reports whether a header's value may be shown unmasked.
func isBenignHeader(name string) bool {
	return slices.Contains(benignHeaders, http.CanonicalHeaderKey(name))
}

// maskHeaders replaces every non-benign header value of a header list with MaskedValue, line by
// line (commented-out headers included). Malformed lines — never valid to save — are hidden whole;
// blank lines and prose comments are kept.
func maskHeaders(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		hl, ok := splitHeaderLine(t)
		switch {
		case !ok:
			if !strings.HasPrefix(t, "#") {
				lines[i] = MaskedValue
			}
		case hl.value == "" || hl.value == MaskedValue || isBenignHeader(hl.name):
			// nothing to hide
		default:
			lines[i] = hl.render(MaskedValue)
		}
	}
	return strings.Join(lines, "\n")
}

// mergeHeaders restores masked header values of incoming from stored: the n-th "Name: ********"
// line takes the value of the n-th "Name" line of stored (names compared canonically, comments in
// their own namespace). Masks without a stored counterpart are left for validation to reject.
func mergeHeaders(incoming, stored string) string {
	storedVals := map[string][]string{}
	for _, line := range strings.Split(stored, "\n") {
		if hl, ok := splitHeaderLine(line); ok {
			storedVals[hl.slot()] = append(storedVals[hl.slot()], hl.value)
		}
	}
	seen := map[string]int{}
	lines := strings.Split(incoming, "\n")
	for i, line := range lines {
		hl, ok := splitHeaderLine(line)
		if !ok {
			continue
		}
		slot := hl.slot()
		n := seen[slot]
		seen[slot]++
		if hl.value != MaskedValue {
			continue
		}
		if vals := storedVals[slot]; n < len(vals) && vals[n] != "" && vals[n] != MaskedValue {
			lines[i] = hl.render(vals[n])
		}
	}
	return strings.Join(lines, "\n")
}

// headerSecrets returns the non-benign values of a header list (commented-out ones included, plus
// the credential part of "Scheme credential" values) and every malformed line, for redaction.
func headerSecrets(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		hl, ok := splitHeaderLine(t)
		switch {
		case ok && hl.value != "" && !isBenignHeader(hl.name):
			out = append(out, hl.value)
			if f := strings.Fields(hl.value); len(f) > 1 {
				out = append(out, f[len(f)-1])
			}
		case !ok && !strings.HasPrefix(t, "#"):
			out = append(out, t)
		}
	}
	return out
}

// maskHeadersJSON masks a raw header-list setting. A non-string value is never echoed.
func maskHeadersJSON(raw json.RawMessage) json.RawMessage {
	s, k := decodeScalar(raw)
	switch k {
	case valueAbsent:
		return raw
	case valueString:
		return jsonString(maskHeaders(s))
	default:
		return maskedJSON
	}
}

// mergeHeadersJSON restores masked header values of a raw header-list setting from stored.
func mergeHeadersJSON(incoming, stored json.RawMessage) json.RawMessage {
	in, ik := decodeScalar(incoming)
	st, sk := decodeScalar(stored)
	if ik != valueString || sk != valueString || !strings.Contains(in, MaskedValue) {
		return incoming
	}
	return jsonString(mergeHeaders(in, st))
}

// jsonString encodes s as a raw JSON string.
func jsonString(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil { // cannot happen: strings always marshal
		return json.RawMessage(`""`)
	}
	return b
}
