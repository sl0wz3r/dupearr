package engine

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestCriteriaSchema(t *testing.T) {
	s := CriteriaSchema()
	if len(s) != 21 || len(s) != len(criterionTypes()) {
		t.Fatalf("schema has %d entries", len(s))
	}
	if s[0].Type != models.CritHealth {
		t.Fatalf("health must be listed first, got %s", s[0].Type)
	}
	seen := map[models.CriterionType]bool{}
	arr := map[models.CriterionType]bool{}
	tol := map[models.CriterionType]bool{}
	watch := map[models.CriterionType]bool{}
	units := map[models.CriterionType]string{}
	for _, c := range s {
		if seen[c.Type] {
			t.Fatalf("duplicate type %s", c.Type)
		}
		seen[c.Type] = true
		if c.Label == "" || c.Description == "" {
			t.Fatalf("%s: missing label/description", c.Type)
		}
		switch c.Kind {
		case kindOrdered:
			if c.Type == models.CritLibrary {
				if len(c.Options) != 0 || len(c.DefaultOrder) != 0 {
					t.Fatalf("library options are dynamic: %+v", c)
				}
				continue
			}
			if len(c.Options) == 0 || len(c.DefaultOrder) == 0 {
				t.Fatalf("%s: ordered criterion without options/default order", c.Type)
			}
			opts := map[string]bool{}
			for _, o := range c.Options {
				if o.Value == "" || o.Label == "" {
					t.Fatalf("%s: bad option %+v", c.Type, o)
				}
				opts[o.Value] = true
			}
			for _, o := range c.DefaultOrder {
				if !opts[o] {
					t.Fatalf("%s: default order value %q is not an option", c.Type, o)
				}
			}
		case kindNumeric:
			if c.DefaultDirection != models.DirectionHigher {
				t.Fatalf("%s: default direction %q", c.Type, c.DefaultDirection)
			}
		case kindBoolean, kindPatterns:
		default:
			t.Fatalf("%s: unknown kind %q", c.Type, c.Kind)
		}
		if c.RequiresArr {
			arr[c.Type] = true
		}
		if c.SupportsTolerance {
			tol[c.Type] = true
		}
		if c.RequiresWatchHistory {
			watch[c.Type] = true
		}
		if c.MinDeltaUnit != "" {
			units[c.Type] = c.MinDeltaUnit
		}
	}
	if !reflect.DeepEqual(watch, map[models.CriterionType]bool{models.CritPlayed: true, models.CritLastPlayed: true}) {
		t.Fatalf("requiresWatchHistory = %v", watch)
	}
	if !reflect.DeepEqual(units, map[models.CriterionType]string{models.CritLastPlayed: "days"}) {
		t.Fatalf("minDeltaUnit = %v", units)
	}
	if !reflect.DeepEqual(arr, map[models.CriterionType]bool{models.CritCustomFormatScore: true, models.CritArrManaged: true}) {
		t.Fatalf("requiresArr = %v", arr)
	}
	if !reflect.DeepEqual(tol, map[models.CriterionType]bool{models.CritVideoBitrate: true, models.CritFileSize: true, models.CritCustomFormatScore: true}) {
		t.Fatalf("supportsTolerance = %v", tol)
	}
	// Fresh values on every call.
	s[1].Options[0].Label = "changed"
	s[1].DefaultOrder[0] = "changed"
	if again := CriteriaSchema(); again[1].Options[0].Label == "changed" || again[1].DefaultOrder[0] == "changed" {
		t.Fatal("CriteriaSchema shares state between calls")
	}
	b, err := json.Marshal(CriteriaSchema())
	if err != nil || !strings.Contains(string(b), `"supportsTolerance":true`) || !strings.Contains(string(b), `"label":"2160p (4K)"`) {
		t.Fatalf("json: %v %s", err, b)
	}
}

func critTypes(cs []models.Criterion) []models.CriterionType {
	out := make([]models.CriterionType, len(cs))
	for i, c := range cs {
		out[i] = c.Type
	}
	return out
}

func TestProfileTemplates(t *testing.T) {
	ps := ProfileTemplates()
	names := []string{"Keep Highest Quality", "Keep One Per Resolution", "Save Space", "Maximum Compatibility", "Trust My *arr"}
	if len(ps) != len(names) {
		t.Fatalf("%d templates", len(ps))
	}
	for i, p := range ps {
		if p.Name != names[i] {
			t.Fatalf("template %d = %q, want %q", i, p.Name, names[i])
		}
		if p.IsDefault != (i == 0) {
			t.Fatalf("%s: IsDefault = %v", p.Name, p.IsDefault)
		}
		if errs := ValidateProfile(p); errs != nil {
			t.Fatalf("%s: invalid template: %v", p.Name, errs)
		}
		if p.KeepCount != 1 || len(p.Criteria) == 0 || p.Criteria[0].Type != models.CritHealth {
			t.Fatalf("%s: keepCount %d criteria %v", p.Name, p.KeepCount, critTypes(p.Criteria))
		}
		if !reflect.DeepEqual(p.Protections, []models.Protection{{Type: models.ProtectArrTag, Value: "dupearr-keep"}}) {
			t.Fatalf("%s: protections %v", p.Name, p.Protections)
		}
		for _, c := range p.Criteria {
			if !c.Enabled {
				t.Fatalf("%s: disabled criterion %s", p.Name, c.Type)
			}
		}
	}
	hq := ps[0]
	wantChain := []models.CriterionType{models.CritHealth, models.CritResolution, models.CritDynamicRange, models.CritSource,
		models.CritCustomFormatScore, models.CritVideoBitrate, models.CritAudioFormat, models.CritAudioChannels, models.CritArrManaged,
		models.CritContainer, models.CritFileSize, models.CritDateAdded}
	if got := critTypes(hq.Criteria); !reflect.DeepEqual(got, wantChain) {
		t.Fatalf("default chain = %v", got)
	}
	byType := map[models.CriterionType]models.Criterion{}
	for _, c := range hq.Criteria {
		byType[c.Type] = c
	}
	if got := byType[models.CritDynamicRange].Order; !reflect.DeepEqual(got, []string{"dv_hdr10", "hdr10plus", "hdr10", "hlg", "dv", "sdr"}) {
		t.Fatalf("dynamic range order = %v", got)
	}
	if got := byType[models.CritContainer].Order; !reflect.DeepEqual(got, []string{"mkv", "mp4", "m4v", "m2ts", "other", "avi", "ts"}) {
		t.Fatalf("container order = %v", got)
	}
	if c := byType[models.CritCustomFormatScore]; c.MinDelta != 10 || c.Direction != models.DirectionHigher {
		t.Fatalf("cf score = %+v", c)
	}
	if c := byType[models.CritVideoBitrate]; c.TolerancePercent != 15 {
		t.Fatalf("video bitrate = %+v", c)
	}
	if c := byType[models.CritFileSize]; c.TolerancePercent != 5 || c.Direction != models.DirectionHigher {
		t.Fatalf("file size = %+v", c)
	}
	if c := byType[models.CritDateAdded]; c.Direction != models.DirectionHigher {
		t.Fatalf("date added = %+v", c)
	}
	if ps[1].KeepPer != models.KeepPerResolution || !reflect.DeepEqual(ps[1].Criteria, hq.Criteria) {
		t.Fatalf("one per resolution = %+v", ps[1])
	}
	if got := critTypes(ps[2].Criteria); !reflect.DeepEqual(got, []models.CriterionType{models.CritHealth, models.CritResolution, models.CritVideoCodec, models.CritFileSize}) ||
		ps[2].Criteria[3].Direction != models.DirectionLower || ps[2].Criteria[2].Order[0] != models.VCodecAV1 {
		t.Fatalf("save space = %v", ps[2].Criteria)
	}
	if ps[3].Criteria[1].Type != models.CritVideoCodec || ps[3].Criteria[1].Order[0] != models.VCodecH264 {
		t.Fatalf("max compatibility = %v", ps[3].Criteria)
	}
	if got := critTypes(ps[4].Criteria); !reflect.DeepEqual(got[:4], []models.CriterionType{models.CritHealth, models.CritArrManaged, models.CritCustomFormatScore, models.CritResolution}) ||
		len(got) != len(wantChain) {
		t.Fatalf("trust my arr = %v", got)
	}
	// Fresh values on every call.
	ps[0].Criteria[1].Order[0] = "changed"
	ps[0].Protections[0].Value = "changed"
	again := ProfileTemplates()
	if again[0].Criteria[1].Order[0] != models.Res2160 || again[0].Protections[0].Value != DefaultKeepTag {
		t.Fatal("ProfileTemplates shares state between calls")
	}
}

func TestValidateProfile(t *testing.T) {
	base := func() models.Profile {
		return models.Profile{Name: "Mine", KeepCount: 1, Criteria: []models.Criterion{crit(models.CritResolution)}}
	}
	nan := math.NaN()
	tests := []struct {
		name   string
		mutate func(p *models.Profile)
		want   []string // "property: message fragment"; nil = valid
	}{
		{"valid minimal", func(*models.Profile) {}, nil},
		{"valid empty criteria", func(p *models.Profile) { p.Criteria = nil }, nil},
		{"name required", func(p *models.Profile) { p.Name = "  " }, []string{"name: Name is required"}},
		{"name too long", func(p *models.Profile) { p.Name = strings.Repeat("é", 101) }, []string{"name: Name must be at most 100 characters"}},
		{"keep count", func(p *models.Profile) { p.KeepCount = 0 }, []string{"keepCount: Keep count must be at least 1"}},
		{"keep per", func(p *models.Profile) { p.KeepPer = "codec" }, []string{"keepPer: "}},
		{"keep per valid", func(p *models.Profile) { p.KeepPer = models.KeepPerDynamicRange }, nil},
		{"unknown type", func(p *models.Profile) { p.Criteria = append(p.Criteria, crit("colour")) }, []string{`criteria[1].type: Unknown criterion type "colour"`}},
		{"duplicate type", func(p *models.Profile) { p.Criteria = append(p.Criteria, crit(models.CritResolution)) },
			[]string{"criteria[1].type: Resolution is already used by criteria[0]"}},
		{"duplicate filename score allowed", func(p *models.Profile) {
			fs := withPatterns(crit(models.CritFilenameScore), models.PatternScore{Pattern: "*x*", Score: 1})
			p.Criteria = append(p.Criteria, fs, fs)
		}, nil},
		{"direction", func(p *models.Profile) { p.Criteria = []models.Criterion{withDir(crit(models.CritFileSize), "bigger")} },
			[]string{"criteria[0].direction: "}},
		{"direction valid", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withDir(crit(models.CritFileSize), models.DirectionLower)}
		}, nil},
		{"tolerance negative", func(p *models.Profile) { p.Criteria = []models.Criterion{withTol(crit(models.CritFileSize), -1, 0)} },
			[]string{"criteria[0].tolerancePercent: Tolerance must be between 0 and 100"}},
		{"tolerance too big", func(p *models.Profile) { p.Criteria = []models.Criterion{withTol(crit(models.CritFileSize), 101, 0)} },
			[]string{"criteria[0].tolerancePercent: "}},
		{"tolerance NaN", func(p *models.Profile) { p.Criteria = []models.Criterion{withTol(crit(models.CritFileSize), nan, 0)} },
			[]string{"criteria[0].tolerancePercent: "}},
		{"tolerance unsupported", func(p *models.Profile) { p.Criteria = []models.Criterion{withTol(crit(models.CritResolution), 5, 0)} },
			[]string{"criteria[0].tolerancePercent: Resolution does not support a tolerance"}},
		{"min delta negative", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withTol(crit(models.CritCustomFormatScore), 0, -1)}
		},
			[]string{"criteria[0].minDelta: Minimum delta must be 0 or more"}},
		{"min delta infinite", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withTol(crit(models.CritCustomFormatScore), 0, math.Inf(1))}
		},
			[]string{"criteria[0].minDelta: "}},
		{"min delta unsupported", func(p *models.Profile) { p.Criteria = []models.Criterion{withTol(crit(models.CritBitDepth), 0, 2)} },
			[]string{"criteria[0].minDelta: Bit depth does not support a minimum delta"}},
		{"order unknown value", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withOrder(crit(models.CritResolution), "2160", "8k")}
		},
			[]string{`criteria[0].order[1]: Unknown Resolution value "8k"`}},
		{"order empty value", func(p *models.Profile) { p.Criteria = []models.Criterion{withOrder(crit(models.CritSource), " ")} },
			[]string{"criteria[0].order[0]: Order values must not be empty"}},
		{"order duplicate", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withOrder(crit(models.CritContainer), "mkv", "MKV")}
		},
			[]string{`criteria[0].order[1]: "MKV" is listed more than once`}},
		{"order normalized values accepted", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withOrder(crit(models.CritDynamicRange), "HDR10+", " DV_HDR10 "), withOrder(crit(models.CritAudioFormat), "TrueHD_Atmos")}
		}, nil},
		{"library order ids", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withOrder(crit(models.CritLibrary), "2", "movies", "0")}
		},
			[]string{`criteria[0].order[1]: "movies" is not a library id`, `criteria[0].order[2]: "0" is not a library id`}},
		{"patterns required", func(p *models.Profile) { p.Criteria = []models.Criterion{crit(models.CritFilenameScore)} },
			[]string{"criteria[0].patterns: At least one pattern is required"}},
		{"pattern checks", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withPatterns(crit(models.CritFilenameScore),
				models.PatternScore{Pattern: " "}, models.PatternScore{Pattern: "(", Regex: true}, models.PatternScore{Pattern: "[a"},
				models.PatternScore{Pattern: `\bremux\b`, Regex: true}, models.PatternScore{Pattern: "**/*Remux*"})}
		}, []string{"criteria[0].patterns[0].pattern: Pattern is required", "criteria[0].patterns[1].pattern: Invalid regular expression",
			`criteria[0].patterns[2].pattern: Invalid glob pattern "[a"`}},
		{"audio language requires value", func(p *models.Profile) { p.Criteria = []models.Criterion{crit(models.CritAudioLanguage)} },
			[]string{"criteria[0].value: A language code"}},
		{"audio language unknown value", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withValue(crit(models.CritAudioLanguage), "und")}
		},
			[]string{"criteria[0].value: "}},
		{"audio language disabled without value", func(p *models.Profile) { p.Criteria = []models.Criterion{{Type: models.CritAudioLanguage}} }, nil},
		{"audio language valid", func(p *models.Profile) {
			p.Criteria = []models.Criterion{withValue(crit(models.CritAudioLanguage), "de")}
		}, nil},
		{"protections valid", func(p *models.Profile) {
			p.Protections = []models.Protection{{Type: models.ProtectPathGlob, Value: "/data/4k/**"}, {Type: models.ProtectLibrary, Value: "2"},
				{Type: models.ProtectArrInstance, Value: "3"}, {Type: models.ProtectArrTag, Value: "keep"}}
		}, nil},
		{"protections invalid", func(p *models.Profile) {
			p.Protections = []models.Protection{{Type: models.ProtectPathGlob, Value: ""}, {Type: models.ProtectPathGlob, Value: "/data/[4k"},
				{Type: models.ProtectLibrary}, {Type: models.ProtectArrInstance, Value: " "}, {Type: models.ProtectArrTag}, {Type: "owner", Value: "me"}}
		}, []string{"protections[0].value: A path or glob pattern is required", `protections[1].value: Invalid glob pattern "/data/[4k"`,
			"protections[2].value: A library id is required", "protections[3].value: An *arr instance id is required",
			"protections[4].value: A tag is required", `protections[5].type: Unknown protection type "owner"`}},
		{"several problems at once", func(p *models.Profile) { p.Name, p.KeepCount = "", -1 },
			[]string{"name: Name is required", "keepCount: Keep count must be at least 1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			tc.mutate(&p)
			errs := ValidateProfile(p)
			if tc.want == nil {
				if errs != nil {
					t.Fatalf("ValidateProfile = %v, want nil", errs)
				}
				return
			}
			if len(errs) != len(tc.want) {
				t.Fatalf("ValidateProfile = %v, want %d errors %v", errs, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if got := errs[i].Error(); !strings.HasPrefix(got, w) {
					t.Fatalf("error %d = %q, want prefix %q", i, got, w)
				}
			}
			var _ []config.ValidationError = errs
		})
	}
}

func TestDisplayValue(t *testing.T) {
	r := remux4k(1)
	tests := []struct {
		name string
		t    models.CriterionType
		v    *models.MediaVersion
		want string
	}{
		{"nil version", models.CritResolution, nil, ""},
		{"unknown type", "bogus", &r, ""},
		{"health", models.CritHealth, &r, "Healthy"},
		{"health unanalyzed", models.CritHealth, ptr(ver(1, pathA, unanalyzed())), "Unhealthy: no video codec, width unknown, bitrate unknown"},
		{"health missing", models.CritHealth, ptr(ver(1, pathA, withExists(false), withAccessible(false))), "Unhealthy: file missing, file not accessible"},
		{"health sample", models.CritHealth, ptr(ver(1, "/m/Sample/x.mkv")), "Unhealthy: sample file name"},
		{"resolution", models.CritResolution, &r, "2160p"},
		{"resolution sd", models.CritResolution, ptr(ver(1, pathA, withRes(320, 240, models.ResSD))), "SD"},
		{"resolution from height", models.CritResolution, ptr(ver(1, pathA, withRes(0, 720, ""))), "720p"},
		{"resolution unknown", models.CritResolution, ptr(ver(1, pathA, withRes(0, 0, ""))), "Unknown"},
		{"resolution odd value", models.CritResolution, ptr(ver(1, pathA, withRes(0, 0, "8K"))), "8k"},
		{"dynamic range", models.CritDynamicRange, &r, "Dolby Vision (HDR10)"},
		{"dynamic range profile 8", models.CritDynamicRange, ptr(remux4k(1, func(v *models.MediaVersion) { v.DVProfile = 8 })), "Dolby Vision P8 (HDR10)"},
		{"dynamic range profile 5", models.CritDynamicRange, ptr(ver(1, pathA, withDR(models.DRDolbyVision), func(v *models.MediaVersion) { v.DVProfile = 5 })), "Dolby Vision P5 (no fallback)"},
		{"dynamic range dv", models.CritDynamicRange, ptr(ver(1, pathA, withDR(models.DRDolbyVision))), "Dolby Vision (no fallback)"},
		{"dynamic range hdr10+", models.CritDynamicRange, ptr(ver(1, pathA, withDR(models.DRHDR10Plus))), "HDR10+"},
		{"dynamic range hlg", models.CritDynamicRange, ptr(ver(1, pathA, withDR(models.DRHLG))), "HLG"},
		{"dynamic range hdr10 profile ignored", models.CritDynamicRange, ptr(ver(1, pathA, withDR(models.DRHDR10), func(v *models.MediaVersion) { v.DVProfile = 8 })), "HDR10"},
		{"dynamic range unknown", models.CritDynamicRange, ptr(ver(1, pathA, withDR(""))), "Unknown"},
		{"source", models.CritSource, &r, "Remux"},
		{"source odd", models.CritSource, ptr(ver(1, pathA, withSource("Telesync"))), "telesync"},
		{"video codec", models.CritVideoCodec, &r, "HEVC"},
		{"video codec unknown", models.CritVideoCodec, ptr(ver(1, pathA, withCodec(""))), "Unknown"},
		{"video codec other", models.CritVideoCodec, ptr(ver(1, pathA, withCodec("prores"))), "Other"},
		{"audio format", models.CritAudioFormat, &r, "TrueHD Atmos 7.1"},
		{"audio format no channels", models.CritAudioFormat, ptr(ver(1, pathA, withAudio(models.AudioTrack{Format: "flac"}))), "FLAC"},
		{"audio format none", models.CritAudioFormat, ptr(ver(1, pathA, withAudio())), "Unknown"},
		{"container", models.CritContainer, &r, "MKV"},
		{"container other", models.CritContainer, ptr(ver(1, pathA, withContainer("wmv"))), "Other"},
		{"container unknown", models.CritContainer, ptr(ver(1, "/m/noext", withContainer(""))), "Unknown"},
		{"library", models.CritLibrary, &r, "Movies"},
		{"library id only", models.CritLibrary, ptr(ver(1, pathA, withLib(3, ""))), "Library 3"},
		{"library unknown", models.CritLibrary, ptr(ver(1, pathA, withLib(0, ""))), "Unknown"},
		{"channels", models.CritAudioChannels, &r, "7.1"},
		{"channels stereo", models.CritAudioChannels, ptr(ver(1, pathA, withAudio(track("aac", 2, "")))), "2.0"},
		{"channels odd", models.CritAudioChannels, ptr(ver(1, pathA, withAudio(track("pcm", 10, "")))), "10 ch"},
		{"channels unknown", models.CritAudioChannels, ptr(ver(1, pathA, withAudio())), "Unknown"},
		{"video bitrate", models.CritVideoBitrate, &r, "56.0 Mbps"},
		{"video bitrate kbps", models.CritVideoBitrate, ptr(ver(1, pathA, withBitrate(0, 800))), "800 kbps"},
		{"video bitrate unknown", models.CritVideoBitrate, ptr(ver(1, pathA, withBitrate(0, 0))), "Unknown"},
		{"file size", models.CritFileSize, &r, "62.0 GiB"},
		{"file size small", models.CritFileSize, ptr(ver(1, pathA, withSize(500))), "500 B"},
		{"file size unknown", models.CritFileSize, ptr(ver(1, pathA, withSize(0))), "Unknown"},
		{"bit depth", models.CritBitDepth, &r, "10-bit"},
		{"bit depth unknown", models.CritBitDepth, ptr(ver(1, pathA, withBitDepth(0))), "Unknown"},
		{"cf untracked", models.CritCustomFormatScore, &r, "Not tracked"},
		{"cf unknown", models.CritCustomFormatScore, ptr(ver(1, pathA, tracked(1, "Radarr", 1, nil))), "Unknown"},
		{"cf score", models.CritCustomFormatScore, ptr(ver(1, pathA, tracked(1, "Radarr", 1, intp(-42)))), "-42"},
		{"date added", models.CritDateAdded, &r, "2024-01-01"},
		{"date added arr", models.CritDateAdded, ptr(ver(1, pathA, tracked(1, "Radarr", 1, nil), arrSet(func(a *models.ArrFileInfo) {
			a.DateAdded = time.Date(2025, 2, 3, 23, 0, 0, 0, time.FixedZone("x", -5*3600))
		}))), "2025-02-04"},
		{"date added unknown", models.CritDateAdded, ptr(ver(1, pathA, withAdded(time.Time{}))), "Unknown"},
		{"audio tracks", models.CritAudioTrackCount, &r, "2"},
		{"subtitle tracks", models.CritSubtitleTrackCount, &r, "12"},
		{"arr untracked", models.CritArrManaged, &r, "No"},
		{"arr named", models.CritArrManaged, ptr(ver(1, pathA, tracked(2, "Radarr 4K", 1, nil))), "Radarr 4K"},
		{"arr unnamed sonarr", models.CritArrManaged, ptr(ver(1, pathA, func(v *models.MediaVersion) {
			v.Arr = &models.ArrFileInfo{InstanceID: 3, Kind: models.ArrSonarr}
		})), "Sonarr #3"},
		{"arr unnamed unknown", models.CritArrManaged, ptr(ver(1, pathA, func(v *models.MediaVersion) { v.Arr = &models.ArrFileInfo{} })), "*arr"},
		{"arr unnamed radarr no id", models.CritArrManaged, ptr(ver(1, pathA, func(v *models.MediaVersion) { v.Arr = &models.ArrFileInfo{Kind: models.ArrRadarr} })), "Radarr"},
		{"audio language", models.CritAudioLanguage, &r, "eng"},
		{"audio languages", models.CritAudioLanguage, ptr(ver(1, pathA, withAudio(track("ac3", 6, "ger"), track("ac3", 6, "en"), models.AudioTrack{Language: "Japanese"}))), "eng, ger, jpn"},
		{"audio language unknown", models.CritAudioLanguage, ptr(ver(1, pathA, withAudio(track("ac3", 6, "und")))), "Unknown"},
		{"filename", models.CritFilenameScore, &r, "Dune (2021) Remux-2160p.mkv"},
		{"filename windows", models.CritFilenameScore, ptr(ver(1, `C:\Movies\Dune.mkv`)), "Dune.mkv"},
		{"filename unknown", models.CritFilenameScore, ptr(ver(1, "", withParts())), "Unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayValue(tc.t, tc.v); got != tc.want {
				t.Fatalf("DisplayValue(%s) = %q, want %q", tc.t, got, tc.want)
			}
		})
	}
}

func ptr(v models.MediaVersion) *models.MediaVersion { return &v }

func TestOptionLabels(t *testing.T) {
	tests := []struct {
		t    models.CriterionType
		v    string
		want string
	}{
		{models.CritResolution, "2160", "2160p (4K)"},
		{models.CritResolution, "1440", "1440p (QHD)"},
		{models.CritResolution, "1080", "1080p"},
		{models.CritResolution, "sd", "SD"},
		{models.CritDynamicRange, "dv_hdr10", "Dolby Vision with HDR10 fallback"},
		{models.CritDynamicRange, "dv", "Dolby Vision without fallback (P5)"},
		{models.CritDynamicRange, "sdr", "SDR"},
		{models.CritVideoCodec, "hevc", "HEVC (H.265)"},
		{models.CritVideoCodec, "h264", "H.264 (AVC)"},
		{models.CritVideoCodec, "mpeg4", "MPEG-4 (XviD/DivX)"},
		{models.CritVideoCodec, "vc1", "VC-1"},
		{models.CritAudioFormat, "eac3_atmos", "E-AC-3 Atmos (DD+ Atmos)"},
		{models.CritAudioFormat, "eac3", "E-AC-3 (DD+)"},
		{models.CritAudioFormat, "ac3", "AC-3 (Dolby Digital)"},
		{models.CritAudioFormat, "dts_hd_ma", "DTS-HD MA"},
		{models.CritContainer, "ts", "TS (MPEG-TS / DVR)"},
		{models.CritContainer, "mkv", "MKV"},
		{models.CritContainer, "other", "Other"},
		{models.CritSource, "webrip", "WEBRip"},
	}
	for _, tc := range tests {
		if got := optionLabel(tc.t, tc.v); got != tc.want {
			t.Errorf("optionLabel(%s, %s) = %q, want %q", tc.t, tc.v, got, tc.want)
		}
	}
	// Every fixed option has a human label (not the raw value) except containers/sources that are words.
	for _, c := range CriteriaSchema() {
		for _, o := range c.Options {
			if o.Label == "" {
				t.Errorf("%s/%s: empty label", c.Type, o.Value)
			}
		}
	}
}
