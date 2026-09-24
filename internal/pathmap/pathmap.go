// Package pathmap translates file paths between the media server / *arr view of the filesystem
// ("remote" paths, e.g. /data/movies inside the Plex container, or D:\Movies on a Windows Plex)
// and Dupearr's own view ("local" paths, e.g. /mnt/user/movies), and stats local files.
//
// Matching is path-segment aware ("/data/movies" never matches "/data/movies2") and uses the
// longest matching prefix. Windows-style prefixes (drive letters, UNC shares) match
// case-insensitively.
package pathmap

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Mapper translates paths using a fixed set of path mappings. It is immutable and safe for
// concurrent use; build a new one when mappings change. A nil *Mapper maps nothing.
type Mapper struct {
	byRemote []entry // longest remote prefix first
	byLocal  []entry // longest local prefix first
}

type entry struct {
	sourceType string
	sourceID   int64

	remote string // Normalize(RemotePath), used for matching
	local  string // Normalize(LocalPath), used for matching

	remoteOut string // remote prefix as emitted by ToRemote (original drive-letter case)
	localOut  string // local prefix as emitted by ToLocal (original drive-letter case)

	remoteBackslash bool // RemotePath was written with backslashes (Windows server): ToRemote emits "\"
}

// New builds a Mapper. Mappings whose remote or local path is empty or not absolute are ignored.
func New(mappings []models.PathMapping) *Mapper {
	m := &Mapper{}
	for _, pm := range mappings {
		r, l := Normalize(pm.RemotePath), Normalize(pm.LocalPath)
		if !isAbs(r) || !isAbs(l) {
			continue
		}
		m.byRemote = append(m.byRemote, entry{
			sourceType:      pm.SourceType,
			sourceID:        pm.SourceID,
			remote:          r,
			local:           l,
			remoteOut:       keepDriveCase(pm.RemotePath, r),
			localOut:        keepDriveCase(pm.LocalPath, l),
			remoteBackslash: strings.Contains(pm.RemotePath, `\`) && !strings.Contains(pm.RemotePath, "/"),
		})
	}
	m.byLocal = append([]entry(nil), m.byRemote...)
	sort.SliceStable(m.byRemote, func(i, j int) bool { return len(m.byRemote[i].remote) > len(m.byRemote[j].remote) })
	sort.SliceStable(m.byLocal, func(i, j int) bool { return len(m.byLocal[i].local) > len(m.byLocal[j].local) })
	return m
}

// ToLocal translates a remote path using the longest matching RemotePath prefix for that source
// (path-segment aware: "/data/movies" must not match "/data/movies2"). ok=false when no mapping.
// The result uses the host OS path separator.
func (m *Mapper) ToLocal(sourceType string, sourceID int64, remote string) (local string, ok bool) {
	if m == nil {
		return "", false
	}
	p := Normalize(remote)
	if !isAbs(p) {
		return "", false
	}
	for _, e := range m.byRemote {
		if e.sourceType != sourceType || e.sourceID != sourceID {
			continue
		}
		if rest, ok := trimPrefix(p, e.remote); ok {
			return filepath.FromSlash(join(e.localOut, rest)), true
		}
	}
	return "", false
}

// ToRemote translates a local path back to the path the given source sees, using the longest
// matching LocalPath prefix. The result uses backslashes when the mapping's RemotePath did.
func (m *Mapper) ToRemote(sourceType string, sourceID int64, local string) (remote string, ok bool) {
	if m == nil {
		return "", false
	}
	p := Normalize(local)
	if !isAbs(p) {
		return "", false
	}
	for _, e := range m.byLocal {
		if e.sourceType != sourceType || e.sourceID != sourceID {
			continue
		}
		if rest, ok := trimPrefix(p, e.local); ok {
			r := join(e.remoteOut, rest)
			if e.remoteBackslash {
				r = strings.ReplaceAll(r, "/", `\`)
			}
			return r, true
		}
	}
	return "", false
}

// Normalize cleans a path for comparison (forward slashes, no trailing slash, Clean; Windows
// drive letters lower-cased; UNC preserved).
//
//	`C:\Movies\`          → "c:/Movies"
//	`\\NAS\share\a\..\b`  → "//NAS/share/b"
//	"/data//movies/"      → "/data/movies"
//	""                    → ""
func Normalize(p string) string {
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, `\`, "/")
	switch {
	case strings.HasPrefix(p, "//"):
		// UNC share (\\server\share\...): keep the leading double slash, clean the rest.
		rest := path.Clean("/" + strings.TrimLeft(p, "/"))
		if rest == "/" {
			return "/"
		}
		return "/" + rest
	case hasDrive(p):
		return strings.ToLower(p[:1]) + ":" + path.Clean("/"+p[2:])
	default:
		return path.Clean(p)
	}
}

// Stat returns size and hardlink count of a local file (linkCount 0 when unknown/unsupported).
func Stat(local string) (size int64, linkCount int, err error) {
	fi, err := os.Stat(local)
	if err != nil {
		return 0, 0, err
	}
	return fi.Size(), hardlinks(local, fi), nil
}

// hasDrive reports whether a slash-normalized path starts with a Windows drive ("c:" or "c:/…").
func hasDrive(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
		return false
	}
	return len(p) == 2 || p[2] == '/'
}

// isAbs reports whether a normalized path is absolute (POSIX root, drive or UNC).
func isAbs(p string) bool {
	return strings.HasPrefix(p, "/") || hasDrive(p)
}

// isWindowsStyle reports whether a normalized path is a drive or UNC path (case-insensitive).
func isWindowsStyle(p string) bool {
	return hasDrive(p) || strings.HasPrefix(p, "//")
}

// trimPrefix returns p relative to prefix (without a leading slash) when prefix is a whole-segment
// prefix of p. Both must be normalized.
func trimPrefix(p, prefix string) (rest string, ok bool) {
	if len(p) < len(prefix) {
		return "", false
	}
	head := p[:len(prefix)]
	if isWindowsStyle(prefix) {
		if !strings.EqualFold(head, prefix) {
			return "", false
		}
	} else if head != prefix {
		return "", false
	}
	rest = p[len(prefix):]
	switch {
	case rest == "":
		return "", true
	case strings.HasSuffix(prefix, "/"): // root prefix: "/" or "c:/"
		return rest, true
	case rest[0] == '/':
		return rest[1:], true
	default: // "/data/movies" vs "/data/movies2"
		return "", false
	}
}

// join appends a relative remainder to a normalized base.
func join(base, rest string) string {
	switch {
	case rest == "":
		return base
	case strings.HasSuffix(base, "/"):
		return base + rest
	default:
		return base + "/" + rest
	}
}

// keepDriveCase returns the normalized path with the drive letter as the user wrote it.
func keepDriveCase(raw, norm string) string {
	if hasDrive(norm) && raw != "" {
		return raw[:1] + norm[1:]
	}
	return norm
}
