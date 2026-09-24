package disc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Detection (docs/research/disc-structures.md §6.2 "Tier 2" and §6.10). Detect looks at one
// folder — normally a movie folder, a season folder or a folder a Plex/*arr path pointed at —
// and reports the discs rooted there. It reads folder listings and the first bytes of
// index.bdmv / one playlist only; Inspect does the expensive work.

// layout is the disc structure found in one disc root.
type layout struct {
	rel   string   // the root relative to the Detect folder ("." for the folder itself)
	typ   Type     // kind of the (first) structure
	flat  bool     // DVD files directly in the root
	owned []string // owned entry names relative to the root, sorted ("PRIVATE/AVCHD" possible)
	whole bool     // every entry of the root is owned
	err   error    // incomplete, unreadable, mixed or symlinked structure
}

// structure is one disc structure found in a root, before the primary one is chosen.
type structure struct {
	typ   Type
	flat  bool
	owned []string
	err   error
}

type detector struct {
	ctx  context.Context
	rt   *os.Root
	dir  string
	opts Options
}

func (s *detector) list(rel string) ([]fs.DirEntry, error) {
	return listDir(s.ctx, s.rt, rel, s.opts.MaxDirEntries)
}

// abs returns the absolute path of rel (relative to the Detect folder).
func (s *detector) abs(rel string) string {
	if rel == "." || rel == "" {
		return s.dir
	}
	return filepath.Join(s.dir, filepath.FromSlash(rel))
}

// Detect reports the discs rooted in the absolute local folder dir, read-only and without
// following symlinks:
//
//   - the folder itself as a disc root (BDMV/, VIDEO_TS/, flat VIDEO_TS.IFO + VTS_*.VOB files,
//     HVDVD_TS/, AVCHD/ or PRIVATE/AVCHD/, BDAV/, or the loose numbered clips of a flattened
//     Blu-ray backup — "00800.m2ts" … with their loose .mpls/.clpi/.bdmv files, type
//     BlurayClips) — the "disc root == movie folder" layout, whose owned entries never include
//     the sibling movie file, NFO, artwork or extras;
//   - "Disc N"/"CDn"/"Part N" sub-folders holding discs, reported as ONE multi-disc set;
//   - macOS .dvdmedia bundles (each a disc, or a set member when named like one);
//   - *.iso / *.img files (stacked "… CD1.iso", "… CD2.iso" images form one set).
//
// Extras folders ("Extras", "Bonus Disc" …) and other sub-folders are not searched. The discs
// come in a stable order: the folder's own disc or set, bundles, then images. A disc whose
// structure is incomplete, unreadable, unclear, mixed or symlinked is still reported, with
// Err set. The returned error is for the call itself (dir unreadable, not absolute, inside a
// disc structure, a symlink, ctx done); dir without discs returns (nil, nil).
func Detect(ctx context.Context, dir string, opts Options) ([]Disc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts = opts.normalized()
	if opts.FollowSymlinks {
		return nil, ErrFollowSymlinks
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("disc: detect %q: %w", dir, ErrNotAbsolute)
	}
	dir = filepath.Clean(dir)
	if IsDiscPath(dir) {
		return nil, fmt.Errorf("disc: detect %s: %w", dir, ErrInsideDisc)
	}
	rt, err := openDir(dir)
	if err != nil {
		return nil, fmt.Errorf("disc: detect: %w", err)
	}
	defer func() { _ = rt.Close() }()
	s := &detector{ctx: ctx, rt: rt, dir: dir, opts: opts}

	entries, err := s.list(".")
	if err != nil {
		return nil, fmt.Errorf("disc: detect %s: %w", dir, err)
	}
	top := s.classify(".", entries)

	type member struct {
		n int
		l *layout
	}
	var (
		members []member
		bundles []*layout
		taint   []error // set folders that cannot be looked into
	)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		bundle := isDVDMedia(name)
		if !bundle && IsDiscEntryName(name) {
			continue // a structure entry of the folder's own disc
		}
		setName := name
		if bundle {
			setName = name[:len(name)-len(".dvdmedia")] // "Movie - Disc 1.dvdmedia" is a set member
		}
		n, isSet := IsSetFolderName(setName)
		if !bundle && !isSet {
			continue
		}
		if isLink(e) {
			l := &layout{rel: name, typ: DVD, owned: []string{name}, err: fmt.Errorf("%s: %w", name, ErrSymlink)}
			if isSet {
				taint = append(taint, l.err)
			} else {
				bundles = append(bundles, l)
			}
			continue
		}
		if !isRealDir(e) {
			continue
		}
		sub, err := s.list(name)
		if err != nil {
			if isSet {
				taint = append(taint, fmt.Errorf("%s: %w", name, err))
			}
			continue
		}
		l := s.classify(name, sub)
		switch {
		case l == nil:
			continue // e.g. a "Part 1" folder holding an ordinary video file
		case isSet:
			members = append(members, member{n: n, l: l})
		default:
			bundles = append(bundles, l)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var discs []Disc
	switch {
	case len(members) > 0:
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].n != members[j].n {
				return members[i].n < members[j].n
			}
			return members[i].l.rel < members[j].l.rel
		})
		ls := make([]*layout, 0, len(members)+1)
		nums := make([]int, 0, len(members))
		for _, m := range members {
			ls = append(ls, m.l)
			nums = append(nums, m.n)
		}
		discs = append(discs, s.buildSet(top, ls, nums, taint))
	case top != nil:
		discs = append(discs, s.buildSingle(top))
	}
	for _, b := range bundles {
		discs = append(discs, s.buildSingle(b))
	}
	discs = append(discs, s.images(entries)...)
	return discs, nil
}

// classify identifies the disc structure(s) in the root rel from its entries, or returns nil.
func (s *detector) classify(rel string, entries []fs.DirEntry) *layout {
	var (
		structs                  []structure
		bdmv, bdav, vts, hv, avc []string
		privAVCHD                string
		bdComp, dvdComp, hdComp  []string
		flat                     []string
		clips, clipMeta          []fs.DirEntry // a loose clip set: numbered clips, loose Blu-ray metadata
	)
	for _, e := range entries {
		name := e.Name()
		up := strings.ToUpper(name)
		switch {
		case up == "BDMV" || up == "BDAV" || up == "VIDEO_TS" || up == "HVDVD_TS" || up == "AVCHD":
			if isLink(e) {
				kind, _ := lookupFold(structureDirs, name)
				structs = append(structs, structure{typ: kind, owned: []string{name},
					err: fmt.Errorf("%s: %w", name, ErrSymlink)})
				continue
			}
			if !isRealDir(e) {
				continue
			}
			switch up {
			case "BDMV":
				bdmv = append(bdmv, name)
			case "BDAV":
				bdav = append(bdav, name)
			case "VIDEO_TS":
				vts = append(vts, name)
			case "HVDVD_TS":
				hv = append(hv, name)
			default:
				avc = append(avc, name)
			}
		case up == "PRIVATE" && isRealDir(e):
			if sub, err := s.list(joinRel(rel, name)); err == nil {
				if a, ok := findFold(sub, "AVCHD"); ok && isRealDir(a) {
					privAVCHD = name + "/" + a.Name()
				}
			}
		case up == "ADV_OBJ":
			hdComp = append(hdComp, name)
		case containsFold(bdCompanions, name):
			bdComp = append(bdComp, name)
		case containsFold(dvdCompanions, name):
			dvdComp = append(dvdComp, name)
		case reFlatDVD.MatchString(name):
			flat = append(flat, name)
		case reClipName.MatchString(name) && (isRegular(e) || isLink(e)):
			clips = append(clips, e)
		case reLooseBDMeta.MatchString(name) && (isRegular(e) || isLink(e)):
			clipMeta = append(clipMeta, e)
		}
	}

	if len(bdmv) > 0 {
		st := structure{typ: Bluray, owned: append(append([]string{}, bdmv...), bdComp...)}
		st.typ, st.err = s.checkBDMV(joinRel(rel, bdmv[0]))
		if len(bdmv) > 1 {
			st.err = errors.Join(st.err, fmt.Errorf("%w: %s", ErrMixedLayout, strings.Join(bdmv, ", ")))
		}
		structs = append(structs, st)
	}
	if len(vts) > 0 {
		st := structure{typ: DVD, owned: append(append([]string{}, vts...), dvdComp...)}
		if sub, err := s.list(joinRel(rel, vts[0])); err != nil {
			st.err = fmt.Errorf("%s: %w", vts[0], err)
		} else if err := checkDVDFiles(sub); err != nil {
			st.err = fmt.Errorf("%s: %w", vts[0], err)
		}
		if len(vts) > 1 {
			st.err = errors.Join(st.err, fmt.Errorf("%w: %s", ErrMixedLayout, strings.Join(vts, ", ")))
		}
		structs = append(structs, st)
	}
	if len(flat) > 0 {
		st := structure{typ: DVD, flat: true, owned: flat}
		if err := checkDVDFiles(entries); err != nil {
			st.err = err
		}
		structs = append(structs, st)
	}
	if len(hv) > 0 {
		owned := append(append([]string{}, hv...), hdComp...)
		for _, c := range bdComp {
			if strings.EqualFold(c, "AACS") {
				owned = append(owned, c)
			}
		}
		structs = append(structs, structure{typ: HDDVD, owned: owned})
	}
	if len(avc) > 0 || privAVCHD != "" {
		owned := append([]string{}, avc...)
		if privAVCHD != "" {
			owned = append(owned, privAVCHD)
		}
		structs = append(structs, structure{typ: AVCHD, owned: owned})
	}
	if len(bdav) > 0 {
		structs = append(structs, structure{typ: BDAV, owned: bdav})
	}
	if len(clips) > 0 {
		// A flattened Blu-ray backup (docs/DECISIONS.md D9 "Loose clip sets"): every numbered clip
		// directly in the root and the loose playlists, clip information and .bdmv files next to
		// them are ONE disc — never an .mkv, NFO, artwork or subtitle beside them.
		st := structure{typ: BlurayClips, flat: true}
		var links []string
		for _, e := range append(append([]fs.DirEntry{}, clips...), clipMeta...) {
			st.owned = append(st.owned, e.Name())
			if isLink(e) {
				links = append(links, e.Name())
			}
		}
		if len(links) > 0 {
			sort.Strings(links)
			st.err = fmt.Errorf("%s: %w", strings.Join(links, ", "), ErrSymlink)
		}
		structs = append(structs, st)
	}
	if len(structs) == 0 {
		return nil
	}

	sort.SliceStable(structs, func(i, j int) bool { return structs[i].typ.priority() < structs[j].typ.priority() })
	l := &layout{rel: rel, typ: structs[0].typ, flat: structs[0].flat, err: structs[0].err}
	owned := structs[0].owned
	if len(structs) > 1 {
		kinds := make([]string, 0, len(structs))
		for _, st := range structs {
			kinds = append(kinds, string(st.typ))
			owned = append(owned, st.owned...)
		}
		owned = append(append(append(owned, bdComp...), dvdComp...), hdComp...)
		l.err = errors.Join(l.err, fmt.Errorf("%w: %s", ErrMixedLayout, strings.Join(kinds, ", ")))
	}
	l.owned = uniqueSorted(owned)
	l.whole = len(entries) > 0
	for _, e := range entries {
		if !containsExact(l.owned, e.Name()) {
			l.whole = false
			break
		}
	}
	return l
}

// checkBDMV validates a BDMV folder (rel inside the Detect folder): index.bdmv (or its
// BDMV/BACKUP copy) with a valid header, ≥ 1 playlist with a valid header and ≥ 1 .m2ts clip.
// A BDMV holding INDEX.BDM instead of index.bdmv is an AVCHD structure. The type is UHDBluray
// for index version 0300.
func (s *detector) checkBDMV(rel string) (Type, error) {
	entries, err := s.list(rel)
	if err != nil {
		return Bluray, fmt.Errorf("%s: %w", rel, err)
	}
	idx, ok := findFold(entries, "index.bdmv")
	if !ok || !isRegular(idx) {
		if bdm, ok := findFold(entries, "INDEX.BDM"); ok && isRegular(bdm) {
			return AVCHD, nil
		}
		return Bluray, fmt.Errorf("%s/index.bdmv is missing: %w", rel, ErrIncomplete)
	}
	typ, err := s.indexType(joinRel(rel, idx.Name()))
	if err != nil {
		if bk, ok := findFold(entries, "BACKUP"); ok && isRealDir(bk) {
			if be, lerr := s.list(joinRel(rel, bk.Name())); lerr == nil {
				if bi, ok := findFold(be, "index.bdmv"); ok && isRegular(bi) {
					if t, berr := s.indexType(joinRel(rel, bk.Name(), bi.Name())); berr == nil {
						typ, err = t, nil
					}
				}
			}
		}
		if err != nil {
			return Bluray, fmt.Errorf("%s/index.bdmv: %v: %w", rel, err, ErrUnreadable)
		}
	}

	pl, ok := findFold(entries, "PLAYLIST")
	if !ok || !isRealDir(pl) {
		return typ, fmt.Errorf("%s/PLAYLIST is missing: %w", rel, ErrIncomplete)
	}
	ple, err := s.list(joinRel(rel, pl.Name()))
	if err != nil {
		return typ, fmt.Errorf("%s/PLAYLIST: %w", rel, err)
	}
	valid, tried := false, 0
	for _, e := range ple {
		if !isRegular(e) || !strings.EqualFold(filepath.Ext(e.Name()), ".mpls") {
			continue
		}
		if tried++; tried > 16 {
			break
		}
		if b, err := readFile(s.rt, joinRel(rel, pl.Name(), e.Name()), 8, true, nil); err == nil {
			if _, err := parseBDHeader(newReader(b), "MPLS"); err == nil {
				valid = true
				break
			}
		}
	}
	if !valid {
		return typ, fmt.Errorf("%s/PLAYLIST holds no valid playlist: %w", rel, ErrIncomplete)
	}

	st, ok := findFold(entries, "STREAM")
	if !ok || !isRealDir(st) {
		return typ, fmt.Errorf("%s/STREAM is missing: %w", rel, ErrIncomplete)
	}
	ste, err := s.list(joinRel(rel, st.Name()))
	if err != nil {
		return typ, fmt.Errorf("%s/STREAM: %w", rel, err)
	}
	for _, e := range ste {
		if isRegular(e) && strings.EqualFold(filepath.Ext(e.Name()), ".m2ts") {
			return typ, nil
		}
	}
	return typ, fmt.Errorf("%s/STREAM holds no .m2ts clip: %w", rel, ErrIncomplete)
}

// indexType reads the 8-byte header of an index.bdmv.
func (s *detector) indexType(rel string) (Type, error) {
	b, err := readFile(s.rt, rel, 8, true, nil)
	if err != nil {
		return Bluray, err
	}
	ver, err := parseBDHeader(newReader(b), "INDX")
	if err != nil {
		return Bluray, err
	}
	if ver == "0300" {
		return UHDBluray, nil
	}
	return Bluray, nil
}

// checkDVDFiles validates the files of a DVD title: VIDEO_TS.IFO and ≥ 1 title VOB.
func checkDVDFiles(entries []fs.DirEntry) error {
	ifo, ok := findFold(entries, "VIDEO_TS.IFO")
	if !ok || !isRegular(ifo) {
		return fmt.Errorf("VIDEO_TS.IFO is missing: %w", ErrIncomplete)
	}
	for _, e := range entries {
		if isRegular(e) && reTitleVOB.MatchString(e.Name()) {
			return nil
		}
	}
	return fmt.Errorf("no title VOB (VTS_NN_1.VOB …): %w", ErrIncomplete)
}

// ownedOf returns the absolute owned entries of a layout; the whole root when allowed (a set
// folder or bundle, never the Detect folder itself) and nothing else lives in it.
func (s *detector) ownedOf(l *layout) []string {
	root := s.abs(l.rel)
	if l.rel != "." && l.whole {
		return []string{root}
	}
	out := make([]string, 0, len(l.owned))
	for _, o := range l.owned {
		out = append(out, filepath.Join(root, filepath.FromSlash(o)))
	}
	return out
}

func (s *detector) buildSingle(l *layout) Disc {
	root := s.abs(l.rel)
	return Disc{
		Type:         l.typ,
		Root:         root,
		Roots:        []string{root},
		OwnedEntries: uniqueSorted(s.ownedOf(l)),
		Flat:         l.flat,
		Err:          l.err,
		opts:         s.opts,
	}
}

// buildSet makes one Disc of the set folders ls (sorted by number nums). The set is unclear
// when the numbers are not 1..N, the kinds differ, a disc also sits in the folder itself, or a
// set folder could not be read.
func (s *detector) buildSet(top *layout, ls []*layout, nums []int, taint []error) Disc {
	var errs []error
	if top != nil {
		errs = append(errs, fmt.Errorf("%w: a disc sits in the folder itself and in set folders", ErrSetUnclear))
		ls = append([]*layout{top}, ls...)
	}
	for i, n := range nums {
		if n != i+1 {
			errs = append(errs, fmt.Errorf("%w: discs are not numbered 1..%d", ErrSetUnclear, len(nums)))
			break
		}
	}
	// Every member is named alike: the same title (or none) before the number. A member named for
	// something else — an extras disc ("Special Features - Disc 2"), another film ("Ronin Part 2")
	// — makes the set unclear, so it is protected instead of moved as a whole (GAP-02).
	prefixes := map[string]bool{}
	for _, l := range ls {
		if l == top {
			continue
		}
		p, _ := SetFolderPrefix(filepath.Base(l.rel))
		prefixes[p] = true
	}
	if len(prefixes) > 1 {
		errs = append(errs, fmt.Errorf("%w: the set folders are named for different titles or extras", ErrSetUnclear))
	}
	d := Disc{Type: ls[0].typ, Flat: ls[0].flat, opts: s.opts}
	var owned []string
	for _, l := range ls {
		if l.typ != ls[0].typ {
			errs = append(errs, fmt.Errorf("%w: mixed disc kinds (%s, %s)", ErrSetUnclear, ls[0].typ, l.typ))
		}
		if l.err != nil {
			errs = append(errs, l.err)
		}
		d.Roots = append(d.Roots, s.abs(l.rel))
		owned = append(owned, s.ownedOf(l)...)
	}
	for _, t := range taint {
		errs = append(errs, fmt.Errorf("%w: %w", ErrSetUnclear, t))
	}
	d.Root = d.Roots[0]
	d.OwnedEntries = uniqueSorted(owned)
	d.Err = errors.Join(errs...)
	return d
}

// images reports the .iso/.img files of the folder: stacked names ("… CD1.iso", "… CD2.iso")
// with a common prefix and two or more members form one set, every other image is a disc of
// its own. A lone "Part 3.iso" is an ordinary image (many titles end in "Part N").
func (s *detector) images(entries []fs.DirEntry) []Disc {
	type image struct {
		path   string
		prefix string
		n      int
		err    error
	}
	var singles []image
	groups := map[string][]image{}
	var order []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !IsImagePath(name) {
			continue
		}
		im := image{path: filepath.Join(s.dir, name)}
		switch {
		case isLink(e):
			im.err = fmt.Errorf("%s: %w", name, ErrSymlink)
		case !isRegular(e):
			continue
		default:
			li, err := s.rt.Lstat(name)
			switch {
			case err != nil:
				im.err = fmt.Errorf("%s: %w", name, err)
			case !li.Mode().IsRegular():
				continue
			case li.Size() == 0:
				im.err = fmt.Errorf("%s is empty: %w", name, ErrIncomplete)
			}
		}
		prefix, n, ok := stackedImage(name)
		if !ok {
			singles = append(singles, im)
			continue
		}
		im.prefix, im.n = prefix, n
		if _, seen := groups[prefix]; !seen {
			order = append(order, prefix)
		}
		groups[prefix] = append(groups[prefix], im)
	}
	var sets [][]image
	for _, p := range order {
		if g := groups[p]; len(g) > 1 {
			sets = append(sets, g)
		} else {
			singles = append(singles, g[0])
		}
	}
	sort.Slice(singles, func(i, j int) bool { return singles[i].path < singles[j].path })

	var out []Disc
	for _, g := range sets {
		sort.SliceStable(g, func(i, j int) bool {
			if g[i].n != g[j].n {
				return g[i].n < g[j].n
			}
			return g[i].path < g[j].path
		})
		d := Disc{Type: ISO, opts: s.opts}
		var errs []error
		for i, im := range g {
			if im.n != i+1 && len(errs) == 0 {
				errs = append(errs, fmt.Errorf("%w: images are not numbered 1..%d", ErrSetUnclear, len(g)))
			}
			if im.err != nil {
				errs = append(errs, im.err)
			}
			d.Roots = append(d.Roots, im.path)
		}
		d.Root = d.Roots[0]
		d.OwnedEntries = uniqueSorted(append([]string{}, d.Roots...))
		d.Err = errors.Join(errs...)
		out = append(out, d)
	}
	for _, im := range singles {
		out = append(out, Disc{Type: ISO, Root: im.path, Roots: []string{im.path},
			OwnedEntries: []string{im.path}, Err: im.err, opts: s.opts})
	}
	return out
}

func uniqueSorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	j := 0
	for i, v := range out {
		if i == 0 || v != out[j-1] {
			out[j] = v
			j++
		}
	}
	return out[:j]
}

func containsExact(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}
