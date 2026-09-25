package models

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The key helpers replace fmt.Sprintf expressions whose output is stored (group keys, version keys,
// signatures): for Plex they must produce exactly the legacy bytes, or every stored group would be
// re-keyed and lose its stable count and approvals.

// TestVersionKeyMatchesLegacyPlexFormat compares VersionKey with the legacy "plex:%d:%d".
func TestVersionKeyMatchesLegacyPlexFormat(t *testing.T) {
	for _, tc := range []struct{ sid, mid int64 }{
		{1, 1}, {1, 100}, {2, 95}, {12, 3456789}, {1, math.MaxInt64}, {math.MaxInt64, math.MaxInt64}, {0, 7},
	} {
		want := fmt.Sprintf("plex:%d:%d", tc.sid, tc.mid)
		for _, kind := range []MediaServerKind{"", MediaServerPlex} {
			if got := VersionKey(kind, tc.sid, strconv.FormatInt(tc.mid, 10)); got != want {
				t.Errorf("VersionKey(%q, %d, %d) = %q, want %q", kind, tc.sid, tc.mid, got, want)
			}
			v := MediaVersion{ServerID: tc.sid, MediaID: tc.mid}
			if got := VersionKey(kind, v.ServerID, v.ServerVersionID()); got != want {
				t.Errorf("VersionKey(%q, ServerVersionID) = %q, want %q", kind, got, want)
			}
		}
	}
	if got := (&MediaVersion{}).ServerVersionID(); got != "" {
		t.Errorf("ServerVersionID without a media id = %q, want \"\"", got)
	}
	if got := VersionKey("k", 3, "abc"); got != "k:3:abc" {
		t.Errorf("VersionKey of another kind = %q, want k:3:abc", got)
	}
}

// TestServerItemKeyMatchesLegacy compares the group-key fallback and the disambiguation suffix with
// the legacy "plex:%d:%s" of the trimmed rating key.
func TestServerItemKeyMatchesLegacy(t *testing.T) {
	for _, tc := range []struct {
		sid int64
		rk  string
	}{{1, "100"}, {2, " 95 "}, {7, ""}, {math.MaxInt64, "abc123"}, {3, "\t42\n"}} {
		want := fmt.Sprintf("plex:%d:%s", tc.sid, strings.TrimSpace(tc.rk))
		for _, kind := range []MediaServerKind{"", MediaServerPlex} {
			it := MediaItem{ServerKind: kind, RatingKey: tc.rk}
			if got := ServerItemKey(it.ServerKind, tc.sid, it.KeyItemID()); got != want {
				t.Errorf("ServerItemKey(%q, %d, %q) = %q, want %q", kind, tc.sid, tc.rk, got, want)
			}
			if got := "@" + ServerItemKey(kind, tc.sid, it.KeyItemID()); got != fmt.Sprintf("@plex:%d:%s", tc.sid, strings.TrimSpace(tc.rk)) {
				t.Errorf("suffix = %q", got)
			}
		}
	}
}

// legacyDisambiguation is the expression DisambiguationIndex replaces.
func legacyDisambiguation(s string) (int, int) {
	if i := strings.Index(s, "@plex:"); i >= 0 {
		return i, len("@plex:")
	}
	return -1, 0
}

func TestDisambiguationIndexMatchesLegacy(t *testing.T) {
	for _, s := range []string{
		"", "movie:tmdb:1", "movie:tmdb:1@plex:2:30", "movie:tmdb:1@plex:2:30#ed-extended", "@plex:", "@plex",
		"movie:tmdb:1#3d~2", "episode:tvdb:5@plex:1:9@plex:2:3", "movie:tmdb:1@jellyfin:2:x", "a@PLEX:1:2",
		"movie:plex:abc@plex:1:2", "x@plexy:1", "@disc:1:2",
	} {
		wi, wn := legacyDisambiguation(s)
		if i, n := DisambiguationIndex(s); i != wi || n != wn {
			t.Errorf("DisambiguationIndex(%q) = %d, %d, want %d, %d", s, i, n, wi, wn)
		}
	}
}

func FuzzDisambiguationIndex(f *testing.F) {
	for _, s := range []string{"movie:tmdb:1@plex:2:30#ed-x", "@plex:", "@ple", "a@plex:b@plex:c", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		wi, wn := legacyDisambiguation(s)
		if i, n := DisambiguationIndex(s); i != wi || n != wn {
			t.Fatalf("DisambiguationIndex(%q) = %d, %d, want %d, %d", s, i, n, wi, wn)
		}
	})
}

func TestKindOfVersionKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		kind MediaServerKind
		ok   bool
	}{
		{"plex:1:2", MediaServerPlex, true},
		{"plex:12:345", MediaServerPlex, true},
		{"disc:1:abcdef", "", false},
		{"jellyfin:1:x", "", false}, // not a kind stored keys can carry yet
		{"plexy:1:2", "", false},
		{"", "", false},
	} {
		if k, ok := KindOfVersionKey(tc.key); k != tc.kind || ok != tc.ok {
			t.Errorf("KindOfVersionKey(%q) = %q, %v, want %q, %v", tc.key, k, ok, tc.kind, tc.ok)
		}
	}
}

func TestMediaServerKindPredicates(t *testing.T) {
	for _, tc := range []struct {
		kind           MediaServerKind
		plex, supports bool
		prefix         string
	}{
		{"", true, true, "plex"},
		{MediaServerPlex, true, true, "plex"},
		{"Plex", false, false, "Plex"}, // kinds are compared exactly, as before
		{"jellyfin", false, false, "jellyfin"},
	} {
		if got := tc.kind.IsPlex(); got != tc.plex {
			t.Errorf("%q.IsPlex() = %v", tc.kind, got)
		}
		if got := tc.kind.Supported(); got != tc.supports {
			t.Errorf("%q.Supported() = %v", tc.kind, got)
		}
		if got := tc.kind.KeyPrefix(); got != tc.prefix {
			t.Errorf("%q.KeyPrefix() = %q", tc.kind, got)
		}
		// The SQL list and the predicate must never disagree.
		if in := slices.Contains(SupportedMediaServerKinds(), string(tc.kind)); in != tc.kind.Supported() {
			t.Errorf("%q: in SupportedMediaServerKinds = %v, Supported = %v", tc.kind, in, tc.kind.Supported())
		}
	}
	for _, k := range SupportedMediaServerKinds() {
		if !MediaServerKind(k).Supported() {
			t.Errorf("SupportedMediaServerKinds lists %q, which Supported refuses", k)
		}
	}
	if got := SupportedMediaServerKinds(); !slices.Equal(got, []string{"plex", ""}) {
		t.Errorf("SupportedMediaServerKinds = %q, want the legacy order [plex, \"\"]", got)
	}
}

func TestKeyItemIDFallsBackToRatingKey(t *testing.T) {
	it := MediaItem{RatingKey: " 100 "}
	if got := it.KeyItemID(); got != "100" {
		t.Errorf("item without KeyID: %q, want the trimmed rating key", got)
	}
	it.KeyID = "  "
	if got := it.KeyItemID(); got != "100" {
		t.Errorf("item with a blank KeyID: %q, want the trimmed rating key", got)
	}
	it.KeyID = " stable "
	if got := it.KeyItemID(); got != "stable" {
		t.Errorf("item with KeyID: %q, want the trimmed KeyID", got)
	}
	v := MediaVersion{RatingKey: "95 "}
	if got := v.KeyItemID(); got != "95" {
		t.Errorf("version without ItemKeyID: %q", got)
	}
	v.ItemKeyID = "stable"
	if got := v.KeyItemID(); got != "stable" {
		t.Errorf("version with ItemKeyID: %q", got)
	}
}

// TestMediaVersionJSONOmitsItemKeyID: the new fields are omitted while empty (always for Plex), so
// stored versions and API answers keep their bytes.
func TestMediaVersionJSONOmitsItemKeyID(t *testing.T) {
	b, err := json.Marshal(MediaVersion{Key: "plex:1:2", RatingKey: "5", MediaID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "itemKeyId") {
		t.Errorf("a Plex version marshals itemKeyId: %s", b)
	}
	b, err = json.Marshal(MediaVersion{ItemKeyID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"itemKeyId":"x"`) {
		t.Errorf("ItemKeyID is not marshalled when set: %s", b)
	}
	b, err = json.Marshal(MediaItem{RatingKey: "5", ServerKind: "", KeyID: ""})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "serverKind") || strings.Contains(s, "keyId") {
		t.Errorf("a Plex item marshals the new fields: %s", b)
	}
}

// TestKeyKindsFollowSupportedKinds: the kinds whose keys the parsers recognise come from
// SupportedMediaServerKinds, so a kind added there also has its "@<kind>:" suffix stripped from
// exclusions and stored keys and its version keys attributed to it. Phase 0: exactly plex.
func TestKeyKindsFollowSupportedKinds(t *testing.T) {
	kinds := keyKinds()
	if !slices.Equal(kinds, []MediaServerKind{MediaServerPlex}) {
		t.Fatalf("keyKinds = %q, want [plex]", kinds)
	}
	for _, s := range SupportedMediaServerKinds() {
		k := MediaServerKind(s)
		prefix := k.KeyPrefix()
		key := "movie:tmdb:1@" + prefix + ":3:x"
		if i, n := DisambiguationIndex(key); i != len("movie:tmdb:1") || n != len("@"+prefix+":") {
			t.Errorf("kind %q: DisambiguationIndex(%q) = %d, %d", s, key, i, n)
		}
		if got, ok := KindOfVersionKey(prefix + ":3:4"); !ok || got.KeyPrefix() != prefix {
			t.Errorf("kind %q: KindOfVersionKey = %q, %v", s, got, ok)
		}
	}
}

// TestServerVersionIDIsTheLegacyKeyGate: version keys are built when ServerVersionID is set, which
// for Plex is exactly the legacy condition MediaID > 0 (with the same id), so the key a version
// gets is unchanged.
func TestServerVersionIDIsTheLegacyKeyGate(t *testing.T) {
	for _, mid := range []int64{math.MinInt64, -1, 0, 1, 95, math.MaxInt64} {
		v := MediaVersion{MediaID: mid}
		id := v.ServerVersionID()
		if (id != "") != (mid > 0) {
			t.Errorf("MediaID %d: ServerVersionID %q, legacy gate MediaID > 0 = %v", mid, id, mid > 0)
		}
		if mid > 0 && VersionKey(MediaServerPlex, 2, id) != fmt.Sprintf("plex:%d:%d", 2, mid) {
			t.Errorf("MediaID %d: key %q", mid, VersionKey(MediaServerPlex, 2, id))
		}
	}
}
