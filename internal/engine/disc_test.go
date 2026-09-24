package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Full-disc backups (docs/research/disc-structures.md §6, docs/DECISIONS.md D9).

// discVer builds a readable, removable 1080p Blu-ray disc found on disk in the Dune folder: one
// synthetic part (the disc root), 45 GiB on disk, 38 GiB main feature.
func discVer(opts ...vopt) models.MediaVersion {
	root := "/data/movies/Dune (2021)"
	v := models.MediaVersion{
		Key:          "disc:1:0123456789abcdef0123456789abcdef01234567",
		ServerID:     1,
		LibraryID:    1,
		LibraryTitle: "Movies",
		SectionKey:   "1",
		RatingKey:    "100",
		ItemTitle:    "Dune",
		Parts:        []models.MediaPart{{Path: root, LocalPath: "/mnt" + root, Size: 45 * gib}},
		Container:    "disc",
		DurationMs:   9_300_000,
		BitrateKbps:  33000,
		VideoBitrate: 33000,
		Width:        1920,
		Height:       1080,
		Resolution:   models.Res1080,
		VideoCodec:   models.VCodecH264,
		BitDepth:     8,
		DynamicRange: models.DRSDR,
		AudioTracks: []models.AudioTrack{
			{Format: models.AudioTrueHD, Codec: "truehd", Channels: 0, LanguageCode: "eng", Default: true},
			{Format: models.AudioAC3, Codec: "ac3", Channels: 0, LanguageCode: "fre"},
		},
		Source:  models.SourceDisc,
		AddedAt: tOld,
		Disc: &models.DiscInfo{
			Type: models.DiscBluray, Root: root, LocalRoot: "/mnt" + root, Discs: 1, FileCount: 312,
			MainFeature: "BDMV/PLAYLIST/00800.mpls", Readable: true, Origin: models.DiscOriginFilesystem,
			Roots: []string{root}, LocalRoots: []string{"/mnt" + root},
			OwnedEntries: []string{"/mnt" + root + "/BDMV", "/mnt" + root + "/CERTIFICATE"},
			TotalBytes:   45 * gib, FeatureBytes: 38 * gib, FreedBytes: 45 * gib, Removable: true,
			Fingerprint: "f1",
		},
	}
	for _, o := range opts {
		o(&v)
	}
	return v
}

func discEnv(allowRemoval, keepPlayable bool) EvalEnv {
	e := env()
	e.AllowDiscRemoval, e.KeepPlayableCopy = allowRemoval, keepPlayable
	return e
}

func withDisc(fn func(d *models.DiscInfo)) vopt { return func(v *models.MediaVersion) { fn(v.Disc) } }

const discKey = "disc:1:0123456789abcdef0123456789abcdef01234567"

func TestDiscDefaultOrdersAndSchema(t *testing.T) {
	if got := defaultSourceOrder(); !slices.Equal(got[:3], []string{models.SourceRemux, models.SourceDisc, models.SourceBluray}) {
		t.Fatalf("default source order %v: disc must follow remux", got)
	}
	if got := defaultContainerOrder(); !slices.Equal(got, []string{"mkv", "mp4", "m4v", "m2ts", "other", "avi", "ts"}) {
		t.Fatalf("default container order %v", got)
	}
	labels := map[string]string{}
	for _, s := range CriteriaSchema() {
		for _, o := range s.Options {
			labels[string(s.Type)+"/"+o.Value] = o.Label
		}
	}
	if got := labels["source/disc"]; got != "Full disc (BDMV/VIDEO_TS/ISO)" {
		t.Errorf("source option disc label %q", got)
	}
	if got := labels["container/m2ts"]; got != "M2TS" {
		t.Errorf("container option m2ts label %q", got)
	}
	// Every template ranks a disc right after a remux.
	for _, p := range ProfileTemplates() {
		for _, c := range p.Criteria {
			if c.Type == models.CritSource {
				i, j := slices.Index(c.Order, models.SourceRemux), slices.Index(c.Order, models.SourceDisc)
				if i < 0 || j != i+1 {
					t.Errorf("template %q source order %v", p.Name, c.Order)
				}
			}
			if c.Type == models.CritContainer && !slices.Contains(c.Order, "m2ts") {
				t.Errorf("template %q container order %v lacks m2ts", p.Name, c.Order)
			}
		}
		if errs := ValidateProfile(p); len(errs) > 0 {
			t.Errorf("template %q invalid: %v", p.Name, errs)
		}
	}
	if got := DisplayValue(models.CritSource, vptr(discVer())); got != "Full disc" {
		t.Errorf("source display %q", got)
	}
	if got := DisplayValue(models.CritContainer, vptr(discVer())); got != "Full disc" {
		t.Errorf("container display %q", got)
	}
	if got := DisplayValue(models.CritFileSize, vptr(discVer())); !strings.Contains(got, "38.0 GiB feature") || !strings.Contains(got, "45.0 GiB on disk") {
		t.Errorf("file size display %q", got)
	}
	if got := normContainer(vptr(ver(1, "/m/Inception (2010).m2ts", withContainer("")))); got != "m2ts" {
		t.Errorf("standalone .m2ts container %q", got)
	}
}

func vptr(v models.MediaVersion) *models.MediaVersion { return &v }

func TestBuildGroupsDiscNextToMKV(t *testing.T) {
	// Plex lists only the MKV; the scanner adds the disc found in the same folder. One group,
	// flag full_disc, and the disc root is not mistaken for another title's folder.
	item := movieItem("100", 1, map[string]string{"tmdb": "438631"}, web1080(1), discVer())
	gs := BuildGroups([]models.MediaItem{item}, GroupOptions{})
	if len(gs) != 1 || len(gs[0].Files) != 2 {
		t.Fatalf("groups %+v", gs)
	}
	g := gs[0]
	if !g.HasFlag(models.FlagFullDisc) {
		t.Errorf("flags %v lack full_disc", g.Flags)
	}
	for _, f := range []string{models.FlagSuspectMerge, models.FlagSameFile, models.FlagStacked, models.FlagDiscUnreadable, models.FlagDiscTracked} {
		if g.HasFlag(f) {
			t.Errorf("unexpected flag %s in %v", f, g.Flags)
		}
	}
	if !BlocksAutoApproval(models.FlagFullDisc) || !BlocksAutoApproval(models.FlagDiscUnreadable) || !BlocksAutoApproval(models.FlagDiscTracked) {
		t.Error("disc flags must keep a group out of auto mode")
	}
}

func TestDiscMultiDiscSetAnd3D(t *testing.T) {
	set := discVer(withParts(
		models.MediaPart{Path: "/data/movies/Dune (2021)/Disc 1", Size: 30 * gib},
		models.MediaPart{Path: "/data/movies/Dune (2021)/Disc 2", Size: 25 * gib},
	), withDisc(func(d *models.DiscInfo) {
		d.Root, d.Discs, d.Roots = "/data/movies/Dune (2021)/Disc 1", 2,
			[]string{"/data/movies/Dune (2021)/Disc 1", "/data/movies/Dune (2021)/Disc 2"}
	}))
	item := movieItem("100", 1, map[string]string{"tmdb": "438631"}, web1080(1), set)
	gs := BuildGroups([]models.MediaItem{item}, GroupOptions{})
	if len(gs) != 1 || gs[0].HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("multi-disc set: %+v", gs)
	}

	// A 3D disc is a 3D variant (a separate group when 3D is distinct).
	d3 := discVer(withDisc(func(d *models.DiscInfo) { d.Is3D = true }))
	item = movieItem("100", 1, map[string]string{"tmdb": "438631"}, web1080(1), web1080(2, withKey(key(2)),
		withParts(models.MediaPart{Path: "/data/movies/Dune (2021)/Dune (2021) WEBDL-720p.mkv", Size: 3 * gib})), d3)
	gs = BuildGroups([]models.MediaItem{item}, GroupOptions{Treat3DAsDistinct: true})
	for _, g := range gs {
		for _, f := range g.Files {
			if f.Version.Key == discKey {
				t.Fatalf("a 3D disc was grouped with 2D copies: %s", g.Key)
			}
		}
	}
}

func TestDiscProtectedUnlessRemovalAllowed(t *testing.T) {
	p := profile(crit(models.CritHealth), models.Criterion{Type: models.CritSource, Enabled: true})
	g := mustEval(t, group(remux4k(1), discVer()), p, discEnv(false, true))
	d := fileByKey(t, g, discKey)
	if d.Decision != models.DecisionKeep || !d.Protected || !strings.Contains(d.ProtectedReason, "disc removal is off") {
		t.Fatalf("disc with removal off: decision %s protected %v reason %q", d.Decision, d.Protected, d.ProtectedReason)
	}
	if g.Status != models.GroupProtected {
		t.Fatalf("status %s (%s)", g.Status, g.StatusReason)
	}

	g = mustEval(t, group(remux4k(1), discVer()), p, discEnv(true, true))
	d = fileByKey(t, g, discKey)
	if d.Decision != models.DecisionRemove || d.Protected {
		t.Fatalf("disc with removal allowed: decision %s protected %v (%q)", d.Decision, d.Protected, d.ProtectedReason)
	}
	if g.Status != models.GroupPending || !g.HasFlag(models.FlagFullDisc) {
		t.Fatalf("status %s flags %v", g.Status, g.Flags)
	}
	if err := ValidateDecisions(g); err != nil {
		t.Fatalf("a whole-disc removal is valid: %v", err)
	}
	// Reclaimable space is what moving the disc frees (hardlinked files free nothing).
	g.Files[1].Version.Disc.FreedBytes = 3 * gib
	g = mustEval(t, g, p, discEnv(true, true))
	if g.ReclaimableBytes != 3*gib {
		t.Fatalf("reclaimable %d, want the disc's freed bytes", g.ReclaimableBytes)
	}
}

func TestDiscNeverRemovableWhenUnsafe(t *testing.T) {
	p := profile(crit(models.CritHealth), crit(models.CritSource))
	cases := map[string]struct {
		opt  vopt
		want string
	}{
		"not verifiable": {withDisc(func(d *models.DiscInfo) { d.Removable, d.Problem = false, "symbolic link (never followed)" }), "cannot be removed safely"},
		"unreachable":    {withDisc(func(d *models.DiscInfo) { d.LocalRoot, d.OwnedEntries = "", nil }), "cannot reach"},
		"shared":         {withDisc(func(d *models.DiscInfo) { d.PlexItems = []string{"555"} }), "other Plex items"},
		"hd dvd":         {withDisc(func(d *models.DiscInfo) { d.Type = models.DiscHDDVD }), "never removed"},
	}
	for name, tc := range cases {
		g := mustEval(t, group(remux4k(1), discVer(tc.opt)), p, discEnv(true, true))
		d := fileByKey(t, g, discKey)
		if d.Decision != models.DecisionKeep || !d.Protected || !strings.Contains(d.ProtectedReason, tc.want) {
			t.Errorf("%s: decision %s protected %v reason %q (want %q)", name, d.Decision, d.Protected, d.ProtectedReason, tc.want)
		}
	}
}

func TestDiscUnreadableGoesToReview(t *testing.T) {
	bad := discVer(withDisc(func(d *models.DiscInfo) {
		d.Readable, d.Removable, d.Problem, d.MainFeature = false, false, "disc metadata could not be read", ""
	}), func(v *models.MediaVersion) {
		v.VideoCodec, v.Width, v.Height, v.Resolution, v.BitrateKbps, v.VideoBitrate = "", 0, 0, "", 0, 0
	})
	g := mustEval(t, group(web1080(1), bad), profile(crit(models.CritHealth)), discEnv(true, false))
	if !g.HasFlag(models.FlagDiscUnreadable) || g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "full-disc backup could not be read") {
		t.Fatalf("flags %v status %s reason %q", g.Flags, g.Status, g.StatusReason)
	}
	d := fileByKey(t, g, discKey)
	if !d.Protected || d.Decision != models.DecisionKeep {
		t.Fatalf("an unreadable disc must be protected: %+v", d)
	}
	if !containsSub(d.Reasons, "disc metadata unreadable") {
		t.Errorf("health reasons %v", d.Reasons)
	}

	// An ISO is not "unreadable", its quality is simply unknown: review, never auto.
	iso := discVer(withParts(models.MediaPart{Path: "/data/movies/Dune (2021)/Dune (2021).iso", Size: 40 * gib}),
		withDisc(func(d *models.DiscInfo) {
			d.Type, d.Root, d.Readable, d.MainFeature, d.FeatureBytes = models.DiscISO, "/data/movies/Dune (2021)/Dune (2021).iso", false, "", 40*gib
		}), func(v *models.MediaVersion) {
			v.VideoCodec, v.Width, v.Height, v.Resolution, v.BitrateKbps, v.VideoBitrate, v.AudioTracks = "", 0, 0, "", 0, 0, nil
		})
	g = mustEval(t, group(web1080(1), iso), profile(crit(models.CritHealth)), discEnv(true, false))
	if g.HasFlag(models.FlagDiscUnreadable) || !g.HasFlag(models.FlagUnanalyzed) || g.Status != models.GroupReview ||
		!strings.Contains(g.StatusReason, "quality of a full-disc backup is unknown") {
		t.Fatalf("ISO: flags %v status %s reason %q", g.Flags, g.Status, g.StatusReason)
	}
}

func TestKeepPlayableCopy(t *testing.T) {
	p := profile(crit(models.CritHealth), crit(models.CritResolution), crit(models.CritSource))
	// The disc ranks first (source disc > webdl): the best non-disc copy is kept as well.
	g := mustEval(t, group(web1080(1), web1080(2, withKey(key(2)), withSource(models.SourceWebRip),
		withParts(models.MediaPart{Path: "/data/movies/Dune (2021)/Dune.WEBRip.mkv", Size: 5 * gib})), discVer()), p, discEnv(true, true))
	if d := fileByKey(t, g, discKey); d.Rank != 1 || d.Decision != models.DecisionKeep {
		t.Fatalf("disc rank %d decision %s", d.Rank, d.Decision)
	}
	web := fileByKey(t, g, key(1))
	if web.Decision != models.DecisionKeep || !strings.Contains(web.ProtectedReason, "kept so Plex has a playable copy") {
		t.Fatalf("playable copy: decision %s reason %q", web.Decision, web.ProtectedReason)
	}
	if rip := fileByKey(t, g, key(2)); rip.Decision != models.DecisionRemove {
		t.Fatalf("the other regular copy is still removed: %s", rip.Decision)
	}
	// A person cannot override the playable copy away.
	web.Override = models.DecisionRemove
	g = mustEval(t, g, p, discEnv(true, true))
	if web = fileByKey(t, g, key(1)); web.Decision != models.DecisionKeep {
		t.Fatalf("override removed the only playable copy")
	}

	// Setting off: the disc may be the only kept copy.
	g = mustEval(t, group(web1080(1), discVer()), p, discEnv(true, false))
	if web = fileByKey(t, g, key(1)); web.Decision != models.DecisionRemove {
		t.Fatalf("keepPlayableCopy off: web decided %s", web.Decision)
	}
	// A regular copy ranking first needs no help.
	g = mustEval(t, group(remux4k(1), discVer()), p, discEnv(true, true))
	if r := fileByKey(t, g, key(1)); r.Decision != models.DecisionKeep || r.Protected {
		t.Fatalf("remux keeper: %+v", r)
	}
}

func TestDiscRankingRules(t *testing.T) {
	// file_size compares the main feature, never the whole disc (menus/extras would always win).
	big := remux4k(1, withRes(1920, 1080, models.Res1080), withDR(models.DRSDR), withSize(40*gib))
	g := mustEval(t, group(big, discVer()), profile(numCrit(models.CritFileSize)), discEnv(true, false))
	if f := fileByKey(t, g, key(1)); f.Rank != 1 {
		t.Fatalf("file_size: 40 GiB remux lost to a 38 GiB feature (disc 45 GiB on disk)")
	}
	// container: a disc ties with everything (it has no container).
	g = mustEval(t, group(ver(1, "/m/a.ts", withContainer("ts")), discVer()),
		profile(crit(models.CritContainer), crit(models.CritSource)), discEnv(true, false))
	if f := fileByKey(t, g, discKey); f.Rank != 1 || f.DecidingCriterion == string(models.CritContainer) {
		t.Fatalf("container decided against a disc: rank %d decider %s", f.Rank, f.DecidingCriterion)
	}
	// audio_channels: a disc's multichannel count is unknown (0): a tie, not "fewer".
	g = mustEval(t, group(web1080(1, withSource(models.SourceRemux)), discVer()),
		profile(numCrit(models.CritAudioChannels), crit(models.CritSource)), discEnv(true, false))
	if f := fileByKey(t, g, key(1)); f.Rank != 1 || f.DecidingCriterion != string(models.CritSource) {
		t.Fatalf("audio_channels: rank %d decider %s", f.Rank, f.DecidingCriterion)
	}
	// audio_format: TrueHD on a disc may be Atmos (not recorded in the disc metadata): a tie with
	// TrueHD Atmos.
	atmos := web1080(1, withSource(models.SourceRemux), withAudio(models.AudioTrack{Format: models.AudioTrueHDAtmos, Channels: 8, Atmos: true, LanguageCode: "eng"}))
	g = mustEval(t, group(atmos, discVer()), profile(crit(models.CritAudioFormat), crit(models.CritSource)), discEnv(true, false))
	if f := fileByKey(t, g, key(1)); f.Rank != 1 || f.DecidingCriterion != string(models.CritSource) {
		t.Fatalf("audio_format: rank %d decider %s", f.Rank, f.DecidingCriterion)
	}
	// Default chain: remux > disc > WEB-DL.
	g = mustEval(t, group(web1080(1), discVer(), remux4k(3, withRes(1920, 1080, models.Res1080), withDR(models.DRSDR))),
		ProfileTemplates()[0], discEnv(true, false))
	if fileByKey(t, g, key(3)).Rank != 1 || fileByKey(t, g, discKey).Rank != 2 || fileByKey(t, g, key(1)).Rank != 3 {
		t.Fatalf("default ranks remux %d disc %d web %d", fileByKey(t, g, key(3)).Rank, fileByKey(t, g, discKey).Rank, fileByKey(t, g, key(1)).Rank)
	}
}

func numCrit(t models.CriterionType) models.Criterion {
	return models.Criterion{Type: t, Enabled: true, Direction: models.DirectionHigher}
}

func TestDiscTVAlwaysProtected(t *testing.T) {
	g := group(web1080(1), discVer())
	g.MediaType = models.MediaTypeEpisode
	g = mustEval(t, g, profile(crit(models.CritSource)), discEnv(true, false))
	d := fileByKey(t, g, discKey)
	if !d.Protected || d.Decision != models.DecisionKeep || !strings.Contains(d.ProtectedReason, "TV") {
		t.Fatalf("TV disc: %+v", d)
	}
	// Forcing the decision is refused by the invariants.
	d.Decision, d.Protected = models.DecisionRemove, false
	fileByKey(t, g, key(1)).Decision = models.DecisionKeep
	if err := ValidateDecisions(g); !errors.Is(err, ErrInvariant) || !strings.Contains(err.Error(), "TV") {
		t.Fatalf("ValidateDecisions: %v", err)
	}
}

func TestPerFileDiscGuard(t *testing.T) {
	// A regular version whose file lies inside a disc structure (e.g. an *arr-tracked clip Plex
	// lists on its own) is never removed on its own, whatever the ranking says.
	clip := ver(2, "/data/movies/Dune (2021)/BDMV/STREAM/00800.m2ts", withSource(models.SourceWebRip), withContainer("m2ts"))
	g := mustEval(t, group(remux4k(1), clip), profile(crit(models.CritSource)), discEnv(true, false))
	f := fileByKey(t, g, key(2))
	if !f.Protected || f.Decision != models.DecisionKeep || !strings.Contains(f.ProtectedReason, "inside a full-disc backup") {
		t.Fatalf("disc member: %+v", f)
	}
	f.Decision, f.Protected = models.DecisionRemove, false
	if err := ValidateDecisions(g); !errors.Is(err, ErrInvariant) || !strings.Contains(err.Error(), "inside a full-disc backup") {
		t.Fatalf("ValidateDecisions: %v", err)
	}
	// The same through its local path only.
	loc := ver(3, "/srv/x.m2ts", withLocal("/mnt/Dune/VIDEO_TS/VTS_01_1.VOB"))
	g = group(remux4k(1), loc)
	g.Files[0].Decision, g.Files[1].Decision = models.DecisionKeep, models.DecisionRemove
	if err := ValidateDecisions(g); !errors.Is(err, ErrInvariant) {
		t.Fatalf("local disc path: %v", err)
	}
}

func TestDiscTrackedClipFlag(t *testing.T) {
	tracked := discVer(withArr(3, "Radarr", 44, 55), withDisc(func(d *models.DiscInfo) {
		d.TrackedClip = "/movies/Dune (2021)/BDMV/STREAM/00800.m2ts"
	}))
	g := mustEval(t, group(web1080(1), tracked), profile(crit(models.CritSource)), discEnv(false, true))
	if !g.HasFlag(models.FlagDiscTracked) {
		t.Fatalf("flags %v", g.Flags)
	}
	// The *arr file of a disc is never the *arr's "quality": the disc keeps its own attributes.
	if d := fileByKey(t, g, discKey); d.Version.Source != models.SourceDisc {
		t.Fatalf("source %s", d.Version.Source)
	}
}

func TestSameDiscTwiceIsSameFile(t *testing.T) {
	// The disc found on disk and a custom scanner's Plex version of the same disc (or a clip of it)
	// are one set of files: never split into keep and remove.
	plexDisc := ver(9, "/data/movies/Dune (2021)/BDMV/STREAM/00800.m2ts", withSource(models.SourceDisc),
		func(v *models.MediaVersion) {
			v.Disc = &models.DiscInfo{Type: models.DiscBluray, Root: "/data/movies/Dune (2021)", Origin: models.DiscOriginPlex, Discs: 1}
		})
	item := movieItem("100", 1, map[string]string{"tmdb": "438631"}, web1080(1), discVer(), plexDisc)
	gs := BuildGroups([]models.MediaItem{item}, GroupOptions{})
	if len(gs) != 1 || !gs[0].HasFlag(models.FlagSameFile) {
		t.Fatalf("same disc twice: %+v", gs)
	}
}
