package engine

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestKeysUseServerKindAndKeyID: the group-key fallback, the disambiguation suffix and the version
// keys the engine fills in are built from the item's server kind and stable key id. For Plex
// (kind "" or "plex", no KeyID) they are the legacy strings byte for byte.
func TestKeysUseServerKindAndKeyID(t *testing.T) {
	kinded := func(it models.MediaItem, kind models.MediaServerKind, keyID string) models.MediaItem {
		it.ServerKind, it.KeyID = kind, keyID
		return it
	}
	noKeys := func(vs ...models.MediaVersion) []models.MediaVersion {
		for i := range vs {
			vs[i].Key, vs[i].ServerID = "", 0
		}
		return vs
	}

	t.Run("group key fallback", func(t *testing.T) {
		base := models.MediaItem{MediaType: models.MediaTypeMovie, RatingKey: " 100 "}
		for _, tc := range []struct {
			kind  models.MediaServerKind
			keyID string
			want  string
		}{
			{"", "", "plex:1:100"},
			{models.MediaServerPlex, "", "plex:1:100"},
			{"k", "", "k:1:100"},
			{"k", " x ", "k:1:x"},
		} {
			if got := GroupKey(kinded(base, tc.kind, tc.keyID), 1); got != tc.want {
				t.Errorf("GroupKey(kind %q, keyID %q) = %q, want %q", tc.kind, tc.keyID, got, tc.want)
			}
		}
	})

	t.Run("disambiguation suffix", func(t *testing.T) {
		for _, tc := range []struct {
			kind         models.MediaServerKind
			keyA, keyB   string
			wantA, wantB string
		}{
			{"", "", "", "movie:tmdb:1@plex:1:100", "movie:tmdb:1@plex:1:200"},
			{models.MediaServerPlex, "", "", "movie:tmdb:1@plex:1:100", "movie:tmdb:1@plex:1:200"},
			{"k", "x", "y", "movie:tmdb:1@k:1:x", "movie:tmdb:1@k:1:y"},
		} {
			a := kinded(movieItem("100", 1, tmdb("1"), web1080(1), bd720(2)), tc.kind, tc.keyA)
			b := kinded(movieItem("200", 1, tmdb("1"), web1080(3), bd720(4)), tc.kind, tc.keyB)
			gs := BuildGroups([]models.MediaItem{b, a}, gopts())
			if got, want := groupKeys(gs), []string{tc.wantA, tc.wantB}; !reflect.DeepEqual(got, want) {
				t.Errorf("kind %q: groups = %v, want %v", tc.kind, got, want)
			}
		}
	})

	t.Run("version keys and ItemKeyID", func(t *testing.T) {
		for _, tc := range []struct {
			kind      models.MediaServerKind
			keyID     string
			wantKey   string
			wantItemK string
		}{
			{"", "", "plex:3:1", ""},
			{models.MediaServerPlex, "", "plex:3:1", ""},
			{"k", "", "k:3:1", ""},
			{"k", "stable", "k:3:1", "stable"},
		} {
			it := kinded(movieItem("100", 1, tmdb("1"), noKeys(web1080(1), bd720(2))...), tc.kind, tc.keyID)
			it.ServerID = 3
			gs := BuildGroups([]models.MediaItem{it}, gopts())
			if len(gs) != 1 {
				t.Fatalf("kind %q: groups = %v", tc.kind, groupKeys(gs))
			}
			v := gs[0].Files[0].Version
			if v.Key != tc.wantKey || v.ItemKeyID != tc.wantItemK {
				t.Errorf("kind %q keyID %q: version key %q, ItemKeyID %q; want %q, %q", tc.kind, tc.keyID, v.Key, v.ItemKeyID, tc.wantKey, tc.wantItemK)
			}
			if it.Versions[0].ItemKeyID != "" || it.Versions[0].Key != "" {
				t.Errorf("kind %q: the input item was modified: %+v", tc.kind, it.Versions[0])
			}
			b, err := json.Marshal(gs[0])
			if err != nil {
				t.Fatal(err)
			}
			if has := strings.Contains(string(b), "itemKeyId"); has != (tc.keyID != "") {
				t.Errorf("kind %q keyID %q: group JSON has itemKeyId = %v", tc.kind, tc.keyID, has)
			}
		}
	})
}
