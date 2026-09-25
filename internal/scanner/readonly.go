package scanner

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Read-only media servers (Jellyfin; docs/DECISIONS.md D12, docs/research/jellyfin-emby.md §5.3).
// Dupearr never removes through such a server, and the server never reports whether a file
// exists, so the scan applies the local truth and marks what it cannot confirm:
//
//   - a part whose mapped local file is gone while its folder is there is missing (a "ghost" the
//     server still lists, S6/S28): the version is left out like a Plex file Plex reports missing,
//     and a group is resolved by the local files, never by the server's listing;
//   - a part without a path mapping cannot be confirmed on disk, and a server whose removal gate
//     reports a problem (path substitutions, a credential that is not an administrator, S12/S14)
//     or cannot be read makes every version report-only (MediaVersion.ReportOnly): the group is
//     shown and never acted on;
//   - a local .strm shortcut the server lists (never a version, S19) is read through the path
//     mapping, and the file it points to records the shortcut (MediaPart.ShortcutOf), so it is
//     never removed; a shortcut that cannot be read leaves the library's shared-file index
//     incomplete (review), and with several servers the server counts as unread.

// maxShortcutSize bounds what is read of a .strm (a path or a URL on one line).
const maxShortcutSize = 4 << 10

// gateResult is a read-only server's removal gate as read once in a run.
type gateResult struct {
	once   sync.Once
	reason string
	err    error
}

// removalGate reads (once per run) whether files the server lists may be removed
// (mediaserver.RemovalGate): "" when they may, a reason when not, an error when that cannot be told.
// A client without the capability has nothing to report.
func (p *pipeline) removalGate(srv models.MediaServer) (string, error) {
	p.gatesMu.Lock()
	g := p.gates[srv.ID]
	if g == nil {
		g = &gateResult{}
		p.gates[srv.ID] = g
	}
	p.gatesMu.Unlock()
	g.once.Do(func() {
		client, err := p.serverClient(srv)
		if err != nil {
			g.err = err
			return
		}
		gate, ok := client.(mediaserver.RemovalGate)
		if !ok {
			return
		}
		g.reason, g.err = gate.RemovalProblem(p.ctx)
	})
	return g.reason, g.err
}

// gateReason is the report-only reason of a read-only server's gate ("" when removals may go ahead).
func (p *pipeline) gateReason(srv models.MediaServer) string {
	reason, err := p.removalGate(srv)
	switch {
	case err != nil:
		return fmt.Sprintf("could not read whether removals from %s are safe (%s)", srv.Name, upstreamerr.Message(err))
	case reason != "":
		return fmt.Sprintf("removals from %s are disabled: %s", srv.Name, reason)
	}
	return ""
}

// decorateReadOnly applies the read-only rules to one part of a version of srv (local is the
// mapped local path, "" when no mapping covers the part): a missing local file in an existing
// folder is missing; an unmapped part makes the version report-only.
func decorateReadOnly(srv models.MediaServer, v *models.MediaVersion, part *models.MediaPart, local string, mapped bool) (gone bool) {
	if !mapped {
		v.ReportOnly = appendOnce(v.ReportOnly, fmt.Sprintf("no path mapping covers %s (%s never reports whether a file exists)", part.Path, srv.Kind.Label()))
		return false
	}
	if _, err := os.Lstat(local); errors.Is(err, fs.ErrNotExist) && dirExists(filepath.Dir(local)) {
		f := false
		part.Exists = &f
		return true
	}
	return false
}

// appendOnce appends s unless it is there already.
func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// indexShortcuts reads the .strm shortcuts of the listings and indexes the files they point to
// (by the shortcut's file name). A shortcut that cannot be read (unmapped, missing, not a regular
// file, too large, not an absolute path or a URL) leaves its library's shared-file index
// incomplete, and with several servers its server counts as unread.
func (p *pipeline) indexShortcuts(listings []libListing) {
	for _, l := range listings {
		for _, ref := range l.refs {
			for _, sc := range ref.Shortcuts {
				target, err := p.readShortcut(l.server, sc.File)
				switch {
				case err != nil:
					why := fmt.Sprintf("the .strm shortcut %s could not be read (%v)", sc.File, err)
					p.mu.Lock()
					if _, ok := p.shortcutProblems[l.lib.ID]; !ok {
						p.shortcutProblems[l.lib.ID] = why
					}
					p.mu.Unlock()
					if p.cfg.multi {
						p.markUnread(l.server.ID, why)
					}
				case target != "":
					p.index.addShortcut(l.server.ID, target, path.Base(strings.ReplaceAll(sc.File, `\`, "/")))
				}
			}
		}
	}
}

// readShortcut reads the target of a .strm the server lists at file: "" for a URL (a remote
// stream, no local file), else the absolute path as the server sees it.
func (p *pipeline) readShortcut(srv models.MediaServer, file string) (string, error) {
	local, ok := p.cfg.mapper.ToLocal(models.PathSourceServer, srv.ID, file)
	if !ok {
		return "", errors.New("no path mapping covers it")
	}
	fi, err := os.Lstat(local)
	switch {
	case err != nil:
		return "", err
	case !fi.Mode().IsRegular():
		return "", errors.New("it is not a regular file")
	case fi.Size() > maxShortcutSize:
		return "", fmt.Errorf("it is larger than %d bytes", maxShortcutSize)
	}
	f, err := os.Open(local)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxShortcutSize+1))
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case strings.Contains(line, "://"):
			return "", nil // a URL: nothing local to protect
		case strings.HasPrefix(line, "/"), strings.HasPrefix(line, `\\`),
			len(line) >= 3 && line[1] == ':' && (line[2] == '\\' || line[2] == '/'):
			return line, nil
		}
		return "", errors.New("its first line is not an absolute path")
	}
	return "", errors.New("it is empty")
}
