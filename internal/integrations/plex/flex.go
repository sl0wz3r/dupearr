package plex

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Lenient JSON scalar types. PMS (and plex.tv) mix numbers, numeric strings and "1"/"0"/true
// booleans for the same attribute across endpoints and versions (docs/research/plex-api.md §2.1).
// These types never fail to decode: unparseable values become the zero value, so one odd
// attribute cannot break a whole listing. JSON null leaves the zero value (and a nil pointer for
// pointer fields, which is how "absent" is told apart from false/0).

// flexInt accepts 123, 123.0, "123", " 123 ", true/false, null.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	*f = flexInt(parseFlexInt(b))
	return nil
}

// Int returns the value as int (clamped to the int range).
func (f flexInt) Int() int {
	v := int64(f)
	if v > math.MaxInt {
		return math.MaxInt
	}
	if v < math.MinInt {
		return math.MinInt
	}
	return int(v)
}

// flexFloat accepts 1.78, "1.78", 2, "2", null.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	v, ok := parseFlexFloat(b)
	if !ok {
		v = 0
	}
	*f = flexFloat(v)
	return nil
}

// flexBool accepts true/false, 1/0, "1"/"0", "true"/"false", "yes"/"no" (any case).
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	*f = flexBool(parseFlexBool(b))
	return nil
}

// flexString accepts a string, a number (kept verbatim, e.g. librarySectionID 1224 → "1224") or a
// boolean; null → "".
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || string(b) == "null":
		*f = ""
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			*f = ""
			return nil
		}
		*f = flexString(s)
	case b[0] == '{' || b[0] == '[':
		*f = "" // objects/arrays are not scalars
	default:
		*f = flexString(b)
	}
	return nil
}

func (f flexString) String() string { return strings.TrimSpace(string(f)) }

// scalarText returns the textual content of a JSON scalar (strings unquoted), "" for null,
// objects and arrays.
func scalarText(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" || b[0] == '{' || b[0] == '[' {
		return ""
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return ""
		}
		return strings.TrimSpace(s)
	}
	return string(b)
}

func parseFlexInt(b []byte) int64 {
	return parseIntText(scalarText(b))
}

// parseIntText parses an integer leniently ("12", "12.0", "true"); 0 when unparseable/out of range.
func parseIntText(s string) int64 {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return 0
	case "true":
		return 1
	case "false":
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f >= math.MaxInt64 || f <= math.MinInt64 {
		return 0
	}
	return int64(f)
}

func parseFlexFloat(b []byte) (float64, bool) {
	s := scalarText(b)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func parseFlexBool(b []byte) bool {
	return parseBoolText(scalarText(b))
}

// parseBoolText parses a boolean leniently; anything unrecognized is false.
func parseBoolText(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "y", "t":
		return true
	case "", "0", "false", "no", "off", "n", "f":
		return false
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return f != 0
	}
	return false
}

// boolPtr converts an optional flexBool to *bool (nil stays nil: "unknown").
func boolPtr(f *flexBool) *bool {
	if f == nil {
		return nil
	}
	v := bool(*f)
	return &v
}
