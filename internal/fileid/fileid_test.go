package fileid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeFS injects fstat, fstatfs and the mount table for two (or more) real temporary files, so
// every filesystem type can be tested on any platform.
type fakeFS struct {
	stats  map[string]Stat  // by file base name
	magic  map[uint64]int64 // f_type by device
	mounts string
}

func (f *fakeFS) prober() *Prober {
	return New(Hooks{
		Supported: true,
		FStat: func(file *os.File) (Stat, error) {
			st, ok := f.stats[filepath.Base(file.Name())]
			if !ok {
				return Stat{}, errors.New("no such fake file")
			}
			return st, nil
		},
		Stat: func(path string) (Stat, error) {
			st, ok := f.stats[filepath.Base(path)]
			if !ok {
				return Stat{}, errors.New("no such fake file")
			}
			return st, nil
		},
		FStatfs: func(file *os.File) (int64, error) {
			st := f.stats[filepath.Base(file.Name())]
			m, ok := f.magic[st.Dev]
			if !ok {
				return 0, errors.New("statfs failed")
			}
			return m, nil
		},
		Statfs: func(path string) (int64, error) {
			st := f.stats[filepath.Base(path)]
			m, ok := f.magic[st.Dev]
			if !ok {
				return 0, errors.New("statfs failed")
			}
			return m, nil
		},
		Mountinfo: func() ([]byte, error) { return []byte(f.mounts), nil },
	})
}

// dev builds a device number whose default major:minor split (Linux: glibc encoding; elsewhere
// the fallback split) is maj:min, so the mount table below can name it.
func dev(t *testing.T, p *Prober, maj, min uint64) uint64 {
	t.Helper()
	// Linux (glibc encoding, small numbers): major in bits 8–19, minor in bits 0–7; the fallback
	// split of the other platforms: major in the top byte of 32 bits.
	for _, d := range []uint64{maj<<8 | min, maj<<24 | min} {
		if p.h.major(d) == maj && p.h.minor(d) == min {
			return d
		}
	}
	t.Fatalf("cannot build device %d:%d", maj, min)
	return 0
}

func mountLine(id int, devID, typ, mountpoint string) string {
	return fmt.Sprintf("%d 1 %s / %s rw,relatime shared:1 - %s /dev/sdx rw\n", id, devID, mountpoint, typ)
}

func twoFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return a, b
}

func TestCompareFilesystemTypes(t *testing.T) {
	a, b := twoFiles(t)
	cases := []struct {
		name    string
		typ     string
		magic   int64
		sameIno bool
		otherDv bool
		sizes   [2]int64
		nlink   uint64
		want    Verdict
		why     string
	}{
		{name: "ext4 different inodes", typ: "ext4", magic: magicExt4, want: Distinct},
		{name: "ext4 hard link count above 1", typ: "ext4", magic: magicExt4, nlink: 2, want: Distinct},
		{name: "xfs different inodes", typ: "xfs", magic: magicXFS, want: Distinct},
		{name: "btrfs different inodes", typ: "btrfs", magic: magicBtrfs, want: Distinct},
		{name: "zfs by its mount type", typ: "zfs", magic: magicZFS, want: Distinct},
		{name: "zfs with an unlisted magic", typ: "zfs", magic: 0x1234, want: Distinct},
		{name: "ext4 same inode", typ: "ext4", magic: magicExt4, sameIno: true, want: Same},
		{name: "ext4 different devices", typ: "ext4", magic: magicExt4, otherDv: true, want: Unknown, why: "different devices"},
		{name: "nfs4", typ: "nfs4", magic: magicNFS, want: Unknown, why: "network filesystem"},
		{name: "cifs", typ: "cifs", magic: magicCIFS, want: Unknown, why: "network filesystem"},
		{name: "9p", typ: "9p", magic: magic9P, want: Unknown, why: "network filesystem"},
		{name: "virtiofs", typ: "virtiofs", magic: magicFUSE, want: Unknown},
		{name: "overlay", typ: "overlay", magic: magicOverlay, want: Unknown, why: "overlay"},
		{name: "mergerfs", typ: "fuse.mergerfs", magic: magicFUSE, want: Unknown, why: "FUSE"},
		{name: "Unraid user share, link count 1 (Phase 0 fail-closed)", typ: "fuse.shfs", magic: magicFUSE, nlink: 1, want: Unknown, why: "fuse.shfs"},
		{name: "unknown type", typ: "apfs", magic: 0x1A, want: Unknown, why: "cannot prove"},
		{name: "ext3 is not on the allowlist", typ: "ext3", magic: magicExt4, want: Unknown},
		{name: "f_type and mount table disagree", typ: "ext4", magic: magicXFS, want: Unknown, why: "reports type xfs"},
		{name: "different sizes on ext4", typ: "ext4", magic: magicExt4, sameIno: false, sizes: [2]int64{10, 20}, want: Distinct},
		{name: "different sizes on nfs", typ: "nfs", magic: magicNFS, sizes: [2]int64{10, 20}, want: Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeFS{magic: map[uint64]int64{}}
			p0 := f.prober()
			d1 := dev(t, p0, 8, 1)
			d2 := dev(t, p0, 8, 2)
			nlink := max(c.nlink, 1)
			sa := Stat{Dev: d1, Ino: 100, Nlink: nlink, Size: 5, Regular: true}
			sb := Stat{Dev: d1, Ino: 200, Nlink: nlink, Size: 5, Regular: true}
			if c.sizes != [2]int64{} {
				sa.Size, sb.Size = c.sizes[0], c.sizes[1]
			}
			if c.sameIno {
				sb.Ino = sa.Ino
			}
			if c.otherDv {
				sb.Dev = d2
				f.magic[d2] = c.magic
			}
			f.magic[d1] = c.magic
			f.stats = map[string]Stat{"a.mkv": sa, "b.mkv": sb}
			f.mounts = mountLine(20, "8:1", c.typ, "/data") + mountLine(21, "8:2", c.typ, "/data2")
			got, why := f.prober().Compare(a, b)
			if got != c.want {
				t.Fatalf("Compare = %s (%s), want %s", got, why, c.want)
			}
			if c.why != "" && !strings.Contains(why, c.why) {
				t.Fatalf("reason %q does not mention %q", why, c.why)
			}
		})
	}
}

func TestCompareUnreadable(t *testing.T) {
	a, _ := twoFiles(t)
	f := &fakeFS{stats: map[string]Stat{}, magic: map[uint64]int64{}}
	if v, why := f.prober().Compare(a, filepath.Join(t.TempDir(), "missing.mkv")); v != Unknown || !strings.Contains(why, "cannot open") {
		t.Fatalf("missing file: %s (%s)", v, why)
	}
	// Opened, but fstat fails.
	if v, _ := f.prober().Compare(a, a); v != Unknown {
		t.Fatalf("fstat failure: %s", v)
	}
	// A mount table that cannot be read: the type is unknown.
	a2, b2 := twoFiles(t)
	f = &fakeFS{magic: map[uint64]int64{}}
	p := New(Hooks{Supported: true,
		FStat: func(file *os.File) (Stat, error) {
			ino := uint64(1)
			if filepath.Base(file.Name()) == "b.mkv" {
				ino = 2
			}
			return Stat{Dev: 5, Ino: ino, Nlink: 1, Size: 1, Regular: true}, nil
		},
		FStatfs:   func(*os.File) (int64, error) { return magicExt4, nil },
		Mountinfo: func() ([]byte, error) { return nil, errors.New("no /proc") },
	})
	_ = f
	if v, why := p.Compare(a2, b2); v != Unknown || !strings.Contains(why, "could not be read") {
		t.Fatalf("no mount table: %s (%s)", v, why)
	}
}

func TestConflictingMountTypes(t *testing.T) {
	a, b := twoFiles(t)
	f := &fakeFS{magic: map[uint64]int64{}}
	d := dev(t, f.prober(), 8, 1)
	f.magic[d] = magicExt4
	f.stats = map[string]Stat{"a.mkv": {Dev: d, Ino: 1, Nlink: 1, Size: 1, Regular: true}, "b.mkv": {Dev: d, Ino: 2, Nlink: 1, Size: 1, Regular: true}}
	// A bind mount of the same device with the same type is fine…
	f.mounts = mountLine(20, "8:1", "ext4", "/data") + mountLine(21, "8:1", "ext4", `/mnt/with\040space`)
	if v, why := f.prober().Compare(a, b); v != Distinct {
		t.Fatalf("bind mount: %s (%s)", v, why)
	}
	// …two types for one device are not.
	f.mounts = mountLine(20, "8:1", "ext4", "/data") + mountLine(21, "8:1", "fuse.shfs", "/mnt/user")
	if v, why := f.prober().Compare(a, b); v != Unknown || !strings.Contains(why, "both") {
		t.Fatalf("conflicting types: %s (%s)", v, why)
	}
	// No entry for the device.
	f.mounts = mountLine(20, "9:9", "ext4", "/data")
	if v, why := f.prober().Compare(a, b); v != Unknown || !strings.Contains(why, "no entry") {
		t.Fatalf("missing entry: %s (%s)", v, why)
	}
}

func TestParseMountinfo(t *testing.T) {
	data := strings.Join([]string{
		`36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue`,
		`37 36 0:52 / /mnt/user rw,noatime shared:2 - fuse.shfs shfs rw,user_id=0`,
		`38 36 8:17 /media /data/My\040Media rw - xfs /dev/sdb1 rw`, // escaped path, no optional fields
		`39 36 0:60 / /proc rw - proc proc rw`,
		`garbage line`,
		`40 36 x:y / /bad rw - ext4 /dev/x rw`,
		``,
	}, "\n")
	got := ParseMountinfo([]byte(data))
	want := []mountEntry{{dev: "98:0", typ: "ext3"}, {dev: "0:52", typ: "fuse.shfs"}, {dev: "8:17", typ: "xfs"}, {dev: "0:60", typ: "proc"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ParseMountinfo = %v, want %v", got, want)
	}
}

func TestPathInfoAndCouldBeDistinct(t *testing.T) {
	a, b := twoFiles(t)
	f := &fakeFS{magic: map[uint64]int64{}}
	p0 := f.prober()
	d1, d2 := dev(t, p0, 8, 1), dev(t, p0, 8, 2)
	f.magic[d1], f.magic[d2] = magicExt4, magicNFS
	f.mounts = mountLine(20, "8:1", "ext4", "/data") + mountLine(21, "8:2", "nfs4", "/remote")
	f.stats = map[string]Stat{
		"a.mkv": {Dev: d1, Ino: 1, Nlink: 1, Size: 1, Regular: true},
		"b.mkv": {Dev: d1, Ino: 2, Nlink: 1, Size: 1, Regular: true},
	}
	p := f.prober()
	ia, err := p.PathInfo(a)
	if err != nil || !ia.Allowlisted || ia.FSType != "ext4" {
		t.Fatalf("PathInfo a = %+v %v", ia, err)
	}
	ib, _ := p.PathInfo(b)
	if !CouldBeDistinct(ia, ib) {
		t.Fatal("two ext4 inodes on one device could be distinct")
	}
	if CouldBeDistinct(ia, ia) {
		t.Fatal("one inode is never distinct")
	}
	f.stats["b.mkv"] = Stat{Dev: d2, Ino: 2, Nlink: 1, Size: 1, Regular: true}
	ib, _ = f.prober().PathInfo(b)
	if CouldBeDistinct(ia, ib) || ib.Allowlisted {
		t.Fatalf("nfs / another device: %+v", ib)
	}
}

// On Linux, a hard link is the same file; on every other platform nothing is ever Distinct.
func TestRealFiles(t *testing.T) {
	dir := t.TempDir()
	a, b, c := filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv"), filepath.Join(dir, "c.mkv")
	if err := os.WriteFile(a, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, b); err != nil {
		t.Skipf("hard links not supported: %v", err)
	}
	if err := os.WriteFile(c, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Default()
	if v, why := p.Compare(a, b); v != Same {
		t.Fatalf("hard link: %s (%s)", v, why)
	}
	v, why := p.Compare(a, c)
	if runtime.GOOS != "linux" {
		if v != Unknown {
			t.Fatalf("%s: two files = %s, want unknown", runtime.GOOS, v)
		}
		return
	}
	// On Linux the answer depends on the test directory's filesystem: distinct on an allowlisted
	// type, unknown elsewhere (tmpfs, overlay …) — never "same".
	info, _ := p.PathInfo(a)
	switch {
	case v == Same:
		t.Fatalf("two files reported as the same file (%s)", why)
	case info.Allowlisted && v != Distinct:
		t.Fatalf("%s: two files = %s (%s), want distinct", info.FSType, v, why)
	case !info.Allowlisted && v != Unknown:
		t.Fatalf("%s: two files = %s, want unknown", info.FSType, v)
	}
}
