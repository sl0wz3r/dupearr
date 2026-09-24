//go:build e2e

package e2e

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Default-scenario ids used below.
const (
	severanceTvdbID = 371980
	matrixFolder    = fakemedia.RemoteMediaRoot + "/movies/The Matrix (1999)"
)

// TestDryRun is scenario 2: in dry run, approving every pending group records what would be
// deleted ("dry_run" actions) and touches nothing: no DELETE, no *arr command or edit, no Plex
// refresh, every file still in place, and the groups become approvable again.
func TestDryRun(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{}) // dry run is the default
	pending := withStatus(s.d.groups(), "pending")
	if len(pending) != 5 {
		t.Fatalf("pending groups: %d, want 5%s", len(pending), s.d.describeGroups())
	}
	var paths []string
	for _, g := range pending {
		for _, f := range s.d.group(g.ID).Files {
			paths = append(paths, partPaths(f.Version)...)
		}
	}
	s.env.ResetRequests()
	s.d.bulkApprove(pending...)
	s.d.waitIdle()

	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("dry run changed the fake world:%s", describeRequests(m))
	}
	for _, p := range paths {
		s.requireFile(p, true)
	}
	runs := s.d.commandsNamed(models.CmdProcessQueue)
	if len(runs) != 1 || runs[0].Status != models.CommandCompleted || !strings.Contains(runs[0].Message, "5 dry run") {
		t.Fatalf("ProcessQueue runs = %+v, want one completed run with 5 dry-run removals", runs)
	}
	for _, g := range pending {
		acts := s.d.actionsOf(g.ID)
		if len(acts) != 1 {
			t.Fatalf("%s: %d actions, want 1", g.label(), len(acts))
		}
		a := acts[0]
		if a.Status != models.ActionDryRun || !a.DryRun || !strings.HasPrefix(a.Message, "Would delete via ") ||
			a.RecyclePath != "" || a.FinishedAt == nil {
			t.Errorf("%s: action = %+v", g.label(), a)
		}
		wantMethod := models.MethodPlex // the preferred method able to remove an untracked file
		if g.label() == "Severance S01E01" {
			wantMethod = models.MethodArr // the loser is tracked by Sonarr
		}
		if a.Method != wantMethod {
			t.Errorf("%s: dry-run method %q, want %q (%s)", g.label(), a.Method, wantMethod, a.Message)
		}
		after := s.d.group(g.ID)
		if after.Status != models.GroupPending || !strings.Contains(after.StatusReason, "Dry run") {
			t.Errorf("%s: after the dry run: %s (%s), want pending again", g.label(), after.Status, after.StatusReason)
		}
		var dry int
		for _, h := range s.d.history(g.ID) {
			if h.EventType == models.EventFileDeleteDryRun {
				dry++
			}
		}
		if dry != 1 {
			t.Errorf("%s: %d fileDeleteDryRun events, want 1", g.label(), dry)
		}
	}
	var stats struct {
		ReclaimedBytes int64 `json:"reclaimedBytes"`
	}
	s.d.expect(http.MethodGet, "/api/v1/duplicate/stats", nil, http.StatusOK, &stats)
	if stats.ReclaimedBytes != 0 {
		t.Errorf("reclaimed bytes after a dry run = %d", stats.ReclaimedBytes)
	}
}

// TestRemoval is scenario 3: real removals with methods [arr, filesystem, plex] and Dupearr's
// recycle bin. Severance's tracked loser is deleted through Sonarr (into Sonarr's recycle bin) and
// Sonarr rescans to adopt the kept file; the untracked losers (the movies' and The Expanse's
// single-episode copy) are moved to the dated recycle bin; the stale Plex entries are removed; the groups are resolved and stay resolved on
// a rescan; keepers are never touched.
func TestRemoval(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{recycleBin: true, settings: map[string]any{
		"dryRun": false, "deletionMethods": []string{"arr", "filesystem", "plex"},
	}})
	pending := withStatus(s.d.groups(), "pending")
	if len(pending) != 5 {
		t.Fatalf("pending groups: %d, want 5%s", len(pending), s.d.describeGroups())
	}
	type loser struct {
		group   groupDetail
		keep    models.GroupFile
		remove  models.GroupFile
		mediaID int64
	}
	losers := map[string]loser{}
	for _, g := range pending {
		gd := s.d.group(g.ID)
		keep, remove := filesOf(gd)
		if len(keep) != 1 || len(remove) != 1 {
			t.Fatalf("%s: keep %d, remove %d", g.label(), len(keep), len(remove))
		}
		losers[g.label()] = loser{group: gd, keep: keep[0], remove: remove[0], mediaID: remove[0].Version.MediaID}
	}
	sev := losers["Severance S01E01"]
	if sev.remove.Version.Arr == nil {
		t.Fatalf("Severance loser is not tracked: %+v", sev.remove.Version)
	}
	sevFileID := sev.remove.Version.Arr.FileID
	if got := s.env.ArrEpisodeFileID(fakemedia.InstanceSonarr, severanceTvdbID, 1, 1); got != sevFileID {
		t.Fatalf("Sonarr tracks episode file %d, Dupearr recorded %d", got, sevFileID)
	}
	if _, err := os.Stat(s.recycleBin); !os.IsNotExist(err) {
		t.Fatalf("the recycle bin exists before the first removal (%v)", err)
	}
	// A bin that does not exist yet (its parent does) is fine: it is created on first use. (The
	// live test saw a stale "recycle bin folder does not exist" error here.)
	for _, h := range s.d.health() {
		if h.Source == "RecycleBinCheck" || h.Type == models.HealthError {
			t.Errorf("health issue before the first removal: %+v", h)
		}
	}

	s.env.ResetRequests()
	s.d.bulkApprove(pending...)
	s.d.waitIdle()

	runs := s.d.commandsNamed(models.CmdProcessQueue)
	if len(runs) != 1 || runs[0].Status != models.CommandCompleted || !strings.Contains(runs[0].Message, "5 deleted") {
		t.Fatalf("ProcessQueue runs = %+v, want one completed run with 5 deletions", runs)
	}
	var reclaimed int64
	for label, l := range losers {
		acts := s.d.actionsOf(l.group.ID)
		if len(acts) != 1 {
			t.Fatalf("%s: %d actions", label, len(acts))
		}
		a := acts[0]
		reclaimed += a.Size
		if a.Status != models.ActionSucceeded || a.DryRun || a.Permanent {
			t.Errorf("%s: action = %+v", label, a)
		}
		for _, p := range partPaths(l.remove.Version) {
			s.requireFile(p, false)
		}
		for _, p := range partPaths(l.keep.Version) {
			s.requireFile(p, true)
		}
		// The stale Plex entry of the removed version was deleted — exactly once, by media id.
		rk := l.remove.Version.RatingKey
		dels := requestsMatching(s.env, fakemedia.ServerPlex, http.MethodDelete, "/library/metadata/")
		var mine []fakemedia.Request
		for _, r := range dels {
			if r.Path == fmt.Sprintf("/library/metadata/%s/media/%d", rk, l.mediaID) {
				mine = append(mine, r)
			}
		}
		if len(mine) != 1 || mine[0].Status != http.StatusOK {
			t.Errorf("%s: Plex media deletes = %s, want one of media %d", label, describeRequests(dels), l.mediaID)
		}
		if ids := s.env.MediaIDs(rk); slices.Contains(ids, l.mediaID) || !slices.Contains(ids, l.keep.Version.MediaID) {
			t.Errorf("%s: Plex media of %s = %v (removed %d, kept %d)", label, rk, ids, l.mediaID, l.keep.Version.MediaID)
		}
		g := s.d.group(l.group.ID)
		if g.Status != models.GroupResolved {
			t.Errorf("%s: %s (%s), want resolved", label, g.Status, g.StatusReason)
		}
		kinds := map[string]int{}
		for _, h := range s.d.history(l.group.ID) {
			kinds[h.EventType]++
		}
		if kinds[models.EventGroupApproved] != 1 || kinds[models.EventFileDeleted] != 1 || kinds[models.EventGroupResolved] != 1 {
			t.Errorf("%s: history = %v", label, kinds)
		}

		if label == "Severance S01E01" {
			if a.Method != models.MethodArr || !strings.Contains(a.Message, "Sonarr") || a.RecyclePath != "" {
				t.Errorf("Severance: action = %+v, want a Sonarr delete", a)
			}
			continue
		}
		// Untracked losers: moved into <bin>/<YYYY-MM-DD>/<path below the mapped folder>.
		if a.Method != models.MethodFilesystem {
			t.Errorf("%s: method %q (%s), want filesystem", label, a.Method, a.Message)
		}
		recycled := strings.Split(a.RecyclePath, "\n")
		if len(recycled) != len(a.Paths) {
			t.Fatalf("%s: recycle paths %q for %d parts", label, a.RecyclePath, len(a.Paths))
		}
		for i, rp := range recycled {
			rel, err := filepath.Rel(s.recycleBin, rp)
			if err != nil || strings.HasPrefix(rel, "..") {
				t.Fatalf("%s: %s is outside the recycle bin %s", label, rp, s.recycleBin)
			}
			dated, rest, _ := strings.Cut(filepath.ToSlash(rel), "/")
			if _, err := time.Parse("2006-01-02", dated); err != nil {
				t.Errorf("%s: %s is not in a dated folder", label, rp)
			}
			if want := strings.TrimPrefix(a.Paths[i], fakemedia.RemoteMediaRoot+"/"); rest != want {
				t.Errorf("%s: recycled as %q, want %q", label, rest, want)
			}
			fi, err := os.Stat(rp)
			if err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("%s: recycled file %s: %v", label, rp, err)
			}
			if fi.Size() != l.remove.Version.Parts[i].Size {
				t.Errorf("%s: recycled size %d, want %d", label, fi.Size(), l.remove.Version.Parts[i].Size)
			}
		}
	}
	// The recycle bin was created on first use, with its .plexignore and marker.
	if b, err := os.ReadFile(filepath.Join(s.recycleBin, ".plexignore")); err != nil || !strings.Contains(string(b), "*") {
		t.Errorf("recycle bin .plexignore: %q, %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(s.recycleBin, ".dupearr-recycle-bin")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("recycle bin marker: %v", err)
	}

	// Sonarr: one DELETE of the confirmed file id, then a series rescan that adopted the keeper.
	sonarrDel := requestsMatching(s.env, fakemedia.InstanceSonarr, http.MethodDelete, "/")
	if len(sonarrDel) != 1 || sonarrDel[0].Path != fmt.Sprintf("/api/v3/episodefile/%d", sevFileID) {
		t.Errorf("Sonarr deletes:%s, want only episodefile/%d", describeRequests(sonarrDel), sevFileID)
	}
	var rescan bool
	for _, r := range requestsMatching(s.env, fakemedia.InstanceSonarr, http.MethodPost, "/api/v3/command") {
		rescan = rescan || (strings.Contains(r.Body, "RescanSeries") && strings.Contains(r.Body, fmt.Sprint(s.env.ArrSeriesID(fakemedia.InstanceSonarr, severanceTvdbID))))
	}
	if !rescan {
		t.Errorf("Sonarr was not asked to rescan Severance")
	}
	adopted := s.env.ArrEpisodeFileID(fakemedia.InstanceSonarr, severanceTvdbID, 1, 1)
	if adopted == 0 || adopted == sevFileID {
		t.Errorf("after the rescan Sonarr tracks episode file %d (deleted %d); the kept file should be adopted", adopted, sevFileID)
	}
	name := path.Base(sev.remove.Version.Parts[0].Path)
	if !fileUnder(t, filepath.Join(s.env.Dir, "recycle", "sonarr"), name) {
		t.Errorf("%s is not in Sonarr's recycle bin", name)
	}
	// Radarr and Radarr 4K were never asked to delete anything.
	for _, inst := range []string{fakemedia.InstanceRadarr, fakemedia.InstanceRadarr4K} {
		if dels := requestsMatching(s.env, inst, http.MethodDelete, "/"); len(dels) > 0 {
			t.Errorf("%s deletes:%s", inst, describeRequests(dels))
		}
	}

	var stats struct {
		ReclaimedBytes int64          `json:"reclaimedBytes"`
		ByStatus       map[string]int `json:"byStatus"`
	}
	s.d.expect(http.MethodGet, "/api/v1/duplicate/stats", nil, http.StatusOK, &stats)
	if stats.ReclaimedBytes != reclaimed || stats.ByStatus["resolved"] != 5 {
		t.Errorf("stats = %+v, want %d bytes reclaimed and 5 resolved groups", stats, reclaimed)
	}
	// The bin exists now: no stale "recycle bin folder does not exist" health issue.
	for _, h := range s.d.health() {
		if h.Source == "RecycleBinCheck" || h.Type == models.HealthError {
			t.Errorf("health issue after the removals: %+v", h)
		}
	}

	// A full rescan keeps the resolved groups resolved and changes nothing.
	s.env.ResetRequests()
	s.scan()
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("the rescan changed the fake world:%s", describeRequests(m))
	}
	for label, l := range losers {
		gs := s.d.groupsBy(label)
		if len(gs) != 1 || gs[0].ID != l.group.ID || gs[0].Status != string(models.GroupResolved) {
			t.Errorf("%s after the rescan: %+v, want group %d still resolved", label, gs, l.group.ID)
		}
	}
	if n := len(s.d.groups()); n != 10 {
		t.Errorf("groups after the rescan: %d, want 10%s", n, s.d.describeGroups())
	}
}

// fileUnder reports whether a file named name exists anywhere below dir.
func fileUnder(t *testing.T, dir, name string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == name {
			found = true
		}
		return nil
	})
	return found
}

// TestPlexOnlyRemoval is scenario 4: with "plex" as the only deletion method, removing an
// untracked loser is exactly one Plex DELETE of that version's media id; the keeper and every
// other file stay untouched and nothing else is deleted.
func TestPlexOnlyRemoval(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{settings: map[string]any{"dryRun": false, "deletionMethods": []string{"plex"}}})
	rk := s.env.RatingKey(fakemedia.SectionMovies, "Blade Runner 2049")
	mediaID := s.env.MediaIDForFile(rk, "1080p.AMZN.WEB-DL")
	keeperID := s.env.MediaIDForFile(rk, "Remux-2160p")
	g := s.d.groupNamed("Blade Runner 2049")
	keep, remove := filesOf(g)
	if len(remove) != 1 || remove[0].Version.MediaID != mediaID || remove[0].Version.RatingKey != rk {
		t.Fatalf("loser = %+v, want media %d of %s", remove, mediaID, rk)
	}

	s.env.ResetRequests()
	g = s.approveAndProcess("Blade Runner 2049")

	var deletes []fakemedia.Request
	for _, r := range s.env.Requests() {
		if r.Method == http.MethodDelete {
			deletes = append(deletes, r)
		}
	}
	want := fmt.Sprintf("/library/metadata/%s/media/%d", rk, mediaID)
	if len(deletes) != 1 || deletes[0].Server != fakemedia.ServerPlex || deletes[0].Path != want || deletes[0].Status != http.StatusOK {
		t.Fatalf("DELETE requests:%s\nwant exactly one Plex DELETE %s", describeRequests(deletes), want)
	}
	if len(deletes[0].Query) > 0 {
		t.Errorf("the Plex delete carries query parameters: %v", deletes[0].Query)
	}
	acts := s.d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionSucceeded || acts[0].Method != models.MethodPlex || !acts[0].Permanent {
		t.Fatalf("actions = %+v, want one permanent Plex removal", acts)
	}
	if g.Status != models.GroupResolved {
		t.Errorf("group %s (%s), want resolved", g.Status, g.StatusReason)
	}
	s.requireFile(remove[0].Version.Parts[0].Path, false)
	s.requireFile(keep[0].Version.Parts[0].Path, true)
	if ids := s.env.MediaIDs(rk); !slices.Equal(ids, []int64{keeperID}) {
		t.Errorf("Plex media of Blade Runner = %v, want only the keeper %d", ids, keeperID)
	}
	// Nothing else in the library changed.
	for label := range defaultScenarioGroups {
		if label == "Blade Runner 2049" {
			continue
		}
		for _, f := range s.d.groupNamed(label).Files {
			for _, p := range partPaths(f.Version) {
				s.requireFile(p, true)
			}
		}
	}
}

// TestRestore is scenario 5: a version moved to the recycle bin is restored to its original path;
// Plex is asked to scan the folder (the fake re-detects the file as a new version), a targeted
// re-scan runs and the group is back — ignored, so the restored copy is not removed again until the
// user un-ignores it (then it is pending again).
func TestRestore(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{recycleBin: true, settings: map[string]any{
		"dryRun": false, "deletionMethods": []string{"filesystem"},
	}})
	g := s.approveAndProcess("The Matrix")
	if g.Status != models.GroupResolved {
		t.Fatalf("The Matrix: %s (%s)", g.Status, g.StatusReason)
	}
	acts := s.d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionSucceeded || acts[0].RecyclePath == "" {
		t.Fatalf("actions = %+v", acts)
	}
	a := acts[0]
	orig := a.Paths[0]
	oldMediaID := fileContaining(t, g, "[WEBRip-720p]").Version.MediaID
	s.requireFile(orig, false)
	rk := s.env.RatingKey(fakemedia.SectionMovies, "The Matrix")
	if n := len(s.env.MediaIDs(rk)); n != 2 {
		t.Fatalf("Plex media after the removal: %d, want the keeper and the optimized version", n)
	}

	s.env.ResetRequests()
	var restored models.Action
	s.d.expect(http.MethodPost, fmt.Sprintf("/api/v1/action/%d/restore", a.ID), nil, http.StatusOK, &restored)
	if restored.RecyclePath != "" || !strings.HasPrefix(restored.Message, "Restored from the recycle bin") {
		t.Errorf("restored action = %+v", restored)
	}
	s.requireFile(orig, true)
	if _, err := os.Stat(a.RecyclePath); !os.IsNotExist(err) {
		t.Errorf("the recycled copy is still in the bin (%v)", err)
	}
	s.d.waitIdle()

	// Plex was asked to scan exactly the restored file's folder, and found the file again.
	var scanned bool
	for _, r := range s.env.RequestsTo(fakemedia.ServerPlex) {
		if strings.HasPrefix(r.Path, "/library/sections/1/refresh") && r.Query.Get("path") == matrixFolder {
			scanned = true
		}
	}
	if !scanned {
		t.Errorf("Plex was not asked to scan %s:%s", matrixFolder, describeRequests(mutations(s.env)))
	}
	if n := len(s.env.MediaIDs(rk)); n != 3 {
		t.Fatalf("Plex media after the restore: %d, want the restored version back", n)
	}
	var targeted bool
	for _, c := range s.d.commandsNamed(models.CmdTargetedScan) {
		targeted = targeted || (c.Status == models.CommandCompleted && strings.Contains(string(c.Body), rk))
	}
	if !targeted {
		t.Errorf("no targeted re-scan of rating key %s ran after the restore", rk)
	}
	hist := s.d.history(g.ID)
	if len(hist) == 0 || !slices.ContainsFunc(hist, func(h models.HistoryEvent) bool { return h.EventType == models.EventFileRestored }) {
		t.Errorf("history has no fileRestored event: %+v", hist)
	}
	// A second restore of the same action is refused.
	if r := s.d.request(http.MethodPost, fmt.Sprintf("/api/v1/action/%d/restore", a.ID), nil); r.Status != http.StatusConflict {
		t.Errorf("second restore: %s, want 409", r)
	}

	// The duplicate is back with the restored version (a new Plex media id), ignored: the user
	// wanted this copy back, so neither auto mode nor a bulk approval removes it again.
	gs := s.d.groupsBy("The Matrix")
	if len(gs) != 1 || gs[0].ID != g.ID {
		t.Fatalf("groups for The Matrix after the restore: %+v, want group %d", gs, g.ID)
	}
	back := s.d.group(g.ID)
	if back.Status != models.GroupIgnored || !strings.Contains(back.StatusReason, "Restored") {
		t.Fatalf("The Matrix after the restore: %s %v (%s), want ignored (restored)", back.Status, back.Flags, back.StatusReason)
	}
	if f := fileContaining(t, back, "[WEBRip-720p]"); f.Version.MediaID == oldMediaID {
		t.Errorf("restored version = %+v (removed media id %d)", f, oldMediaID)
	}
	if back.Signature == g.Signature {
		t.Errorf("the signature did not change although the version is new")
	}
	// Un-ignoring re-evaluates it: the restored copy is marked for removal again. (The e2e stacks
	// run without a minimum age; see newStack.)
	var reopened groupDetail
	s.d.expect(http.MethodPost, fmt.Sprintf("/api/v1/duplicate/%d/unignore", g.ID), nil, http.StatusOK, &reopened)
	if reopened.Status != models.GroupPending {
		t.Fatalf("The Matrix after un-ignoring: %s (%s), want pending", reopened.Status, reopened.StatusReason)
	}
	if f := fileContaining(t, reopened, "[WEBRip-720p]"); f.Decision != models.DecisionRemove {
		t.Errorf("restored version after un-ignoring = %+v, want remove", f)
	}
	s.d.waitIdle()
	if m := mutations(s.env); slices.ContainsFunc(m, func(r fakemedia.Request) bool { return r.Method == http.MethodDelete }) {
		t.Errorf("a restore deleted something:%s", describeRequests(m))
	}
}
