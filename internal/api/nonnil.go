package api

import (
	"encoding"
	"encoding/json"
	"reflect"
	"sync"
)

// nonNil returns v with every nil slice and nil map reachable through exported struct fields,
// slices, arrays, maps, pointers and interfaces replaced by an empty one, so the JSON encoding
// contains [] / {} instead of null (docs/API.md). Nil pointers stay null (e.g. profileId, lastScan).
// Byte slices (json.RawMessage) and types with their own MarshalJSON/MarshalText are left alone.
//
// The input is never modified: the parts that need fixing are copied, so values shared with other
// goroutines (bus events, cached health results) are safe to pass. Values whose type cannot hold
// a slice or map are returned as is.
func nonNil(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if !needsFix(rv.Type()) {
		return v
	}
	return fixValue(rv, 0).Interface()
}

const maxFixDepth = 64

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()

	fixCache sync.Map // reflect.Type → bool
)

// customEncoding reports whether t controls its own JSON encoding.
func customEncoding(t reflect.Type) bool {
	if t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) {
		return true
	}
	if t.Kind() != reflect.Pointer {
		pt := reflect.PointerTo(t)
		return pt.Implements(jsonMarshalerType) || pt.Implements(textMarshalerType)
	}
	return false
}

// needsFix reports whether a value of type t can contain a nil slice or map that encodes as null.
func needsFix(t reflect.Type) bool {
	if v, ok := fixCache.Load(t); ok {
		return v.(bool)
	}
	res := computeNeedsFix(t, map[reflect.Type]bool{})
	fixCache.Store(t, res)
	return res
}

// computeNeedsFix walks t; types already being visited (recursive types) count as false for the
// branch that re-enters them — another branch decides.
func computeNeedsFix(t reflect.Type, visiting map[reflect.Type]bool) bool {
	if v, ok := fixCache.Load(t); ok {
		return v.(bool)
	}
	if visiting[t] {
		return false
	}
	visiting[t] = true
	defer delete(visiting, t)
	if customEncoding(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Slice:
		return t.Elem().Kind() != reflect.Uint8
	case reflect.Map, reflect.Interface:
		return true
	case reflect.Pointer, reflect.Array:
		return computeNeedsFix(t.Elem(), visiting)
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if f.IsExported() && f.Tag.Get("json") != "-" && computeNeedsFix(f.Type, visiting) {
				return true
			}
		}
	}
	return false
}

// fixValue returns a copy of v (same type) with nil slices/maps replaced.
func fixValue(v reflect.Value, depth int) reflect.Value {
	t := v.Type()
	if depth > maxFixDepth || !needsFix(t) {
		return v
	}
	switch t.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return reflect.MakeSlice(t, 0, 0)
		}
		out := reflect.MakeSlice(t, v.Len(), v.Len())
		if !needsFix(t.Elem()) {
			reflect.Copy(out, v)
			return out
		}
		for i := range v.Len() {
			out.Index(i).Set(fixValue(v.Index(i), depth+1))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.MakeMap(t)
		}
		out := reflect.MakeMapWithSize(t, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), fixValue(iter.Value(), depth+1))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(t.Elem())
		out.Elem().Set(fixValue(v.Elem(), depth+1))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(t).Elem()
		out.Set(fixValue(v.Elem(), depth+1))
		return out
	case reflect.Array:
		out := reflect.New(t).Elem()
		out.Set(v)
		for i := range v.Len() {
			out.Index(i).Set(fixValue(v.Index(i), depth+1))
		}
		return out
	case reflect.Struct:
		out := reflect.New(t).Elem()
		out.Set(v)
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" || !needsFix(f.Type) {
				continue
			}
			if fv := out.Field(i); fv.CanSet() {
				fv.Set(fixValue(v.Field(i), depth+1))
			}
		}
		return out
	}
	return v
}
