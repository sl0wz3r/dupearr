//go:build e2e

package e2e

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Full-disc backups (docs/research/disc-structures.md, docs/DECISIONS.md D9), end to end: the real
// binary against the fakemedia "discs" scenario, whose disc trees are complete and valid (BDMV with
// hundreds of files, VIDEO_TS, ISO, a two-disc set with a bonus disc, a damaged disc, a clip Radarr
// tracks, a TV season disc), plus a few movies added below. Every stack also fails when a disc ends
// up partially removed (fakemedia.Env.AssertDiscsIntact) or when Plex or an *arr was asked to delete
// a file of a disc (a fakemedia safety violation).

// Titles (group labels) of the disc scenario.
const (
	titleBladeRunner = "Blade Runner 2049"
	titleDarkKnight  = "The Dark Knight"
	titleCasablanca  = "Casablanca"
	titleHeat        = "Heat"
	titleLotR        = "The Lord of the Rings: The Fellowship of the Ring"
	titleAlien       = "Alien"
	titleGladiator   = "Gladiator"
	titleTenet       = "Tenet"
	titleInception   = "Inception"
	titleOppenheimer = "Oppenheimer" // added by discScenario
	titleTopGun      = "Top Gun: Maverick"
	titleArrival     = "Arrival"
	labelPlanetEarth = "Planet Earth II S01E01"

	gladiatorTmdbID = 98
)

// discScenario is fakemedia.Discs() plus:
//   - Oppenheimer: a UHD disc and a 2160p HDR10 remux (same resolution and dynamic range, so the
//     source decides: remux before disc);
//   - Top Gun: Maverick: a UHD disc, a 2160p HDR10 Blu-ray encode (Radarr) and a 1080p WEB-DL (the
//     disc ranks first by source; the encode is kept as Plex's playable copy; the WEB-DL goes);
//   - Arrival: a plain duplicate without any disc (the auto-mode control);
//   - a second, untracked copy of Planet Earth II S01E01 in the season folder that holds a BDMV
//     (the TV disc is only flagged, never a version).
//
// customScanner makes the movie libraries use the legacy "Plex Movie Scanner with Disc Image
// Support": every disc becomes one Plex version with one part per BDMV/STREAM clip.
func discScenario(customScanner bool) *fakemedia.Scenario {
	sc := fakemedia.Discs()
	if customScanner {
		for i := range sc.Libraries {
			if sc.Libraries[i].Type == fakemedia.LibraryMovie {
				sc.Libraries[i].Scanner = fakemedia.ScannerMovieDiscImage
			}
		}
	}
	opp := fakemedia.DirMovies + "/Oppenheimer (2023)"
	sc.AddMovie(fakemedia.Movie{
		Section: fakemedia.SectionMovies, Title: titleOppenheimer, Year: 2023, TmdbID: 872585, ImdbID: "tt15398776",
		Versions: []fakemedia.Version{{
			Parts: []fakemedia.Part{{File: opp + "/Oppenheimer (2023) [Remux-2160p][HDR10][DTS-HD MA 5.1]-FraMeSToR.mkv", Size: fakemedia.GiB(78.4)}},
			Video: fakemedia.HDR10UHD(), Audio: []fakemedia.Audio{fakemedia.DTSHDMA("eng", 6)},
			DurationMs: fakemedia.Mins(180.5), Tracked: fakemedia.InstanceRadarr,
		}},
		Discs: []fakemedia.Disc{{
			Kind: fakemedia.DiscUHDBluray, Root: opp, FeatureSize: fakemedia.GiB(79.1), DurationMs: fakemedia.Mins(180.5),
			Video: fakemedia.HDR10UHD(), Audio: []fakemedia.Audio{fakemedia.DTSHDMA("eng", 6)}, FeatureClips: 6,
		}},
	})
	tg := fakemedia.DirMovies + "/Top Gun Maverick (2022)"
	sc.AddMovie(fakemedia.Movie{
		Section: fakemedia.SectionMovies, Title: titleTopGun, Year: 2022, TmdbID: 361743, ImdbID: "tt1745960",
		Versions: []fakemedia.Version{
			{
				Parts: []fakemedia.Part{{File: tg + "/Top Gun Maverick (2022) [Bluray-2160p][HDR10][TrueHD Atmos 7.1][x265]-SWTYBLZ.mkv", Size: fakemedia.GiB(31.4)}},
				Video: fakemedia.HDR10UHD(), Audio: []fakemedia.Audio{fakemedia.TrueHDAtmos("eng")},
				DurationMs: fakemedia.Mins(130.7), Tracked: fakemedia.InstanceRadarr,
			},
			{
				Parts: []fakemedia.Part{{File: tg + "/Top.Gun.Maverick.2022.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb.mkv", Size: fakemedia.GiB(9.8)}},
				Video: fakemedia.FHD("h264"), Audio: []fakemedia.Audio{fakemedia.EAC3("eng", 6)}, DurationMs: fakemedia.Mins(130.7),
			},
		},
		Discs: []fakemedia.Disc{{
			Kind: fakemedia.DiscUHDBluray, Root: tg, FeatureSize: fakemedia.GiB(62.3), DurationMs: fakemedia.Mins(130.7),
			Video: fakemedia.HDR10UHD(), Audio: []fakemedia.Audio{fakemedia.TrueHDAtmos("eng")}, FeatureClips: 5,
		}},
	})
	arv := fakemedia.DirMovies + "/Arrival (2016)"
	sc.AddMovie(fakemedia.Movie{
		Section: fakemedia.SectionMovies, Title: titleArrival, Year: 2016, TmdbID: 329865, ImdbID: "tt2543164",
		Versions: []fakemedia.Version{
			{
				Parts: []fakemedia.Part{{File: arv + "/Arrival (2016) [Bluray-2160p][HDR10][TrueHD Atmos 7.1][x265].mkv", Size: fakemedia.GiB(24.6)}},
				Video: fakemedia.HDR10UHD(), Audio: []fakemedia.Audio{fakemedia.TrueHDAtmos("eng")},
				DurationMs: fakemedia.Mins(116.3), Tracked: fakemedia.InstanceRadarr,
			},
			{
				Parts: []fakemedia.Part{{File: arv + "/Arrival.2016.1080p.WEB-DL.DD5.1.H264-FGT.mkv", Size: fakemedia.GiB(4.2)}},
				Video: fakemedia.FHD("h264"), Audio: []fakemedia.Audio{fakemedia.EAC3("eng", 6)}, DurationMs: fakemedia.Mins(116.3),
			},
		},
	})
	for i := range sc.Shows {
		sh := &sc.Shows[i]
		if sh.Title != "Planet Earth II" {
			continue
		}
		for j := range sh.Episodes {
			if ep := &sh.Episodes[j]; ep.Season == 1 && ep.Episode == 1 {
				ep.Versions = append(ep.Versions, fakemedia.Version{
					Parts: []fakemedia.Part{{File: sh.Folder + "/Season 01/Planet Earth II (2016) - S01E01 - Islands [HDTV-720p][AAC 2.0][x264].mkv", Size: fakemedia.GiB(1.9)}},
					Video: fakemedia.HD("h264"), Audio: []fakemedia.Audio{fakemedia.AAC("eng", 2)}, DurationMs: fakemedia.Mins(58),
				})
			}
		}
	}
	return sc
}

// discStackOptions configures newDiscStack.
type discStackOptions struct {
	customScanner bool
	settings      map[string]any
	recycleBin    bool
}

// allowDiscRemovals are the settings under which a person may remove a whole disc (the recycle bin
// is set by discStackOptions.recycleBin).
var allowDiscRemovals = map[string]any{
	"dryRun": false, "allowDiscRemoval": true, "deletionMethods": []string{"arr", "filesystem", "plex"},
}

// newDiscStack starts the disc scenario and a configured, scanned dupearr. Every test ends with
// every disc either complete or moved as a whole.
func newDiscStack(t *testing.T, o discStackOptions) *stack {
	t.Helper()
	s := newStack(t, stackOptions{scenario: discScenario(o.customScanner), settings: o.settings, recycleBin: o.recycleBin})
	t.Cleanup(func() { s.env.AssertDiscsIntact(t) })
	return s
}

// ---------------------------------------------------------------------------
// Ground truth
// ---------------------------------------------------------------------------

// movieDiscSet returns the fakemedia fixtures of the (non-extras) disc of a movie: one disc, or the
// discs of a multi-disc set in disc order.
func movieDiscSet(t testing.TB, env *fakemedia.Env, title string) []fakemedia.DiscFixture {
	t.Helper()
	var set []fakemedia.DiscFixture
	for _, f := range env.DiscFixtures() {
		if f.Title == title && !f.Show && !f.Extras {
			set = append(set, f)
		}
	}
	if len(set) == 0 {
		t.Fatalf("no disc fixture for %q", title)
	}
	sort.SliceStable(set, func(i, j int) bool { return set[i].SetNumber < set[j].SetNumber })
	return set
}

// discTruth is what Dupearr must report for a movie's disc (set).
type discTruth struct {
	kind        string
	roots       []string // as the servers see them, disc order
	localRoots  []string
	owned       []string // local owned entries (sorted)
	folder      string   // the movie folder as the servers see it
	files       int
	total       int64
	feature     int64
	durationMs  int64
	readable    bool
	mainFeature string
	clips       int // Blu-ray: clips in BDMV/STREAM (all discs)
	height      int
}

func truthOf(set []fakemedia.DiscFixture) discTruth {
	tr := discTruth{kind: set[0].Kind, readable: true, mainFeature: set[0].MainFeature, folder: set[0].Folder, height: set[0].Video.Height}
	for _, f := range set {
		tr.roots = append(tr.roots, f.Root)
		tr.localRoots = append(tr.localRoots, f.LocalRoot)
		tr.owned = append(tr.owned, f.OwnedEntries...)
		tr.files += f.Files
		tr.total += f.TotalSize
		tr.feature += f.FeatureSize
		tr.durationMs += f.DurationMs
		tr.readable = tr.readable && f.Readable
		tr.clips += f.Clips
	}
	sort.Strings(tr.owned)
	return tr
}

// wantDiscKey is the key of a disc version found on disk: "disc:<serverID>:<hex SHA-1 of the
// normalized local root>" (the contract; computed here independently of internal/disc).
func wantDiscKey(serverID int64, localRoot string) string {
	sum := sha1.Sum([]byte(path.Clean(filepath.ToSlash(localRoot))))
	return fmt.Sprintf("disc:%d:%s", serverID, hex.EncodeToString(sum[:]))
}

// wantResolution maps a main-feature height to Dupearr's resolution tier.
func wantResolution(height int) string {
	switch {
	case height >= 2160:
		return models.Res2160
	case height >= 1080:
		return models.Res1080
	case height >= 720:
		return models.Res720
	case height > 0:
		return models.Res480
	}
	return ""
}

// ---------------------------------------------------------------------------
// Group helpers
// ---------------------------------------------------------------------------

// discFiles returns the group's disc versions.
func discFiles(g groupDetail) []models.GroupFile {
	var out []models.GroupFile
	for _, f := range g.Files {
		if f.Version.Disc != nil {
			out = append(out, f)
		}
	}
	return out
}

// regularFiles returns the group's versions that are not discs.
func regularFiles(g groupDetail) []models.GroupFile {
	var out []models.GroupFile
	for _, f := range g.Files {
		if f.Version.Disc == nil {
			out = append(out, f)
		}
	}
	return out
}

// discOf returns the one disc version of a group.
func discOf(t testing.TB, g groupDetail) models.GroupFile {
	t.Helper()
	ds := discFiles(g)
	if len(ds) != 1 {
		t.Fatalf("%s: %d disc versions, want 1 (%d versions)", groupLabel(g.DuplicateGroup), len(ds), len(g.Files))
	}
	return ds[0]
}

// regularContaining returns the group's regular version whose first part path contains substr.
func regularContaining(t testing.TB, g groupDetail, substr string) models.GroupFile {
	t.Helper()
	for _, f := range regularFiles(g) {
		if len(f.Version.Parts) > 0 && strings.Contains(f.Version.Parts[0].Path, substr) {
			return f
		}
	}
	t.Fatalf("%s has no regular version with a file containing %q", groupLabel(g.DuplicateGroup), substr)
	return models.GroupFile{}
}

// summaryFile is one entry of DuplicateGroupSummary.files with its raw "disc" member.
type summaryFile struct {
	ID       int64           `json:"id"`
	Decision string          `json:"decision"`
	Size     int64           `json:"size"`
	Disc     json.RawMessage `json:"disc"`
}

type rawSummary struct {
	ID        int64         `json:"id"`
	Title     string        `json:"title"`
	ShowTitle string        `json:"showTitle"`
	Season    int           `json:"season"`
	Episode   int           `json:"episode"`
	Status    string        `json:"status"`
	FileCount int           `json:"fileCount"`
	Files     []summaryFile `json:"files"`
}

// summaryDisc is DuplicateGroupSummary.files[].disc.
type summaryDisc struct {
	Type      string `json:"type"`
	FileCount int    `json:"fileCount"`
	Discs     int    `json:"discs"`
}

// rawSummaries lists the groups as the duplicate list endpoint returns them, by label.
func rawSummaries(t testing.TB, d *dupearr) map[string]rawSummary {
	t.Helper()
	var p paged[rawSummary]
	d.expect(http.MethodGet, "/api/v1/duplicate?pageSize=1000&sortKey=title&sortDirection=ascending", nil, http.StatusOK, &p)
	out := map[string]rawSummary{}
	for _, g := range p.Records {
		out[groupSummary{Title: g.Title, ShowTitle: g.ShowTitle, Season: g.Season, Episode: g.Episode}.label()] = g
	}
	return out
}

// putSettingsSettled saves settings like putSettings, then waits until the background
// re-evaluation of the open groups that saving starts has finished: every open (or ignored) group
// was stored again after the save, and nothing changed for a moment. An approval sent while that
// re-evaluation still runs can be undone by it: a re-evaluation that read a group just before the
// approval stores the pre-approval status, and the executor then cancels the approved removals
// ("the group is no longer queued"). That race is not specific to discs; it is reported separately.
func putSettingsSettled(t testing.TB, d *dupearr, patch map[string]any) models.Settings {
	t.Helper()
	since := time.Now()
	st := d.putSettings(patch)
	const quiet = 250 * time.Millisecond
	deadline := time.Now().Add(commandTimeout)
	var last time.Time
	stableSince := time.Time{}
	for {
		newest, stale := time.Time{}, ""
		for _, g := range d.groups() {
			if g.Status == string(models.GroupResolved) {
				continue
			}
			u := d.group(g.ID).UpdatedAt
			if !u.After(since) {
				stale = fmt.Sprintf("%s last stored %s (settings saved %s)", g.label(), u.Format(time.RFC3339Nano), since.Format(time.RFC3339Nano))
			}
			if u.After(newest) {
				newest = u
			}
		}
		switch {
		case stale != "":
			stableSince = time.Time{}
		case !newest.Equal(last) || stableSince.IsZero():
			last, stableSince = newest, time.Now()
		case time.Since(stableSince) >= quiet:
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("the re-evaluation after saving settings did not settle: %s", stale)
		}
		time.Sleep(pollInterval)
	}
}

// discRootsOfAction returns the paths an action names (a disc action: its disc roots), for messages.
func discRootsOfAction(a models.Action) string { return strconv.Quote(strings.Join(a.Paths, ", ")) }

// overridePath is the override endpoint of a group file.
func overridePath(groupID, fileID int64) string {
	return fmt.Sprintf("/api/v1/duplicate/%d/file/%d/override", groupID, fileID)
}

// requireOverrideRefused asks to remove a version and fails unless the API refuses it with 400
// and leaves the group exactly as it was.
func requireOverrideRefused(t testing.TB, d *dupearr, label string, f models.GroupFile, why ...string) {
	t.Helper()
	before := d.groupNamed(label)
	r := d.request(http.MethodPut, overridePath(before.ID, f.ID), map[string]any{"decision": "remove"})
	if r.Status != http.StatusBadRequest {
		t.Fatalf("%s: override remove of %s: %s, want 400", label, f.Version.Key, r)
	}
	msg := r.message()
	t.Logf("%s: removing %s refused: %s", label, f.Version.Key, msg)
	for _, w := range why {
		if !strings.Contains(msg, w) {
			t.Errorf("%s: override refusal %q does not mention %q", label, msg, w)
		}
	}
	after := d.group(before.ID)
	if after.Signature != before.Signature || after.Status != before.Status {
		t.Errorf("%s: a refused override changed the group (%s → %s)", label, before.Status, after.Status)
	}
	for _, af := range after.Files {
		if af.ID == f.ID && (af.Override != "" || af.Decision != models.DecisionKeep) {
			t.Errorf("%s: after the refused override %s is %s (override %q)", label, af.Version.Key, af.Decision, af.Override)
		}
	}
}

// setOverride sets a keep/remove override and fails unless the API accepts it.
func setOverride(t testing.TB, d *dupearr, groupID, fileID int64, decision string) groupDetail {
	t.Helper()
	var g groupDetail
	d.expect(http.MethodPut, overridePath(groupID, fileID), map[string]any{"decision": decision}, http.StatusOK, &g)
	return g
}

// ---------------------------------------------------------------------------
// Filesystem snapshots
// ---------------------------------------------------------------------------

// smallFile is the size up to which snapshots hash a file's content (disc navigation files,
// NFO, artwork; the sparse stream files are compared by identity, size and modification time).
const smallFile = 1 << 20

// entryState is one entry of a directory tree.
type entryState struct {
	info fs.FileInfo
	sum  string // SHA-256 of a regular file up to smallFile bytes
}

// snapshot records every entry below root (root itself excluded), keyed by its slash-separated
// path relative to root, without following symbolic links.
func snapshot(t testing.TB, root string) map[string]entryState {
	t.Helper()
	out := map[string]entryState{}
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		st := entryState{info: fi}
		if fi.Mode().IsRegular() && fi.Size() <= smallFile {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			st.sum = hex.EncodeToString(h.Sum(nil))
		}
		out[filepath.ToSlash(rel)] = st
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// filterSnapshot returns the entries whose path is (or lies below) one of the prefixes (keep=true),
// or the others (keep=false).
func filterSnapshot(s map[string]entryState, prefixes []string, keep bool) map[string]entryState {
	out := map[string]entryState{}
	for k, v := range s {
		in := slices.ContainsFunc(prefixes, func(p string) bool { return k == p || strings.HasPrefix(k, p+"/") })
		if in == keep {
			out[k] = v
		}
	}
	return out
}

// requireSameTree fails unless got holds exactly the entries of want: the same names, kinds and
// permissions, and for files the same inode (renamed, never copied), size, modification time and
// (small files) content.
func requireSameTree(t testing.TB, what string, want, got map[string]entryState) {
	t.Helper()
	var problems []string
	for k, w := range want {
		g, ok := got[k]
		switch {
		case !ok:
			problems = append(problems, "missing "+k)
		case w.info.IsDir() != g.info.IsDir() || w.info.Mode() != g.info.Mode():
			problems = append(problems, fmt.Sprintf("%s: mode %v, want %v", k, g.info.Mode(), w.info.Mode()))
		case w.info.IsDir():
		case !os.SameFile(w.info, g.info):
			problems = append(problems, k+": not the same file (copied or replaced)")
		case w.info.Size() != g.info.Size() || !w.info.ModTime().Equal(g.info.ModTime()) || w.sum != g.sum:
			problems = append(problems, fmt.Sprintf("%s: size %d mtime %s, want %d %s (content equal %t)",
				k, g.info.Size(), g.info.ModTime().Format(time.RFC3339Nano), w.info.Size(), w.info.ModTime().Format(time.RFC3339Nano), w.sum == g.sum))
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			problems = append(problems, "unexpected "+k)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		if len(problems) > 15 {
			problems = append(problems[:15], fmt.Sprintf("… %d more", len(problems)-15))
		}
		t.Fatalf("%s: %d entries, want %d:\n  %s", what, len(got), len(want), strings.Join(problems, "\n  "))
	}
}

// relNames returns the owned entries relative to the movie folder (local paths).
func relNames(t testing.TB, folder string, owned []string) []string {
	t.Helper()
	out := make([]string, 0, len(owned))
	for _, o := range owned {
		rel, err := filepath.Rel(folder, o)
		if err != nil || !filepath.IsLocal(rel) {
			t.Fatalf("owned entry %s is not inside %s", o, folder)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

// writeSidecars puts the files a real movie folder holds next to a disc (NFO, artwork, an external
// subtitle, a Kodi extrathumbs folder) into a movie folder; a disc removal must leave every one of
// them alone.
func writeSidecars(t testing.TB, folder string) {
	t.Helper()
	base := filepath.Base(folder)
	for name, content := range map[string]string{
		"movie.nfo":              "<movie><title>" + base + "</title></movie>\n",
		"poster.jpg":             "\xff\xd8\xff\xe0 poster of " + base,
		"fanart.jpg":             "\xff\xd8\xff\xe0 fanart of " + base,
		base + ".en.srt":         "1\n00:00:01,000 --> 00:00:02,000\nHello\n",
		"extrathumbs/thumb1.jpg": "\xff\xd8\xff\xe0 thumb of " + base,
	} {
		p := filepath.Join(folder, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Detection
// ---------------------------------------------------------------------------

// TestDiscDetection: every disc layout next to a movie is ONE version of that movie — a UHD BDMV
// of 937 files (300 clips), a MakeMKV Blu-ray, a DVD VIDEO_TS, an ISO, a "Disc 1"/"Disc 2" set (the
// "Bonus Disc" beside it is not a version), a disc whose clip Radarr tracks and a damaged disc —
// with the disc's type, root, owned entries, file count and sizes from the files on disk, the key
// "disc:<server>:<sha1 of the local root>" and one part per disc root; the duplicate list shows the
// disc summary. A disc-only folder (no Plex item) and a standalone .m2ts are not discs. A rescan
// keeps the same versions. Nothing is changed anywhere.
func TestDiscDetection(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{})
	d := s.d
	summaries := rawSummaries(t, d)

	titles := []string{titleBladeRunner, titleDarkKnight, titleCasablanca, titleHeat, titleLotR, titleGladiator, titleTenet, titleOppenheimer, titleTopGun}
	keys := map[string]string{}
	for _, title := range titles {
		t.Run(title, func(t *testing.T) {
			tr := truthOf(movieDiscSet(t, s.env, title))
			g := d.groupNamed(title)
			if !g.HasFlag(models.FlagFullDisc) {
				t.Errorf("flags %v, want %s", g.Flags, models.FlagFullDisc)
			}
			f := discOf(t, g)
			v, di := f.Version, f.Version.Disc
			wantDiscs := len(tr.roots)
			switch {
			case di.Type != tr.kind:
				t.Errorf("type %q, want %q", di.Type, tr.kind)
			case di.Origin != models.DiscOriginFilesystem:
				t.Errorf("origin %q, want filesystem", di.Origin)
			case di.Root != tr.roots[0] || di.LocalRoot != tr.localRoots[0]:
				t.Errorf("root %q (local %q), want %q (%q)", di.Root, di.LocalRoot, tr.roots[0], tr.localRoots[0])
			case di.Discs != wantDiscs:
				t.Errorf("discs %d, want %d", di.Discs, wantDiscs)
			case di.FileCount != tr.files:
				t.Errorf("fileCount %d, want %d", di.FileCount, tr.files)
			case di.TotalBytes != tr.total:
				t.Errorf("totalBytes %d, want %d", di.TotalBytes, tr.total)
			case di.Readable != tr.readable:
				t.Errorf("readable %t, want %t (problem %q)", di.Readable, tr.readable, di.Problem)
			}
			if wantDiscs > 1 && (!slices.Equal(di.Roots, tr.roots) || !slices.Equal(di.LocalRoots, tr.localRoots)) {
				t.Errorf("roots %q / %q, want %q / %q", di.Roots, di.LocalRoots, tr.roots, tr.localRoots)
			}
			if owned := slices.Sorted(slices.Values(di.OwnedEntries)); !slices.Equal(owned, tr.owned) {
				t.Errorf("owned entries %q, want %q", owned, tr.owned)
			}
			if tr.readable {
				if di.MainFeature != tr.mainFeature || di.FeatureBytes != tr.feature || v.DurationMs != tr.durationMs {
					t.Errorf("main feature %q (%d bytes, %d ms), want %q (%d bytes, %d ms)",
						di.MainFeature, di.FeatureBytes, v.DurationMs, tr.mainFeature, tr.feature, tr.durationMs)
				}
				if got, want := v.Resolution, wantResolution(tr.height); tr.kind != models.DiscISO && got != want {
					t.Errorf("resolution %q, want %q", got, want)
				}
			}
			if di.Readable && di.Problem != "" {
				t.Errorf("a readable disc has a problem: %q", di.Problem)
			}
			if !tr.readable && di.MainFeature != "" {
				t.Errorf("unreadable disc names a main feature %q", di.MainFeature)
			}
			if want := wantDiscKey(s.serverID, tr.localRoots[0]); v.Key != want {
				t.Errorf("key %q, want %q", v.Key, want)
			}
			if v.Source != models.SourceDisc || v.Container != "disc" || v.MediaID != 0 {
				t.Errorf("source %q container %q mediaId %d, want disc/disc/0", v.Source, v.Container, v.MediaID)
			}
			// One part per disc root, never one per file.
			if got := partPaths(v); !slices.Equal(got, tr.roots) {
				t.Errorf("parts %q, want the disc roots %q", got, tr.roots)
			}
			var partBytes int64
			for _, p := range v.Parts {
				partBytes += p.Size
			}
			if partBytes != tr.total || v.TotalSize() != tr.total {
				t.Errorf("parts hold %d bytes (TotalSize %d), want %d", partBytes, v.TotalSize(), tr.total)
			}
			// The duplicate list: the disc summary for the disc, nothing for the other versions.
			sum, ok := summaries[title]
			if !ok || sum.FileCount != len(g.Files) {
				t.Fatalf("summary %+v (detail has %d versions)", sum, len(g.Files))
			}
			for _, sf := range sum.Files {
				isDisc := sf.ID == f.ID
				raw := strings.TrimSpace(string(sf.Disc))
				switch {
				case !isDisc && raw != "" && raw != "null":
					t.Errorf("summary: regular file %d carries disc %s", sf.ID, raw)
				case isDisc:
					var sd summaryDisc
					if err := json.Unmarshal(sf.Disc, &sd); err != nil || sd != (summaryDisc{Type: tr.kind, FileCount: tr.files, Discs: wantDiscs}) {
						t.Errorf("summary disc %s (%v), want %+v", raw, err, summaryDisc{Type: tr.kind, FileCount: tr.files, Discs: wantDiscs})
					}
					if sf.Size != tr.total {
						t.Errorf("summary size %d, want %d", sf.Size, tr.total)
					}
				}
			}
			keys[title] = v.Key
		})
	}

	t.Run("hundreds of files are one version", func(t *testing.T) {
		tr := truthOf(movieDiscSet(t, s.env, titleBladeRunner))
		g := d.groupNamed(titleBladeRunner)
		if tr.files < 900 || tr.clips != 300 || len(g.Files) != 2 {
			t.Fatalf("Blade Runner: %d files, %d clips in the fixture; %d versions in the group, want 2", tr.files, tr.clips, len(g.Files))
		}
	})
	t.Run("multi-disc set without its bonus disc", func(t *testing.T) {
		g := d.groupNamed(titleLotR)
		f := discOf(t, g)
		var bonus string
		for _, fx := range s.env.DiscFixtures() {
			if fx.Title == titleLotR && fx.Extras {
				bonus = fx.LocalRoot
			}
		}
		if bonus == "" || len(g.Files) != 2 {
			t.Fatalf("bonus fixture %q, %d versions", bonus, len(g.Files))
		}
		for _, p := range append(slices.Clone(f.Version.Disc.OwnedEntries), f.Version.Disc.LocalRoots...) {
			if p == bonus || strings.HasPrefix(p, bonus+string(filepath.Separator)) {
				t.Errorf("the bonus disc is part of the set: %s", p)
			}
		}
	})
	t.Run("damaged disc", func(t *testing.T) {
		f := discOf(t, d.groupNamed(titleTenet))
		if f.Version.Disc.Removable || f.Version.Disc.Problem == "" {
			t.Errorf("Tenet disc removable %t problem %q, want not removable with a problem", f.Version.Disc.Removable, f.Version.Disc.Problem)
		}
	})
	t.Run("tracked clip", func(t *testing.T) {
		g := d.groupNamed(titleGladiator)
		f := discOf(t, g)
		tracked := s.env.ArrMovieFilePath(fakemedia.InstanceRadarr, gladiatorTmdbID)
		if !g.HasFlag(models.FlagDiscTracked) || f.Version.Disc.TrackedClip != tracked || f.Version.Arr == nil ||
			f.Version.Arr.InstanceName != "Radarr" || !strings.HasSuffix(tracked, "/"+fakemedia.DiscMainClip) {
			t.Errorf("Gladiator: flags %v, trackedClip %q (Radarr tracks %q), arr %+v", g.Flags, f.Version.Disc.TrackedClip, tracked, f.Version.Arr)
		}
		for _, r := range regularFiles(g) {
			if r.Version.Arr != nil {
				t.Errorf("the untracked MKV is attributed to %s", r.Version.Arr.InstanceName)
			}
		}
	})
	t.Run("not discs", func(t *testing.T) {
		if gs := d.groupsBy(titleAlien); len(gs) != 0 {
			t.Errorf("a disc-only folder (no Plex item) formed a group: %+v", gs)
		}
		inc := d.groupNamed(titleInception)
		if len(discFiles(inc)) != 0 || inc.HasFlag(models.FlagFullDisc) || inc.Status != models.GroupPending {
			t.Errorf("Inception: %d discs, flags %v, %s — a standalone .m2ts is an ordinary version", len(discFiles(inc)), inc.Flags, inc.Status)
		}
		m2ts := regularContaining(t, inc, ".m2ts")
		if m2ts.Version.Container != "m2ts" || m2ts.Decision != models.DecisionKeep {
			t.Errorf("standalone .m2ts: container %q decision %s, want m2ts/keep", m2ts.Version.Container, m2ts.Decision)
		}
		arv := d.groupNamed(titleArrival)
		if len(discFiles(arv)) != 0 || arv.HasFlag(models.FlagFullDisc) {
			t.Errorf("Arrival: %d discs, flags %v", len(discFiles(arv)), arv.Flags)
		}
	})
	t.Run("TV season disc", func(t *testing.T) {
		g := d.groupNamed(labelPlanetEarth)
		if len(g.Files) != 2 || len(discFiles(g)) != 0 || !g.HasFlag(models.FlagFullDisc) {
			t.Errorf("%s: %d versions, %d discs, flags %v; want 2 regular versions, flagged %s", labelPlanetEarth, len(g.Files), len(discFiles(g)), g.Flags, models.FlagFullDisc)
		}
	})

	t.Run("API defaults", func(t *testing.T) {
		var st models.Settings
		d.expect(http.MethodGet, "/api/v1/config/settings", nil, http.StatusOK, &st)
		if !st.DetectDiscs || st.AllowDiscRemoval || !st.KeepPlayableCopy {
			t.Errorf("settings detectDiscs %t allowDiscRemoval %t keepPlayableCopy %t, want true/false/true", st.DetectDiscs, st.AllowDiscRemoval, st.KeepPlayableCopy)
		}
		type option struct {
			Value string `json:"value"`
			Label string `json:"label"`
		}
		var schema struct {
			Criteria []struct {
				Type         string   `json:"type"`
				Options      []option `json:"options"`
				DefaultOrder []string `json:"defaultOrder"`
			} `json:"criteria"`
		}
		d.expect(http.MethodGet, "/api/v1/profile/schema", nil, http.StatusOK, &schema)
		found := 0
		for _, c := range schema.Criteria {
			switch c.Type {
			case string(models.CritSource):
				found++
				if !slices.Contains(c.Options, option{Value: models.SourceDisc, Label: "Full disc (BDMV/VIDEO_TS/ISO)"}) {
					t.Errorf("source options %+v lack the full disc", c.Options)
				}
				if len(c.DefaultOrder) < 3 || !slices.Equal(c.DefaultOrder[:3], []string{models.SourceRemux, models.SourceDisc, models.SourceBluray}) {
					t.Errorf("source default order %q, want remux, disc, bluray, …", c.DefaultOrder)
				}
			case string(models.CritContainer):
				found++
				if want := []string{"mkv", "mp4", "m4v", "m2ts", "other", "avi", "ts"}; !slices.Equal(c.DefaultOrder, want) {
					t.Errorf("container default order %q, want %q", c.DefaultOrder, want)
				}
			}
		}
		if found != 2 {
			t.Errorf("schema lacks the source or container criterion: %+v", schema.Criteria)
		}
	})

	// A rescan finds the same discs under the same keys and changes nothing.
	s.env.ResetRequests()
	s.scan()
	for title, key := range keys {
		g := d.groupNamed(title)
		if f := discOf(t, g); f.Version.Key != key {
			t.Errorf("%s: disc key %s after a rescan, was %s", title, f.Version.Key, key)
		}
	}
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("detection changed the fake world:%s", describeRequests(m))
	}
	if acts := d.actions(); len(acts) != 0 {
		t.Fatalf("detection created actions: %+v", acts)
	}
}

// ---------------------------------------------------------------------------
// Ranking and the playable copy
// ---------------------------------------------------------------------------

// TestDiscRanking checks the default profile ("Keep Highest Quality": resolution, dynamic range,
// then source remux, disc, bluray, webdl, …) and settings.keepPlayableCopy:
//   - Oppenheimer: remux and UHD disc alike but for the source → the remux ranks first;
//   - Blade Runner 2049: the remux carries Dolby Vision → it ranks first by dynamic range;
//   - Top Gun: Maverick: the UHD disc beats the 2160p encode by source; the encode is kept as Plex's
//     playable copy (a disc is never the only kept copy) and the 1080p WEB-DL goes;
//   - The Dark Knight: the 1080p disc beats the WEB-DL, which is kept as the playable copy;
//   - with keepPlayableCopy off, the regular copies behind a disc are ranked as usual.
func TestDiscRanking(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{})
	d := s.d

	rank := func(t *testing.T, label string, want []string) groupDetail {
		t.Helper()
		g := d.groupNamed(label)
		byRank := slices.Clone(g.Files)
		sort.Slice(byRank, func(i, j int) bool { return byRank[i].Rank < byRank[j].Rank })
		var got []string
		for _, f := range byRank {
			if f.Version.Disc != nil {
				got = append(got, "disc")
			} else {
				got = append(got, path.Base(f.Version.Parts[0].Path))
			}
		}
		ok := len(got) == len(want)
		for i := 0; ok && i < len(want); i++ {
			ok = strings.Contains(got[i], want[i])
		}
		if !ok {
			t.Fatalf("%s ranking %q, want %q", label, got, want)
		}
		return g
	}
	playable := "playable copy"

	t.Run("remux before disc", func(t *testing.T) {
		g := rank(t, titleOppenheimer, []string{"[Remux-2160p]", "disc"})
		dv := discOf(t, g)
		if dv.DecidingCriterion != string(models.CritSource) {
			t.Errorf("Oppenheimer: deciding criterion %q, want source (reasons %q)", dv.DecidingCriterion, dv.Reasons)
		}
		if dv.Values[string(models.CritSource)] == "" || dv.Values[string(models.CritContainer)] == "" {
			t.Errorf("disc comparison values %v", dv.Values)
		}
	})
	t.Run("dynamic range before source", func(t *testing.T) {
		g := rank(t, titleBladeRunner, []string{"[Remux-2160p][DV HDR10]", "disc"})
		if dv := discOf(t, g); dv.DecidingCriterion != string(models.CritDynamicRange) {
			t.Errorf("Blade Runner: deciding criterion %q, want dynamic_range", dv.DecidingCriterion)
		}
	})
	t.Run("disc before encode, encode kept playable", func(t *testing.T) {
		g := rank(t, titleTopGun, []string{"disc", "[Bluray-2160p]", "WEB-DL"})
		enc, web := regularContaining(t, g, "[Bluray-2160p]"), regularContaining(t, g, "WEB-DL")
		if enc.Decision != models.DecisionKeep || !enc.Protected || !strings.Contains(enc.ProtectedReason, playable) {
			t.Errorf("Top Gun encode: %s protected %t (%q), want kept as the playable copy", enc.Decision, enc.Protected, enc.ProtectedReason)
		}
		if web.Decision != models.DecisionRemove || web.Protected {
			t.Errorf("Top Gun WEB-DL: %s protected %t, want remove", web.Decision, web.Protected)
		}
		if g.Status != models.GroupPending || !g.HasFlag(models.FlagFullDisc) {
			t.Errorf("Top Gun: %s %v, want pending with %s (manual approval only)", g.Status, g.Flags, models.FlagFullDisc)
		}
	})
	t.Run("disc before WEB-DL, WEB-DL kept playable", func(t *testing.T) {
		g := rank(t, titleDarkKnight, []string{"disc", "WEBDL-1080p"})
		web := regularFiles(g)[0]
		if web.Decision != models.DecisionKeep || !strings.Contains(web.ProtectedReason, playable) {
			t.Errorf("Dark Knight WEB-DL: %s (%q), want kept as the playable copy", web.Decision, web.ProtectedReason)
		}
	})

	t.Run("keepPlayableCopy off", func(t *testing.T) {
		putSettingsSettled(t, d, map[string]any{"keepPlayableCopy": false})
		s.scan()
		tg := d.groupNamed(titleTopGun)
		for _, f := range regularFiles(tg) {
			if f.Decision != models.DecisionRemove || strings.Contains(f.ProtectedReason, playable) {
				t.Errorf("Top Gun without the playable-copy rule: %s is %s (%q), want remove",
					path.Base(f.Version.Parts[0].Path), f.Decision, f.ProtectedReason)
			}
		}
		if dv := discOf(t, tg); dv.Decision != models.DecisionKeep || dv.Rank != 1 {
			t.Errorf("Top Gun disc: %s rank %d", dv.Decision, dv.Rank)
		}
		dk := d.groupNamed(titleDarkKnight)
		if web := regularFiles(dk)[0]; web.Decision != models.DecisionRemove {
			t.Errorf("Dark Knight WEB-DL without the playable-copy rule: %s (%q)", web.Decision, web.ProtectedReason)
		}
		if dk.Status != models.GroupPending || !dk.HasFlag(models.FlagFullDisc) {
			t.Errorf("Dark Knight: %s %v", dk.Status, dk.Flags)
		}
		// Back on: the playable copy is kept again.
		putSettingsSettled(t, d, map[string]any{"keepPlayableCopy": true})
		s.scan()
		if web := regularFiles(d.groupNamed(titleDarkKnight))[0]; web.Decision != models.DecisionKeep {
			t.Errorf("Dark Knight WEB-DL with the rule back on: %s", web.Decision)
		}
	})
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("ranking changed the fake world:%s", describeRequests(m))
	}
}

// ---------------------------------------------------------------------------
// Default protection
// ---------------------------------------------------------------------------

// TestDiscProtectedByDefault: with the default settings (disc removal off) every disc is kept and
// protected with a reason naming the setting; groups whose only candidate is a disc are protected
// ("Nothing to remove"), unreadable discs and images put their group in review, and a group that
// removes a regular copy next to a disc stays pending (manual approval only). Approving a protected
// disc group is refused (400), and so is every attempt to mark a disc — or the copy kept playable
// for Plex — for removal through the override API. Nothing reaches the fake servers.
func TestDiscProtectedByDefault(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{})
	d := s.d
	s.env.ResetRequests()

	want := map[string]models.GroupStatus{
		titleBladeRunner: models.GroupProtected, titleDarkKnight: models.GroupProtected, titleCasablanca: models.GroupProtected,
		titleLotR: models.GroupProtected, titleGladiator: models.GroupProtected, titleOppenheimer: models.GroupProtected,
		titleHeat: models.GroupReview, titleTenet: models.GroupReview, titleTopGun: models.GroupPending,
	}
	for label, status := range want {
		g := d.groupNamed(label)
		if g.Status != status || !g.HasFlag(models.FlagFullDisc) {
			t.Errorf("%s: %s %v (%s), want %s with %s", label, g.Status, g.Flags, g.StatusReason, status, models.FlagFullDisc)
		}
		if g.StatusReason == "" && status != models.GroupPending {
			t.Errorf("%s: no status reason", label)
		}
		f := discOf(t, g)
		if f.Decision != models.DecisionKeep || !f.Protected || f.ProtectedReason == "" {
			t.Errorf("%s: disc %s protected %t (%q), want a protected keeper", label, f.Decision, f.Protected, f.ProtectedReason)
		}
		if f.Version.Disc.Removable && !strings.Contains(f.ProtectedReason, "disc removal is off") {
			t.Errorf("%s: protection reason %q does not name the setting", label, f.ProtectedReason)
		}
		for _, r := range g.Files {
			if r.Decision == models.DecisionRemove && r.Version.Disc != nil {
				t.Errorf("%s: disc %s is marked for removal", label, r.Version.Key)
			}
		}
		if status == models.GroupProtected {
			if r := d.approve(g.ID, g.Signature); r.Status != http.StatusBadRequest || !strings.Contains(r.message(), "Nothing to remove") {
				t.Errorf("approve %s: %s, want 400 Nothing to remove", label, r)
			}
		}
		requireOverrideRefused(t, d, label, f, "protected")
	}
	if g := d.groupNamed(titleHeat); !g.HasFlag(models.FlagUnanalyzed) {
		t.Errorf("Heat (ISO): flags %v, want %s", g.Flags, models.FlagUnanalyzed)
	}
	if g := d.groupNamed(titleTenet); !g.HasFlag(models.FlagDiscUnreadable) {
		t.Errorf("Tenet (damaged disc): flags %v, want %s", g.Flags, models.FlagDiscUnreadable)
	}
	// Top Gun removes only its WEB-DL; the copy kept playable for Plex cannot be overridden away.
	tg := d.groupNamed(titleTopGun)
	if _, rm := filesOf(tg); len(rm) != 1 || !strings.Contains(rm[0].Version.Parts[0].Path, "WEB-DL") {
		t.Errorf("Top Gun removals: %+v, want only the WEB-DL", rm)
	}
	requireOverrideRefused(t, d, titleTopGun, regularContaining(t, tg, "[Bluray-2160p]"))
	requireOverrideRefused(t, d, titleDarkKnight, regularFiles(d.groupNamed(titleDarkKnight))[0])

	// Bulk approval of everything: only Top Gun (a regular removal) could be queued — dry run.
	var ids []int64
	for label := range want {
		ids = append(ids, d.groupID(label))
	}
	res := d.bulk("approve", ids...)
	if len(res.Succeeded) != 1 || res.Succeeded[0] != tg.ID {
		t.Errorf("bulk approve: %+v, want only Top Gun", res)
	}
	d.waitIdle()
	for _, a := range d.actions() {
		if a.GroupID != tg.ID || a.Status != models.ActionDryRun || strings.HasPrefix(a.VersionKey, models.DiscKeyPrefix) {
			t.Errorf("action %+v, want only Top Gun's WEB-DL (dry run)", a)
		}
	}
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("disc protection changed the fake world:%s", describeRequests(m))
	}
}

// TestDiscRemovalNeedsRecycleBin: allowDiscRemoval cannot be saved without a recycle bin, the bin
// cannot be cleared while it is on, and discs stay protected. With a bin and the setting on, a disc
// removal still needs the filesystem method (the approval is refused while it is off), and a disc
// removal queued before the setting and the bin were switched off is never carried out.
func TestDiscRemovalNeedsRecycleBin(t *testing.T) {
	t.Parallel()
	s := newDiscStack(t, discStackOptions{settings: map[string]any{"dryRun": false}})
	d := s.d
	s.env.ResetRequests()

	r := d.request(http.MethodPut, "/api/v1/config/settings", map[string]any{"allowDiscRemoval": true})
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "allowDiscRemoval") || !strings.Contains(string(r.Body), "recycle bin") {
		t.Fatalf("allowDiscRemoval without a recycle bin: %s, want 400 naming the recycle bin", r)
	}
	var st models.Settings
	d.expect(http.MethodGet, "/api/v1/config/settings", nil, http.StatusOK, &st)
	if st.AllowDiscRemoval || st.RecycleBinPath != "" {
		t.Fatalf("a refused update was saved: %+v", st)
	}
	s.scan()
	if f := discOf(t, d.groupNamed(titleBladeRunner)); !f.Protected || f.Decision != models.DecisionKeep {
		t.Fatalf("Blade Runner disc after the refused update: %s protected %t", f.Decision, f.Protected)
	}

	// With a bin the setting is accepted; the bin cannot be cleared while it is on.
	putSettingsSettled(t, d, map[string]any{"allowDiscRemoval": true, "recycleBinPath": s.recycleBin})
	r = d.request(http.MethodPut, "/api/v1/config/settings", map[string]any{"recycleBinPath": ""})
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "allowDiscRemoval") {
		t.Fatalf("clearing the recycle bin while disc removal is on: %s, want 400", r)
	}

	// Without the filesystem method a disc removal is refused (with a warning when saving).
	r = d.request(http.MethodPut, "/api/v1/config/settings", map[string]any{"deletionMethods": []string{"arr", "plex"}})
	if r.Status != http.StatusAccepted || !strings.Contains(r.Header.Get("X-Dupearr-Warning"), "filesystem") {
		t.Fatalf("disc removal without the filesystem method: %s (warning %q), want 202 with a warning", r, r.Header.Get("X-Dupearr-Warning"))
	}
	s.scan()
	br := d.groupNamed(titleBladeRunner)
	if f := discOf(t, br); f.Decision != models.DecisionRemove || br.Status != models.GroupPending {
		t.Fatalf("Blade Runner with disc removal on: disc %s, group %s (%s)", f.Decision, br.Status, br.StatusReason)
	}
	if r := d.approve(br.ID, br.Signature); r.Status != http.StatusConflict || !strings.Contains(r.message(), "filesystem") {
		t.Fatalf("approve a disc removal without the filesystem method: %s, want 409", r)
	}
	if n := len(d.actionsOf(br.ID)); n != 0 {
		t.Fatalf("a refused approval queued %d removal(s)", n)
	}

	// A removal queued while allowed (deferred: the movie is playing) never runs once disc removal
	// and the recycle bin are switched off.
	putSettingsSettled(t, d, map[string]any{"deletionMethods": []string{"arr", "filesystem", "plex"}})
	s.scan()
	br = d.groupNamed(titleBladeRunner)
	s.env.SetPlaying(br.Files[0].Version.RatingKey)
	s.approveAndProcess(titleBladeRunner)
	acts := d.actionsOf(br.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionPending {
		t.Fatalf("while playing: actions %+v, want one pending removal", acts)
	}
	putSettingsSettled(t, d, map[string]any{"allowDiscRemoval": false, "recycleBinPath": ""})
	s.env.SetPlaying()
	d.runCommand(models.CmdProcessQueue, nil)
	d.waitIdle()
	for _, a := range d.actionsOf(br.ID) {
		if a.Status == models.ActionSucceeded || a.Status == models.ActionPending || a.Status == models.ActionRunning || a.RecyclePath != "" {
			t.Errorf("the queued disc removal after disc removal was switched off: %+v", a)
		}
		t.Logf("queued disc removal after disc removal was switched off: %s (%s)", a.Status, a.Message)
	}
	tr := truthOf(movieDiscSet(t, s.env, titleBladeRunner))
	for _, o := range tr.owned {
		if _, err := os.Lstat(o); err != nil {
			t.Errorf("%s: %v", o, err)
		}
	}
	if _, err := os.Stat(s.recycleBin); err == nil {
		t.Errorf("the recycle bin %s was created although nothing was removed", s.recycleBin)
	}
	for _, r := range s.env.Requests() {
		if r.Method == http.MethodDelete {
			t.Errorf("DELETE %s %s", r.Server, r.Path)
		}
	}
}
