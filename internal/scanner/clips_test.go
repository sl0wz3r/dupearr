package scanner

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Loose clip sets (clips.go; docs/DECISIONS.md D9 "Loose clip sets"). The incident: a library
// keeps Blu-ray backups flattened — "/data/movies/Elemental (2023)/00174.m2ts", "00175.m2ts" … —
// and Plex lists each loose clip as a separate version of the movie. The old scanner made a group
// of 100+ "copies", proposed removing all but the longest clip, and a manual approval deleted 337
// clips permanently through Plex.

// clipVer is a Plex version whose single part is a loose clip of folder dir.
func clipVer(mediaID int64, dir string, clip int, size int64, width int, durMs int64) models.MediaVersion {
	return ver(mediaID, fmt.Sprintf("%s/%05d.m2ts", dir, clip), size, width, withDuration(durMs), func(v *models.MediaVersion) {
		v.Container = "ts" // what Plex reports for every .m2ts clip (and for a real .ts movie)
		v.Parts[0].Duration = durMs
	})
}

// incidentClips returns n clip versions (media ids from firstID) of dir; clip mainAt is the
// 102-minute film clip, the others are menus, logos and extras of a few seconds to minutes.
func incidentClips(firstID int64, dir string, firstClip, n, mainAt int) []models.MediaVersion {
	var out []models.MediaVersion
	for i := 0; i < n; i++ {
		size, dur, width := int64(1+i%5)<<20, int64(5_000+i*1_000), 1920
		if i == mainAt {
			size, dur, width = 53<<30, 6_150_000, 3840
		}
		out = append(out, clipVer(firstID+int64(i), dir, firstClip+i, size, width, dur))
	}
	return out
}

func clipSetKey(serverID int64, dir string) string {
	return fmt.Sprintf("disc:%d:%s", serverID, disc.ClipSetHash(dir))
}

func TestLooseClipsIncidentNoLongerAGroup(t *testing.T) {
	// The user's instance cannot see /data (no path mapping): the merge works from Plex's part
	// paths alone. Elemental: 197 loose clips, one Plex item, no other copy.
	s := newDiscSetup(t, false)
	dir := "/data/movies/Elemental (2023)"
	clips := incidentClips(1000, dir, 174, 197, 150)
	s.fp.put(s.lib.SectionKey, movie("3383", 1022789, "Elemental", 2023, clips...))

	run := s.h.fullScan()
	// One version (the clip set): no duplicate, nothing to approve, nothing removable.
	if run.Stats.GroupsFound != 0 {
		t.Fatalf("groups found %d: a loose clip set is one version", run.Stats.GroupsFound)
	}
	s.h.noGroup("movie:tmdb:1022789")
	if len(s.h.approvals()) != 0 {
		t.Fatalf("approvals %v", s.h.approvals())
	}
	// The merge does not depend on disc detection (nor on a path mapping).
	st := s.h.settings()
	st.DetectDiscs = false
	s.h.saveSettings(st)
	if run := s.h.fullScan(); run.Stats.GroupsFound != 0 {
		t.Fatalf("groups found %d with detection off", run.Stats.GroupsFound)
	}
}

func TestLooseClipsStoredPerClipGroupResolves(t *testing.T) {
	// A group stored by the old code: 111 per-clip versions, the longest kept, the others marked
	// for removal with a user override, queued, with a pending removal action. The next scan
	// merges the clips: the group disappears, is resolved, and its queued removal is cancelled.
	s := newDiscSetup(t, false)
	dir := "/data/movies/Bad Boys (1995)"
	clips := incidentClips(2000, dir, 1, 111, 0)
	s.fp.put(s.lib.SectionKey, movie("77", 9737, "Bad Boys", 1995, clips...))
	const key = "movie:tmdb:9737"
	old := &models.DuplicateGroup{Key: key, MediaType: models.MediaTypeMovie, Title: "Bad Boys", Year: 1995,
		ServerID: s.srv.ID, LibraryIDs: []int64{s.lib.ID}, ExternalIDs: map[string]string{"tmdb": "9737"},
		Status: models.GroupQueued, StatusReason: "approved", Flags: []string{}}
	for i := range clips {
		v := clips[i]
		v.Key, v.ServerID, v.LibraryID, v.RatingKey, v.SectionKey = fmt.Sprintf("plex:%d:%d", s.srv.ID, v.MediaID), s.srv.ID, s.lib.ID, "77", s.lib.SectionKey
		f := models.GroupFile{Version: v, Decision: models.DecisionRemove, Override: models.DecisionRemove}
		if i == 0 {
			f.Decision, f.Override = models.DecisionKeep, ""
		}
		old.Files = append(old.Files, f)
	}
	if _, err := s.h.db.Groups().Upsert(s.h.ctx, old); err != nil {
		t.Fatal(err)
	}
	stored := s.h.group(key)
	rm := stored.Files[5]
	action := &models.Action{GroupID: stored.ID, GroupFileID: rm.ID, VersionKey: rm.Version.Key, Title: "Bad Boys",
		Paths: []string{rm.Version.Parts[0].Path}, Size: rm.Version.TotalSize(), Status: models.ActionPending}
	if err := s.h.db.Actions().Create(s.h.ctx, action); err != nil {
		t.Fatal(err)
	}

	s.h.fullScan()
	g := s.h.group(key)
	if g.Status != models.GroupResolved {
		t.Fatalf("stored per-clip group status %s, want resolved", g.Status)
	}
	a, err := s.h.db.Actions().Get(s.h.ctx, action.ID)
	if err != nil || a.Status != models.ActionCancelled {
		t.Fatalf("the queued removal of a clip must be cancelled: %+v %v", a, err)
	}

	// A regular copy appears later: the group comes back as clip set + MKV — the per-clip
	// overrides of the resolved group never come back, and nothing of the clips is removable.
	mkv := ver(9, dir+"/Bad Boys (1995) Remux-1080p.mkv", 30<<30, 1920)
	s.fp.put(s.lib.SectionKey, movie("77", 9737, "Bad Boys", 1995, append([]models.MediaVersion{mkv}, clips...)...))
	s.h.now = s.h.now.Add(time.Hour)
	s.h.fullScan()
	g = s.h.group(key)
	if len(g.Files) != 2 {
		t.Fatalf("files %d, want the MKV and one clip set", len(g.Files))
	}
	cs := discFile(t, g)
	if cs.Version.Key != clipSetKey(s.srv.ID, dir) || cs.Override != "" || cs.Decision != models.DecisionKeep || !cs.Protected {
		t.Fatalf("clip set %s override %q decision %s protected %v", cs.Version.Key, cs.Override, cs.Decision, cs.Protected)
	}
	for _, f := range g.Files {
		if f.Decision == models.DecisionRemove {
			t.Fatalf("%s decided remove", f.Version.Key)
		}
	}
}

func TestLooseClipsQueuedGroupWithMKVDropsClipRemovals(t *testing.T) {
	// The Equalizer 3: an MKV and 129 clips in one folder; the old group kept the MKV and queued
	// every clip for removal. The next scan keeps the MKV and the clip set (protected) and the
	// queued clip removals are cancelled.
	s := newDiscSetup(t, false)
	dir := "/data/movies/The Equalizer 3 (2023)"
	mkv := ver(1, dir+"/flame-the.equalizer.3.2023.proper.2160p.uhd.bluray.h265.mkv", 46<<30, 3840, withDuration(6_540_000))
	clips := incidentClips(100, dir, 1, 129, 0)
	s.fp.put(s.lib.SectionKey, movie("50", 926393, "The Equalizer 3", 2023, append([]models.MediaVersion{mkv}, clips...)...))
	const key = "movie:tmdb:926393"
	old := &models.DuplicateGroup{Key: key, MediaType: models.MediaTypeMovie, Title: "The Equalizer 3", Year: 2023,
		ServerID: s.srv.ID, LibraryIDs: []int64{s.lib.ID}, ExternalIDs: map[string]string{"tmdb": "926393"},
		Status: models.GroupQueued, StatusReason: "approved", Flags: []string{}}
	for i, v := range append([]models.MediaVersion{mkv}, clips...) {
		v.Key, v.ServerID, v.LibraryID, v.RatingKey, v.SectionKey = fmt.Sprintf("plex:%d:%d", s.srv.ID, v.MediaID), s.srv.ID, s.lib.ID, "50", s.lib.SectionKey
		f := models.GroupFile{Version: v, Decision: models.DecisionRemove}
		if i == 0 {
			f.Decision = models.DecisionKeep
		}
		old.Files = append(old.Files, f)
	}
	if _, err := s.h.db.Groups().Upsert(s.h.ctx, old); err != nil {
		t.Fatal(err)
	}
	stored := s.h.group(key)
	var actions []*models.Action
	for _, f := range stored.Files[1:4] {
		a := &models.Action{GroupID: stored.ID, GroupFileID: f.ID, VersionKey: f.Version.Key, Title: "The Equalizer 3",
			Paths: []string{f.Version.Parts[0].Path}, Size: f.Version.TotalSize(), Status: models.ActionPending}
		if err := s.h.db.Actions().Create(s.h.ctx, a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}

	s.h.fullScan()
	g := s.h.group(key)
	if len(g.Files) != 2 || g.Status == models.GroupQueued || g.Status == models.GroupPending {
		t.Fatalf("files %d status %s (%s)", len(g.Files), g.Status, g.StatusReason)
	}
	for _, f := range g.Files {
		if f.Decision != models.DecisionKeep {
			t.Fatalf("%s decided %s", f.Version.Key, f.Decision)
		}
	}
	for _, a := range actions {
		got, err := s.h.db.Actions().Get(s.h.ctx, a.ID)
		if err != nil || got.Status != models.ActionCancelled {
			t.Fatalf("queued clip removal %d: %+v %v", a.ID, got, err)
		}
	}
}

func TestLooseClipsMergeCorrectness(t *testing.T) {
	s := newDiscSetup(t, false)
	dir := "/data/movies/Terminator Genisys (2015)"
	hdr := func(v *models.MediaVersion) {
		v.DynamicRange, v.DVProfile, v.VideoProfile = models.DRDolbyVisionHDR10, 8, "main 10"
		v.AudioTracks = []models.AudioTrack{{Format: models.AudioTrueHDAtmos, Codec: "truehd", Channels: 8, LanguageCode: "eng", Default: true, Atmos: true}}
	}
	c1 := clipVer(11, dir, 1, 900<<20, 1920, 90_000)
	c10 := clipVer(12, dir, 10, 68<<30, 3840, 7_542_000) // the 125.7-minute film clip
	hdr(&c10)
	c147 := clipVer(13, dir, 147, 30<<20, 720, 300_000)
	// A version listing two clips (a stack) and one clip listed twice: one file, one part.
	stack := ver(14, dir+"/00002.m2ts", 2<<20, 1920, func(v *models.MediaVersion) {
		v.Parts = append(v.Parts, models.MediaPart{ID: 99, Path: dir + "/00003.m2ts", Size: 3 << 20})
	})
	dup := clipVer(15, dir, 1, 900<<20, 1920, 90_000)
	mkv := ver(1, dir+"/Terminator Genisys (2015) WEBDL-1080p.mkv", 8<<30, 1920, withDuration(7_560_000))
	s.fp.put(s.lib.SectionKey, movie("60", 87101, "Terminator Genisys", 2015, mkv, c1, c10, c147, stack, dup))

	s.h.fullScan()
	g := s.h.group("movie:tmdb:87101")
	if len(g.Files) != 2 {
		t.Fatalf("files %d, want the MKV and one clip set", len(g.Files))
	}
	f := discFile(t, g)
	v := f.Version
	if v.Key != clipSetKey(s.srv.ID, dir) || v.MediaID != 0 || v.RatingKey != "60" || v.LibraryID != s.lib.ID ||
		v.Source != models.SourceDisc || v.Container != "disc" {
		t.Fatalf("identity %+v", v)
	}
	var paths []string
	for _, p := range v.Parts {
		paths = append(paths, filepath.Base(p.Path))
	}
	if !slices.Equal(paths, []string{"00001.m2ts", "00002.m2ts", "00003.m2ts", "00010.m2ts", "00147.m2ts"}) {
		t.Fatalf("parts %v", paths)
	}
	wantTotal := int64(900<<20 + 68<<30 + 30<<20 + 2<<20 + 3<<20)
	if v.TotalSize() != wantTotal {
		t.Fatalf("size %d, want %d", v.TotalSize(), wantTotal)
	}
	// Attributes of the longest clip (the main-feature candidate).
	if v.Width != 3840 || v.Resolution != models.Res2160 || v.DynamicRange != models.DRDolbyVisionHDR10 || v.DVProfile != 8 ||
		v.DurationMs != 7_542_000 || len(v.AudioTracks) != 1 || v.AudioTracks[0].Format != models.AudioTrueHDAtmos {
		t.Fatalf("attributes %+v", v)
	}
	d := v.Disc
	if d.Type != models.DiscBlurayClips || d.Origin != models.DiscOriginPlex || d.Root != dir || d.ClipCount != 5 || d.FileCount != 5 ||
		d.MainClip != "00010.m2ts" || d.MainFeature != "00010.m2ts" || d.FeatureBytes != 68<<30 || d.TotalBytes != wantTotal ||
		!slices.Equal(d.PlexMediaIDs, []int64{11, 12, 13, 14, 15}) || d.Removable || !strings.Contains(d.Problem, "no path mapping") {
		t.Fatalf("disc %+v", d)
	}
	if !strings.Contains(v.DisplayTitle, "Blu-ray clips (loose .m2ts) · 5 clips") {
		t.Fatalf("display title %q", v.DisplayTitle)
	}
	// Protected (disc removal off); the MKV is kept too (the only Plex-playable copy): nothing to
	// remove.
	if !f.Protected || f.Decision != models.DecisionKeep || !hasFlag(g, models.FlagFullDisc) {
		t.Fatalf("clip set decision %s protected %v flags %v", f.Decision, f.Protected, g.Flags)
	}
	for _, gf := range g.Files {
		if gf.Decision == models.DecisionRemove {
			t.Fatalf("%s decided remove", gf.Version.Key)
		}
	}
	if err := engine.ValidateDecisions(g); err != nil {
		t.Fatal(err)
	}

	// Stable key: the next scan (different listing order) yields the same version and signature.
	sig := g.Signature
	s.fp.put(s.lib.SectionKey, movie("60", 87101, "Terminator Genisys", 2015, c147, dup, mkv, c10, stack, c1))
	s.h.now = s.h.now.Add(time.Hour)
	s.h.fullScan()
	g = s.h.group("movie:tmdb:87101")
	if discFile(t, g).Version.Key != v.Key || g.Signature != sig {
		t.Fatalf("key/signature changed: %s %s", discFile(t, g).Version.Key, g.Signature)
	}
}

func TestLooseClipsPartialSetAndFolders(t *testing.T) {
	s := newDiscSetup(t, false)
	// Only two clips left after the incident: still one clip set — no group.
	a := "/data/movies/Willy Wonka & the Chocolate Factory (1971)"
	s.fp.put(s.lib.SectionKey, movie("1", 252, "Willy Wonka & the Chocolate Factory", 1971,
		clipVer(1, a, 77, 63<<30, 1920, 5_976_000), clipVer(2, a, 3, 10<<20, 1920, 60_000)))
	// One clip next to an MKV: a two-version group, the clip set protected.
	b := "/data/movies/Poltergeist (1982)"
	s.fp.put(s.lib.SectionKey, movie("2", 609, "Poltergeist", 1982,
		ver(3, b+"/Poltergeist (1982).mkv", 20<<30, 1920), clipVer(4, b, 2, 45<<30, 1920, 6_864_000)))
	// Clips in two folders: two clip sets (two disc versions), both protected.
	c1, c2 := "/data/movies/LOTR (2003)/Disc 1", "/data/movies/LOTR (2003)/Disc 2"
	s.fp.put(s.lib.SectionKey, movie("3", 122, "The Lord of the Rings: The Return of the King", 2003,
		clipVer(5, c1, 4, 1<<20, 1920, 30_000), clipVer(6, c1, 5, 1<<20, 1920, 20_000), clipVer(7, c2, 4, 1<<20, 1920, 30_000)))
	// A version whose parts span two folders is not a set: it stays a regular version, and the
	// per-file guard protects it.
	d := "/data/movies/Coming to America (1988)"
	span := ver(8, d+"/00294.m2ts", 40<<30, 1920, func(v *models.MediaVersion) {
		v.Parts = append(v.Parts, models.MediaPart{ID: 88, Path: d + "/extras/00001.m2ts", Size: 1 << 20})
	})
	s.fp.put(s.lib.SectionKey, movie("4", 1552, "Coming to America", 1988, ver(9, d+"/Coming to America (1988).mkv", 10<<30, 1920), span))

	s.h.fullScan()
	s.h.noGroup("movie:tmdb:252")

	g := s.h.group("movie:tmdb:609")
	cs := discFile(t, g)
	if len(g.Files) != 2 || cs.Version.Disc.ClipCount != 1 || cs.Version.Disc.Type != models.DiscBlurayClips || !cs.Protected || cs.Decision != models.DecisionKeep {
		t.Fatalf("partial set: files %d disc %+v protected %v", len(g.Files), cs.Version.Disc, cs.Protected)
	}

	g = s.h.group("movie:tmdb:122")
	if len(g.Files) != 2 {
		t.Fatalf("LOTR files %d", len(g.Files))
	}
	for _, f := range g.Files {
		if f.Version.Disc == nil || !f.Protected || f.Decision != models.DecisionKeep {
			t.Fatalf("LOTR %s disc %v protected %v decision %s", f.Version.Key, f.Version.Disc != nil, f.Protected, f.Decision)
		}
	}
	if k1, k2 := g.Files[0].Version.Key, g.Files[1].Version.Key; k1 == k2 || !slices.Contains([]string{clipSetKey(s.srv.ID, c1), clipSetKey(s.srv.ID, c2)}, k1) {
		t.Fatalf("LOTR keys %s %s", k1, k2)
	}

	g = s.h.group("movie:tmdb:1552")
	sp := fileByMedia(t, g, 8)
	if sp.Version.Disc != nil || !sp.Protected || sp.Decision != models.DecisionKeep || !hasFlag(g, models.FlagFullDisc) {
		t.Fatalf("spanning version disc %v protected %v decision %s flags %v", sp.Version.Disc, sp.Protected, sp.Decision, g.Flags)
	}
}

func TestLooseDVDFilesAndEpisodes(t *testing.T) {
	s := newDiscSetup(t, false)
	dir := "/data/movies/Casablanca (1942)"
	vob := func(id int64, name string, size int64) models.MediaVersion {
		return ver(id, dir+"/"+name, size, 720, func(v *models.MediaVersion) { v.Container = "vob" })
	}
	s.fp.put(s.lib.SectionKey, movie("1", 289, "Casablanca", 1942,
		ver(1, dir+"/Casablanca (1942).mkv", 8<<30, 1920),
		vob(2, "VTS_01_1.VOB", 1<<30), vob(3, "VTS_01_2.VOB", 1<<30), vob(4, "VIDEO_TS.VOB", 1<<20)))
	tv := s.h.addLibrary(s.srv.ID, "tv", "TV", "show", "")
	edir := "/data/tv/Planet Earth/Season 01"
	s.fp.put(tv.SectionKey, episode("20", 79257, "Planet Earth", 1, 1,
		clipVer(21, edir, 1, 20<<30, 1920, 3_000_000), clipVer(22, edir, 2, 1<<20, 1920, 30_000)))

	run := s.h.fullScan()
	if run.Stats.GroupsFound != 1 {
		t.Fatalf("groups found %d, want only the movie's", run.Stats.GroupsFound)
	}
	g := s.h.group("movie:tmdb:289")
	cs := discFile(t, g)
	if len(g.Files) != 2 || cs.Version.Disc.Type != models.DiscDVDClips || cs.Version.Disc.ClipCount != 3 || !cs.Protected {
		t.Fatalf("DVD files: files %d disc %+v", len(g.Files), cs.Version.Disc)
	}
	// An episode's loose clips are one version too: no group.
	s.h.noGroup("episode:tvdb:79257:s1e1")
}

func TestLooseClipsMappedSetOnDisk(t *testing.T) {
	// The folder is visible: the set owns every clip-named file of the folder (also those Plex does
	// not list) and nothing else; with disc removal allowed a person may remove it as a whole.
	s := newDiscSetup(t, true)
	dir := "/data/movies/Whiplash (2014)"
	mkv := dir + "/Whiplash (2014) Bluray-2160p.mkv"
	s.file(t, mkv, 30<<20)
	for _, other := range []string{"/Whiplash (2014).nfo", "/poster.jpg", "/Whiplash (2014).en.srt"} {
		s.file(t, dir+other, 1<<10)
	}
	sizes := map[int]int64{1: 20 << 20, 2: 1 << 20, 3: 2 << 20, 350: 3 << 20, 351: 4 << 20}
	for n, size := range sizes {
		s.file(t, fmt.Sprintf("%s/%05d.m2ts", dir, n), size)
	}
	listed := []models.MediaVersion{clipVer(2, dir, 1, 20<<20, 1920, 6_420_000), clipVer(3, dir, 2, 1<<20, 1920, 10_000), clipVer(4, dir, 3, 2<<20, 1920, 20_000)}
	s.fp.put(s.lib.SectionKey, movie("5", 244786, "Whiplash", 2014, append([]models.MediaVersion{ver(1, mkv, 30<<20, 3840)}, listed...)...))
	// The files were just written: their change time dates the set (minimum age, 24 h here).
	s.h.now = time.Now().Add(48 * time.Hour)

	s.h.fullScan()
	g := s.h.group("movie:tmdb:244786")
	if len(g.Files) != 2 {
		t.Fatalf("files %d", len(g.Files))
	}
	f := discFile(t, g)
	d := f.Version.Disc
	var want []string
	var total int64
	for n, size := range sizes {
		want = append(want, s.localOf(fmt.Sprintf("%s/%05d.m2ts", dir, n)))
		total += size
	}
	slices.Sort(want)
	if d.Type != models.DiscBlurayClips || d.Origin != models.DiscOriginPlex || d.LocalRoot != s.localOf(dir) ||
		!slices.Equal(d.OwnedEntries, want) || d.FileCount != 5 || d.ClipCount != 5 || d.TotalBytes != total ||
		!d.Removable || d.Problem != "" || d.FeatureBytes != 20<<20 || d.MainClip != "00001.m2ts" || len(d.PlexMediaIDs) != 3 {
		t.Fatalf("disc %+v", d)
	}
	if len(f.Version.Parts) != 3 {
		t.Fatalf("parts %d: the Plex-listed clips", len(f.Version.Parts))
	}
	// Disc removal off: protected.
	if !f.Protected || f.Decision != models.DecisionKeep || hasFlag(g, models.FlagDiscUnreadable) {
		t.Fatalf("decision %s protected %v flags %v", f.Decision, f.Protected, g.Flags)
	}

	// Disc removal on (recycle bin, filesystem method): the lower-ranked clip set may be removed
	// as a whole — by a person only (full_disc keeps it out of auto mode).
	st := s.h.settings()
	st.AllowDiscRemoval, st.RecycleBinPath = true, filepath.Join(s.local, ".dupearr-recycle")
	st.DeletionMethods = []string{models.MethodFilesystem}
	s.h.saveSettings(st)
	s.h.now = s.h.now.Add(time.Hour)
	s.h.fullScan()
	g = s.h.group("movie:tmdb:244786")
	f = discFile(t, g)
	if f.Decision != models.DecisionRemove || f.Protected || !hasFlag(g, models.FlagFullDisc) || !engine.BlocksAutoApproval(models.FlagFullDisc) {
		t.Fatalf("with disc removal: decision %s protected %v status %s flags %v", f.Decision, f.Protected, g.Status, g.Flags)
	}
	if err := engine.ValidateDecisions(g); err != nil {
		t.Fatal(err)
	}

	// A clip Plex lists that is not on disk: the set on disk is not the version — never removable.
	s.fp.put(s.lib.SectionKey, movie("5", 244786, "Whiplash", 2014, append([]models.MediaVersion{ver(1, mkv, 30<<20, 3840),
		clipVer(9, dir, 777, 1<<20, 1920, 1_000)}, listed...)...))
	s.h.now = s.h.now.Add(time.Hour)
	s.h.fullScan()
	f = discFile(t, s.h.group("movie:tmdb:244786"))
	if f.Version.Disc.Removable || !strings.Contains(f.Version.Disc.Problem, "not part of the set on disk") || !f.Protected {
		t.Fatalf("disc %+v protected %v", f.Version.Disc, f.Protected)
	}
}

func TestLooseClipsFoundOnDiskOnly(t *testing.T) {
	// Plex lists only the MKV; the folder holds loose clips too: a clip set found on disk
	// (origin filesystem), protected.
	s := newDiscSetup(t, true)
	dir := "/data/movies/Hocus Pocus (1993)"
	mkv := dir + "/Hocus Pocus (1993).mkv"
	s.file(t, mkv, 10<<20)
	for n := 174; n < 180; n++ {
		s.file(t, fmt.Sprintf("%s/%05d.m2ts", dir, n), 1<<20)
	}
	s.fp.put(s.lib.SectionKey, movie("7", 10439, "Hocus Pocus", 1993, ver(1, mkv, 10<<20, 1920)))
	s.h.fullScan()
	g := s.h.group("movie:tmdb:10439")
	f := discFile(t, g)
	d := f.Version.Disc
	if len(g.Files) != 2 || d.Type != models.DiscBlurayClips || d.Origin != models.DiscOriginFilesystem || len(d.OwnedEntries) != 6 ||
		f.Version.Key != fmt.Sprintf("disc:%d:%s", s.srv.ID, disc.RootHash(s.localOf(dir))) || !f.Protected {
		t.Fatalf("files %d disc %+v key %s protected %v", len(g.Files), d, f.Version.Key, f.Protected)
	}
}

func TestLooseClipsArrTracksTheMKV(t *testing.T) {
	// Radarr tracks the MKV next to the clips: the MKV version is tracked, the clip set is not.
	s := newDiscSetup(t, false)
	dir := "/data/movies/Sausage Party (2016)"
	mkv := dir + "/Sausage Party (2016).mkv"
	radarr := &fakeArr{queue: map[int64]bool{}}
	r := s.h.addArr("Radarr", models.ArrRadarr, radarr)
	radarr.files = []arr.TrackedFile{{Path: mkv, Size: 9 << 30, TmdbID: 223702,
		Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 1, ItemID: 7}}}
	s.fp.put(s.lib.SectionKey, movie("8", 223702, "Sausage Party", 2016, ver(1, mkv, 9<<30, 1920),
		clipVer(2, dir, 1, 40<<30, 1920, 5_328_000), clipVer(3, dir, 2, 1<<20, 1920, 10_000)))
	s.h.fullScan()
	g := s.h.group("movie:tmdb:223702")
	if fileByMedia(t, g, 1).Version.Arr == nil || discFile(t, g).Version.Arr != nil || hasFlag(g, models.FlagDiscTracked) {
		t.Fatalf("arr attachment wrong: flags %v", g.Flags)
	}
}

func TestLooseClipsOldApprovalNeverCarriesOverToTheSet(t *testing.T) {
	// Disc removal is on and the clip set, readable on disk, now ranks below the MKV: the scan
	// decides "remove" for the whole set. The old approval (MKV kept, clips queued one by one) must
	// not carry over to it: the clip removals are cancelled and the set needs its own approval.
	s := newDiscSetup(t, true)
	dir := "/data/movies/The Equalizer 3 (2023)"
	mkvPath := dir + "/The Equalizer 3 (2023) Remux-2160p.mkv"
	s.file(t, mkvPath, 40<<20)
	var clips []models.MediaVersion
	for i := 1; i <= 4; i++ {
		s.file(t, fmt.Sprintf("%s/%05d.m2ts", dir, i), int64(i)<<20)
		clips = append(clips, clipVer(int64(10+i), dir, i, int64(i)<<20, 1920, int64(i)*60_000))
	}
	mkv := ver(1, mkvPath, 40<<20, 3840, withDuration(6_540_000), func(v *models.MediaVersion) { v.Source = models.SourceRemux })
	s.fp.put(s.lib.SectionKey, movie("50", 926393, "The Equalizer 3", 2023, append([]models.MediaVersion{mkv}, clips...)...))
	st := s.h.settings()
	st.AllowDiscRemoval, st.RecycleBinPath = true, filepath.Join(s.local, ".dupearr-recycle")
	st.DeletionMethods = []string{models.MethodFilesystem}
	s.h.saveSettings(st)

	const key = "movie:tmdb:926393"
	old := &models.DuplicateGroup{Key: key, MediaType: models.MediaTypeMovie, Title: "The Equalizer 3", Year: 2023,
		ServerID: s.srv.ID, LibraryIDs: []int64{s.lib.ID}, ExternalIDs: map[string]string{"tmdb": "926393"},
		Status: models.GroupQueued, StatusReason: "approved", Flags: []string{}}
	for i, v := range append([]models.MediaVersion{mkv}, clips...) {
		v.Key, v.ServerID, v.LibraryID, v.RatingKey, v.SectionKey = fmt.Sprintf("plex:%d:%d", s.srv.ID, v.MediaID), s.srv.ID, s.lib.ID, "50", s.lib.SectionKey
		f := models.GroupFile{Version: v, Decision: models.DecisionRemove}
		if i == 0 {
			f.Decision = models.DecisionKeep
		}
		old.Files = append(old.Files, f)
	}
	if _, err := s.h.db.Groups().Upsert(s.h.ctx, old); err != nil {
		t.Fatal(err)
	}
	stored := s.h.group(key)
	var actions []*models.Action
	for _, f := range stored.Files[1:] {
		a := &models.Action{GroupID: stored.ID, GroupFileID: f.ID, VersionKey: f.Version.Key, Title: "The Equalizer 3",
			Paths: []string{f.Version.Parts[0].Path}, Size: f.Version.TotalSize(), Status: models.ActionPending}
		if err := s.h.db.Actions().Create(s.h.ctx, a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}

	s.h.fullScan()
	g := s.h.group(key)
	cs := discFile(t, g)
	if len(g.Files) != 2 || cs.Decision != models.DecisionRemove || cs.Protected || !cs.Version.Disc.Removable {
		t.Fatalf("files %d clip set decision %s protected %v disc %+v", len(g.Files), cs.Decision, cs.Protected, cs.Version.Disc)
	}
	if g.Status == models.GroupQueued {
		t.Fatalf("the old approval carried over: %s (%s)", g.Status, g.StatusReason)
	}
	for _, a := range actions {
		if got, err := s.h.db.Actions().Get(s.h.ctx, a.ID); err != nil || got.Status != models.ActionCancelled {
			t.Fatalf("queued clip removal %d: %+v %v", a.ID, got, err)
		}
	}
	if len(s.h.approvals()) != 0 {
		t.Fatalf("auto approvals %v", s.h.approvals())
	}
}

func TestLooseClipsNewestClipDatesTheSet(t *testing.T) {
	// A clip copied in a minute ago makes the whole set new (minimum age), whatever Plex's addedAt.
	s := newDiscSetup(t, true)
	dir := "/data/movies/Sausage Party (2016)"
	mkv := dir + "/Sausage Party (2016) Remux-2160p.mkv"
	s.file(t, mkv, 30<<20)
	s.file(t, dir+"/00001.m2ts", 2<<20)
	s.file(t, dir+"/00002.m2ts", 1<<20)
	st := s.h.settings()
	st.AllowDiscRemoval, st.RecycleBinPath = true, filepath.Join(s.local, ".dupearr-recycle")
	st.DeletionMethods = []string{models.MethodFilesystem}
	s.h.saveSettings(st)
	s.fp.put(s.lib.SectionKey, movie("9", 223702, "Sausage Party", 2016,
		ver(1, mkv, 30<<20, 3840, func(v *models.MediaVersion) { v.Source = models.SourceRemux }),
		clipVer(2, dir, 1, 2<<20, 1920, 5_000_000), clipVer(3, dir, 2, 1<<20, 1920, 10_000)))
	s.h.now = time.Now()
	s.h.fullScan()
	g := s.h.group("movie:tmdb:223702")
	cs := discFile(t, g)
	if cs.Decision != models.DecisionRemove || !hasFlag(g, models.FlagMinAge) || cs.Version.AddedAt.Before(time.Now().Add(-time.Hour)) {
		t.Fatalf("decision %s flags %v added %s", cs.Decision, g.Flags, cs.Version.AddedAt)
	}
	if err := engine.ValidateDecisions(g); err == nil {
		t.Fatal("a set younger than the minimum age must not be removable yet")
	}
}
