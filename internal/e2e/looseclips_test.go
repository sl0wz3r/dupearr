//go:build e2e

package e2e

import (
	"archive/tar"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Loose clip sets (flattened disc backups), end to end: the real binary against the fakemedia
// "looseclips" scenario, which mirrors a real library whose Blu-ray backups lie flattened in the
// movie folders — 60–190 numbered .m2ts clips each (and a flat DVD), every clip listed by Plex as
// a SEPARATE version of the movie. In that library the old Dupearr showed 100+ "copies" per movie
// and, approved by hand with dry run off, deleted 337 clips one by one through Plex. The contract:
//
//   - all clips of one folder are ONE disc version (bluray_clips / dvd_clips, origin plex, key
//     "disc:<server>:<sha1 of the folder + ":clips">", parts = the clips, attributes from the longest
//     clip, clip count and merged Plex media ids in the disc info);
//   - an item whose only copy is a clip set forms no group; a clip set next to a regular file forms a
//     normal two-version group where the set is protected unless disc removal is allowed, and is not
//     a playable copy for settings.keepPlayableCopy;
//   - no clip is ever removed on its own, by any method; a whole set is only moved (all clip-named
//     files + loose navigation files of the folder, nothing else) by the filesystem method into the
//     recycle bin, on a person's approval of that one group, with disc removal allowed;
//   - Plex and the *arrs never receive a DELETE for a clip — the fake records every such request
//     (fakemedia.Env.ClipDeletes, stale entries included) and every test requires none.

// Movies of the scenario whose only copy is a clip set: never a group.
var looseClipOnly = []string{
	fakemedia.LooseBadBoys, fakemedia.LooseComingToAmerica, fakemedia.LooseEightCrazyNights,
	fakemedia.LooseTerminator, fakemedia.LooseLotRReturn,
}

// Movies with a clip set next to a regular file: one group of two versions.
var looseWithFile = []string{
	fakemedia.LooseElemental, fakemedia.LooseEqualizer3, fakemedia.LooseWillyWonka,
	fakemedia.LoosePoltergeist, fakemedia.LooseSandlot,
}

// looseClipTypes are the DiscInfo types of loose clip sets.
var looseClipTypes = []string{fakemedia.ClipSetBluray, fakemedia.ClipSetDVD}

type looseStackOptions struct {
	settings   map[string]any
	recycleBin bool
	// noMappings configures no path mapping: Dupearr sees the clips only through Plex (the
	// situation of the library the incident happened in).
	noMappings bool
}

// newLooseStack starts the looseclips scenario and a configured, scanned dupearr. Every test ends
// with every clip set complete (or moved as a whole) and without a single DELETE request for a
// clip at Plex or an *arr.
func newLooseStack(t *testing.T, o looseStackOptions) *stack {
	t.Helper()
	var s *stack
	if o.noMappings {
		s = newStackWithoutMappings(t, fakemedia.LooseClips(), o.settings, o.recycleBin)
	} else {
		s = newStack(t, stackOptions{scenario: fakemedia.LooseClips(), settings: o.settings, recycleBin: o.recycleBin})
	}
	t.Cleanup(func() {
		s.env.AssertDiscsIntact(t)
		requireNoClipDeletes(t, s.env)
	})
	return s
}

// newStackWithoutMappings is newStack without path mappings.
func newStackWithoutMappings(t *testing.T, sc *fakemedia.Scenario, settings map[string]any, recycleBin bool) *stack {
	t.Helper()
	env := fakemedia.StartWithOptions(t, fakemedia.Options{Scenario: sc, Dir: t.TempDir()})
	t.Cleanup(func() { env.AssertNoViolations(t) })
	s := &stack{
		t: t, env: env, d: startDupearr(t, startOptions{}), arrIDs: map[string]int64{},
		recycleBin: filepath.Join(env.Dir, "dupearr-recycle"),
	}
	d := s.d
	var ms models.MediaServer
	d.expect(http.MethodPost, "/api/v1/mediaserver", map[string]any{
		"name": "Fake Plex", "kind": "plex", "url": env.Plex.URL, "token": env.PlexToken, "enabled": true,
	}, http.StatusCreated, &ms)
	s.serverID = ms.ID
	for _, name := range []string{fakemedia.InstanceRadarr, fakemedia.InstanceRadarr4K, fakemedia.InstanceSonarr} {
		srv := env.Instances[name]
		var a models.ArrInstance
		d.expect(http.MethodPost, "/api/v1/arr", map[string]any{
			"name": srv.InstanceName, "kind": srv.Kind, "url": srv.URL, "apiKey": srv.APIKey, "enabled": true,
		}, http.StatusCreated, &a)
		s.arrIDs[name] = a.ID
	}
	var libs []models.Library
	d.expect(http.MethodGet, fmt.Sprintf("/api/v1/mediaserver/%d/library", s.serverID), nil, http.StatusOK, &libs)
	for _, l := range libs {
		patch := map[string]any{"enabled": true}
		if l.Type == fakemedia.LibraryMovie {
			patch["scopeGroup"] = scopeGroup
		}
		d.expect(http.MethodPut, fmt.Sprintf("/api/v1/library/%d", l.ID), patch, http.StatusAccepted, nil)
	}
	st := map[string]any{"minAgeHours": 0}
	for k, v := range settings {
		st[k] = v
	}
	if recycleBin {
		st["recycleBinPath"] = s.recycleBin
	}
	d.putSettings(st)
	d.waitIdle()
	s.scan()
	return s
}

// requireNoClipDeletes fails for every request that asked Plex or an *arr to delete a clip-named
// file — files on disk (a clip removed on its own) and stale entries (files already gone) alike.
func requireNoClipDeletes(t testing.TB, env *fakemedia.Env) {
	t.Helper()
	var onDisk, stale []string
	for _, cd := range env.ClipDeletes() {
		if cd.Existed {
			onDisk = append(onDisk, cd.String())
		} else {
			stale = append(stale, cd.String())
		}
	}
	limit := func(s []string) string {
		if len(s) > 8 {
			s = append(s[:8:8], fmt.Sprintf("… %d more", len(s)-8))
		}
		return strings.Join(s, "\n  ")
	}
	if len(onDisk) > 0 {
		t.Errorf("%d DELETE request(s) for disc clips that were on disk (a clip removed on its own):\n  %s", len(onDisk), limit(onDisk))
	}
	if len(stale) > 0 {
		t.Errorf("%d DELETE request(s) for stale clip entries (files already gone; not even these are allowed):\n  %s", len(stale), limit(stale))
	}
}

// looseFixture returns the clip set fixture of a movie.
func looseFixture(t testing.TB, env *fakemedia.Env, title string) fakemedia.ClipSetFixture {
	t.Helper()
	for _, f := range env.ClipSetFixtures() {
		if f.Title == title {
			return f
		}
	}
	t.Fatalf("no clip set fixture for %q", title)
	return fakemedia.ClipSetFixture{}
}

// clipSetsOf returns the group's loose clip set versions.
func clipSetsOf(g groupDetail) []models.GroupFile {
	var out []models.GroupFile
	for _, f := range g.Files {
		if f.Version.Disc != nil && slices.Contains(looseClipTypes, f.Version.Disc.Type) {
			out = append(out, f)
		}
	}
	return out
}

// clipSetOf returns the one clip set version of a group.
func clipSetOf(t testing.TB, g groupDetail) models.GroupFile {
	t.Helper()
	cs := clipSetsOf(g)
	if len(cs) != 1 {
		t.Fatalf("%s: %d clip set versions, want 1 (%s)", groupLabel(g.DuplicateGroup), len(cs), describeVersions(g))
	}
	return cs[0]
}

// describeVersions renders a group's versions for messages (at most 6).
func describeVersions(g groupDetail) string {
	var parts []string
	for i, f := range g.Files {
		if i == 6 {
			parts = append(parts, fmt.Sprintf("… %d more", len(g.Files)-6))
			break
		}
		name, typ := "?", "file"
		if len(f.Version.Parts) > 0 {
			name = path.Base(f.Version.Parts[0].Path)
		}
		if f.Version.Disc != nil {
			typ = f.Version.Disc.Type
		}
		parts = append(parts, fmt.Sprintf("%s %s %s (%d parts)", typ, name, f.Decision, len(f.Version.Parts)))
	}
	return fmt.Sprintf("%d versions: %s", len(g.Files), strings.Join(parts, "; "))
}

// wantClipKey is the key of a clip set: "disc:<serverID>:<hex SHA-1 of the normalized folder +
// ":clips">", the folder as the media server sees it — the same whether or not a path mapping lets
// Dupearr see the files, so adding a mapping does not re-key the group.
func wantClipKey(serverID int64, fx fakemedia.ClipSetFixture) string {
	sum := sha1.Sum([]byte(path.Clean(fx.Folder) + ":clips"))
	return fmt.Sprintf("disc:%d:%s", serverID, hex.EncodeToString(sum[:]))
}

// rawDiscOf returns the raw "disc" object of one version of a group (for members the shared types
// may not declare yet, such as clipCount and mediaIds).
func rawDiscOf(t testing.TB, d *dupearr, groupID, fileID int64) map[string]json.RawMessage {
	t.Helper()
	var g struct {
		Files []struct {
			ID      int64 `json:"id"`
			Version struct {
				Disc map[string]json.RawMessage `json:"disc"`
			} `json:"version"`
		} `json:"files"`
	}
	d.expect(http.MethodGet, fmt.Sprintf("/api/v1/duplicate/%d", groupID), nil, http.StatusOK, &g)
	for _, f := range g.Files {
		if f.ID == fileID {
			return f.Version.Disc
		}
	}
	t.Fatalf("group %d has no version %d", groupID, fileID)
	return nil
}

// wantDynamicRange is the dynamic range of a fixture video.
func wantDynamicRange(v fakemedia.Video) models.DynamicRange {
	switch {
	case v.DOVIProfile > 0:
		return models.DRDolbyVisionHDR10
	case v.ColorTrc == "smpte2084":
		return models.DRHDR10
	}
	return models.DRSDR
}

// checkClipSet checks a group's clip set version against the fixture: one disc version of the
// set's type built from the Plex versions of its clips. localVisible: path mappings let Dupearr see
// the folder (the set then also holds the clips Plex does not list and the loose navigation files).
func checkClipSet(t testing.TB, s *stack, g groupDetail, fx fakemedia.ClipSetFixture, localVisible bool) models.GroupFile {
	t.Helper()
	f := clipSetOf(t, g)
	v, di := f.Version, f.Version.Disc
	if di.Type != fx.Kind || di.Origin != models.DiscOriginPlex {
		t.Errorf("type %q origin %q, want %q from Plex", di.Type, di.Origin, fx.Kind)
	}
	if v.Source != models.SourceDisc {
		t.Errorf("source %q, want %q", v.Source, models.SourceDisc)
	}
	if want := wantClipKey(s.serverID, fx); v.Key != want {
		t.Errorf("key %q, want %q (sha1 of the server folder + \":clips\")", v.Key, want)
	}
	// Parts: every clip Plex lists, and nothing but files of the set.
	parts := partPaths(v)
	for _, n := range fx.Listed {
		if !slices.Contains(parts, fx.Folder+"/"+n) {
			t.Errorf("parts lack the listed clip %s (%d parts)", n, len(parts))
			break
		}
	}
	for _, p := range parts {
		base := path.Base(p)
		if path.Dir(p) != fx.Folder || !(slices.Contains(fx.Clips, base) || slices.Contains(fx.NavFiles, base)) {
			t.Errorf("part %s is not a file of the set", p)
		}
	}
	// What a removal would move, and the size.
	owned := slices.Sorted(slices.Values(di.OwnedEntries))
	switch {
	case localVisible:
		if !slices.Equal(owned, fx.Files) {
			t.Errorf("owned entries (%d) differ from the set's clips and navigation files (%d):\n  got  %s\n  want %s",
				len(owned), len(fx.Files), firstN(owned, 4), firstN(fx.Files, 4))
		}
		if v.TotalSize() != fx.TotalSize {
			t.Errorf("size %d, want %d (every clip and navigation file)", v.TotalSize(), fx.TotalSize)
		}
	default:
		if len(owned) != 0 || di.LocalRoot != "" {
			t.Errorf("without a path mapping the set names local paths: localRoot %q, %d owned entries", di.LocalRoot, len(owned))
		}
		if v.TotalSize() != fx.ListedSize {
			t.Errorf("size %d, want %d (the clips Plex lists)", v.TotalSize(), fx.ListedSize)
		}
	}
	// Attributes from the longest clip (readable loose navigation files may decide the duration).
	// (a DVD's IFO counts frames: allow a second of rounding there)
	okDuration := v.DurationMs == fx.MainDurationMs ||
		(localVisible && fx.FeatureDurationMs > 0 && max(v.DurationMs-fx.FeatureDurationMs, fx.FeatureDurationMs-v.DurationMs) <= 1000)
	if !okDuration {
		t.Errorf("duration %d ms, want %d (main clip %s) or the navigation files' %d", v.DurationMs, fx.MainDurationMs, fx.MainClip, fx.FeatureDurationMs)
	}
	if want := wantResolution(fx.MainVideo.Height); v.Resolution != want {
		t.Errorf("resolution %q, want %q (main clip %s)", v.Resolution, want, fx.MainClip)
	}
	if want := wantDynamicRange(fx.MainVideo); v.DynamicRange != want {
		t.Errorf("dynamic range %q, want %q (main clip %s)", v.DynamicRange, want, fx.MainClip)
	}
	if len(v.AudioTracks) != len(fx.MainAudio) {
		t.Errorf("%d audio tracks, want the main clip's %d", len(v.AudioTracks), len(fx.MainAudio))
	}
	// Clip count and the merged Plex media ids (additive DiscInfo members).
	raw := rawDiscOf(t, s.d, g.ID, f.ID)
	var clipCount int
	counts := []int{len(fx.Listed)}
	if localVisible {
		counts = append(counts, len(fx.Clips), designClipCount(fx))
	}
	if b, ok := raw["clipCount"]; !ok || json.Unmarshal(b, &clipCount) != nil {
		t.Errorf("disc info has no clipCount: %s", rawKeys(raw))
	} else if !slices.Contains(counts, clipCount) {
		t.Errorf("clipCount %d, want one of %v (listed, all clips, all clip-named files)", clipCount, counts)
	}
	if b, ok := raw["mainClip"]; ok {
		var main string
		if json.Unmarshal(b, &main) != nil || main != fx.MainClip {
			t.Errorf("mainClip %s, want %q (the longest clip Plex lists)", b, fx.MainClip)
		}
	}
	var ids []int64
	if b, ok := raw["mediaIds"]; !ok || json.Unmarshal(b, &ids) != nil {
		t.Errorf("disc info has no mediaIds: %s", rawKeys(raw))
	} else if slices.Sort(ids); !slices.Equal(ids, slices.Sorted(slices.Values(fx.MediaIDs))) {
		t.Errorf("mediaIds %v, want the %d Plex media of the listed clips", firstN(ids, 5), len(fx.MediaIDs))
	}
	return f
}

// reDesignClip is the design's clip name: a numbered Blu-ray/AVCHD clip or a DVD clip
// (VTS_NN_N.VOB, VIDEO_TS.VOB/.IFO/.BUP).
var reDesignClip = regexp.MustCompile(`(?i)^(?:[0-9]{5}(?:\.[0-9]+)?\.(?:m2ts|mts|m2t)|VTS_[0-9]{2}_[0-9]\.VOB|VIDEO_TS\.(?:VOB|IFO|BUP))$`)

// designClipCount is the number of clip-named files of a set on disk.
func designClipCount(fx fakemedia.ClipSetFixture) int {
	n := 0
	for _, p := range fx.Files {
		if reDesignClip.MatchString(filepath.Base(p)) {
			n++
		}
	}
	return n
}

func rawKeys(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func firstN[T any](s []T, n int) string {
	if len(s) > n {
		return fmt.Sprintf("%v … (%d)", s[:n], len(s))
	}
	return fmt.Sprintf("%v", s)
}

// requireNoRegularClips fails when an open group (any status but resolved: a resolved group is a
// record of the past) lists a clip-named file as an ordinary version — a clip that could be judged,
// and removed, on its own.
func requireNoRegularClips(t testing.TB, d *dupearr) {
	t.Helper()
	for _, gs := range d.groups() {
		if gs.Status == string(models.GroupResolved) {
			continue
		}
		g := d.group(gs.ID)
		for _, f := range g.Files {
			if f.Version.Disc != nil {
				continue
			}
			for _, p := range f.Version.Parts {
				if fakemedia.IsLooseClipName(path.Base(p.Path)) {
					t.Errorf("%s (%s): the clip %s is an ordinary version (%s)", gs.label(), gs.Status, p.Path, f.Decision)
					break
				}
			}
		}
	}
}

// requireClipsOnDisk fails unless every file of every clip set is where the scenario put it.
func requireClipsOnDisk(t testing.TB, env *fakemedia.Env) {
	t.Helper()
	for _, fx := range env.ClipSetFixtures() {
		for _, p := range fx.Files {
			if _, err := os.Lstat(p); err != nil {
				t.Errorf("%s: %v", fx.Title, err)
				break
			}
		}
	}
}

// clipActions returns the actions whose version is a clip set or a single clip.
func clipActions(t testing.TB, d *dupearr) []models.Action {
	t.Helper()
	var out []models.Action
	for _, a := range d.actions() {
		for _, p := range a.Paths {
			if fakemedia.IsLooseClipName(path.Base(p)) || strings.HasPrefix(a.VersionKey, models.DiscKeyPrefix) {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Detection
// ---------------------------------------------------------------------------

// TestLooseClipsDetection: with default settings every clip set is ONE disc version; a movie whose
// only copy is a clip set (111, 172, 189 clips, a partial set of two, two discs flattened into one
// folder with ".1" names and no film clip) forms no group; a clip set next to a regular file is a
// two-version group flagged full_disc whose set is kept and protected, with the regular file kept as
// the only reliably playable copy; Radarr's tracked clip is reported; the full-length .ts control is
// an ordinary duplicate; no group lists a clip as an ordinary version. A rescan keeps the keys and
// nothing is changed anywhere.
func TestLooseClipsDetection(t *testing.T) {
	t.Parallel()
	s := newLooseStack(t, looseStackOptions{})
	d := s.d
	summaries := rawSummaries(t, d)

	requireNoRegularClips(t, d)
	for _, title := range looseClipOnly {
		t.Run(title, func(t *testing.T) {
			for _, gs := range d.groupsBy(title) {
				g := d.group(gs.ID)
				t.Errorf("a group of clips alone: %s %v (%s)", g.Status, g.Flags, describeVersions(g))
			}
		})
	}
	keys := map[string]string{}
	for _, title := range looseWithFile {
		t.Run(title, func(t *testing.T) {
			fx := looseFixture(t, s.env, title)
			g := d.groupNamed(title)
			if len(g.Files) != 2 {
				t.Fatalf("%s", describeVersions(g))
			}
			f := checkClipSet(t, s, g, fx, true)
			if !g.HasFlag(models.FlagFullDisc) {
				t.Errorf("flags %v, want %s", g.Flags, models.FlagFullDisc)
			}
			if f.Decision != models.DecisionKeep || !f.Protected || f.ProtectedReason == "" {
				t.Errorf("clip set %s protected %t (%q), want a protected keeper", f.Decision, f.Protected, f.ProtectedReason)
			}
			reg := regularFiles(g)
			if len(reg) != 1 || reg[0].Decision != models.DecisionKeep {
				t.Errorf("the regular copy next to the clips: %s, want kept (%s)", describeVersions(g), g.StatusReason)
			}
			if g.Status != models.GroupProtected && g.Status != models.GroupReview {
				t.Errorf("group %s (%s), want protected or review: nothing can be removed", g.Status, g.StatusReason)
			}
			sum, ok := summaries[title]
			if !ok || sum.FileCount != 2 {
				t.Fatalf("summary %+v, want 2 files", sum)
			}
			for _, sf := range sum.Files {
				if sf.ID != f.ID {
					continue
				}
				var sd summaryDisc
				if err := json.Unmarshal(sf.Disc, &sd); err != nil || sd.Type != fx.Kind {
					t.Errorf("summary disc %s (%v), want type %q", sf.Disc, err, fx.Kind)
				}
			}
			keys[title] = f.Version.Key
		})
	}
	t.Run("the clip set is not the playable copy", func(t *testing.T) {
		// The clip set ranks first here (2160p against 1080p, or disc against an encode): the regular
		// file is kept anyway, because a movie can span clips and Plex cannot play a set as one copy.
		for _, title := range []string{fakemedia.LooseEqualizer3, fakemedia.LooseWillyWonka, fakemedia.LoosePoltergeist} {
			g := d.groupNamed(title)
			cs := clipSetOf(t, g)
			reg := regularFiles(g)
			if cs.Rank != 1 {
				t.Logf("%s: the clip set ranks %d (%s)", title, cs.Rank, cs.DecidingCriterion)
				continue
			}
			if len(reg) != 1 {
				t.Errorf("%s: %s", title, describeVersions(g))
				continue
			}
			if reg[0].Decision != models.DecisionKeep || !strings.Contains(reg[0].ProtectedReason, "playable") {
				t.Errorf("%s: regular copy %s (%q), want kept as the playable copy", title, reg[0].Decision, reg[0].ProtectedReason)
			}
		}
	})
	t.Run("tracked clip", func(t *testing.T) {
		fx := looseFixture(t, s.env, fakemedia.LoosePoltergeist)
		g := d.groupNamed(fakemedia.LoosePoltergeist)
		cs := clipSetOf(t, g)
		if !g.HasFlag(models.FlagDiscTracked) || cs.Version.Disc.TrackedClip != fx.TrackedPath || cs.Version.Arr == nil {
			t.Errorf("flags %v, trackedClip %q (Radarr tracks %q), arr %+v", g.Flags, cs.Version.Disc.TrackedClip, fx.TrackedPath, cs.Version.Arr)
		}
		for _, r := range regularFiles(g) {
			if r.Version.Arr != nil {
				t.Errorf("the untracked WEB-DL is attributed to %s", r.Version.Arr.InstanceName)
			}
		}
	})
	t.Run("the .ts control", func(t *testing.T) {
		g := d.groupNamed(fakemedia.LooseJumanji)
		if len(g.Files) != 2 || len(discFiles(g)) != 0 || g.HasFlag(models.FlagFullDisc) {
			t.Fatalf("Jumanji: %s, flags %v; want two ordinary versions", describeVersions(g), g.Flags)
		}
		ts := regularContaining(t, g, "Jumanji (1995).ts")
		if ts.Version.Container != "ts" {
			t.Errorf("the full-length .ts: container %q", ts.Version.Container)
		}
		if _, rm := filesOf(g); len(rm) != 1 {
			t.Errorf("Jumanji: %d removals, want the ordinary duplicate handled as usual (%s)", len(rm), g.StatusReason)
		}
	})

	s.env.ResetRequests()
	s.scan()
	for title, key := range keys {
		if f := clipSetOf(t, d.groupNamed(title)); f.Version.Key != key {
			t.Errorf("%s: key %s after a rescan, was %s", title, f.Version.Key, key)
		}
	}
	for _, title := range looseClipOnly {
		if gs := d.groupsBy(title); len(gs) != 0 {
			t.Errorf("%s: a group of clips alone after a rescan", title)
		}
	}
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("detection changed the fake world:%s", describeRequests(m))
	}
	if acts := d.actions(); len(acts) != 0 {
		t.Fatalf("detection created actions: %+v", acts)
	}
}

// TestLooseClipsWithoutPathMappings: without any path mapping (Dupearr cannot see the files) the
// clip sets are built from Plex's part paths alone — same keys, parts = the listed clips, size = their
// sum, no local paths — and nothing can remove them even with disc removal allowed, while the .ts
// control is still removed through Plex/Radarr.
func TestLooseClipsWithoutPathMappings(t *testing.T) {
	t.Parallel()
	settings := map[string]any{"dryRun": false, "allowDiscRemoval": true, "deletionMethods": []string{"arr", "plex", "filesystem"}}
	s := newLooseStack(t, looseStackOptions{noMappings: true, settings: settings, recycleBin: true})
	d := s.d
	requireNoRegularClips(t, d)
	for _, title := range looseClipOnly {
		if gs := d.groupsBy(title); len(gs) != 0 {
			t.Errorf("%s: a group of clips alone", title)
		}
	}
	for _, title := range looseWithFile {
		t.Run(title, func(t *testing.T) {
			fx := looseFixture(t, s.env, title)
			g := d.groupNamed(title)
			f := checkClipSet(t, s, g, fx, false)
			if f.Decision == models.DecisionRemove {
				t.Errorf("a clip set Dupearr cannot reach is marked for removal (%q)", f.ProtectedReason)
			}
			if f.Decision != models.DecisionRemove {
				requireOverrideRefused(t, d, title, f)
			}
			if r := d.approve(g.ID, g.Signature); r.Status == http.StatusOK {
				d.waitIdle()
				for _, a := range d.actionsOf(g.ID) {
					if a.VersionKey == f.Version.Key && a.Status == models.ActionSucceeded {
						t.Errorf("the unreachable clip set was removed: %+v", a)
					}
				}
			}
		})
	}
	// The control: an ordinary duplicate is still removed (through Radarr or Plex).
	j := d.groupNamed(fakemedia.LooseJumanji)
	if r := d.approve(j.ID, j.Signature); r.Status != http.StatusOK {
		t.Fatalf("approve Jumanji: %s", r)
	}
	d.waitIdle()
	if acts := d.actionsOf(j.ID); len(acts) != 1 || acts[0].Status != models.ActionSucceeded {
		t.Fatalf("Jumanji: actions %+v, want the ordinary duplicate removed", acts)
	}
	for _, a := range clipActions(t, d) {
		if a.Status == models.ActionSucceeded {
			t.Errorf("clip action carried out: %+v", a)
		}
	}
	requireClipsOnDisk(t, s.env)
}

// ---------------------------------------------------------------------------
// Protection
// ---------------------------------------------------------------------------

// TestLooseClipsNeverRemovedByDefault: dry run OFF, disc removal off (the setting the incident ran
// with). Every clip set is a protected keeper; approving its group is refused (400/409, "Nothing to
// remove"), marking the set for removal is refused, a bulk approval of every group removes only the
// .ts control's loser, and not one clip is touched.
func TestLooseClipsNeverRemovedByDefault(t *testing.T) {
	t.Parallel()
	s := newLooseStack(t, looseStackOptions{settings: map[string]any{"dryRun": false}})
	d := s.d
	s.env.ResetRequests()
	requireNoRegularClips(t, d)

	var ids []int64
	for _, title := range looseWithFile {
		t.Run(title, func(t *testing.T) {
			g := d.groupNamed(title)
			f := clipSetOf(t, g)
			if f.Decision != models.DecisionKeep || !f.Protected {
				t.Errorf("clip set %s protected %t (%q)", f.Decision, f.Protected, f.ProtectedReason)
			}
			for _, x := range g.Files {
				if x.Decision == models.DecisionRemove {
					t.Errorf("%s is marked for removal (%s)", describeVersions(g), g.StatusReason)
					break
				}
			}
			r := d.approve(g.ID, g.Signature)
			if r.Status != http.StatusBadRequest && r.Status != http.StatusConflict {
				t.Errorf("approve: %s, want 400/409", r)
			}
			t.Logf("approve refused: %s", r.message())
			requireOverrideRefused(t, d, title, f)
			ids = append(ids, g.ID)
		})
	}
	if res := d.bulk("approve", ids...); len(res.Succeeded) != 0 {
		t.Errorf("bulk approve of the clip set groups: %+v, want every one refused", res)
	}
	j := d.groupNamed(fakemedia.LooseJumanji)
	if r := d.approve(j.ID, j.Signature); r.Status != http.StatusOK {
		t.Errorf("approve Jumanji (the control): %s", r)
	}
	d.waitIdle()
	d.runCommand(models.CmdProcessQueue, nil)
	for _, a := range d.actions() {
		if a.GroupID != j.ID {
			t.Errorf("action outside the control: %+v", a)
		}
	}
	if acts := d.actionsOf(j.ID); len(acts) != 1 || acts[0].Status != models.ActionSucceeded {
		t.Errorf("Jumanji: %+v, want its loser removed (dry run is off)", acts)
	}
	for _, title := range looseClipOnly {
		if gs := d.groupsBy(title); len(gs) != 0 {
			t.Errorf("%s: a group of clips alone", title)
		}
	}
	requireClipsOnDisk(t, s.env)
	requireNoClipDeletes(t, s.env)
}

// TestLooseClipsAutoModeNeverApproves: auto mode, real removals, disc removal allowed: repeated
// scans remove the .ts control's loser but never approve a group holding a clip set.
func TestLooseClipsAutoModeNeverApproves(t *testing.T) {
	t.Parallel()
	settings := map[string]any{"mode": models.ModeAuto, "stableScansRequired": 1}
	for k, v := range allowDiscRemovals {
		settings[k] = v
	}
	s := newLooseStack(t, looseStackOptions{settings: settings, recycleBin: true})
	d := s.d
	for range 3 {
		s.scan()
	}
	j := d.groupNamed(fakemedia.LooseJumanji)
	if acts := d.actionsOf(j.ID); len(acts) != 1 || acts[0].Status != models.ActionSucceeded {
		t.Fatalf("Jumanji (the control): %s, actions %+v — auto mode did not approve a plain duplicate", j.Status, acts)
	}
	for _, title := range looseWithFile {
		g := d.groupNamed(title)
		if acts := d.actionsOf(g.ID); len(acts) > 0 {
			t.Errorf("%s: auto mode approved it: %+v", title, acts)
		}
		if !g.HasFlag(models.FlagFullDisc) || g.Status == models.GroupQueued || g.Status == models.GroupResolved {
			t.Errorf("%s: %s %v", title, g.Status, g.Flags)
		}
	}
	if acts := clipActions(t, d); len(acts) > 0 {
		t.Errorf("clip actions: %+v", acts)
	}
	requireClipsOnDisk(t, s.env)
}

// ---------------------------------------------------------------------------
// Whole-set removal (opt-in)
// ---------------------------------------------------------------------------

// TestLooseClipSetRemovalAndRestore: with disc removal allowed, a recycle bin and real removals, a
// person removes a whole clip set — The Equalizer 3 (129 listed + 2 unlisted clips and 5 loose
// navigation files next to the MKV), The Sandlot's flat DVD and Willy Wonka's set of ONE clip:
//   - a bulk approval is refused (a set must be approved on its own);
//   - the action runs through the filesystem method into the recycle bin (not permanent);
//   - exactly the clip-named files and loose navigation files move, renamed (same inodes, sizes,
//     times) into <bin>/<YYYY-MM-DD>/<path below the mapped folder>; the MKV, NFO, artwork and
//     subtitles stay; no DELETE reaches Plex or an *arr; the group is resolved;
//   - the restore puts back the identical tree and a second restore is refused.
func TestLooseClipSetRemovalAndRestore(t *testing.T) {
	t.Parallel()
	s := newLooseStack(t, looseStackOptions{settings: allowDiscRemovals, recycleBin: true})
	d := s.d
	for _, title := range []string{fakemedia.LooseEqualizer3, fakemedia.LooseSandlot, fakemedia.LooseWillyWonka} {
		t.Run(title, func(t *testing.T) {
			fx := looseFixture(t, s.env, title)
			writeSidecars(t, fx.LocalFolder)
			names := make([]string, 0, len(fx.Files))
			for _, p := range fx.Files {
				names = append(names, filepath.Base(p))
			}
			before := snapshot(t, fx.LocalFolder)

			g := d.groupNamed(title)
			cs := clipSetOf(t, g)
			if cs.Decision == models.DecisionKeep {
				g = setOverride(t, d, g.ID, cs.ID, "remove")
				cs = clipSetOf(t, g)
			}
			keep, remove := filesOf(g)
			if len(remove) != 1 || remove[0].ID != cs.ID || len(keep) != 1 {
				t.Fatalf("keep %d, remove %d: %s; want the clip set removed and the regular file kept", len(keep), len(remove), describeVersions(g))
			}
			regular := keep[0]
			if res := d.bulk("approve", g.ID); len(res.Succeeded) != 0 {
				t.Fatalf("bulk approval of a clip set removal: %+v, want refused", res)
			}
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
			if a.VersionKey != cs.Version.Key {
				t.Errorf("action key %s, want %s", a.VersionKey, cs.Version.Key)
			}
			recycled := strings.Split(strings.TrimSpace(a.RecyclePath), "\n")
			folderBelowRoot := strings.TrimPrefix(fx.Folder, fakemedia.RemoteMediaRoot+"/")
			var got, want []string
			var day string
			for _, rp := range recycled {
				dd, rest := recycledDay(t, s.recycleBin, rp)
				if day != "" && dd != day {
					t.Errorf("files of one set in two dated folders: %s, %s", day, dd)
				}
				day = dd
				got = append(got, rest)
			}
			for _, n := range names {
				want = append(want, folderBelowRoot+"/"+n)
			}
			sort.Strings(got)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Fatalf("recycled %d entries %s, want exactly the set's %d files %s", len(got), firstN(got, 4), len(want), firstN(want, 4))
			}
			binFolder := filepath.Join(s.recycleBin, day, filepath.FromSlash(folderBelowRoot))
			requireSameTree(t, "the recycled set", filterSnapshot(before, names, true), snapshot(t, binFolder))
			requireSameTree(t, "the movie folder after the removal", filterSnapshot(before, names, false), snapshot(t, fx.LocalFolder))
			s.requireFile(regular.Version.Parts[0].Path, true)
			if ds := deletes(s.env); len(ds) > 0 {
				t.Errorf("a clip set removal sent DELETE requests:%s", describeRequests(ds))
			}
			if !plexScannedFolder(s.env, fx.Folder) {
				t.Errorf("Plex was not asked to scan %s", fx.Folder)
			}
			if after := d.group(g.ID); after.Status != models.GroupResolved {
				t.Errorf("group after the removal: %s (%s), want resolved", after.Status, after.StatusReason)
			}
			if p := s.env.DiscProblems(); len(p) > 0 {
				t.Fatalf("sets damaged: %q", p)
			}

			s.env.ResetRequests()
			var restored models.Action
			d.expect(http.MethodPost, fmt.Sprintf("/api/v1/action/%d/restore", a.ID), nil, http.StatusOK, &restored)
			d.waitIdle()
			requireSameTree(t, "the movie folder after the restore", before, snapshot(t, fx.LocalFolder))
			if r := d.request(http.MethodPost, fmt.Sprintf("/api/v1/action/%d/restore", a.ID), nil); r.Status != http.StatusConflict {
				t.Errorf("second restore: %s, want 409", r)
			}
			if ds := deletes(s.env); len(ds) > 0 {
				t.Errorf("the restore sent DELETE requests:%s", describeRequests(ds))
			}
			back := d.group(g.ID)
			t.Logf("after the restore: %s (%s)", back.Status, back.StatusReason)
			if back.Status == models.GroupQueued {
				t.Errorf("the restored set is queued for removal again")
			}
		})
	}
	requireNoRegularClips(t, d)
}

// TestLooseClipTrackedByRadarr: Radarr tracks Poltergeist's largest clip. The clip is never deleted
// through Radarr: a removal of the set is refused without the filesystem method, and when a person
// removes the set, every clip goes through the filesystem method, Radarr is only asked to rescan
// (it then tracks the WEB-DL that stayed) and no DELETE reaches Radarr or Plex.
func TestLooseClipTrackedByRadarr(t *testing.T) {
	t.Parallel()
	s := newLooseStack(t, looseStackOptions{settings: allowDiscRemovals, recycleBin: true})
	d := s.d
	fx := looseFixture(t, s.env, fakemedia.LoosePoltergeist)
	g := d.groupNamed(fakemedia.LoosePoltergeist)
	cs := clipSetOf(t, g)
	if !g.HasFlag(models.FlagDiscTracked) || cs.Version.Arr == nil {
		t.Fatalf("Poltergeist: flags %v, clip set arr %+v", g.Flags, cs.Version.Arr)
	}
	web := regularFiles(g)[0]
	if cs.Decision == models.DecisionKeep {
		g = setOverride(t, d, g.ID, cs.ID, "remove")
	}
	putSettingsSettled(t, d, map[string]any{"deletionMethods": []string{"arr", "plex"}})
	s.env.ResetRequests()
	g = d.group(g.ID)
	if r := d.approve(g.ID, g.Signature); r.Status != http.StatusConflict && r.Status != http.StatusBadRequest {
		t.Fatalf("approve with arr/plex only: %s, want refused", r)
	} else {
		t.Logf("refused: %s", r.message())
	}
	d.waitIdle()
	for _, a := range d.actionsOf(g.ID) {
		if a.Status == models.ActionSucceeded {
			t.Fatalf("carried out without the filesystem method: %+v", a)
		}
	}
	if ds := deletes(s.env); len(ds) > 0 {
		t.Fatalf("DELETE requests:%s", describeRequests(ds))
	}

	putSettingsSettled(t, d, map[string]any{"deletionMethods": []string{"arr", "filesystem", "plex"}})
	s.env.ResetRequests()
	g = d.group(g.ID)
	if cs := clipSetOf(t, g); cs.Decision != models.DecisionRemove {
		g = setOverride(t, d, g.ID, cs.ID, "remove")
	}
	if r := d.approve(g.ID, g.Signature); r.Status != http.StatusOK {
		t.Fatalf("approve: %s", r)
	}
	d.waitIdle()
	var done *models.Action
	for _, a := range d.actionsOf(g.ID) {
		if a.Status == models.ActionSucceeded {
			done = &a
		}
	}
	if done == nil || done.Method != models.MethodFilesystem {
		t.Fatalf("actions %+v, want one filesystem removal", d.actionsOf(g.ID))
	}
	for _, p := range fx.Files {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still in place (%v)", p, err)
		}
	}
	if ds := deletes(s.env); len(ds) > 0 {
		t.Fatalf("the set removal sent DELETE requests (the tracked clip must never be deleted through Radarr):%s", describeRequests(ds))
	}
	if !radarrRescanned(s.env, 609) {
		t.Errorf("Radarr was not asked to rescan Poltergeist:%s", describeRequests(mutations(s.env)))
	}
	if now := s.env.ArrMovieFilePath(fakemedia.InstanceRadarr, 609); now != web.Version.Parts[0].Path {
		t.Errorf("after the rescan Radarr tracks %q, want the WEB-DL %q", now, web.Version.Parts[0].Path)
	}
	s.requireFile(web.Version.Parts[0].Path, true)
}

// ---------------------------------------------------------------------------
// Upgrade from per-clip groups
// ---------------------------------------------------------------------------

// preClipFixCommit is the last commit whose Dupearr treats every loose clip as its own version.
const preClipFixCommit = "9a298dc"

// oldDupearrBinary returns a Dupearr binary built from preClipFixCommit into the test's temporary
// directory (DUPEARR_E2E_OLD_BINARY overrides). The build needs git and the commit in the
// repository's history; the test is skipped without them.
func oldDupearrBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("DUPEARR_E2E_OLD_BINARY"); p != "" {
		return p
	}
	root, err := repoRoot()
	if err != nil {
		t.Skipf("no pre-fix binary: %v", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("no pre-fix binary: git: %v", err)
	}
	if err := exec.Command("git", "-C", root, "cat-file", "-e", preClipFixCommit+"^{commit}").Run(); err != nil {
		t.Skipf("no pre-fix binary: commit %s is not in the history: %v", preClipFixCommit, err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	archive := exec.Command("git", "-C", root, "archive", "--format=tar", preClipFixCommit)
	out, err := archive.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Start(); err != nil {
		t.Fatal(err)
	}
	extractErr := extractTar(out, src)
	if err := archive.Wait(); err != nil || extractErr != nil {
		t.Fatalf("git archive %s: %v %v", preClipFixCommit, err, extractErr)
	}
	bin := filepath.Join(dir, "dupearr-old")
	build := exec.Command("go", "build", "-o", bin, "./cmd/dupearr")
	build.Dir = src
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", preClipFixCommit, err, b)
	}
	return bin
}

// extractTar writes a tar stream below dir (regular files and directories only).
func extractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dir, filepath.FromSlash(h.Name))
		if rel, err := filepath.Rel(dir, p); err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("tar entry %q escapes the directory", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0o755|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}

// launchWith starts the given binary on dataDir (not parallel-safe: callers must not run in
// parallel, see TestLooseClipsUpgradeFromPerClipGroups).
func launchWith(t *testing.T, bin, dataDir string) *dupearr {
	t.Helper()
	saved := dupearrBin
	dupearrBin = bin
	defer func() { dupearrBin = saved }()
	for attempt := 1; ; attempt++ {
		d, err := launch(t, dataDir, startOptions{})
		if err == nil {
			return d
		}
		if errors.Is(err, errPortInUse) && attempt < 5 {
			continue
		}
		t.Fatalf("starting %s: %v", bin, err)
	}
}

// TestLooseClipsUpgradeFromPerClipGroups replays the incident with the pre-fix binary and upgrades:
//  1. the old Dupearr shows every clip as its own version (Bad Boys: 111 "copies", The Equalizer 3:
//     the MKV + 129 clips, Terminator Genisys: 2 clips); a person approves those groups with dry run
//     off while the movies play, so the per-clip removals wait in the queue;
//  2. the new Dupearr starts on the same data directory; once nothing plays the queue runs — every
//     queued clip removal is refused at execution time (never carried out, no DELETE reaches Plex);
//  3. a rescan (and repeated auto-mode scans) never approves or removes a clip: the clip-only movies
//     have no open group left, The Equalizer 3 is the MKV + one protected clip set, and no per-clip
//     override survives the re-keying.
//
// It is not parallel: it swaps the binary under test while launching the old one.
func TestLooseClipsUpgradeFromPerClipGroups(t *testing.T) {
	oldBin := oldDupearrBinary(t)
	env := fakemedia.StartWithOptions(t, fakemedia.Options{Scenario: fakemedia.LooseClips(), Dir: t.TempDir()})
	t.Cleanup(func() {
		env.AssertNoViolations(t)
		env.AssertDiscsIntact(t)
		requireNoClipDeletes(t, env)
	})
	dataDir := t.TempDir()

	// 1. The old binary: per-clip groups, approved while the movies play.
	old := &stack{t: t, env: env, d: launchWith(t, oldBin, dataDir), arrIDs: map[string]int64{}, recycleBin: filepath.Join(env.Dir, "dupearr-recycle")}
	old.configure()
	old.d.putSettings(map[string]any{"minAgeHours": 0, "dryRun": false, "deletionMethods": []string{"arr", "plex", "filesystem"}})
	old.d.waitIdle()
	old.scan()
	approved := []string{fakemedia.LooseBadBoys, fakemedia.LooseEqualizer3, fakemedia.LooseTerminator}
	var playing []string
	for _, title := range approved {
		playing = append(playing, looseFixture(t, env, title).RatingKey)
	}
	env.SetPlaying(playing...)
	queued := map[string]int{}
	oldGroups := map[string]int64{}
	for _, title := range approved {
		g := old.d.groupNamed(title)
		oldGroups[title] = g.ID
		clipVersions := 0
		for _, f := range g.Files {
			if len(f.Version.Parts) > 0 && fakemedia.IsLooseClipName(path.Base(f.Version.Parts[0].Path)) && f.Version.Disc == nil {
				clipVersions++
			}
		}
		t.Logf("pre-fix %s: %s, %d versions (%d single clips), flags %v", title, g.Status, len(g.Files), clipVersions, g.Flags)
		if clipVersions < 2 {
			t.Fatalf("the pre-fix binary does not show %s clip by clip (%s): the replay needs per-clip groups", title, describeVersions(g))
		}
		if r := old.d.approve(g.ID, g.Signature); r.Status != http.StatusOK {
			t.Fatalf("pre-fix approve %s: %s", title, r)
		}
		old.d.waitIdle()
		for _, a := range old.d.actionsOf(g.ID) {
			if a.Status != models.ActionPending {
				t.Fatalf("pre-fix %s: action %s (%s), want every removal waiting while the movie plays", title, a.Status, a.Message)
			}
			queued[title]++
		}
	}
	t.Logf("queued per-clip removals: %v", queued)
	requireNoClipDeletes(t, env)
	if !old.d.stop() {
		t.Fatalf("the pre-fix binary did not stop on SIGTERM")
	}

	// 2. The new binary on the same data: the queue runs once nothing plays.
	s := &stack{t: t, env: env, d: launchWith(t, dupearrBin, dataDir), arrIDs: old.arrIDs, serverID: old.serverID, recycleBin: old.recycleBin}
	d := s.d
	d.waitIdle()
	env.SetPlaying()
	d.runCommand(models.CmdProcessQueue, nil)
	statuses := map[models.ActionStatus]int{}
	for _, a := range clipActions(t, d) {
		statuses[a.Status]++
		if a.Status == models.ActionSucceeded || a.Status == models.ActionRunning {
			t.Errorf("queued per-clip removal %s: %s (%s)", path.Base(strings.Join(a.Paths, ",")), a.Status, a.Message)
		}
	}
	t.Logf("queued removals after the upgrade: %v", statuses)
	for _, a := range d.actions() {
		if a.Status != models.ActionPending {
			t.Logf("e.g. %s: %s", a.Status, a.Message)
			break
		}
	}
	requireNoClipDeletes(t, env)
	requireClipsOnDisk(t, env)

	// 3. Rescans never approve or remove a clip.
	s.scan()
	d.putSettings(map[string]any{"mode": models.ModeAuto, "stableScansRequired": 1})
	d.waitIdle()
	for range 2 {
		s.scan()
	}
	d.runCommand(models.CmdProcessQueue, nil)
	requireNoRegularClips(t, d)
	// The stored per-clip groups cannot be approved again.
	for title, id := range oldGroups {
		g := d.group(id)
		if r := d.approve(id, g.Signature); r.Status == http.StatusOK {
			d.waitIdle()
			t.Errorf("the stored per-clip group of %s (%s) was approved again", title, g.Status)
		} else {
			t.Logf("stored group of %s: %s (%d versions); approving it: %s", title, g.Status, len(g.Files), r.message())
		}
	}
	for _, title := range looseClipOnly {
		for _, gs := range d.groupsBy(title) {
			if gs.Status != string(models.GroupResolved) && gs.Status != string(models.GroupIgnored) {
				t.Errorf("%s: an open group of clips alone after the upgrade: %s (%d files)", title, gs.Status, gs.FileCount)
			}
		}
	}
	var eqOpen []groupSummary
	for _, gs := range d.groupsBy(fakemedia.LooseEqualizer3) {
		if gs.Status != string(models.GroupResolved) {
			eqOpen = append(eqOpen, gs)
		}
	}
	if len(eqOpen) != 1 {
		t.Fatalf("The Equalizer 3: %d open groups after the upgrade, want 1", len(eqOpen))
	}
	eq := d.group(eqOpen[0].ID)
	cs := clipSetOf(t, eq)
	if len(eq.Files) != 2 || cs.Decision != models.DecisionKeep || cs.Override != "" {
		t.Errorf("The Equalizer 3 after the upgrade: %s; clip set %s (override %q) — want the MKV and one kept clip set, no per-clip override left",
			describeVersions(eq), cs.Decision, cs.Override)
	}
	for _, a := range clipActions(t, d) {
		if a.Status == models.ActionSucceeded {
			t.Errorf("a clip action was carried out after the upgrade: %s %s (%s)", a.Title, strings.Join(a.Paths, ","), a.Message)
		}
	}
	requireClipsOnDisk(t, env)
	requireNoClipDeletes(t, env)
}
