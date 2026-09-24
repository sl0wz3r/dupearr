//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Whole-disc removals (docs/DECISIONS.md D9): only with settings.allowDiscRemoval, only by a
// person's approval of the one group, only through the filesystem method into the recycle bin, and
// only the entries the disc owns — never a single clip through Plex or an *arr.

// recycledDay splits a recycled path into its dated folder and the path below it; it fails unless
// the path lies in a dated folder of the bin.
func recycledDay(t testing.TB, bin, recycled string) (day, rest string) {
	t.Helper()
	rel, err := filepath.Rel(bin, recycled)
	if err != nil || !filepath.IsLocal(rel) {
		t.Fatalf("%s is outside the recycle bin %s", recycled, bin)
	}
	day, rest, _ = strings.Cut(filepath.ToSlash(rel), "/")
	if _, err := time.Parse("2006-01-02", day); err != nil || rest == "" {
		t.Fatalf("%s is not in a dated folder of the recycle bin", recycled)
	}
	return day, rest
}

// plexScannedFolder reports whether Plex was asked to scan exactly folder (a server path).
func plexScannedFolder(env *fakemedia.Env, folder string) bool {
	for _, r := range env.RequestsTo(fakemedia.ServerPlex) {
		if strings.Contains(r.Path, "/refresh") && r.Query.Get("path") == folder {
			return true
		}
	}
	return false
}

// deletes returns the DELETE requests received since the last ResetRequests.
func deletes(env *fakemedia.Env) []fakemedia.Request {
	var out []fakemedia.Request
	for _, r := range env.Requests() {
		if r.Method == http.MethodDelete {
			out = append(out, r)
		}
	}
	return out
}

// radarrRescanned reports whether Radarr was asked to rescan the movie with the given tmdb id.
func radarrRescanned(env *fakemedia.Env, tmdbID int) bool {
	id := env.ArrMovieID(fakemedia.InstanceRadarr, tmdbID)
	for _, r := range requestsMatching(env, fakemedia.InstanceRadarr, http.MethodPost, "/api/v3/command") {
		if strings.Contains(r.Body, "RescanMovie") && strings.Contains(r.Body, fmt.Sprint(id)) {
			return true
		}
	}
	return false
}

// TestDiscRemovalAndRestore removes, by a person's approval, each disc layout that loses to a
// regular copy — a UHD BDMV whose root is the movie folder (BDMV/, CERTIFICATE/, AACS/), a DVD
// (VIDEO_TS/, AUDIO_TS/), an ISO and a two-disc set ("Disc 1", "Disc 2"; the "Bonus Disc" stays) —
// plus a MakeMKV backup (BDMV/, CERTIFICATE/, MAKEMKV/) that ranks first but that a person marks
// for removal (the WEB-DL next to it stays), and restores each:
//   - bulk approval refuses a disc removal (it must be approved on its own);
//   - the action names the disc root(s), never hundreds of files, and runs through the filesystem
//     method into the recycle bin (not permanent);
//   - exactly the disc's owned entries move, renamed (same inodes, sizes, times and content) into
//     <bin>/<YYYY-MM-DD>/<path below the mapped folder>; the MKV, NFO, artwork and subtitles next
//     to the disc stay untouched; Plex and the *arrs never receive a DELETE; Plex is asked to scan
//     the movie folder; the group is resolved;
//   - the restore puts back the identical tree (names, count, inodes, sizes, times, content), and
//     a second restore is refused.
func TestDiscRemovalAndRestore(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{settings: allowDiscRemovals, recycleBin: true})
	d := s.d

	br := d.groupNamed(titleBladeRunner)
	if br.Status != models.GroupPending || discOf(t, br).Decision != models.DecisionRemove {
		t.Fatalf("Blade Runner: %s (%s), disc %s; want pending with the disc to remove", br.Status, br.StatusReason, discOf(t, br).Decision)
	}
	res := d.bulk("approve", br.ID)
	if len(res.Succeeded) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "full-disc backup") {
		t.Fatalf("bulk approval of a disc removal: %+v, want refused", res)
	}
	if n := len(d.actionsOf(br.ID)); n != 0 {
		t.Fatalf("a refused bulk approval queued %d removal(s)", n)
	}

	cases := []struct {
		title    string
		override bool // the disc ranks first: a person marks it for removal
	}{
		{titleBladeRunner, false}, {titleCasablanca, false}, {titleHeat, false}, {titleLotR, false}, {titleDarkKnight, true},
	}
	for _, c := range cases {
		title := c.title
		t.Run(title, func(t *testing.T) {
			tr := truthOf(movieDiscSet(t, s.env, title))
			localFolder, ok := s.env.LocalPath(tr.folder)
			if !ok {
				t.Fatalf("no local path for %s", tr.folder)
			}
			writeSidecars(t, localFolder)
			owned := relNames(t, localFolder, tr.owned)
			before := snapshot(t, localFolder)
			var bonus map[string]entryState
			if title == titleLotR {
				bonus = filterSnapshot(before, []string{"Bonus Disc"}, true)
				if len(bonus) < 10 {
					t.Fatalf("the bonus disc holds %d entries", len(bonus))
				}
			}

			g := d.groupNamed(title)
			disc := discOf(t, g)
			if c.override {
				if disc.Decision != models.DecisionKeep || disc.Rank != 1 || g.Status != models.GroupProtected {
					t.Fatalf("%s: disc %s rank %d, group %s; want the best version, nothing to remove", title, disc.Decision, disc.Rank, g.Status)
				}
				g = setOverride(t, d, g.ID, disc.ID, "remove")
				disc = discOf(t, g)
			}
			keep, remove := filesOf(g)
			if len(remove) != 1 || remove[0].ID != disc.ID || len(keep) != 1 {
				t.Fatalf("%s: keep %d, remove %d (disc %s); want the disc removed, the MKV kept", title, len(keep), len(remove), disc.Decision)
			}
			mkv := keep[0]
			s.env.ResetRequests()
			if r := d.approve(g.ID, g.Signature); r.Status != http.StatusOK {
				t.Fatalf("approve: %s", r)
			}
			d.waitIdle()

			acts := d.actionsOf(g.ID)
			if len(acts) != 1 {
				t.Fatalf("actions %+v, want 1", acts)
			}
			a := acts[0]
			if a.Status != models.ActionSucceeded || a.Method != models.MethodFilesystem || a.Permanent || a.DryRun {
				t.Fatalf("action %s via %q permanent %t (%s), want a succeeded filesystem move", a.Status, a.Method, a.Permanent, a.Message)
			}
			t.Logf("removal: %s", a.Message)
			if !slices.Equal(a.Paths, tr.roots) || a.VersionKey != disc.Version.Key || a.Size != tr.total {
				t.Errorf("action paths %s key %s size %d, want the disc roots %q, %s, %d", discRootsOfAction(a), a.VersionKey, a.Size, tr.roots, disc.Version.Key, tr.total)
			}
			// Exactly the owned entries, each into <bin>/<day>/<path below the mapped folder>.
			recycled := strings.Split(a.RecyclePath, "\n")
			var day string
			var got []string
			for _, rp := range recycled {
				dd, rest := recycledDay(t, s.recycleBin, rp)
				if day != "" && dd != day {
					t.Errorf("entries of one disc in two dated folders: %s, %s", day, dd)
				}
				day = dd
				got = append(got, rest)
			}
			folderBelowRoot := strings.TrimPrefix(tr.folder, fakemedia.RemoteMediaRoot+"/")
			var want []string
			for _, o := range owned {
				want = append(want, folderBelowRoot+"/"+o)
			}
			sort.Strings(got)
			if !slices.Equal(got, want) {
				t.Fatalf("recycled %q, want exactly the disc's entries %q", got, want)
			}
			binFolder := filepath.Join(s.recycleBin, day, filepath.FromSlash(folderBelowRoot))
			requireSameTree(t, "the recycled disc", filterSnapshot(before, owned, true), snapshot(t, binFolder))
			requireSameTree(t, "the movie folder after the removal", filterSnapshot(before, owned, false), snapshot(t, localFolder))
			if bonus != nil {
				requireSameTree(t, "the bonus disc", bonus, filterSnapshot(snapshot(t, localFolder), []string{"Bonus Disc"}, true))
			}
			s.requireFile(mkv.Version.Parts[0].Path, true)

			if ds := deletes(s.env); len(ds) > 0 {
				t.Errorf("a disc removal sent DELETE requests:%s", describeRequests(ds))
			}
			if !plexScannedFolder(s.env, tr.folder) {
				t.Errorf("Plex was not asked to scan %s:%s", tr.folder, describeRequests(mutations(s.env)))
			}
			after := d.group(g.ID)
			if after.Status != models.GroupResolved {
				t.Errorf("group after the removal: %s (%s), want resolved", after.Status, after.StatusReason)
			}
			var deleted bool
			for _, h := range d.history(g.ID) {
				deleted = deleted || h.EventType == models.EventFileDeleted
			}
			if !deleted {
				t.Errorf("no fileDeleted event")
			}
			if probs := s.env.DiscProblems(); len(probs) > 0 {
				t.Fatalf("discs damaged: %q", probs)
			}

			// Restore: the identical tree is back, the bin entries are gone.
			s.env.ResetRequests()
			var restored models.Action
			d.expect(http.MethodPost, fmt.Sprintf("/api/v1/action/%d/restore", a.ID), nil, http.StatusOK, &restored)
			if restored.RecyclePath != "" || !strings.HasPrefix(restored.Message, "Restored") {
				t.Errorf("restored action %+v", restored)
			}
			d.waitIdle()
			requireSameTree(t, "the movie folder after the restore", before, snapshot(t, localFolder))
			for _, rp := range recycled {
				if _, err := os.Lstat(rp); !os.IsNotExist(err) {
					t.Errorf("%s is still in the recycle bin (%v)", rp, err)
				}
			}
			if !plexScannedFolder(s.env, tr.folder) {
				t.Errorf("Plex was not asked to scan %s after the restore", tr.folder)
			}
			if ds := deletes(s.env); len(ds) > 0 {
				t.Errorf("the restore sent DELETE requests:%s", describeRequests(ds))
			}
			if r := d.request(http.MethodPost, fmt.Sprintf("/api/v1/action/%d/restore", a.ID), nil); r.Status != http.StatusConflict {
				t.Errorf("second restore: %s, want 409", r)
			}
			var restoredEvent bool
			for _, h := range d.history(g.ID) {
				restoredEvent = restoredEvent || h.EventType == models.EventFileRestored
			}
			if !restoredEvent {
				t.Errorf("no fileRestored event")
			}
			// The disc is found again (same key); like any restored copy the group is set aside
			// (ignored) instead of being removed again.
			back := d.group(g.ID)
			if f := discFiles(back); len(f) != 1 || f[0].Version.Key != disc.Version.Key {
				t.Errorf("after the restore the group has discs %+v, want %s back", f, disc.Version.Key)
			}
			if back.Status != models.GroupIgnored {
				t.Errorf("after the restore: %s (%s), want ignored", back.Status, back.StatusReason)
			}
			if probs := s.env.DiscProblems(); len(probs) > 0 {
				t.Fatalf("discs damaged: %q", probs)
			}
		})
	}
}

// TestDiscRegularCopyNextToDisc: removing a regular copy that lies next to a disc (Top Gun:
// Maverick's WEB-DL; the disc ranks first and the 2160p encode is kept as Plex's playable copy)
// removes that one file and leaves the disc and every other file of the folder untouched.
func TestDiscRegularCopyNextToDisc(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{settings: allowDiscRemovals, recycleBin: true})
	d := s.d
	tr := truthOf(movieDiscSet(t, s.env, titleTopGun))
	localFolder, _ := s.env.LocalPath(tr.folder)
	writeSidecars(t, localFolder)
	before := snapshot(t, localFolder)

	g := d.groupNamed(titleTopGun)
	web := regularContaining(t, g, "WEB-DL")
	enc := regularContaining(t, g, "[Bluray-2160p]")
	if _, rm := filesOf(g); len(rm) != 1 || rm[0].ID != web.ID || g.Status != models.GroupPending {
		t.Fatalf("Top Gun: %s, removals %+v; want only the WEB-DL", g.Status, rm)
	}
	s.env.ResetRequests()
	if r := d.approve(g.ID, g.Signature); r.Status != http.StatusOK {
		t.Fatalf("approve: %s", r)
	}
	d.waitIdle()
	acts := d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionSucceeded || acts[0].VersionKey != web.Version.Key {
		t.Fatalf("actions %+v, want the WEB-DL removed", acts)
	}
	webRel := strings.TrimPrefix(web.Version.Parts[0].Path, tr.folder+"/")
	requireSameTree(t, "the movie folder", filterSnapshot(before, []string{webRel}, false), snapshot(t, localFolder))
	s.requireFile(enc.Version.Parts[0].Path, true)
	for _, r := range deletes(s.env) {
		if r.Server != fakemedia.ServerPlex || r.Path != fmt.Sprintf("/library/metadata/%s/media/%d", web.Version.RatingKey, web.Version.MediaID) {
			t.Errorf("DELETE %s %s (only the removed WEB-DL's stale Plex entry may go)", r.Server, r.Path)
		}
	}
	if after := d.group(g.ID); after.Status != models.GroupProtected && after.Status != models.GroupResolved {
		t.Errorf("Top Gun after the removal: %s (%s)", after.Status, after.StatusReason)
	}
}

// TestDiscTrackedClipNeverDeletedThroughRadarr: Radarr tracks the main clip inside Gladiator's
// Blu-ray (BR-DISK, a manual in-place import). The clip is never deleted through Radarr: removal is
// off by default (protected), refused without the filesystem method, and, when a person removes the
// disc, the whole disc goes through the filesystem method and Radarr is only asked to rescan the
// movie (it then tracks the MKV that stayed).
func TestDiscTrackedClipNeverDeletedThroughRadarr(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{settings: allowDiscRemovals, recycleBin: true})
	d := s.d
	clip := s.env.ArrMovieFilePath(fakemedia.InstanceRadarr, gladiatorTmdbID)
	if !strings.HasSuffix(clip, "/"+fakemedia.DiscMainClip) {
		t.Fatalf("Radarr tracks %q, want the main clip of the disc", clip)
	}

	g := d.groupNamed(titleGladiator)
	disc := discOf(t, g)
	mkv := regularFiles(g)[0]
	if !g.HasFlag(models.FlagDiscTracked) || disc.Version.Arr == nil || disc.Version.Arr.InstanceName != "Radarr" {
		t.Fatalf("Gladiator: flags %v, disc arr %+v", g.Flags, disc.Version.Arr)
	}
	// The disc ranks first (source disc > webdl); the MKV is kept as Plex's playable copy.
	if disc.Rank != 1 || mkv.Decision != models.DecisionKeep || g.Status != models.GroupProtected {
		t.Fatalf("Gladiator: disc rank %d, MKV %s, group %s", disc.Rank, mkv.Decision, g.Status)
	}
	// A person keeps the MKV and removes the disc.
	g = setOverride(t, d, g.ID, disc.ID, "remove")
	if f := discOf(t, g); f.Decision != models.DecisionRemove || !g.HasFlag(models.FlagDiscTracked) {
		t.Fatalf("after the override: disc %s, flags %v", f.Decision, g.Flags)
	}

	// Without the filesystem method the removal is refused — never "via Radarr".
	putSettingsSettled(t, d, map[string]any{"deletionMethods": []string{"arr", "plex"}})
	s.env.ResetRequests()
	g = d.group(g.ID)
	if r := d.approve(g.ID, g.Signature); r.Status != http.StatusConflict || !strings.Contains(r.message(), "filesystem") {
		t.Fatalf("approve with arr/plex only: %s, want 409", r)
	}
	if ds := deletes(s.env); len(ds) > 0 {
		t.Fatalf("DELETE requests:%s", describeRequests(ds))
	}

	putSettingsSettled(t, d, map[string]any{"deletionMethods": []string{"arr", "filesystem", "plex"}})
	s.env.ResetRequests()
	g = d.group(g.ID)
	if r := d.approve(g.ID, g.Signature); r.Status != http.StatusOK {
		t.Fatalf("approve: %s", r)
	}
	d.waitIdle()
	acts := d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionSucceeded || acts[0].Method != models.MethodFilesystem {
		t.Fatalf("actions %+v, want one filesystem removal", acts)
	}
	t.Logf("removal: %s", acts[0].Message)
	for _, o := range disc.Version.Disc.OwnedEntries {
		if _, err := os.Lstat(o); !os.IsNotExist(err) {
			t.Errorf("%s is still in place (%v)", o, err)
		}
	}
	if ds := deletes(s.env); len(ds) > 0 {
		t.Fatalf("the disc removal sent DELETE requests (the clip must never be deleted through Radarr):%s", describeRequests(ds))
	}
	if !radarrRescanned(s.env, gladiatorTmdbID) {
		t.Errorf("Radarr was not asked to rescan Gladiator after the disc holding its clip was removed:%s", describeRequests(mutations(s.env)))
	}
	if now := s.env.ArrMovieFilePath(fakemedia.InstanceRadarr, gladiatorTmdbID); now != mkv.Version.Parts[0].Path {
		t.Errorf("after the rescan Radarr tracks %q, want the kept MKV %q", now, mkv.Version.Parts[0].Path)
	}
	s.requireFile(mkv.Version.Parts[0].Path, true)
	if after := d.group(g.ID); after.Status != models.GroupResolved {
		t.Errorf("Gladiator after the removal: %s (%s)", after.Status, after.StatusReason)
	}
}

// TestDiscAutoModeNeverApproves: in auto mode with real removals and disc removal allowed, repeated
// scans approve the plain duplicate (the control) but never a group that removes a disc, nor a group
// that removes a regular copy next to a disc (full_disc), nor the TV episode next to a season disc.
func TestDiscAutoModeNeverApproves(t *testing.T) {
	t.Parallel()
	settings := map[string]any{"mode": models.ModeAuto, "stableScansRequired": 1}
	for k, v := range allowDiscRemovals {
		settings[k] = v
	}
	s := newDiscStack(t, discStackOptions{settings: settings, recycleBin: true})
	d := s.d
	for range 3 {
		s.scan()
	}

	arv := d.groupNamed(titleArrival)
	if acts := d.actionsOf(arv.ID); len(acts) != 1 || acts[0].Status != models.ActionSucceeded || arv.Status != models.GroupResolved {
		t.Fatalf("Arrival (the control): %s, actions %+v — auto mode did not approve a plain duplicate", arv.Status, acts)
	}
	discRemovals := 0
	for _, label := range []string{titleBladeRunner, titleCasablanca, titleLotR, titleOppenheimer, titleTopGun, labelPlanetEarth} {
		g := d.groupNamed(label)
		if !g.HasFlag(models.FlagFullDisc) || g.Status != models.GroupPending {
			t.Errorf("%s: %s %v, want pending with %s", label, g.Status, g.Flags, models.FlagFullDisc)
		}
		if acts := d.actionsOf(g.ID); len(acts) > 0 {
			t.Errorf("%s: auto mode approved it: %+v", label, acts)
		}
		for _, f := range discFiles(g) {
			if f.Decision == models.DecisionRemove {
				discRemovals++
			}
		}
		if g.StableCount < 3 {
			t.Errorf("%s: stable for %d scans, want ≥ 3 (the scans must have seen it)", label, g.StableCount)
		}
	}
	if discRemovals < 4 {
		t.Errorf("only %d pending disc removals; the test needs discs auto mode could have taken", discRemovals)
	}
	for _, a := range d.actions() {
		if a.GroupID != arv.ID || strings.HasPrefix(a.VersionKey, models.DiscKeyPrefix) {
			t.Errorf("unexpected action %+v", a)
		}
	}
	for _, f := range s.env.DiscFixtures() {
		if _, err := os.Lstat(f.OwnedEntries[0]); err != nil {
			t.Errorf("%s disc of %q: %v", f.Kind, f.Title, err)
		}
	}
}

// TestDiscCustomScanner: with Plex's legacy "Disc Image" scanner a Blu-ray is one Plex version with
// one part per BDMV/STREAM clip (300). Dupearr shows it as ONE disc version (origin plex, attributes
// read from the disc, not Plex's). No part of it is ever deleted through Plex: a version of one disc
// of a set cannot be marked for removal, a removal is refused while "plex" is the only method, and
// even with "plex" first the disc is moved as a whole by the filesystem method (the action names the
// disc root, not 300 parts); Plex's stale entry is only removed once every part is gone.
func TestDiscCustomScanner(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{customScanner: true, settings: allowDiscRemovals, recycleBin: true})
	d := s.d
	tr := truthOf(movieDiscSet(t, s.env, titleBladeRunner))
	br := d.groupNamed(titleBladeRunner)
	disc := discOf(t, br)
	v, di := disc.Version, disc.Version.Disc
	if len(br.Files) != 2 || len(v.Parts) != tr.clips || tr.clips != 300 {
		t.Fatalf("Blade Runner: %d versions, disc with %d parts (%d clips on disc)", len(br.Files), len(v.Parts), tr.clips)
	}
	for _, p := range v.Parts {
		if !strings.HasPrefix(p.Path, tr.roots[0]+"/BDMV/STREAM/") {
			t.Fatalf("part %s is not a clip of the disc", p.Path)
		}
	}
	if di.Origin != models.DiscOriginPlex || !strings.HasPrefix(v.Key, "plex:") || v.MediaID == 0 || di.Type != tr.kind ||
		di.FileCount != tr.files || di.TotalBytes != tr.total || di.FeatureBytes != tr.feature || !di.Readable ||
		v.DurationMs != tr.durationMs || di.MainFeature != tr.mainFeature || v.Source != models.SourceDisc {
		t.Fatalf("Plex disc version: key %s, source %s, duration %d, disc %+v; want %+v", v.Key, v.Source, v.DurationMs, di, tr)
	}
	if sum := rawSummaries(t, d)[titleBladeRunner]; sum.FileCount != 2 {
		t.Fatalf("summary lists %d files, want 2", sum.FileCount)
	}
	if disc.Decision != models.DecisionRemove || br.Status != models.GroupPending {
		t.Fatalf("Blade Runner: disc %s, group %s (%s)", disc.Decision, br.Status, br.StatusReason)
	}

	// A version that is one disc of a set (Plex lists "Disc 1" and "Disc 2" separately) is never
	// removable.
	lotr := d.groupNamed(titleLotR)
	lotrDiscs := discFiles(lotr)
	if len(lotrDiscs) != 2 || lotr.Status != models.GroupReview {
		t.Fatalf("LotR: %d disc versions, %s, want 2 in review", len(lotrDiscs), lotr.Status)
	}
	for _, f := range lotrDiscs {
		if f.Decision != models.DecisionKeep || !f.Protected || f.Version.Disc.Removable {
			t.Errorf("LotR %s: %s protected %t removable %t", f.Version.Key, f.Decision, f.Protected, f.Version.Disc.Removable)
		}
		requireOverrideRefused(t, d, titleLotR, f)
	}

	// "plex" only: refused, nothing is deleted.
	putSettingsSettled(t, d, map[string]any{"deletionMethods": []string{"plex"}})
	s.env.ResetRequests()
	br = d.group(br.ID)
	if r := d.approve(br.ID, br.Signature); r.Status != http.StatusConflict || !strings.Contains(r.message(), "filesystem") {
		t.Fatalf("approve with plex only: %s, want 409", r)
	}
	if ds := deletes(s.env); len(ds) > 0 {
		t.Fatalf("DELETE requests:%s", describeRequests(ds))
	}

	// "plex" first, filesystem second: the disc is moved as a whole.
	putSettingsSettled(t, d, map[string]any{"deletionMethods": []string{"plex", "filesystem"}})
	s.env.ResetRequests()
	br = d.group(br.ID)
	if r := d.approve(br.ID, br.Signature); r.Status != http.StatusOK {
		t.Fatalf("approve: %s", r)
	}
	d.waitIdle()
	acts := d.actionsOf(br.ID)
	if len(acts) != 1 {
		t.Fatalf("actions %+v", acts)
	}
	a := acts[0]
	if a.Status != models.ActionSucceeded || a.Method != models.MethodFilesystem || !slices.Equal(a.Paths, tr.roots) {
		t.Fatalf("action %s via %q paths %s (%s), want the disc root moved by the filesystem method", a.Status, a.Method, discRootsOfAction(a), a.Message)
	}
	t.Logf("removal: %s; DELETE requests:%s", a.Message, describeRequests(deletes(s.env)))
	if got := len(strings.Split(a.RecyclePath, "\n")); got != len(tr.owned) {
		t.Errorf("recycled %d entries, want the disc's %d", got, len(tr.owned))
	}
	for _, o := range tr.owned {
		if _, err := os.Lstat(o); !os.IsNotExist(err) {
			t.Errorf("%s is still in place (%v)", o, err)
		}
	}
	// No *arr delete at all; a Plex delete only of the stale media, after all its parts were gone
	// (a delete while any clip existed is a fakemedia violation, checked at the end).
	for _, r := range deletes(s.env) {
		if r.Server != fakemedia.ServerPlex || r.Path != fmt.Sprintf("/library/metadata/%s/media/%d", v.RatingKey, v.MediaID) {
			t.Errorf("DELETE %s %s", r.Server, r.Path)
		}
	}
	if probs := s.env.DiscProblems(); len(probs) > 0 {
		t.Fatalf("discs damaged: %q", probs)
	}
}

// TestDiscTVSeasonProtected: a BDMV in a season folder is never a version of an episode: the
// episode's duplicate group is flagged full_disc (a person must approve it), and approving it
// removes only the regular losing copy — the season disc stays complete.
func TestDiscTVSeasonProtected(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{settings: allowDiscRemovals, recycleBin: true})
	d := s.d
	var tv fakemedia.DiscFixture
	for _, f := range s.env.DiscFixtures() {
		if f.Show {
			tv = f
		}
	}
	if tv.LocalRoot == "" {
		t.Fatal("no TV disc fixture")
	}
	localFolder, _ := s.env.LocalPath(tv.Folder)
	before := snapshot(t, localFolder)
	owned := relNames(t, localFolder, tv.OwnedEntries)

	g := d.groupNamed(labelPlanetEarth)
	if len(discFiles(g)) != 0 || !g.HasFlag(models.FlagFullDisc) || g.Status != models.GroupPending {
		t.Fatalf("%s: %d discs, %s %v", labelPlanetEarth, len(discFiles(g)), g.Status, g.Flags)
	}
	_, remove := filesOf(g)
	if len(remove) != 1 || !strings.Contains(remove[0].Version.Parts[0].Path, "[HDTV-720p]") {
		t.Fatalf("%s removals %+v, want the 720p copy", labelPlanetEarth, remove)
	}
	res := d.bulk("approve", g.ID)
	if len(res.Succeeded) != 1 {
		t.Fatalf("bulk approve of an episode next to a disc: %+v", res)
	}
	d.waitIdle()
	acts := d.actionsOf(g.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionSucceeded || acts[0].VersionKey != remove[0].Version.Key {
		t.Fatalf("actions %+v, want the 720p copy removed", acts)
	}
	s.requireFile(remove[0].Version.Parts[0].Path, false)
	loser := strings.TrimPrefix(remove[0].Version.Parts[0].Path, tv.Folder+"/")
	requireSameTree(t, "the season folder", filterSnapshot(before, []string{loser}, false), snapshot(t, localFolder))
	if len(filterSnapshot(before, owned, true)) < tv.Files {
		t.Fatalf("the season disc snapshot misses files")
	}
	if probs := s.env.DiscProblems(); len(probs) > 0 {
		t.Fatalf("discs damaged: %q", probs)
	}
}

// TestDiscChangedOnDiskIsNotMoved: a disc that changed after the scan (a file added — e.g. a backup
// still being written — or a symbolic link placed inside it) is re-measured right before the move
// and left alone: the approved removal is not carried out, nothing reaches the recycle bin and
// every file stays where it was.
func TestDiscChangedOnDiskIsNotMoved(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{settings: allowDiscRemovals, recycleBin: true})
	d := s.d
	cases := []struct {
		title  string
		change func(t *testing.T, root string)
	}{
		{titleBladeRunner, func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "BDMV", "STREAM", "00999.m2ts"), []byte("still being written"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{titleCasablanca, func(t *testing.T, root string) {
			if err := os.Symlink("/etc", filepath.Join(root, "VIDEO_TS", "LINK")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			tr := truthOf(movieDiscSet(t, s.env, c.title))
			localFolder, _ := s.env.LocalPath(tr.folder)
			g := d.groupNamed(c.title)
			if f := discOf(t, g); f.Decision != models.DecisionRemove || g.Status != models.GroupPending {
				t.Fatalf("%s: disc %s, group %s", c.title, f.Decision, g.Status)
			}
			c.change(t, tr.localRoots[0])
			before := snapshot(t, localFolder)
			s.env.ResetRequests()
			if r := d.approve(g.ID, g.Signature); r.Status != http.StatusOK {
				t.Fatalf("approve: %s", r)
			}
			d.waitIdle()
			acts := d.actionsOf(g.ID)
			if len(acts) != 1 || acts[0].Status == models.ActionSucceeded || acts[0].Status == models.ActionPending || acts[0].RecyclePath != "" {
				t.Fatalf("actions %+v, want the removal not carried out", acts)
			}
			t.Logf("%s: %s (%s); group %s", c.title, acts[0].Status, acts[0].Message, d.group(g.ID).Status)
			requireSameTree(t, "the movie folder", before, snapshot(t, localFolder))
			if after := d.group(g.ID); after.Status == models.GroupResolved || after.Status == models.GroupQueued {
				t.Errorf("group after the refused move: %s (%s)", after.Status, after.StatusReason)
			}
			if ds := deletes(s.env); len(ds) > 0 {
				t.Errorf("DELETE requests:%s", describeRequests(ds))
			}
		})
	}
	if entries, err := os.ReadDir(s.recycleBin); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				if inner, _ := os.ReadDir(filepath.Join(s.recycleBin, e.Name())); len(inner) > 0 {
					t.Errorf("the recycle bin holds %s/%s", e.Name(), inner[0].Name())
				}
			}
		}
	}
}

// TestDiscDetectionOff: with settings.detectDiscs off Dupearr does not look for discs on disk (a
// folder with an MKV next to BDMV/ is one version, no duplicate), but a disc a custom Plex scanner
// lists is still a disc version — its clips never become ordinary, removable files: it is protected
// even with disc removal allowed, and marking it for removal is refused.
func TestDiscDetectionOff(t *testing.T) {
	t.Parallel()
	settings := map[string]any{"detectDiscs": false}
	for k, v := range allowDiscRemovals {
		settings[k] = v
	}
	t.Run("default scanner", func(t *testing.T) {
		t.Parallel()
		s := newDiscStack(t, discStackOptions{settings: settings, recycleBin: true})
		for _, title := range []string{titleBladeRunner, titleCasablanca, titleHeat, titleLotR, titleOppenheimer} {
			if gs := s.d.groupsBy(title); len(gs) != 0 {
				t.Errorf("%s: a group without disc detection: %+v", title, gs)
			}
		}
		tg := s.d.groupNamed(titleTopGun)
		if len(tg.Files) != 2 || len(discFiles(tg)) != 0 {
			t.Errorf("Top Gun: %d versions, %d discs; want its 2 regular copies only", len(tg.Files), len(discFiles(tg)))
		}
	})
	t.Run("custom scanner", func(t *testing.T) {
		t.Parallel()
		s := newDiscStack(t, discStackOptions{customScanner: true, settings: settings, recycleBin: true})
		d := s.d
		for _, title := range []string{titleBladeRunner, titleCasablanca, titleOppenheimer} {
			g := d.groupNamed(title)
			f := discOf(t, g)
			if f.Version.Disc.Origin != models.DiscOriginPlex || !f.Protected || f.Decision != models.DecisionKeep || f.Version.Disc.Removable {
				t.Errorf("%s: Plex disc version %s protected %t removable %t (%q)", title, f.Decision, f.Protected, f.Version.Disc.Removable, f.Version.Disc.Problem)
			}
			if !strings.Contains(f.Version.Disc.Problem, "detection is off") {
				t.Errorf("%s: problem %q, want it to name the setting", title, f.Version.Disc.Problem)
			}
			requireOverrideRefused(t, d, title, f, "protected")
			if g.Status == models.GroupProtected {
				if r := d.approve(g.ID, g.Signature); r.Status != http.StatusBadRequest {
					t.Errorf("approve %s: %s, want 400", title, r)
				}
			}
		}
		if n := len(d.actions()); n != 0 {
			t.Errorf("%d actions", n)
		}
	})
}
