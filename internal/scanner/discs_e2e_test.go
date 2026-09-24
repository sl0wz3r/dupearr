package scanner

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// discE2E runs a full scan with the real Plex and *arr clients against the fakemedia "discs"
// scenario (real disc structures on disk: UHD/Blu-ray BDMV, DVD, ISO, a two-disc set with a bonus
// disc, a damaged disc, a clip Radarr tracks inside a disc, a TV season disc).
type discE2E struct {
	h     *harness
	svc   *Service
	env   *fakemedia.Env
	srv   models.MediaServer
	insts map[string]models.ArrInstance
}

func newDiscE2E(t *testing.T, opts fakemedia.Options) *discE2E {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	if opts.Scenario == nil {
		opts.Scenario = fakemedia.Discs()
	}
	env := fakemedia.StartWithOptions(t, opts)
	h := newHarness(t)
	deps := h.deps
	deps.Now = time.Now
	deps.PlexFactory = func(s models.MediaServer) PlexClient {
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-disc-test", Product: "Dupearr", Version: "test"})
	}
	deps.ArrFactory = func(a models.ArrInstance) ArrClient { return arr.New(a, arr.Options{}) }
	e := &discE2E{h: h, svc: New(deps), env: env, insts: map[string]models.ArrInstance{}}
	ctx := h.ctx
	e.srv = models.MediaServer{Name: "Fake Plex", Kind: models.MediaServerPlex, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
	if err := h.db.MediaServers().Create(ctx, &e.srv); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SyncLibraries(ctx, e.srv.ID); err != nil {
		t.Fatalf("sync libraries: %v", err)
	}
	for name, s := range env.Instances {
		kind := models.ArrRadarr
		if s.Kind == fakemedia.KindSonarr {
			kind = models.ArrSonarr
		}
		a := models.ArrInstance{Name: name, Kind: kind, URL: s.URL, APIKey: s.APIKey, Enabled: true}
		if err := h.db.ArrInstances().Create(ctx, &a); err != nil {
			t.Fatal(err)
		}
		e.insts[name] = a
	}
	for _, m := range env.PathMappings() {
		pm := models.PathMapping{SourceType: models.PathSourceServer, SourceID: e.srv.ID, RemotePath: m.Remote, LocalPath: m.Local}
		if m.Server != fakemedia.ServerPlex {
			pm.SourceType, pm.SourceID = models.PathSourceArr, e.insts[m.Server].ID
		}
		if err := h.db.PathMappings().Create(ctx, &pm); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *discE2E) scan(t *testing.T) *models.ScanRun {
	t.Helper()
	run, err := e.svc.FullScan(e.h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
	if err != nil {
		t.Fatalf("full scan: %v", err)
	}
	if run.Stats.Errors != 0 {
		t.Fatalf("scan errors: %+v", run.Stats)
	}
	return run
}

// fixture returns the fakemedia ground truth of the (non-extras) disc of a title.
func (e *discE2E) fixture(t *testing.T, title string) fakemedia.DiscFixture {
	t.Helper()
	var set []fakemedia.DiscFixture
	for _, f := range e.env.DiscFixtures() {
		if f.Title == title && !f.Extras {
			set = append(set, f)
		}
	}
	if len(set) == 0 {
		t.Fatalf("no disc fixture for %q", title)
	}
	return set[0]
}

func TestDiscsEndToEnd(t *testing.T) {
	e := newDiscE2E(t, fakemedia.Options{})
	e.scan(t)
	h := e.h

	// Blade Runner 2049: UHD BDMV (300 clips) + a 2160p remux tracked by Radarr. The remux wins
	// (source remux > disc); the disc is kept (removal off by default): nothing to remove.
	br := h.group("movie:tmdb:335984")
	if len(br.Files) != 2 || !hasFlag(br, models.FlagFullDisc) {
		t.Fatalf("Blade Runner: files %d flags %v", len(br.Files), br.Flags)
	}
	fx := e.fixture(t, "Blade Runner 2049")
	d := discFile(t, br)
	di := d.Version.Disc
	if di.Type != models.DiscUHDBluray || !di.Readable || di.MainFeature != fakemedia.DiscMainPlaylist ||
		di.FileCount != fx.Files || di.TotalBytes != fx.TotalSize || di.FeatureBytes != fx.FeatureSize ||
		di.Origin != models.DiscOriginFilesystem || di.LocalRoot != fx.LocalRoot || di.Root != fx.Root || !di.Removable {
		t.Fatalf("Blade Runner disc %+v (fixture %+v)", di, fx)
	}
	if d.Version.Key != "disc:"+strconv.FormatInt(e.srv.ID, 10)+":"+disc.RootHash(fx.LocalRoot) {
		t.Fatalf("disc key %s", d.Version.Key)
	}
	if d.Version.Resolution != models.Res2160 || d.Version.DurationMs != fx.DurationMs || d.Version.Source != models.SourceDisc {
		t.Fatalf("disc attributes: %s %d %s", d.Version.Resolution, d.Version.DurationMs, d.Version.Source)
	}
	if d.Rank != 2 || !d.Protected || d.Decision != models.DecisionKeep || br.Status != models.GroupProtected {
		t.Fatalf("Blade Runner: disc rank %d protected %v status %s (%s)", d.Rank, d.Protected, br.Status, br.StatusReason)
	}

	// The Dark Knight: 1080p BD + a WEB-DL. The disc ranks first; the WEB-DL is kept as Plex's
	// playable copy.
	tdk := h.group("movie:tmdb:155")
	if d := discFile(t, tdk); d.Rank != 1 {
		t.Fatalf("Dark Knight: disc rank %d", d.Rank)
	}
	for _, f := range tdk.Files {
		if f.Version.Disc == nil && (f.Decision != models.DecisionKeep || !strings.Contains(f.ProtectedReason, "playable copy")) {
			t.Fatalf("Dark Knight: WEB-DL %s (%q)", f.Decision, f.ProtectedReason)
		}
	}
	if di := discFile(t, tdk).Version.Disc; di.Is3D {
		t.Fatal("Dark Knight: an empty SSIF folder is not 3D")
	}

	// Casablanca: DVD (VIDEO_TS) vs a 1080p MKV.
	cas := h.group("movie:tmdb:289")
	if d := discFile(t, cas); d.Version.Disc.Type != models.DiscDVD || d.Version.Disc.MainFeature != fakemedia.DiscMainTitleSet ||
		d.Version.Resolution != models.Res480 || d.Rank != 2 {
		t.Fatalf("Casablanca disc %+v rank %d", d.Version.Disc, d.Rank)
	}

	// Heat: an ISO — attributes unknown, review.
	heat := h.group("movie:tmdb:949")
	if d := discFile(t, heat); d.Version.Disc.Type != models.DiscISO || heat.Status != models.GroupReview {
		t.Fatalf("Heat: %+v %s", d.Version.Disc, heat.Status)
	}

	// The Fellowship of the Ring: a two-disc set is ONE version; the bonus disc is not a version.
	lotr := h.group("movie:tmdb:120")
	if len(lotr.Files) != 2 {
		t.Fatalf("LotR files %d", len(lotr.Files))
	}
	if d := discFile(t, lotr); d.Version.Disc.Discs != 2 || len(d.Version.Parts) != 2 || strings.Contains(strings.Join(d.Version.Disc.OwnedEntries, "|"), "Bonus") {
		t.Fatalf("LotR disc %+v", d.Version.Disc)
	}

	// Alien: a disc only, no Plex item: nothing to compare.
	h.noGroup("movie:tmdb:348")

	// Gladiator: Radarr tracks the main clip inside the disc.
	gl := h.group("movie:tmdb:98")
	if d := discFile(t, gl); d.Version.Arr == nil || d.Version.Disc.TrackedClip == "" || !hasFlag(gl, models.FlagDiscTracked) {
		t.Fatalf("Gladiator: arr %+v disc %+v flags %v", d.Version.Arr, d.Version.Disc, gl.Flags)
	}

	// Tenet: a damaged disc — unreadable, protected, review.
	tenet := h.group("movie:tmdb:577922")
	if d := discFile(t, tenet); !d.Protected || tenet.Status != models.GroupReview || !hasFlag(tenet, models.FlagDiscUnreadable) {
		t.Fatalf("Tenet: protected %v status %s flags %v", d.Protected, tenet.Status, tenet.Flags)
	}

	// Inception: a standalone .m2ts is an ordinary version (container m2ts), no disc.
	inc := h.group("movie:tmdb:27205")
	for _, f := range inc.Files {
		if f.Version.Disc != nil {
			t.Fatal("Inception: a standalone .m2ts is not a disc")
		}
		if strings.HasSuffix(f.Version.Parts[0].Path, ".m2ts") && f.Version.Container != "m2ts" {
			t.Fatalf("Inception: container %q", f.Version.Container)
		}
	}
	if hasFlag(inc, models.FlagFullDisc) {
		t.Fatalf("Inception flags %v", inc.Flags)
	}
	e.env.AssertNoViolations(t)
	e.env.AssertDiscsIntact(t)
}

func TestDiscsEndToEndDiscImageScanner(t *testing.T) {
	// A custom "Disc Image" scanner lists every BDMV/STREAM clip as a part of one Plex version: it
	// is the disc version (read from disk), never counted twice.
	e := newDiscE2E(t, fakemedia.Options{DiscImageScanner: true})
	e.scan(t)
	br := e.h.group("movie:tmdb:335984")
	if len(br.Files) != 2 {
		t.Fatalf("Blade Runner files %d", len(br.Files))
	}
	d := discFile(t, br)
	fx := e.fixture(t, "Blade Runner 2049")
	if d.Version.Disc.Origin != models.DiscOriginPlex || !strings.HasPrefix(d.Version.Key, "plex:") ||
		len(d.Version.Parts) < 100 || d.Version.Disc.FileCount != fx.Files || !d.Version.Disc.Readable ||
		d.Version.DurationMs != fx.DurationMs {
		t.Fatalf("plex disc: key %s parts %d disc %+v duration %d", d.Version.Key, len(d.Version.Parts), d.Version.Disc, d.Version.DurationMs)
	}
	if hasFlag(br, models.FlagSameFile) || hasFlag(br, models.FlagSample) || hasFlag(br, models.FlagDurationMismatch) {
		t.Fatalf("flags %v", br.Flags)
	}
	// The scanner makes Disc 1 and Disc 2 of a set two Plex versions: never duplicates of each
	// other — both are kept and the group is reviewed.
	lotr := e.h.group("movie:tmdb:120")
	discs := 0
	for _, f := range lotr.Files {
		if f.Version.Disc == nil {
			continue
		}
		discs++
		if !f.Protected || f.Decision != models.DecisionKeep || !strings.Contains(f.Version.Disc.Problem, "of the 2 discs") {
			t.Fatalf("LotR disc of a set: %s protected %v disc %+v", f.Decision, f.Protected, f.Version.Disc)
		}
	}
	if discs != 2 || lotr.Status != models.GroupReview {
		t.Fatalf("LotR: %d discs, status %s (%s)", discs, lotr.Status, lotr.StatusReason)
	}
	e.env.AssertNoViolations(t)
	e.env.AssertDiscsIntact(t)
}
