//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Scenario 6: safety. Each test runs its own stack; every stack also fails on any request the
// fake servers classify as a safety violation (deleting a playing item, the last copy, a
// multi-episode file another episode needs, an optimized version, …).

var realRemovals = map[string]any{"dryRun": false, "deletionMethods": []string{"arr", "filesystem", "plex"}}

// TestApprovalGuards checks the API refusals: review groups are never bulk-approved, protected
// groups and groups without removals cannot be approved, overrides that leave no keeper or remove
// a shared multi-episode file are refused, and a stale signature is a 409. Nothing reaches the
// fake servers.
func TestApprovalGuards(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{recycleBin: true, settings: realRemovals})
	d := s.d
	s.env.ResetRequests()

	t.Run("review groups are not bulk-approved", func(t *testing.T) {
		review := withStatus(d.groups(), "review")
		if len(review) != 4 {
			t.Fatalf("review groups: %d%s", len(review), d.describeGroups())
		}
		ids := make([]int64, 0, len(review))
		for _, g := range review {
			ids = append(ids, g.ID)
		}
		res := d.bulk("approve", ids...)
		if len(res.Succeeded) != 0 || len(res.Failed) != len(ids) {
			t.Fatalf("bulk approve of review groups: %+v", res)
		}
		for _, f := range res.Failed {
			if !strings.Contains(f.Message, "needs a review") {
				t.Errorf("group %d: %q", f.ID, f.Message)
			}
		}
		for _, g := range withStatus(d.groups(), "review") {
			if len(d.actionsOf(g.ID)) != 0 {
				t.Errorf("%s got actions", g.label())
			}
		}
		if n := len(d.commandsNamed(models.CmdProcessQueue)); n != 0 {
			t.Errorf("a refused bulk approval queued %d ProcessQueue run(s)", n)
		}
	})
	t.Run("nothing to remove", func(t *testing.T) {
		for _, label := range []string{"Dune", "Interstellar"} { // protected; review with keep-only
			r := d.approve(d.groupID(label), "")
			if r.Status != http.StatusBadRequest || !strings.Contains(r.message(), "Nothing to remove") {
				t.Errorf("approve %s: %s, want 400 Nothing to remove", label, r)
			}
		}
	})
	t.Run("override removing every keeper", func(t *testing.T) {
		g := d.groupNamed("Blade Runner 2049")
		keep, _ := filesOf(g)
		r := d.request(http.MethodPut, fmt.Sprintf("/api/v1/duplicate/%d/file/%d/override", g.ID, keep[0].ID), map[string]any{"decision": "remove"})
		if r.Status != http.StatusBadRequest || !strings.Contains(r.message(), "no version would be kept") {
			t.Fatalf("override: %s, want 400", r)
		}
		after := d.group(g.ID)
		if after.Signature != g.Signature || fileContaining(t, after, "Remux-2160p").Override != "" {
			t.Errorf("a refused override changed the group")
		}
	})
	t.Run("multi-episode file cannot be marked for removal", func(t *testing.T) {
		g := d.groupNamed("The Expanse S01E01")
		multi := fileContaining(t, g, "S01E01-E02")
		if !multi.Protected || multi.Decision != models.DecisionKeep {
			t.Fatalf("multi-episode version = %+v, want a protected keeper", multi)
		}
		r := d.request(http.MethodPut, fmt.Sprintf("/api/v1/duplicate/%d/file/%d/override", g.ID, multi.ID), map[string]any{"decision": "remove"})
		if r.Status != http.StatusBadRequest || !strings.Contains(r.message(), "shares its file with other episodes") {
			t.Fatalf("override: %s, want 400", r)
		}
		if after := d.group(g.ID); after.Signature != g.Signature {
			t.Errorf("a refused override changed the group")
		}
	})
	t.Run("signature mismatch", func(t *testing.T) {
		g := d.groupNamed("Blade Runner 2049")
		r := d.approve(g.ID, strings.Repeat("0", 40))
		if r.Status != http.StatusConflict || !strings.Contains(r.message(), "changed since it was displayed") {
			t.Fatalf("approve with a wrong signature: %s, want 409", r)
		}
		// A real change: keeping the loser changes the decisions (and the signature).
		_, remove := filesOf(g)
		var changed groupDetail
		d.expect(http.MethodPut, fmt.Sprintf("/api/v1/duplicate/%d/file/%d/override", g.ID, remove[0].ID), map[string]any{"decision": "keep"}, http.StatusOK, &changed)
		if changed.Signature == g.Signature || changed.Status != models.GroupProtected {
			t.Fatalf("after keeping everything: %s, signature changed %t", changed.Status, changed.Signature != g.Signature)
		}
		if r := d.approve(g.ID, g.Signature); r.Status != http.StatusConflict {
			t.Fatalf("approve with the reviewed (now stale) signature: %s, want 409", r)
		}
		// Clearing the override restores the reviewed decisions.
		d.expect(http.MethodPut, fmt.Sprintf("/api/v1/duplicate/%d/file/%d/override", g.ID, remove[0].ID), map[string]any{"decision": nil}, http.StatusOK, &changed)
		if changed.Signature != g.Signature || changed.Status != models.GroupPending {
			t.Fatalf("after clearing the override: %s %q", changed.Status, changed.Signature)
		}
		if n := len(d.actionsOf(g.ID)); n != 0 {
			t.Errorf("refused approvals created %d action(s)", n)
		}
	})
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("refused requests changed the fake world:%s", describeRequests(m))
	}
	if acts := d.actions(); len(acts) != 0 {
		t.Fatalf("refused requests created actions: %+v", acts)
	}
}

// TestMultiEpisodeFileKept approves The Expanse S01E01 (approved on its own): only
// the separate single-episode copy goes; the S01E01-E02 file that S01E02 also uses is never
// touched, and Sonarr (which tracks it) is never asked to delete anything.
func TestMultiEpisodeFileKept(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{recycleBin: true, settings: realRemovals})
	g := s.d.groupNamed("The Expanse S01E01")
	multi := fileContaining(t, g, "S01E01-E02")
	single := fileContaining(t, g, "S01E01 - Dulcinea [WEBDL-1080p]")
	e02 := s.env.EpisodeRatingKey("The Expanse", 1, 2)
	e02Media := s.env.MediaIDs(e02)
	if !slices.Contains(multi.Version.Parts[0].SharedWith, e02) {
		t.Errorf("multi-episode part shared with %v, want S01E02 (%s)", multi.Version.Parts[0].SharedWith, e02)
	}
	s.env.ResetRequests()
	g = s.approveAndProcess("The Expanse S01E01")

	acts := s.d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionSucceeded || acts[0].VersionKey != single.Version.Key {
		t.Fatalf("actions = %+v, want only the single-episode copy removed", acts)
	}
	s.requireFile(single.Version.Parts[0].Path, false)
	s.requireFile(multi.Version.Parts[0].Path, true)
	if got := s.env.MediaIDs(e02); !slices.Equal(got, e02Media) {
		t.Errorf("S01E02 media = %v, before %v", got, e02Media)
	}
	if dels := requestsMatching(s.env, fakemedia.InstanceSonarr, http.MethodDelete, "/"); len(dels) > 0 {
		t.Errorf("Sonarr deletes:%s", describeRequests(dels))
	}
	for _, r := range requestsMatching(s.env, fakemedia.ServerPlex, http.MethodDelete, "/") {
		if strings.HasSuffix(r.Path, fmt.Sprintf("/media/%d", multi.Version.MediaID)) {
			t.Errorf("the multi-episode media was deleted in Plex: %s", r.Path)
		}
	}
}

// TestPlayingItemDeferred: a version whose item is playing is not removed; the approved removal
// stays queued (and can be cancelled), and runs once playback stopped.
func TestPlayingItemDeferred(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{recycleBin: true, settings: realRemovals})
	d := s.d
	rk := s.env.RatingKey(fakemedia.SectionMovies, "The Matrix")
	loser := fileContaining(t, d.groupNamed("The Matrix"), "[WEBRip-720p]")
	s.env.SetPlaying(rk)
	s.env.ResetRequests()

	g := s.approveAndProcess("The Matrix")
	if g.Status != models.GroupQueued || !g.HasFlag(models.FlagPlaying) {
		t.Fatalf("while playing: %s %v (%s), want queued with the playing flag", g.Status, g.Flags, g.StatusReason)
	}
	acts := d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionPending {
		t.Fatalf("actions while playing = %+v, want one pending", acts)
	}
	runs := d.commandsNamed(models.CmdProcessQueue)
	if len(runs) != 1 || !strings.Contains(runs[0].Message, "deferred") {
		t.Fatalf("ProcessQueue runs = %+v, want a run that deferred the group", runs)
	}
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("a playing item was touched:%s", describeRequests(m))
	}
	s.requireFile(loser.Version.Parts[0].Path, true)
	if r := d.approve(g.ID, ""); r.Status != http.StatusConflict {
		t.Errorf("approving a queued group again: %s, want 409", r)
	}

	// The queued removal can be cancelled; the group can then be approved again.
	var q paged[models.Action]
	d.expect(http.MethodGet, "/api/v1/queue", nil, http.StatusOK, &q)
	if len(q.Records) != 1 || q.Records[0].ID != acts[0].ID {
		t.Fatalf("queue = %+v", q.Records)
	}
	d.expect(http.MethodDelete, fmt.Sprintf("/api/v1/queue/%d", acts[0].ID), nil, http.StatusOK, nil)
	if a := d.actionsOf(g.ID)[0]; a.Status != models.ActionCancelled {
		t.Fatalf("cancelled action = %+v", a)
	}
	if r := d.request(http.MethodDelete, fmt.Sprintf("/api/v1/queue/%d", acts[0].ID), nil); r.Status != http.StatusConflict {
		t.Errorf("cancelling twice: %s, want 409", r)
	}
	if st := d.group(g.ID).Status; st == models.GroupQueued {
		t.Fatalf("the group stays queued after its only removal was cancelled")
	}

	// Playback stopped: approve again → removed.
	s.env.SetPlaying()
	s.scan() // clears the playing flag
	g = s.approveAndProcess("The Matrix")
	if g.Status != models.GroupResolved {
		t.Fatalf("after playback stopped: %s (%s)", g.Status, g.StatusReason)
	}
	s.requireFile(loser.Version.Parts[0].Path, false)
}

// withSicario adds a movie whose Radarr-tracked copy is the loser (an untracked 2160p keeper sits
// in the same folder), so its removal goes through Radarr.
func withSicario(sc *fakemedia.Scenario) *fakemedia.Scenario {
	dir := fakemedia.DirMovies + "/Sicario (2015)/"
	return sc.AddMovie(fakemedia.Movie{
		Section: fakemedia.SectionMovies, Title: "Sicario", Year: 2015, TmdbID: 273481, ImdbID: "tt3397884",
		Versions: []fakemedia.Version{
			{
				Parts: []fakemedia.Part{{File: dir + "Sicario (2015) [Bluray-2160p][HDR10][DTS-HD MA 5.1][x265].mkv", Size: fakemedia.GiB(31.2)}},
				Video: fakemedia.HDR10UHD(), Audio: []fakemedia.Audio{fakemedia.DTSHDMA("eng", 6)}, DurationMs: fakemedia.Mins(121),
			},
			{
				Parts: []fakemedia.Part{{File: dir + "Sicario (2015) [HDTV-720p][AC3 5.1][x264].mkv", Size: fakemedia.GiB(3.9)}},
				Video: fakemedia.HD("h264"), Audio: []fakemedia.Audio{fakemedia.AC3("eng", 6)}, DurationMs: fakemedia.Mins(121),
				Tracked: fakemedia.InstanceRadarr,
			},
		},
	})
}

// TestArrConflictAbortsRun: Radarr answers 409 to a moviefile delete (its movie/root folder is
// missing or empty from Radarr's point of view — an unmounted share in its container). The run
// is aborted at once: the next queued removal is not attempted and stays queued, nothing is
// deleted. After the mount is fixed, the queue resumes and the group can be approved again.
func TestArrConflictAbortsRun(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{scenario: withSicario(fakemedia.Default()), recycleBin: true, settings: realRemovals})
	d := s.d
	sicario := d.groupNamed("Sicario")
	_, remove := filesOf(sicario)
	if len(remove) != 1 || remove[0].Version.Arr == nil || remove[0].Version.Arr.InstanceName != "Radarr" {
		t.Fatalf("Sicario loser = %+v, want the Radarr-tracked copy", remove)
	}
	tracked := remove[0].Version
	br := d.groupNamed("Blade Runner 2049")
	_, brRemove := filesOf(br)

	s.env.InjectFault(fakemedia.Fault{
		Server: fakemedia.InstanceRadarr, Method: http.MethodDelete, PathPrefix: "/api/v3/moviefile/",
		Status: http.StatusConflict, ContentType: "application/json",
		Body: `{"message":"Movie's root folder (/data/media/movies) is missing or empty"}`,
	})
	s.env.ResetRequests()
	d.bulkApprove(d.groupsBy("Sicario")[0], d.groupsBy("Blade Runner 2049")[0]) // queued in this order
	d.waitIdle()

	runs := d.commandsNamed(models.CmdProcessQueue)
	if len(runs) != 1 || runs[0].Status != models.CommandFailed || !strings.Contains(runs[0].Message, "aborted") {
		t.Fatalf("ProcessQueue runs = %+v, want one aborted run", runs)
	}
	sa := d.actionsOf(sicario.ID)
	if len(sa) != 1 || sa[0].Status != models.ActionFailed || !strings.Contains(sa[0].Message, "HTTP 409") {
		t.Fatalf("Sicario actions = %+v", sa)
	}
	if g := d.group(sicario.ID); g.Status != models.GroupFailed {
		t.Errorf("Sicario: %s (%s), want failed", g.Status, g.StatusReason)
	}
	ba := d.actionsOf(br.ID)
	if len(ba) != 1 || ba[0].Status != models.ActionPending || ba[0].StartedAt != nil {
		t.Fatalf("Blade Runner actions = %+v, want still pending (never attempted)", ba)
	}
	var deletes []fakemedia.Request
	for _, r := range s.env.Requests() {
		if r.Method == http.MethodDelete {
			deletes = append(deletes, r)
		}
	}
	if len(deletes) != 1 || deletes[0].Server != fakemedia.InstanceRadarr || deletes[0].Status != http.StatusConflict ||
		deletes[0].Path != fmt.Sprintf("/api/v3/moviefile/%d", tracked.Arr.FileID) {
		t.Fatalf("DELETE requests:%s\nwant only the refused Radarr delete", describeRequests(deletes))
	}
	s.requireFile(tracked.Parts[0].Path, true)
	s.requireFile(brRemove[0].Version.Parts[0].Path, true)

	// Mount fixed: the queue resumes with the removal that was left queued.
	s.env.ClearFaults()
	d.runCommand(models.CmdProcessQueue, nil)
	if a := d.actionsOf(br.ID)[0]; a.Status != models.ActionSucceeded {
		t.Fatalf("Blade Runner after the fix: %+v", a)
	}
	s.requireFile(brRemove[0].Version.Parts[0].Path, false)
	// …and the failed group can be approved again: deleted via Radarr, which adopts the keeper.
	g := s.approveAndProcess("Sicario")
	if g.Status != models.GroupResolved {
		t.Fatalf("Sicario after re-approval: %s (%s)", g.Status, g.StatusReason)
	}
	s.requireFile(tracked.Parts[0].Path, false)
	if id := s.env.ArrMovieFileID(fakemedia.InstanceRadarr, 273481); id == 0 || id == tracked.Arr.FileID {
		t.Errorf("Radarr tracks file %d after the rescan (deleted %d)", id, tracked.Arr.FileID)
	}
}

// TestUnmountedShareDeletesNothing: the movies share disappears locally (Plex, the *arr and
// Dupearr all see it gone) after the scan; an approved removal is skipped and nothing is deleted,
// not even by later scans. After the share is back, a scan finds every duplicate again.
func TestUnmountedShareDeletesNothing(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{recycleBin: true, settings: realRemovals})
	g := s.d.groupNamed("Blade Runner 2049")
	keep, remove := filesOf(g)
	if err := s.env.Unmount(fakemedia.DirMovies); err != nil {
		t.Fatal(err)
	}
	s.env.ResetRequests()
	g = s.approveAndProcess("Blade Runner 2049")
	acts := s.d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionSkipped {
		t.Fatalf("actions = %+v, want the removal skipped", acts)
	}
	before := map[string]groupSummary{}
	for _, gs := range s.d.groups() {
		before[gs.label()] = gs
	}
	s.scan()
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("an unmounted share was touched:%s", describeRequests(m))
	}
	// Missing files are not a resolved duplicate: every movie group of the unmounted share keeps
	// its status, flagged and explained — Dune too, whose other copy is in the still mounted
	// Movies 4K library; nothing is resolved.
	for _, gs := range s.d.groups() {
		if gs.MediaType != string(models.MediaTypeMovie) {
			continue
		}
		prev := before[gs.label()]
		if gs.Status == string(models.GroupResolved) || gs.Status != prev.Status {
			t.Errorf("%s with its share unmounted: %s (%s), was %s", gs.label(), gs.Status, gs.StatusReason, prev.Status)
		}
		if !slices.Contains(gs.Flags, models.FlagUnavailableVersion) || gs.StatusReason != "Some files are unavailable — check your mounts" {
			t.Errorf("%s with its share unmounted: flags %v, reason %q", gs.label(), gs.Flags, gs.StatusReason)
		}
	}
	for _, h := range s.d.history(g.ID) {
		if h.EventType == models.EventGroupResolved {
			t.Errorf("Blade Runner was resolved while its share was unmounted: %+v", h)
		}
	}
	if err := s.env.Remount(fakemedia.DirMovies); err != nil {
		t.Fatal(err)
	}
	s.requireFile(keep[0].Version.Parts[0].Path, true)
	s.requireFile(remove[0].Version.Parts[0].Path, true)
	s.scan()
	if gs := s.d.groupsBy("Blade Runner 2049"); len(gs) != 1 || gs[0].ID != g.ID || gs[0].Status != string(models.GroupPending) {
		t.Fatalf("Blade Runner after the remount: %+v, want group %d pending", gs, g.ID)
	}
	checkDefaultGroups(t, s.d)
}

// mediaServer fetches a media server (token masked).
func (d *dupearr) mediaServer(id int64) models.MediaServer {
	d.t.Helper()
	var ms models.MediaServer
	d.expect(http.MethodGet, fmt.Sprintf("/api/v1/mediaserver/%d", id), nil, http.StatusOK, &ms)
	return ms
}

// TestDeletionLimits: the per-run circuit breakers. maxDeletionsPerRun=1 stops a run after one
// removal (the rest stay queued for the next run); maxBytesPerRunGb lets the first removal of a
// run through even when it alone is larger, and stops before the next one.
func TestDeletionLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		settings map[string]any
		stopped  string
	}{
		{"count", map[string]any{"maxDeletionsPerRun": 1}, "limit of 1 deletions per run"},
		{"bytes", map[string]any{"maxBytesPerRunGb": 5}, "limit of 5 GB per run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			settings := map[string]any{"dryRun": false, "deletionMethods": []string{"filesystem"}}
			for k, v := range tc.settings {
				settings[k] = v
			}
			s := newStack(t, stackOptions{recycleBin: true, settings: settings})
			d := s.d
			br, mx := d.groupsBy("Blade Runner 2049")[0], d.groupsBy("The Matrix")[0] // 8.1 GiB, 1.4 GiB
			d.bulkApprove(br, mx)
			d.waitIdle()
			runs := d.commandsNamed(models.CmdProcessQueue)
			if len(runs) != 1 || runs[0].Status != models.CommandCompleted || !strings.Contains(runs[0].Message, "1 deleted") ||
				!strings.Contains(runs[0].Message, tc.stopped) {
				t.Fatalf("ProcessQueue runs = %+v, want 1 deletion then %q", runs, tc.stopped)
			}
			if a := d.actionsOf(br.ID); len(a) != 1 || a[0].Status != models.ActionSucceeded {
				t.Fatalf("first removal: %+v", a)
			}
			if a := d.actionsOf(mx.ID); len(a) != 1 || a[0].Status != models.ActionPending {
				t.Fatalf("second removal: %+v, want still queued", a)
			}
			if g := d.group(mx.ID); g.Status != models.GroupQueued {
				t.Errorf("The Matrix: %s, want queued", g.Status)
			}
			// The next run takes the rest.
			d.runCommand(models.CmdProcessQueue, nil)
			if a := d.actionsOf(mx.ID); a[0].Status != models.ActionSucceeded {
				t.Fatalf("second removal after the next run: %+v", a[0])
			}
			for _, id := range []int64{br.ID, mx.ID} {
				if g := d.group(id); g.Status != models.GroupResolved {
					t.Errorf("%s: %s", groupLabel(g.DuplicateGroup), g.Status)
				}
			}
		})
	}
}

// TestRepointedConnections: a connection edited to reach another server never leads to a
// deletion. An *arr URL already used by another application is refused; a media server URL that
// answers as a different Plex server is refused — also with ?forceSave=true (409): skipping the
// test does not skip the identity. Saved while that server did not answer (forced), the queued
// removals are cancelled, approvals wait for a re-scan, the scan itself refuses the other server
// once it answers (and health reports it) and no request that changes anything reaches either
// server.
func TestRepointedConnections(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{recycleBin: true, settings: realRemovals})
	d := s.d
	sc := fakemedia.Minimal()
	sc.Server.MachineIdentifier = strings.Repeat("1", 40)
	other := fakemedia.Start(t, sc)
	t.Cleanup(func() { other.AssertNoViolations(t) })

	radarr := s.env.Instances[fakemedia.InstanceRadarr]
	r := d.request(http.MethodPut, fmt.Sprintf("/api/v1/arr/%d", s.arrIDs[fakemedia.InstanceSonarr]),
		map[string]any{"url": radarr.URL, "apiKey": radarr.APIKey})
	if r.Status != http.StatusConflict {
		t.Fatalf("pointing Sonarr at Radarr's URL: %s, want 409", r)
	}

	// A queued removal (held back while the item plays).
	br := d.groupNamed("Blade Runner 2049")
	_, remove := filesOf(br)
	s.env.SetPlaying(s.env.RatingKey(fakemedia.SectionMovies, "Blade Runner 2049"))
	if g := s.approveAndProcess("Blade Runner 2049"); g.Status != models.GroupQueued {
		t.Fatalf("Blade Runner: %s, want queued", g.Status)
	}
	s.env.ResetRequests()

	path := fmt.Sprintf("/api/v1/mediaserver/%d", s.serverID)
	repoint := map[string]any{"url": other.Plex.URL, "token": other.PlexToken}
	if r := d.request(http.MethodPut, path, repoint); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "different Plex server") {
		t.Fatalf("pointing the media server at another Plex server: %s, want 400", r)
	}
	if r := d.request(http.MethodPut, path+"?forceSave=true", repoint); r.Status != http.StatusConflict || !strings.Contains(r.message(), "different Plex server") {
		t.Fatalf("forcing the re-point to a reachable other server: %s, want 409", r)
	}
	if ms := d.mediaServer(s.serverID); ms.URL != s.env.Plex.URL || ms.MachineIdentifier != s.env.MachineIdentifier {
		t.Fatalf("the refused save changed the media server: %+v", ms)
	}
	// Saved while the other server does not answer: accepted, the stored identity stays.
	other.InjectFault(fakemedia.Fault{Server: fakemedia.ServerPlex, Status: http.StatusServiceUnavailable})
	d.expect(http.MethodPut, path+"?forceSave=true", repoint, http.StatusAccepted, nil)
	other.ClearFaults()
	if ms := d.mediaServer(s.serverID); ms.URL != other.Plex.URL || ms.MachineIdentifier != s.env.MachineIdentifier {
		t.Fatalf("after the forced save: %+v, want the new URL with the old identity", ms)
	}
	d.waitIdle()
	g := d.group(br.ID)
	if g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "points to a different URL") {
		t.Fatalf("Blade Runner after the re-point: %s (%s), want review", g.Status, g.StatusReason)
	}
	if a := d.actionsOf(br.ID); len(a) != 1 || a[0].Status != models.ActionCancelled {
		t.Fatalf("queued removal after the re-point: %+v, want cancelled", a)
	}
	if r := d.approve(br.ID, ""); r.Status != http.StatusConflict || !strings.Contains(r.message(), "different URL") {
		t.Fatalf("approve after the re-point: %s, want 409", r)
	}

	before := d.groups()
	c := d.waitCommand(d.startCommand(models.CmdDuplicateScan, nil).ID)
	if c.Status != models.CommandFailed || !strings.Contains(c.Message, "machine identifier") {
		t.Fatalf("scan against the other server: %s (%s), want failed on the identity check", c.Status, c.Message)
	}
	d.waitIdle()
	if after := d.groups(); len(after) != len(before) {
		t.Fatalf("the refused scan changed the groups: %d → %d", len(before), len(after))
	}
	var identity bool
	for _, h := range d.health() {
		identity = identity || (h.Source == "MediaServerConnectivityCheck" && h.Type == models.HealthError && strings.Contains(h.Message, "different Plex server"))
	}
	if !identity {
		t.Errorf("health does not report the server identity change: %+v", d.health())
	}

	s.env.SetPlaying()
	d.runCommand(models.CmdProcessQueue, nil)
	for name, env := range map[string]*fakemedia.Env{"configured": s.env, "other": other} {
		if m := mutations(env); len(m) > 0 {
			t.Errorf("the %s fake was changed:%s", name, describeRequests(m))
		}
	}
	s.requireFile(remove[0].Version.Parts[0].Path, true)
}
