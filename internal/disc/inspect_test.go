package disc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// inspectOne detects exactly one disc in dir and inspects it.
func inspectOne(t *testing.T, dir string, opts Options) Disc {
	t.Helper()
	discs, err := Detect(context.Background(), dir, opts)
	if err != nil || len(discs) != 1 {
		t.Fatalf("Detect(%s) = %+v, %v", dir, discs, err)
	}
	d := discs[0]
	if err := Inspect(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestInspectBluray(t *testing.T) {
	dir := t.TempDir()
	writeBluray(t, dir, standardBluray())
	sizedFile(t, filepath.Join(dir, "Movie (2010).mkv"), 50<<20) // sibling: never counted
	writeFile(t, filepath.Join(dir, "movie.nfo"), []byte("x"))
	d := inspectOne(t, dir, Options{})
	if d.Err != nil || !d.Readable() || d.Type != Bluray || d.Is3D {
		t.Fatalf("disc = %+v", d)
	}
	m := d.Main
	if m.Playlist != "BDMV/PLAYLIST/00800.mpls" || m.DurationMs != 120*60*1000 || m.Chapters != 24 || m.Clips != 3 ||
		!slices.Equal(m.ClipIDs, []string{"00055", "00056", "00057"}) || m.Bytes != 30<<20 {
		t.Fatalf("main = %+v", m)
	}
	if m.Width != 1920 || m.Height != 1080 || m.VideoCodec != models.VCodecH264 || m.FrameRate != "23.976" ||
		m.DynamicRange != models.DRSDR || m.BitDepth != 8 {
		t.Fatalf("video = %+v", m)
	}
	if len(m.AudioTracks) != 2 || m.AudioTracks[0].Format != models.AudioDTSHDMA || !m.AudioTracks[0].Default ||
		m.AudioTracks[1].Format != models.AudioAC3 || m.AudioTracks[1].LanguageCode != "fre" || m.AudioTracks[1].Default {
		t.Fatalf("audio = %+v", m.AudioTracks)
	}
	if len(m.SubtitleTracks) != 2 || m.SubtitleTracks[1].LanguageCode != "ger" {
		t.Fatalf("subs = %+v", m.SubtitleTracks)
	}
	// The 45-minute extra is another long playlist with other clips; the menu loop and the
	// trailer are not.
	if len(d.Alternates) != 1 || d.Alternates[0].Playlist != "BDMV/PLAYLIST/00003.mpls" || d.Alternates[0].DurationMs != 45*60*1000 {
		t.Fatalf("alternates = %+v", d.Alternates)
	}
	// Sizes: every file under BDMV and CERTIFICATE (clips, clip info, playlists, index,
	// MovieObject, the certificate file), never the sibling MKV or NFO.
	var files int
	var bytes int64
	for _, e := range d.OwnedEntries {
		_ = filepath.Walk(e, func(_ string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().IsRegular() {
				files++
				bytes += fi.Size()
			}
			return nil
		})
	}
	if d.FileCount != files || d.TotalSize != bytes || d.TotalSize >= 50<<20+bytes || d.FileCount < 6+6+4+2 {
		t.Fatalf("FileCount %d TotalSize %d, want %d / %d", d.FileCount, d.TotalSize, files, bytes)
	}
	if d.FreedBytes != d.TotalSize || d.HardlinkedFiles != 0 || d.Irregular != 0 || d.Fingerprint == "" || d.NewestModTime.IsZero() {
		t.Fatalf("stats = %+v", d)
	}
	if d.FeatureBytes() != 30<<20 {
		t.Fatalf("FeatureBytes = %d", d.FeatureBytes())
	}
	if ok, why := d.Removable(); !ok {
		t.Fatalf("Removable = false: %s", why)
	}
}

// Seamless branching (research §4.1): two cuts share most clips; the longer one is the main
// feature and the other is reported as an alternate. Identical decoys and looping playlists
// are dropped.
func TestMainFeatureSeamlessBranchingAndDecoys(t *testing.T) {
	stn := featureSTN()
	theatrical := simplePlaylist(stn, 20, []string{"00001", "00002", "00003", "00005"}, 30, 40, 20, 30)
	extended := simplePlaylist(stn, 22, []string{"00001", "00002", "00004", "00005"}, 30, 40, 35, 30)
	decoy := extended // an identical copy under another number
	reordered := simplePlaylist(stn, 22, []string{"00005", "00004", "00002", "00001"}, 30, 35, 40, 30)
	loop := gPlaylist{chapters: 30}
	for i := 0; i < 200; i++ { // a 200-minute obfuscation loop over one clip segment
		loop.items = append(loop.items, gItem{clip: "00009", in: 0, out: minutes(1), stn: stn})
	}
	bd := bluray{
		index: gIndex{titles: 2},
		playlists: map[string]gPlaylist{
			"00800": theatrical, "00801": extended, "00802": decoy, "00803": reordered, "00900": loop,
		},
		clips: map[string]int64{"00001": 1 << 20, "00002": 1 << 20, "00003": 1 << 20, "00004": 2 << 20, "00005": 1 << 20, "00009": 1 << 10},
	}
	dir := t.TempDir()
	writeBluray(t, dir, bd)
	d := inspectOne(t, dir, Options{})
	if d.Err != nil || d.Main.Playlist != "BDMV/PLAYLIST/00801.mpls" || d.Main.DurationMs != 135*60*1000 || d.Main.Bytes != 5<<20 {
		t.Fatalf("main = %+v, err %v", d.Main, d.Err)
	}
	// 00802 duplicates 00801; 00803 plays the same clips as 00801 in another order; 00900 loops.
	if len(d.Alternates) != 1 || d.Alternates[0].Playlist != "BDMV/PLAYLIST/00800.mpls" || d.Alternates[0].DurationMs != 120*60*1000 {
		t.Fatalf("alternates = %+v", d.Alternates)
	}
}

// libbluray's tie-breaks for two feature-length playlists (both > 30 min).
func TestMainFeatureTieBreaks(t *testing.T) {
	hd := featureSTN()
	uhd := gSTN{video: []gStream{vHEVC2160}, audio: []gStream{aTrueHD}}
	lossy := gSTN{video: []gStream{vH264p1080}, audio: []gStream{aAC3fra}}
	mpeg2 := gSTN{video: []gStream{{coding: codingMPEG2Video, format: 6, rate: 1}}, audio: []gStream{aDTSHDMA}}
	moreAudio := gSTN{video: []gStream{vH264p1080}, audio: []gStream{aDTSHDMA, aAC3fra, aAC3stereo}, pg: []gStream{sPGeng, sPGdeu}}
	cases := []struct {
		name  string
		a, b  gPlaylist // a = 00100, b = 00200
		known []string
		want  string
	}{
		{"chapters beat length when one has none", simplePlaylist(hd, 0, []string{"00001"}, 130),
			simplePlaylist(hd, 18, []string{"00002"}, 110), nil, "00200"},
		{"chapter rule needs a difference over 5", simplePlaylist(hd, 1, []string{"00001"}, 130),
			simplePlaylist(hd, 6, []string{"00002"}, 110), nil, "00100"},
		{"chapter rule needs one side under 2", simplePlaylist(hd, 2, []string{"00001"}, 130),
			simplePlaylist(hd, 30, []string{"00002"}, 110), nil, "00100"},
		{"2160p beats a longer 1080p", simplePlaylist(hd, 20, []string{"00001"}, 130),
			simplePlaylist(uhd, 20, []string{"00002"}, 110), nil, "00200"},
		{"H.264 beats MPEG-2", simplePlaylist(mpeg2, 20, []string{"00001"}, 130),
			simplePlaylist(hd, 20, []string{"00002"}, 110), nil, "00200"},
		{"HD audio beats lossy", simplePlaylist(lossy, 20, []string{"00001"}, 130),
			simplePlaylist(hd, 20, []string{"00002"}, 110), nil, "00200"},
		{"known playlist", simplePlaylist(hd, 20, []string{"00001"}, 130),
			simplePlaylist(hd, 20, []string{"00002"}, 110), []string{"00200.mpls"}, "00200"},
		{"then the longer one", simplePlaylist(hd, 20, []string{"00001"}, 110),
			simplePlaylist(hd, 20, []string{"00002"}, 130), nil, "00200"},
		{"then more streams", simplePlaylist(hd, 20, []string{"00001"}, 120),
			simplePlaylist(moreAudio, 20, []string{"00002"}, 120), nil, "00200"},
		{"then the lowest number", simplePlaylist(hd, 20, []string{"00001"}, 120),
			simplePlaylist(hd, 20, []string{"00002"}, 120), nil, "00100"},
		{"short playlists: only length counts", simplePlaylist(hd, 0, []string{"00001"}, 20),
			simplePlaylist(uhd, 30, []string{"00002"}, 10), nil, "00100"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeBluray(t, dir, bluray{
				playlists: map[string]gPlaylist{"00100": c.a, "00200": c.b},
				clips:     map[string]int64{"00001": 100, "00002": 100},
			})
			d := inspectOne(t, dir, Options{KnownPlaylists: c.known})
			if d.Err != nil || !strings.Contains(d.Main.Playlist, c.want) {
				t.Fatalf("main = %s (err %v), want %s", d.Main.Playlist, d.Err, c.want)
			}
		})
	}
}

func TestInspectUHDDynamicRange(t *testing.T) {
	cases := []struct {
		name  string
		video gStream
		dv    []gStream
		want  models.DynamicRange
		dvp   int
	}{
		{"Dolby Vision enhancement layer", vHEVC2160, []gStream{{coding: codingHEVC, format: 8, rate: 1, dr: 2, pid: 0x1015}}, models.DRDolbyVisionHDR10, 7},
		{"Dolby Vision stream type", gStream{coding: codingHEVC, format: 8, rate: 1, dr: 2}, nil, models.DRDolbyVisionHDR10, 0},
		{"HDR10+", gStream{coding: codingHEVC, format: 8, rate: 1, dr: 1, hdrPlus: true}, nil, models.DRHDR10Plus, 0},
		{"HDR10", vHEVC2160, nil, models.DRHDR10, 0},
		{"SDR HEVC", gStream{coding: codingHEVC, format: 8, rate: 1}, nil, models.DRSDR, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			stn := gSTN{video: []gStream{c.video}, audio: []gStream{aTrueHD}, dv: c.dv}
			writeBluray(t, dir, bluray{
				index:     gIndex{version: "0300", titles: 1},
				playlists: map[string]gPlaylist{"00800": simplePlaylist(stn, 30, []string{"00001"}, 140)},
				clips:     map[string]int64{"00001": 1 << 20},
			})
			d := inspectOne(t, dir, Options{})
			if d.Type != UHDBluray || d.Err != nil {
				t.Fatalf("disc = %+v", d)
			}
			if d.Main.DynamicRange != c.want || d.Main.DVProfile != c.dvp || d.Main.Width != 3840 || d.Main.BitDepth != 10 || d.Main.VideoCodec != models.VCodecHEVC {
				t.Fatalf("main = %+v", d.Main)
			}
		})
	}
}

func TestInspect3DAndClipInfoFallback(t *testing.T) {
	dir := t.TempDir()
	noVideo := gSTN{audio: []gStream{aTrueHD}}
	writeBluray(t, dir, bluray{
		index:     gIndex{version: "0300", titles: 1, initialDR: 1},
		playlists: map[string]gPlaylist{"00800": simplePlaylist(noVideo, 10, []string{"00001"}, 100)},
		clips:     map[string]int64{"00001": 1 << 20},
		clipInfo: map[string]gClip{"00001": {end: minutes(100), streams: []gClipStream{
			{pid: 0x1011, coding: codingHEVC, format: 8, rate: 2},
			{pid: 0x1100, coding: codingTrueHD, format: 6, rate: 1, lang: "eng"},
		}}},
		ssif: true,
	})
	d := inspectOne(t, dir, Options{})
	if d.Err != nil || !d.Is3D || d.Type != UHDBluray {
		t.Fatalf("disc = %+v", d)
	}
	if m := d.Main; m.VideoCodec != models.VCodecHEVC || m.Width != 3840 || m.FrameRate != "24" || m.DynamicRange != models.DRHDR10 || len(m.AudioTracks) != 1 {
		t.Fatalf("main = %+v", m)
	}
}

func TestInspectUnreadableBluray(t *testing.T) {
	t.Run("every playlist broken after detection", func(t *testing.T) {
		dir := t.TempDir()
		bd := standardBluray()
		writeBluray(t, dir, bd)
		discs, err := Detect(context.Background(), dir, Options{})
		if err != nil || len(discs) != 1 || discs[0].Err != nil {
			t.Fatal(discs, err)
		}
		for id := range bd.playlists {
			writeFile(t, filepath.Join(dir, "BDMV", "PLAYLIST", id+".mpls"), append([]byte("MPLS0200"), 0xff, 0xff))
		}
		d := discs[0]
		if err := Inspect(context.Background(), &d); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(d.Err, ErrUnreadable) || d.Main != nil || d.Readable() || d.FileCount == 0 {
			t.Fatalf("disc = %+v", d)
		}
		if ok, _ := d.Removable(); ok {
			t.Fatal("an unreadable disc must not be removable")
		}
	})
	t.Run("broken primary playlist, good BACKUP copy", func(t *testing.T) {
		dir := t.TempDir()
		bd := standardBluray()
		bd.backup = true
		writeBluray(t, dir, bd)
		writeFile(t, filepath.Join(dir, "BDMV", "PLAYLIST", "00800.mpls"), append([]byte("MPLS0200"), 0, 0, 0, 0x40))
		d := inspectOne(t, dir, Options{})
		if d.Err != nil || d.Main.Playlist != "BDMV/BACKUP/PLAYLIST/00800.mpls" || d.Main.DurationMs != 120*60*1000 {
			t.Fatalf("main = %+v, err %v", d.Main, d.Err)
		}
	})
	t.Run("main feature clip missing", func(t *testing.T) {
		dir := t.TempDir()
		writeBluray(t, dir, standardBluray())
		if err := os.Remove(filepath.Join(dir, "BDMV", "STREAM", "00056.m2ts")); err != nil {
			t.Fatal(err)
		}
		d := inspectOne(t, dir, Options{})
		if !errors.Is(d.Err, ErrIncomplete) || d.Main == nil || d.Main.Bytes != 20<<20 || d.Readable() {
			t.Fatalf("disc = %+v", d)
		}
	})
}

func TestInspectLimits(t *testing.T) {
	dir := t.TempDir()
	writeBluray(t, dir, standardBluray())
	for name, opts := range map[string]Options{
		"files":     {MaxFiles: 10},
		"depth":     {MaxDepth: 1},
		"playlists": {MaxPlaylists: 2},
		"metadata":  {MaxMetadataBytes: 64},
	} {
		t.Run(name, func(t *testing.T) {
			d := inspectOne(t, dir, opts)
			if !errors.Is(d.Err, ErrLimit) {
				t.Fatalf("Err = %v, want ErrLimit", d.Err)
			}
			if ok, _ := d.Removable(); ok {
				t.Fatal("a disc over the limits must not be removable")
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	d := Disc{Type: Bluray, Root: dir, OwnedEntries: []string{filepath.Join(dir, "BDMV")}}
	if err := Inspect(canceled, &d); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Inspect = %v", err)
	}
	if err := Inspect(context.Background(), nil); err == nil {
		t.Fatal("nil disc")
	}
}

func TestInspectMultiDiscSet(t *testing.T) {
	dir := t.TempDir()
	stn := featureSTN()
	part := func(clip string, mins float64) bluray {
		return bluray{index: gIndex{titles: 1},
			playlists: map[string]gPlaylist{"00800": simplePlaylist(stn, 10, []string{clip}, mins)},
			clips:     map[string]int64{clip: 4 << 20}}
	}
	writeBluray(t, filepath.Join(dir, "Movie - Disc 1"), part("00001", 100))
	writeBluray(t, filepath.Join(dir, "Movie - Disc 2"), part("00002", 80))
	d := inspectOne(t, dir, Options{})
	if d.Err != nil || d.Discs() != 2 || len(d.DiscFeatures) != 2 {
		t.Fatalf("set = %+v", d)
	}
	if m := d.Main; m.DurationMs != 180*60*1000 || m.Bytes != 8<<20 || m.Chapters != 20 || m.Clips != 2 ||
		m.VideoCodec != models.VCodecH264 || m.Playlist != "BDMV/PLAYLIST/00800.mpls" || len(m.AudioTracks) != 2 {
		t.Fatalf("merged = %+v", m)
	}
	if d.DiscFeatures[1].DurationMs != 80*60*1000 {
		t.Fatalf("disc 2 = %+v", d.DiscFeatures[1])
	}
	if len(d.Alternates) != 0 {
		t.Fatal("sets have no alternates")
	}
}

func TestInspectDVD(t *testing.T) {
	for _, flat := range []bool{false, true} {
		dir := t.TempDir()
		dv := standardDVD()
		dv.flat = flat
		writeDVD(t, dir, dv)
		d := inspectOne(t, dir, Options{})
		if d.Err != nil || d.Flat != flat || !d.Readable() {
			t.Fatalf("flat=%v: disc = %+v", flat, d)
		}
		m := d.Main
		want := "VIDEO_TS/VTS_02_0.IFO"
		if flat {
			want = "VTS_02_0.IFO"
		}
		if m.Playlist != want || m.Bytes != 24<<20 || m.Clips != 3 || !slices.Equal(m.ClipIDs, []string{"VTS_02_1.VOB", "VTS_02_2.VOB", "VTS_02_3.VOB"}) {
			t.Fatalf("flat=%v: main = %+v", flat, m)
		}
		if m.DurationMs != (1*3600+58*60+30)*1000+12*40 || m.Chapters != 28 || m.Width != 720 || m.Height != 576 ||
			m.VideoCodec != models.VCodecMPEG2 || m.FrameRate != "25" || m.DynamicRange != models.DRSDR {
			t.Fatalf("flat=%v: attributes = %+v", flat, m)
		}
		if len(m.AudioTracks) != 3 || m.AudioTracks[0].Channels != 6 || m.AudioTracks[1].LanguageCode != "ger" ||
			m.AudioTracks[1].Format != models.AudioDTS || m.AudioTracks[2].Title != "Director's comments" {
			t.Fatalf("flat=%v: audio = %+v", flat, m.AudioTracks)
		}
		if len(m.SubtitleTracks) != 3 || m.SubtitleTracks[1].LanguageCode != "fre" || m.SubtitleTracks[2].LanguageCode != "" {
			t.Fatalf("flat=%v: subs = %+v", flat, m.SubtitleTracks)
		}
	}
}

func TestInspectUnreadableDVD(t *testing.T) {
	t.Run("broken title set IFO, good BUP", func(t *testing.T) {
		dir := t.TempDir()
		writeDVD(t, dir, standardDVD())
		writeFile(t, filepath.Join(dir, "VIDEO_TS", "VTS_02_0.IFO"), []byte("DVDVIDEO-VTS"))
		d := inspectOne(t, dir, Options{})
		if d.Err != nil || d.Main.Playlist != "VIDEO_TS/VTS_02_0.BUP" || d.Main.Chapters != 28 {
			t.Fatalf("disc = %+v, err %v", d.Main, d.Err)
		}
	})
	t.Run("broken title set IFO without BUP", func(t *testing.T) {
		dir := t.TempDir()
		dv := standardDVD()
		dv.noBUP = true
		writeDVD(t, dir, dv)
		writeFile(t, filepath.Join(dir, "VIDEO_TS", "VTS_02_0.IFO"), []byte("garbage"))
		d := inspectOne(t, dir, Options{})
		if !errors.Is(d.Err, ErrUnreadable) || d.Main == nil || d.Main.Bytes != 24<<20 || d.Main.DurationMs != 0 || d.Readable() {
			t.Fatalf("disc = %+v", d)
		}
	})
	t.Run("broken VIDEO_TS.IFO", func(t *testing.T) {
		dir := t.TempDir()
		dv := standardDVD()
		dv.noBUP = true
		writeDVD(t, dir, dv)
		writeFile(t, filepath.Join(dir, "VIDEO_TS", "VIDEO_TS.IFO"), []byte("garbage"))
		d := inspectOne(t, dir, Options{})
		if !errors.Is(d.Err, ErrUnreadable) {
			t.Fatalf("disc = %+v", d)
		}
	})
}

func TestInspectImage(t *testing.T) {
	dir := t.TempDir()
	sizedFile(t, filepath.Join(dir, "Movie.iso"), 7<<20)
	d := inspectOne(t, dir, Options{})
	if d.Err != nil || d.Main != nil || d.Readable() || d.FileCount != 1 || d.TotalSize != 7<<20 || d.FeatureBytes() != 7<<20 {
		t.Fatalf("image = %+v", d)
	}
	if ok, why := d.Removable(); !ok {
		t.Fatalf("an image is removable as a whole: %s", why)
	}
}

func TestMeasureFingerprintAndHardlinks(t *testing.T) {
	dir := t.TempDir()
	writeBluray(t, dir, standardBluray())
	entries := []string{filepath.Join(dir, "CERTIFICATE"), filepath.Join(dir, "BDMV")}
	ctx := context.Background()
	a, err := Measure(ctx, entries, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Measure(ctx, []string{entries[1], entries[0], entries[1]}, Options{})
	if err != nil || b.Fingerprint != a.Fingerprint || b.Files != a.Files {
		t.Fatalf("order/duplicates changed the result: %+v vs %+v (%v)", a, b, err)
	}
	clip := filepath.Join(dir, "BDMV", "STREAM", "00055.m2ts")
	if err := os.Chtimes(clip, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, err := Measure(ctx, entries, Options{})
	if err != nil || c.Fingerprint == a.Fingerprint || !c.Newest.After(a.Newest) {
		t.Fatal("a modified clip must change the fingerprint and the newest time")
	}
	writeFile(t, filepath.Join(dir, "BDMV", "STREAM", "00099.m2ts"), []byte("new"))
	if d, _ := Measure(ctx, entries, Options{}); d.Fingerprint == c.Fingerprint || d.Files != c.Files+1 {
		t.Fatal("an added file must change the fingerprint")
	}
	if _, err := Measure(ctx, []string{"relative"}, Options{}); !errors.Is(err, ErrNotAbsolute) {
		t.Fatalf("relative entry: %v", err)
	}
	if st, err := Measure(ctx, []string{filepath.Join(dir, "missing")}, Options{}); err == nil || st.Fingerprint != "" {
		t.Fatalf("missing entry: %+v, %v", st, err)
	}
	if st, err := Measure(ctx, nil, Options{}); err != nil || st.Files != 0 {
		t.Fatalf("no entries: %+v, %v", st, err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	// A hardlinked clip (a seeding torrent) frees nothing when the disc is moved away.
	seed := filepath.Join(t.TempDir(), "seed.m2ts")
	if err := os.Link(clip, seed); err != nil {
		t.Skipf("hardlinks unsupported here: %v", err)
	}
	h, err := Measure(ctx, entries, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if h.HardlinkedFiles != 1 || h.FreedBytes != h.Bytes-(12<<20) {
		t.Fatalf("hardlinks: %+v", h)
	}
}

func TestMeasureNeverFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir, outside := t.TempDir(), t.TempDir()
	writeBluray(t, dir, standardBluray())
	sizedFile(t, filepath.Join(outside, "big.m2ts"), 99<<20)
	mkdir(t, filepath.Join(outside, "tree", "deep"))
	if err := os.Symlink(filepath.Join(outside, "big.m2ts"), filepath.Join(dir, "BDMV", "STREAM", "00077.m2ts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "tree"), filepath.Join(dir, "BDMV", "JAR")); err != nil {
		t.Fatal(err)
	}
	d := inspectOne(t, dir, Options{})
	if d.Irregular != 2 || d.TotalSize >= 99<<20 {
		t.Fatalf("irregular %d, size %d", d.Irregular, d.TotalSize)
	}
	if ok, why := d.Removable(); ok || !strings.Contains(why, "symbolic") {
		t.Fatalf("Removable = %v, %q", ok, why)
	}
}

func TestRemovableAndHelpers(t *testing.T) {
	var nilDisc *Disc
	if ok, _ := nilDisc.Removable(); ok || nilDisc.Discs() != 1 || nilDisc.FeatureBytes() != 0 || nilDisc.Readable() || nilDisc.Owns("/x") {
		t.Fatal("nil disc helpers")
	}
	d := Disc{Type: ISO, Root: "/m/M.iso", OwnedEntries: []string{"/m/M.iso"}}
	if ok, why := d.Removable(); ok || !strings.Contains(why, "inspected") {
		t.Fatalf("uninspected: %v %q", ok, why)
	}
	for _, typ := range []Type{Bluray, UHDBluray, DVD, HDDVD, AVCHD, ISO, BDAV} {
		if !typ.Valid() || typ.Label() == "Unknown disc" {
			t.Errorf("%s: invalid or unlabeled", typ)
		}
	}
	if Type("floppy").Valid() || !Type("floppy").ProtectOnly() || Bluray.ProtectOnly() || ISO.ProtectOnly() {
		t.Fatal("ProtectOnly")
	}
}

// A DVD set whose discs use different layouts (nested VIDEO_TS and flat files) is still one
// readable set: each disc is inspected on its own.
func TestInspectDVDSetMixedLayouts(t *testing.T) {
	dir := t.TempDir()
	writeDVD(t, filepath.Join(dir, "CD1"), standardDVD())
	flat := standardDVD()
	flat.flat = true
	writeDVD(t, filepath.Join(dir, "CD2"), flat)
	d := inspectOne(t, dir, Options{})
	if d.Err != nil || d.Type != DVD || d.Discs() != 2 || len(d.DiscFeatures) != 2 {
		t.Fatalf("set = %+v", d)
	}
	one := int64((1*3600+58*60+30)*1000 + 12*40)
	if d.Main.DurationMs != 2*one || d.Main.Bytes != 48<<20 || d.DiscFeatures[1].Playlist != "VTS_02_0.IFO" {
		t.Fatalf("main = %+v, disc 2 = %+v", d.Main, d.DiscFeatures[1])
	}
	requireOwned(t, d, joinAll(dir, "CD1", "CD2"))
}
