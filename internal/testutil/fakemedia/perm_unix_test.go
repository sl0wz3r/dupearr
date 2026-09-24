//go:build unix

package fakemedia

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestPlexPartialStackedDelete: PMS cannot delete the second part of a stacked version (permissions)
// after removing the first one — the delete fails with 400, and the lost availability is recorded.
func TestPlexPartialStackedDelete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	folder := "movies/Stacked (1990)"
	sc := Base("stacked").AddMovie(Movie{
		Section: SectionMovies, Title: "Stacked", Year: 1990, TmdbID: 9,
		Versions: []Version{{
			Parts: []Part{
				{File: folder + "/CD1/Stacked (1990) - cd1.avi", Size: MiB(700)},
				{File: folder + "/CD2/Stacked (1990) - cd2.avi", Size: MiB(700)},
			},
			Video: SD("mpeg4", 640, 352), Audio: []Audio{AC3("eng", 2)},
		}},
	})
	e := Start(t, sc)
	locked := filepath.Join(e.MediaRoot, filepath.FromSlash(folder+"/CD2"))
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	rk := e.RatingKey(SectionMovies, "Stacked")
	plexDeleteMedia(t, e, rk, e.MediaIDs(rk)[0], http.StatusBadRequest)
	if e.FileExists(RemoteMediaRoot + "/" + folder + "/CD1/Stacked (1990) - cd1.avi") {
		t.Fatal("first part survived")
	}
	if !e.FileExists(RemoteMediaRoot + "/" + folder + "/CD2/Stacked (1990) - cd2.avi") {
		t.Fatal("locked part was deleted")
	}
	if m := detail(t, e, rk); len(m.Media) != 1 {
		t.Fatalf("a failed delete removed the media entry: %+v", m.Media)
	}
	requireRules(t, e, RuleDeleteLastCopy)
}
