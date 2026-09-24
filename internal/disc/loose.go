package disc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Loose clip sets (docs/DECISIONS.md D9 "Loose clip sets").
//
// Some libraries keep Blu-ray backups FLATTENED: the numbered STREAM clips ("00174.m2ts",
// "00175.m2ts" …) lie loose in the movie folder, without BDMV/. Plex's default scanner lists every
// such clip as a separate version of the movie, so a movie shows up with a hundred "copies" — and
// removing all but one of them destroys the backup (a feature can span clips, and the longest clip
// is not always the film). Dupearr therefore treats every clip-named file of a folder (IsClipName,
// plus the loose .mpls/.clpi/.bdmv files next to them) as ONE disc version of type BlurayClips:
// Detect reports it, Inspect measures it and reads the main feature from loose playlists when the
// backup kept them, and IsDiscPath protects each clip on its own.

// inspectLooseBluray reads the main feature of the loose clip set in root from the playlists a
// flattened backup kept next to its clips ("00800.mpls", the clip's "00800.clpi", an optional
// "index.bdmv"). No playlist is not an error: the set's attributes are then unknown (res nil).
// Playlists that all fail to parse, or a main feature whose clips are missing, are (ErrUnreadable,
// ErrIncomplete — the set is protected).
func inspectLooseBluray(ctx context.Context, root string, opts Options, bud *budget) (*titleResult, error) {
	rt, err := openDir(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rt.Close() }()
	entries, err := listDir(ctx, rt, ".", opts.MaxDirEntries)
	if err != nil {
		return nil, err
	}
	var names []string
	clipSizes := map[string]int64{}
	var index fs.DirEntry
	for _, e := range entries {
		name := e.Name()
		if !isRegular(e) {
			continue
		}
		switch {
		case reMPLSName.MatchString(name):
			names = append(names, name)
		case reClipName.MatchString(name):
			if li, err := rt.Lstat(name); err == nil && li.Mode().IsRegular() {
				clipSizes[strings.ToUpper(strings.TrimSuffix(name, path.Ext(name)))] = li.Size()
			}
		case strings.EqualFold(name, "index.bdmv"):
			index = e
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > opts.MaxPlaylists {
		return nil, fmt.Errorf("%d playlists (limit %d): %w", len(names), opts.MaxPlaylists, ErrLimit)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })

	res := &titleResult{}
	var idx *indexFile
	if index != nil {
		if b, err := readFile(rt, index.Name(), maxIndexBytes, false, bud); err == nil {
			if x, err := parseIndex(b); err == nil {
				idx = x
				res.uhd = idx.uhd()
			}
		} else if errors.Is(err, ErrLimit) {
			return nil, err
		}
	}
	var cands []*candidate
	failed := 0
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pl, err := readPlaylist(rt, name, bud)
		if errors.Is(err, ErrLimit) {
			return nil, err
		}
		if err != nil {
			failed++
			continue
		}
		cands = append(cands, newCandidate(reMPLSName.FindStringSubmatch(name)[1], filepath.ToSlash(name), pl))
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: no readable playlist (%d found, %d unreadable)", ErrUnreadable, len(names), failed)
	}
	main, kept := selectMain(cands, opts.KnownPlaylists)
	if main == nil {
		return nil, fmt.Errorf("%w: every playlist loops or repeats a segment", ErrUnreadable)
	}
	f, missing := buildFeature(main, clipSizes)
	if f.VideoCodec == "" && len(f.ClipIDs) > 0 {
		if e, ok := findFold(entries, f.ClipIDs[0]+".clpi"); ok && isRegular(e) {
			if b, err := readFile(rt, e.Name(), maxClipInfoBytes, true, bud); err == nil {
				if c, err := parseCLPI(b); err == nil {
					applyClipInfo(&f, c, idx)
				}
			}
		}
	}
	res.main = &f
	for _, c := range alternateCandidates(main, kept, maxAlternates) {
		af, _ := buildFeature(c, clipSizes)
		res.alternates = append(res.alternates, af)
	}
	if len(missing) > 0 {
		return res, fmt.Errorf("%w: main feature clip(s) missing from the folder: %s", ErrIncomplete, strings.Join(missing, ", "))
	}
	return res, nil
}
