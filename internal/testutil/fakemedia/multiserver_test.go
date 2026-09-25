package fakemedia

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Several Plex servers (Options.ShareMedia, PlexServer.MediaRoot, section scan times and the
// last-copy assertions).

const (
	msID    = "5ec0d5ec0d5ec0d5ec0d5ec0d5ec0d5ec0d5ec0d"
	msToken = "fAkEpLeXtOkEn0000002"
)

// twoMovies: one library over movies/ and movies4k/ with a 4K and a 1080p copy of one movie.
func twoMovies() *Scenario {
	s := Base("two-movies")
	s.Libraries = []Library{{Key: SectionMovies, Title: "Movies", Type: LibraryMovie, Dirs: []string{DirMovies, DirMovies4K}}}
	s.Instances = nil
	s.AddMovie(Movie{Section: SectionMovies, Title: "Heat", Year: 1995, TmdbID: 949, Versions: []Version{
		{Parts: []Part{{File: "movies4k/Heat (1995)/Heat 2160p.mkv", Size: GiB(50)}}, Video: HDR10UHD()},
		{Parts: []Part{{File: "movies/Heat (1995)/Heat 1080p.mkv", Size: GiB(10)}}, Video: FHD("h264")},
	}})
	return s
}

func sectionsOf(t *testing.T, e *Env) []map[string]any {
	t.Helper()
	mc := plexRaw(t, e, "/library/sections")
	var out []map[string]any
	for _, d := range mc["Directory"].([]any) {
		out = append(out, d.(map[string]any))
	}
	return out
}

func TestShareMedia(t *testing.T) {
	a := Start(t, twoMovies())
	b := StartWithOptions(t, Options{Scenario: SharedServer(twoMovies(), "Plex B", msID, msToken, DirMovies), ShareMedia: a})
	if b.Dir != a.Dir || b.Tautulli != nil || len(b.Instances) != 0 {
		t.Fatalf("shared env %+v", b)
	}
	// B lists only the 1080p copy; its identity and token are its own.
	m := detail(t, b, b.RatingKey("", "Heat"))
	if len(m.Media) != 1 || !strings.Contains(m.Media[0].Parts[0].File, "Heat 1080p") {
		t.Fatalf("B's item %+v", m)
	}
	if mc := plexMC(t, b, "/identity"); mc.MachineIdentifier != msID {
		t.Fatalf("identity %q", mc.MachineIdentifier)
	}
	// A deletes the 1080p: B keeps listing it; its file check tells the truth until B's refresh.
	rk := a.RatingKey("", "Heat")
	id := a.MediaIDForFile(rk, "Heat 1080p")
	if r := plexDo(t, a, http.MethodDelete, "/library/metadata/"+rk+"/media/"+strconv.FormatInt(id, 10)); r.Status != http.StatusOK {
		t.Fatalf("delete: %s", r)
	}
	bm := detail(t, b, b.RatingKey("", "Heat"))
	if len(bm.Media) != 1 || bm.Media[0].Parts[0].Exists == nil || *bm.Media[0].Parts[0].Exists {
		t.Fatalf("B after A's delete: %+v", bm.Media)
	}
	if got := b.ItemsWithoutFile(); len(got) != 1 || !strings.Contains(got[0], "Heat") {
		t.Fatalf("items without file %v", got)
	}
	if got := a.ItemsWithoutFile(); len(got) != 0 {
		t.Fatalf("A keeps the 4K copy: %v", got)
	}
	if lost := TitlesWithoutFile(a, b); len(lost) != 0 {
		t.Fatalf("the title still has a file: %v", lost)
	}
	rec := &recordingTB{t: t}
	b.AssertEveryItemHasAFile(rec)
	if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], RuleDeleteOtherServerLastCopy) {
		t.Fatalf("assertion %v", rec.errors)
	}
	// Closing B never removes the shared tree.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.MediaRoot); err != nil {
		t.Fatalf("shared tree gone: %v", err)
	}
}

func TestShareMediaRefusals(t *testing.T) {
	a := Start(t, twoMovies())
	if _, err := New(Options{Scenario: SharedServer(twoMovies(), "B", a.MachineIdentifier, msToken), ShareMedia: a}); err == nil {
		t.Fatal("same identity accepted")
	}
	withArr := SharedServer(twoMovies(), "B", msID, msToken)
	withArr.Instances = Base("x").Instances
	if _, err := New(Options{Scenario: withArr, ShareMedia: a}); err == nil {
		t.Fatal("instances accepted")
	}
	// An existing file with another size than the scenario declares.
	bad := SharedServer(twoMovies(), "B", msID, msToken)
	bad.Movies[0].Versions[0].Parts[0].Size = 7
	if _, err := New(Options{Scenario: bad, ShareMedia: a}); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("size mismatch: %v", err)
	}
	// A missing file is created.
	extra := SharedServer(twoMovies(), "B", msID, msToken)
	extra.AddMovie(Movie{Section: SectionMovies, Title: "Ronin", Year: 1998, Versions: []Version{
		{Parts: []Part{{File: "movies/Ronin (1998)/Ronin.mkv", Size: MiB(3)}}, Video: FHD("h264")},
	}})
	b, err := New(Options{Scenario: extra, ShareMedia: a})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !a.FileExists(a.RemoteMediaPath("movies/Ronin (1998)/Ronin.mkv")) {
		t.Fatal("missing file not created")
	}
}

func TestMediaRootAndSectionState(t *testing.T) {
	a := Start(t, twoMovies())
	sc := SharedServer(twoMovies(), "Plex B", msID, msToken)
	sc.Server.MediaRoot = "/srv"
	b := StartWithOptions(t, Options{Scenario: sc, ShareMedia: a})
	secs := sectionsOf(t, b)
	loc := secs[0]["Location"].([]any)[0].(map[string]any)["path"]
	if loc != "/srv/movies" {
		t.Fatalf("location %v", loc)
	}
	m := detail(t, b, b.RatingKey("", "Heat"))
	for _, md := range m.Media {
		if !strings.HasPrefix(md.Parts[0].File, "/srv/") {
			t.Fatalf("part %s", md.Parts[0].File)
		}
	}
	if pm := b.PathMappings(); pm[0].Remote != "/srv" || pm[0].Local != a.MediaRoot {
		t.Fatalf("mappings %+v", pm)
	}
	// A refresh path is checked against /srv.
	if r := plexDo(t, b, http.MethodGet, "/library/sections/1/refresh?path=/data/media/movies/Heat%20(1995)"); r.Status != http.StatusBadRequest {
		t.Fatalf("refresh with A's path: %s", r)
	}
	requireViolation(t, b, RulePlexScanOutsideSection)

	// Scan times: settable; a refresh bumps scannedAt, contentChangedAt only when media changed.
	if err := b.SetSectionState("1", SectionState{ScannedAt: 10, ContentChangedAt: 5, Refreshing: true}); err != nil {
		t.Fatal(err)
	}
	s := sectionsOf(t, b)[0]
	if str(s["scannedAt"]) != "10" || str(s["contentChangedAt"]) != "5" || s["refreshing"] != true {
		t.Fatalf("section %v", s)
	}
	if err := b.SetSectionState("9", SectionState{}); err == nil {
		t.Fatal("unknown section accepted")
	}
	_ = b.SetSectionState("1", SectionState{ScannedAt: 10, ContentChangedAt: 5})
	if r := plexDo(t, b, http.MethodGet, "/library/sections/1/refresh?path=/srv/movies"); r.Status != http.StatusOK {
		t.Fatalf("refresh: %s", r)
	}
	st, _ := b.SectionStateOf("1")
	if st.ScannedAt <= 10 || st.ContentChangedAt != 5 {
		t.Fatalf("after a refresh without changes %+v", st)
	}
	if err := a.RemoveFile("movies/Heat (1995)/Heat 1080p.mkv"); err != nil {
		t.Fatal(err)
	}
	if r := plexDo(t, b, http.MethodGet, "/library/sections/1/refresh?path=/srv/movies"); r.Status != http.StatusOK {
		t.Fatalf("refresh: %s", r)
	}
	if st2, _ := b.SectionStateOf("1"); st2.ContentChangedAt <= 5 || st2.ScannedAt <= st.ScannedAt {
		t.Fatalf("after a refresh that trashed media %+v", st2)
	}
	// A new identity.
	b.SetMachineIdentifier("another")
	if mc := plexMC(t, b, "/identity"); mc.MachineIdentifier != "another" {
		t.Fatalf("identity %q", mc.MachineIdentifier)
	}
}

func TestMirrorServer(t *testing.T) {
	a := Start(t, twoMovies())
	b := StartWithOptions(t, Options{Scenario: MirrorServer(twoMovies(), "Mirror", msID, msToken), Dir: t.TempDir()})
	if b.Dir == a.Dir || b.MediaRoot == a.MediaRoot {
		t.Fatal("a mirror has its own tree")
	}
	rel := "movies/Heat (1995)/Heat 1080p.mkv"
	fa, err := os.Stat(a.MediaRoot + "/" + rel)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Stat(b.MediaRoot + "/" + rel)
	if err != nil {
		t.Fatal(err)
	}
	if fa.Size() != fb.Size() || os.SameFile(fa, fb) {
		t.Fatal("the mirror's file must have the same size and be another file")
	}
	if err := a.RemoveFile(rel); err != nil {
		t.Fatal(err)
	}
	if lost := TitlesWithoutFile(a, b); len(lost) != 0 {
		t.Fatalf("lost %v", lost)
	}
	if err := a.RemoveFile("movies4k/Heat (1995)/Heat 2160p.mkv"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{rel, "movies4k/Heat (1995)/Heat 2160p.mkv"} {
		if err := os.Remove(b.MediaRoot + "/" + rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if lost := TitlesWithoutFile(a, b); len(lost) != 1 {
		t.Fatalf("lost %v", lost)
	}
}
