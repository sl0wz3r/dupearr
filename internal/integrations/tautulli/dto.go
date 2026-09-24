package tautulli

import (
	"bytes"
	"encoding/json"
	"html"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Wire types of Tautulli's API v2 (docs/research/watch-history.md). Tautulli is lenient with types:
// ids come as numbers or strings, a falsy value is often "" instead of 0, and string values are
// HTML-escaped. Decoding is lenient in what it accepts and strict in what it concludes: a missing
// field is "unknown", never "false" or "0".

// envelopeDTO is every answer: {"response": {"result": "success"|"error", "message": …, "data": …}}.
type envelopeDTO struct {
	Response *struct {
		Result  string          `json:"result"`
		Message json.RawMessage `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"response"`
}

// flexString accepts a JSON string, number or bool (and null / absent as "").
type flexString struct {
	s   string
	set bool
}

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*f = flexString{}
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString{s: s, set: true}
		return nil
	}
	*f = flexString{s: string(b), set: true}
	return nil
}

func (f flexString) String() string { return f.s }

// flexInt accepts a JSON number, a numeric string or a bool; "" and null are "not reported".
type flexInt struct {
	n   int64
	set bool
}

func (f *flexInt) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*f = flexInt{}
	switch {
	case bytes.Equal(b, []byte("null")):
		return nil
	case bytes.Equal(b, []byte("true")):
		*f = flexInt{n: 1, set: true}
		return nil
	case bytes.Equal(b, []byte("false")):
		*f = flexInt{n: 0, set: true}
		return nil
	}
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt{n: n, set: true}
		return nil
	}
	// A float ("1.0", 1e9): accepted when it is a whole number in range.
	x, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(x) || math.IsInf(x, 0) || x != math.Trunc(x) || math.Abs(x) > 1<<53 {
		return &json.UnmarshalTypeError{Value: "number " + s, Type: nil}
	}
	*f = flexInt{n: int64(x), set: true}
	return nil
}

// boolPtr returns the value as a flag, nil when not reported.
func (f flexInt) boolPtr() *bool {
	if !f.set {
		return nil
	}
	b := f.n != 0
	return &b
}

// unixTime converts a Unix timestamp (seconds); 0 / unset is the zero time.
func (f flexInt) unixTime() time.Time {
	if !f.set || f.n <= 0 {
		return time.Time{}
	}
	return time.Unix(f.n, 0).UTC()
}

type tautulliInfoDTO struct {
	Version flexString `json:"tautulli_version"`
}

type serverInfoDTO struct {
	PMSIdentifier flexString `json:"pms_identifier"`
	PMSName       flexString `json:"pms_name"`
}

// userDTO keeps the flags only: names and e-mail addresses are never decoded.
type userDTO struct {
	IsActive    flexInt `json:"is_active"`
	KeepHistory flexInt `json:"keep_history"`
}

type libraryDTO struct {
	SectionID   flexString `json:"section_id"`
	KeepHistory flexInt    `json:"keep_history"`
}

// historyDTO is get_history's data. RecordsFiltered is required: without it an answer cannot be
// told apart from an empty history.
type historyDTO struct {
	RecordsFiltered *flexInt         `json:"recordsFiltered"`
	Data            *json.RawMessage `json:"data"`
}

type historyRowDTO struct {
	RowID     flexInt    `json:"row_id"`
	RatingKey flexString `json:"rating_key"`
	GUID      flexString `json:"guid"`
	UserID    flexInt    `json:"user_id"`
	Date      flexInt    `json:"date"`
	Started   flexInt    `json:"started"`
	Stopped   flexInt    `json:"stopped"`
}

// cleanText makes a value from Tautulli safe for a message: HTML entities decoded, control and
// format characters dropped, at most 100 runes.
func cleanText(s string) string {
	s = html.UnescapeString(s)
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if n == 100 {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
