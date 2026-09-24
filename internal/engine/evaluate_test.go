package engine

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

const (
	pathA = "/data/movies/Dune (2021)/Dune (2021) a.mkv"
	pathB = "/data/movies/Dune (2021)/Dune (2021) b.mkv"
)

func withOrder(c models.Criterion, order ...string) models.Criterion { c.Order = order; return c }
func withDir(c models.Criterion, d string) models.Criterion          { c.Direction = d; return c }
func withTol(c models.Criterion, pct, delta float64) models.Criterion {
	c.TolerancePercent, c.MinDelta = pct, delta
	return c
}
func withValue(c models.Criterion, v string) models.Criterion { c.Value = v; return c }
func withPatterns(c models.Criterion, ps ...models.PatternScore) models.Criterion {
	c.Patterns = ps
	return c
}

func tracked(inst int64, name string, fileID int64, score *int) vopt {
	return func(v *models.MediaVersion) {
		v.Arr = &models.ArrFileInfo{InstanceID: inst, InstanceName: name, Kind: models.ArrRadarr, FileID: fileID, ItemID: 9,
			CustomFormatScore: score}
	}
}

func track(format string, ch int, lang string) models.AudioTrack {
	return models.AudioTrack{Format: format, Channels: ch, LanguageCode: lang}
}

func TestEvaluateCriteria(t *testing.T) {
	a := func(opts ...vopt) models.MediaVersion { return ver(1, pathA, opts...) }
	b := func(opts ...vopt) models.MediaVersion { return ver(2, pathB, opts...) }
	res := crit(models.CritResolution)
	dr := crit(models.CritDynamicRange)
	lang := crit(models.CritAudioLanguage)
	fn := crit(models.CritFilenameScore)
	tests := []struct {
		name    string
		c       models.Criterion
		a, b    models.MediaVersion
		want    int64
		decider string
	}{
		// resolution
		{"resolution higher wins", res, a(), b(withRes(3840, 2160, models.Res2160)), 2, "resolution"},
		{"resolution custom order", withOrder(res, "1080", "2160"), a(), b(withRes(3840, 2160, models.Res2160)), 1, "resolution"},
		{"resolution unlisted ranks last", withOrder(res, "2160", "1080"), a(withRes(1280, 720, models.Res720)), b(), 2, "resolution"},
		{"resolution missing ranks below unlisted", withOrder(res, "sd"), a(withRes(0, 0, "")), b(), 2, "resolution"},
		{"resolution from width when tier empty", res, a(withRes(3840, 1600, "")), b(), 1, "resolution"},
		{"resolution 4k alias", res, a(func(v *models.MediaVersion) { v.Resolution = "4K" }), b(), 1, "resolution"},
		{"resolution tie falls through", res, a(), b(), 1, TiebreakMediaID},
		// dynamic range
		{"dynamic range hdr beats sdr", dr, a(), b(withDR(models.DRHDR10)), 2, "dynamic_range"},
		{"dv without fallback below hdr10", dr, a(withDR(models.DRDolbyVision)), b(withDR(models.DRHDR10)), 2, "dynamic_range"},
		{"dv with fallback first", dr, a(withDR(models.DRDolbyVisionHDR10)), b(withDR(models.DRHDR10Plus)), 1, "dynamic_range"},
		{"dynamic range raw alias and custom order", withOrder(dr, "HDR10+", "hdr10"), a(withDR("HDR10+")), b(withDR(models.DRHDR10)), 1, "dynamic_range"},
		{"dynamic range missing", dr, a(withDR("")), b(), 2, "dynamic_range"},
		// source
		{"source remux beats webdl", crit(models.CritSource), a(), b(withSource(models.SourceRemux)), 2, "source"},
		{"source alias web-dl", withOrder(crit(models.CritSource), "webdl", "bluray"), a(withSource("WEB-DL")), b(withSource("Blu-ray")), 1, "source"},
		// video codec
		{"codec default order prefers hevc", crit(models.CritVideoCodec), a(), b(withCodec(models.VCodecHEVC)), 2, "video_codec"},
		{"codec raw names normalized", withOrder(crit(models.CritVideoCodec), "h264", "hevc"), a(withCodec("x265")), b(withCodec("AVC")), 2, "video_codec"},
		// audio format
		{"audio format uses best track", crit(models.CritAudioFormat),
			a(withAudio(models.AudioTrack{Format: models.AudioAAC, Channels: 2, Default: true}, track(models.AudioTrueHDAtmos, 8, "eng"))),
			b(withAudio(models.AudioTrack{Format: models.AudioDTSHDMA, Channels: 8, Default: true})), 1, "audio_format"},
		{"audio format atmos flag folds", crit(models.CritAudioFormat),
			a(withAudio(models.AudioTrack{Format: "TrueHD", Channels: 8, Atmos: true})), b(withAudio(track(models.AudioTrueHD, 8, "eng"))), 1, "audio_format"},
		{"audio format eac3 atmos flag folds", crit(models.CritAudioFormat),
			a(withAudio(track(models.AudioEAC3, 6, "eng"))), b(withAudio(models.AudioTrack{Format: models.AudioEAC3, Channels: 6, Atmos: true})), 2, "audio_format"},
		{"audio format missing", crit(models.CritAudioFormat), a(withAudio()), b(), 2, "audio_format"},
		{"audio format unknown ranks last", crit(models.CritAudioFormat), a(withAudio(track("weird", 8, ""))), b(withAudio(track(models.AudioMP3, 2, ""))), 2, "audio_format"},
		{"audio format custom order", withOrder(crit(models.CritAudioFormat), "ac3", "truehd"), a(withAudio(track(models.AudioTrueHD, 8, ""))), b(withAudio(track(models.AudioAC3, 6, ""))), 2, "audio_format"},
		// container
		{"container mkv beats mp4", crit(models.CritContainer), a(withContainer("mp4")), b(), 2, "container"},
		{"container from extension", crit(models.CritContainer),
			ver(1, "/m/Dune (2021)/a.avi", withContainer("")), ver(2, "/m/Dune (2021)/b.MKV", withContainer("")), 2, "container"},
		{"container ts last", crit(models.CritContainer), a(withContainer("mpegts")), b(withContainer("avi")), 2, "container"},
		{"container unknown maps to other", crit(models.CritContainer), a(withContainer("wmv")), b(withContainer("avi")), 1, "container"},
		// library
		{"library order", withOrder(crit(models.CritLibrary), "2", "1"), a(), b(withLib(2, "Movies 4K")), 2, "library"},
		{"library unlisted tie", withOrder(crit(models.CritLibrary), "3"), a(), b(withLib(2, "Movies 4K")), 1, TiebreakMediaID},
		{"library unknown id ranks last", withOrder(crit(models.CritLibrary), "1"), a(withLib(0, "")), b(), 2, "library"},
		// audio channels
		{"channels higher", crit(models.CritAudioChannels), a(), b(withAudio(track(models.AudioEAC3, 8, "eng"))), 2, "audio_channels"},
		{"channels max over tracks", crit(models.CritAudioChannels), a(withAudio(track(models.AudioAAC, 2, ""), track(models.AudioDTS, 8, ""))), b(), 1, "audio_channels"},
		{"channels lower", withDir(crit(models.CritAudioChannels), models.DirectionLower), a(), b(withAudio(track(models.AudioEAC3, 8, "eng"))), 1, "audio_channels"},
		{"channels missing ranks below even when lower", withDir(crit(models.CritAudioChannels), models.DirectionLower), a(withAudio()), b(withAudio(track(models.AudioEAC3, 8, ""))), 2, "audio_channels"},
		// video bitrate
		{"bitrate higher same codec", crit(models.CritVideoBitrate), a(), b(withBitrate(12000, 12500)), 2, "video_bitrate"},
		{"bitrate within tolerance ties", withTol(crit(models.CritVideoBitrate), 15, 0), a(), b(withBitrate(9000, 9500)), 1, TiebreakMediaID},
		{"bitrate beyond tolerance", withTol(crit(models.CritVideoBitrate), 15, 0), a(), b(withBitrate(9500, 9900)), 2, "video_bitrate"},
		{"bitrate different codec ties", crit(models.CritVideoBitrate), a(), b(withCodec(models.VCodecHEVC), withBitrate(20000, 21000)), 1, TiebreakMediaID},
		{"bitrate overall fallback", crit(models.CritVideoBitrate), a(withBitrate(0, 8500)), b(withBitrate(0, 12000)), 2, "video_bitrate"},
		{"bitrate missing ranks below", crit(models.CritVideoBitrate), a(withBitrate(0, 0)), b(), 2, "video_bitrate"},
		{"bitrate min delta", withTol(crit(models.CritVideoBitrate), 0, 5000), a(), b(withBitrate(12000, 12000)), 1, TiebreakMediaID},
		// file size
		{"size larger", crit(models.CritFileSize), a(), b(withSize(10 * gib)), 2, "file_size"},
		// Within the profile tolerance the final tiebreak (larger size, exact) still decides.
		{"size within tolerance falls to tiebreak", withTol(crit(models.CritFileSize), 5, 0), a(), b(withSize(8*gib + 300<<20)), 2, "file_size"},
		{"size equal falls to media id", withTol(crit(models.CritFileSize), 5, 0), a(), b(), 1, TiebreakMediaID},
		{"size lower", withDir(crit(models.CritFileSize), models.DirectionLower), a(), b(withSize(6 * gib)), 2, "file_size"},
		{"size lower with tolerance falls to tiebreak", withTol(withDir(crit(models.CritFileSize), models.DirectionLower), 30, 0), a(), b(withSize(6 * gib)), 1, "file_size"},
		{"size missing ranks below", withDir(crit(models.CritFileSize), models.DirectionLower), a(withSize(0)), b(), 2, "file_size"},
		// bit depth
		{"bit depth higher", crit(models.CritBitDepth), a(), b(withBitDepth(10)), 2, "bit_depth"},
		{"bit depth missing ranks below", withDir(crit(models.CritBitDepth), models.DirectionLower), a(withBitDepth(0)), b(withBitDepth(10)), 2, "bit_depth"},
		// custom format score
		{"cf score same instance", crit(models.CritCustomFormatScore), a(tracked(1, "Radarr", 10, intp(100))), b(tracked(1, "Radarr", 11, intp(150))), 2, "custom_format_score"},
		{"cf score min delta", withTol(crit(models.CritCustomFormatScore), 0, 10), a(tracked(1, "Radarr", 10, intp(100))), b(tracked(1, "Radarr", 11, intp(105))), 1, TiebreakMediaID},
		{"cf score negative values", crit(models.CritCustomFormatScore), a(tracked(1, "Radarr", 10, intp(-10000))), b(tracked(1, "Radarr", 11, intp(-50))), 2, "custom_format_score"},
		{"cf score different instances tie", crit(models.CritCustomFormatScore), a(tracked(1, "Radarr", 10, intp(100))), b(tracked(2, "Radarr 4K", 11, intp(900))), 1, TiebreakMediaID},
		{"cf score unknown ties", crit(models.CritCustomFormatScore), a(tracked(1, "Radarr", 10, nil)), b(tracked(1, "Radarr", 11, intp(900))), 1, TiebreakMediaID},
		{"cf score untracked ties then arr tiebreak", crit(models.CritCustomFormatScore), a(), b(tracked(1, "Radarr", 11, intp(900))), 2, "arr_managed"},
		// date added
		{"date added newer", crit(models.CritDateAdded), a(), b(withAdded(tOld.Add(24 * time.Hour))), 2, "date_added"},
		{"date added prefers arr date", crit(models.CritDateAdded),
			a(tracked(1, "Radarr", 10, nil), arrSet(func(x *models.ArrFileInfo) { x.DateAdded = tOld.Add(240 * time.Hour) })),
			b(withAdded(tOld.Add(24 * time.Hour))), 1, "date_added"},
		{"date added older", withDir(crit(models.CritDateAdded), models.DirectionLower), a(), b(withAdded(tOld.Add(24 * time.Hour))), 1, "date_added"},
		{"date added missing", crit(models.CritDateAdded), a(withAdded(time.Time{})), b(), 2, "date_added"},
		// track counts
		{"audio track count", crit(models.CritAudioTrackCount), a(), b(withAudio(track(models.AudioEAC3, 6, "eng"), track(models.AudioAC3, 6, "ger"))), 2, "audio_track_count"},
		{"subtitle track count", crit(models.CritSubtitleTrackCount), a(), b(withSubs(2)), 2, "subtitle_track_count"},
		// arr managed
		{"arr managed", crit(models.CritArrManaged), a(), b(tracked(1, "Radarr", 11, nil)), 2, "arr_managed"},
		{"arr managed specific instance", withValue(crit(models.CritArrManaged), "5"), a(tracked(3, "Radarr", 10, nil)), b(tracked(5, "Radarr 4K", 11, nil)), 2, "arr_managed"},
		{"arr managed instance by name", withValue(crit(models.CritArrManaged), "radarr 4k"), a(tracked(3, "Radarr", 10, nil)), b(tracked(5, "Radarr 4K", 11, nil)), 2, "arr_managed"},
		// audio language
		{"audio language present", withValue(lang, "ger"), a(), b(withAudio(track(models.AudioEAC3, 6, "eng"), track(models.AudioAC3, 6, "deu"))), 2, "audio_language"},
		{"audio language 639-1 value", withValue(lang, "de"), a(), b(withAudio(models.AudioTrack{Format: models.AudioAC3, Language: "German"})), 2, "audio_language"},
		{"audio language unknown ranks below absent", withValue(lang, "ger"), a(withAudio(track(models.AudioAC3, 6, ""))), b(), 2, "audio_language"},
		{"audio language without value skipped", lang, a(), b(withAudio(track(models.AudioAC3, 6, "ger"))), 1, TiebreakMediaID},
		// filename score
		{"filename glob on file name", withPatterns(fn, models.PatternScore{Pattern: "*Remux*", Score: 100}),
			a(), ver(2, "/data/movies/Dune (2021)/Dune (2021) Remux-2160p.mkv"), 2, "filename_score"},
		{"filename glob case-insensitive by default", withPatterns(fn, models.PatternScore{Pattern: "*REMUX*", Score: 100}),
			a(), ver(2, "/data/movies/Dune (2021)/Dune (2021) Remux-2160p.mkv"), 2, "filename_score"},
		{"filename glob case-sensitive", withPatterns(fn, models.PatternScore{Pattern: "*REMUX*", Score: 100, CaseSensitive: true}),
			a(), ver(2, "/data/movies/Dune (2021)/Dune (2021) Remux-2160p.mkv"), 1, TiebreakMediaID},
		{"filename glob on full path", withPatterns(fn, models.PatternScore{Pattern: "/data/movies4k/**", Score: 10}),
			a(), ver(2, "/data/movies4k/Dune (2021)/b.mkv"), 2, "filename_score"},
		{"filename relative glob matches at any depth", withPatterns(fn, models.PatternScore{Pattern: "movies4k/**/*.mkv", Score: 10}),
			a(), ver(2, "/data/movies4k/Dune (2021)/b.mkv"), 2, "filename_score"},
		{"filename regex", withPatterns(fn, models.PatternScore{Pattern: `\bremux\b`, Score: 50, Regex: true}),
			a(), ver(2, "/data/movies/Dune (2021)/Dune.2021.REMUX.mkv"), 2, "filename_score"},
		{"filename regex case-sensitive", withPatterns(fn, models.PatternScore{Pattern: `remux`, Score: 50, Regex: true, CaseSensitive: true}),
			a(), ver(2, "/data/movies/Dune (2021)/Dune.2021.REMUX.mkv"), 1, TiebreakMediaID},
		{"filename negative score", withPatterns(fn, models.PatternScore{Pattern: "*.ts", Score: -1000}),
			ver(1, "/data/movies/Dune (2021)/a.ts"), b(), 2, "filename_score"},
		{"filename scores sum", withPatterns(fn, models.PatternScore{Pattern: "*Remux*", Score: 100}, models.PatternScore{Pattern: "*FraMeSToR*", Score: 50}),
			ver(1, "/m/Dune (2021)/Dune Remux FraMeSToR.mkv"), ver(2, "/m/Dune (2021)/Dune Remux-2160p.mkv"), 1, "filename_score"},
		{"filename invalid patterns ignored", withPatterns(fn, models.PatternScore{Pattern: "(", Regex: true, Score: 5}, models.PatternScore{Pattern: "[", Score: 5}, models.PatternScore{Pattern: " ", Score: 5}),
			a(), ver(2, "/m/(/[.mkv"), 1, TiebreakMediaID},
		{"filename lower direction", withDir(withPatterns(fn, models.PatternScore{Pattern: "*Remux*", Score: 100}), models.DirectionLower),
			a(), ver(2, "/data/movies/Dune (2021)/Dune (2021) Remux-2160p.mkv"), 1, "filename_score"},
		// health
		{"health beats resolution", crit(models.CritHealth), a(withRes(3840, 2160, models.Res2160), unanalyzed()), b(), 2, "health"},
		{"health sample duration", crit(models.CritHealth), a(withDuration(1_000_000)), b(), 2, "health"},
		{"health sample name", crit(models.CritHealth), ver(1, "/m/Dune (2021)/dune-sample.mkv"), b(), 2, "health"},
		{"health inaccessible", crit(models.CritHealth), a(withAccessible(false)), b(), 2, "health"},
		// disabled / unknown
		{"disabled criterion skipped", models.Criterion{Type: models.CritResolution}, a(), b(withRes(3840, 2160, models.Res2160)), 1, TiebreakMediaID},
		{"unknown criterion skipped", crit("bogus"), a(), b(withRes(3840, 2160, models.Res2160)), 1, TiebreakMediaID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := mustEval(t, group(tc.a, tc.b), profile(tc.c), env())
			if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(tc.want)}) {
				t.Fatalf("kept %v, want %s\nreasons: %v / %v", got, key(tc.want), g.Files[0].Reasons, g.Files[1].Reasons)
			}
			loser := int64(1)
			if tc.want == 1 {
				loser = 2
			}
			lf := fileByKey(t, g, key(loser))
			if lf.DecidingCriterion != tc.decider {
				t.Fatalf("deciding criterion = %q, want %q (reasons %v)", lf.DecidingCriterion, tc.decider, lf.Reasons)
			}
			kf := fileByKey(t, g, key(tc.want))
			if kf.DecidingCriterion != tc.decider {
				t.Fatalf("keeper deciding criterion = %q, want %q (reasons %v)", kf.DecidingCriterion, tc.decider, kf.Reasons)
			}
			if kf.Rank != 1 || lf.Rank != 2 {
				t.Fatalf("ranks keeper %d loser %d", kf.Rank, lf.Rank)
			}
			if err := ValidateDecisions(g); err != nil {
				t.Fatalf("ValidateDecisions: %v", err)
			}
		})
	}
}

func TestEvaluateTiebreak(t *testing.T) {
	tests := []struct {
		name    string
		a, b    models.MediaVersion
		want    string
		decider string
	}{
		{"arr managed first", ver(1, pathA), ver(2, pathB, tracked(1, "Radarr", 5, nil)), key(2), "arr_managed"},
		{"larger size", ver(1, pathA), ver(2, pathB, withSize(9*gib)), key(2), "file_size"},
		{"older added", ver(1, pathA, withAdded(tOld.Add(time.Hour))), ver(2, pathB), key(2), "date_added"},
		{"unknown added ranks below", ver(1, pathA, withAdded(time.Time{})), ver(2, pathB), key(2), "date_added"},
		{"lower media id", ver(2, pathA), ver(1, pathB), key(1), TiebreakMediaID},
		{"smaller key", ver(0, pathA, withKey("plex:1:b")), ver(0, pathB, withKey("plex:1:a")), "plex:1:a", TiebreakKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := mustEval(t, group(tc.a, tc.b), profile(), env())
			if got := keptKeys(g); !reflect.DeepEqual(got, []string{tc.want}) {
				t.Fatalf("kept %v, want %s", got, tc.want)
			}
			for _, f := range g.Files {
				if f.DecidingCriterion != tc.decider {
					t.Fatalf("%s decided by %q, want %q (%v)", f.Version.Key, f.DecidingCriterion, tc.decider, f.Reasons)
				}
			}
		})
	}
}

func TestEvaluateReasons(t *testing.T) {
	t.Run("resolution", func(t *testing.T) {
		g := mustEval(t, group(web1080(2), remux4k(1)), profile(crit(models.CritResolution)), env())
		if got := fileByKey(t, g, key(1)).Reasons; got[0] != "Keep — best by Resolution (2160p vs 1080p)" {
			t.Fatalf("keeper reasons = %v", got)
		}
		if got := fileByKey(t, g, key(2)).Reasons; got[0] != "Remove — lower Resolution: 1080p vs 2160p" {
			t.Fatalf("loser reasons = %v", got)
		}
	})
	t.Run("tiebreak", func(t *testing.T) {
		g := mustEval(t, group(ver(1, pathA), ver(2, pathB)), profile(crit(models.CritResolution)), env())
		if got := fileByKey(t, g, key(1)).Reasons[0]; got != "Keep — best: tied on all profile criteria; won the tiebreak on Plex media id (1 vs 2)" {
			t.Fatalf("keeper reason = %q", got)
		}
		if got := fileByKey(t, g, key(2)).Reasons[0]; got != "Remove — tied on all profile criteria; tiebreak: higher Plex media id: 2 vs 1" {
			t.Fatalf("loser reason = %q", got)
		}
	})
	t.Run("key", func(t *testing.T) {
		g := mustEval(t, group(ver(0, pathA, withKey("plex:1:b")), ver(0, pathB, withKey("plex:1:a"))), profile(), env())
		if got := fileByKey(t, g, "plex:1:a").Reasons[0]; got != "Keep — best: tied on every criterion with plex:1:b; first by version key" {
			t.Fatalf("keeper reason = %q", got)
		}
		if got := fileByKey(t, g, "plex:1:b").Reasons[0]; got != "Remove — tied on every criterion with plex:1:a; ordered by version key" {
			t.Fatalf("loser reason = %q", got)
		}
	})
	tests := []struct {
		name string
		c    models.Criterion
		a, b models.MediaVersion
		want string // loser's first reason
	}{
		{"missing value", crit(models.CritResolution), ver(1, pathA, withRes(0, 0, "")), ver(2, pathB), "Remove — Resolution unknown (vs 1080p)"},
		{"health", crit(models.CritHealth), ver(1, pathA, unanalyzed()), ver(2, pathB), "Remove — unhealthy: no video codec, width unknown, bitrate unknown"},
		{"arr any", crit(models.CritArrManaged), ver(1, pathA), ver(2, pathB, tracked(1, "Radarr", 5, nil)), "Remove — not managed by an *arr (Radarr tracks the other version)"},
		{"arr instance", withValue(crit(models.CritArrManaged), "5"), ver(1, pathA, tracked(1, "Radarr", 5, nil)), ver(2, pathB, tracked(5, "Radarr 4K", 6, nil)),
			"Remove — not tracked by *arr instance 5"},
		{"language", withValue(crit(models.CritAudioLanguage), "ger"), ver(1, pathA), ver(2, pathB, withAudio(track(models.AudioAC3, 6, "ger"))), "Remove — no ger audio track"},
		{"size", withDir(crit(models.CritFileSize), models.DirectionLower), ver(1, pathA), ver(2, pathB, withSize(4*gib)), "Remove — larger File size: 8.0 GiB vs 4.0 GiB"},
		{"date", crit(models.CritDateAdded), ver(1, pathA), ver(2, pathB, withAdded(tOld.Add(48*time.Hour))), "Remove — older Date added: 2024-01-01 vs 2024-01-03"},
		{"arr tiebreak", crit(models.CritResolution), ver(1, pathA), ver(2, pathB, tracked(1, "Radarr", 5, nil)),
			"Remove — tied on all profile criteria; tiebreak: not managed by an *arr (Radarr tracks the other version)"},
		{"audio", crit(models.CritAudioFormat), ver(1, pathA), ver(2, pathB, withAudio(track(models.AudioTrueHDAtmos, 8, "eng"))), "Remove — lower Audio format: E-AC-3 5.1 vs TrueHD Atmos 7.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := mustEval(t, group(tc.a, tc.b), profile(tc.c), env())
			var loser *models.GroupFile
			for i := range g.Files {
				if g.Files[i].Decision == models.DecisionRemove {
					loser = &g.Files[i]
				}
			}
			if loser == nil || loser.Reasons[0] != tc.want {
				t.Fatalf("loser reasons = %v, want first %q", loser, tc.want)
			}
		})
	}
}

func TestEvaluateKeepPerAndKeepCount(t *testing.T) {
	web4k := ver(2, "/data/movies4k/Dune (2021)/Dune (2021) WEBDL-2160p.mkv", withRes(3840, 1600, models.Res2160),
		withCodec(models.VCodecHEVC), withDR(models.DRDolbyVisionHDR10), withSize(20*gib), withBitrate(18000, 19000))
	bd1080 := ver(4, "/data/movies/Dune (2021)/Dune (2021) Bluray-1080p.mkv", withSource(models.SourceBluray), withSize(12*gib))
	hq := ProfileTemplates()[0]

	t.Run("keep per resolution", func(t *testing.T) {
		p := hq
		p.KeepPer = models.KeepPerResolution
		g := mustEval(t, group(remux4k(1), web4k, web1080(3), bd1080, bd720(5)), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(4), key(5)}) {
			t.Fatalf("kept %v", got)
		}
		if got := fileByKey(t, g, key(2)).Reasons[0]; got != "Remove — lower Source: WEB-DL vs Remux (within 2160p)" {
			t.Fatalf("reason = %q", got)
		}
		if got := fileByKey(t, g, key(1)).Reasons[0]; got != "Keep — best 2160p version by Source (Remux vs WEB-DL)" {
			t.Fatalf("reason = %q", got)
		}
		if got := fileByKey(t, g, key(5)).Reasons[0]; got != "Keep — only 720p version" {
			t.Fatalf("reason = %q", got)
		}
		if f := fileByKey(t, g, key(3)); f.DecidingCriterion != "source" || f.Rank != 4 {
			t.Fatalf("1080p web: decided by %q rank %d", f.DecidingCriterion, f.Rank)
		}
		if g.Status != models.GroupPending {
			t.Fatalf("status %s (%s)", g.Status, g.StatusReason)
		}
	})

	t.Run("keep per dynamic range", func(t *testing.T) {
		p := hq
		p.KeepPer = models.KeepPerDynamicRange
		g := mustEval(t, group(remux4k(1), web4k, web1080(3), bd1080), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(4)}) {
			t.Fatalf("kept %v", got)
		}
	})

	t.Run("keep per unknown partition", func(t *testing.T) {
		p := profile(crit(models.CritFileSize))
		p.KeepPer = models.KeepPerResolution
		g := mustEval(t, group(ver(1, pathA, withRes(0, 0, "")), ver(2, pathB, withRes(0, 0, ""), withSize(9*gib)), web1080(3)), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(2), key(3)}) {
			t.Fatalf("kept %v", got)
		}
		if got := fileByKey(t, g, key(1)).Reasons[0]; !strings.Contains(got, "(within unknown-resolution)") {
			t.Fatalf("reason = %q", got)
		}
		p.KeepPer = models.KeepPerDynamicRange
		g = mustEval(t, group(ver(1, pathA, withDR("")), ver(2, pathB, withDR(""), withSize(9*gib)), web1080(3)), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(2), key(3)}) {
			t.Fatalf("kept %v", got)
		}
	})

	t.Run("unknown keep per means none", func(t *testing.T) {
		p := hq
		p.KeepPer = "bogus"
		g := mustEval(t, group(remux4k(1), web1080(3), bd720(5)), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) {
			t.Fatalf("kept %v", got)
		}
	})

	t.Run("keep count", func(t *testing.T) {
		p := hq
		p.KeepCount = 2
		g := mustEval(t, group(remux4k(1), web1080(3), bd720(5)), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(3)}) {
			t.Fatalf("kept %v", got)
		}
		if got := fileByKey(t, g, key(3)).Reasons[0]; got != "Keep — ranked #2 (keep count 2)" {
			t.Fatalf("reason = %q", got)
		}
		p.KeepCount = 0 // invalid → treated as 1
		g = mustEval(t, group(remux4k(1), web1080(3), bd720(5)), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) {
			t.Fatalf("kept %v", got)
		}
	})

	t.Run("keep count per partition", func(t *testing.T) {
		p := hq
		p.KeepCount = 2
		p.KeepPer = models.KeepPerResolution
		g := mustEval(t, group(remux4k(1), web4k, web1080(3), bd1080, ver(6, pathA), bd720(5)), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2), key(3), key(4), key(5)}) {
			t.Fatalf("kept %v", got)
		}
		if got := fileByKey(t, g, key(3)).Reasons[0]; !strings.HasPrefix(got, "Keep — ranked #2 among 1080p versions") {
			t.Fatalf("reason = %q", got)
		}
	})
}

func TestEvaluateProtections(t *testing.T) {
	saveSpace := profile(withDir(crit(models.CritFileSize), models.DirectionLower))
	build := func(opts ...vopt) *models.DuplicateGroup {
		r := remux4k(1)
		for _, o := range opts {
			o(&r)
		}
		return group(r, web1080(2), bd720(3))
	}
	libs := map[int64]models.Library{2: {ID: 2, Title: "Movies 4K"}}
	tests := []struct {
		name   string
		prot   models.Protection
		opts   []vopt
		reason string // "" = not protected
	}{
		{"path glob", models.Protection{Type: models.ProtectPathGlob, Value: "/data/movies4k/**"}, nil, "path matches /data/movies4k/**"},
		{"path glob case-insensitive", models.Protection{Type: models.ProtectPathGlob, Value: "/DATA/Movies4K/**"}, nil, "path matches /DATA/Movies4K/**"},
		{"path glob literal prefix", models.Protection{Type: models.ProtectPathGlob, Value: "/data/movies4k"}, nil, "path matches /data/movies4k"},
		{"path glob literal prefix is segment aware", models.Protection{Type: models.ProtectPathGlob, Value: "/data/movies4"}, nil, ""},
		{"path glob file name", models.Protection{Type: models.ProtectPathGlob, Value: "*Remux*"}, nil, "path matches *Remux*"},
		{"path glob on local path", models.Protection{Type: models.ProtectPathGlob, Value: "/mnt/user/**/keep/*"},
			[]vopt{withLocal("/mnt/user/data/keep/Dune.mkv")}, "path matches /mnt/user/**/keep/*"},
		{"path glob windows path", models.Protection{Type: models.ProtectPathGlob, Value: `D:\Movies4K\**`},
			[]vopt{func(v *models.MediaVersion) { v.Parts[0].Path = `D:\Movies4K\Dune\Dune.mkv` }}, `path matches D:\Movies4K\**`},
		{"path glob no match", models.Protection{Type: models.ProtectPathGlob, Value: "/other/**"}, nil, ""},
		{"path glob invalid", models.Protection{Type: models.ProtectPathGlob, Value: "/data/[movies"}, nil, ""},
		{"library id", models.Protection{Type: models.ProtectLibrary, Value: "2"}, []vopt{withLib(2, "")}, "in library Movies 4K"},
		{"library title", models.Protection{Type: models.ProtectLibrary, Value: "movies 4k"}, []vopt{withLib(2, "Movies 4K")}, "in library Movies 4K"},
		{"library other", models.Protection{Type: models.ProtectLibrary, Value: "3"}, []vopt{withLib(2, "")}, ""},
		{"arr instance id", models.Protection{Type: models.ProtectArrInstance, Value: "5"}, []vopt{tracked(5, "Radarr 4K", 9, nil)}, "tracked by Radarr 4K"},
		{"arr instance other", models.Protection{Type: models.ProtectArrInstance, Value: "6"}, []vopt{tracked(5, "Radarr 4K", 9, nil)}, ""},
		{"arr tag case-insensitive", models.Protection{Type: models.ProtectArrTag, Value: "DupeArr-Keep"},
			[]vopt{tracked(5, "Radarr 4K", 9, nil), arrSet(func(a *models.ArrFileInfo) { a.Tags = []string{"4k", "dupearr-keep"} })}, "*arr tag DupeArr-Keep"},
		{"arr tag untracked", models.Protection{Type: models.ProtectArrTag, Value: "dupearr-keep"}, nil, ""},
		{"empty value ignored", models.Protection{Type: models.ProtectPathGlob, Value: " "}, nil, ""},
		{"unknown type ignored", models.Protection{Type: "bogus", Value: "*"}, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := saveSpace
			p.Protections = []models.Protection{tc.prot}
			e := env()
			e.Libraries = libs
			g := mustEval(t, build(tc.opts...), p, e)
			f := fileByKey(t, g, key(1))
			if tc.reason == "" {
				if f.Protected || f.Decision != models.DecisionRemove {
					t.Fatalf("unexpectedly protected: %+v", f)
				}
				return
			}
			if !f.Protected || f.Decision != models.DecisionKeep || f.EngineDecision != models.DecisionKeep || f.ProtectedReason != tc.reason {
				t.Fatalf("protected=%v decision=%s reason=%q, want %q", f.Protected, f.Decision, f.ProtectedReason, tc.reason)
			}
			if f.Reasons[0] != "Protected — "+tc.reason || !strings.HasPrefix(f.Reasons[1], "Would otherwise be removed — larger File size: 62.0 GiB vs 4.0 GiB") {
				t.Fatalf("reasons = %v", f.Reasons)
			}
			if f.Rank != 3 {
				t.Fatalf("protected version must still be ranked: rank %d", f.Rank)
			}
			if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(3)}) {
				t.Fatalf("kept %v", got)
			}
			if g.Status != models.GroupPending {
				t.Fatalf("status %s", g.Status)
			}
		})
	}

	t.Run("protected loser does not displace the best", func(t *testing.T) {
		p := ProfileTemplates()[0]
		p.Protections = append(p.Protections, models.Protection{Type: models.ProtectPathGlob, Value: "**/*WEBDL*"})
		g := mustEval(t, group(remux4k(1), web1080(2), bd720(3)), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
			t.Fatalf("kept %v", got)
		}
		f := fileByKey(t, g, key(2))
		if !f.Protected || f.Rank != 2 || f.DecidingCriterion != "resolution" {
			t.Fatalf("file 2: %+v", f)
		}
	})

	t.Run("protected best keeps both reasons", func(t *testing.T) {
		p := ProfileTemplates()[0]
		g := mustEval(t, group(remux4k(1), web1080(2)), p, env())
		g.Files[0].Version.Arr = &models.ArrFileInfo{InstanceID: 1, InstanceName: "Radarr", Tags: []string{DefaultKeepTag}}
		mustEval(t, g, p, env())
		f := fileByKey(t, g, key(1))
		if !f.Protected || f.Reasons[0] != "Keep — best by Resolution (2160p vs 1080p)" || !hasString(f.Reasons, "Protected — *arr tag dupearr-keep") {
			t.Fatalf("reasons = %v", f.Reasons)
		}
	})
}

func TestEvaluateMultiEpisodeProtection(t *testing.T) {
	g := mustEval(t, group(remux4k(1), ver(2, pathB, withShared("201", "202", "201")), bd720(3)), ProfileTemplates()[0], env())
	f := fileByKey(t, g, key(2))
	if !f.Protected || f.Decision != models.DecisionKeep || f.ProtectedReason != "file is shared with other episodes (201, 202)" {
		t.Fatalf("shared file: %+v", f)
	}
	if !g.HasFlag(models.FlagMultiEpisode) {
		t.Fatalf("flags %v", g.Flags)
	}
	if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
		t.Fatalf("kept %v", got)
	}
	// A blank SharedWith entry is still treated as shared (when in doubt, keep).
	g = mustEval(t, group(remux4k(1), ver(2, pathB, withShared(""))), ProfileTemplates()[0], env())
	if f := fileByKey(t, g, key(2)); !f.Protected || f.ProtectedReason != "file is shared with other episodes" {
		t.Fatalf("shared file: %+v", f)
	}
	if g.Status != models.GroupProtected {
		t.Fatalf("status %s", g.Status)
	}
	// Override cannot remove it.
	g.Files[1].Override = models.DecisionRemove
	mustEval(t, g, ProfileTemplates()[0], env())
	if f := fileByKey(t, g, key(2)); f.Decision != models.DecisionKeep || !containsSub(f.Reasons, "Override ignored — the version is protected") {
		t.Fatalf("override not ignored: %+v", f)
	}
}

func TestEvaluateSameFile(t *testing.T) {
	hq := ProfileTemplates()[0]
	t.Run("partner of keeper is protected", func(t *testing.T) {
		best := remux4k(1)
		twin := remux4k(2)
		twin.Parts[0].Path = strings.ToUpper(best.Parts[0].Path)
		g := mustEval(t, group(best, twin, bd720(3)), hq, env())
		f := fileByKey(t, g, key(2))
		if !f.Protected || f.Decision != models.DecisionKeep || f.ProtectedReason != "same file as plex:1:1 (kept)" {
			t.Fatalf("twin: %+v", f)
		}
		if !g.HasFlag(models.FlagSameFile) || g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "same file") {
			t.Fatalf("flags %v status %s (%s)", g.Flags, g.Status, g.StatusReason)
		}
		if err := ValidateDecisions(g); err != nil {
			t.Fatal(err)
		}
		// Removing the twin by override is refused.
		g.Files[1].Override = models.DecisionRemove
		mustEval(t, g, hq, env())
		if f := fileByKey(t, g, key(2)); f.Decision != models.DecisionKeep || !containsSub(f.Reasons, "Override ignored — the version is protected") {
			t.Fatalf("twin: %+v", f)
		}
	})
	t.Run("keep override on one twin keeps the other", func(t *testing.T) {
		g := group(remux4k(1), ver(2, pathA, withInode("64:77")), ver(3, pathB, withInode("64:77")))
		g.Files[2].Override = models.DecisionKeep
		mustEval(t, g, hq, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2), key(3)}) {
			t.Fatalf("kept %v", got)
		}
		if f := fileByKey(t, g, key(2)); !f.Protected || f.ProtectedReason != "same file as plex:1:3 (kept)" {
			t.Fatalf("twin: %+v", f)
		}
	})
	t.Run("twins that both lose are both removed", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1), ver(2, pathA, withInode("64:77")), ver(3, pathB, withInode("64:77"))), hq, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) {
			t.Fatalf("kept %v", got)
		}
		if g.ReclaimableBytes != 8*gib {
			t.Fatalf("reclaimable %d (one shared file counted once)", g.ReclaimableBytes)
		}
		if err := ValidateDecisions(g); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("remove override on keeper with kept twin is refused", func(t *testing.T) {
		best := remux4k(1)
		twin := remux4k(2)
		g := group(best, twin, bd720(3))
		g.Files[0].Override = models.DecisionRemove
		mustEval(t, g, hq, env())
		// The engine keeps 1 and protects 2 (same file); the override on 1 cannot apply because 2
		// is kept and is the same file.
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
			t.Fatalf("kept %v", got)
		}
		if f := fileByKey(t, g, key(1)); !containsSub(f.Reasons, "Override ignored — same file as a kept version") {
			t.Fatalf("reasons %v", f.Reasons)
		}
		if err := ValidateDecisions(g); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEvaluateOverrides(t *testing.T) {
	hq := ProfileTemplates()[0]
	t.Run("swap", func(t *testing.T) {
		g := group(remux4k(1), web1080(2))
		g.Files[0].Override = models.DecisionRemove
		g.Files[1].Override = " KEEP "
		mustEval(t, g, hq, env())
		f1, f2 := fileByKey(t, g, key(1)), fileByKey(t, g, key(2))
		if f1.Decision != models.DecisionRemove || f1.EngineDecision != models.DecisionKeep ||
			f2.Decision != models.DecisionKeep || f2.EngineDecision != models.DecisionRemove {
			t.Fatalf("decisions: %s/%s %s/%s", f1.Decision, f1.EngineDecision, f2.Decision, f2.EngineDecision)
		}
		if f1.Reasons[0] != "Remove — manual override" || f1.Reasons[1] != "Profile decision: Keep — best by Resolution (2160p vs 1080p)" {
			t.Fatalf("reasons 1 = %v", f1.Reasons)
		}
		if f2.Reasons[0] != "Keep — manual override" || f2.Reasons[1] != "Profile decision: Remove — lower Resolution: 1080p vs 2160p" {
			t.Fatalf("reasons 2 = %v", f2.Reasons)
		}
		if g.ReclaimableBytes != 62*gib {
			t.Fatalf("reclaimable = %d", g.ReclaimableBytes)
		}
		if err := ValidateDecisions(g); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("removing every version is refused", func(t *testing.T) {
		g := group(remux4k(1), web1080(2))
		g.Files[0].Override = models.DecisionRemove
		g.Files[1].Override = models.DecisionRemove
		mustEval(t, g, hq, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) {
			t.Fatalf("kept %v", got)
		}
		if f := fileByKey(t, g, key(1)); !hasString(f.Reasons, "Override ignored — it would leave no version to keep") {
			t.Fatalf("reasons %v", f.Reasons)
		}
		if f := fileByKey(t, g, key(2)); !hasString(f.Reasons, "Manual override: remove (same as the profile)") {
			t.Fatalf("reasons %v", f.Reasons)
		}
	})
	t.Run("removing every keeper with keep count 2 restores them", func(t *testing.T) {
		p := hq
		p.KeepCount = 2
		g := group(remux4k(1), web1080(2), bd720(3))
		g.Files[0].Override = models.DecisionRemove
		g.Files[1].Override = models.DecisionRemove
		g.Files[2].Override = models.DecisionRemove
		mustEval(t, g, p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
			t.Fatalf("kept %v", got)
		}
	})
	t.Run("invalid and redundant overrides", func(t *testing.T) {
		g := group(remux4k(1), web1080(2))
		g.Files[0].Override = "maybe"
		g.Files[1].Override = models.DecisionRemove
		mustEval(t, g, hq, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) {
			t.Fatalf("kept %v", got)
		}
		if f := fileByKey(t, g, key(1)); !hasString(f.Reasons, `Override ignored — invalid value "maybe"`) {
			t.Fatalf("reasons %v", f.Reasons)
		}
		g.Files[0].Override = models.DecisionKeep
		mustEval(t, g, hq, env())
		if f := fileByKey(t, g, key(1)); !hasString(f.Reasons, "Manual override: keep (same as the profile)") {
			t.Fatalf("reasons %v", f.Reasons)
		}
	})
	t.Run("keep everything", func(t *testing.T) {
		g := group(remux4k(1), web1080(2))
		g.Files[1].Override = models.DecisionKeep
		mustEval(t, g, hq, env())
		if g.Status != models.GroupProtected || g.ReclaimableBytes != 0 {
			t.Fatalf("status %s reclaimable %d", g.Status, g.ReclaimableBytes)
		}
	})
	t.Run("override changes the signature", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1), web1080(2)), hq, env())
		before := g.Signature
		g.Files[1].Override = models.DecisionKeep
		mustEval(t, g, hq, env())
		if g.Signature == before {
			t.Fatal("signature did not change")
		}
		g.Files[1].Override = ""
		mustEval(t, g, hq, env())
		if g.Signature != before {
			t.Fatal("signature not restored")
		}
	})
}

func TestEvaluateMinAge(t *testing.T) {
	hq := ProfileTemplates()[0]
	week := 7 * 24 * time.Hour
	ageEnv := EvalEnv{Now: tNow, MinAge: week}
	tests := []struct {
		name     string
		loser    []vopt
		keeper   []vopt
		deferred bool
		reason   string
		note     string
	}{
		{"old enough", nil, nil, false, "", ""},
		{"young loser", []vopt{withAdded(tNow.Add(-48 * time.Hour))}, nil, true,
			"Minimum age not reached (a version to remove was added 2d ago; minimum 7d)", "Waiting — added 2d ago (minimum age 7d)"},
		{"young keeper does not matter", nil, []vopt{withAdded(tNow.Add(-time.Hour))}, false, "", ""},
		{"arr date is newer", []vopt{tracked(1, "Radarr", 5, nil), arrSet(func(a *models.ArrFileInfo) { a.DateAdded = tNow.Add(-90 * time.Minute) })}, nil, true,
			"Minimum age not reached (a version to remove was added 1h 30m ago; minimum 7d)", "Waiting — added 1h 30m ago (minimum age 7d)"},
		{"unknown date", []vopt{withAdded(time.Time{})}, nil, true, "Minimum age not reached (date added unknown)", "Waiting — date added unknown (minimum age 7d)"},
		{"future date", []vopt{withAdded(tNow.Add(time.Hour))}, nil, true, "Minimum age not reached (date added is in the future)",
			"Waiting — date added is in the future (minimum age 7d)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := remux4k(1)
			for _, o := range tc.keeper {
				o(&k)
			}
			l := web1080(2)
			for _, o := range tc.loser {
				o(&l)
			}
			g := mustEval(t, group(k, l), hq, ageEnv)
			if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) {
				t.Fatalf("kept %v (min age must not change decisions)", got)
			}
			if g.HasFlag(models.FlagMinAge) != tc.deferred {
				t.Fatalf("min_age flag = %v, want %v", g.HasFlag(models.FlagMinAge), tc.deferred)
			}
			err := ValidateDecisions(g)
			if !tc.deferred {
				if g.Status != models.GroupPending || err != nil {
					t.Fatalf("status %s (%s), validate %v", g.Status, g.StatusReason, err)
				}
				return
			}
			if g.Status != models.GroupDeferred || !strings.HasPrefix(g.StatusReason, tc.reason) {
				t.Fatalf("status %s reason %q, want deferred %q", g.Status, g.StatusReason, tc.reason)
			}
			if !hasString(fileByKey(t, g, key(2)).Reasons, tc.note) {
				t.Fatalf("reasons %v, want note %q", fileByKey(t, g, key(2)).Reasons, tc.note)
			}
			if !errors.Is(err, ErrInvariant) {
				t.Fatalf("ValidateDecisions = %v, want invariant error", err)
			}
		})
	}

	t.Run("disabled", func(t *testing.T) {
		l := web1080(2, withAdded(time.Time{}))
		g := mustEval(t, group(remux4k(1), l), hq, EvalEnv{Now: tNow})
		if g.HasFlag(models.FlagMinAge) || g.Status != models.GroupPending {
			t.Fatalf("flags %v status %s", g.Flags, g.Status)
		}
	})
	t.Run("stale flag is cleared", func(t *testing.T) {
		g := group(remux4k(1), web1080(2))
		g.Flags = []string{models.FlagMinAge, models.FlagArrUntrackedKeeper, models.FlagCrossLibrary}
		mustEval(t, g, hq, ageEnv)
		if !reflect.DeepEqual(g.Flags, []string{models.FlagCrossLibrary}) {
			t.Fatalf("flags %v", g.Flags)
		}
	})
	t.Run("zero now uses the clock", func(t *testing.T) {
		l := web1080(2, withAdded(time.Now().Add(-time.Hour)))
		g := mustEval(t, group(remux4k(1), l), hq, EvalEnv{MinAge: week})
		if !g.HasFlag(models.FlagMinAge) {
			t.Fatalf("flags %v", g.Flags)
		}
	})
}

func TestEvaluateStatus(t *testing.T) {
	hq := ProfileTemplates()[0]
	two := func(opts ...vopt) *models.DuplicateGroup {
		l := web1080(2)
		for _, o := range opts {
			o(&l)
		}
		return group(remux4k(1), l)
	}
	withFlags := func(g *models.DuplicateGroup, flags ...string) *models.DuplicateGroup {
		g.Flags = flags
		return g
	}
	intentional := func() *models.DuplicateGroup {
		a := remux4k(1)
		tracked(2, "Radarr 4K", 7, nil)(&a)
		return group(a, web1080(2, tracked(1, "Radarr", 8, nil)))
	}
	tests := []struct {
		name   string
		g      *models.DuplicateGroup
		env    EvalEnv
		status models.GroupStatus
		reason string
		flag   string
	}{
		{"pending", two(), env(), models.GroupPending, "", ""},
		{"suspect merge", withFlags(two(), models.FlagSuspectMerge), env(), models.GroupReview,
			"Possible mismatched merge: versions may belong to different titles", ""},
		{"suspect merge explained", withFlags(two(withArr(1, "Radarr", 5, 1), func(v *models.MediaVersion) {}), models.FlagSuspectMerge),
			EvalEnv{Now: tNow, MaxGroupSize: 1}, models.GroupReview, "Possible mismatched merge: 2 versions in one group (more than 1)", ""},
		{"unanalyzed", two(unanalyzed()), env(), models.GroupReview, "Some versions are not analyzed by the media server", models.FlagUnanalyzed},
		{"duration mismatch", withFlags(two(withDuration(7_000_000)), models.FlagDurationMismatch), env(), models.GroupReview,
			"Durations differ (1h 56m vs 2h 35m)", models.FlagSample},
		{"duration mismatch without durations", withFlags(two(withDuration(0)), models.FlagDurationMismatch), EvalEnv{Now: tNow},
			models.GroupReview, "Durations differ", ""},
		{"same file", two(func(v *models.MediaVersion) { v.Parts[0].Path = remux4k(1).Parts[0].Path }), env(), models.GroupReview,
			"Two or more versions point to the same file", models.FlagSameFile},
		{"inaccessible keeper", group(remux4k(1, withAccessible(false)), web1080(2, withAccessible(false))), env(), models.GroupReview,
			"A kept version is not accessible to the media server", models.FlagMissingKeeperFile},
		{"intentional instances", intentional(), EvalEnv{Now: tNow, DifferentArrInstancesIntentional: true}, models.GroupProtected,
			"Versions are tracked by different *arr instances (Radarr, Radarr 4K) — treated as intentional", models.FlagIntentionalArr},
		{"instances not intentional", withFlags(intentional(), models.FlagIntentionalArr), env(), models.GroupPending, "", models.FlagArrUntrackedKeeper},
		{"nothing to remove", group(remux4k(1), web1080(2, withShared("7"))), env(), models.GroupProtected,
			"Nothing to remove — every version is kept or protected", models.FlagMultiEpisode},
		{"queue busy", withFlags(two(), models.FlagArrQueueBusy), env(), models.GroupDeferred,
			"The *arr has an active download or import for this title", models.FlagArrQueueBusy},
		{"playing", withFlags(two(), models.FlagPlaying), env(), models.GroupDeferred, "A version is currently playing", models.FlagPlaying},
		{"deferred reasons combine", withFlags(two(withAdded(tNow.Add(-time.Hour))), models.FlagPlaying, models.FlagArrQueueBusy),
			EvalEnv{Now: tNow, MinAge: time.Hour * 24}, models.GroupDeferred,
			"Minimum age not reached (a version to remove was added 1h ago; minimum 1d); the *arr has an active download or import for this title; a version is currently playing", ""},
		{"review beats deferred", withFlags(two(unanalyzed()), models.FlagPlaying), env(), models.GroupReview, "Some versions", ""},
		{"review beats protected", withFlags(intentional(), models.FlagSuspectMerge), EvalEnv{Now: tNow, DifferentArrInstancesIntentional: true},
			models.GroupReview, "Possible mismatched merge", models.FlagIntentionalArr},
		{"review reasons combine", withFlags(two(unanalyzed(), withDuration(7_000_000)), models.FlagDurationMismatch), env(), models.GroupReview,
			"Some versions are not analyzed by the media server (analyze them in Plex); durations differ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := mustEval(t, tc.g, hq, tc.env)
			if g.Status != tc.status || !strings.HasPrefix(g.StatusReason, tc.reason) || (tc.reason == "" && g.StatusReason != "") {
				t.Fatalf("status %s reason %q, want %s %q", g.Status, g.StatusReason, tc.status, tc.reason)
			}
			if tc.flag != "" && !g.HasFlag(tc.flag) {
				t.Fatalf("missing flag %s: %v", tc.flag, g.Flags)
			}
			if g.ProfileID != hq.ID {
				t.Fatalf("profile id %d", g.ProfileID)
			}
		})
	}

	t.Run("keeps ignored", func(t *testing.T) {
		g := two(unanalyzed())
		g.Status, g.StatusReason = models.GroupIgnored, "user"
		mustEval(t, g, hq, env())
		if g.Status != models.GroupIgnored || g.StatusReason != "user" || g.Signature == "" || g.Files[0].Rank == 0 {
			t.Fatalf("status %s reason %q", g.Status, g.StatusReason)
		}
	})
	// A queued (approved) group stays queued only while its removals are still allowed; a status
	// that blocks removals replaces it so the store cancels the queued actions.
	queued := []struct {
		name   string
		g      *models.DuplicateGroup
		env    EvalEnv
		status models.GroupStatus
	}{
		{"queued stays queued while pending", two(), env(), models.GroupQueued},
		{"queued stays queued while only playing", withFlags(two(), models.FlagPlaying), env(), models.GroupQueued},
		{"queued needs review", two(unanalyzed()), env(), models.GroupReview},
		{"queued suspect merge", withFlags(two(), models.FlagSuspectMerge), env(), models.GroupReview},
		{"queued with nothing left to remove", group(remux4k(1), web1080(2, withShared("7"))), env(), models.GroupProtected},
		{"queued intentional instances", intentional(), EvalEnv{Now: tNow, DifferentArrInstancesIntentional: true}, models.GroupProtected},
		{"queued queue busy", withFlags(two(), models.FlagArrQueueBusy), env(), models.GroupDeferred},
		{"queued playing and queue busy", withFlags(two(), models.FlagPlaying, models.FlagArrQueueBusy), env(), models.GroupDeferred},
		{"queued min age", two(withAdded(tNow.Add(-time.Hour))), EvalEnv{Now: tNow, MinAge: 24 * time.Hour}, models.GroupDeferred},
	}
	for _, tc := range queued {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.g
			g.Status, g.StatusReason = models.GroupQueued, "approved"
			mustEval(t, g, hq, tc.env)
			if g.Status != tc.status {
				t.Fatalf("status %s (%q), want %s", g.Status, g.StatusReason, tc.status)
			}
			if tc.status == models.GroupQueued && g.StatusReason != "approved" {
				t.Fatalf("reason %q", g.StatusReason)
			}
			if tc.status != models.GroupQueued && g.StatusReason == "approved" {
				t.Fatalf("stale reason %q", g.StatusReason)
			}
		})
	}
	t.Run("resolved group is re-derived", func(t *testing.T) {
		g := two()
		g.Status = models.GroupResolved
		mustEval(t, g, hq, env())
		if g.Status != models.GroupPending {
			t.Fatalf("status %s", g.Status)
		}
	})
}

func TestEvaluateArrFlags(t *testing.T) {
	hq := ProfileTemplates()[0]
	t.Run("untracked keeper", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1), web1080(2, tracked(1, "Radarr", 8, nil))), hq, env())
		if !g.HasFlag(models.FlagArrUntrackedKeeper) {
			t.Fatalf("flags %v", g.Flags)
		}
		if f := fileByKey(t, g, key(2)); !hasString(f.Reasons, "Warning — tracked by Radarr but no kept version is: the *arr may re-download it") {
			t.Fatalf("reasons %v", f.Reasons)
		}
	})
	t.Run("keeper tracked by the same instance", func(t *testing.T) {
		a := remux4k(1)
		tracked(1, "Radarr", 7, nil)(&a)
		g := mustEval(t, group(a, web1080(2, tracked(1, "Radarr", 8, nil))), hq, env())
		if g.HasFlag(models.FlagArrUntrackedKeeper) {
			t.Fatalf("flags %v", g.Flags)
		}
	})
	t.Run("cutoff unmet", func(t *testing.T) {
		a := remux4k(1)
		tracked(1, "Radarr", 7, nil)(&a)
		a.Arr.QualityCutoffNotMet = true
		g := mustEval(t, group(a, web1080(2, tracked(1, "", 8, nil), arrSet(func(x *models.ArrFileInfo) { x.QualityCutoffNotMet = true }))), hq, env())
		if !g.HasFlag(models.FlagArrCutoffUnmet) {
			t.Fatalf("flags %v", g.Flags)
		}
		if f := fileByKey(t, g, key(1)); !hasString(f.Reasons, "Warning — quality cutoff not met in Radarr: it may upgrade and recreate the duplicate") {
			t.Fatalf("reasons %v", f.Reasons)
		}
	})
	t.Run("cutoff unmet on a removed version only", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1), web1080(2, tracked(1, "Radarr", 8, nil), arrSet(func(x *models.ArrFileInfo) { x.QualityCutoffNotMet = true }))), hq, env())
		if g.HasFlag(models.FlagArrCutoffUnmet) {
			t.Fatalf("flags %v", g.Flags)
		}
	})
	t.Run("version flags are refreshed", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1), web1080(2, withLinks(3))), hq, env())
		if !g.HasFlag(models.FlagHardlinked) || g.ReclaimableBytes != 0 {
			t.Fatalf("flags %v reclaimable %d", g.Flags, g.ReclaimableBytes)
		}
	})
}

func TestEvaluateReclaimable(t *testing.T) {
	stacked := ver(3, pathB, withParts(models.MediaPart{Path: "/m/cd1.mkv", Size: 3 * gib}, models.MediaPart{Path: "/m/cd2.mkv", Size: 2 * gib, LinkCount: 1}))
	tests := []struct {
		name string
		g    *models.DuplicateGroup
		want int64
	}{
		{"sum of removed", group(remux4k(1), web1080(2), bd720(3)), 12 * gib},
		{"hardlinked counts zero", group(remux4k(1), web1080(2, withLinks(2)), bd720(3)), 4 * gib},
		{"stacked parts summed", group(remux4k(1), web1080(2), stacked), 13 * gib},
		{"nothing removed", group(remux4k(1)), 0},
		{"unknown sizes", group(remux4k(1), web1080(2, withSize(0))), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := mustEval(t, tc.g, ProfileTemplates()[0], env())
			if g.ReclaimableBytes != tc.want {
				t.Fatalf("reclaimable = %d, want %d", g.ReclaimableBytes, tc.want)
			}
		})
	}
}

func TestEvaluateSignature(t *testing.T) {
	hq := ProfileTemplates()[0]
	g := mustEval(t, group(bd720(3), remux4k(1), web1080(2)), hq, env())
	sum := sha1.Sum([]byte("plex:1:1=keep\nplex:1:2=remove\nplex:1:3=remove"))
	if want := hex.EncodeToString(sum[:]); g.Signature != want {
		t.Fatalf("signature %s, want %s", g.Signature, want)
	}
	g2 := mustEval(t, group(web1080(2), bd720(3), remux4k(1)), hq, env())
	if g2.Signature != g.Signature {
		t.Fatal("signature depends on file order")
	}
	g3 := mustEval(t, group(bd720(3), remux4k(1), web1080(2)), hq, EvalEnv{Now: tNow, MinAge: time.Hour})
	if g3.Signature != g.Signature {
		t.Fatal("signature must only depend on keys and decisions")
	}
}

func TestEvaluateDeterministicAndIdempotent(t *testing.T) {
	hq := ProfileTemplates()[0]
	build := func() []models.MediaVersion {
		return []models.MediaVersion{
			remux4k(1), web1080(2), bd720(3),
			ver(4, "/data/movies/Dune (2021)/x.mkv", withBitrate(8100, 8600)), // ties with 2 within tolerance
			ver(5, "/data/movies/Dune (2021)/y.mkv", withCodec(models.VCodecHEVC), withBitrate(4000, 4200)),
			ver(6, "/data/movies/Dune (2021)/z.mkv", tracked(1, "Radarr", 1, intp(50))),
		}
	}
	type snapshot struct {
		Decision models.Decision
		Rank     int
		Reasons  []string
		Decider  string
	}
	snap := func(g *models.DuplicateGroup) map[string]snapshot {
		out := map[string]snapshot{}
		for _, f := range g.Files {
			out[f.Version.Key] = snapshot{f.Decision, f.Rank, f.Reasons, f.DecidingCriterion}
		}
		return out
	}
	ref := mustEval(t, group(build()...), hq, env())
	want := snap(ref)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 30; i++ {
		vs := build()
		rng.Shuffle(len(vs), func(a, b int) { vs[a], vs[b] = vs[b], vs[a] })
		g := mustEval(t, group(vs...), hq, env())
		if got := snap(g); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d differs:\n got %v\nwant %v", i, got, want)
		}
		if g.Signature != ref.Signature || !reflect.DeepEqual(g.Flags, ref.Flags) || g.Status != ref.Status {
			t.Fatalf("iteration %d: group fields differ", i)
		}
	}
	// Idempotent.
	again := mustEval(t, group(build()...), hq, env())
	mustEval(t, again, hq, env())
	if !reflect.DeepEqual(snap(again), want) || again.Signature != ref.Signature {
		t.Fatal("second evaluation changed the result")
	}
	// Ranks are a permutation of 1..n.
	seen := map[int]bool{}
	for _, f := range ref.Files {
		seen[f.Rank] = true
	}
	for r := 1; r <= len(ref.Files); r++ {
		if !seen[r] {
			t.Fatalf("rank %d missing", r)
		}
	}
}

func TestEvaluateValues(t *testing.T) {
	p := profile(withPatterns(crit(models.CritFilenameScore), models.PatternScore{Pattern: "*Remux*", Score: 100}),
		withPatterns(crit(models.CritFilenameScore), models.PatternScore{Pattern: "*.mkv", Score: 1}))
	w := web1080(2)
	w.LibraryTitle = ""
	e := EvalEnv{Now: tNow, Libraries: map[int64]models.Library{1: {ID: 1, Title: "Films"}}}
	g := mustEval(t, group(remux4k(1), w), p, e)
	for _, f := range g.Files {
		for _, ct := range criterionTypes() {
			if v, ok := f.Values[string(ct)]; !ok || v == "" {
				t.Fatalf("%s: value for %s missing (%v)", f.Version.Key, ct, f.Values)
			}
		}
	}
	f1, f2 := fileByKey(t, g, key(1)), fileByKey(t, g, key(2))
	want1 := map[string]string{
		"resolution": "2160p", "dynamic_range": "Dolby Vision (HDR10)", "source": "Remux", "video_codec": "HEVC",
		"audio_format": "TrueHD Atmos 7.1", "container": "MKV", "library": "Movies", "audio_channels": "7.1",
		"video_bitrate": "56.0 Mbps", "file_size": "62.0 GiB", "bit_depth": "10-bit", "custom_format_score": "Not tracked",
		"date_added": "2024-01-01", "audio_track_count": "2", "subtitle_track_count": "12", "arr_managed": "No",
		"audio_language": "eng", "filename_score": "100 / 1", "health": "Healthy",
		"played": "Unknown (no watch-history source)", "last_played": "Unknown (no watch-history source)",
	}
	if !reflect.DeepEqual(f1.Values, want1) {
		t.Fatalf("values 1:\n got %v\nwant %v", f1.Values, want1)
	}
	if f2.Values["library"] != "Films" || f2.Values["filename_score"] != "0 / 1" {
		t.Fatalf("values 2: %v", f2.Values)
	}
	// Without filename patterns the value shows the file name.
	g = mustEval(t, group(remux4k(1), web1080(2)), profile(), env())
	if got := fileByKey(t, g, key(2)).Values["filename_score"]; got != "Dune (2021) WEBDL-1080p.mkv" {
		t.Fatalf("filename value %q", got)
	}
}

func TestEvaluateIneligibleVersions(t *testing.T) {
	hq := ProfileTemplates()[0]
	t.Run("unavailable best is not a keeper", func(t *testing.T) {
		// Health (first in every template) would rank it last; without health it ranks first
		// but is still never chosen as a keeper.
		g := mustEval(t, group(remux4k(1, withExists(false)), web1080(2), bd720(3)), profile(crit(models.CritResolution)), env())
		f := fileByKey(t, g, key(1))
		if !f.Protected || f.Decision != models.DecisionKeep || f.Rank != 1 || f.DecidingCriterion != "" ||
			f.ProtectedReason != "the media server reports the file as missing (not acted on)" {
			t.Fatalf("unavailable: %+v", f)
		}
		want := []string{"Protected — the media server reports the file as missing (not acted on)", "Not considered as a keeper (ranked #1)", "Health — file missing"}
		if !reflect.DeepEqual(f.Reasons, want) {
			t.Fatalf("reasons %v", f.Reasons)
		}
		g = mustEval(t, group(remux4k(1, withExists(false)), web1080(2), bd720(3)), hq, env())
		if f := fileByKey(t, g, key(1)); f.Rank != 3 || f.DecidingCriterion != "health" || f.Decision != models.DecisionKeep {
			t.Fatalf("with health: %+v", f)
		}
		g = mustEval(t, group(remux4k(1, withExists(false)), web1080(2), bd720(3)), profile(crit(models.CritResolution)), env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
			t.Fatalf("kept %v", got)
		}
		if f := fileByKey(t, g, key(2)); f.Reasons[0] != "Keep — best by Resolution (1080p vs 720p)" {
			t.Fatalf("reasons %v", f.Reasons)
		}
		if f := fileByKey(t, g, key(3)); f.DecidingCriterion != "resolution" {
			t.Fatalf("decided by %q", f.DecidingCriterion)
		}
		if err := ValidateDecisions(g); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("all unavailable", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1, withExists(false)), web1080(2, withParts())), hq, env())
		if got := keptKeys(g); len(got) != 2 || g.Status != models.GroupProtected {
			t.Fatalf("kept %v status %s", got, g.Status)
		}
		if f := fileByKey(t, g, key(2)); !hasString(f.Reasons, "Not considered as a keeper (ranked #2)") {
			t.Fatalf("reasons %v", f.Reasons)
		}
	})
	t.Run("optimized never removed", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1), web1080(2, withOptimized())), hq, env())
		f := fileByKey(t, g, key(2))
		if !f.Protected || f.Decision != models.DecisionKeep || f.ProtectedReason != "Plex optimized version (never touched)" {
			t.Fatalf("optimized: %+v", f)
		}
		if !hasString(f.Reasons, "Protected — Plex optimized version (never touched)") {
			t.Fatalf("reasons %v", f.Reasons)
		}
	})
	t.Run("remove override on the only available version", func(t *testing.T) {
		g := group(remux4k(1, withExists(false)), web1080(2))
		g.Files[1].Override = models.DecisionRemove
		mustEval(t, g, hq, env())
		if f := fileByKey(t, g, key(2)); f.Decision != models.DecisionKeep {
			t.Fatalf("the only available version was removed: %+v", f)
		}
	})
	t.Run("health notes", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1), web1080(2, withDuration(4_000_000))), hq, env())
		if f := fileByKey(t, g, key(2)); !hasString(f.Reasons, "Health — possible sample (1h 6m of 2h 35m)") {
			t.Fatalf("reasons %v", f.Reasons)
		}
	})
	t.Run("single version group", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1)), hq, env())
		f := g.Files[0]
		if f.Decision != models.DecisionKeep || f.Reasons[0] != "Keep — only version" || g.Status != models.GroupProtected {
			t.Fatalf("file %+v status %s", f, g.Status)
		}
	})
}

func TestEvaluateErrors(t *testing.T) {
	tests := []struct {
		name string
		g    *models.DuplicateGroup
	}{
		{"nil", nil},
		{"no files", &models.DuplicateGroup{Key: "k"}},
		{"empty key", group(ver(1, pathA, withKey(" ")), ver(2, pathB))},
		{"duplicate key", group(ver(1, pathA), ver(1, pathB))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Evaluate(tc.g, ProfileTemplates()[0], env()); !errors.Is(err, ErrInvalidGroup) {
				t.Fatalf("Evaluate = %v, want ErrInvalidGroup", err)
			}
		})
	}
}

func TestTemplatesOnRealisticGroups(t *testing.T) {
	byName := map[string]models.Profile{}
	for _, p := range ProfileTemplates() {
		byName[p.Name] = p
	}
	web1080HEVC := ver(4, "/data/movies/Dune (2021)/Dune (2021) WEBDL-1080p x265.mkv", withCodec(models.VCodecHEVC), withSize(5*gib), withBitrate(5000, 5500))
	bluray1080 := ver(4, "/data/movies/Dune (2021)/Dune (2021) Bluray-1080p.mkv", withSource(models.SourceBluray), withSize(14*gib),
		withAudio(track(models.AudioDTSHDMA, 8, "eng")))
	dvP5 := ver(5, "/data/movies/Dune (2021)/Dune (2021) WEBDL-2160p DV.mkv", withRes(3840, 1600, models.Res2160), withCodec(models.VCodecHEVC),
		withDR(models.DRDolbyVision), withSize(18*gib), func(v *models.MediaVersion) { v.DVProfile = 5 })
	hdr10 := ver(6, "/data/movies/Dune (2021)/Dune (2021) WEBDL-2160p HDR10.mkv", withRes(3840, 1600, models.Res2160), withCodec(models.VCodecHEVC),
		withDR(models.DRHDR10), withSize(17*gib))

	tests := []struct {
		name     string
		template string
		vs       []models.MediaVersion
		kept     []string
		decider  map[string]string
	}{
		{"highest quality keeps the 4K remux", "Keep Highest Quality", []models.MediaVersion{remux4k(1), web1080(2), bd720(3)},
			[]string{key(1)}, map[string]string{key(2): "resolution", key(3): "resolution"}},
		{"highest quality: HDR10 over DV without fallback", "Keep Highest Quality", []models.MediaVersion{dvP5, hdr10},
			[]string{key(6)}, map[string]string{key(5): "dynamic_range"}},
		{"highest quality: blu-ray over web at 1080p", "Keep Highest Quality", []models.MediaVersion{web1080(2), bluray1080},
			[]string{key(4)}, map[string]string{key(2): "source"}},
		{"highest quality: healthy 1080p over broken 4K", "Keep Highest Quality", []models.MediaVersion{remux4k(1, unanalyzed()), web1080(2)},
			[]string{key(2)}, map[string]string{key(1): "health"}},
		{"one per resolution", "Keep One Per Resolution", []models.MediaVersion{remux4k(1), web1080(2), bd720(3), bluray1080},
			[]string{key(1), key(3), key(4)}, map[string]string{key(2): "source"}},
		{"save space prefers 1080p", "Save Space", []models.MediaVersion{remux4k(1), web1080(2), bd720(3)},
			[]string{key(2)}, map[string]string{key(1): "resolution", key(3): "resolution"}},
		{"save space prefers efficient codec", "Save Space", []models.MediaVersion{remux4k(1), web1080(2), bd720(3), web1080HEVC},
			[]string{key(4)}, map[string]string{key(2): "video_codec"}},
		{"save space smaller at equal codec", "Save Space", []models.MediaVersion{web1080(2), bluray1080},
			[]string{key(2)}, map[string]string{key(4): "file_size"}},
		{"maximum compatibility keeps h264 sdr", "Maximum Compatibility", []models.MediaVersion{remux4k(1), web1080(2)},
			[]string{key(2)}, map[string]string{key(1): "video_codec"}},
		{"maximum compatibility prefers eac3 over dts-hd", "Maximum Compatibility", []models.MediaVersion{web1080(2), bluray1080},
			[]string{key(2)}, map[string]string{key(4): "audio_format"}},
		{"trust my arr keeps the tracked file", "Trust My *arr", []models.MediaVersion{remux4k(1), web1080(2, tracked(1, "Radarr", 8, intp(100)))},
			[]string{key(2)}, map[string]string{key(1): "arr_managed"}},
		{"trust my arr falls back to quality", "Trust My *arr", []models.MediaVersion{remux4k(1), web1080(2)},
			[]string{key(1)}, map[string]string{key(2): "resolution"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := byName[tc.template]
			if !ok {
				t.Fatalf("no template %q", tc.template)
			}
			g := mustEval(t, group(tc.vs...), p, env())
			got := keptKeys(g)
			want := append([]string{}, tc.kept...)
			sortStrings(got)
			sortStrings(want)
			if !reflect.DeepEqual(got, want) {
				for _, f := range g.Files {
					t.Logf("%s rank %d %s %v", f.Version.Key, f.Rank, f.Decision, f.Reasons)
				}
				t.Fatalf("kept %v, want %v", got, want)
			}
			for k, d := range tc.decider {
				if f := fileByKey(t, g, k); f.DecidingCriterion != d {
					t.Fatalf("%s decided by %q, want %q (%v)", k, f.DecidingCriterion, d, f.Reasons)
				}
			}
			if err := ValidateDecisions(g); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("keep tag protects under every template", func(t *testing.T) {
		for _, p := range ProfileTemplates() {
			tagged := bd720(3, tracked(1, "Radarr", 8, nil), arrSet(func(a *models.ArrFileInfo) { a.Tags = []string{"dupearr-keep"} }))
			g := mustEval(t, group(remux4k(1), web1080(2), tagged), p, env())
			if f := fileByKey(t, g, key(3)); !f.Protected || f.Decision != models.DecisionKeep {
				t.Fatalf("%s: tagged version not protected: %+v", p.Name, f)
			}
		}
	})
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestValidateDecisions(t *testing.T) {
	valid := func() *models.DuplicateGroup {
		return mustEval(t, group(remux4k(1), web1080(2), bd720(3)), ProfileTemplates()[0], env())
	}
	tests := []struct {
		name   string
		mutate func(g *models.DuplicateGroup) *models.DuplicateGroup
		want   string // "" = valid
	}{
		{"evaluated group is valid", func(g *models.DuplicateGroup) *models.DuplicateGroup { return g }, ""},
		{"nil group", func(*models.DuplicateGroup) *models.DuplicateGroup { return nil }, "nil group"},
		{"no files", func(g *models.DuplicateGroup) *models.DuplicateGroup { g.Files = nil; return g }, "has no files"},
		{"no keeper", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[0].Decision = models.DecisionRemove
			return g
		}, "no version would be kept"},
		{"missing decision", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[1].Decision = ""
			return g
		}, "has no valid decision"},
		{"protected removed", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[1].Protected = true
			return g
		}, "protected version plex:1:2 would be removed"},
		{"shared removed", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[1].Version.Parts[0].SharedWith = []string{"77"}
			return g
		}, "shares its file with other episodes"},
		{"optimized removed", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[1].Version.OptimizedVersion = true
			return g
		}, "optimized version plex:1:2 would be removed"},
		{"same path as keeper", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[1].Version.Parts[0].Path = strings.ToUpper(g.Files[0].Version.Parts[0].Path)
			return g
		}, "version plex:1:2 is the same file as kept version plex:1:1"},
		{"same local path as keeper", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[0].Version.Parts[0].LocalPath = "/mnt/x.mkv"
			g.Files[2].Version.Parts[0].LocalPath = `/mnt//x.mkv`
			return g
		}, "version plex:1:3 is the same file as kept version plex:1:1"},
		{"same inode as keeper", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[0].Version.Parts[0].Inode = "1:2"
			g.Files[1].Version.Parts[0].Inode = "1:2"
			return g
		}, "is the same file as kept version"},
		{"only unavailable keeper", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[0].Version.Parts[0].Exists = boolp(false)
			return g
		}, "no available, accessible, non-optimized version would be kept"},
		{"only inaccessible keeper", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[0].Version.Parts[0].Accessible = boolp(false)
			return g
		}, "no available, accessible, non-optimized version would be kept"},
		{"sonarr multi-episode file removed", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[1].Version.Arr = &models.ArrFileInfo{InstanceID: 3, InstanceName: "Sonarr", EpisodeIDs: []int64{5, 6}}
			return g
		}, "version plex:1:2 shares its file with other episodes"},
		{"multi-episode file name removed", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Files[2].Version.Parts[0].Path = "/tv/Show/Season 01/Show - S01E01-E02 - Pilot.mkv"
			return g
		}, "version plex:1:3 shares its file with other episodes"},
		{"tracked version removed with intentional instances", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Flags = append(g.Flags, models.FlagIntentionalArr)
			g.Files[1].Version.Arr = &models.ArrFileInfo{InstanceID: 1, InstanceName: "Radarr"}
			return g
		}, "version plex:1:2 is tracked by Radarr and versions tracked by different *arr instances are treated as intentional"},
		{"untracked version removed with intentional instances", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Flags = append(g.Flags, models.FlagIntentionalArr)
			return g
		}, ""},
		{"arr queue busy", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Flags = append(g.Flags, models.FlagArrQueueBusy)
			return g
		}, "the *arr has an active download or import for this title"},
		{"min age", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			g.Flags = append(g.Flags, models.FlagMinAge)
			return g
		}, "younger than the minimum age"},
		{"all kept is valid with min age", func(g *models.DuplicateGroup) *models.DuplicateGroup {
			for i := range g.Files {
				g.Files[i].Decision = models.DecisionKeep
			}
			g.Flags = append(g.Flags, models.FlagMinAge)
			return g
		}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.mutate(valid())
			err := ValidateDecisions(g)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateDecisions = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvariant) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateDecisions = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestConcurrentUse exercises the package from many goroutines (run with -race): no shared
// mutable state may exist between calls.
func TestConcurrentUse(t *testing.T) {
	opts := gopts()
	opts.Libraries = scopedLibs()
	done := make(chan error, 16)
	for w := 0; w < 16; w++ {
		go func() {
			items := []models.MediaItem{movieItem("100", 1, tmdb("1"), web1080(1), bd720(2)), movieItem("200", 2, tmdb("1"), remux4k(3))}
			for _, g := range BuildGroups(items, opts) {
				for _, p := range ProfileTemplates() {
					if err := Evaluate(g, p, EvalEnv{Now: tNow, MinAge: time.Hour}); err != nil {
						done <- err
						return
					}
					if err := ValidateDecisions(g); err != nil {
						done <- err
						return
					}
				}
			}
			_ = CriteriaSchema()
			done <- nil
		}()
	}
	for w := 0; w < 16; w++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
