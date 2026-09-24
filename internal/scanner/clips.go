package scanner

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Loose clip sets (docs/DECISIONS.md D9 "Loose clip sets").
//
// A flattened Blu-ray backup keeps its numbered STREAM clips loose in the movie folder
// ("/data/Movies/Elemental (2023)/00174.m2ts", "00175.m2ts" …, no BDMV/). Plex's default scanner
// lists EVERY such clip as a separate Media (version) of the movie, so the item shows up with a
// hundred "copies" — and removing all but one of them destroys the backup: a feature can span
// clips, the longest clip is not always the film, and 00102.m2ts is the same studio logo on five
// different discs. The old behaviour let a person approve such a group and 337 clips were deleted
// permanently through Plex.
//
// Therefore every Plex version of an item whose parts are all files of a loose clip set lying
// directly in one folder (disc.IsLooseSetFileName, outside any disc structure; at least one clip
// name) is merged into ONE disc version before grouping — built from the Plex part paths alone, so
// it also works when Dupearr cannot see the files:
//   - key "disc:<serverID>:<disc.ClipSetHash(server folder)>" (stable across scans; never the key
//     of a clip, so no per-clip override or approval carries over to it), origin "plex";
//   - parts: the union of the clips' parts; size: their sum (DiscInfo.TotalBytes);
//   - attributes (resolution, HDR/DV, codecs, audio, duration): the longest clip's — the
//     main-feature candidate (DiscInfo.MainClip, FeatureBytes);
//   - DiscInfo.PlexMediaIDs / ClipCount record what was merged.
//
// When the folder is visible locally (path mappings, settings.DetectDiscs) the set found on disk
// (disc.Detect: every clip-named file of the folder, also the ones Plex does not list, plus the
// loose .mpls/.clpi/.bdmv files) is what the version owns and what a whole-set removal would move;
// loose playlists, when readable, describe the version instead of the longest clip. Otherwise the
// set is not reachable and therefore never removable.
//
// Consequences: an item whose only versions are one clip set has ONE version — no duplicate group
// (the stored per-clip groups resolve on the next scan); an item with a clip set and an MKV is a
// normal two-version group in which the clip set is a disc version (protected unless
// settings.AllowDiscRemoval; never a Plex-playable copy for settings.KeepPlayableCopy).

// clipSet is the loose clip set of one folder of an item: the Plex versions merged into it.
type clipSet struct {
	dir      string    // the folder as the media server sees it (from the first part)
	kind     disc.Type // disc.BlurayClips or disc.DVDClips
	mixed    bool      // Blu-ray clips and DVD files in the same folder
	versions []int     // indexes into the item's versions, in listing order
}

// looseClipFolder returns the folder (server path, as listed) that every part of a Plex version
// lies directly in, when every part is a file of a loose clip set (disc.IsLooseSetFileName) whose
// disc root is that folder (not inside BDMV/, VIDEO_TS/ …) and at least one is a clip name. kind
// is the set's kind (Blu-ray clips win over DVD files; mixed reports both).
func looseClipFolder(v *models.MediaVersion) (dir string, kind disc.Type, mixed bool, ok bool) {
	if v.Disc != nil || v.OptimizedVersion || len(v.Parts) == 0 {
		return "", "", false, false
	}
	var norm string
	bd, dvd := false, false
	for _, pt := range v.Parts {
		p := strings.TrimSpace(pt.Path)
		if p == "" {
			return "", "", false, false
		}
		slash := strings.ReplaceAll(p, `\`, "/")
		name := path.Base(slash)
		if !disc.IsLooseSetFileName(name) {
			return "", "", false, false
		}
		root, _, found := disc.RootOf(p)
		parent := serverDir(p)
		if !found || pathKey(root) != pathKey(parent) {
			return "", "", false, false // inside a disc structure: a custom-scanner disc, not a loose set
		}
		if norm == "" {
			dir, norm = parent, pathKey(parent)
		} else if pathKey(parent) != norm {
			return "", "", false, false // spans folders: left to the per-file guard
		}
		switch k, _ := disc.LooseClipKind(p); k {
		case disc.BlurayClips:
			bd = true
		case disc.DVDClips:
			dvd = true
		}
	}
	switch {
	case bd:
		return dir, disc.BlurayClips, dvd, true
	case dvd:
		return dir, disc.DVDClips, false, true
	}
	return "", "", false, false // only metadata files (.mpls, .IFO …): no clip
}

// serverDir returns the folder of a server-side path, keeping its separators ("" when none).
func serverDir(p string) string {
	p = strings.TrimRight(p, `/\`)
	i := strings.LastIndexAny(p, `/\`)
	switch {
	case i < 0:
		return ""
	case i == 0:
		return p[:1]
	case i == 2 && p[1] == ':':
		return p[:3] // "D:\"
	}
	return p[:i]
}

// looseClipSets returns the loose clip sets of an item's Plex versions, in listing order.
func looseClipSets(it *models.MediaItem) []*clipSet {
	var sets []*clipSet
	byDir := map[string]*clipSet{}
	for i := range it.Versions {
		dir, kind, mixed, ok := looseClipFolder(&it.Versions[i])
		if !ok {
			continue
		}
		// pathKey: a Windows server's folder in two spellings ("D:\Movies\M", "d:\movies\m") is one
		// folder and one set — two sets of it would let one be removed as a "duplicate" of the
		// other, moving the kept one's files with it.
		n := pathKey(dir)
		cs := byDir[n]
		if cs == nil {
			cs = &clipSet{dir: dir, kind: kind}
			byDir[n] = cs
			sets = append(sets, cs)
		}
		if kind == disc.BlurayClips && cs.kind == disc.DVDClips || kind == disc.DVDClips && cs.kind == disc.BlurayClips {
			cs.mixed = true
		}
		if kind == disc.BlurayClips {
			cs.kind = disc.BlurayClips
		}
		cs.mixed = cs.mixed || mixed
		cs.versions = append(cs.versions, i)
	}
	return sets
}

// clipSetLocalRoot returns the local folder of a clip set, when a path mapping covers it.
func (p *pipeline) clipSetLocalRoot(it *models.MediaItem, cs *clipSet) (string, bool) {
	local, ok := p.cfg.mapper.ToLocal(models.PathSourceServer, it.ServerID, cs.dir)
	if !ok || !filepath.IsAbs(local) {
		return "", false
	}
	return filepath.Clean(local), true
}

// mergeClipSets replaces the Plex versions of an item that are loose clips of one folder by ONE
// disc version per folder (see the file comment). detect is settings.DetectDiscs: the local set
// is only read with it on (the folders were detected in attachDiscs' first step).
func (p *pipeline) mergeClipSets(it *models.MediaItem, detect bool) {
	sets := looseClipSets(it)
	if len(sets) == 0 {
		return
	}
	merged := map[int]bool{}
	first := map[int]*clipSet{}
	for _, cs := range sets {
		for _, i := range cs.versions {
			merged[i] = true
		}
		first[cs.versions[0]] = cs
	}
	out := make([]models.MediaVersion, 0, len(it.Versions)-len(merged)+len(sets))
	for i := range it.Versions {
		if cs, ok := first[i]; ok {
			out = append(out, p.clipSetVersion(it, cs, detect))
			continue
		}
		if !merged[i] {
			out = append(out, it.Versions[i])
		}
	}
	it.Versions = out
}

// clipSetVersion builds the one disc version of a loose clip set.
func (p *pipeline) clipSetVersion(it *models.MediaItem, cs *clipSet, detect bool) models.MediaVersion {
	members := make([]*models.MediaVersion, 0, len(cs.versions))
	for _, i := range cs.versions {
		members = append(members, &it.Versions[i])
	}
	main := mainClipVersion(members)

	// Parts: the union of the clips' parts (one file listed by two versions is one part).
	var parts []models.MediaPart
	seen := map[string]bool{}
	var ids []int64
	var added time.Time
	for _, m := range members {
		if m.MediaID > 0 && !slices.Contains(ids, m.MediaID) {
			ids = append(ids, m.MediaID)
		}
		if m.AddedAt.After(added) {
			added = m.AddedAt // the newest clip dates the set (a later date only delays a removal)
		}
		for _, pt := range m.Parts {
			if k := pathKey(pt.Path); !seen[k] {
				seen[k] = true
				parts = append(parts, pt)
			}
		}
	}
	sort.SliceStable(parts, func(a, b int) bool {
		na, nb := strings.ToLower(path.Base(strings.ReplaceAll(parts[a].Path, `\`, "/"))), strings.ToLower(path.Base(strings.ReplaceAll(parts[b].Path, `\`, "/")))
		if na != nb {
			return na < nb
		}
		return parts[a].Path < parts[b].Path
	})
	slices.Sort(ids)

	var total, freed, mainBytes int64
	hardlinked, clips := 0, 0
	for _, pt := range parts {
		total += pt.Size
		if pt.LinkCount > 1 {
			hardlinked++
		} else {
			freed += pt.Size
		}
		if _, ok := disc.LooseClipKind(pt.Path); ok {
			clips++
		}
	}
	for _, pt := range main.Parts {
		mainBytes += pt.Size
	}
	mainClip := ""
	if len(main.Parts) > 0 {
		mainClip = path.Base(strings.ReplaceAll(main.Parts[0].Path, `\`, "/"))
	}

	v := models.MediaVersion{
		Key:            fmt.Sprintf("%s%d:%s", models.DiscKeyPrefix, it.ServerID, disc.ClipSetHash(cs.dir)),
		ServerID:       it.ServerID,
		LibraryID:      main.LibraryID,
		LibraryTitle:   main.LibraryTitle,
		SectionKey:     main.SectionKey,
		RatingKey:      main.RatingKey,
		ItemTitle:      main.ItemTitle,
		Parts:          parts,
		Container:      "disc",
		Source:         models.SourceDisc,
		DurationMs:     clipDuration(main),
		BitrateKbps:    main.BitrateKbps,
		VideoBitrate:   main.VideoBitrate,
		Width:          main.Width,
		Height:         main.Height,
		Resolution:     main.Resolution,
		VideoCodec:     main.VideoCodec,
		VideoProfile:   main.VideoProfile,
		BitDepth:       main.BitDepth,
		FrameRate:      main.FrameRate,
		DynamicRange:   main.DynamicRange,
		DVProfile:      main.DVProfile,
		AudioTracks:    append([]models.AudioTrack{}, main.AudioTracks...),
		SubtitleTracks: append([]models.SubtitleTrack{}, main.SubtitleTracks...),
		Edition:        main.Edition,
		AddedAt:        added,
	}
	if v.LibraryID == 0 {
		v.LibraryID, v.LibraryTitle, v.SectionKey = it.LibraryID, it.LibraryTitle, it.SectionKey
	}
	if strings.TrimSpace(v.RatingKey) == "" {
		v.RatingKey = it.RatingKey
	}
	if v.ItemTitle == "" {
		v.ItemTitle = it.Title
	}
	if v.AddedAt.IsZero() {
		v.AddedAt = it.AddedAt
	}
	info := &models.DiscInfo{
		Type: string(cs.kind), Root: cs.dir, Roots: []string{cs.dir}, Discs: 1, Origin: models.DiscOriginPlex,
		FileCount: len(parts), ClipCount: clips, MainClip: mainClip, MainFeature: mainClip,
		TotalBytes: total, FeatureBytes: mainBytes, FreedBytes: freed, HardlinkedFiles: hardlinked,
		PlexMediaIDs: ids,
	}
	v.Disc = info
	p.readClipSet(it, &v, cs, detect)
	if cs.mixed {
		info.Problem = joinProblems(info.Problem, "the folder holds loose Blu-ray clips and DVD files")
		info.Removable = false
	}
	if it.MediaType == models.MediaTypeMovie {
		info.PlexItems = p.plexItemsInDisc(it.ServerID, it.RatingKey, info)
	}
	// What decorate recorded per clip holds for the set: data found out of date (review), the newest
	// placed clip (minimum age) and a clip Plex reports missing (unavailable: never resolved away).
	p.mu.Lock()
	for _, m := range members {
		if reason, ok := p.stale[m.Key]; ok {
			if _, done := p.stale[v.Key]; !done {
				p.stale[v.Key] = reason
			}
		}
		if t, ok := p.placedAt[m.Key]; ok && t.After(p.placedAt[v.Key]) {
			p.placedAt[v.Key] = t
		}
		if p.unavailableVersions[m.Key] {
			p.unavailableVersions[v.Key] = true
		}
	}
	p.mu.Unlock()
	v.DisplayTitle = discDisplayTitle(info, &v)
	return v
}

// readClipSet describes a clip set from disk when the folder is mapped and was detected: what it
// owns (every clip-named file and loose disc metadata file of the folder), its sizes and — when
// the backup kept readable playlists — its main feature. Otherwise the set is not reachable
// (Problem set, never removable).
func (p *pipeline) readClipSet(it *models.MediaItem, v *models.MediaVersion, cs *clipSet, detect bool) {
	info := v.Disc
	localRoot, mapped := p.clipSetLocalRoot(it, cs)
	var found *disc.Disc
	var foundIn string
	switch {
	case it.MediaType == models.MediaTypeEpisode:
		// TV discs are only shown and kept in v1: never read, never a removal candidate.
		info.LocalRoot = localRoot
		info.Problem = "a full-disc backup in a TV library (not read; always kept)"
		return
	case !detect:
		info.Problem = "full-disc detection is off (Settings → Media Management), so these clips were not read"
		return
	case !mapped:
		info.Problem = "no path mapping covers " + cs.dir + ", so Dupearr cannot read these clips"
		return
	}
	info.LocalRoot = localRoot
	if dir := p.detectDirFor(it, localRoot); dir != "" {
		found, foundIn = p.findDisc(dir, localRoot), dir
	}
	if found == nil {
		info.Problem = "the clips at " + cs.dir + " were not found on disk"
		return
	}
	plexClips := info.ClipCount
	mainClip, mainBytes := info.MainClip, info.FeatureBytes
	ids := info.PlexMediaIDs
	fillDisc(info, found, p.cfg.mapper, it.ServerID)
	info.Type, info.Origin = string(cs.kind), models.DiscOriginPlex
	info.PlexMediaIDs, info.MainClip = ids, mainClip
	info.ClipCount = 0
	for _, e := range found.OwnedEntries {
		if disc.IsClipName(filepath.Base(e)) || disc.IsDVDClipName(filepath.Base(e)) {
			info.ClipCount++
		}
	}
	info.ClipCount = max(info.ClipCount, plexClips)
	if found.Main != nil && found.Main.VideoCodec != "" {
		applyFeature(v, found, it) // the loose playlists describe the set better than one clip
	} else {
		info.FeatureBytes, info.MainFeature = mainBytes, mainClip
	}
	if n := found.Discs(); n > 1 {
		info.Problem = joinProblems(info.Problem, fmt.Sprintf("the clips are one of the %d discs of a set", n))
		info.Removable = false
	}
	// Every clip Plex lists must be a file of the set found on disk: otherwise removing the set
	// would leave the version half removed (or the folder changed since Plex scanned it).
	var outside []string
	for _, pt := range v.Parts {
		local, ok := p.cfg.mapper.ToLocal(models.PathSourceServer, it.ServerID, pt.Path)
		if !ok || !found.Owns(local) {
			outside = append(outside, pt.Path)
		}
	}
	if len(outside) > 0 {
		shown := outside
		if len(shown) > 3 {
			shown = append(append([]string{}, shown[:3]...), fmt.Sprintf("… %d more", len(outside)-3))
		}
		info.Problem = joinProblems(info.Problem, "Plex lists clips that are not part of the set on disk ("+strings.Join(shown, ", ")+")")
		info.Removable = false
	}
	if discNamedForOther(foundIn, found, p.itemNamesIn(it, foundIn)) {
		info.Problem = joinProblems(info.Problem, "the clips' folders are named for extras or for another title")
		info.Removable = false
	}
}

// mainClipVersion picks the main-feature candidate of a clip set: the longest clip (Plex's
// duration), then the largest, then the lowest media id.
func mainClipVersion(members []*models.MediaVersion) *models.MediaVersion {
	best := members[0]
	for _, m := range members[1:] {
		dm, db := clipDuration(m), clipDuration(best)
		sm, sb := versionBytes(m), versionBytes(best)
		switch {
		case dm != db:
			if dm > db {
				best = m
			}
		case sm != sb:
			if sm > sb {
				best = m
			}
		case m.MediaID > 0 && (best.MediaID <= 0 || m.MediaID < best.MediaID):
			best = m
		}
	}
	return best
}

// clipDuration is a Plex version's duration: the media's, else the sum of its parts'.
func clipDuration(v *models.MediaVersion) int64 {
	if v.DurationMs > 0 {
		return v.DurationMs
	}
	var n int64
	for _, pt := range v.Parts {
		if pt.Duration > 0 {
			n += pt.Duration
		}
	}
	return n
}

// versionBytes is the sum of a version's part sizes.
func versionBytes(v *models.MediaVersion) int64 {
	var n int64
	for _, pt := range v.Parts {
		n += pt.Size
	}
	return n
}
