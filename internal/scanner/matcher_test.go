package scanner

import (
	"sync"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

func tracked(path string, size int64, inst, fileID int64) arr.TrackedFile {
	return arr.TrackedFile{Path: path, Size: size, Info: models.ArrFileInfo{InstanceID: inst, FileID: fileID}}
}

func version(paths ...string) *models.MediaVersion {
	v := &models.MediaVersion{Key: "plex:1:1"}
	for _, p := range paths {
		v.Parts = append(v.Parts, models.MediaPart{Path: p, Size: 100})
	}
	return v
}

func TestMatcher(t *testing.T) {
	mappings := []models.PathMapping{
		{SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data/movies", LocalPath: "/mnt/user/movies"},
		{SourceType: models.PathSourceArr, SourceID: 10, RemotePath: "/movies", LocalPath: "/mnt/user/movies"},
		{SourceType: models.PathSourceArr, SourceID: 11, RemotePath: "/movies", LocalPath: "/mnt/other/movies"},
		{SourceType: models.PathSourceServer, SourceID: 2, RemotePath: `D:\Movies`, LocalPath: "/mnt/win/movies"},
		{SourceType: models.PathSourceArr, SourceID: 12, RemotePath: "/win", LocalPath: "/mnt/win/movies"},
		{SourceType: models.PathSourceArr, SourceID: 13, RemotePath: "/data/movies", LocalPath: "/mnt/elsewhere"},
	}
	tests := []struct {
		name     string
		files    map[int64][]arr.TrackedFile // instance → files
		server   int64
		v        *models.MediaVersion
		wantFile int64 // 0 = no match
	}{
		{
			name:     "mapped local path",
			files:    map[int64][]arr.TrackedFile{10: {tracked("/movies/A (2000)/A.mkv", 100, 10, 1)}},
			server:   1,
			v:        version("/data/movies/A (2000)/A.mkv"),
			wantFile: 1,
		},
		{
			name:     "mapped local path, different instance root",
			files:    map[int64][]arr.TrackedFile{11: {tracked("/movies/A (2000)/A.mkv", 100, 11, 2)}},
			server:   1,
			v:        version("/data/movies/A (2000)/A.mkv"),
			wantFile: 0, // /mnt/other/movies/... is a different file; raw paths differ too
		},
		{
			name: "raw path equality without mappings",
			files: map[int64][]arr.TrackedFile{20: {
				tracked("/data/media/movies/B (2001)/B.mkv", 999, 20, 3),
			}},
			server:   3,
			v:        version("/data/media/movies/B (2001)/B.mkv"),
			wantFile: 3,
		},
		{
			name: "raw path equality when only the media server side is mapped",
			files: map[int64][]arr.TrackedFile{11: {
				tracked("/data/movies/A (2000)/A.mkv", 999, 11, 4), // not under /movies: unmapped for 11
			}},
			server:   1,
			v:        version("/data/movies/A (2000)/A.mkv"),
			wantFile: 4,
		},
		{
			name: "raw path equality when only the *arr side is mapped",
			files: map[int64][]arr.TrackedFile{11: {
				tracked("/movies/x.mkv", 999, 11, 5),
			}},
			server:   9,
			v:        version("/movies/x.mkv"),
			wantFile: 5,
		},
		{
			name: "both sides mapped to different local files: no raw or name fallback",
			files: map[int64][]arr.TrackedFile{13: {
				tracked("/data/movies/x.mkv", 100, 13, 12),
			}},
			server:   1,
			v:        version("/data/movies/x.mkv"),
			wantFile: 0,
		},
		{
			name:     "raw windows paths are case-insensitive",
			files:    map[int64][]arr.TrackedFile{20: {tracked(`c:\media\h.mkv`, 55, 20, 13)}},
			server:   3,
			v:        version(`C:\Media\H.mkv`),
			wantFile: 13,
		},
		{
			name: "unique name and size",
			files: map[int64][]arr.TrackedFile{20: {
				tracked(`E:\Films\C (2002)\C (2002).mkv`, 100, 20, 6),
				tracked("/elsewhere/C (2002).mkv", 101, 20, 7), // same name, different size
			}},
			server:   3,
			v:        version("/data/C (2002)/c (2002).MKV"),
			wantFile: 6,
		},
		{
			name: "ambiguous name and size",
			files: map[int64][]arr.TrackedFile{
				20: {tracked("/a/C (2002).mkv", 100, 20, 8)},
				21: {tracked("/b/C (2002).mkv", 100, 21, 9)},
			},
			server:   3,
			v:        version("/data/C (2002)/C (2002).mkv"),
			wantFile: 0,
		},
		{
			name: "same path tracked by two instances: lowest instance wins",
			files: map[int64][]arr.TrackedFile{
				21: {tracked("/data/D.mkv", 100, 21, 30)},
				20: {tracked("/data/D.mkv", 100, 20, 31)},
			},
			server:   3,
			v:        version("/data/D.mkv"),
			wantFile: 31,
		},
		{
			name:     "windows paths are case-insensitive",
			files:    map[int64][]arr.TrackedFile{12: {tracked("/win/E (2004)/E.mkv", 100, 12, 40)}},
			server:   2,
			v:        version(`d:\movies\E (2004)\E.mkv`),
			wantFile: 40,
		},
		{
			name:     "posix paths are case-sensitive",
			files:    map[int64][]arr.TrackedFile{20: {tracked("/data/F.mkv", 55, 20, 50)}},
			server:   3,
			v:        version("/data/f.mkv"),
			wantFile: 0, // sizes differ (100 vs 55), so no name fallback either
		},
		{
			name:     "second part of a stacked version",
			files:    map[int64][]arr.TrackedFile{20: {tracked("/data/G/G cd2.avi", 999, 20, 60)}},
			server:   3,
			v:        version("/data/G/G cd1.avi", "/data/G/G cd2.avi"),
			wantFile: 60,
		},
		{
			name:     "no parts",
			files:    map[int64][]arr.TrackedFile{20: {tracked("/data/H.mkv", 100, 20, 70)}},
			server:   3,
			v:        &models.MediaVersion{},
			wantFile: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMatcher(pathmap.New(mappings))
			for inst, files := range tc.files {
				m.Add(inst, files)
			}
			got := m.Match(tc.server, tc.v)
			switch {
			case tc.wantFile == 0 && got != nil:
				t.Fatalf("matched file %d (%s), want none", got.Info.FileID, got.Path)
			case tc.wantFile != 0 && got == nil:
				t.Fatalf("no match, want file %d", tc.wantFile)
			case tc.wantFile != 0 && got.Info.FileID != tc.wantFile:
				t.Fatalf("matched file %d, want %d", got.Info.FileID, tc.wantFile)
			}
		})
	}
}

func TestMatcherMisc(t *testing.T) {
	m := NewMatcher(nil)
	m.Add(7, []arr.TrackedFile{{Path: "/x/a.mkv", Size: 1}, {Path: "  ", Size: 1}})
	got := m.Match(1, version("/x/a.mkv"))
	if got == nil || got.Info.InstanceID != 7 {
		t.Fatalf("instance id not defaulted: %+v", got)
	}
	got.Path = "mutated"
	if again := m.Match(1, version("/x/a.mkv")); again == nil || again.Path != "/x/a.mkv" {
		t.Fatalf("Match must return a copy")
	}
	var nilM *Matcher
	if nilM.Match(1, version("/x/a.mkv")) != nil || m.Match(1, nil) != nil {
		t.Fatalf("nil inputs must not match")
	}

	// Concurrent use is safe.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			m.Add(int64(100+i), []arr.TrackedFile{{Path: "/y/b.mkv", Size: 2}})
			_ = m.Match(1, version("/y/b.mkv"))
		})
	}
	wg.Wait()
}

func TestApplyArr(t *testing.T) {
	score := 50
	tests := []struct {
		name              string
		v                 models.MediaVersion
		info              models.ArrFileInfo
		wantSource        string
		wantDR            models.DynamicRange
		wantEdition       string
		wantCFScoreCopied bool
	}{
		{
			name:       "fills unknown source, HDR over SDR, edition",
			v:          models.MediaVersion{Source: models.SourceUnknown, DynamicRange: models.DRSDR},
			info:       models.ArrFileInfo{QualitySource: "bluray", QualityModifier: "remux", DynamicRangeType: "HDR10", Edition: "Director's Cut", CustomFormatScore: &score},
			wantSource: models.SourceRemux, wantDR: models.DRHDR10, wantEdition: "Director's Cut", wantCFScoreCopied: true,
		},
		{
			name:       "keeps what Plex knows",
			v:          models.MediaVersion{Source: models.SourceWebDL, DynamicRange: models.DRDolbyVision, Edition: "Extended"},
			info:       models.ArrFileInfo{QualitySource: "bluray", DynamicRangeType: "HDR10", Edition: "Theatrical"},
			wantSource: models.SourceWebDL, wantDR: models.DRDolbyVision, wantEdition: "Extended",
		},
		{
			name:       "SDR per *arr does not override SDR; unknown source stays unknown",
			v:          models.MediaVersion{Source: "", DynamicRange: models.DRSDR},
			info:       models.ArrFileInfo{QualitySource: "cam", DynamicRangeType: ""},
			wantSource: "", wantDR: models.DRSDR,
		},
		{
			name:       "unknown dynamic range takes the *arr value",
			v:          models.MediaVersion{DynamicRange: ""},
			info:       models.ArrFileInfo{DynamicRangeType: "SDR"},
			wantSource: "", wantDR: models.DRSDR,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			tf := &arr.TrackedFile{Info: tc.info}
			applyArr(&v, tf)
			if v.Arr == nil || v.Source != tc.wantSource || v.DynamicRange != tc.wantDR || v.Edition != tc.wantEdition {
				t.Fatalf("got source=%q dr=%q edition=%q arr=%v", v.Source, v.DynamicRange, v.Edition, v.Arr != nil)
			}
			if tc.wantCFScoreCopied {
				*v.Arr.CustomFormatScore = 1
				if *tc.info.CustomFormatScore != 50 {
					t.Fatalf("custom format score aliased")
				}
				*tc.info.CustomFormatScore = 50
			}
			if v.Arr.Tags == nil || v.Arr.EpisodeIDs == nil {
				t.Fatalf("slices must be non-nil")
			}
		})
	}
}
