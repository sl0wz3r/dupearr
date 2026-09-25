// Package fileid decides whether two local paths are the same file, different files, or cannot
// be told apart (docs/research/multi-server.md §3.4–§3.5, docs/DECISIONS.md D11). It is used
// when several Plex servers list files: removing a file that another server's item lists is only
// allowed while that item keeps another version proven to be a different file.
//
// The rules are asymmetric. Sameness may be assumed (equal device and inode); distinctness must
// be proven:
//
//   - Distinct needs both files open at the same time, fstat on both descriptors giving the same
//     st_dev and different st_ino (or different sizes), and a filesystem type on the allowlist on
//     both: the fstatfs magic and the mountinfo entry of that device must agree, and be ext4, XFS
//     or btrfs; ZFS is recognised by its mountinfo type. On these filesystems every file has a
//     unique inode number within the device and hard links are names of one inode, so different
//     inode numbers are different files whatever the link count.
//   - Everything else is Unknown: different devices (two mounts, a FUSE layer over its branch),
//     NFS, CIFS/SMB, 9p, virtiofs, overlay, FUSE filesystems (mergerfs, rclone …), a type that
//     cannot be determined, an unreadable file, and every platform other than Linux. Unknown pairs
//     are kept together, never split into keep and remove.
//   - Unraid's user shares (fuse.shfs) are NOT on the allowlist until the live checks of Phase 0
//     (docs/research/multi-server.md §7.1 Q2) have shown how shfs reports device and inode numbers
//     and link counts: until then every pair on a user share is Unknown (fail-closed).
//
// Scan-time numbers (PathInfo) only find files that are or may be the same and rule out what can
// never be proven (CouldBeDistinct); proof happens only in Compare, right before a removal, on
// descriptors open at the same time (inode numbers of FUSE filesystems may change during uptime).
package fileid

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Verdict is the answer of Compare.
type Verdict int

const (
	// Unknown: the two paths could not be proven to be different files, or to be one file.
	Unknown Verdict = iota
	// Same: the two paths are one file (equal device and inode numbers).
	Same
	// Distinct: the two paths are proven to be different files.
	Distinct
)

func (v Verdict) String() string {
	switch v {
	case Same:
		return "same"
	case Distinct:
		return "distinct"
	}
	return "unknown"
}

// Filesystem magic numbers (statfs f_type) of the types the allowlist and the reasons name.
const (
	magicExt4     = 0xEF53 // ext2, ext3 and ext4 share it; mountinfo tells them apart
	magicXFS      = 0x58465342
	magicBtrfs    = 0x9123683E
	magicZFS      = 0x2FC12FC1
	magicNFS      = 0x6969
	magicCIFS     = 0xFF534D42
	magicSMB2     = 0xFE534D42
	magicFUSE     = 0x65735546 // every FUSE filesystem, virtiofs included
	magicOverlay  = 0x794C7630
	magic9P       = 0x01021997
	magicTmpfs    = 0x01021994
	magicSquashFS = 0x73717368
)

// magicNames maps f_type to the mountinfo type it must agree with ("" = no fixed type).
var magicNames = map[int64]string{
	magicExt4: "ext4", magicXFS: "xfs", magicBtrfs: "btrfs", magicZFS: "zfs", magicNFS: "nfs",
	magicCIFS: "cifs", magicSMB2: "smb2", magicFUSE: "fuse", magicOverlay: "overlay", magic9P: "9p",
	magicTmpfs: "tmpfs", magicSquashFS: "squashfs",
}

// allowlisted is the set of mountinfo types whose inode numbers identify files (§3.5). fuse.shfs
// (Unraid user shares) is deliberately absent until Phase 0 Q2 (package comment); so are nfs, nfs4,
// cifs, smb3, 9p, virtiofs, overlay and every other FUSE type.
var allowlisted = map[string]bool{"ext4": true, "xfs": true, "btrfs": true, "zfs": true}

// ShfsAllowlisted reports whether Unraid's fuse.shfs is on the allowlist. It is a constant false
// until the Phase 0 live checks (docs/research/multi-server.md §7.1 Q2); it exists so the reason
// messages and the tests can name the fail-closed decision.
const ShfsAllowlisted = false

// Stat is what fstat/stat report about one file.
type Stat struct {
	Dev, Ino, Nlink uint64
	Size            int64
	Regular         bool
}

// Info is the identity of one local file as read by PathInfo.
type Info struct {
	Dev, Ino, Nlink uint64
	Size            int64
	// FSType is the filesystem type of the file's device ("" when it could not be determined):
	// the mountinfo type (fuse.<subtype> for FUSE filesystems).
	FSType string
	// Allowlisted reports a filesystem type whose inode numbers identify files.
	Allowlisted bool
}

// Prober reads file identities. The zero value is not usable; use Default or New. A Prober caches
// the filesystem type per device; create one per scan or queue run (Default does), so a remount
// is noticed by the next run.
type Prober struct {
	h      hooks
	assume string // Hooks.AssumeType

	mu    sync.Mutex
	types map[uint64]fsType // by st_dev
	mi    []mountEntry
	miErr error
	miSet bool
}

// hooks are the system calls a Prober makes (replaced in tests).
type hooks struct {
	// supported is false where identity cannot be proven (every platform but Linux): Compare then
	// always answers Unknown.
	supported bool
	open      func(path string) (*os.File, error)
	fstat     func(f *os.File) (Stat, error)
	stat      func(path string) (Stat, error)
	fstatfs   func(f *os.File) (int64, error) // f_type
	statfs    func(path string) (int64, error)
	mountinfo func() ([]byte, error) // /proc/self/mountinfo
	// major/minor split a device number the way the kernel does (mountinfo's "major:minor").
	major, minor func(dev uint64) uint64
}

// Hooks lets tests replace the system calls of a Prober (see New).
type Hooks struct {
	FStat     func(f *os.File) (Stat, error)
	Stat      func(path string) (Stat, error)
	FStatfs   func(f *os.File) (int64, error)
	Statfs    func(path string) (int64, error)
	Mountinfo func() ([]byte, error)
	// Supported overrides the platform: true makes Compare decide on any platform (tests).
	Supported bool
	// AssumeType, when set, reports every device as this mountinfo type without reading the mount
	// table or f_type (tests that run on real files: "ext4" declares the test tree allowlisted,
	// "fuse.shfs" or "nfs4" declares it not).
	AssumeType string
}

// Default returns a Prober using the real system calls.
func Default() *Prober { return &Prober{h: defaultHooks(), types: map[uint64]fsType{}} }

// New returns a Prober whose system calls are replaced by the non-nil hooks (tests).
func New(h Hooks) *Prober {
	p := Default()
	p.h.supported = h.Supported
	if h.FStat != nil {
		p.h.fstat = h.FStat
	}
	if h.Stat != nil {
		p.h.stat = h.Stat
	}
	if h.FStatfs != nil {
		p.h.fstatfs = h.FStatfs
	}
	if h.Statfs != nil {
		p.h.statfs = h.Statfs
	}
	if h.Mountinfo != nil {
		p.h.mountinfo = h.Mountinfo
	}
	p.assume = h.AssumeType
	return p
}

// fsType is the determined filesystem type of one device.
type fsType struct {
	name        string // mountinfo type ("" = unknown)
	allowlisted bool
	why         string // why the type is unknown or not allowlisted
}

// PathInfo stats path (following symbolic links) and determines its filesystem type (cached per
// device). The numbers are a snapshot: they find same or possibly-same files, they never prove a
// difference (use Compare for that).
func (p *Prober) PathInfo(path string) (Info, error) {
	if p == nil {
		return Info{}, errors.New("no file identity prober")
	}
	st, err := p.h.stat(path)
	if err != nil {
		return Info{}, err
	}
	if !st.Regular {
		return Info{}, fmt.Errorf("%s is not a regular file", path)
	}
	info := Info{Dev: st.Dev, Ino: st.Ino, Nlink: st.Nlink, Size: st.Size}
	ft := p.typeOf(st.Dev, func() (int64, error) { return p.h.statfs(path) })
	info.FSType, info.Allowlisted = ft.name, ft.allowlisted && p.h.supported
	return info, nil
}

// CouldBeDistinct reports whether two files read by PathInfo could ever be proven different
// files: the same device, different inode numbers and an allowlisted filesystem type on both.
// false means they are, or may be, one file (keep them together).
func CouldBeDistinct(a, b Info) bool {
	return a.Allowlisted && b.Allowlisted && a.Dev == b.Dev && a.Ino != 0 && b.Ino != 0 && a.Ino != b.Ino
}

// Compare opens both regular files, reads fstat on both descriptors while both are open and the
// filesystem type of each descriptor, and answers Same (one file), Distinct (proven different
// files) or Unknown, with a human reason for anything but Distinct.
func (p *Prober) Compare(a, b string) (Verdict, string) {
	if p == nil {
		return Unknown, "no file identity prober"
	}
	fa, err := p.h.open(a)
	if err != nil {
		return Unknown, fmt.Sprintf("cannot open %s (%v)", a, err)
	}
	defer fa.Close()
	fb, err := p.h.open(b)
	if err != nil {
		return Unknown, fmt.Sprintf("cannot open %s (%v)", b, err)
	}
	defer fb.Close()
	sa, err := p.h.fstat(fa)
	if err != nil {
		return Unknown, fmt.Sprintf("cannot read %s (%v)", a, err)
	}
	sb, err := p.h.fstat(fb)
	if err != nil {
		return Unknown, fmt.Sprintf("cannot read %s (%v)", b, err)
	}
	if !sa.Regular || !sb.Regular {
		return Unknown, "not both regular files"
	}
	if sa.Dev == sb.Dev && sa.Ino != 0 && sa.Ino == sb.Ino {
		return Same, "the same file (equal device and inode numbers)"
	}
	if !p.h.supported {
		return Unknown, "different files can only be proven on Linux"
	}
	if sa.Dev != sb.Dev {
		return Unknown, "they are on different devices (two mounts, or a layer over another filesystem, can show one file twice)"
	}
	ta := p.typeOf(sa.Dev, func() (int64, error) { return p.h.fstatfs(fa) })
	tb := p.typeOf(sb.Dev, func() (int64, error) { return p.h.fstatfs(fb) })
	for _, t := range []fsType{ta, tb} {
		if !t.allowlisted {
			return Unknown, t.why
		}
	}
	if sa.Ino != 0 && sb.Ino != 0 && sa.Ino != sb.Ino {
		return Distinct, ""
	}
	if sa.Size != sb.Size {
		return Distinct, ""
	}
	return Unknown, "their inode numbers could not be read"
}

// typeOf determines (and caches) the filesystem type of device dev. magic reads its f_type.
func (p *Prober) typeOf(dev uint64, magic func() (int64, error)) fsType {
	p.mu.Lock()
	if t, ok := p.types[dev]; ok {
		p.mu.Unlock()
		return t
	}
	p.mu.Unlock()
	t := p.determine(dev, magic)
	p.mu.Lock()
	p.types[dev] = t
	p.mu.Unlock()
	return t
}

func (p *Prober) determine(dev uint64, magic func() (int64, error)) fsType {
	if p.assume != "" {
		t := fsType{name: p.assume, allowlisted: allowlisted[p.assume]}
		if !t.allowlisted {
			t.why = notAllowlistedReason(p.assume)
		}
		return t
	}
	entries, err := p.mountEntries()
	if err != nil {
		return fsType{why: fmt.Sprintf("the filesystem type could not be read (%v)", err)}
	}
	id := fmt.Sprintf("%d:%d", p.h.major(dev), p.h.minor(dev))
	name := ""
	for _, e := range entries {
		if e.dev != id {
			continue
		}
		if name != "" && name != e.typ {
			// Two mounts of one device with different types: nothing can be relied on.
			return fsType{why: fmt.Sprintf("the mount table reports device %s as both %s and %s", id, name, e.typ)}
		}
		name = e.typ
	}
	if name == "" {
		return fsType{why: fmt.Sprintf("the mount table has no entry for device %s", id)}
	}
	t := fsType{name: name}
	m, err := magic()
	if err != nil {
		t.why = fmt.Sprintf("the filesystem type of %s could not be confirmed (%v)", name, err)
		return t
	}
	if want, known := magicNames[m]; known && !agrees(want, name) {
		t.why = notAllowlistedReason(name)
		if allowlisted[name] {
			t.why = fmt.Sprintf("the filesystem reports type %s but the mount table %s", want, name)
		}
		return t
	}
	switch {
	case name == "zfs":
		// ZFS is recognised by its mountinfo type (its f_type is not a fixed kernel magic).
		t.allowlisted = !knownOther(m, "zfs")
	case allowlisted[name]:
		want, known := magicNames[m]
		t.allowlisted = known && want == name
	}
	if !t.allowlisted {
		t.why = notAllowlistedReason(name)
	}
	return t
}

// agrees reports whether the type an f_type magic names fits the mount table's type (one magic
// covers a family: ext2/3/4, nfs/nfs4, the SMB variants, every FUSE filesystem).
func agrees(magicName, mountType string) bool {
	base, _, _ := strings.Cut(mountType, ".")
	switch magicName {
	case "ext4":
		return base == "ext2" || base == "ext3" || base == "ext4"
	case "nfs":
		return base == "nfs" || base == "nfs4"
	case "cifs", "smb2":
		return base == "cifs" || strings.HasPrefix(base, "smb")
	case "fuse":
		return base == "fuse" || base == "fuseblk" || base == "virtiofs"
	}
	return magicName == base
}

// knownOther reports a magic that names a type other than name.
func knownOther(m int64, name string) bool {
	want, known := magicNames[m]
	return known && !agrees(want, name)
}

// notAllowlistedReason explains why a filesystem type cannot prove that two files differ.
func notAllowlistedReason(name string) string {
	switch {
	case name == "fuse.shfs":
		return "they are on an Unraid user share (fuse.shfs), where Dupearr cannot yet prove that two paths are different files (use a disk share path, or wait for the verified shfs support)"
	case strings.HasPrefix(name, "nfs"), name == "cifs", strings.HasPrefix(name, "smb"), name == "9p":
		return "they are on a network filesystem (" + name + "), whose inode numbers do not prove that two paths are different files"
	case name == "fuse", strings.HasPrefix(name, "fuse."), name == "virtiofs":
		return "they are on a FUSE filesystem (" + name + "), whose inode numbers do not prove that two paths are different files"
	case name == "overlay":
		return "they are on an overlay filesystem, whose inode numbers do not prove that two paths are different files"
	}
	return "the filesystem type " + name + " cannot prove that two paths are different files"
}

// mountEntry is one line of /proc/self/mountinfo: the device and the filesystem type.
type mountEntry struct {
	dev string // "major:minor"
	typ string // fstype, e.g. ext4 or fuse.shfs
}

// mountEntries reads (once per Prober) and parses the mount table.
func (p *Prober) mountEntries() ([]mountEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.miSet {
		p.miSet = true
		data, err := p.h.mountinfo()
		if err != nil {
			p.miErr = err
		} else {
			p.mi = ParseMountinfo(data)
		}
	}
	return p.mi, p.miErr
}

// ParseMountinfo parses /proc/self/mountinfo (proc(5)): "ID PARENT MAJOR:MINOR ROOT MOUNTPOINT
// OPTIONS [OPTIONAL…] - FSTYPE SOURCE SUPEROPTIONS". Paths are octal-escaped (\040 for a space),
// so fields never contain raw spaces. Malformed lines are skipped (a device without a usable
// entry has an unknown type).
func ParseMountinfo(data []byte) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(f) {
			continue
		}
		dev := f[2]
		maj, min, ok := strings.Cut(dev, ":")
		if !ok {
			continue
		}
		if _, err := strconv.ParseUint(maj, 10, 32); err != nil {
			continue
		}
		if _, err := strconv.ParseUint(min, 10, 32); err != nil {
			continue
		}
		out = append(out, mountEntry{dev: dev, typ: f[sep+1]})
	}
	return out
}
