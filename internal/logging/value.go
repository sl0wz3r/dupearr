package logging

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Limits for renderAny, so that a huge or cyclic value cannot blow up a log line.
const (
	maxAnyDepth = 6
	maxAnyItems = 64
	maxAnyBytes = 8 << 10
)

// renderAny renders an arbitrary attribute value (slog.KindAny) like fmt's %+v, except that
// struct fields and map entries whose name looks secret (see sensitiveKey: Token, ApiKey,
// Password, X-Plex-Token, …) are replaced by Removed. fmt.Sprint would print a struct such as
// models.MediaServer as "{1 plex http://… abcdef…}", with no field names left for Redact to
// recognise, so structured values are walked here instead.
//
// errors and fmt.Stringers (also when only their pointer implements the method) render through
// fmt, which recovers from panicking methods; the caller still passes the result through Redact.
func renderAny(v any) (out string) {
	defer func() {
		if r := recover(); r != nil { // a panicking String/Error method outside fmt's protection
			out = fmt.Sprintf("%%!v(PANIC=%T)", v)
		}
	}()
	w := anyWriter{}
	w.value(reflect.ValueOf(v), 0)
	return w.b.String()
}

type anyWriter struct {
	b strings.Builder
}

func (w *anyWriter) full() bool { return w.b.Len() >= maxAnyBytes }

func (w *anyWriter) value(v reflect.Value, depth int) {
	if w.full() {
		w.b.WriteString("…")
		return
	}
	if !v.IsValid() {
		w.b.WriteString("<nil>")
		return
	}
	if s, ok := formatted(v); ok {
		w.b.WriteString(s)
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			w.b.WriteString("<nil>")
			return
		}
		if depth >= maxAnyDepth {
			w.b.WriteString("…")
			return
		}
		if v.Kind() == reflect.Pointer && depth == 0 {
			w.b.WriteByte('&')
		}
		w.value(v.Elem(), depth+1)
	case reflect.Struct:
		w.structValue(v, depth)
	case reflect.Map:
		w.mapValue(v, depth)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			w.b.WriteString("[]")
			return
		}
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 { // []byte as text
			w.b.Write(v.Bytes())
			return
		}
		if depth >= maxAnyDepth {
			w.b.WriteString("[…]")
			return
		}
		w.b.WriteByte('[')
		for i := range v.Len() {
			if i > 0 {
				w.b.WriteByte(' ')
			}
			if i == maxAnyItems || w.full() {
				w.b.WriteString("…")
				break
			}
			w.value(v.Index(i), depth+1)
		}
		w.b.WriteByte(']')
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		w.b.WriteString(v.Type().String())
	default:
		w.b.WriteString(fmt.Sprint(v.Interface()))
	}
}

func (w *anyWriter) structValue(v reflect.Value, depth int) {
	if depth >= maxAnyDepth {
		w.b.WriteString("{…}")
		return
	}
	t := v.Type()
	w.b.WriteByte('{')
	first := true
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() { // unexported state is never logged
			continue
		}
		if !first {
			w.b.WriteByte(' ')
		}
		first = false
		w.b.WriteString(f.Name)
		w.b.WriteByte(':')
		fv := v.Field(i)
		jsonName, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if (sensitiveKey(f.Name) || sensitiveKey(jsonName)) && !fv.IsZero() {
			w.b.WriteString(Removed)
			continue
		}
		w.value(fv, depth+1)
		if w.full() {
			w.b.WriteString("…")
			break
		}
	}
	w.b.WriteByte('}')
}

func (w *anyWriter) mapValue(v reflect.Value, depth int) {
	if depth >= maxAnyDepth {
		w.b.WriteString("map[…]")
		return
	}
	type entry struct {
		key string
		val reflect.Value
	}
	entries := make([]entry, 0, min(v.Len(), maxAnyItems+1))
	for it := v.MapRange(); it.Next(); {
		entries = append(entries, entry{key: renderAny(it.Key().Interface()), val: it.Value()})
	}
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.key, b.key) })
	w.b.WriteString("map[")
	for i, e := range entries {
		if i > 0 {
			w.b.WriteByte(' ')
		}
		if i == maxAnyItems || w.full() {
			w.b.WriteString("…")
			break
		}
		w.b.WriteString(e.key)
		w.b.WriteByte(':')
		if sensitiveKey(e.key) && !e.val.IsZero() {
			w.b.WriteString(Removed)
			continue
		}
		w.value(e.val, depth+1)
	}
	w.b.WriteByte(']')
}

// formatted renders v through fmt when it (or, for a struct, a pointer to it) is an error,
// fmt.Stringer or fmt.Formatter, e.g. time.Time, *url.URL or url.URL values.
func formatted(v reflect.Value) (string, bool) {
	if !v.CanInterface() {
		return "", false
	}
	if isFormattable(v.Interface()) {
		return fmt.Sprint(v.Interface()), true
	}
	if v.Kind() == reflect.Struct { // methods with pointer receivers, e.g. url.URL.String
		p := reflect.New(v.Type())
		p.Elem().Set(v)
		if isFormattable(p.Interface()) {
			return fmt.Sprint(p.Interface()), true
		}
	}
	return "", false
}

func isFormattable(x any) bool {
	switch x.(type) {
	case error, fmt.Stringer, fmt.Formatter:
		return true
	}
	return false
}
