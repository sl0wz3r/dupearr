package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Shared fixtures and builders for the engine tests.

var (
	tOld = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	tNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
)

const gib = int64(1) << 30

func boolp(b bool) *bool { return &b }
func intp(i int) *int    { return &i }

type vopt func(*models.MediaVersion)

// ver builds a healthy 1080p H.264 WEB-DL version with one part at path.
func ver(mediaID int64, path string, opts ...vopt) models.MediaVersion {
	v := models.MediaVersion{
		Key:          fmt.Sprintf("plex:1:%d", mediaID),
		ServerID:     1,
		LibraryID:    1,
		LibraryTitle: "Movies",
		SectionKey:   "1",
		RatingKey:    "100",
		MediaID:      mediaID,
		ItemTitle:    "Dune",
		Parts:        []models.MediaPart{{ID: mediaID * 10, Path: path, Size: 8 * gib, Duration: 9_300_000}},
		Container:    "mkv",
		DurationMs:   9_300_000,
		BitrateKbps:  8500,
		VideoBitrate: 8000,
		Width:        1920,
		Height:       1080,
		Resolution:   models.Res1080,
		VideoCodec:   models.VCodecH264,
		BitDepth:     8,
		DynamicRange: models.DRSDR,
		AudioTracks:  []models.AudioTrack{{Format: models.AudioEAC3, Channels: 6, LanguageCode: "eng", Default: true}},
		Source:       models.SourceWebDL,
		AddedAt:      tOld,
	}
	for _, o := range opts {
		o(&v)
	}
	return v
}

func withRes(w, h int, tier string) vopt {
	return func(v *models.MediaVersion) { v.Width, v.Height, v.Resolution = w, h, tier }
}
func withCodec(c string) vopt { return func(v *models.MediaVersion) { v.VideoCodec = c } }
func withDR(d models.DynamicRange) vopt {
	return func(v *models.MediaVersion) { v.DynamicRange = d }
}
func withSource(s string) vopt    { return func(v *models.MediaVersion) { v.Source = s } }
func withContainer(c string) vopt { return func(v *models.MediaVersion) { v.Container = c } }
func withSize(n int64) vopt       { return func(v *models.MediaVersion) { v.Parts[0].Size = n } }
func withBitrate(video, overall int) vopt {
	return func(v *models.MediaVersion) { v.VideoBitrate, v.BitrateKbps = video, overall }
}
func withBitDepth(n int) vopt { return func(v *models.MediaVersion) { v.BitDepth = n } }
func withAudio(tracks ...models.AudioTrack) vopt {
	return func(v *models.MediaVersion) { v.AudioTracks = tracks }
}
func withSubs(n int) vopt {
	return func(v *models.MediaVersion) {
		v.SubtitleTracks = make([]models.SubtitleTrack, n)
	}
}
func withAdded(t time.Time) vopt { return func(v *models.MediaVersion) { v.AddedAt = t } }
func withLib(id int64, title string) vopt {
	return func(v *models.MediaVersion) { v.LibraryID, v.LibraryTitle = id, title }
}
func withDuration(ms int64) vopt {
	return func(v *models.MediaVersion) { v.DurationMs = ms; v.Parts[0].Duration = ms }
}
func withEdition(e string) vopt { return func(v *models.MediaVersion) { v.Edition = e } }
func withLocal(p string) vopt   { return func(v *models.MediaVersion) { v.Parts[0].LocalPath = p } }
func withShared(rks ...string) vopt {
	return func(v *models.MediaVersion) { v.Parts[0].SharedWith = rks }
}
func withLinks(n int) vopt    { return func(v *models.MediaVersion) { v.Parts[0].LinkCount = n } }
func withInode(s string) vopt { return func(v *models.MediaVersion) { v.Parts[0].Inode = s } }
func withExists(b bool) vopt  { return func(v *models.MediaVersion) { v.Parts[0].Exists = boolp(b) } }
func withAccessible(b bool) vopt {
	return func(v *models.MediaVersion) { v.Parts[0].Accessible = boolp(b) }
}
func withOptimized() vopt   { return func(v *models.MediaVersion) { v.OptimizedVersion = true } }
func withKey(k string) vopt { return func(v *models.MediaVersion) { v.Key = k } }
func withParts(parts ...models.MediaPart) vopt {
	return func(v *models.MediaVersion) { v.Parts = parts }
}

// withArr marks the version as tracked by an *arr instance.
func withArr(instanceID int64, name string, fileID, itemID int64) vopt {
	return func(v *models.MediaVersion) {
		v.Arr = &models.ArrFileInfo{InstanceID: instanceID, InstanceName: name, Kind: models.ArrRadarr,
			FileID: fileID, ItemID: itemID, Monitored: true}
	}
}

// arrSet mutates the (existing) *arr info.
func arrSet(fn func(a *models.ArrFileInfo)) vopt {
	return func(v *models.MediaVersion) {
		if v.Arr == nil {
			v.Arr = &models.ArrFileInfo{}
		}
		fn(v.Arr)
	}
}

func unanalyzed() vopt {
	return func(v *models.MediaVersion) { v.VideoCodec, v.Width, v.BitrateKbps, v.VideoBitrate = "", 0, 0, 0 }
}

// Realistic fixtures: 4K DV remux, 1080p WEB-DL, 720p Blu-ray encode.
func remux4k(id int64, opts ...vopt) models.MediaVersion {
	return with(ver(id, "/data/movies4k/Dune (2021)/Dune (2021) Remux-2160p.mkv",
		withRes(3840, 1600, models.Res2160), withCodec(models.VCodecHEVC), withDR(models.DRDolbyVisionHDR10),
		withSource(models.SourceRemux), withSize(62*gib), withBitrate(56000, 61000), withBitDepth(10),
		withAudio(models.AudioTrack{Format: models.AudioTrueHDAtmos, Channels: 8, LanguageCode: "eng", Default: true, Atmos: true},
			models.AudioTrack{Format: models.AudioAC3, Channels: 6, LanguageCode: "eng"}),
		withSubs(12)), opts)
}

func web1080(id int64, opts ...vopt) models.MediaVersion {
	return with(ver(id, "/data/movies/Dune (2021)/Dune (2021) WEBDL-1080p.mkv", withSubs(3)), opts)
}

func bd720(id int64, opts ...vopt) models.MediaVersion {
	return with(ver(id, "/data/movies/Dune (2021)/Dune (2021) Bluray-720p.mp4",
		withRes(1280, 536, models.Res720), withSource(models.SourceBluray), withSize(4*gib),
		withBitrate(4000, 4400), withContainer("mp4"),
		withAudio(models.AudioTrack{Format: models.AudioAC3, Channels: 6, LanguageCode: "eng"})), opts)
}

// with applies extra options to a fixture.
func with(v models.MediaVersion, opts []vopt) models.MediaVersion {
	for _, o := range opts {
		o(&v)
	}
	return v
}

// group builds an unevaluated group from versions.
func group(vs ...models.MediaVersion) *models.DuplicateGroup {
	g := &models.DuplicateGroup{Key: "movie:tmdb:438631", MediaType: models.MediaTypeMovie, Title: "Dune",
		Year: 2021, ServerID: 1, Flags: []string{}}
	for _, v := range vs {
		g.Files = append(g.Files, models.GroupFile{Version: v})
	}
	return g
}

func profile(criteria ...models.Criterion) models.Profile {
	return models.Profile{ID: 7, Name: "test", KeepCount: 1, Criteria: criteria}
}

func crit(t models.CriterionType) models.Criterion { return models.Criterion{Type: t, Enabled: true} }

func env() EvalEnv { return EvalEnv{Now: tNow} }

func mustEval(t *testing.T, g *models.DuplicateGroup, p models.Profile, e EvalEnv) *models.DuplicateGroup {
	t.Helper()
	if err := Evaluate(g, p, e); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return g
}

// fileByKey returns the file with the given version key.
func fileByKey(t *testing.T, g *models.DuplicateGroup, key string) *models.GroupFile {
	t.Helper()
	for i := range g.Files {
		if g.Files[i].Version.Key == key {
			return &g.Files[i]
		}
	}
	t.Fatalf("no file %s in group", key)
	return nil
}

// keptKeys returns the version keys with an effective keep decision (file order).
func keptKeys(g *models.DuplicateGroup) []string {
	var out []string
	for _, f := range g.Files {
		if f.Decision == models.DecisionKeep {
			out = append(out, f.Version.Key)
		}
	}
	return out
}

func key(id int64) string { return fmt.Sprintf("plex:1:%d", id) }

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// containsSub reports whether any line contains sub.
func containsSub(list []string, sub string) bool {
	for _, x := range list {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}

func movieItem(rk string, lib int64, ids map[string]string, vs ...models.MediaVersion) models.MediaItem {
	for i := range vs {
		vs[i].RatingKey = rk
		vs[i].LibraryID = lib
		vs[i].LibraryTitle = fmt.Sprintf("Lib %d", lib)
	}
	return models.MediaItem{ServerID: 1, LibraryID: lib, LibraryTitle: fmt.Sprintf("Lib %d", lib), SectionKey: fmt.Sprint(lib),
		RatingKey: rk, MediaType: models.MediaTypeMovie, Title: "Dune", Year: 2021, ExternalIDs: ids,
		Thumb: "/library/metadata/" + rk + "/thumb", AddedAt: tOld, Versions: vs}
}
