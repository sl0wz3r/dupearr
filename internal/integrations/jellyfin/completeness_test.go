package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// simpleRow is a movie row with one file source (id n).
func simpleRow(n int) string {
	id := fmt.Sprintf("4%031d", n)
	return fmt.Sprintf(`{"Id": %q, "Name": "M%d", "Type": "Movie", "Path": "/media/movies/M%d/M%d.mkv", "LocationType": "FileSystem",
		"MediaSources": [{"Protocol": "File", "Id": %q, "Path": "/media/movies/M%d/M%d.mkv", "Type": "Default", "Size": 10}]}`, id, n, n, n, id, n, n)
}

// pagedFixture serves a listing of total rows in pages, with a hook per page.
func pagedFixture(t *testing.T, total int, page func(start, limit int) (rows []string, reported int)) *fixture {
	f := newFixture(t)
	f.standard()
	f.handle("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("Limit"))
		rows, reported := page(start, limit)
		body(fmt.Sprintf(`{"Items": [%s], "TotalRecordCount": %d, "StartIndex": %d}`, strings.Join(rows, ","), reported, start))(w, r)
	})
	for i := 0; i < total+5; i++ {
		f.handle("GET /Videos/"+fmt.Sprintf("4%031d", i)+"/AdditionalParts", file(t, "parts_none.json"))
	}
	return f
}

func rows(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, simpleRow(i))
	}
	return out
}

// TestListingCompleteness (S10): a listing that cannot be proven complete is ErrIncomplete, never a
// partial result.
func TestListingCompleteness(t *testing.T) {
	ctx := context.Background()
	list := func(f *fixture) error {
		c := f.client()
		c.pageSize = 3
		_, err := c.AllItems(ctx, moviesLib, models.MediaTypeMovie)
		return err
	}
	ok := pagedFixture(t, 7, func(start, limit int) ([]string, int) { return rows(start, min(start+limit, 7)), 7 })
	if err := list(ok); err != nil {
		t.Fatalf("complete listing: %v", err)
	}
	cases := map[string]func(start, limit int) ([]string, int){
		"total changes between pages": func(start, limit int) ([]string, int) {
			if start == 0 {
				return rows(0, 3), 7
			}
			return rows(start, min(start+limit, 8)), 8
		},
		"repeated row": func(start, limit int) ([]string, int) {
			if start == 3 {
				return append(rows(2, 3), rows(4, 6)...), 7
			}
			return rows(start, min(start+limit, 7)), 7
		},
		"short page before the total": func(start, limit int) ([]string, int) {
			if start == 3 {
				return rows(3, 5), 7
			}
			return rows(start, min(start+limit, 7)), 7
		},
		"empty page before the total": func(start, limit int) ([]string, int) {
			if start == 6 {
				return nil, 7
			}
			return rows(start, min(start+limit, 7)), 7
		},
		"more rows than the total": func(start, limit int) ([]string, int) { return rows(start, start+limit), 5 },
		"no total":                 func(start, limit int) ([]string, int) { return rows(start, min(start+limit, 7)), -1 },
	}
	for name, page := range cases {
		t.Run(name, func(t *testing.T) {
			if err := list(pagedFixture(t, 8, page)); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("err = %v, want ErrIncomplete", err)
			}
		})
	}
}

// TestListingRowChecks: a MediaSourceCount that disagrees with the sources, a source id under two
// rows and a failed parts read fail the listing.
func TestListingRowChecks(t *testing.T) {
	ctx := context.Background()
	serve := func(t *testing.T, items string, n int) *fixture {
		f := newFixture(t)
		f.standard()
		f.handle("GET /Items", body(fmt.Sprintf(`{"Items": [%s], "TotalRecordCount": %d}`, items, n)))
		return f
	}
	src := func(id, p string) string {
		return fmt.Sprintf(`{"Protocol": "File", "Id": %q, "Path": %q, "Type": "Default", "Size": 10}`, id, p)
	}
	a, b := "50000000000000000000000000000001", "50000000000000000000000000000002"
	mismatch := fmt.Sprintf(`{"Id": %q, "Type": "Movie", "Path": "/media/movies/A/A.mkv", "LocationType": "FileSystem", "MediaSourceCount": 3, "MediaSources": [%s, %s]}`,
		a, src(a, "/media/movies/A/A.mkv"), src(b, "/media/movies/A/A - 720p.mkv"))
	f := serve(t, mismatch, 1)
	f.partsNone(a, b)
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("MediaSourceCount mismatch: %v", err)
	}
	absent := fmt.Sprintf(`{"Id": %q, "Type": "Movie", "Path": "/media/movies/A/A.mkv", "LocationType": "FileSystem", "MediaSources": [%s, %s]}`,
		a, src(a, "/media/movies/A/A.mkv"), src(b, "/media/movies/A/A - 720p.mkv"))
	f = serve(t, absent, 1)
	f.partsNone(a, b)
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("absent MediaSourceCount (= 1) with two sources: %v", err)
	}
	c := "50000000000000000000000000000003"
	dup := fmt.Sprintf(`{"Id": %q, "Type": "Movie", "Path": "/media/movies/A/A.mkv", "LocationType": "FileSystem", "MediaSources": [%s]},
		{"Id": %q, "Type": "Movie", "Path": "/media/movies/C/C.mkv", "LocationType": "FileSystem", "MediaSourceCount": 2, "MediaSources": [%s, %s]}`,
		a, src(a, "/media/movies/A/A.mkv"), c, src(c, "/media/movies/C/C.mkv"), src(a, "/media/movies/A/A.mkv"))
	f = serve(t, dup, 2)
	f.partsNone(a, c)
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a source under two rows: %v", err)
	}
	two := fmt.Sprintf(`{"Id": %q, "Type": "Movie", "Path": "/media/movies/A/A.mkv", "LocationType": "FileSystem", "MediaSourceCount": 2, "MediaSources": [%s, %s]}`,
		a, src(a, "/media/movies/A/A.mkv"), src(b, "/media/movies/A/A - 720p.mkv"))
	f = serve(t, two, 1)
	f.partsNone(a)
	f.handle("GET /Videos/"+b+"/AdditionalParts", status(http.StatusInternalServerError))
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); err == nil {
		t.Fatal("a failed parts read gave a listing")
	}
	f.handle("GET /Videos/"+b+"/AdditionalParts", body(`{"Items": [{"Id": "50000000000000000000000000000009", "Path": "/media/movies/A/A - 720p-cd2.mkv", "MediaSources": [{"Id": "50000000000000000000000000000009"}]}], "TotalRecordCount": 1}`))
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a part without a size: %v", err)
	}
	// A file version without a size: its size is what the several-servers index compares.
	sizeless := fmt.Sprintf(`{"Id": %q, "Type": "Movie", "Path": "/media/movies/A/A.mkv", "LocationType": "FileSystem", "MediaSourceCount": 2, "MediaSources": [%s,
		{"Protocol": "File", "Id": %q, "Path": "/media/movies/A/A - 720p.mkv", "Type": "Default"}]}`, a, src(a, "/media/movies/A/A.mkv"), b)
	f = serve(t, sizeless, 1)
	f.partsNone(a, b)
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a version without a size: %v", err)
	}
	// A row whose own file lies outside the library's folders: the server rewrites its paths.
	outside := fmt.Sprintf(`{"Id": %q, "Type": "Movie", "Path": "//nas/media/movies/A/A.mkv", "LocationType": "FileSystem", "MediaSources": [%s]}`,
		a, src(a, "//nas/media/movies/A/A.mkv"))
	f = serve(t, outside, 1)
	f.partsNone(a)
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a row outside the library folders: %v", err)
	}
}

// TestMergedRowsOfOneLibrary: MergeVersions of two copies in one library with a primary in another
// library lists both rows, each with the other (and the other library's copy) as Grouping sources
// (live on 12.1). They are one title: one ref led by the smallest row id, each file once, and the
// other library's copy left to its own row. A row's own source under two rows is still incomplete.
func TestMergedRowsOfOneLibrary(t *testing.T) {
	ctx := context.Background()
	a, b, k := "80000000000000000000000000000002", "80000000000000000000000000000001", "80000000000000000000000000000009"
	src := func(id, typ, p string) string {
		return fmt.Sprintf(`{"Protocol": "File", "Id": %q, "Path": %q, "Type": %q, "Size": 10}`, id, p, typ)
	}
	pa, pb, pk := "/media/movies/Zeta (2017)/Zeta (2017).mkv", "/media/movies/Zeta Remux (2017)/Zeta Remux (2017).mkv", "/media/movies4k/Zeta (2017)/Zeta (2017).mkv"
	row := func(id, p string, sources ...string) string {
		return fmt.Sprintf(`{"Id": %q, "Name": "Zeta", "Type": "Movie", "Path": %q, "LocationType": "FileSystem", "MediaSourceCount": %d, "MediaSources": [%s]}`,
			id, p, len(sources), strings.Join(sources, ","))
	}
	rowA := row(a, pa, src(a, "Default", pa), src(k, "Grouping", pk), src(b, "Grouping", pb))
	rowB := row(b, pb, src(b, "Default", pb), src(k, "Grouping", pk), src(a, "Grouping", pa))
	f := newFixture(t)
	f.standard()
	f.handle("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("Ids") {
		case "":
			body(fmt.Sprintf(`{"Items": [%s, %s], "TotalRecordCount": 2}`, rowA, rowB))(w, r)
		case b:
			body(fmt.Sprintf(`{"Items": [%s], "TotalRecordCount": 1}`, rowB))(w, r)
		default:
			body(`{"Items": [], "TotalRecordCount": 0}`)(w, r)
		}
	})
	f.partsNone(a, b, k)
	refs, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].RatingKey != b || refs[0].MediaCount != 2 || len(refs[0].Media) != 2 ||
		refs[0].Media[0].VersionID != b || refs[0].Media[1].VersionID != a {
		t.Fatalf("refs = %+v, want one ref of %s with its own copy first", refs, b)
	}
	it, err := f.client().Item(ctx, b)
	if err != nil || len(it.Versions) != 2 {
		t.Fatalf("the lead row's re-read lists the same copies: %+v %v", it, err)
	}
	// Two rows claiming one file as their own is never a merge.
	clash := row(b, pb, src(b, "Default", pb), src(a, "Default", pa))
	f.handle("GET /Items", body(fmt.Sprintf(`{"Items": [%s, %s], "TotalRecordCount": 2}`, rowA, clash)))
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a row's own source under two rows: %v", err)
	}
}

// TestPathSubstitutionsStopListings (S12): with path substitutions set Jellyfin rewrites every path
// it reports but not its library folders, so listings and re-reads refuse to work on them instead
// of reporting every copy as another library's.
func TestPathSubstitutionsStopListings(t *testing.T) {
	ctx := context.Background()
	f := moviesFixture(t)
	f.handle("GET /System/Configuration", body(`{"PathSubstitutions": [{"From": "/media", "To": "//nas/media"}], "LibraryMonitorDelay": 60}`))
	c := f.client()
	if _, err := c.AllItems(ctx, moviesLib, models.MediaTypeMovie); !errors.Is(err, ErrPathSubstitutions) {
		t.Fatalf("listing: err = %v", err)
	}
	if _, err := c.Item(ctx, "10000000000000000000000000000a01"); !errors.Is(err, ErrPathSubstitutions) {
		t.Fatalf("re-read: err = %v", err)
	}
	f.handle("GET /System/Configuration", status(http.StatusInternalServerError))
	if _, err := f.client().AllItems(ctx, moviesLib, models.MediaTypeMovie); err == nil {
		t.Fatal("an unreadable configuration gave a listing")
	}
}

// TestItemPartsProblemsAreReportOnly: in the detail, a failed parts read or a part without a size
// makes that version report-only (the group is shown, never acted on) instead of failing the item;
// a row Jellyfin no longer lists is ErrNotFound, also as mediaserver.ErrNotFound.
func TestItemPartsProblemsAreReportOnly(t *testing.T) {
	ctx := context.Background()
	a, b := "60000000000000000000000000000001", "60000000000000000000000000000002"
	f := newFixture(t)
	f.standard()
	f.handle("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Ids") != a {
			body(`{"Items": [], "TotalRecordCount": 0}`)(w, r)
			return
		}
		body(fmt.Sprintf(`{"Items": [{"Id": %q, "Type": "Movie", "Name": "A", "Path": "/media/movies/A/A.mkv", "LocationType": "FileSystem", "MediaSourceCount": 2,
			"MediaSources": [{"Protocol": "File", "Id": %q, "Path": "/media/movies/A/A.mkv", "Type": "Default", "Size": 10},
			                 {"Protocol": "File", "Id": %q, "Path": "/media/movies/A/A - 720p.mkv", "Type": "Default", "Size": 20}]}], "TotalRecordCount": 1}`, a, a, b))(w, r)
	})
	f.handle("GET /Videos/"+a+"/AdditionalParts", status(http.StatusBadGateway))
	f.handle("GET /Videos/"+b+"/AdditionalParts", body(`{"Items": [{"Id": "60000000000000000000000000000009", "Path": "/media/movies/A/A - 720p-cd2.mkv", "MediaSources": [{"Id": "60000000000000000000000000000009", "Path": "/media/movies/A/A - 720p-cd2.mkv"}]}], "TotalRecordCount": 1}`))
	it, err := f.client().Item(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(it.Versions) != 2 {
		t.Fatalf("versions = %+v", it.Versions)
	}
	for _, v := range it.Versions {
		if len(v.ReportOnly) == 0 || !strings.Contains(v.ReportOnly[0], "stack parts") {
			t.Errorf("version %s: ReportOnly = %v", v.SourceID, v.ReportOnly)
		}
	}
	_, err = f.client().Item(ctx, b)
	if !errors.Is(err, ErrNotFound) || !errors.Is(err, mediaserver.ErrNotFound) {
		t.Fatalf("a hidden alternate's id: err = %v, want ErrNotFound", err)
	}
}

// TestSectionOf resolves a row's library from the library folders (longest match; a tie gives no
// key).
func TestSectionOf(t *testing.T) {
	folders := []virtualFolderDTO{
		{Name: "Movies", ItemID: "70000000000000000000000000000001", Locations: []string{"/media/movies"}},
		{Name: "4K", ItemID: "70000000000000000000000000000002", Locations: []string{"/media/movies/4k"}},
		{Name: "A", ItemID: "70000000000000000000000000000003", Locations: []string{"/media/same"}},
		{Name: "B", ItemID: "70000000000000000000000000000004", Locations: []string{"/media/same"}},
	}
	if key, title, _ := sectionOf("/media/movies/4k/X/X.mkv", folders); key != "70000000000000000000000000000002" || title != "4K" {
		t.Fatalf("nested: %s %s", key, title)
	}
	if key, _, _ := sectionOf("/media/movies/X/X.mkv", folders); key != "70000000000000000000000000000001" {
		t.Fatalf("outer: %s", key)
	}
	if key, _, locs := sectionOf("/media/same/X.mkv", folders); key != "" || len(locs) != 2 {
		t.Fatalf("tie: %q %v", key, locs)
	}
	if _, _, locs := sectionOf("/elsewhere/X.mkv", folders); locs != nil {
		t.Fatalf("outside: %v", locs)
	}
	if _, _, locs := sectionOf("/media/moviesX/X.mkv", folders); locs != nil {
		t.Fatalf("a folder name prefix is not inside: %v", locs)
	}
}
