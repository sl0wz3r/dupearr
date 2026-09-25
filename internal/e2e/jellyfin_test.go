//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Jellyfin 12.1+ as a read-only media server (docs/DECISIONS.md D12, issue #4 Phase 1): the real
// binary against the fake Jellyfin serving the tree of docs/research/jellyfin-emby.md Appendix A.
// Nothing is ever deleted through Jellyfin (the fake records any such request as a violation); a
// copy is removed through its *arr or into Dupearr's recycle bin, only into a recycle bin, only by a
// person's approval of that one group, and Jellyfin is told the exact paths afterwards.

// jfStack is dupearr with the fake Jellyfin (and optionally the fake Plex over the same tree).
type jfStack struct {
	t        *testing.T
	env      *fakemedia.Env
	d        *dupearr
	serverID int64 // the Jellyfin server
	plexID   int64 // the Plex server (jfOptions.plex)
	arrIDs   map[string]int64
	bin      string // Dupearr's recycle bin
}

type jfOptions struct {
	scenario func(*fakemedia.Scenario)
	settings map[string]any
	plex     bool // also add the fake Plex over the same files (docs/DECISIONS.md D11)
	noScan   bool
}

func newJFStack(t *testing.T, o jfOptions) *jfStack {
	t.Helper()
	sc := fakemedia.JellyfinAppendixA()
	if o.scenario != nil {
		o.scenario(sc)
	}
	env := fakemedia.StartWithOptions(t, fakemedia.Options{Scenario: sc, Dir: t.TempDir()})
	t.Cleanup(func() { env.AssertNoViolations(t) })
	s := &jfStack{t: t, env: env, d: startDupearr(t, startOptions{}), arrIDs: map[string]int64{},
		bin: filepath.Join(env.Dir, "dupearr-recycle")}
	d := s.d

	// The connection test the UI runs before saving.
	body := map[string]any{"name": "Jelly", "kind": "jellyfin", "url": env.Jellyfin.URL, "token": env.JellyfinAPIKey, "enabled": true}
	var res struct {
		Product           string `json:"product"`
		Version           string `json:"version"`
		MachineIdentifier string `json:"machineIdentifier"`
		Administrator     bool   `json:"administrator"`
		RemovalsDisabled  string `json:"removalsDisabled"`
	}
	d.expect(http.MethodPost, "/api/v1/mediaserver/test", body, http.StatusOK, &res)
	if res.Product != "Jellyfin Server" || res.Version != fakemedia.DefaultJellyfinVersion || !res.Administrator ||
		res.MachineIdentifier != env.JellyfinServerID || res.RemovalsDisabled != "" {
		t.Fatalf("connection test = %+v", res)
	}
	var ms models.MediaServer
	d.expect(http.MethodPost, "/api/v1/mediaserver", body, http.StatusCreated, &ms)
	if ms.Kind != models.MediaServerJellyfin || ms.Token != "********" || ms.MachineIdentifier != env.JellyfinServerID {
		t.Fatalf("created media server = %+v", ms)
	}
	s.serverID = ms.ID
	if o.plex {
		d.expect(http.MethodPost, "/api/v1/mediaserver", map[string]any{"name": "Plex", "kind": "plex", "url": env.Plex.URL,
			"token": env.PlexToken, "enabled": true}, http.StatusCreated, &ms)
		s.plexID = ms.ID
	}
	for _, name := range []string{fakemedia.InstanceRadarr, fakemedia.InstanceSonarr} {
		srv := env.Instances[name]
		var a models.ArrInstance
		d.expect(http.MethodPost, "/api/v1/arr", map[string]any{"name": srv.InstanceName, "kind": srv.Kind, "url": srv.URL,
			"apiKey": srv.APIKey, "enabled": true}, http.StatusCreated, &a)
		s.arrIDs[name] = a.ID
	}
	for _, m := range env.PathMappings() {
		pm := map[string]any{"remotePath": m.Remote, "localPath": m.Local}
		switch {
		case m.Server == fakemedia.ServerJellyfin:
			pm["sourceType"], pm["sourceId"] = models.PathSourceServer, s.serverID
		case m.Server == fakemedia.ServerPlex && o.plex:
			pm["sourceType"], pm["sourceId"] = models.PathSourceServer, s.plexID
		case s.arrIDs[m.Server] != 0:
			pm["sourceType"], pm["sourceId"] = models.PathSourceArr, s.arrIDs[m.Server]
		default:
			continue
		}
		d.expect(http.MethodPost, "/api/v1/pathmapping", pm, http.StatusCreated, nil)
	}
	for _, sid := range []int64{s.serverID, s.plexID} {
		if sid == 0 {
			continue
		}
		var libs []models.Library
		d.expect(http.MethodGet, fmt.Sprintf("/api/v1/mediaserver/%d/library", sid), nil, http.StatusOK, &libs)
		if len(libs) != 3 {
			t.Fatalf("server %d libraries = %+v, want 3", sid, libs)
		}
		for _, l := range libs {
			d.expect(http.MethodPut, fmt.Sprintf("/api/v1/library/%d", l.ID), map[string]any{"enabled": true}, http.StatusAccepted, nil)
		}
	}
	settings := map[string]any{"minAgeHours": 0, "dryRun": false, "recycleBinPath": s.bin,
		"deletionMethods": []string{"arr", "plex", "filesystem"}}
	for k, v := range o.settings {
		settings[k] = v
	}
	d.putSettings(settings)
	d.waitIdle()
	if !o.noScan {
		s.scan()
	}
	return s
}

func (s *jfStack) scan() { s.t.Helper(); s.d.runCommand(models.CmdDuplicateScan, nil) }

// processQueue runs the queue once and waits for everything it started.
func (s *jfStack) processQueue() {
	s.t.Helper()
	s.d.runCommand(models.CmdProcessQueue, nil)
	s.d.waitIdle()
}

// groupWith returns server sid's group holding a version whose first file is rel (the server's
// own path for it).
func (s *jfStack) groupWith(sid int64, rel string) groupDetail {
	s.t.Helper()
	want := s.env.JellyfinMediaPath(rel)
	if sid == s.plexID {
		want = s.env.PlexMediaPath(rel)
	}
	for _, g := range s.d.groups() {
		gd := s.d.group(g.ID)
		if gd.ServerID != sid {
			continue
		}
		for _, f := range gd.Files {
			if len(f.Version.Parts) > 0 && f.Version.Parts[0].Path == want {
				return gd
			}
		}
	}
	s.t.Fatalf("no group of server %d lists %s:%s", sid, rel, s.d.describeGroups())
	return groupDetail{}
}

// jfGroup returns the Jellyfin group holding rel.
func (s *jfStack) jfGroup(rel string) groupDetail { s.t.Helper(); return s.groupWith(s.serverID, rel) }

// approve approves one group as a person does on its page and waits for the queue run.
func (s *jfStack) approve(g groupDetail) groupDetail {
	s.t.Helper()
	if r := s.d.approve(g.ID, g.Signature); r.Status != http.StatusOK {
		s.t.Fatalf("approve %s: %s", groupLabel(g.DuplicateGroup), r)
	}
	s.d.waitIdle()
	return s.d.group(g.ID)
}

// exists reports whether the media-root relative file is on disk.
func (s *jfStack) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(s.env.MediaRoot, filepath.FromSlash(rel)))
	return err == nil
}

// noJellyfinWrites fails on any Jellyfin request other than a read or the change notification.
func (s *jfStack) noJellyfinWrites() {
	s.t.Helper()
	for _, r := range s.env.RequestsTo(fakemedia.ServerJellyfin) {
		if r.Method != http.MethodGet && !(r.Method == http.MethodPost && r.Path == "/Library/Media/Updated") {
			s.t.Errorf("Jellyfin request %s %s", r.Method, r.Path)
		}
		if strings.Contains(r.Query.Encode(), s.env.JellyfinAPIKey) {
			s.t.Errorf("Jellyfin request %s %s carried the API key in the URL", r.Method, r.Path)
		}
	}
}

// TestJellyfinRemovalAndRestore: a Jellyfin group is approved only by a person and on its own (the
// list's bulk approval and auto mode never take it); the loser moves into Dupearr's recycle bin
// (with its .ignore), Jellyfin is told the exact path, the ghost Jellyfin keeps listing resolves the
// group at the next scan, and a restore tells Jellyfin the file is back.
func TestJellyfinRemovalAndRestore(t *testing.T) {
	t.Parallel()
	s := newJFStack(t, jfOptions{settings: map[string]any{"mode": models.ModeAuto, "stableScansRequired": 1}})
	d := s.d
	s.scan() // auto mode: stable now, and still never approved
	g := s.jfGroup(fakemedia.Alpha1080)
	if g.Status != models.GroupPending || !g.HasFlag(models.FlagManualOnly) || g.HasFlag(models.FlagReportOnly) {
		t.Fatalf("Alpha: %s %v %q", g.Status, g.Flags, g.StatusReason)
	}
	if acts := d.actions(); len(acts) != 0 {
		t.Fatalf("auto mode approved a Jellyfin group: %+v", acts)
	}
	for _, f := range g.Files {
		if !strings.HasPrefix(f.Version.Key, "jellyfin:") || f.Version.SourceID == "" || f.Version.MediaID != 0 {
			t.Fatalf("version %+v", f.Version)
		}
	}
	if res := d.bulk("approve", g.ID); len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "Jellyfin") {
		t.Fatalf("bulk approval of a Jellyfin group: %+v", res)
	}
	d.putSettings(map[string]any{"mode": models.ModeManual})

	s.env.ResetRequests()
	g = s.approve(g)
	if g.Status != models.GroupResolved {
		t.Fatalf("after the approval: %s %q", g.Status, g.StatusReason)
	}
	if s.exists(fakemedia.Alpha1080) || !s.exists(fakemedia.Alpha2160) || !s.exists(fakemedia.AlphaMarker) {
		t.Fatal("the wrong files moved")
	}
	acts := d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionSucceeded || acts[0].Method != models.MethodFilesystem ||
		!strings.HasPrefix(acts[0].RecyclePath, s.bin) {
		t.Fatalf("actions %+v", acts)
	}
	// Jellyfin skips a folder holding an empty .ignore (research S25), Plex one with a .plexignore.
	if fi, err := os.Stat(filepath.Join(s.bin, ".ignore")); err != nil || fi.Size() != 0 {
		t.Errorf("the recycle bin's .ignore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.bin, ".plexignore")); err != nil {
		t.Errorf("the recycle bin's .plexignore: %v", err)
	}
	n := s.env.JellyfinNotifications()
	if len(n) != 1 || len(n[0].Paths) != 1 || n[0].Paths[0] != s.env.JellyfinMediaPath(fakemedia.Alpha1080) || n[0].UpdateType[0] != "Deleted" {
		t.Fatalf("notifications %+v", n)
	}
	s.noJellyfinWrites()

	// Jellyfin still lists the removed file until its monitor re-reads the folder.
	if !s.env.JellyfinListed(fakemedia.Alpha1080) {
		t.Fatal("the fake dropped the ghost before its delay")
	}
	s.scan()
	if g = d.group(g.ID); g.Status != models.GroupResolved {
		t.Fatalf("after the re-scan: %s %q", g.Status, g.StatusReason)
	}

	var restored models.Action
	d.expect(http.MethodPost, fmt.Sprintf("/api/v1/action/%d/restore", acts[0].ID), nil, http.StatusOK, &restored)
	if !s.exists(fakemedia.Alpha1080) || !strings.HasPrefix(restored.Message, "Restored") {
		t.Fatalf("restore: %+v", restored)
	}
	if n := s.env.JellyfinNotifications(); len(n) != 2 || n[1].UpdateType[0] != "Created" || n[1].Paths[0] != n[0].Paths[0] {
		t.Fatalf("notifications after the restore %+v", n)
	}
	s.noJellyfinWrites()
}

// TestJellyfinArrRemoval: a copy Radarr tracks goes through Radarr, and only into Radarr's recycle
// bin; without one the removal is refused and the file stays.
func TestJellyfinArrRemoval(t *testing.T) {
	t.Parallel()
	track := func(bin string) func(*fakemedia.Scenario) {
		return func(sc *fakemedia.Scenario) {
			for i := range sc.Movies {
				for j := range sc.Movies[i].Versions {
					if sc.Movies[i].Versions[j].Parts[0].File == fakemedia.Alpha1080 {
						sc.Movies[i].Versions[j].Tracked = fakemedia.InstanceRadarr
					}
				}
			}
			sc.Instance(fakemedia.InstanceRadarr).RecycleBin = bin
		}
	}
	for _, c := range []struct {
		name, bin string
	}{{"permanent", ""}, {"into its recycle bin", fakemedia.RemoteRoot + "/recycle/radarr"}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newJFStack(t, jfOptions{scenario: track(c.bin), settings: map[string]any{"deletionMethods": []string{"arr"}}})
			g := s.approve(s.jfGroup(fakemedia.Alpha1080))
			acts := s.d.actionsOf(g.ID)
			deletes := requestsMatching(s.env, fakemedia.InstanceRadarr, http.MethodDelete, "moviefile")
			if c.bin == "" {
				if !s.exists(fakemedia.Alpha1080) || len(deletes) != 0 || len(acts) != 1 ||
					!strings.Contains(acts[0].Message, "only removed into a recycle bin") {
					t.Fatalf("without a recycle bin: deletes %d, actions %+v", len(deletes), acts)
				}
				return
			}
			if len(deletes) != 1 || len(acts) != 1 || acts[0].Status != models.ActionSucceeded || acts[0].Method != models.MethodArr {
				t.Fatalf("through Radarr: deletes %d, actions %+v", len(deletes), acts)
			}
			if n := s.env.JellyfinNotifications(); len(n) != 1 || n[0].Paths[0] != s.env.JellyfinMediaPath(fakemedia.Alpha1080) {
				t.Fatalf("notifications %+v", n)
			}
			s.noJellyfinWrites()
		})
	}
}

// TestJellyfinReportOnlyAndProtected: the Lambda group (a local .strm next to its target) is only
// reported, the multi-episode file behind S01E03 is never removed, a stacked loser moves whole, and
// with path substitutions (Jellyfin then reports rewritten paths) the server is not read at all:
// the scan fails with the reason, its groups stay as they were, and Health shows an error.
func TestJellyfinReportOnlyAndProtected(t *testing.T) {
	t.Parallel()
	s := newJFStack(t, jfOptions{scenario: func(sc *fakemedia.Scenario) {
		// A second copy of Lambda makes it a group (the .strm alone is never a version).
		sc.Movies = append(sc.Movies, fakemedia.Movie{Section: fakemedia.SectionMovies, Title: "Lambda", Year: 2019, TmdbID: 621,
			Versions: []fakemedia.Version{{Parts: []fakemedia.Part{{File: fakemedia.LambdaDir + "/Lambda (2019) - 720p.mkv", Size: fakemedia.GiB(3)}},
				Video: fakemedia.HD("h264"), Audio: []fakemedia.Audio{fakemedia.AAC("eng", 2)}, DurationMs: fakemedia.Mins(100)}}})
	}})
	d := s.d

	lambda := s.jfGroup(fakemedia.Lambda1080)
	if lambda.Status != models.GroupProtected || !lambda.HasFlag(models.FlagReportOnly) || !strings.Contains(lambda.StatusReason, ".strm") {
		t.Fatalf("Lambda: %s %v %q", lambda.Status, lambda.Flags, lambda.StatusReason)
	}
	for _, f := range lambda.Files {
		if strings.HasSuffix(f.Version.Parts[0].Path, ".strm") {
			t.Fatal("a .strm is a version")
		}
	}
	if r := d.approve(lambda.ID, lambda.Signature); r.Status == http.StatusOK {
		t.Fatal("a report-only group was approved")
	}

	e3 := s.jfGroup(fakemedia.ShowS01E03)
	for _, f := range e3.Files {
		if strings.HasSuffix(f.Version.Parts[0].Path, "S01E03-E04.mkv") && (f.Decision != models.DecisionKeep || !f.Protected) {
			t.Fatalf("the multi-episode file: %+v", f)
		}
	}
	if e3.Status == models.GroupPending {
		s.approve(e3)
	}
	if !s.exists(fakemedia.ShowS01E0304) {
		t.Fatal("the multi-episode file was removed")
	}

	kappa := s.approve(s.jfGroup(fakemedia.KappaMain))
	if kappa.Status != models.GroupResolved || s.exists(fakemedia.KappaCD1) || s.exists(fakemedia.KappaCD2) || !s.exists(fakemedia.KappaMain) {
		t.Fatalf("Kappa: %s %q — the stacked loser must move whole", kappa.Status, kappa.StatusReason)
	}

	before := s.jfGroup(fakemedia.BetaMain)
	s.env.SetJellyfinPathSubstitutions(fakemedia.JellyfinPathSubstitution{From: "/data/media", To: `\\nas\media`})
	if c := d.waitCommand(d.startCommand(models.CmdDuplicateScan, nil).ID); c.Status == models.CommandCompleted || !strings.Contains(c.Message, "path substitutions") {
		t.Fatalf("scan with path substitutions: %s %q", c.Status, c.Message)
	}
	d.waitIdle()
	if beta := d.group(before.ID); beta.Status != before.Status || !s.exists(fakemedia.BetaMain) || !s.exists(fakemedia.Beta720) {
		t.Fatalf("Beta with path substitutions: %s %q (was %s)", beta.Status, beta.StatusReason, before.Status)
	}
	d.runCommand(models.CmdCheckHealth, nil)
	found := false
	for _, c := range d.health() {
		found = found || (c.Source == "JellyfinServerCheck" && c.Type == models.HealthError && strings.Contains(c.Message, "Path substitutions"))
	}
	if !found {
		t.Fatalf("no health error for the path substitutions: %+v", d.health())
	}
	s.noJellyfinWrites()
}

// TestJellyfinRunChecks: a session playing the alternate (by its media source) defers the
// removal; a server answering with another id is never acted on.
func TestJellyfinRunChecks(t *testing.T) {
	t.Parallel()
	s := newJFStack(t, jfOptions{})
	d := s.d
	alpha := s.jfGroup(fakemedia.Alpha1080)
	s.env.SetJellyfinSessions(fakemedia.JellyfinSession{ItemID: s.env.JellyfinRowID(fakemedia.Alpha2160),
		MediaSourceID: s.env.JellyfinSourceID(fakemedia.Alpha1080), Paused: true})
	g := s.approve(alpha)
	if g.Status != models.GroupQueued || !g.HasFlag(models.FlagPlaying) || !s.exists(fakemedia.Alpha1080) {
		t.Fatalf("while playing: %s %v %q", g.Status, g.Flags, g.StatusReason)
	}
	s.env.SetJellyfinSessions()
	s.processQueue()
	if g = d.group(g.ID); g.Status != models.GroupResolved || s.exists(fakemedia.Alpha1080) {
		t.Fatalf("after playback stopped: %s %q", g.Status, g.StatusReason)
	}

	beta := s.jfGroup(fakemedia.BetaMain)
	if _, remove := filesOf(beta); len(remove) != 1 {
		t.Fatalf("Beta removals %+v", remove)
	}
	s.env.SetJellyfinServerID("0000000000000000000000000000beef")
	beta = s.approve(beta)
	if !s.exists(fakemedia.BetaMain) || !s.exists(fakemedia.Beta720) || !strings.Contains(beta.StatusReason, "answers as Jellyfin server") {
		t.Fatalf("another server: %s %q", beta.Status, beta.StatusReason)
	}
	s.noJellyfinWrites()
}

// TestJellyfinAndPlexOnOneShare (docs/DECISIONS.md D11 across kinds): with Plex and Jellyfin over
// the same files, a Plex removal of a file Jellyfin lists relies on the Jellyfin item keeping a
// different copy — which only a filesystem that can prove two paths different files allows — and
// Jellyfin is never asked to delete anything.
func TestJellyfinAndPlexOnOneShare(t *testing.T) {
	t.Parallel()
	s := newJFStack(t, jfOptions{plex: true})
	pg := s.groupWith(s.plexID, fakemedia.Alpha2160)
	if pg.CrossServer == nil || !pg.CrossServer.Complete {
		t.Fatalf("Plex Alpha: %s %q, cross-server record %+v", pg.Status, pg.StatusReason, pg.CrossServer)
	}
	fingerprints := 0
	for _, l := range pg.CrossServer.Libraries {
		if l.ServerID == s.serverID && l.Fingerprint != "" {
			fingerprints++
		}
	}
	if fingerprints == 0 {
		t.Fatalf("no Jellyfin library recorded its listing fingerprint: %+v", pg.CrossServer.Libraries)
	}
	info, err := fileid.Default().PathInfo(filepath.Join(s.env.MediaRoot, filepath.FromSlash(fakemedia.Alpha1080)))
	if err != nil || !info.Allowlisted {
		// The tree's filesystem cannot prove the 1080p and the 2160p different files: the Plex
		// group keeps both.
		t.Logf("the test tree's filesystem (%q) cannot prove different files: checking the protection", info.FSType)
		f := fileContaining(t, pg, "1080p")
		if pg.Status != models.GroupProtected || !f.Protected || !strings.Contains(f.ProtectedReason, "on Jelly") ||
			!strings.Contains(f.ProtectedReason, "cannot be proven a different file") {
			t.Fatalf("Plex Alpha on a filesystem without file identities: %s %+v", pg.Status, f)
		}
		if r := s.d.approve(pg.ID, pg.Signature); r.Status == http.StatusOK {
			t.Fatal("a group with nothing to remove was approved")
		}
		return
	}
	t.Logf("the test tree's filesystem (%q) proves different files: checking the removal", info.FSType)
	if pg.Status != models.GroupPending || !pg.HasFlag(models.FlagOtherServerListing) {
		t.Fatalf("Plex Alpha: %s %v %q", pg.Status, pg.Flags, pg.StatusReason)
	}
	pg = s.approve(pg)
	if pg.Status != models.GroupResolved || s.exists(fakemedia.Alpha1080) || !s.exists(fakemedia.Alpha2160) {
		t.Fatalf("Plex Alpha: %s %q", pg.Status, pg.StatusReason)
	}
	s.noJellyfinWrites()
}
