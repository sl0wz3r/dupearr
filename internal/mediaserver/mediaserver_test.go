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

// TestListingFingerprint: equal listings give the same fingerprint whatever their order; any change
// of a row id, version id, path, size or part item id (or a shortcut) changes it.
func TestListingFingerprint(t *testing.T) {
	base := func() []ItemRef {
		return []ItemRef{
			{RatingKey: "r1", Media: []MediaRef{
				{VersionID: "v1", Parts: []PartRef{{File: "/m/a.mkv", Size: 10, ItemID: "v1"}}},
				{VersionID: "v2", Parts: []PartRef{{File: "/m/b-cd1.mkv", Size: 5, ItemID: "v2"}, {File: "/m/b-cd2.mkv", Size: 6, ItemID: "p2"}}},
			}},
			{RatingKey: "r2", Media: []MediaRef{{VersionID: "v3", Parts: []PartRef{{File: "/m/c.mkv", Size: 7, ItemID: "v3"}}}}},
		}
	}
	want := ListingFingerprint(base())
	if len(want) != 64 {
		t.Fatalf("fingerprint %q", want)
	}
	reordered := base()
	reordered[0], reordered[1] = reordered[1], reordered[0]
	reordered[1].Media[0], reordered[1].Media[1] = reordered[1].Media[1], reordered[1].Media[0]
	if got := ListingFingerprint(reordered); got != want {
		t.Fatal("the order of rows or versions changed the fingerprint")
	}
	for name, change := range map[string]func([]ItemRef){
		"row id":       func(r []ItemRef) { r[0].RatingKey = "r9" },
		"version id":   func(r []ItemRef) { r[0].Media[0].VersionID = "v9" },
		"path":         func(r []ItemRef) { r[1].Media[0].Parts[0].File = "/m/c2.mkv" },
		"size":         func(r []ItemRef) { r[1].Media[0].Parts[0].Size = 8 },
		"part item id": func(r []ItemRef) { r[0].Media[1].Parts[1].ItemID = "p9" },
		"part added": func(r []ItemRef) {
			r[1].Media[0].Parts = append(r[1].Media[0].Parts, PartRef{File: "/m/c-cd2.mkv", Size: 1})
		},
		"row removed": func(r []ItemRef) { r[1].Media = nil },
		"shortcut":    func(r []ItemRef) { r[1].Shortcuts = []PartRef{{File: "/m/c.strm", Size: 54}} },
	} {
		refs := base()
		change(refs)
		if ListingFingerprint(refs) == want {
			t.Errorf("%s: the fingerprint did not change", name)
		}
	}
	if ListingFingerprint(nil) != ListingFingerprint([]ItemRef{}) {
		t.Error("an empty listing has two fingerprints")
	}
}
