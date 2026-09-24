package plex

import (
	"encoding/json"
	"math"
	"testing"
)

func TestFlexInt(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{`123`, 123},
		{`"123"`, 123},
		{`" 42 "`, 42},
		{`123.0`, 123},
		{`"9123456789"`, 9123456789},
		{`-5`, -5},
		{`true`, 1},
		{`false`, 0},
		{`"true"`, 1},
		{`null`, 0},
		{`""`, 0},
		{`"abc"`, 0},
		{`{}`, 0},
		{`[1,2]`, 0},
		{`1e30`, 0}, // out of range → 0, never a wrapped value
		{`"NaN"`, 0},
	}
	for _, tt := range tests {
		var v struct {
			N flexInt `json:"n"`
		}
		if err := json.Unmarshal([]byte(`{"n":`+tt.in+`}`), &v); err != nil {
			t.Fatalf("%s: unexpected error %v", tt.in, err)
		}
		if int64(v.N) != tt.want {
			t.Errorf("flexInt(%s) = %d, want %d", tt.in, v.N, tt.want)
		}
	}
}

func TestFlexIntPointerNull(t *testing.T) {
	var v struct {
		P *flexInt `json:"p"`
		Q *flexInt `json:"q"`
	}
	if err := json.Unmarshal([]byte(`{"p":null,"q":"0"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.P != nil {
		t.Errorf("null should leave a nil pointer (absent), got %d", *v.P)
	}
	if v.Q == nil || *v.Q != 0 {
		t.Errorf(`"0" should be a present zero, got %v`, v.Q)
	}
}

func TestFlexBool(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{`true`, true}, {`false`, false},
		{`1`, true}, {`0`, false},
		{`"1"`, true}, {`"0"`, false},
		{`"true"`, true}, {`"TRUE"`, true}, {`"false"`, false},
		{`"yes"`, true}, {`"no"`, false},
		{`2`, true}, {`null`, false}, {`""`, false}, {`"garbage"`, false},
		{`{}`, false}, {`[]`, false},
	}
	for _, tt := range tests {
		var v struct {
			B flexBool `json:"b"`
		}
		if err := json.Unmarshal([]byte(`{"b":`+tt.in+`}`), &v); err != nil {
			t.Fatalf("%s: unexpected error %v", tt.in, err)
		}
		if bool(v.B) != tt.want {
			t.Errorf("flexBool(%s) = %v, want %v", tt.in, v.B, tt.want)
		}
	}
}

func TestBoolPtrTriState(t *testing.T) {
	var v struct {
		A *flexBool `json:"accessible"`
		E *flexBool `json:"exists"`
	}
	if err := json.Unmarshal([]byte(`{"exists":"0"}`), &v); err != nil {
		t.Fatal(err)
	}
	if boolPtr(v.A) != nil {
		t.Error("absent attribute must map to nil (unknown)")
	}
	if p := boolPtr(v.E); p == nil || *p {
		t.Errorf(`exists "0" must map to a false pointer, got %v`, p)
	}
}

func TestFlexString(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`"abc"`, "abc"},
		{`1224`, "1224"},
		{`12.5`, "12.5"},
		{`true`, "true"},
		{`null`, ""},
		{`{"a":1}`, ""},
		{`["a"]`, ""},
		{`"  padded  "`, "padded"},
	}
	for _, tt := range tests {
		var v struct {
			S flexString `json:"s"`
		}
		if err := json.Unmarshal([]byte(`{"s":`+tt.in+`}`), &v); err != nil {
			t.Fatalf("%s: unexpected error %v", tt.in, err)
		}
		if v.S.String() != tt.want {
			t.Errorf("flexString(%s) = %q, want %q", tt.in, v.S.String(), tt.want)
		}
	}
}

func TestFlexFloat(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{`1.78`, 1.78}, {`"2.35"`, 2.35}, {`2`, 2}, {`null`, 0}, {`"x"`, 0}, {`"Infinity"`, 0},
	}
	for _, tt := range tests {
		var v struct {
			F flexFloat `json:"f"`
		}
		if err := json.Unmarshal([]byte(`{"f":`+tt.in+`}`), &v); err != nil {
			t.Fatalf("%s: unexpected error %v", tt.in, err)
		}
		if float64(v.F) != tt.want {
			t.Errorf("flexFloat(%s) = %v, want %v", tt.in, v.F, tt.want)
		}
	}
}

func TestListLenient(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"array", `[{"id":"1"},{"id":2}]`, 2},
		{"single object", `{"id":1}`, 1},
		{"null", `null`, 0},
		{"bool", `true`, 0},
		{"string", `"x"`, 0},
		{"empty array", `[]`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v struct {
				L list[locationDTO] `json:"Location"`
			}
			if err := json.Unmarshal([]byte(`{"Location":`+tt.in+`}`), &v); err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if len(v.L) != tt.want {
				t.Errorf("len = %d, want %d", len(v.L), tt.want)
			}
		})
	}
}

// A lower-case attribute that folds onto an element array name must not break decoding.
func TestContainerAttributeFoldingDoesNotBreakDecode(t *testing.T) {
	body := `{"MediaContainer":{"size":1,"directory":true,"metadata":"x","Metadata":[{"ratingKey":"1","guid":"plex://movie/abc","Guid":[{"id":"imdb://tt0000001"}]}]}}`
	var resp containerResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n := len(resp.MediaContainer.Metadata); n != 1 {
		t.Fatalf("Metadata len = %d, want 1", n)
	}
	m := resp.MediaContainer.Metadata[0]
	if m.GUID.String() != "plex://movie/abc" {
		t.Errorf("guid = %q", m.GUID.String())
	}
	if len(m.Guids) != 1 || m.Guids[0].ID != "imdb://tt0000001" {
		t.Errorf("Guid[] = %+v", m.Guids)
	}
}

func TestStreamHDR10PlusAttributeDetection(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{`{"streamType":1,"HDR10PlusPresent":true}`, true},
		{`{"streamType":1,"hdr10PlusPresent":"1"}`, true},
		{`{"streamType":1,"HDR10PlusPresent":false}`, false},
		{`{"streamType":1}`, false},
	}
	for _, tt := range tests {
		var s streamDTO
		if err := json.Unmarshal([]byte(tt.in), &s); err != nil {
			t.Fatalf("%s: %v", tt.in, err)
		}
		if s.HDR10Plus != tt.want || int(s.StreamType) != 1 {
			t.Errorf("%s: HDR10Plus = %v (type %d), want %v", tt.in, s.HDR10Plus, s.StreamType, tt.want)
		}
	}
}

func TestParseIntTextClamp(t *testing.T) {
	var f flexInt = 1 << 40
	want := int64(1) << 40
	if want > math.MaxInt { // 32-bit platforms: clamped to the int range
		want = math.MaxInt
	}
	if int64(f.Int()) != want {
		t.Errorf("Int() = %d, want %d", f.Int(), want)
	}
	if parseIntText("12.9") != 12 {
		t.Errorf("float truncation")
	}
}
