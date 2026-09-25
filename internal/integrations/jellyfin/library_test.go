package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// moviesFixture serves the movies listing of the verification run with its stack parts.
func moviesFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.standard()
	f.items(map[string]string{moviesLib: "movies.json", showsLib: "episodes.json"}, map[string]string{
		"20000000000000000000000000000fff": "series.json",
	})
	f.parts(map[string]string{"10000000000000000000000000000b02": "parts_kappa.json", "10000000000000000000000000000d01": "parts_gamma.json"})
	f.partsNone("10000000000000000000000000000a01", "10000000000000000000000000000a02", "10000000000000000000000000000b01",
		"10000000000000000000000000000c02", "10000000000000000000000000000e01", "20000000000000000000000000000a01",
		"20000000000000000000000000000a02", "20000000000000000000000000000b01")
	return f
}

func refByID(refs []mediaserver.ItemRef, id string) *mediaserver.ItemRef {
	for i := range refs {
		if refs[i].RatingKey == id {
			return &refs[i]
		}
	}
	return nil
}

// TestListingExpandsVersionsAndParts covers the listing traps of the verification run: two-version
// rows; a stacked alternate whose row has no PartCount (its AdditionalParts are read by its own id);
// a stacked primary (PartCount 2); a local .strm primary (a shortcut, never a version); a source
// merged from another library (left to that library); extras, virtual rows, remote and placeholder
// sources skipped; provider ids normalised.
func TestListingExpandsVersionsAndParts(t *testing.T) {
	f := moviesFixture(t)
	c := f.client()
	refs, err := c.AllItems(context.Background(), moviesLib, models.MediaTypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	// Alpha, Kappa, Lambda, Gamma, Zeta, Remote (no file version) and Omega; the extra and the
	// virtual row are skipped.
	if len(refs) != 7 {
		t.Fatalf("refs = %d, want 7: %+v", len(refs), refs)
	}
	if remote := refByID(refs, "10000000000000000000000000000f03"); remote == nil || remote.MediaCount != 0 || len(remote.Media) != 0 {
		t.Fatalf("remote and placeholder sources are never versions: %+v", remote)
	}
	alpha := refByID(refs, "10000000000000000000000000000a01")
	if alpha == nil || alpha.MediaCount != 2 || alpha.ExternalIDs["tmdb"] != "603" || alpha.ExternalIDs["imdb"] != "tt0133093" || len(alpha.ExternalIDs) != 2 {
		t.Fatalf("alpha = %+v", alpha)
	}
	if alpha.Media[1].VersionID != "10000000000000000000000000000a02" || alpha.Media[1].ID != 0 || alpha.Media[0].Width != 3840 {
		t.Fatalf("alpha versions = %+v", alpha.Media)
	}
	kappa := refByID(refs, "10000000000000000000000000000b01")
	if kappa == nil || len(kappa.Media) != 2 || len(kappa.Media[0].Parts) != 1 || len(kappa.Media[1].Parts) != 2 {
		t.Fatalf("kappa = %+v", kappa)
	}
	if p := kappa.Media[1].Parts[1]; !strings.HasSuffix(p.File, "720p-cd2.mkv") || p.Size != 158360 || p.ItemID != "10000000000000000000000000000b03" {
		t.Fatalf("kappa cd2 = %+v", p)
	}
	lambda := refByID(refs, "10000000000000000000000000000c01")
	if lambda == nil || lambda.MediaCount != 1 || lambda.Media[0].VersionID != "10000000000000000000000000000c02" ||
		len(lambda.Shortcuts) != 1 || !strings.HasSuffix(lambda.Shortcuts[0].File, ".strm") || len(lambda.ExternalIDs) != 0 {
		t.Fatalf("lambda = %+v", lambda)
	}
	gamma := refByID(refs, "10000000000000000000000000000d01")
	if gamma == nil || len(gamma.Media) != 1 || len(gamma.Media[0].Parts) != 2 || len(gamma.ExternalIDs) != 0 {
		t.Fatalf("gamma = %+v", gamma)
	}
	zeta := refByID(refs, "10000000000000000000000000000e01")
	if zeta == nil || zeta.MediaCount != 1 {
		t.Fatalf("zeta = %+v (the Movies 4K source belongs to that library's row)", zeta)
	}
	omega := refByID(refs, "10000000000000000000000000000f05")
	if omega == nil || omega.ExternalIDs["tmdb"] != "999" {
		t.Fatalf("omega = %+v", omega)
	}
	// Parts are read for every alternate and for a stacked primary, never from the row alone.
	var partReads []string
	for _, r := range f.all() {
		if strings.HasSuffix(r.URL.Path, "/AdditionalParts") {
			partReads = append(partReads, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/Videos/"), "/AdditionalParts"))
		}
	}
	for _, want := range []string{"10000000000000000000000000000a02", "10000000000000000000000000000b02", "10000000000000000000000000000c02", "10000000000000000000000000000d01"} {
		found := false
		for _, got := range partReads {
			found = found || got == want
		}
		if !found {
			t.Errorf("AdditionalParts of %s was not read (reads: %v)", want, partReads)
		}
	}
}

// TestItemDetail: the detail of each trap row.
func TestItemDetail(t *testing.T) {
	f := moviesFixture(t)
	c := f.client()
	ctx := context.Background()
	// Rows are served one at a time from the listing fixtures.
	serveRow := func(id string) {
		f.handle("GET /Items", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("Ids") == "20000000000000000000000000000fff" {
				file(t, "series.json")(w, r)
				return
			}
			if q.Get("Ids") != id {
				body(`{"Items": [], "TotalRecordCount": 0}`)(w, r)
				return
			}
			body(rowJSON(t, id))(w, r)
		})
	}

	serveRow("10000000000000000000000000000b01")
	kappa, err := c.Item(ctx, "10000000000000000000000000000b01")
	if err != nil {
		t.Fatal(err)
	}
	if kappa.SectionKey != moviesLib || kappa.LibraryTitle != "Movies" || len(kappa.Versions) != 2 || kappa.KeyID != "10000000000000000000000000000b01" {
		t.Fatalf("kappa = %+v", kappa)
	}
	alt := kappa.Versions[1]
	if alt.SourceID != "10000000000000000000000000000b02" || alt.MediaID != 0 || alt.RatingKey != kappa.RatingKey ||
		len(alt.Parts) != 2 || alt.Parts[1].ItemID != "10000000000000000000000000000b03" || alt.ServerVersionID() != alt.SourceID {
		t.Fatalf("kappa alternate = %+v", alt)
	}
	for _, v := range kappa.Versions {
		for _, p := range v.Parts {
			if p.Exists != nil || p.Accessible != nil {
				t.Fatal("Jellyfin never reports whether a file exists")
			}
		}
	}

	serveRow("10000000000000000000000000000c01")
	lambda, err := c.Item(ctx, "10000000000000000000000000000c01")
	if err != nil {
		t.Fatal(err)
	}
	if len(lambda.Versions) != 1 || len(lambda.Versions[0].ReportOnly) != 1 || !strings.Contains(lambda.Versions[0].ReportOnly[0], ".strm") {
		t.Fatalf("lambda = %+v", lambda.Versions)
	}

	serveRow("10000000000000000000000000000f05")
	omega, err := c.Item(ctx, "10000000000000000000000000000f05")
	if err != nil {
		t.Fatal(err)
	}
	if len(omega.Versions) != 1 || !strings.Contains(strings.Join(omega.Versions[0].ReportOnly, ";"), "disc") {
		t.Fatalf("omega (BluRay) = %+v", omega.Versions)
	}

	serveRow("20000000000000000000000000000a01")
	e3, err := c.Item(ctx, "20000000000000000000000000000a01")
	if err != nil {
		t.Fatal(err)
	}
	if e3.MediaType != models.MediaTypeEpisode || e3.Season != 1 || e3.Episode != 3 || e3.ShowTitle != "Show" || e3.ShowIDs["tvdb"] != "12345" || e3.ShowIDs["imdb"] != "tt7654321" {
		t.Fatalf("episode 3 = %+v", e3)
	}
	for _, v := range e3.Versions {
		if v.EpisodeEnd != 0 {
			t.Fatalf("the hidden multi-episode alternate got EpisodeEnd %d (the row has no IndexNumberEnd)", v.EpisodeEnd)
		}
	}

	serveRow("20000000000000000000000000000b01")
	e34, err := c.Item(ctx, "20000000000000000000000000000b01")
	if err != nil {
		t.Fatal(err)
	}
	if len(e34.Versions) != 1 || e34.Versions[0].EpisodeEnd != 4 {
		t.Fatalf("lone multi-episode file = %+v", e34.Versions)
	}
	// The series ids are fetched once per client.
	n := 0
	for _, r := range f.all() {
		if r.URL.Query().Get("Ids") == "20000000000000000000000000000fff" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("series read %d times, want 1", n)
	}
}

// readFixture returns a testdata file.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// rowJSON returns an /Items answer with the one fixture row whose id is id (the row as the fixture
// has it: its raw JSON object, so no field the client reads is lost in a round trip).
func rowJSON(t *testing.T, id string) string {
	t.Helper()
	for _, name := range []string{"movies.json", "episodes.json"} {
		var page struct {
			Items []json.RawMessage
		}
		if err := json.Unmarshal([]byte(readFixture(t, name)), &page); err != nil {
			t.Fatal(err)
		}
		for _, raw := range page.Items {
			var head struct {
				ID string `json:"Id"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				t.Fatal(err)
			}
			if head.ID == id {
				return `{"Items": [` + string(raw) + `], "TotalRecordCount": 1, "StartIndex": 0}`
			}
		}
	}
	t.Fatalf("no fixture row %s", id)
	return ""
}

// TestKeyIDSurvivesAChangeOfPrimary (research Q11): KeyID is the smallest source id of the row's
// file versions, whichever version Jellyfin makes the primary (the row id changes, S9).
func TestKeyIDSurvivesAChangeOfPrimary(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.partsNone("30000000000000000000000000000001", "30000000000000000000000000000002", "30000000000000000000000000000003")
	row := func(primary string, others ...string) string {
		srcs := []string{}
		for _, id := range append([]string{primary}, others...) {
			srcs = append(srcs, fmt.Sprintf(`{"Protocol": "File", "Id": %q, "Path": "/media/movies/X/X - %s.mkv", "Type": "Default", "Size": 10}`, id, id[len(id)-1:]))
		}
		cnt := ""
		if len(srcs) != 1 {
			cnt = `"MediaSourceCount": ` + strconv.Itoa(len(srcs)) + `,`
		}
		return fmt.Sprintf(`{"Items": [{"Id": %q, "Name": "X", "Type": "Movie", "Path": "/media/movies/X/X - %s.mkv", "LocationType": "FileSystem", %s "MediaSources": [%s]}], "TotalRecordCount": 1}`,
			primary, primary[len(primary)-1:], cnt, strings.Join(srcs, ","))
	}
	f.handle("GET /Items", body(row("30000000000000000000000000000003", "30000000000000000000000000000002", "30000000000000000000000000000001")))
	a, err := f.client().Item(context.Background(), "30000000000000000000000000000003")
	if err != nil {
		t.Fatal(err)
	}
	f.handle("GET /Items", body(row("30000000000000000000000000000002", "30000000000000000000000000000001")))
	b, err := f.client().Item(context.Background(), "30000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyID != "30000000000000000000000000000001" || b.KeyID != a.KeyID {
		t.Fatalf("KeyID %q then %q, want the smallest source id both times", a.KeyID, b.KeyID)
	}
}

// TestProviderIDs: keys lower-cased, only tmdb/imdb/tvdb, empty and "0" dropped, malformed dropped.
func TestProviderIDs(t *testing.T) {
	got := providerIDs(map[string]string{"Tmdb": "603", "IMDB": "TT0133093", "Tvdb": "0", "TvMaze": "1", "tmdbcollection": "5"})
	if len(got) != 2 || got["tmdb"] != "603" || got["imdb"] != "tt0133093" {
		t.Fatalf("got %v", got)
	}
	if got := providerIDs(map[string]string{"Tmdb": "", "Imdb": "nm123", "Tvdb": "x1"}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := providerIDs(nil); got == nil {
		t.Fatal("nil map")
	}
}

// TestIdentity: product, version and id checks.
func TestIdentity(t *testing.T) {
	f := newFixture(t)
	c := f.client()
	ctx := context.Background()
	info := func(product, version, id string) {
		answer := body(fmt.Sprintf(`{"ProductName": %q, "Version": %q, "Id": %q, "ServerName": "srv"}`, product, version, id))
		f.handle("GET /System/Info", answer)
		f.handle("GET /System/Info/Public", answer)
	}
	info("Emby Server", "4.10.0.40", testServerID)
	if _, err := c.Identity(ctx); !errors.Is(err, ErrWrongApp) {
		t.Fatalf("Emby: %v", err)
	}
	info("Jellyfin Server", "12.0.5", testServerID)
	if _, err := c.Identity(ctx); !errors.Is(err, ErrTooOld) {
		t.Fatalf("12.0.5: %v", err)
	}
	info("Jellyfin Server", "10.11.4", testServerID)
	if _, err := c.Identity(ctx); !errors.Is(err, ErrTooOld) {
		t.Fatalf("10.11.4: %v", err)
	}
	info("Jellyfin Server", "12.1.0", strings.ToUpper(testServerID))
	id, err := c.Identity(ctx)
	if err != nil || id.MachineIdentifier != testServerID || id.Version != "12.1.0" || id.FriendlyName != "srv" || UntestedVersion(id.Version) {
		t.Fatalf("12.1.0: %+v %v", id, err)
	}
	info("Jellyfin Server", "12.2.0", testServerID)
	if id, err := c.Identity(ctx); err != nil || !UntestedVersion(id.Version) {
		t.Fatalf("12.2.0: %+v %v (accepted, untested)", id, err)
	}
	info("Jellyfin Server", "12.1.0", "")
	if _, err := c.Identity(ctx); !errors.Is(err, ErrWrongApp) {
		t.Fatalf("no id: %v", err)
	}
}

// TestSections: CollectionType mapping (a library Dupearr does not sync but that may hold video
// files is OtherVideo) and the administrator proof (S14); every call reads the libraries again.
func TestSections(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.client()
	secs, err := c.Sections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	other := map[string]bool{}
	for _, s := range secs {
		types[s.Title] = s.Type
		other[s.Title] = s.OtherVideo
		if s.ScannedAt != 0 || s.ContentChangedAt != 0 {
			t.Fatal("Jellyfin reports no scan times")
		}
	}
	if types["Movies"] != "movie" || types["Shows"] != "show" || types["Home Videos"] != "" || types["Music"] != "" {
		t.Fatalf("types = %v", types)
	}
	if other["Movies"] || other["Shows"] || !other["Home Videos"] || other["Music"] {
		t.Fatalf("other video = %v (mixed content holds video, music does not)", other)
	}
	for ct, want := range map[string]bool{"": true, "homevideos": true, "musicvideos": true, "mixed": true, "photos": true,
		"somethingnew": true, "music": false, "books": false, "boxsets": false, "playlists": false} {
		if got := mayHoldVideo(ct); got != want {
			t.Errorf("CollectionType %q: may hold video = %v, want %v", ct, got, want)
		}
	}
	f.handle("GET /Library/VirtualFolders", body(`[{"Name": "Movies", "Locations": ["/media/movies"], "CollectionType": "movies", "ItemId": "f137a2dd21bbc1b99aa5c0f6bf02a805"}]`))
	if secs, err := c.Sections(context.Background()); err != nil || len(secs) != 1 {
		t.Fatalf("a second read was served from the first answer: %+v %v", secs, err)
	}
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f.handle("GET /Library/VirtualFolders", status(code))
		if _, err := f.client().Sections(context.Background()); !errors.Is(err, ErrForbidden) {
			t.Errorf("HTTP %d: err = %v, want ErrForbidden", code, err)
		}
	}
}
