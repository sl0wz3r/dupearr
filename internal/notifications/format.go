package notifications

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Severity values of Message.Severity.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// EventTest is the Message.Event of test notifications (not a subscribable trigger).
const EventTest = "test"

const (
	defaultTitle = "Dupearr notification"
	linkLabel    = "Open in Dupearr"
	ellipsis     = "…"
)

// normalizeSeverity maps free-form severities onto info|warning|error (default info).
func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "warning", "warn":
		return SeverityWarning
	case "error", "err", "failure", "failed", "critical", "fatal":
		return SeverityError
	default:
		return SeverityInfo
	}
}

// eventType renders a models.On* event as the PascalCase webhook eventType, following the *arr
// convention: "onDuplicatesFound" → "DuplicatesFound", EventTest → "Test".
func eventType(event string) string {
	e := strings.TrimSpace(event)
	if e == "" {
		return "Unknown"
	}
	if strings.HasPrefix(e, "on") && len(e) > 2 {
		if r, _ := utf8.DecodeRuneInString(e[2:]); unicode.IsUpper(r) {
			e = e[2:]
		}
	}
	r, size := utf8.DecodeRuneInString(e)
	return string(unicode.ToUpper(r)) + e[size:]
}

// cleanText removes control characters except newlines/tabs, normalizes line endings and trims.
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case isControl(r), r == utf8.RuneError:
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// cleanLine is cleanText collapsed to a single line.
func cleanLine(s string) string {
	return strings.Join(strings.Fields(cleanText(s)), " ")
}

// truncate shortens s to at most max runes, ending with an ellipsis when cut.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	if max == 1 {
		return ellipsis
	}
	return strings.TrimRightFunc(string(r[:max-1]), unicode.IsSpace) + ellipsis
}

// truncateBytes shortens s to at most max bytes (on a rune boundary), ending with an ellipsis
// when cut.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	limit := max - len(ellipsis)
	if limit <= 0 {
		return ""
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return strings.TrimRightFunc(s[:limit], unicode.IsSpace) + ellipsis
}

// normalizeMessage sanitizes a message before rendering: severity, title fallback, control
// characters, empty fields. The caller's Fields slice is never modified.
func normalizeMessage(m Message) Message {
	out := Message{
		Event:    strings.TrimSpace(m.Event),
		Title:    cleanLine(m.Title),
		Body:     cleanText(m.Body),
		URL:      strings.TrimSpace(m.URL),
		Severity: normalizeSeverity(m.Severity),
	}
	if out.Title == "" {
		out.Title = defaultTitle
	}
	out.Fields = make([]Field, 0, len(m.Fields))
	for _, f := range m.Fields {
		f.Name, f.Value = cleanLine(f.Name), cleanText(f.Value)
		if f.Name == "" && f.Value == "" {
			continue
		}
		out.Fields = append(out.Fields, f)
	}
	return out
}

// absURL returns m.URL when it is an absolute http(s) URL (safe for providers that reject
// relative or malformed links), otherwise "".
func (m Message) absURL() string {
	if m.URL == "" || checkHTTPURL(m.URL) != "" {
		return ""
	}
	if _, err := url.Parse(m.URL); err != nil {
		return ""
	}
	return m.URL
}

// plainText renders body, fields ("Name: Value" lines) and link as plain text.
func plainText(m Message) string {
	var b strings.Builder
	b.WriteString(m.Body)
	if len(m.Fields) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		for i, f := range m.Fields {
			if i > 0 {
				b.WriteByte('\n')
			}
			writeFieldLine(&b, f)
		}
	}
	if u := m.absURL(); u != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(u)
	}
	if b.Len() == 0 {
		return m.Title
	}
	return b.String()
}

// writeFieldLine writes "Name: Value" (or whichever part is present).
func writeFieldLine(b *strings.Builder, f Field) {
	switch {
	case f.Name == "":
		b.WriteString(f.Value)
	case f.Value == "":
		b.WriteString(f.Name)
	default:
		b.WriteString(f.Name)
		b.WriteString(": ")
		b.WriteString(f.Value)
	}
}

// severityEmoji is a prefix for chat providers without colour support.
func severityEmoji(severity string) string {
	switch severity {
	case SeverityWarning:
		return "⚠️ "
	case SeverityError:
		return "🚨 "
	default:
		return ""
	}
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
