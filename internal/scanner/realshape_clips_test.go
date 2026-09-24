package scanner

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Real-shape loose clip sets (anonymised from a replay of a real library's stored data). A
// seamless-branching disc flattened into its movie folder: ~200 clips, the film split across
// 2–6 minute segments, the rest menus and logos of a few seconds, some clips without video (Plex
// reports no codec for them); no playlist kept. Next to it Plex lists a 1080p WEB-DL.

func TestSplitFeatureClipSetNextToAnMKV(t *testing.T) {
	s := newDiscSetup(t, true)
	st := s.h.settings()
	st.AllowDiscRemoval, st.RecycleBinPath = true, filepath.Join(s.local, ".dupearr-recycle")
	st.DeletionMethods = []string{models.MethodFilesystem}
	s.h.saveSettings(st)

	dir := "/data/movies/Movie A (2023)"
	mkv := dir + "/Movie A (2023) WEBDL-1080p.mkv"
	s.file(t, mkv, 12<<20)
	versions := []models.MediaVersion{ver(1, mkv, 12<<20, 1920, withDuration(6_087_872), func(v *models.MediaVersion) { v.Source = models.SourceWebDL })}
	for i := 0; i < 60; i++ {
		clip := 174 + i*13
		size, dur := int64(1+i%4)<<16, int64(1_000+i*40) // menus, logos: seconds
		switch {
		case i%3 == 0: // feature segments: 2–6 minutes, none feature-length
			size, dur = int64(2+i%5)<<20, int64(120_000+(i%5)*60_000)
		case i%7 == 1: // audio-only menu clips: no video stream
			size, dur = 64<<10, 2_000
		}
		s.file(t, fmt.Sprintf("%s/%05d.m2ts", dir, clip), size)
		v := clipVer(int64(100+i), dir, clip, size, 3840, dur)
		v.DynamicRange = models.DRHDR10
		if i%7 == 1 {
			v.VideoCodec, v.Width, v.Height, v.Resolution = "", 0, 0, ""
		}
		versions = append(versions, v)
	}
	s.fp.put(s.lib.SectionKey, movie("1", 900001, "Movie A", 2023, versions...))
	s.h.now = time.Now().Add(30 * 24 * time.Hour) // the files were just written (minimum age)

	s.h.fullScan()
	g := s.h.group("movie:tmdb:900001")
	if len(g.Files) != 2 {
		t.Fatalf("files %d, want the MKV and one clip set", len(g.Files))
	}
	set := discFile(t, g)
	if set.Version.Disc.Type != models.DiscBlurayClips || set.Version.Disc.ClipCount != 60 || set.Version.DurationMs != 360_000 {
		t.Fatalf("clip set %+v duration %d", set.Version.Disc, set.Version.DurationMs)
	}
	// The complete 2160p disc is not a "sample" of the 1080p WEB-DL: it ranks first and is kept; the
	// WEB-DL stays too (the copy Plex can play). Nothing is proposed for removal.
	for _, r := range set.Reasons {
		if strings.Contains(r, "sample") {
			t.Fatalf("the clip set is judged a sample: %v", set.Reasons)
		}
	}
	if set.Decision != models.DecisionKeep || set.Rank != 1 || hasFlag(g, models.FlagSample) || hasFlag(g, models.FlagUnanalyzed) {
		t.Fatalf("clip set decision %s rank %d flags %v reasons %v", set.Decision, set.Rank, g.Flags, set.Reasons)
	}
	for _, f := range g.Files {
		if f.Decision == models.DecisionRemove {
			t.Fatalf("%s decided remove (status %s)", f.Version.Key, g.Status)
		}
	}
}
