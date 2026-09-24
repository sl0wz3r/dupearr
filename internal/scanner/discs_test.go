package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Full-disc backups (discs.go; docs/DECISIONS.md D9).

// writeSized creates a (sparse) file of the given size, with its folders.
func writeSized(t *testing.T, p string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeFile creates a file with the given content, with its folders.
func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDamagedBDMV creates a Blu-ray structure under root that disc.Detect recognizes (valid
// index.bdmv and playlist headers, a clip) but whose playlist cannot be parsed: an "unreadable"
// disc. It returns the clips' sizes by name.
func writeDamagedBDMV(t *testing.T, root string) map[string]int64 {
	t.Helper()
	header := func(magic string) []byte { return append([]byte(magic+"0200"), make([]byte, 24)...) }
	writeFile(t, filepath.Join(root, "BDMV", "index.bdmv"), header("INDX"))
	writeFile(t, filepath.Join(root, "BDMV", "MovieObject.bdmv"), header("MOBJ"))
	writeFile(t, filepath.Join(root, "BDMV", "PLAYLIST", "00800.mpls"), header("MPLS"))
	writeFile(t, filepath.Join(root, "BDMV", "CLIPINF", "00800.clpi"), header("HDMV"))
	clips := map[string]int64{"00800.m2ts": 3 << 20, "00001.m2ts": 1 << 20}
	for name, size := range clips {
		writeSized(t, filepath.Join(root, "BDMV", "STREAM", name), size)
	}
	writeFile(t, filepath.Join(root, "CERTIFICATE", "id.bdmv"), header("BDID"))
	return clips
}

// discSetup is a server with a movie library "/data/movies" mapped to a temp folder.
type discSetup struct {
	h     *harness
	srv   models.MediaServer
	fp    *fakePlex
	lib   models.Library
	local string // local folder of /data
}

func newDiscSetup(t *testing.T, mapped bool) *discSetup {
	h := newHarness(t)
	srv, fp := h.addServer("Plex")
	lib := h.addLibrary(srv.ID, "movies", "Movies", "movie", "")
	s := &discSetup{h: h, srv: srv, fp: fp, lib: lib, local: t.TempDir()}
	if mapped {
		h.addMapping(models.PathSourceServer, srv.ID, "/data", s.local)
	}
	return s
}

// localOf returns the local path of a /data path.
func (s *discSetup) localOf(remote string) string {
	return filepath.Join(s.local, filepath.FromSlash(strings.TrimPrefix(remote, "/data/")))
}

// file creates the local copy of a Plex part.
func (s *discSetup) file(t *testing.T, remote string, size int64) {
	writeSized(t, s.localOf(remote), size)
}

func discFile(t *testing.T, g *models.DuplicateGroup) *models.GroupFile {
	t.Helper()
	for i := range g.Files {
		if g.Files[i].Version.Disc != nil {
			return &g.Files[i]
		}
	}
	t.Fatalf("group %q has no disc version", g.Key)
	return nil
}

func TestDiscNextToMovieIsAVersion(t *testing.T) {
	s := newDiscSetup(t, true)
	mkv := "/data/movies/Heat (1995)/Heat (1995) Bluray-1080p.mkv"
	s.file(t, mkv, 10<<20)
	iso := s.localOf("/data/movies/Heat (1995)/Heat (1995).iso")
	writeSized(t, iso, 40<<20)
	s.fp.put(s.lib.SectionKey, movie("10", 949, "Heat", 1995, ver(1, mkv, 10<<20, 1920)))

	s.h.fullScan()
	g := s.h.group("movie:tmdb:949")
	if len(g.Files) != 2 || !hasFlag(g, models.FlagFullDisc) {
		t.Fatalf("files %d flags %v", len(g.Files), g.Flags)
	}
	d := discFile(t, g)
	v := d.Version
	wantKey := fmt.Sprintf("disc:%d:%s", s.srv.ID, disc.RootHash(iso))
	if v.Key != wantKey || v.MediaID != 0 || v.RatingKey != "10" || v.LibraryID != s.lib.ID || v.Source != models.SourceDisc {
		t.Fatalf("disc version identity %+v", v)
	}
	di := v.Disc
	if di.Type != models.DiscISO || di.Origin != models.DiscOriginFilesystem || di.Root != "/data/movies/Heat (1995)/Heat (1995).iso" ||
		di.LocalRoot != iso || di.Discs != 1 || di.FileCount != 1 || di.TotalBytes != 40<<20 || !di.Removable ||
		len(di.OwnedEntries) != 1 || di.OwnedEntries[0] != iso || di.Readable {
		t.Fatalf("disc info %+v", di)
	}
	if v.TotalSize() != 40<<20 || len(v.Parts) != 1 || v.Parts[0].Path != di.Root || v.Parts[0].LocalPath != iso {
		t.Fatalf("parts %+v", v.Parts)
	}
	// Removal is off by default: the disc is kept; an ISO's quality is unknown: review.
	if !d.Protected || d.Decision != models.DecisionKeep {
		t.Fatalf("disc decision %s protected %v", d.Decision, d.Protected)
	}
	if g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "full-disc backup") {
		t.Fatalf("status %s (%s)", g.Status, g.StatusReason)
	}

	// Detection off: the movie has one Plex version and no duplicate.
	st := s.h.settings()
	st.DetectDiscs = false
	s.h.saveSettings(st)
	s.h.fullScan()
	if g := s.h.group("movie:tmdb:949"); g.Status != models.GroupResolved {
		t.Fatalf("without detection the group should resolve: %s", g.Status)
	}
}

func TestDiscSearchOnlyInATitleFolder(t *testing.T) {
	s := newDiscSetup(t, true)
	// Two different titles share a folder: a disc there cannot be attributed.
	a, b := "/data/movies/Collection/Heat (1995).mkv", "/data/movies/Collection/Ronin (1998).mkv"
	s.file(t, a, 10<<20)
	s.file(t, b, 10<<20)
	writeSized(t, s.localOf("/data/movies/Collection/Heat (1995).iso"), 40<<20)
	// A movie in the library root: the root is never searched (a flat library holds many titles).
	c := "/data/movies/Alien (1979).mkv"
	s.file(t, c, 10<<20)
	writeSized(t, s.localOf("/data/movies/Alien (1979).iso"), 40<<20)
	s.fp.put(s.lib.SectionKey, movie("10", 949, "Heat", 1995, ver(1, a, 10<<20, 1920)))
	s.fp.put(s.lib.SectionKey, movie("11", 8195, "Ronin", 1998, ver(2, b, 10<<20, 1920)))
	s.fp.put(s.lib.SectionKey, movie("12", 348, "Alien", 1979, ver(3, c, 10<<20, 1920)))

	run := s.h.fullScan()
	if run.Stats.GroupsFound != 0 {
		t.Fatalf("groups found %d", run.Stats.GroupsFound)
	}
}

func TestDiscPlexVersionUnmapped(t *testing.T) {
	// A custom Plex scanner lists the disc's clips as the parts of one version; Dupearr cannot read
	// the disc (no path mapping): the version is a disc with unknown attributes, always kept.
	s := newDiscSetup(t, false)
	clips := []models.MediaPart{
		{ID: 21, Path: "/data/movies/Heat (1995)/BDMV/STREAM/00001.m2ts", Size: 1 << 20, Duration: 60_000},
		{ID: 22, Path: "/data/movies/Heat (1995)/BDMV/STREAM/00800.m2ts", Size: 30 << 20, Duration: 7_000_000},
		{ID: 23, Path: "/data/movies/Heat (1995)/BDMV/STREAM/00801.m2ts", Size: 2 << 20, Duration: 300_000},
	}
	plexDisc := ver(2, clips[0].Path, clips[0].Size, 1920, func(v *models.MediaVersion) { v.Parts = clips })
	s.fp.put(s.lib.SectionKey, movie("10", 949, "Heat", 1995,
		ver(1, "/data/movies/Heat (1995)/Heat (1995).mkv", 10<<20, 1920), plexDisc))

	s.h.fullScan()
	g := s.h.group("movie:tmdb:949")
	d := fileByMedia(t, g, 2)
	v := d.Version
	if v.Disc == nil || v.Disc.Origin != models.DiscOriginPlex || v.Disc.Root != "/data/movies/Heat (1995)" ||
		v.Disc.Type != models.DiscBluray || !strings.Contains(v.Disc.Problem, "no path mapping") || v.Disc.Removable {
		t.Fatalf("plex disc %+v", v.Disc)
	}
	if v.Key != fmt.Sprintf("plex:%d:2", s.srv.ID) || len(v.Parts) != 3 {
		t.Fatalf("a Plex disc keeps its key and parts: %s %d", v.Key, len(v.Parts))
	}
	if v.VideoCodec != "" || v.DurationMs != 0 || v.Source != models.SourceDisc {
		t.Fatalf("Plex's attributes of a disc must not be used: %+v", v)
	}
	if !d.Protected || d.Decision != models.DecisionKeep || g.Status != models.GroupReview ||
		!hasFlag(g, models.FlagDiscUnreadable) || !hasFlag(g, models.FlagFullDisc) {
		t.Fatalf("decision %s protected %v status %s flags %v", d.Decision, d.Protected, g.Status, g.Flags)
	}
}

func TestDiscPlexVersionMappedNoSecondVersion(t *testing.T) {
	// The same disc is a Plex version (custom scanner) and sits in the movie's folder: one version
	// (the Plex one, described by the disc on disk), never a second, synthetic one.
	s := newDiscSetup(t, true)
	root := s.localOf("/data/movies/Heat (1995)")
	sizes := writeDamagedBDMV(t, root)
	mkv := "/data/movies/Heat (1995)/Heat (1995).mkv"
	s.file(t, mkv, 10<<20)
	var parts []models.MediaPart
	for i, name := range []string{"00001.m2ts", "00800.m2ts"} {
		parts = append(parts, models.MediaPart{ID: int64(30 + i), Path: "/data/movies/Heat (1995)/BDMV/STREAM/" + name, Size: sizes[name]})
	}
	plexDisc := ver(2, parts[0].Path, parts[0].Size, 1920, func(v *models.MediaVersion) { v.Parts = parts })
	s.fp.put(s.lib.SectionKey, movie("10", 949, "Heat", 1995, ver(1, mkv, 10<<20, 1920), plexDisc))

	s.h.fullScan()
	g := s.h.group("movie:tmdb:949")
	if len(g.Files) != 2 {
		t.Fatalf("files %d (a disc must not be counted twice)", len(g.Files))
	}
	v := fileByMedia(t, g, 2).Version
	if v.Disc == nil || v.Disc.Origin != models.DiscOriginPlex || v.Disc.LocalRoot != root ||
		len(v.Disc.OwnedEntries) != 2 || v.Disc.FileCount == 0 || v.Disc.Problem == "" || v.Disc.Removable {
		t.Fatalf("plex disc read from disk: %+v", v.Disc)
	}
	if hasFlag(g, models.FlagSameFile) {
		t.Fatalf("flags %v", g.Flags)
	}
}

func TestDiscTrackedClip(t *testing.T) {
	// Radarr imported the main clip in place: the disc version is tracked (never deleted through
	// Radarr), flag disc_tracked_clip; the MKV next to it stays untracked.
	s := newDiscSetup(t, true)
	root := s.localOf("/data/movies/Gladiator (2000)")
	writeDamagedBDMV(t, root)
	mkv := "/data/movies/Gladiator (2000)/Gladiator (2000) WEBDL-1080p.mkv"
	s.file(t, mkv, 10<<20)
	s.fp.put(s.lib.SectionKey, movie("10", 98, "Gladiator", 2000, ver(1, mkv, 10<<20, 1920)))
	fa := &fakeArr{}
	inst := s.h.addArr("Radarr", models.ArrRadarr, fa)
	fa.files = []arr.TrackedFile{{Path: "/data/movies/Gladiator (2000)/BDMV/STREAM/00800.m2ts", Size: 3 << 20, TmdbID: 98,
		Info: models.ArrFileInfo{InstanceID: inst.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 7, ItemID: 70,
			QualitySource: "bluray", QualityModifier: "brdisk"}}}
	s.h.addMapping(models.PathSourceArr, inst.ID, "/data", s.local)

	s.h.fullScan()
	g := s.h.group("movie:tmdb:98")
	d := discFile(t, g)
	if d.Version.Arr == nil || d.Version.Arr.InstanceID != inst.ID ||
		d.Version.Disc.TrackedClip != "/data/movies/Gladiator (2000)/BDMV/STREAM/00800.m2ts" {
		t.Fatalf("tracked clip: arr %+v disc %+v", d.Version.Arr, d.Version.Disc)
	}
	if !hasFlag(g, models.FlagDiscTracked) {
		t.Fatalf("flags %v", g.Flags)
	}
	if fileByMedia(t, g, 1).Version.Arr != nil {
		t.Fatal("the MKV must stay untracked")
	}
}

func TestDiscNextToEpisodesFlagsTheGroup(t *testing.T) {
	h := newHarness(t)
	srv, fp := h.addServer("Plex")
	lib := h.addLibrary(srv.ID, "tv", "TV", "show", "")
	local := t.TempDir()
	h.addMapping(models.PathSourceServer, srv.ID, "/data", local)
	a := "/data/tv/Show/Season 01/Show - S01E01 - Pilot 1080p.mkv"
	b := "/data/tv/Show/Season 01/Show - S01E01 - Pilot 720p.mkv"
	for _, p := range []string{a, b} {
		writeSized(t, filepath.Join(local, strings.TrimPrefix(p, "/data/")), 5<<20)
	}
	writeSized(t, filepath.Join(local, "tv/Show/Season 01/Show Season 1.iso"), 40<<20)
	fp.put(lib.SectionKey, episode("20", 555, "Show", 1, 1, ver(1, a, 5<<20, 1920), ver(2, b, 5<<20, 1280)))

	h.fullScan()
	g := h.group("episode:tvdb:555:s1e1")
	if len(g.Files) != 2 || !hasFlag(g, models.FlagFullDisc) {
		t.Fatalf("episode group files %d flags %v", len(g.Files), g.Flags)
	}
	for _, f := range g.Files {
		if f.Version.Disc != nil {
			t.Fatal("a TV disc is never a version of an episode")
		}
	}
}

func TestAutoModeNeverRemovesADisc(t *testing.T) {
	h := newHarness(t)
	st := h.settings()
	st.Mode = models.ModeAuto
	p := &pipeline{s: h.svc, cfg: &scanConfig{evalConfig: &evalConfig{settings: st}}}
	g := &models.DuplicateGroup{ID: 1, Status: models.GroupPending, StableCount: 10, Flags: []string{},
		Files: []models.GroupFile{
			{Decision: models.DecisionKeep, Version: models.MediaVersion{Key: "plex:1:1"}},
			{Decision: models.DecisionRemove, Version: models.MediaVersion{Key: "disc:1:ab", Disc: &models.DiscInfo{Type: models.DiscBluray}}},
		}}
	if p.autoEligible(g) {
		t.Fatal("auto mode may never approve a disc removal")
	}
	g.Files[1].Version.Disc = nil
	if !p.autoEligible(g) {
		t.Fatal("control: a regular removal is eligible")
	}
}

func TestDiscTargetedScan(t *testing.T) {
	// A webhook for one movie finds the disc next to it too (the item has one Plex version).
	s := newDiscSetup(t, true)
	mkv := "/data/movies/Heat (1995)/Heat (1995).mkv"
	s.file(t, mkv, 10<<20)
	writeSized(t, s.localOf("/data/movies/Heat (1995)/Heat (1995).iso"), 40<<20)
	s.fp.put(s.lib.SectionKey, movie("10", 949, "Heat", 1995, ver(1, mkv, 10<<20, 1920)))
	run, err := s.h.svc.TargetedScan(s.h.ctx, models.TargetedScanBody{ServerID: s.srv.ID, RatingKeys: []string{"10"}}, models.TriggerWebhook)
	if err != nil || run.Stats.Errors != 0 {
		t.Fatalf("targeted scan: %v %+v", err, run)
	}
	g := s.h.group("movie:tmdb:949")
	if len(g.Files) != 2 || discFile(t, g).Version.Disc.Type != models.DiscISO {
		t.Fatalf("targeted: %+v", g.Files)
	}
}

func TestIsPlexDisc(t *testing.T) {
	cases := map[string]bool{
		"/m/Heat (1995)/BDMV/STREAM/00800.m2ts":   true,
		`D:\m\Heat\BDMV\STREAM\00001.M2TS`:        true,
		"/m/Heat (1995)/VIDEO_TS/VTS_01_1.VOB":    true,
		"/m/Heat (1995)/VIDEO_TS/VIDEO_TS.IFO":    true,
		"/m/Heat (1995)/VTS_01_1.VOB":             true, // flat DVD
		"/m/Heat (1995)/Heat (1995).iso":          true,
		"/m/Heat (1995)/Heat (1995).mkv":          false,
		"/m/Inception (2010)/Inception.m2ts":      false,
		"/m/Certificate/Certificate (2019).mkv":   false, // a title, not a disc
		"/m/Heat (1995)/BDMV/BACKUP/00800.mpls":   false, // Plex never lists these
		"/m/Heat (1995)/HVDVD_TS/FEATURE_1.EVO":   true,
		"/m/Heat (1995)/BDAV/STREAM/00001.m2ts":   true,
		"/m/Heat (1995)/Disc 1/BDMV/STREAM/1.mts": true,
	}
	for p, want := range cases {
		v := models.MediaVersion{Parts: []models.MediaPart{{Path: p}}}
		if got := isPlexDisc(&v); got != want {
			t.Errorf("isPlexDisc(%q) = %v, want %v", p, got, want)
		}
	}
}
