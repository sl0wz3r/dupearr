package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestIdentity(t *testing.T) {
	t.Run("from root", func(t *testing.T) {
		f := newFakeServer(t, routes(map[string]string{"/": rootJSON}))
		id, err := newTestClient(f, "").Identity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := &Identity{MachineIdentifier: "0123456789abcdef0123456789abcdef", Version: "1.43.4.10903-abc", FriendlyName: "Tower"}
		if !reflect.DeepEqual(id, want) {
			t.Errorf("Identity = %+v, want %+v", id, want)
		}
		if f.countPath("/identity") != 0 {
			t.Error("/identity should not be needed")
		}
	})
	t.Run("fallback to /identity", func(t *testing.T) {
		f := newFakeServer(t, routes(map[string]string{
			"/":         `{"MediaContainer":{"size":0,"friendlyName":"Tower"}}`,
			"/identity": `{"MediaContainer":{"size":1,"claimed":true,"machineIdentifier":"abc","version":"1.40.2.8395-c67dce28e"}}`,
		}))
		id, err := newTestClient(f, "").Identity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if id.MachineIdentifier != "abc" || id.Version != "1.40.2.8395-c67dce28e" || id.FriendlyName != "Tower" {
			t.Errorf("Identity = %+v", id)
		}
	})
	t.Run("not a PMS", func(t *testing.T) {
		f := newFakeServer(t, routes(map[string]string{"/": `{"MediaContainer":{"size":0}}`}))
		if _, err := newTestClient(f, "").Identity(context.Background()); err == nil {
			t.Error("expected an error without machineIdentifier")
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
		if _, err := newTestClient(f, "").Identity(context.Background()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("err = %v, want ErrUnauthorized", err)
		}
	})
}

// Official /library/sections example (Windows paths), plus a music section and numeric keys.
const sectionsJSON = `{ "MediaContainer": { "size": "3", "allowSync": true, "title1": "Plex Library",
  "Directory": [
    { "key": "1", "type": "movie", "title": "Movies", "agent": "tv.plex.agents.movie", "uuid": "70cb5089-b165-429b-809a-9e0a31493abf",
      "scanner": "Plex Movie", "language": "en-US", "refreshing": false, "updatedAt": 1689270983,
      "Location": [ { "id": 1, "path": "O:\\fatboy\\Media\\Ripped\\Movies" }, { "id": "3", "path": "/data/media/movies" } ] },
    { "key": 2, "type": "show", "title": "TV Shows", "agent": "tv.plex.agents.series",
      "Location": { "id": 2, "path": "O:\\fatboy\\Media\\Ripped\\Shows" } },
    { "key": "5", "type": "artist", "title": "Music", "Location": [] },
    { "key": "", "type": "movie", "title": "broken" } ] } }`

func TestSections(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/library/sections": sectionsJSON}))
	secs, err := newTestClient(f, "").Sections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Section{
		{Key: "1", Type: "movie", Title: "Movies", UUID: "70cb5089-b165-429b-809a-9e0a31493abf",
			Locations: []string{`O:\fatboy\Media\Ripped\Movies`, "/data/media/movies"}},
		{Key: "2", Type: "show", Title: "TV Shows", Locations: []string{`O:\fatboy\Media\Ripped\Shows`}},
		{Key: "5", Type: "artist", Title: "Music", Locations: []string{}},
	}
	if !reflect.DeepEqual(secs, want) {
		t.Errorf("Sections =\n%+v\nwant\n%+v", secs, want)
	}
	if f.requests()[0].Path != "/library/sections" {
		t.Errorf("path = %q", f.requests()[0].Path)
	}
}

// The library scan times and the refreshing flag (docs/DECISIONS.md D11): numbers or numeric
// strings; absent or unparseable values are 0 ("not reported"), never a time.
func TestSectionsScanTimes(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/library/sections": `{ "MediaContainer": { "size": 3, "Directory": [
    { "key": "1", "type": "movie", "title": "Movies", "refreshing": true, "scannedAt": 1758700000, "contentChangedAt": "1758690000",
      "Location": [ { "id": 1, "path": "/data/media/movies" } ] },
    { "key": "2", "type": "show", "title": "TV", "refreshing": "0", "scannedAt": "junk", "Location": [] },
    { "key": "3", "type": "movie", "title": "Old", "scannedAt": -5, "contentChangedAt": 0, "Location": [] } ] } }`}))
	secs, err := newTestClient(f, "").Sections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Section{
		{Key: "1", Type: "movie", Title: "Movies", Locations: []string{"/data/media/movies"}, Refreshing: true, ScannedAt: 1758700000, ContentChangedAt: 1758690000},
		{Key: "2", Type: "show", Title: "TV", Locations: []string{}},
		{Key: "3", Type: "movie", Title: "Old", Locations: []string{}},
	}
	if !reflect.DeepEqual(secs, want) {
		t.Errorf("Sections =\n%+v\nwant\n%+v", secs, want)
	}
}

func TestSectionsEmptyIsNotNil(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/library/sections": `{"MediaContainer":{"size":0}}`}))
	secs, err := newTestClient(f, "").Sections(context.Background())
	if err != nil || secs == nil || len(secs) != 0 {
		t.Errorf("Sections = %#v, %v; want empty non-nil", secs, err)
	}
}

// Movie listing modelled on the research examples: strings-as-numbers, an optimized version, a
// Windows "Plex Versions" path and a legacy-agent movie.
const movieListingJSON = `{ "MediaContainer": {
  "librarySectionID": 26, "librarySectionTitle": "Movies", "size": "3", "totalSize": "3", "offset": 0,
  "Metadata": [
    { "ratingKey": "1049", "key": "/library/metadata/1049", "type": "movie", "title": "Zoolander", "year": "2001",
      "guid": "plex://movie/5d776b59ad5437001f79c6f8", "addedAt": "1408525217",
      "Guid": [ { "id": "imdb://tt0196229" }, { "id": "tmdb://9398" } ],
      "Media": [
        { "id": 827, "duration": "5129000", "videoResolution": "1080", "width": 1920, "height": 1080,
          "Part": [ { "id": "827", "file": "/movies/Zoolander (2001)/Zoolander (2001) Bluray-1080p.mkv", "size": "9123456789" } ] },
        { "id": "9001", "videoResolution": "4k", "width": "3840", "height": "2160",
          "Part": [ { "id": 9001, "file": "/movies/Zoolander (2001)/Zoolander (2001) Remux-2160p.mkv", "size": 61234567890 } ] },
        { "id": 9050, "proxyType": "42", "target": "Optimized for Mobile", "title": "Optimized for Mobile",
          "Part": [ { "id": 9050, "file": "/movies/Zoolander (2001)/Plex Versions/Optimized for Mobile/Zoolander (2001).mp4" } ] } ] },
    { "ratingKey": "2001", "type": "movie", "title": "Solo", "year": 2018,
      "guid": "com.plexapp.agents.imdb://tt3778644?lang=en",
      "Media": [
        { "id": 3001, "width": 1920, "height": 800, "Part": [ { "id": 3001, "file": "/movies/Solo (2018)/Solo.mkv", "size": 100 } ] },
        { "id": 3002, "width": 1280, "height": 720,
          "Part": [ { "id": 3002, "file": "D:\\Movies\\plex versions\\Solo\\Solo.mp4", "size": 50 } ] } ] },
    { "ratingKey": "2002", "type": "movie", "title": "Single", "year": 1999, "guid": "com.plexapp.agents.themoviedb://603?lang=en",
      "Media": { "id": 4001, "Part": { "id": 4001, "file": "/movies/Single (1999)/Single.mkv", "size": 1 } } }
  ] } }`

func TestAllItemsMovies(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/library/sections/1/all": movieListingJSON}))
	items, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	req := f.requests()[0]
	if req.Query.Get("type") != "1" || req.Query.Get("includeGuids") != "1" {
		t.Errorf("query = %v, want type=1&includeGuids=1", req.Query)
	}
	if req.Query.Get("X-Plex-Container-Start") != "0" || req.Query.Get("X-Plex-Container-Size") != "100" ||
		req.Header.Get("X-Plex-Container-Start") != "0" || req.Header.Get("X-Plex-Container-Size") != "100" {
		t.Errorf("pagination params: query %v headers start=%q size=%q", req.Query,
			req.Header.Get("X-Plex-Container-Start"), req.Header.Get("X-Plex-Container-Size"))
	}
	if req.Query.Has("duplicate") {
		t.Error("listing must not depend on the duplicate=1 filter")
	}

	z := items[0]
	wantZ := ItemRef{
		RatingKey: "1049", MediaType: models.MediaTypeMovie, Title: "Zoolander", Year: 2001,
		GUID:        "plex://movie/5d776b59ad5437001f79c6f8",
		ExternalIDs: map[string]string{"imdb": "tt0196229", "tmdb": "9398", "plex": "plex://movie/5d776b59ad5437001f79c6f8"},
		MediaCount:  2,
		Media: []MediaRef{
			{ID: 827, Width: 1920, Height: 1080, DurationMs: 5129000,
				Parts: []PartRef{{ID: 827, File: "/movies/Zoolander (2001)/Zoolander (2001) Bluray-1080p.mkv", Size: 9123456789}}},
			{ID: 9001, Width: 3840, Height: 2160,
				Parts: []PartRef{{ID: 9001, File: "/movies/Zoolander (2001)/Zoolander (2001) Remux-2160p.mkv", Size: 61234567890}}},
			{ID: 9050, Optimized: true,
				Parts: []PartRef{{ID: 9050, File: "/movies/Zoolander (2001)/Plex Versions/Optimized for Mobile/Zoolander (2001).mp4"}}},
		},
		AddedAt: time.Unix(1408525217, 0).UTC(),
	}
	if !reflect.DeepEqual(z, wantZ) {
		t.Errorf("Zoolander =\n%+v\nwant\n%+v", z, wantZ)
	}

	solo := items[1]
	if solo.MediaCount != 1 || !solo.Media[1].Optimized {
		t.Errorf("Solo: Windows lower-case 'plex versions' path must be optimized: %+v", solo)
	}
	if !reflect.DeepEqual(solo.ExternalIDs, map[string]string{"imdb": "tt3778644"}) {
		t.Errorf("Solo ids = %v", solo.ExternalIDs)
	}
	single := items[2]
	if single.MediaCount != 1 || len(single.Media) != 1 || single.Media[0].Parts[0].File != "/movies/Single (1999)/Single.mkv" {
		t.Errorf("single-object Media/Part must decode as one-element lists: %+v", single)
	}
	if !reflect.DeepEqual(single.ExternalIDs, map[string]string{"tmdb": "603"}) {
		t.Errorf("legacy themoviedb ids = %v", single.ExternalIDs)
	}
}

func TestDuplicateItemsFiltersOnNonOptimizedCount(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/library/sections/1/all": movieListingJSON}))
	dups, err := newTestClient(f, "").DuplicateItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	if len(dups) != 1 || dups[0].RatingKey != "1049" {
		t.Fatalf("DuplicateItems = %+v, want only 1049 (Solo's 2nd media is optimized)", dups)
	}
	if f.requests()[0].Query.Has("duplicate") {
		t.Error("DuplicateItems must not use duplicate=1")
	}
}

func TestDuplicateItemsEmptyIsNotNil(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/library/sections/1/all": `{"MediaContainer":{"size":0}}`}))
	dups, err := newTestClient(f, "").DuplicateItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil || dups == nil || len(dups) != 0 {
		t.Errorf("DuplicateItems = %#v, %v", dups, err)
	}
}

const episodeListingJSON = `{ "MediaContainer": { "size": 2, "totalSize": 2, "librarySectionID": "4",
  "Metadata": [
    { "ratingKey": "481485", "type": "episode", "title": "A Grave Mistake", "grandparentTitle": "Hi Hi Puffy AmiYumi",
      "grandparentRatingKey": "481398", "index": "6", "parentIndex": "3", "year": 2006, "addedAt": 1711557838,
      "guid": "plex://episode/5d9c0cd2ffd9ef001e9bc730",
      "Guid": [ { "id": "tmdb://4083174" }, { "id": "tvdb://5664724" } ],
      "Media": [ { "id": 956026, "width": 1920, "height": 1080, "duration": 1353888,
        "Part": [ { "id": 1405223, "file": "/tv/Hi Hi Puffy AmiYumi (2004)/Season 03/Hi Hi Puffy AmiYumi (2004) - S03E04-E06.mkv", "size": 960925199 } ] } ] },
    { "ratingKey": "900", "type": "episode", "title": "Legacy", "grandparentTitle": "Old Show", "index": 2, "parentIndex": 1,
      "guid": "com.plexapp.agents.thetvdb://75159/1/2?lang=en",
      "Media": [ { "id": 1 , "Part": [ { "id": 1, "file": "/tv/a.mkv" } ] }, { "id": 2, "Part": [ { "id": 2, "file": "/tv/b.mkv" } ] } ] }
  ] } }`

func TestAllItemsEpisodes(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/library/sections/4/all": episodeListingJSON}))
	items, err := newTestClient(f, "").AllItems(context.Background(), "4", models.MediaTypeEpisode)
	if err != nil {
		t.Fatal(err)
	}
	if f.requests()[0].Query.Get("type") != "4" {
		t.Errorf("type = %q, want 4", f.requests()[0].Query.Get("type"))
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	e := items[0]
	if e.ShowTitle != "Hi Hi Puffy AmiYumi" || e.Season != 3 || e.Episode != 6 || e.MediaCount != 1 {
		t.Errorf("episode = %+v", e)
	}
	wantIDs := map[string]string{"tmdb": "4083174", "tvdb": "5664724", "plex": "plex://episode/5d9c0cd2ffd9ef001e9bc730"}
	if !reflect.DeepEqual(e.ExternalIDs, wantIDs) {
		t.Errorf("episode ids = %v, want %v", e.ExternalIDs, wantIDs)
	}
	legacy := items[1]
	if len(legacy.ExternalIDs) != 0 {
		t.Errorf("a legacy thetvdb://show/s/e guid is the SHOW id and must not become an episode id: %v", legacy.ExternalIDs)
	}
	if legacy.ExternalIDs == nil {
		t.Error("ExternalIDs must never be nil")
	}
	if legacy.MediaCount != 2 {
		t.Errorf("legacy MediaCount = %d", legacy.MediaCount)
	}
}

func TestAllItemsSkipsRowsOfOtherTypesAndWithoutRatingKey(t *testing.T) {
	body := `{"MediaContainer":{"size":3,"Metadata":[
	  {"ratingKey":"1","type":"movie","title":"ok"},
	  {"ratingKey":"2","type":"collection","title":"coll"},
	  {"type":"movie","title":"no key"}]}}`
	f := newFakeServer(t, routes(map[string]string{"/library/sections/1/all": body}))
	items, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RatingKey != "1" {
		t.Errorf("items = %+v", items)
	}
}

func TestAllItemsArgumentValidation(t *testing.T) {
	f := newFakeServer(t, nil)
	c := newTestClient(f, "")
	for _, key := range []string{"", "1,2", "1/../2", "a b", "%31"} {
		if _, err := c.AllItems(context.Background(), key, models.MediaTypeMovie); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("section %q: err = %v, want ErrInvalidArgument", key, err)
		}
	}
	if _, err := c.AllItems(context.Background(), "1", models.MediaType("show")); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("media type show: err = %v, want ErrInvalidArgument", err)
	}
	if f.count() != 0 {
		t.Errorf("invalid arguments must not send requests (sent %d)", f.count())
	}
}

// pagedServer serves total movies, returning at most maxPage rows per page whatever the client
// asks for (PMS "might include a different number of items than what was requested").
func pagedServer(t *testing.T, total, maxPage int, mode string) *fakeServer {
	return newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size"))
		if size > maxPage {
			size = maxPage
		}
		var rows []string
		for i := start; i < start+size && i < total; i++ {
			rows = append(rows, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"m%d","Media":[{"id":%d}]}`, i+1, i, i+1))
		}
		mc := fmt.Sprintf(`"size":%d,"offset":%d`, len(rows), start)
		switch mode {
		case "totalSize":
			mc += fmt.Sprintf(`,"totalSize":"%d"`, total)
		case "header":
			w.Header().Set("X-Plex-Container-Total-Size", strconv.Itoa(total))
		}
		writeJSON(w, 200, `{"MediaContainer":{`+mc+`,"Metadata":[`+strings.Join(rows, ",")+`]}}`)
	})
}

func TestAllItemsPaginationAdvancesByReturnedSize(t *testing.T) {
	for _, mode := range []string{"totalSize", "header", "none"} {
		t.Run(mode, func(t *testing.T) {
			const total = 250
			f := pagedServer(t, total, 70, mode)
			items, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != total {
				t.Fatalf("items = %d, want %d", len(items), total)
			}
			for i, it := range items {
				if it.RatingKey != strconv.Itoa(i+1) {
					t.Fatalf("item %d = %s: rows skipped or duplicated", i, it.RatingKey)
				}
			}
			var starts []string
			for _, r := range f.requests() {
				starts = append(starts, r.Query.Get("X-Plex-Container-Start"))
			}
			want := []string{"0", "70", "140", "210"}
			if mode == "none" {
				want = append(want, "250") // stops only on the empty page
			}
			if !reflect.DeepEqual(starts, want) {
				t.Errorf("starts = %v, want %v", starts, want)
			}
		})
	}
}

func TestAllItemsExactPageMultiple(t *testing.T) {
	f := pagedServer(t, 200, 100, "totalSize")
	items, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil || len(items) != 200 {
		t.Fatalf("items = %d, %v", len(items), err)
	}
	if f.count() != 2 {
		t.Errorf("requests = %d, want 2 (stop at totalSize)", f.count())
	}
}

func TestAllItemsServerIgnoringOffset(t *testing.T) {
	// Returns the same full list for every offset and no totalSize: stop once nothing new comes.
	body := `{"MediaContainer":{"size":2,"Metadata":[{"ratingKey":"1","type":"movie"},{"ratingKey":"2","type":"movie"}]}}`
	f := newFakeServer(t, routes(map[string]string{"/library/sections/1/all": body}))
	items, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %+v, %v", items, err)
	}
	if f.count() != 2 {
		t.Errorf("requests = %d, want 2", f.count())
	}
}

func TestAllItemsNonAdvancingPaginationFails(t *testing.T) {
	// Claims 10 items but always returns the same 2: must fail rather than loop or under-report.
	body := `{"MediaContainer":{"size":2,"totalSize":10,"Metadata":[{"ratingKey":"1","type":"movie"},{"ratingKey":"2","type":"movie"}]}}`
	f := newFakeServer(t, routes(map[string]string{"/library/sections/1/all": body}))
	_, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err == nil || !strings.Contains(err.Error(), "not advancing") {
		t.Fatalf("err = %v, want pagination error", err)
	}
}

func TestAllItemsShrinkingTotal(t *testing.T) {
	// totalSize smaller than what was already seen (library shrank): page until an empty page.
	calls := 0
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			writeJSON(w, 200, `{"MediaContainer":{"size":2,"totalSize":5,"Metadata":[{"ratingKey":"1","type":"movie"},{"ratingKey":"2","type":"movie"}]}}`)
		case 2:
			writeJSON(w, 200, `{"MediaContainer":{"size":1,"totalSize":1,"Metadata":[{"ratingKey":"3","type":"movie"}]}}`)
		default:
			writeJSON(w, 200, `{"MediaContainer":{"size":0,"totalSize":1}}`)
		}
	})
	items, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil || len(items) != 3 {
		t.Fatalf("items = %d, %v", len(items), err)
	}
}

func TestAllItemsDeduplicatesRatingKeys(t *testing.T) {
	calls := 0
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeJSON(w, 200, `{"MediaContainer":{"size":2,"totalSize":3,"Metadata":[{"ratingKey":"1","type":"movie"},{"ratingKey":"2","type":"movie"}]}}`)
			return
		}
		// An item shifted between pages: "2" is listed again.
		writeJSON(w, 200, `{"MediaContainer":{"size":2,"totalSize":3,"Metadata":[{"ratingKey":"2","type":"movie"},{"ratingKey":"3","type":"movie"}]}}`)
	})
	items, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, it := range items {
		keys = append(keys, it.RatingKey)
	}
	if !reflect.DeepEqual(keys, []string{"1", "2", "3"}) {
		t.Errorf("keys = %v", keys)
	}
}

func TestAllItemsContextCancelledBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		writeJSON(w, 200, `{"MediaContainer":{"size":1,"totalSize":5,"Metadata":[{"ratingKey":"1","type":"movie"}]}}`)
	})
	_, err := newTestClient(f, "").AllItems(ctx, "1", models.MediaTypeMovie)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestAllItemsPageSizeOverride(t *testing.T) {
	f := pagedServer(t, 5, 100, "totalSize")
	c := newTestClient(f, "")
	c.pageSize = 2
	items, err := c.AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err != nil || len(items) != 5 || f.count() != 3 {
		t.Errorf("items = %d, requests = %d, err = %v", len(items), f.count(), err)
	}
}

func TestIsOptimized(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{"proxyType 42", `{"id":1,"proxyType":42}`, true},
		{"proxyType string", `{"id":1,"proxyType":"42"}`, true},
		{"target", `{"id":1,"target":"Custom: Android"}`, true},
		{"plex versions path", `{"id":1,"Part":[{"file":"/m/Plex Versions/Optimized for TV/x.mp4"}]}`, true},
		{"library-level plex versions", `{"id":1,"Part":[{"file":"/movies/Plex Versions/x.mp4"}]}`, true},
		{"windows path", `{"id":1,"Part":[{"file":"D:\\M\\PLEX VERSIONS\\x.mp4"}]}`, true},
		{"second part", `{"id":1,"Part":[{"file":"/m/a.mkv"},{"file":"/m/plex versions/b.mkv"}]}`, true},
		{"normal", `{"id":1,"Part":[{"file":"/m/Movie (2001)/Movie.mkv"}]}`, false},
		{"similar name", `{"id":1,"Part":[{"file":"/m/Plex VersionsX/Movie.mkv"}]}`, false},
		{"other proxy", `{"id":1,"proxyType":"41"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var md mediaDTO
			if err := json.Unmarshal([]byte(tt.json), &md); err != nil {
				t.Fatal(err)
			}
			if got := isOptimized(&md); got != tt.want {
				t.Errorf("isOptimized = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseGUID(t *testing.T) {
	tests := []struct {
		in      string
		key, id string
		hasPath bool
		ok      bool
	}{
		{"imdb://tt0088763", "imdb", "tt0088763", false, true},
		{"tmdb://105", "tmdb", "105", false, true},
		{"tvdb://5664724", "tvdb", "5664724", false, true},
		{"com.plexapp.agents.imdb://tt0088763?lang=en", "imdb", "tt0088763", false, true},
		{"com.plexapp.agents.IMDB://TT0088763", "imdb", "tt0088763", false, true},
		{"com.plexapp.agents.themoviedb://105?lang=en", "tmdb", "105", false, true},
		{"com.plexapp.agents.thetvdb://75159/3/6?lang=en", "tvdb", "75159", true, true},
		{"com.plexapp.agents.thetvdb://75159?lang=en", "tvdb", "75159", false, true},
		{"com.plexapp.agents.thetvdbdvdorder://75159/1/1", "tvdb", "75159", true, true},
		{"com.plexapp.agents.xbmcnfo://tt0088763?lang=xn", "imdb", "tt0088763", false, true},
		{"plex://movie/5d776b59ad5437001f79c6f8", "", "", false, false},
		{"local://1234", "", "", false, false},
		{"com.plexapp.agents.none://1234", "", "", false, false},
		{"com.plexapp.agents.hama://anidb-123", "", "", false, false},
		{"tmdb://abc", "", "", false, false},
		{"imdb://12345", "", "", false, false},
		{"tmdb://", "", "", false, false},
		{"garbage", "", "", false, false},
		{"", "", "", false, false},
	}
	for _, tt := range tests {
		key, id, hasPath, ok := parseGUID(tt.in)
		if key != tt.key || id != tt.id || hasPath != tt.hasPath || ok != tt.ok {
			t.Errorf("parseGUID(%q) = (%q,%q,%v,%v), want (%q,%q,%v,%v)", tt.in, key, id, hasPath, ok, tt.key, tt.id, tt.hasPath, tt.ok)
		}
	}
}

func TestExternalIDs(t *testing.T) {
	guids := list[guidDTO]{{ID: "imdb://tt0196229"}, {ID: "tmdb://9398"}, {ID: "tmdb://1111"}, {ID: "bogus"}}
	got := externalIDs("plex://movie/abc", guids)
	want := map[string]string{"imdb": "tt0196229", "tmdb": "9398", "plex": "plex://movie/abc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("externalIDs = %v, want %v (first Guid wins)", got, want)
	}
	// Guid[] is authoritative over a legacy primary guid.
	got = externalIDs("com.plexapp.agents.imdb://tt9999999", list[guidDTO]{{ID: "imdb://tt0000001"}})
	if got["imdb"] != "tt0000001" || len(got) != 1 {
		t.Errorf("externalIDs legacy = %v", got)
	}
	if got := externalIDs("", nil); got == nil || len(got) != 0 {
		t.Errorf("externalIDs empty = %#v", got)
	}
}

func TestUnixTime(t *testing.T) {
	if !unixTime(0).IsZero() || !unixTime(-5).IsZero() {
		t.Error("non-positive epochs must be zero time")
	}
	if got := unixTime(1408525217); !got.Equal(time.Unix(1408525217, 0)) || got.Location() != time.UTC {
		t.Errorf("seconds = %v", got)
	}
	if got := unixTime(1408525217123); !got.Equal(time.UnixMilli(1408525217123)) {
		t.Errorf("milliseconds = %v", got)
	}
}
