package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// timeLayout is the *arr log timestamp (NLog "yyyy-MM-dd HH:mm:ss.fff"), in local time.
	timeLayout = "2006-01-02 15:04:05.000"
	// defaultComponent is Entry.Logger for records without a "component" attribute.
	defaultComponent = "Dupearr"
	maxComponentLen  = 64

	// maxMessageBytes and maxValueBytes cap the message and each attribute value of a record, and
	// maxTextBytes the whole rendered text (message, attributes and errors). Values often come
	// from upstreams (titles and paths from Plex and the *arrs, error texts): a hostile one could
	// otherwise keep megabytes per entry in the ring buffer and rotate every earlier record out
	// of the log files with a handful of lines. Truncation happens after redaction, so a secret
	// is never cut out of a redaction match. A stack trace (a recovered panic) gets more room.
	maxMessageBytes = 2 << 10
	maxValueBytes   = 2 << 10
	maxStackBytes   = 16 << 10
	maxTextBytes    = 24 << 10
)

// capText truncates s to at most limit bytes (on a UTF-8 boundary) and appends a marker saying how
// many bytes were dropped.
func capText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(truncated " + strconv.Itoa(len(s)-cut) + " bytes)"
}

// kv is one rendered (and redacted) attribute.
type kv struct{ key, val string }

// core is the state shared by the root handler and every handler derived from it.
type core struct {
	level *slog.LevelVar

	mu         sync.Mutex
	file       *rotator // nil once closed
	stdout     io.Writer
	ring       ring
	fileFailed bool // the last file write failed (reported once on stdout)
}

// write emits one record to the file, stdout and (when e != nil) the ring buffer.
func (c *core) write(level slog.Level, fileLine, consoleLine string, e *Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var err error
	if c.file != nil {
		if _, err = io.WriteString(c.file, fileLine); err != nil {
			if !c.fileFailed && c.stdout != nil {
				_, _ = fmt.Fprintf(c.stdout, "%s [Error] Logging: %s\n", time.Now().Format(timeLayout), escapeControl(Redact(err.Error())))
			}
			c.fileFailed = true
		} else {
			c.fileFailed = false
		}
	}
	if c.stdout != nil {
		_, _ = io.WriteString(c.stdout, consoleLine)
	}
	if e != nil {
		c.ring.add(*e, level)
	}
	return err
}

// handler is Dupearr's slog.Handler. It renders each record once and writes it as an *arr-style
// line to the log file ("2026-09-22 17:30:00.123|Info|Scanner|message k=v"), as a human line to
// stdout ("2026-09-22 17:30:00.123 [Info] Scanner: message k=v") and, for info and above, into
// the ring buffer behind System → Events.
//
// A top-level "component" attribute becomes the logger name; top-level "error"/"err" attributes
// become Entry.Exception (and stay at the end of the text lines). The message, the component and
// every attribute value are passed through Redact; attributes whose key names a secret (apiKey,
// token, password, …) are replaced by "(removed)" entirely, and so are secret-named struct fields
// and map entries inside structured values (see renderAny). Control characters and Unicode line
// separators in the message are escaped so a record always stays on one line.
type handler struct {
	core      *core
	component string
	attrs     []kv   // from WithAttrs
	errs      []kv   // top-level error attributes from WithAttrs
	groups    string // open group prefix: "" or "a.b."
}

var _ slog.Handler = (*handler)(nil)

// Enabled implements slog.Handler.
func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.core.level.Level() }

// WithAttrs implements slog.Handler.
func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	st := h.state()
	for _, a := range as {
		st.add(a, h.groups)
	}
	return &handler{core: h.core, component: st.component, attrs: st.attrs, errs: st.errs, groups: h.groups}
}

// WithGroup implements slog.Handler.
func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := *h
	h2.groups = h.groups + name + "."
	return &h2
}

// Handle implements slog.Handler.
func (h *handler) Handle(_ context.Context, r slog.Record) error {
	st := h.state()
	r.Attrs(func(a slog.Attr) bool {
		st.add(a, h.groups)
		return true
	})

	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	component := st.component
	if component == "" {
		component = defaultComponent
	}
	msg := escapeControl(capText(Redact(r.Message), maxMessageBytes))
	attrText := capText(joinKV(st.attrs), maxTextBytes)
	text := capText(joinNonEmpty(msg, attrText, joinKV(st.errs)), maxTextBytes)

	stamp := t.Format(timeLayout)
	title := levelTitle(r.Level)
	fileLine := stamp + "|" + title + "|" + component + "|" + text + "\n"
	consoleLine := stamp + " [" + title + "] " + component + ": " + text + "\n"

	var entry *Entry
	if r.Level >= slog.LevelInfo {
		exc := make([]string, len(st.errs))
		for i, e := range st.errs {
			exc[i] = e.val
		}
		entry = &Entry{
			Time:      t.UTC(),
			Level:     LevelName(r.Level),
			Logger:    component,
			Message:   capText(joinNonEmpty(msg, attrText), maxTextBytes),
			Exception: capText(strings.Join(exc, "; "), maxTextBytes),
		}
	}
	return h.core.write(r.Level, fileLine, consoleLine, entry)
}

// recordState accumulates the attributes of one record (or one WithAttrs call).
type recordState struct {
	component   string
	attrs, errs []kv
}

// state returns a state seeded with the handler's attributes; the slices are clipped so that
// appends never write into the handler's backing arrays.
func (h *handler) state() recordState {
	return recordState{component: h.component, attrs: slices.Clip(h.attrs), errs: slices.Clip(h.errs)}
}

// add renders a (flattening groups into dotted keys) under prefix.
func (st *recordState) add(a slog.Attr, prefix string) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		p := prefix
		if a.Key != "" {
			p = prefix + a.Key + "."
		}
		for _, ga := range group {
			st.add(ga, p)
		}
		return
	}
	if a.Key == "" {
		return // includes the zero Attr, which handlers must ignore
	}
	top := prefix == ""
	if top && a.Key == "component" {
		if c := cleanComponent(Redact(valueString(a.Value))); c != "" {
			st.component = c
		}
		return
	}
	val := valueString(a.Value)
	if val != "" && sensitiveKey(a.Key) {
		val = Removed
	} else {
		limit := maxValueBytes
		if a.Key == "stack" {
			limit = maxStackBytes
		}
		val = capText(Redact(val), limit)
	}
	item := kv{key: prefix + a.Key, val: val}
	if top && (a.Key == "error" || a.Key == "err") {
		st.errs = append(st.errs, item)
		return
	}
	st.attrs = append(st.attrs, item)
}

// valueString renders a resolved, non-group value.
func valueString(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	case slog.KindAny:
		return renderAny(v.Any()) // masks secret-named struct fields and map keys
	default:
		return v.String()
	}
}

// joinKV renders attributes as space-separated key=value pairs, quoting where needed.
func joinKV(kvs []kv) string {
	if len(kvs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, a := range kvs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteIfNeeded(a.key))
		b.WriteByte('=')
		b.WriteString(quoteIfNeeded(a.val))
	}
	return b.String()
}

func joinNonEmpty(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// quoteIfNeeded quotes s (Go syntax) when it is empty or contains spaces, '=', '"' or anything
// non-printable, like slog.TextHandler; this also keeps attribute values on one line.
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		if r == ' ' || r == '=' || r == '"' || r == utf8.RuneError || !unicode.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

// escapeControl escapes control characters (newlines, carriage returns, escape sequences, …) so a
// message cannot break the one-record-per-line format or forge log lines. Tabs are kept.
func escapeControl(s string) string {
	if !strings.ContainsFunc(s, isEscaped) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case isEscaped(r):
			q := strconv.QuoteRuneToASCII(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isEscaped reports whether r must not appear raw in a log line: control characters other than
// tab, and the Unicode line/paragraph separators some viewers treat as line breaks.
func isEscaped(r rune) bool {
	return r != '\t' && (unicode.IsControl(r) || r == '\u2028' || r == '\u2029')
}

// cleanComponent makes a component name safe for the "|"-separated file format.
func cleanComponent(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r == '|' || r == '\t' || isEscaped(r) {
			return '_'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxComponentLen {
		s = string(r[:maxComponentLen])
	}
	return s
}

// levelTitle is the NLog-style level name used in log lines.
func levelTitle(l slog.Level) string {
	switch LevelName(l) {
	case "trace":
		return "Trace"
	case "debug":
		return "Debug"
	case "info":
		return "Info"
	case "warn":
		return "Warn"
	default:
		return "Error"
	}
}

// ring is a fixed-size circular buffer of the most recent entries.
type ring struct {
	buf  []ringEntry
	next int
	full bool
}

type ringEntry struct {
	entry Entry
	level slog.Level
}

func newRing(size int) ring { return ring{buf: make([]ringEntry, size)} }

func (r *ring) add(e Entry, l slog.Level) {
	if len(r.buf) == 0 {
		return
	}
	r.buf[r.next] = ringEntry{entry: e, level: l}
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// newestFirst calls fn for each entry, newest to oldest, until fn returns false.
func (r *ring) newestFirst(fn func(ringEntry) bool) {
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	for i := range n {
		if !fn(r.buf[(r.next-1-i+len(r.buf))%len(r.buf)]) {
			return
		}
	}
}
