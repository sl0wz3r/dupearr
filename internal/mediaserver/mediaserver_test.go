package mediaserver

import (
	"math"
	"testing"
)

// TestVersionIDOf: a Plex media (no VersionID) is keyed by its numeric id, exactly as the legacy
// "plex:%d:%d" key wrote it; a server's own version id wins when set.
func TestVersionIDOf(t *testing.T) {
	for _, tc := range []struct {
		m    MediaRef
		want string
	}{
		{MediaRef{ID: 42}, "42"},
		{MediaRef{ID: math.MaxInt64}, "9223372036854775807"},
		{MediaRef{ID: 0}, "0"},
		{MediaRef{ID: 7, VersionID: "a1b2"}, "a1b2"},
		{MediaRef{VersionID: "src"}, "src"},
	} {
		if got := VersionIDOf(&tc.m); got != tc.want {
			t.Errorf("VersionIDOf(%+v) = %q, want %q", tc.m, got, tc.want)
		}
	}
}
