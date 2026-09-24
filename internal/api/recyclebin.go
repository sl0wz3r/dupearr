package api

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Recycle bin placement (PUT /config/settings).
//
// Files in Dupearr's recycle bin are permanently deleted after recycleBinCleanupDays, so the bin
// must never hold anything but recycled files. On top of the basic checks of validateSettings
// (absolute, not a filesystem root), the bin must not:
//
//   - be or contain the local folder of any path mapping (media would be purged by the cleanup);
//   - be or contain a library folder (a library location mapped to its local path; a location
//     without a mapping is compared as is, in case Dupearr sees the same paths as Plex);
//   - lie inside a library folder, unless the part below the library folder is hidden (a
//     ".dupearr-recycle" folder): Plex skips hidden folders and the executor also writes a
//     ".plexignore" into the bin, while a visible folder would be picked up by Plex and the *arrs
//     (docs/DECISIONS.md D6 — "outside Plex library roots or ignored by Plex");
//   - be, contain or lie inside Dupearr's data folder (config.xml, the database, backups, logs):
//     the cleanup would delete them, and a bin at <data>/.restore would be taken for a staged
//     restore at the next start (r2-data-files#2).
//
// Paths are compared lexically and with symlinks resolved (the longest existing prefix), and case
// insensitively on case-insensitive platforms (erring towards rejecting).

// recycleBinPlacementProblems checks bin (normalised, absolute, not a root) against the path
// mappings, the library folders and the data directory.
func (s *Server) recycleBinPlacementProblems(ctx context.Context, bin string) ([]config.ValidationError, error) {
	if bin == "" {
		return nil, nil
	}
	mappings, err := s.d.Store.PathMappings().List(ctx)
	if err != nil {
		return nil, err
	}
	libs, err := s.d.Store.Libraries().List(ctx)
	if err != nil {
		return nil, err
	}
	var errs []config.ValidationError
	add := func(format string, args ...any) {
		e := invalid("recycleBinPath", format, args...)
		errs = appendUnique(errs, e)
	}
	bins := pathForms(bin)

	for _, m := range mappings {
		local := strings.TrimSpace(m.LocalPath)
		if local == "" || !filepath.IsAbs(local) {
			continue
		}
		if anyWithin(pathForms(local), bins) {
			add("Must not be or contain the media folder %s (path mapping): the recycle bin cleanup permanently deletes what is inside. Use a dedicated folder such as /data/.dupearr-recycle", filepath.Clean(local))
		}
	}

	mapper := pathmap.New(mappings)
	for _, l := range libs {
		for _, loc := range l.Locations {
			root, ok := mapper.ToLocal(models.PathSourceServer, l.ServerID, loc)
			if !ok {
				root = strings.TrimSpace(loc)
			}
			if root == "" || !filepath.IsAbs(root) {
				continue
			}
			roots := pathForms(root)
			switch {
			case anyWithin(roots, bins):
				add("Must not be or contain the library folder %s (library %q): the recycle bin cleanup permanently deletes what is inside", filepath.Clean(root), l.Title)
			case anyWithinVisible(bins, roots):
				add("Must not be inside the library folder %s (library %q): Plex and the *arrs would pick up removed files. Use a folder outside your libraries or a hidden one such as %s",
					filepath.Clean(root), l.Title, filepath.Join(filepath.Clean(root), ".dupearr-recycle"))
			}
		}
	}

	if s.d.Config != nil {
		if dataDir := s.d.Config.DataDir(); dataDir != "" {
			data := pathForms(dataDir)
			if anyWithin(data, bins) || anyWithin(bins, data) {
				add("Must not be, contain or lie inside Dupearr's data folder %s: use a folder outside it, such as /data/.dupearr-recycle", filepath.Clean(dataDir))
			}
		}
	}
	return errs, nil
}

// pathForms returns the cleaned path and, when it differs, its form with symlinks resolved in its
// longest existing prefix.
func pathForms(p string) []string {
	p = filepath.Clean(p)
	out := []string{p}
	if r := resolveExistingPrefix(p); r != p {
		out = append(out, r)
	}
	return out
}

// resolveExistingPrefix resolves symlinks in the longest existing prefix of p and appends the
// rest unchanged (it does not exist yet).
func resolveExistingPrefix(p string) string {
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// relBelow returns the path of p relative to root when p equals root (".") or lies below it.
func relBelow(p, root string) (string, bool) {
	if runtime.GOOS != "linux" {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// anyWithin reports whether any form of p equals or lies below any form of root.
func anyWithin(ps, roots []string) bool {
	for _, p := range ps {
		for _, r := range roots {
			if _, ok := relBelow(p, r); ok {
				return true
			}
		}
	}
	return false
}

// anyWithinVisible reports whether a form of p lies strictly below a form of root through
// visible folders only (no path segment below root starts with ".").
func anyWithinVisible(ps, roots []string) bool {
	for _, p := range ps {
		for _, r := range roots {
			rel, ok := relBelow(p, r)
			if !ok || rel == "." {
				continue
			}
			hidden := false
			for _, seg := range strings.Split(rel, string(filepath.Separator)) {
				if strings.HasPrefix(seg, ".") {
					hidden = true
					break
				}
			}
			if !hidden {
				return true
			}
		}
	}
	return false
}
