package disc_test

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/mediainfo"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// TestFakemediaDiscsCrossCheck runs Detect and Inspect over every disc the fakemedia "discs"
// scenario writes (internal/testutil/fakemedia: an independent writer of index.bdmv, MPLS, CLPI and
// IFO files) and compares the result with the generator's ground truth (Env.DiscFixtures). Two
// independent implementations of the formats must agree on the disc's identity (kind, roots,
// owned entries, set membership), its size (files, bytes, feature bytes) and its main feature
// (playlist, duration, chapters, resolution, streams).
func TestFakemediaDiscsCrossCheck(t *testing.T) {
	env := fakemedia.Start(t, fakemedia.Discs())
	fixtures := env.DiscFixtures()
	if len(fixtures) == 0 {
		t.Fatal("the discs scenario has no discs")
	}
	ctx := context.Background()

	// Detect each movie/season folder once (a set's discs share one Disc).
	detected := map[string][]disc.Disc{}
	detect := func(folder string) []disc.Disc {
		if ds, ok := detected[folder]; ok {
			return ds
		}
		ds, err := disc.Detect(ctx, folder, disc.Options{})
		if err != nil {
			t.Fatalf("Detect(%s): %v", folder, err)
		}
		for i := range ds {
			if err := disc.Inspect(ctx, &ds[i]); err != nil {
				t.Fatalf("Inspect(%s): %v", ds[i].Root, err)
			}
		}
		detected[folder] = ds
		return ds
	}
	find := func(ds []disc.Disc, root string) *disc.Disc {
		for i := range ds {
			if slices.Contains(ds[i].Roots, root) {
				return &ds[i]
			}
		}
		return nil
	}

	sets := map[string][]fakemedia.DiscFixture{} // detect folder → members of a multi-disc set
	for _, f := range fixtures {
		root := filepath.Clean(f.LocalRoot)
		folder := root
		if f.Kind == fakemedia.DiscISO || f.SetNumber > 0 || f.Extras {
			folder = filepath.Dir(root)
		}
		ds := detect(folder)
		d := find(ds, root)
		t.Run(f.Title+"/"+filepath.Base(root), func(t *testing.T) {
			if f.Extras {
				if d != nil {
					t.Fatalf("the extras disc %s is reported as a disc of %s: %+v", root, folder, d.Roots)
				}
				return
			}
			if d == nil {
				t.Fatalf("Detect(%s) does not report the disc %s (found %d discs)", folder, root, len(ds))
			}
			if f.SetNumber > 0 {
				sets[folder] = append(sets[folder], f)
				if got := d.Discs(); got != f.SetSize {
					t.Errorf("Discs() = %d, want %d", got, f.SetSize)
				}
				if f.SetNumber > len(d.DiscFeatures) {
					t.Fatalf("no feature for disc %d of the set (%d features)", f.SetNumber, len(d.DiscFeatures))
				}
				if d.Roots[f.SetNumber-1] != root {
					t.Errorf("Roots[%d] = %s, want %s", f.SetNumber-1, d.Roots[f.SetNumber-1], root)
				}
				checkFeature(t, f, &d.DiscFeatures[f.SetNumber-1])
				return
			}
			if got, want := string(d.Type), f.Kind; got != want {
				t.Errorf("Type = %s, want %s", got, want)
			}
			if !slices.Equal(d.OwnedEntries, sortedClean(f.OwnedEntries)) {
				t.Errorf("OwnedEntries = %q, want %q", d.OwnedEntries, sortedClean(f.OwnedEntries))
			}
			if d.FileCount != f.Files || d.TotalSize != f.TotalSize {
				t.Errorf("FileCount, TotalSize = %d, %d; want %d, %d", d.FileCount, d.TotalSize, f.Files, f.TotalSize)
			}
			if got := d.FeatureBytes(); f.Readable && got != f.FeatureSize {
				// (An unreadable disc has no known main feature: its whole size stands in.)
				t.Errorf("FeatureBytes() = %d, want %d", got, f.FeatureSize)
			}
			if got := d.Readable(); got != f.Readable {
				t.Errorf("Readable() = %v (err %v), want %v", got, d.Err, f.Readable)
			}
			if d.Is3D {
				t.Errorf("Is3D = true for a 2D disc (an empty STREAM/SSIF is not 3D)")
			}
			switch {
			case f.Kind == fakemedia.DiscISO:
				// An image: attributes unknown by design, not a damaged disc.
				if d.Err != nil || d.Main != nil || d.FeatureBytes() != f.FeatureSize {
					t.Errorf("image: Err %v, Main %v, FeatureBytes %d (want nil, nil, %d)", d.Err, d.Main, d.FeatureBytes(), f.FeatureSize)
				}
				return
			case !f.Readable:
				if d.Err == nil {
					t.Errorf("a damaged disc has no Err")
				}
				if ok, _ := d.Removable(); ok {
					t.Errorf("a damaged disc is removable")
				}
				return
			}
			if ok, why := d.Removable(); !ok {
				t.Errorf("Removable() = false (%s) for an intact disc", why)
			}
			if d.Main == nil {
				t.Fatalf("no main feature (err %v)", d.Err)
			}
			checkFeature(t, f, d.Main)
		})
	}

	// A set: one Disc whose owned entries are the members' and whose sizes are their sum.
	for folder, members := range sets {
		d := find(detected[folder], filepath.Clean(members[0].LocalRoot))
		var owned []string
		files, bytes, feature := 0, int64(0), int64(0)
		for _, m := range members {
			owned = append(owned, m.OwnedEntries...)
			files += m.Files
			bytes += m.TotalSize
			feature += m.FeatureSize
		}
		if !slices.Equal(d.OwnedEntries, sortedClean(owned)) {
			t.Errorf("set %s: OwnedEntries = %q, want %q", folder, d.OwnedEntries, sortedClean(owned))
		}
		if d.FileCount != files || d.TotalSize != bytes || d.FeatureBytes() != feature {
			t.Errorf("set %s: files, bytes, feature = %d, %d, %d; want %d, %d, %d", folder,
				d.FileCount, d.TotalSize, d.FeatureBytes(), files, bytes, feature)
		}
		if ok, why := d.Removable(); !ok {
			t.Errorf("set %s: Removable() = false (%s)", folder, why)
		}
	}
	env.AssertDiscsIntact(t)
}

// checkFeature compares a main feature with the generator's description of it.
func checkFeature(t *testing.T, f fakemedia.DiscFixture, m *disc.Feature) {
	t.Helper()
	if got := path.Clean(m.Playlist); got != f.MainFeature {
		t.Errorf("Playlist = %s, want %s", got, f.MainFeature)
	}
	if m.DurationMs != f.DurationMs {
		t.Errorf("DurationMs = %d, want %d", m.DurationMs, f.DurationMs)
	}
	if f.Chapters != 0 && m.Chapters != f.Chapters {
		t.Errorf("Chapters = %d, want %d", m.Chapters, f.Chapters)
	}
	if m.Bytes != f.FeatureSize {
		t.Errorf("Bytes = %d, want %d", m.Bytes, f.FeatureSize)
	}
	if m.Width != f.Video.Width || m.Height != f.Video.Height {
		t.Errorf("resolution = %dx%d, want %dx%d", m.Width, m.Height, f.Video.Width, f.Video.Height)
	}
	if len(f.MainClips) > 0 {
		var ids []string
		for _, c := range f.MainClips {
			ids = append(ids, strings.TrimSuffix(path.Base(c), path.Ext(c)))
		}
		if !slices.Equal(m.ClipIDs, ids) {
			t.Errorf("ClipIDs = %q, want %q", m.ClipIDs, ids)
		}
	}
	var gotAudio, wantAudio []string
	// Both sides normalized to ISO 639-2/B (the fake writes the /T form "fra", Dupearr's
	// models use "fre").
	for _, a := range m.AudioTracks {
		gotAudio = append(gotAudio, mediainfo.LanguageCode(a.LanguageCode))
	}
	for _, a := range f.Audio {
		wantAudio = append(wantAudio, mediainfo.LanguageCode(a.LanguageCode))
	}
	if !slices.Equal(gotAudio, wantAudio) {
		t.Errorf("audio languages = %q, want %q", gotAudio, wantAudio)
	}
	var gotSubs, wantSubs []string
	for _, s := range m.SubtitleTracks {
		gotSubs = append(gotSubs, mediainfo.LanguageCode(s.LanguageCode))
	}
	for _, s := range f.Subtitles {
		wantSubs = append(wantSubs, mediainfo.LanguageCode(s.LanguageCode))
	}
	if !slices.Equal(gotSubs, wantSubs) {
		t.Errorf("subtitle languages = %q, want %q", gotSubs, wantSubs)
	}
}

func sortedClean(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		out = append(out, filepath.Clean(p))
	}
	slices.Sort(out)
	return out
}
