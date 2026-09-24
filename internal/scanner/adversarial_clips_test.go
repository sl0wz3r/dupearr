package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Adversarial review of loose clip sets (clips.go; docs/DECISIONS.md D9 "Loose clip sets"): layouts
// and Plex answers that could still turn a clip into a removable version, or make a whole-set
// removal take more than the set.

// allowDiscRemoval turns on everything a whole-set removal needs.
func (s *discSetup) allowDiscRemoval() {
	st := s.h.settings()
	st.AllowDiscRemoval, st.RecycleBinPath = true, filepath.Join(s.local, ".dupearr-recycle")
	st.DeletionMethods = []string{models.MethodFilesystem}
	s.h.saveSettings(st)
}

// noClipRemoval fails when a version of g with a clip (a disc version or a regular one) is decided
// "remove".
func noClipRemoval(t *testing.T, g *models.DuplicateGroup) {
	t.Helper()
	for _, f := range g.Files {
		if f.Decision != models.DecisionRemove {
			continue
		}
		if f.Version.Disc != nil {
			t.Fatalf("%s: the clip set %s is decided remove (%+v)", g.Key, f.Version.Key, f.Version.Disc)
		}
		for _, p := range f.Version.Parts {
			if strings.HasSuffix(strings.ToLower(p.Path), ".m2ts") {
				t.Fatalf("%s: clip %s is decided remove", g.Key, p.Path)
			}
		}
	}
}

func TestAdversarialCopySuffixedClipsMergeIntoTheSet(t *testing.T) {
	// Two discs flattened into one folder by a file manager: "00001 (2).m2ts", "00002 - Copy.m2ts",
	// "00003 2.m2ts" are clips of the set too. None may stay behind as a regular version.
	s := newDiscSetup(t, false)
	dir := "/data/movies/Elemental (2023)"
	var vs []models.MediaVersion
	for i, name := range []string{"00001.m2ts", "00001 (2).m2ts", "00002 - Copy.m2ts", "00003 2.m2ts", "00004_1.M2TS"} {
		v := clipVer(int64(10+i), dir, 1, int64(1+i)<<20, 1920, int64(10_000+i))
		v.Parts[0].Path = dir + "/" + name
		vs = append(vs, v)
	}
	s.fp.put(s.lib.SectionKey, movie("3383", 1022789, "Elemental", 2023, vs...))
	if run := s.h.fullScan(); run.Stats.GroupsFound != 0 {
		t.Fatalf("groups found %d: the copies are clips of the one set", run.Stats.GroupsFound)
	}
	// Next to an MKV: one clip set holding all five, protected.
	mkv := ver(1, dir+"/Elemental (2023).mkv", 8<<30, 1920)
	s.fp.put(s.lib.SectionKey, movie("3383", 1022789, "Elemental", 2023, append([]models.MediaVersion{mkv}, vs...)...))
	s.h.now = s.h.now.Add(time.Hour)
	s.h.fullScan()
	g := s.h.group("movie:tmdb:1022789")
	cs := discFile(t, g)
	if len(g.Files) != 2 || cs.Version.Disc.ClipCount != 5 || len(cs.Version.Parts) != 5 || !cs.Protected {
		t.Fatalf("files %d disc %+v parts %d", len(g.Files), cs.Version.Disc, len(cs.Version.Parts))
	}
	noClipRemoval(t, g)
}

func TestAdversarialOneFolderSplitAcrossTwoPlexItems(t *testing.T) {
	// A person split the item in Plex (or Plex matched part of the folder to a second item with the
	// same TMDB id): both items list clips of the same folder. Removing either "set" would move the
	// whole folder's clips — the other item's too. Neither may be removable, and the group must
	// still be stored (two versions of the same folder).
	s := newDiscSetup(t, true)
	dir := "/data/movies/Bad Boys (1995)"
	mkv := dir + "/Bad Boys (1995) Remux-2160p.mkv"
	s.file(t, mkv, 60<<20)
	for n := 1; n <= 6; n++ {
		s.file(t, fmt.Sprintf("%s/%05d.m2ts", dir, n), int64(n)<<20)
	}
	s.allowDiscRemoval()
	s.fp.put(s.lib.SectionKey, movie("77", 9737, "Bad Boys", 1995, ver(1, mkv, 60<<20, 3840),
		clipVer(2, dir, 1, 1<<20, 1920, 10_000), clipVer(3, dir, 2, 2<<20, 1920, 20_000), clipVer(4, dir, 3, 3<<20, 1920, 30_000)))
	s.fp.put(s.lib.SectionKey, movie("78", 9737, "Bad Boys", 1995,
		clipVer(5, dir, 4, 4<<20, 1920, 40_000), clipVer(6, dir, 5, 5<<20, 1920, 50_000), clipVer(7, dir, 6, 6<<20, 1920, 60_000)))
	s.h.now = time.Now().Add(48 * time.Hour)
	run := s.h.fullScan()
	if run.Stats.Errors > 0 || run.Stats.GroupsFound == 0 {
		t.Fatalf("scan stats %+v", run.Stats)
	}
	g := s.h.group("movie:tmdb:9737")
	discs := 0
	for _, f := range g.Files {
		if f.Version.Disc != nil {
			discs++
			if !f.Protected || f.Decision != models.DecisionKeep {
				t.Fatalf("set %s of item %s: decision %s protected %v (%s)", f.Version.Key, f.Version.RatingKey, f.Decision, f.Protected, f.Version.Disc.Problem)
			}
		}
	}
	if discs == 0 {
		t.Fatalf("no clip set in %+v", g.Files)
	}
	noClipRemoval(t, g)
}

func TestAdversarialDiscFoldersOfLooseClips(t *testing.T) {
	// A two-disc set flattened per disc ("Disc 1/00001.m2ts", "Disc 2/00001.m2ts"): two sets of one
	// film. With disc removal on and a better MKV, neither disc of the set may be removed on its own.
	s := newDiscSetup(t, true)
	dir := "/data/movies/The Lord of the Rings The Return of the King (2003)"
	mkv := dir + "/The Lord of the Rings The Return of the King (2003) Remux-2160p.mkv"
	s.file(t, mkv, 90<<20)
	var vs []models.MediaVersion
	for d := 1; d <= 2; d++ {
		for n := 1; n <= 3; n++ {
			sub := fmt.Sprintf("%s/Disc %d", dir, d)
			s.file(t, fmt.Sprintf("%s/%05d.m2ts", sub, n), int64(n)<<20)
			vs = append(vs, clipVer(int64(10*d+n), sub, n, int64(n)<<20, 1920, int64(n)*1_000_000))
		}
	}
	s.allowDiscRemoval()
	s.fp.put(s.lib.SectionKey, movie("9", 122, "The Lord of the Rings: The Return of the King", 2003,
		append([]models.MediaVersion{ver(1, mkv, 90<<20, 3840)}, vs...)...))
	s.h.now = time.Now().Add(48 * time.Hour)
	s.h.fullScan()
	g := s.h.group("movie:tmdb:122")
	for _, f := range g.Files {
		if f.Version.Disc != nil && (!f.Protected || f.Decision != models.DecisionKeep) {
			t.Fatalf("disc %s: decision %s protected %v problem %q", f.Version.Disc.Root, f.Decision, f.Protected, f.Version.Disc.Problem)
		}
	}
	noClipRemoval(t, g)
}

func TestAdversarialClipsInTheLibraryRoot(t *testing.T) {
	// Loose clips directly in the library folder (no movie folder): the "set" would be every clip
	// of the library, of any film. Never removable.
	s := newDiscSetup(t, true)
	root := "/data/movies"
	mkv := root + "/Elemental (2023)/Elemental (2023) Remux-2160p.mkv"
	s.file(t, mkv, 60<<20)
	for n := 1; n <= 3; n++ {
		s.file(t, fmt.Sprintf("%s/%05d.m2ts", root, n), int64(n)<<20)
	}
	s.allowDiscRemoval()
	s.fp.put(s.lib.SectionKey, movie("5", 1022789, "Elemental", 2023, ver(1, mkv, 60<<20, 3840),
		clipVer(2, root, 1, 1<<20, 1920, 10_000), clipVer(3, root, 2, 2<<20, 1920, 20_000)))
	s.h.now = time.Now().Add(48 * time.Hour)
	s.h.fullScan()
	g := s.h.group("movie:tmdb:1022789")
	if cs := discFile(t, g); !cs.Protected || cs.Decision != models.DecisionKeep {
		t.Fatalf("library-root set: decision %s protected %v problem %q", cs.Decision, cs.Protected, cs.Version.Disc.Problem)
	}
	noClipRemoval(t, g)
}

func TestAdversarialStreamFolderWithoutBDMV(t *testing.T) {
	// BDMV/ was dropped but its sub-folders kept: "M/STREAM/00001.m2ts", "M/PLAYLIST/00800.mpls",
	// "M/CLIPINF/00001.clpi". The clips are a set of the STREAM folder; whatever Dupearr decides, it
	// never removes a clip alone, and a whole-set removal owns nothing outside STREAM/.
	s := newDiscSetup(t, true)
	dir := "/data/movies/Heat (1995)"
	mkv := dir + "/Heat (1995) Remux-2160p.mkv"
	s.file(t, mkv, 60<<20)
	for n := 1; n <= 3; n++ {
		s.file(t, fmt.Sprintf("%s/STREAM/%05d.m2ts", dir, n), int64(n)<<20)
		s.file(t, fmt.Sprintf("%s/CLIPINF/%05d.clpi", dir, n), 1<<10)
	}
	s.file(t, dir+"/PLAYLIST/00800.mpls", 1<<10)
	s.allowDiscRemoval()
	s.fp.put(s.lib.SectionKey, movie("6", 949, "Heat", 1995, ver(1, mkv, 60<<20, 3840),
		clipVer(2, dir+"/STREAM", 1, 1<<20, 1920, 10_000), clipVer(3, dir+"/STREAM", 2, 2<<20, 1920, 20_000), clipVer(4, dir+"/STREAM", 3, 3<<20, 1920, 30_000)))
	s.h.now = time.Now().Add(48 * time.Hour)
	s.h.fullScan()
	g := s.h.group("movie:tmdb:949")
	cs := discFile(t, g)
	for _, e := range cs.Version.Disc.OwnedEntries {
		if filepath.Base(filepath.Dir(e)) != "STREAM" {
			t.Fatalf("the STREAM set owns %s", e)
		}
	}
	for _, f := range g.Files {
		if f.Version.Disc == nil && f.Version.Key != fmt.Sprintf("plex:%d:1", s.srv.ID) {
			t.Fatalf("a clip stayed a regular version: %+v", f.Version.Parts)
		}
	}
	if cs.Decision == models.DecisionRemove && cs.Version.Disc.Removable {
		// A whole-set removal is allowed here: it must be exactly the STREAM clips.
		if len(cs.Version.Disc.OwnedEntries) != 3 {
			t.Fatalf("owned %v", cs.Version.Disc.OwnedEntries)
		}
	}
}

func TestAdversarialOneFolderInTwoCases(t *testing.T) {
	// A Windows media server reports the same folder in two spellings ("D:\Movies\Elemental (2023)"
	// and "d:\movies\ELEMENTAL (2023)": case-insensitive). They are ONE folder: two sets of it would
	// let one be removed as a "duplicate" of the other — moving the kept set's files too.
	s := newDiscSetup(t, false)
	local := filepath.Join(s.local, "Movies")
	s.h.addMapping(models.PathSourceServer, s.srv.ID, `D:\Movies`, local)
	s.h.addMapping(models.PathSourceServer, s.srv.ID, `d:\movies`, local)
	lib := s.h.addLibraryAt(s.srv.ID, "2", "Films", "movie", true, `D:\Movies`)
	folder := filepath.Join(local, "Elemental (2023)")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	mkvLocal := filepath.Join(folder, "Elemental (2023) Remux-2160p.mkv")
	writeSized(t, mkvLocal, 60<<20)
	for n := 1; n <= 4; n++ {
		writeSized(t, filepath.Join(folder, fmt.Sprintf("%05d.m2ts", n)), int64(n)<<20)
	}
	s.allowDiscRemoval()
	up, low := `D:\Movies\Elemental (2023)`, `d:\movies\ELEMENTAL (2023)`
	clip := func(id int64, dir string, n int) models.MediaVersion {
		v := clipVer(id, dir, n, int64(n)<<20, 1920, int64(n)*10_000)
		v.Parts[0].Path = fmt.Sprintf(`%s\%05d.m2ts`, dir, n)
		return v
	}
	s.fp.put(lib.SectionKey, movie("11", 1022789, "Elemental", 2023,
		ver(1, up+`\Elemental (2023) Remux-2160p.mkv`, 60<<20, 3840),
		clip(2, up, 1), clip(3, up, 2), clip(4, low, 3), clip(5, low, 4)))
	s.h.now = time.Now().Add(48 * time.Hour)
	s.h.fullScan()
	g := s.h.group("movie:tmdb:1022789")
	for _, f := range g.Files {
		if f.Version.Disc != nil && f.Decision == models.DecisionRemove {
			// Only acceptable when this is the one set of the folder (both spellings merged).
			sets := 0
			for _, o := range g.Files {
				if o.Version.Disc != nil {
					sets++
				}
			}
			if sets > 1 {
				t.Fatalf("one of %d sets of the same folder is decided remove: %s (%+v)", sets, f.Version.Key, f.Version.Disc)
			}
		}
	}
	noRegularClip(t, g)
}

// noRegularClip fails when a clip stayed a regular version of g.
func noRegularClip(t *testing.T, g *models.DuplicateGroup) {
	t.Helper()
	for _, f := range g.Files {
		if f.Version.Disc != nil {
			continue
		}
		for _, p := range f.Version.Parts {
			if strings.HasSuffix(strings.ToLower(p.Path), ".m2ts") {
				t.Fatalf("clip %s is a regular version of %s", p.Path, g.Key)
			}
		}
	}
}

func TestAdversarialTargetedScanMergesClips(t *testing.T) {
	// A webhook (Radarr import, Plex library.new) scans one title: the clips must be merged there
	// too, and a stored per-clip group of the title resolves.
	s := newDiscSetup(t, false)
	dir := "/data/movies/Bad Boys (1995)"
	clips := incidentClips(2000, dir, 1, 40, 0)
	s.fp.put(s.lib.SectionKey, movie("77", 9737, "Bad Boys", 1995, clips...))
	run := s.h.targetedScan(models.TargetedScanBody{TmdbID: 9737})
	if run.Stats.GroupsFound != 0 {
		t.Fatalf("targeted scan found %d groups", run.Stats.GroupsFound)
	}
	s.h.noGroup("movie:tmdb:9737")
	// With an MKV (the only playable copy): the group is MKV + one set; the MKV is kept (playable
	// copy) and the set protected.
	mkv := ver(9, dir+"/Bad Boys (1995).mkv", 5<<30, 1280)
	s.fp.put(s.lib.SectionKey, movie("77", 9737, "Bad Boys", 1995, append([]models.MediaVersion{mkv}, clips...)...))
	s.h.now = s.h.now.Add(time.Hour)
	s.h.targetedScan(models.TargetedScanBody{TmdbID: 9737})
	g := s.h.group("movie:tmdb:9737")
	if len(g.Files) != 2 {
		t.Fatalf("files %d", len(g.Files))
	}
	noClipRemoval(t, g)
	noRegularClip(t, g)
}

func TestAdversarialSpanningClipVersionIsNotAPlayableKeeper(t *testing.T) {
	// A version whose clips span two folders is not merged (it stays a regular, protected version).
	// It is not a copy Plex can play either: the MKV next to it is the playable copy and is kept.
	s := newDiscSetup(t, false)
	d := "/data/movies/Coming to America (1988)"
	span := ver(8, d+"/00294.m2ts", 40<<30, 3840, func(v *models.MediaVersion) {
		v.Parts = append(v.Parts, models.MediaPart{ID: 88, Path: d + "/extras/00001.m2ts", Size: 1 << 20})
	})
	s.fp.put(s.lib.SectionKey, movie("4", 1552, "Coming to America", 1988, ver(9, d+"/Coming to America (1988).mkv", 10<<30, 1280), span))
	s.h.fullScan()
	g := s.h.group("movie:tmdb:1552")
	if m := fileByMedia(t, g, 9); m.Decision != models.DecisionKeep {
		t.Fatalf("the only playable copy is decided %s (%s)", m.Decision, m.ProtectedReason)
	}
	noClipRemoval(t, g)
}

func TestAdversarialClipsAsPartsOfOneVersionAndAsVersions(t *testing.T) {
	// A custom scanner stacks some clips into one version while Plex lists the rest one per
	// version, and a Plex glitch lists one clip twice: all of it is the one set of the folder.
	s := newDiscSetup(t, false)
	dir := "/data/movies/Eight Crazy Nights (2002)"
	stack := ver(20, dir+"/00001.m2ts", 1<<20, 1920, func(v *models.MediaVersion) {
		v.Parts = []models.MediaPart{
			{ID: 201, Path: dir + "/00001.m2ts", Size: 1 << 20},
			{ID: 202, Path: dir + "/00002.m2ts", Size: 2 << 20},
			{ID: 203, Path: dir + "/00003 (1).m2ts", Size: 3 << 20},
		}
	})
	dup := clipVer(21, dir, 2, 2<<20, 1920, 20_000) // 00002.m2ts again, as its own version
	s.fp.put(s.lib.SectionKey, movie("17", 16430, "Eight Crazy Nights", 2002, stack, dup,
		clipVer(22, dir, 4, 4<<20, 1920, 5_400_000), clipVer(23, dir, 5, 5<<20, 1920, 30_000)))
	if run := s.h.fullScan(); run.Stats.GroupsFound != 0 {
		t.Fatalf("groups found %d", run.Stats.GroupsFound)
	}
	// With a regular copy: two versions, the set holding five distinct clips once each.
	s.fp.put(s.lib.SectionKey, movie("17", 16430, "Eight Crazy Nights", 2002, ver(1, dir+"/Eight Crazy Nights (2002).mkv", 3<<30, 1280),
		stack, dup, clipVer(22, dir, 4, 4<<20, 1920, 5_400_000), clipVer(23, dir, 5, 5<<20, 1920, 30_000)))
	s.h.now = s.h.now.Add(time.Hour)
	s.h.fullScan()
	g := s.h.group("movie:tmdb:16430")
	cs := discFile(t, g)
	if len(g.Files) != 2 || len(cs.Version.Parts) != 5 || cs.Version.Disc.ClipCount != 5 || cs.Version.Disc.TotalBytes != 15<<20 {
		t.Fatalf("files %d parts %d disc %+v", len(g.Files), len(cs.Version.Parts), cs.Version.Disc)
	}
	if m := fileByMedia(t, g, 1); m.Decision != models.DecisionKeep {
		t.Fatalf("the MKV (only playable copy) is decided %s", m.Decision)
	}
	noClipRemoval(t, g)
	noRegularClip(t, g)
}

func TestAdversarialRetainedPerClipOverrideNeverRemovesTheClip(t *testing.T) {
	// A stored per-clip group carries "remove" overrides on clips. The next scan merges the clips
	// (the overrides are retained for keys that left the group). Later one clip's Plex version is no
	// longer mergeable (Plex adds a part in another folder): its old key — and its retained "remove"
	// override — come back. The clip must still be kept, and nothing may be queued.
	s := newDiscSetup(t, false)
	dir := "/data/movies/Terminator Genisys (2015)"
	clips := incidentClips(3000, dir, 1, 6, 0)
	mkv := ver(9, dir+"/Terminator Genisys (2015).mkv", 5<<30, 1280)
	s.fp.put(s.lib.SectionKey, movie("45", 87101, "Terminator Genisys", 2015, append([]models.MediaVersion{mkv}, clips...)...))
	const key = "movie:tmdb:87101"
	old := &models.DuplicateGroup{Key: key, MediaType: models.MediaTypeMovie, Title: "Terminator Genisys", Year: 2015,
		ServerID: s.srv.ID, LibraryIDs: []int64{s.lib.ID}, ExternalIDs: map[string]string{"tmdb": "87101"},
		Status: models.GroupPending, Flags: []string{}}
	for i, v := range append([]models.MediaVersion{mkv}, clips...) {
		v.Key, v.ServerID, v.LibraryID, v.RatingKey, v.SectionKey = fmt.Sprintf("plex:%d:%d", s.srv.ID, v.MediaID), s.srv.ID, s.lib.ID, "45", s.lib.SectionKey
		f := models.GroupFile{Version: v, Decision: models.DecisionRemove, Override: models.DecisionRemove}
		if i <= 1 {
			f.Decision, f.Override = models.DecisionKeep, ""
		}
		old.Files = append(old.Files, f)
	}
	if _, err := s.h.db.Groups().Upsert(s.h.ctx, old); err != nil {
		t.Fatal(err)
	}
	s.h.fullScan()
	noClipRemoval(t, s.h.group(key))

	// Clip 3 now spans two folders in Plex: not mergeable, its old key returns.
	spanning := clips[2]
	spanning.Parts = append(spanning.Parts, models.MediaPart{ID: 999, Path: dir + "/extras/00099.m2ts", Size: 1 << 20})
	vs := append([]models.MediaVersion{mkv}, clips...)
	vs[3] = spanning
	s.fp.put(s.lib.SectionKey, movie("45", 87101, "Terminator Genisys", 2015, vs...))
	for i := 0; i < 2; i++ { // the override is restored by one scan and applied by the next
		s.h.now = s.h.now.Add(time.Hour)
		s.h.fullScan()
	}
	g := s.h.group(key)
	sp := fileByMedia(t, g, spanning.MediaID)
	if sp.Decision != models.DecisionKeep || !sp.Protected {
		t.Fatalf("the clip with a resurrected override: decision %s override %q protected %v", sp.Decision, sp.Override, sp.Protected)
	}
	if sp.Override != models.DecisionRemove {
		t.Fatalf("setup: the retained override did not come back (%q)", sp.Override)
	}
	if m := fileByMedia(t, g, 9); m.Decision != models.DecisionKeep {
		t.Fatalf("the MKV is decided %s", m.Decision)
	}
	noClipRemoval(t, g)
	if g.Status == models.GroupQueued || len(s.h.approvals()) != 0 {
		t.Fatalf("status %s approvals %v", g.Status, s.h.approvals())
	}
}
