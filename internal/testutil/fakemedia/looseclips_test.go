package fakemedia

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// looseFixture returns the clip set fixture of a movie.
func looseFixture(t *testing.T, e *Env, title string) ClipSetFixture {
	t.Helper()
	for _, f := range e.ClipSetFixtures() {
		if f.Title == title {
			return f
		}
	}
	t.Fatalf("no clip set fixture for %q", title)
	return ClipSetFixture{}
}

// clipMedia returns the media of an item whose single part is a clip of the fixture's folder.
func clipMedia(m tMeta, f ClipSetFixture) []tMedia {
	var out []tMedia
	for _, md := range m.Media {
		if len(md.Parts) == 1 && path.Dir(md.Parts[0].File) == f.Folder && IsLooseClipName(path.Base(md.Parts[0].File)) {
			out = append(out, md)
		}
	}
	return out
}

func TestIsLooseClipName(t *testing.T) {
	for name, want := range map[string]bool{
		"00174.m2ts": true, "00004.1.m2ts": true, "44110.m2ts": true, "00001.MTS": true, "00001.m2t": true,
		"VTS_01_1.VOB": true, "vts_02_0.ifo": true, "VIDEO_TS.BUP": true, "VIDEO_TS.VOB": true,
		"Inception (2010) [Remux-1080p].m2ts": false, "Jumanji (1995).ts": false, "0001.m2ts": false,
		"000001.m2ts": false, "00001.ts": false, "00001.mkv": false, "00800.mpls": false, "VTS_1_1.VOB": false,
	} {
		if got := IsLooseClipName(name); got != want {
			t.Errorf("IsLooseClipName(%q) = %t, want %t", name, got, want)
		}
	}
}

// TestLooseClipsPlex: Plex's default scanner lists every loose clip as its own version (one part,
// container "ts") of ONE movie item; unlisted clips and navigation files are on disk only.
func TestLooseClipsPlex(t *testing.T) {
	e := Start(t, LooseClips())
	want := map[string]struct{ clips, listed, others int }{
		LooseBadBoys: {111, 111, 0}, LooseComingToAmerica: {172, 172, 0}, LooseEightCrazyNights: {189, 189, 0},
		LooseElemental: {190, 190, 1}, LooseEqualizer3: {131, 129, 1}, LooseTerminator: {2, 2, 0},
		LooseWillyWonka: {1, 1, 1}, LoosePoltergeist: {62, 62, 1}, LooseLotRReturn: {25, 25, 0}, LooseSandlot: {7, 7, 1},
	}
	fixtures := e.ClipSetFixtures()
	if len(fixtures) != len(want) {
		t.Fatalf("%d clip sets, want %d", len(fixtures), len(want))
	}
	var tiny, small, total int
	for _, f := range fixtures {
		w, ok := want[f.Title]
		if !ok {
			t.Fatalf("unexpected clip set %q", f.Title)
		}
		t.Run(f.Title, func(t *testing.T) {
			if len(f.Clips) != w.clips || len(f.Listed) != w.listed || len(f.MediaIDs) != w.listed {
				t.Fatalf("clips %d listed %d media %d, want %d/%d", len(f.Clips), len(f.Listed), len(f.MediaIDs), w.clips, w.listed)
			}
			if f.RatingKey == "" || f.RatingKey != e.RatingKey(SectionMovies, f.Title) {
				t.Fatalf("rating key %q", f.RatingKey)
			}
			m := detail(t, e, f.RatingKey)
			if len(m.Media) != w.listed+w.others {
				t.Fatalf("Plex lists %d versions, want %d clips + %d other", len(m.Media), w.listed, w.others)
			}
			cm := clipMedia(m, f)
			var ids []int64
			var listed []string
			for _, md := range cm {
				ids = append(ids, md.ID)
				listed = append(listed, path.Base(md.Parts[0].File))
				wantContainer := "ts"
				if f.Kind == ClipSetDVD {
					wantContainer = "mpeg"
				}
				if md.Container != wantContainer || md.Parts[0].Exists == nil || !*md.Parts[0].Exists {
					t.Errorf("clip %s: container %q exists %v", md.Parts[0].File, md.Container, md.Parts[0].Exists)
				}
			}
			slices.Sort(listed)
			if !slices.Equal(ids, f.MediaIDs) || !slices.Equal(listed, f.Listed) {
				t.Errorf("Plex clip media %v %q, fixture %v %q", ids, listed, f.MediaIDs, f.Listed)
			}
			var size int64
			for _, p := range f.Files {
				fi, err := os.Stat(p)
				if err != nil {
					t.Fatal(err)
				}
				size += fi.Size()
				if filepath.Dir(p) != f.LocalFolder {
					t.Errorf("%s is not directly in %s", p, f.LocalFolder)
				}
			}
			if size != f.TotalSize || len(f.Files) != len(f.Clips)+len(f.NavFiles) {
				t.Errorf("files hold %d bytes (%d files), fixture says %d (%d clips + %d nav)", size, len(f.Files), f.TotalSize, len(f.Clips), len(f.NavFiles))
			}
			for _, n := range f.Clips {
				if f.Title == LooseElemental {
					break // its film is split into 145 clips of up to 6 minutes
				}
				tiny += btoi(fileSizeOf(t, filepath.Join(f.LocalFolder, n)) < MiB(1))
				small += btoi(fileSizeOf(t, filepath.Join(f.LocalFolder, n)) < MiB(10))
				total++
			}
		})
	}
	if t.Failed() {
		return
	}
	// Like the real library (720 of 1539 clips below 1 MB, 1311 below 10 MB): about half of the
	// clips below 1 MiB, most below 10 MiB.
	if tiny*100/total < 40 || tiny*100/total > 60 || small*100/total < 75 {
		t.Errorf("%d of %d clips below 1 MiB, %d below 10 MiB", tiny, total, small)
	}

	facts := []struct {
		title, main string
		durMs       int64
	}{
		{LooseBadBoys, "00001.m2ts", Mins(118.8)},
		{LooseComingToAmerica, "00294.m2ts", Mins(116.8)},
		{LooseElemental, "00976.m2ts", 359_000}, // the film is split: the longest clip is 6 min
		{LooseEqualizer3, "00001.m2ts", Mins(102.5)},
		{LooseTerminator, "00010.m2ts", Mins(125.7)},
		{LoosePoltergeist, "00002.m2ts", Mins(114.4)},
		{LooseLotRReturn, "00029.1.m2ts", 30_000}, // no film clip at all
		{LooseSandlot, "VTS_01_1.VOB", Mins(101) * dvdVOBMax / (3*dvdVOBMax + 325_000*dvdSector)},
	}
	for _, c := range facts {
		if f := looseFixture(t, e, c.title); f.MainClip != c.main || f.MainDurationMs != c.durMs {
			t.Errorf("%s: main clip %s (%d ms), want %s (%d ms)", c.title, f.MainClip, f.MainDurationMs, c.main, c.durMs)
		}
	}
	eq := looseFixture(t, e, LooseEqualizer3)
	if !slices.Equal(eq.Unlisted, []string{"00990.m2ts", "00991.m2ts"}) || eq.FeatureDurationMs != Mins(109) ||
		!slices.Equal(eq.NavFiles, []string{"00001.clpi", "00725.clpi", LoosePlaylist, "MovieObject.bdmv", "index.bdmv"}) {
		t.Errorf("Equalizer 3: unlisted %q, playlist %d ms, nav %q", eq.Unlisted, eq.FeatureDurationMs, eq.NavFiles)
	}
	if head := readHead(t, filepath.Join(eq.LocalFolder, LoosePlaylist), 8); string(head) != "MPLS0300" {
		t.Errorf("loose playlist header %q", head)
	}
	if head := readHead(t, filepath.Join(eq.LocalFolder, "index.bdmv"), 8); string(head) != "INDX0300" {
		t.Errorf("loose index.bdmv header %q", head)
	}
	sl := looseFixture(t, e, LooseSandlot)
	if d := sl.FeatureDurationMs - Mins(101); d < -1000 || d > 0 { // the sum of the VOBs' rounded durations
		t.Errorf("Sandlot: main title %d ms, want 101 min", sl.FeatureDurationMs)
	}
	if !slices.Equal(sl.NavFiles, []string{"VIDEO_TS.BUP", "VIDEO_TS.IFO", "VTS_01_0.BUP", "VTS_01_0.IFO", "VTS_02_0.BUP", "VTS_02_0.IFO"}) {
		t.Errorf("Sandlot nav files %q", sl.NavFiles)
	}
	if head := readHead(t, filepath.Join(sl.LocalFolder, "VIDEO_TS.IFO"), 12); string(head) != "DVDVIDEO-VMG" {
		t.Errorf("VIDEO_TS.IFO header %q", head)
	}
	lotr := looseFixture(t, e, LooseLotRReturn)
	if !slices.Contains(lotr.Clips, "00004.1.m2ts") || !slices.Contains(lotr.Clips, "00004.m2ts") {
		t.Errorf("LotR clips %q lack the .1 variants", lotr.Clips)
	}
	// The shared boilerplate clip is byte-for-byte the same size in several folders.
	var shared int
	for _, f := range fixtures {
		if fi, err := os.Stat(filepath.Join(f.LocalFolder, "00102.m2ts")); err == nil && fi.Size() == 8_074_752 {
			shared++
		}
	}
	if shared < 3 {
		t.Errorf("00102.m2ts is in %d folders, want ≥ 3", shared)
	}
	// The control: a full-length .ts is an ordinary version.
	jm := detail(t, e, e.RatingKey(SectionMovies, LooseJumanji))
	if ts := mediaWithFile(t, jm, "Jumanji (1995).ts"); ts.Container != "ts" || IsLooseClipName(path.Base(ts.Parts[0].File)) {
		t.Errorf("Jumanji .ts: container %q", ts.Container)
	}
	var out bytes.Buffer
	e.Describe(&out)
	for _, s := range []string{"Loose clip sets (10;", "bluray_clips", "dvd_clips", "radarr tracks 00002.m2ts", "2 not in Plex"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("Describe lacks %q:\n%s", s, out.String())
		}
	}
	requireNoViolations(t, e)
	e.AssertDiscsIntact(t)
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func fileSizeOf(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// TestLooseClipsArr: Radarr tracks Poltergeist's largest clip (Bluray-2160p by extension and
// width); a rescan of a movie without a file adopts the largest loose clip (the folder carries the
// year, so Radarr parses the clip) — which is how the real library got there.
func TestLooseClipsArr(t *testing.T) {
	e := Start(t, LooseClips())
	pg := looseFixture(t, e, LoosePoltergeist)
	if pg.TrackedBy != InstanceRadarr || e.ArrMovieFilePath(InstanceRadarr, 609) != pg.TrackedPath || !strings.HasSuffix(pg.TrackedPath, "/00002.m2ts") {
		t.Fatalf("Poltergeist: tracked by %q at %q; Radarr tracks %q", pg.TrackedBy, pg.TrackedPath, e.ArrMovieFilePath(InstanceRadarr, 609))
	}
	var mf struct {
		Quality struct {
			Quality struct {
				Name string `json:"name"`
			} `json:"quality"`
		} `json:"quality"`
	}
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 609)), nil, http.StatusOK, &mf)
	if mf.Quality.Quality.Name != "Bluray-2160p" {
		t.Errorf("tracked clip quality %q", mf.Quality.Quality.Name)
	}
	// Bad Boys: Radarr knows the movie but has no file; a rescan adopts the largest clip.
	id := e.ArrMovieID(InstanceRadarr, 9737)
	if id == 0 || e.ArrMovieFileID(InstanceRadarr, 9737) != 0 {
		t.Fatalf("Bad Boys: Radarr movie %d with file %d", id, e.ArrMovieFileID(InstanceRadarr, 9737))
	}
	arrJSON(t, e.Radarr, http.MethodPost, "/api/v3/command", map[string]any{"name": "RescanMovie", "movieId": id}, http.StatusCreated, nil)
	if got := e.ArrMovieFilePath(InstanceRadarr, 9737); !strings.HasSuffix(got, "/Bad Boys (1995)/00001.m2ts") {
		t.Errorf("after a rescan Radarr tracks %q, want the largest clip", got)
	}
	requireNoViolations(t, e)
}

// TestLooseClipsDeleteViolations: deleting a loose clip through Plex or Radarr is a violation and
// leaves the set incomplete; a stale entry (files already moved as a whole) is no violation but
// still listed by ClipDeletes; a whole-set move and restore keeps the set intact and Plex finds
// the restored clips again under new media ids.
func TestLooseClipsDeleteViolations(t *testing.T) {
	t.Run("Plex delete of one clip", func(t *testing.T) {
		e := Start(t, LooseClips())
		f := looseFixture(t, e, LooseBadBoys)
		mid := e.MediaIDForFile(f.RatingKey, "/00001.m2ts")
		if r := plexDo(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", f.RatingKey, mid)); r.Status != http.StatusOK {
			t.Fatalf("delete: %s", r)
		}
		v := requireViolation(t, e, RuleDeleteLooseClip)
		if !strings.Contains(v.Detail, "Bad Boys (1995)/00001.m2ts") {
			t.Errorf("violation %v", v)
		}
		cd := e.ClipDeletes()
		if len(cd) != 1 || !cd[0].Existed || cd[0].Server != ServerPlex || len(cd[0].Files) != 1 {
			t.Errorf("clip deletes %v", cd)
		}
		if p := e.DiscProblems(); len(p) != 1 || !strings.Contains(p[0], "bluray_clips set") || !strings.Contains(p[0], "1 of 111 files missing") {
			t.Errorf("problems %q", p)
		}
	})
	t.Run("Radarr delete of the tracked clip", func(t *testing.T) {
		e := Start(t, LooseClips())
		arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 609)), nil, http.StatusOK, nil)
		requireViolation(t, e, RuleDeleteLooseClip)
		if cd := e.ClipDeletes(); len(cd) != 1 || cd[0].Server != InstanceRadarr || !strings.HasSuffix(cd[0].Files[0], "/00002.m2ts") {
			t.Errorf("clip deletes %v", cd)
		}
	})
	t.Run("Plex delete of a loose VOB", func(t *testing.T) {
		e := Start(t, LooseClips())
		f := looseFixture(t, e, LooseSandlot)
		mid := e.MediaIDForFile(f.RatingKey, "/VTS_01_2.VOB")
		if r := plexDo(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", f.RatingKey, mid)); r.Status != http.StatusOK {
			t.Fatalf("delete: %s", r)
		}
		requireViolation(t, e, RuleDeleteDiscMember)
		if len(e.ClipDeletes()) != 1 || len(e.DiscProblems()) != 1 {
			t.Errorf("clip deletes %v, problems %q", e.ClipDeletes(), e.DiscProblems())
		}
	})
	t.Run("the non-clip version next to clips", func(t *testing.T) {
		e := Start(t, LooseClips())
		f := looseFixture(t, e, LooseWillyWonka)
		mid := e.MediaIDForFile(f.RatingKey, ".mkv")
		if r := plexDo(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", f.RatingKey, mid)); r.Status != http.StatusOK {
			t.Fatalf("delete: %s", r)
		}
		for _, v := range e.Violations() {
			if v.Rule == RuleDeleteLooseClip || v.Rule == RuleDeleteDiscMember {
				t.Errorf("violation %v", v)
			}
		}
		if len(e.ClipDeletes()) != 0 || len(e.DiscProblems()) != 0 {
			t.Errorf("clip deletes %v, problems %q", e.ClipDeletes(), e.DiscProblems())
		}
	})
	t.Run("whole-set move, stale entry, restore", func(t *testing.T) {
		e := Start(t, LooseClips())
		f := looseFixture(t, e, LooseEqualizer3)
		bin := filepath.Join(e.Dir, "bin")
		move := func(from, to string) {
			t.Helper()
			if err := os.MkdirAll(to, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, p := range f.Files {
				if err := os.Rename(filepath.Join(from, filepath.Base(p)), filepath.Join(to, filepath.Base(p))); err != nil {
					t.Fatal(err)
				}
			}
		}
		move(f.LocalFolder, bin)
		if p := e.DiscProblems(); len(p) != 0 {
			t.Fatalf("a set moved as a whole reported: %q", p)
		}
		if !e.FileExists(f.Folder + "/flame-the.equalizer.3.2023.proper.2160p.uhd.bluray.h265.mkv") {
			t.Fatal("the MKV next to the clips is gone")
		}
		refresh := "/library/sections/1/refresh?path=" + url.QueryEscape(f.Folder)
		if r := plexDo(t, e, http.MethodGet, refresh); r.Status != http.StatusOK {
			t.Fatalf("refresh: %s", r)
		}
		mid := f.MediaIDs[0]
		if r := plexDo(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", f.RatingKey, mid)); r.Status != http.StatusOK {
			t.Fatalf("stale delete: %s", r)
		}
		requireNoViolations(t, e)
		if cd := e.ClipDeletes(); len(cd) != 1 || cd[0].Existed {
			t.Errorf("clip deletes %v, want the stale one", cd)
		}
		move(bin, f.LocalFolder)
		if r := plexDo(t, e, http.MethodGet, refresh); r.Status != http.StatusOK {
			t.Fatalf("refresh: %s", r)
		}
		back := looseFixture(t, e, LooseEqualizer3)
		if len(back.MediaIDs) < len(f.Listed) || slices.Contains(back.MediaIDs, mid) {
			t.Errorf("after the restore: media %v (the deleted stale one was %d)", back.MediaIDs, mid)
		}
		e.AssertDiscsIntact(t)
	})
}

func TestLooseClipsValidation(t *testing.T) {
	bd := func(folder string, clips ...Clip) ClipSet {
		return ClipSet{Kind: ClipSetBluray, Folder: folder, Clips: clips}
	}
	clip := func(name string) Clip { return Clip{Name: name, Size: MiB(1), DurationMs: 1_000} }
	movie := func(cs ...ClipSet) Movie {
		return Movie{Section: SectionMovies, Title: "X", Year: 2010, TmdbID: 1, ClipSets: cs}
	}
	tests := []struct {
		name string
		m    Movie
		want string
	}{
		{"kind", movie(ClipSet{Kind: "flat", Folder: "movies/X (2010)", Clips: []Clip{clip("00001.m2ts")}}), "kind must be"},
		{"folder outside the library", movie(bd("tv/X (2010)", clip("00001.m2ts"))), "not a movie folder"},
		{"the library root", movie(bd("movies", clip("00001.m2ts"))), "not a movie folder"},
		{"inside a disc", movie(bd("movies/X (2010)/BDMV/STREAM", clip("00001.m2ts"))), "inside a disc structure"},
		{"no clips", movie(bd("movies/X (2010)")), "at least one clip"},
		{"clip name", movie(bd("movies/X (2010)", clip("X (2010).m2ts"))), "named NNNNN.m2ts"},
		{"VOB in a Blu-ray set", movie(bd("movies/X (2010)", clip("VTS_01_1.VOB"))), "named NNNNN.m2ts"},
		{"IFO in a DVD set", movie(ClipSet{Kind: ClipSetDVD, Folder: "movies/X (2010)", Clips: []Clip{clip("VTS_01_0.IFO")}}), "a DVD clip is a VOB"},
		{"duplicate", movie(bd("movies/X (2010)", clip("00001.m2ts"), clip("00001.M2TS"))), "duplicate name"},
		{"no duration", movie(bd("movies/X (2010)", Clip{Name: "00001.m2ts", Size: 1})), "positive duration"},
		{"unlisted tracked", movie(bd("movies/X (2010)", Clip{Name: "00001.m2ts", Size: 1, DurationMs: 1, Unlisted: true, Tracked: InstanceRadarr})), "unlisted clip cannot be tracked"},
		{"tracked by Sonarr", movie(bd("movies/X (2010)", Clip{Name: "00001.m2ts", Size: 1, DurationMs: 1, Tracked: InstanceSonarr})), "is a sonarr"},
		{"two sets in one folder", movie(bd("movies/X (2010)", clip("00001.m2ts")), bd("movies/X (2010)", clip("00002.m2ts"))), "one set per folder"},
		{"playlist clip missing", movie(ClipSet{Kind: ClipSetBluray, Folder: "movies/X (2010)", Clips: []Clip{clip("00001.m2ts")}, Playlist: []string{"00002.m2ts"}}), "playlist clip"},
		{"NavFiles on a Blu-ray", movie(ClipSet{Kind: ClipSetBluray, Folder: "movies/X (2010)", Clips: []Clip{clip("00001.m2ts")}, NavFiles: true}), "NavFiles is for DVD"},
		{"DVD NavFiles sizes", movie(ClipSet{Kind: ClipSetDVD, Folder: "movies/X (2010)", Clips: []Clip{{Name: "VTS_01_1.VOB", Size: 1000, DurationMs: 1}}, NavFiles: true}), "multiple of 2048"},
		{"a clip declared as a version", Movie{
			Section: SectionMovies, Title: "X", Year: 2010,
			Versions: []Version{{Parts: []Part{{File: "movies/X (2010)/00001.m2ts", Size: MiB(1)}}}},
			ClipSets: []ClipSet{bd("movies/X (2010)", clip("00001.m2ts"))},
		}, "also declared as a version file"},
		{"two tracked files", Movie{
			Section: SectionMovies, Title: "X", Year: 2010, TmdbID: 1,
			Versions: []Version{{Parts: []Part{{File: "movies/X (2010)/X (2010).mkv", Size: MiB(1)}}, Tracked: InstanceRadarr}},
			ClipSets: []ClipSet{bd("movies/X (2010)", Clip{Name: "00001.m2ts", Size: 1, DurationMs: 1, Tracked: InstanceRadarr})},
		}, "one file per movie"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Base("x")
			s.AddMovie(tt.m)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tt.want)
			}
		})
	}
	// A set of unlisted clips only: Plex has no item, the files are there.
	s := Base("x")
	s.AddMovie(movie(bd("movies/X (2010)", Clip{Name: "00001.m2ts", Size: MiB(1), DurationMs: 1_000, Unlisted: true})))
	e := Start(t, s)
	if rk := e.RatingKey(SectionMovies, "X"); rk != "" {
		t.Errorf("an unlisted-only set has a Plex item %s", rk)
	}
	if f := looseFixture(t, e, "X"); f.RatingKey != "" || len(f.Files) != 1 || !e.FileExists(f.Folder+"/00001.m2ts") {
		t.Errorf("fixture %+v", f)
	}
}
