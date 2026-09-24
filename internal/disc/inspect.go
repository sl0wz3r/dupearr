package disc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Per-file read limits for metadata.
const (
	maxIndexBytes    = 1 << 20 // index.bdmv
	maxPlaylistBytes = 1 << 20 // one .mpls
	maxClipInfoBytes = 4 << 20 // prefix of one .clpi (header, clip, sequence and program info)
	maxIFOBytes      = 8 << 20 // one .IFO/.BUP
	maxAlternates    = 16
)

var reMPLSName = regexp.MustCompile(`(?i)^([0-9]{5})\.mpls$`)

// Inspect measures the owned entries of d (FileCount, TotalSize, FreedBytes, HardlinkedFiles,
// Irregular, NewestModTime, Fingerprint) and reads the main feature: Blu-ray index.bdmv +
// playlists (+ the first clip's .clpi when a playlist lacks video attributes), DVD IFO files of
// the largest title set; nothing for images and protect-only kinds (Main stays nil). For a
// multi-disc set the per-disc features go to DiscFeatures and Main is their sum.
//
// Problems with the disc (limits, unreadable or missing metadata, missing main-feature clips,
// I/O errors) are recorded in d.Err, joined with whatever Detect had found; the call itself
// only fails when ctx is done or d is nil. Inspect may be called again to refresh a disc.
func Inspect(ctx context.Context, d *Disc) error {
	if d == nil {
		return errors.New("disc: inspect: nil disc")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !d.inspected {
		d.baseErr = d.Err
		d.inspected = true
	}
	opts := d.opts.normalized()
	var errs []error
	if len(d.Roots) == 0 && d.Root != "" {
		d.Roots = []string{d.Root}
	}

	st, err := Measure(ctx, d.OwnedEntries, opts)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		errs = append(errs, err)
	}
	d.FileCount, d.TotalSize, d.FreedBytes = st.Files, st.Bytes, st.FreedBytes
	d.HardlinkedFiles, d.Irregular, d.NewestModTime, d.Fingerprint = st.HardlinkedFiles, st.Irregular, st.Newest, st.Fingerprint
	d.Main, d.Alternates, d.DiscFeatures, d.Is3D = nil, nil, nil, false

	if d.Type == Bluray || d.Type == UHDBluray || d.Type == DVD || d.Type == BlurayClips {
		bud := &budget{left: opts.MaxMetadataBytes}
		feats := make([]Feature, 0, len(d.Roots))
		complete, uhd := true, len(d.Roots) > 0
		for i, root := range d.Roots {
			var res *titleResult
			switch d.Type {
			case DVD:
				res, err = inspectDVD(ctx, root, opts, bud)
			case BlurayClips:
				res, err = inspectLooseBluray(ctx, root, opts, bud)
			default:
				res, err = inspectBluray(ctx, root, opts, bud)
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil {
				if len(d.Roots) > 1 {
					err = fmt.Errorf("disc %d: %w", i+1, err)
				}
				errs = append(errs, err)
			}
			if res == nil || res.main == nil {
				complete, uhd = false, false
				feats = append(feats, Feature{})
				continue
			}
			feats = append(feats, *res.main)
			uhd = uhd && res.uhd
			d.Is3D = d.Is3D || res.is3D
			if len(d.Roots) == 1 {
				d.Alternates = res.alternates
			}
		}
		if d.Type == Bluray && uhd {
			d.Type = UHDBluray
		}
		switch {
		case len(d.Roots) == 1 && complete:
			m := feats[0]
			d.Main = &m
		case len(d.Roots) > 1 && (complete || d.Type != BlurayClips):
			// (A set of loose clip folders without readable playlists has no main feature.)
			d.DiscFeatures = feats
			m := mergeFeatures(feats)
			d.Main = &m
		}
	}
	d.Err = errors.Join(append([]error{d.baseErr}, errs...)...)
	return nil
}

// titleResult is what one disc root yields.
type titleResult struct {
	main       *Feature
	alternates []Feature
	uhd        bool
	is3D       bool
}

// mergeFeatures sums the discs of a set: stream attributes from the first disc that has
// video, the first disc's playlist, and durations, chapters, clips and bytes summed.
func mergeFeatures(feats []Feature) Feature {
	var m Feature
	for _, f := range feats {
		if f.VideoCodec != "" {
			m.Width, m.Height, m.VideoCodec, m.FrameRate, m.BitDepth = f.Width, f.Height, f.VideoCodec, f.FrameRate, f.BitDepth
			m.DynamicRange, m.DVProfile = f.DynamicRange, f.DVProfile
			m.AudioTracks = append([]models.AudioTrack(nil), f.AudioTracks...)
			m.SubtitleTracks = append([]models.SubtitleTrack(nil), f.SubtitleTracks...)
			break
		}
	}
	for _, f := range feats {
		if m.Playlist == "" {
			m.Playlist = f.Playlist
		}
		m.DurationMs += f.DurationMs
		m.Chapters += f.Chapters
		m.Clips += f.Clips
		m.ClipIDs = append(m.ClipIDs, f.ClipIDs...)
		m.Bytes += f.Bytes
	}
	return m
}

// ---------------------------------------------------------------------------
// Blu-ray
// ---------------------------------------------------------------------------

// inspectBluray reads the main feature of the Blu-ray disc rooted at root.
func inspectBluray(ctx context.Context, root string, opts Options, bud *budget) (*titleResult, error) {
	rt, err := openDir(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rt.Close() }()
	list := func(rel string) ([]fs.DirEntry, error) { return listDir(ctx, rt, rel, opts.MaxDirEntries) }

	rootEntries, err := list(".")
	if err != nil {
		return nil, err
	}
	bd, ok := findFold(rootEntries, "BDMV")
	if !ok || !isRealDir(bd) {
		return nil, fmt.Errorf("BDMV is missing: %w", ErrIncomplete)
	}
	bdmv := bd.Name()
	be, err := list(bdmv)
	if err != nil {
		return nil, err
	}
	// BDMV/BACKUP holds live copies of index.bdmv, MovieObject.bdmv, PLAYLIST/ and CLIPINF/,
	// read when a primary file is unreadable (libbluray).
	var backup []fs.DirEntry
	backupName := ""
	if bk, ok := findFold(be, "BACKUP"); ok && isRealDir(bk) {
		backupName = bk.Name()
		if backup, err = list(joinRel(bdmv, backupName)); err != nil {
			backup, backupName = nil, ""
		}
	}
	subList := func(parent []fs.DirEntry, parentRel, name string) (string, []fs.DirEntry) {
		e, ok := findFold(parent, name)
		if !ok || !isRealDir(e) {
			return "", nil
		}
		rel := joinRel(parentRel, e.Name())
		entries, err := list(rel)
		if err != nil {
			return "", nil
		}
		return rel, entries
	}

	res := &titleResult{}
	idx, err := readIndex(rt, bdmv, be, backupName, backup, bud)
	if err != nil {
		return nil, fmt.Errorf("%w: index.bdmv: %w", ErrUnreadable, err)
	}
	res.uhd = idx.uhd()

	plRel, plEntries := subList(be, bdmv, "PLAYLIST")
	bkPlRel, bkPlEntries := "", []fs.DirEntry(nil)
	if backupName != "" {
		bkPlRel, bkPlEntries = subList(backup, joinRel(bdmv, backupName), "PLAYLIST")
	}
	source, sourceRel := plEntries, plRel
	if len(plEntries) == 0 {
		source, sourceRel = bkPlEntries, bkPlRel
	}
	var names []string
	for _, e := range source {
		if isRegular(e) && reMPLSName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) > opts.MaxPlaylists {
		return nil, fmt.Errorf("%d playlists (limit %d): %w", len(names), opts.MaxPlaylists, ErrLimit)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })

	var cands []*candidate
	failed := 0
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := reMPLSName.FindStringSubmatch(name)[1]
		rel := joinRel(sourceRel, name)
		pl, err := readPlaylist(rt, rel, bud)
		if errors.Is(err, ErrLimit) {
			return nil, err
		}
		if err != nil && sourceRel == plRel && bkPlRel != "" {
			if bk, ok := findFold(bkPlEntries, name); ok && isRegular(bk) {
				bkRel := joinRel(bkPlRel, bk.Name())
				if bpl, berr := readPlaylist(rt, bkRel, bud); berr == nil {
					pl, rel, err = bpl, bkRel, nil
				} else if errors.Is(berr, ErrLimit) {
					return nil, berr
				}
			}
		}
		if err != nil {
			failed++
			continue
		}
		cands = append(cands, newCandidate(id, filepath.ToSlash(rel), pl))
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: no readable playlist (%d found, %d unreadable)", ErrUnreadable, len(names), failed)
	}
	main, kept := selectMain(cands, opts.KnownPlaylists)
	if main == nil {
		return nil, fmt.Errorf("%w: every playlist loops or repeats a segment", ErrUnreadable)
	}

	// Clip sizes: STREAM/<id>.m2ts; 3D discs keep interleaved copies in STREAM/SSIF.
	clipSizes := map[string]int64{}
	stRel, stEntries := subList(be, bdmv, "STREAM")
	for _, e := range stEntries {
		name := e.Name()
		if !isRegular(e) || !strings.EqualFold(path.Ext(name), ".m2ts") {
			continue
		}
		if li, err := rt.Lstat(joinRel(stRel, name)); err == nil && li.Mode().IsRegular() {
			clipSizes[strings.ToUpper(strings.TrimSuffix(name, path.Ext(name)))] = li.Size()
		}
	}
	if _, ssif := subList(stEntries, stRel, "SSIF"); len(ssif) > 0 {
		for _, e := range ssif {
			if isRegular(e) {
				res.is3D = true
				break
			}
		}
	}

	f, missing := buildFeature(main, clipSizes)
	if f.VideoCodec == "" && len(f.ClipIDs) > 0 {
		if c := readClipInfo(ctx, rt, opts, bdmv, be, backupName, backup, f.ClipIDs[0], bud); c != nil {
			applyClipInfo(&f, c, idx)
		}
	}
	res.main = &f
	for _, c := range alternateCandidates(main, kept, maxAlternates) {
		af, _ := buildFeature(c, clipSizes)
		res.alternates = append(res.alternates, af)
	}
	if len(missing) > 0 {
		return res, fmt.Errorf("%w: main feature clip(s) missing from STREAM: %s", ErrIncomplete, strings.Join(missing, ", "))
	}
	return res, nil
}

// readIndex reads BDMV/index.bdmv, falling back to BDMV/BACKUP/index.bdmv.
func readIndex(rt *os.Root, bdmv string, be []fs.DirEntry, backupName string, backup []fs.DirEntry, bud *budget) (*indexFile, error) {
	var firstErr error
	try := func(entries []fs.DirEntry, dir string) *indexFile {
		e, ok := findFold(entries, "index.bdmv")
		if !ok || !isRegular(e) {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s/index.bdmv is missing", filepath.ToSlash(dir))
			}
			return nil
		}
		b, err := readFile(rt, joinRel(dir, e.Name()), maxIndexBytes, false, bud)
		var idx *indexFile
		if err == nil {
			idx, err = parseIndex(b)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		return idx
	}
	if idx := try(be, bdmv); idx != nil {
		return idx, nil
	}
	if backupName != "" && !errors.Is(firstErr, ErrLimit) {
		if idx := try(backup, joinRel(bdmv, backupName)); idx != nil {
			return idx, nil
		}
	}
	return nil, firstErr
}

func readPlaylist(rt *os.Root, rel string, bud *budget) (*mplsFile, error) {
	b, err := readFile(rt, rel, maxPlaylistBytes, false, bud)
	if err != nil {
		return nil, err
	}
	return parseMPLS(b)
}

// readClipInfo reads CLIPINF/<id>.clpi (or its BACKUP copy); nil when unavailable.
func readClipInfo(ctx context.Context, rt *os.Root, opts Options, bdmv string, be []fs.DirEntry, backupName string, backup []fs.DirEntry, id string, bud *budget) *clipFile {
	try := func(entries []fs.DirEntry, dir string) *clipFile {
		ci, ok := findFold(entries, "CLIPINF")
		if !ok || !isRealDir(ci) {
			return nil
		}
		clips, err := listDir(ctx, rt, joinRel(dir, ci.Name()), opts.MaxDirEntries)
		if err != nil {
			return nil
		}
		e, ok := findFold(clips, id+".clpi")
		if !ok || !isRegular(e) {
			return nil
		}
		b, err := readFile(rt, joinRel(dir, ci.Name(), e.Name()), maxClipInfoBytes, true, bud)
		if err != nil {
			return nil
		}
		c, err := parseCLPI(b)
		if err != nil {
			return nil
		}
		return c
	}
	if c := try(be, bdmv); c != nil {
		return c
	}
	if backupName != "" {
		return try(backup, joinRel(bdmv, backupName))
	}
	return nil
}

// ---------------------------------------------------------------------------
// DVD
// ---------------------------------------------------------------------------

// inspectDVD reads the main feature of the DVD rooted at root (a VIDEO_TS folder, or the files
// directly in root when there is none — each disc of a set is looked at on its own): the title
// set with the most title-VOB bytes (Jellyfin keeps the title sets with large VOBs; the largest
// is the feature), its attributes and longest program chain from VTS_NN_0.IFO, the chapter
// count from the VIDEO_TS.IFO title table. Each IFO falls back to its .BUP copy.
func inspectDVD(ctx context.Context, root string, opts Options, bud *budget) (*titleResult, error) {
	rt, err := openDir(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rt.Close() }()
	entries, err := listDir(ctx, rt, ".", opts.MaxDirEntries)
	if err != nil {
		return nil, err
	}
	dir, prefix := ".", ""
	if v, ok := findFold(entries, "VIDEO_TS"); ok && isRealDir(v) {
		dir, prefix = v.Name(), v.Name()+"/"
		if entries, err = listDir(ctx, rt, dir, opts.MaxDirEntries); err != nil {
			return nil, err
		}
	}

	type titleSet struct {
		bytes int64
		vobs  []string
	}
	sets := map[int]*titleSet{}
	for _, e := range entries {
		m := reTitleVOB.FindStringSubmatch(e.Name())
		if m == nil || !isRegular(e) {
			continue
		}
		li, err := rt.Lstat(joinRel(dir, e.Name()))
		if err != nil || !li.Mode().IsRegular() {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		ts := sets[n]
		if ts == nil {
			ts = &titleSet{}
			sets[n] = ts
		}
		ts.bytes += li.Size()
		ts.vobs = append(ts.vobs, e.Name())
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("no title VOB: %w", ErrIncomplete)
	}
	best := 0
	for n, ts := range sets {
		if best == 0 || ts.bytes > sets[best].bytes || (ts.bytes == sets[best].bytes && n < best) {
			best = n
		}
	}
	ts := sets[best]
	sort.Strings(ts.vobs)

	readIFO := func(base string) ([]byte, string, error) {
		var firstErr error
		for _, ext := range []string{".IFO", ".BUP"} {
			e, ok := findFold(entries, base+ext)
			if !ok || !isRegular(e) {
				continue
			}
			b, err := readFile(rt, joinRel(dir, e.Name()), maxIFOBytes, false, bud)
			if err == nil {
				return b, e.Name(), nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s.IFO is missing", base)
		}
		return nil, "", firstErr
	}

	vtsBase := fmt.Sprintf("VTS_%02d_0", best)
	f := Feature{Playlist: prefix + vtsBase + ".IFO", Clips: len(ts.vobs), ClipIDs: ts.vobs, Bytes: ts.bytes}
	res := &titleResult{main: &f}

	var vmg *vmgFile
	b, _, vmgErr := readIFO("VIDEO_TS")
	if vmgErr == nil {
		vmg, vmgErr = parseVMGI(b)
	}
	if vmgErr != nil {
		// The disc's navigation is broken; one more parse of the title set is not worth it.
		return res, fmt.Errorf("%w: VIDEO_TS.IFO: %w", ErrUnreadable, vmgErr)
	}

	b, name, err := readIFO(vtsBase)
	var vts *vtsFile
	if err == nil {
		// Try the .BUP when the .IFO reads but does not parse.
		if vts, err = parseVTSI(b); err != nil && strings.EqualFold(path.Ext(name), ".ifo") {
			if bup, ok := findFold(entries, vtsBase+".BUP"); ok && isRegular(bup) {
				if bb, berr := readFile(rt, joinRel(dir, bup.Name()), maxIFOBytes, false, bud); berr == nil {
					if bv, perr := parseVTSI(bb); perr == nil {
						vts, err, name = bv, nil, bup.Name()
					}
				}
			}
		}
	}
	if err != nil {
		return res, fmt.Errorf("%w: %s: %w", ErrUnreadable, vtsBase+".IFO", err)
	}
	f.Playlist = prefix + name
	applyVTS(&f, vts)
	for _, t := range vmg.titles {
		if t.vts == best && t.chapters > f.Chapters {
			f.Chapters = t.chapters
		}
	}
	if f.Chapters == 0 {
		f.Chapters = vts.longestPrograms
	}
	return res, nil
}

// applyVTS fills a feature from a title set's attributes.
func applyVTS(f *Feature, v *vtsFile) {
	f.VideoCodec = models.VCodecOther
	if v.mpeg2 {
		f.VideoCodec = models.VCodecMPEG2
	}
	f.Width, f.Height = v.width, v.height
	f.FrameRate = "29.97"
	if v.pal {
		f.FrameRate = "25"
	}
	f.BitDepth = 8
	f.DynamicRange = models.DRSDR
	f.DurationMs = v.longestMs
	for i, a := range v.audio {
		f.AudioTracks = append(f.AudioTracks, dvdAudioTrack(a, i == 0))
	}
	for _, s := range v.subs {
		f.SubtitleTracks = append(f.SubtitleTracks, models.SubtitleTrack{Codec: "vobsub", LanguageCode: langCode(s)})
	}
}

// dvdAudioTrack maps a DVD audio stream attribute.
func dvdAudioTrack(a dvdAudio, first bool) models.AudioTrack {
	t := models.AudioTrack{Channels: a.channels, LanguageCode: langCode(a.lang), Default: first}
	switch a.format {
	case 0:
		t.Format, t.Codec = models.AudioAC3, "ac3"
	case 2, 3:
		t.Format, t.Codec = models.AudioOther, "mp2"
	case 4:
		t.Format, t.Codec = models.AudioPCM, "pcm_dvd"
	case 6:
		t.Format, t.Codec = models.AudioDTS, "dca"
	default:
		t.Format, t.Codec = models.AudioOther, ""
	}
	if a.codeExt == 3 || a.codeExt == 4 {
		t.Title = "Director's comments"
	}
	return t
}

// ---------------------------------------------------------------------------
// Measure
// ---------------------------------------------------------------------------

// Stats summarises the files below a disc's owned entries (Measure).
type Stats struct {
	// Files and Bytes: regular files and their total size.
	Files int
	Bytes int64
	// Dirs: folders visited (the owned folders included).
	Dirs int
	// FreedBytes: bytes of regular files with a single link (or an unknown link count);
	// HardlinkedFiles: files with more than one link (moving them frees nothing).
	FreedBytes      int64
	HardlinkedFiles int
	// Irregular: symlinks, devices, sockets, pipes and mount points (not followed or crossed).
	Irregular int
	// Newest: the newest modification time of any file or folder.
	Newest time.Time
	// Fingerprint: hex SHA-256 over every visited entry (absolute path, kind, size and
	// modification time) in a deterministic order; "" when Measure failed.
	Fingerprint string
}

// Measure walks the absolute local paths entries (typically Disc.OwnedEntries) without
// following symlinks or crossing mount points and returns their Stats. Run it right before
// acting on a disc and compare Fingerprint with the one Inspect recorded: any added, removed,
// resized or modified file changes it. Walks are bounded by Options.MaxFiles, MaxDepth and
// MaxDirEntries (ErrLimit). A missing entry is an error.
func Measure(ctx context.Context, entries []string, opts Options) (Stats, error) {
	opts = opts.normalized()
	var st Stats
	if len(entries) == 0 {
		return st, nil
	}
	sorted := make([]string, 0, len(entries))
	for _, e := range entries {
		if e == "" || !filepath.IsAbs(e) {
			return st, fmt.Errorf("disc: measure %q: %w", e, ErrNotAbsolute)
		}
		sorted = append(sorted, filepath.Clean(e))
	}
	sorted = uniqueSorted(sorted)
	w := &walker{ctx: ctx, opts: opts, st: &st, h: sha256.New()}
	for _, e := range sorted {
		if err := w.entry(e); err != nil {
			return st, err
		}
	}
	st.Fingerprint = hex.EncodeToString(w.h.Sum(nil))
	return st, nil
}

type walker struct {
	ctx     context.Context
	opts    Options
	st      *Stats
	h       hash.Hash
	visited int
}

func (w *walker) record(abs string, kind byte, fi fs.FileInfo) {
	var size, mtime int64
	if fi != nil {
		size, mtime = fi.Size(), fi.ModTime().UnixNano()
		if kind == 'd' {
			size = 0
		}
		if (kind == 'f' || kind == 'd') && fi.ModTime().After(w.st.Newest) {
			w.st.Newest = fi.ModTime()
		}
	}
	_, _ = fmt.Fprintf(w.h, "%s\x00%c\x00%d\x00%d\n", abs, kind, size, mtime)
}

func (w *walker) count() error {
	w.visited++
	if w.visited > w.opts.MaxFiles {
		return fmt.Errorf("disc: more than %d entries: %w", w.opts.MaxFiles, ErrLimit)
	}
	if w.visited%256 == 0 {
		return w.ctx.Err()
	}
	return nil
}

// visit accounts one entry (not the folders' contents) and reports whether it is a folder to
// descend into.
func (w *walker) visit(abs string, fi fs.FileInfo, rootDev uint64, haveDev bool) bool {
	mode := fi.Mode()
	switch {
	case mode.IsRegular():
		w.st.Files++
		w.st.Bytes += fi.Size()
		if _, nlink, ok := statSys(fi); ok && nlink > 1 {
			w.st.HardlinkedFiles++
		} else {
			w.st.FreedBytes += fi.Size()
		}
		w.record(abs, 'f', fi)
	case mode.IsDir():
		if dev, _, ok := statSys(fi); ok && haveDev && dev != rootDev {
			w.st.Irregular++
			w.record(abs, 'm', fi)
			return false
		}
		w.st.Dirs++
		w.record(abs, 'd', fi)
		return true
	case mode&fs.ModeSymlink != 0:
		w.st.Irregular++
		w.record(abs, 'l', fi)
	default:
		w.st.Irregular++
		w.record(abs, 'o', fi)
	}
	return false
}

// entry measures one owned entry through an os.Root opened on its parent folder.
func (w *walker) entry(abs string) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	parent, base := filepath.Dir(abs), filepath.Base(abs)
	rt, err := openDir(parent)
	if err != nil {
		return fmt.Errorf("disc: measure %s: %w", abs, err)
	}
	defer func() { _ = rt.Close() }()
	pi, err := rt.Stat(".")
	if err != nil {
		return fmt.Errorf("disc: measure %s: %w", abs, err)
	}
	rootDev, _, haveDev := statSys(pi)
	fi, err := rt.Lstat(base)
	if err != nil {
		return fmt.Errorf("disc: measure %s: %w", abs, err)
	}
	if err := w.count(); err != nil {
		return err
	}
	if !w.visit(abs, fi, rootDev, haveDev) {
		return nil
	}

	type dirItem struct {
		rel   string
		depth int
	}
	stack := []dirItem{{rel: base}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if err := w.ctx.Err(); err != nil {
			return err
		}
		f, _, err := openSubdir(rt, it.rel)
		if err != nil {
			return fmt.Errorf("disc: measure %s: %w", filepath.Join(parent, it.rel), err)
		}
		limit := min(w.opts.MaxDirEntries, w.opts.MaxFiles-w.visited)
		children, err := readEntries(w.ctx, f, it.rel, max(limit, 0))
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("disc: measure %s: %w", filepath.Join(parent, it.rel), err)
		}
		var subdirs []dirItem
		for _, c := range children {
			if err := w.count(); err != nil {
				return err
			}
			rel := filepath.Join(it.rel, c.Name())
			ci, err := rt.Lstat(rel)
			if err != nil {
				return fmt.Errorf("disc: measure %s: %w", filepath.Join(parent, rel), err)
			}
			if w.visit(filepath.Join(parent, rel), ci, rootDev, haveDev) {
				if it.depth+1 > w.opts.MaxDepth {
					return fmt.Errorf("disc: %s is nested deeper than %d levels: %w", filepath.Join(parent, rel), w.opts.MaxDepth, ErrLimit)
				}
				subdirs = append(subdirs, dirItem{rel: rel, depth: it.depth + 1})
			}
		}
		for i := len(subdirs) - 1; i >= 0; i-- { // pop in name order
			stack = append(stack, subdirs[i])
		}
	}
	return nil
}
