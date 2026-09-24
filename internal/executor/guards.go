package executor

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Identity guards: a connection whose URL now points at another server (a different Plex server,
// another Radarr/Sonarr) answers the stored rating keys, media ids and file ids with unrelated
// items. Acting on them would delete the wrong media, so the executor confirms the identity of
// the Plex server before a group is re-verified and before every Plex delete, and what an *arr file
// id names when an *arr removal is planned and again right before DeleteFile.

// serverIdentityProblem confirms that media server sid is still the server Dupearr stored: its
// machineIdentifier must equal models.MediaServer.MachineIdentifier (a server stored without one
// is not checked). It returns a non-empty problem on a mismatch and err when the identity could
// not be read.
func (r *run) serverIdentityProblem(ctx context.Context, sid int64, c PlexClient) (string, error) {
	srv := r.servers[sid]
	want := strings.TrimSpace(srv.MachineIdentifier)
	if want == "" {
		return "", nil
	}
	id, err := c.Identity(ctx)
	if err != nil {
		return "", err
	}
	if id == nil {
		return "", errors.New("the server did not report its identity")
	}
	if got := strings.TrimSpace(id.MachineIdentifier); !strings.EqualFold(got, want) {
		return fmt.Sprintf("server identity changed: %s now answers as Plex server %q but Dupearr knows it as %q (was its URL changed to another server?); nothing was removed — check the media server's settings, then scan again",
			srv.Name, got, want), nil
	}
	return "", nil
}

// arrFileProblem confirms that the *arr file id of an *arr removal still names the version being
// removed. GET …/{moviefile|episodefile}/{id} must report:
//   - the same file: the *arr's path, mapped with the instance's path mappings, equals the
//     version's mapped local path (or both resolve to the same path); when the two paths cannot
//     both be mapped, the raw paths must be equal (the *arr and Plex see the same path). These are
//     the scanner's matching rules without its file-name fallback;
//   - the size of the version (as verified right before);
//   - the movie/series recorded by the scan.
//
// Otherwise the id names something else — the instance's URL was re-pointed at another *arr, the
// file was upgraded or re-imported — and DeleteFile would remove an unrelated file: a non-empty
// stale reason is returned (the removal is skipped and the group re-scanned). err is set when the
// *arr could not be asked.
func (r *run) arrFileProblem(ctx context.Context, at *arrTarget, v *models.MediaVersion, lv *verifiedVersion) (string, error) {
	info, name := at.info, at.inst.Name
	ref, err := at.client.File(ctx, info.FileID)
	switch {
	case errors.Is(err, arr.ErrNotFound):
		return fmt.Sprintf("%s no longer tracks file %d (it was upgraded, renamed or deleted since the last scan, or the instance's URL now points at another server)",
			name, info.FileID), nil
	case err != nil:
		return "", err
	case ref == nil:
		return "", fmt.Errorf("%s returned no data for file %d", name, info.FileID)
	}
	if info.ItemID > 0 && ref.ItemID != info.ItemID {
		return fmt.Sprintf("%s file %d now belongs to item %d instead of %d (was the instance's URL changed to another server?)",
			name, info.FileID, ref.ItemID, info.ItemID), nil
	}
	if disc.IsDiscPath(ref.Path) {
		// The per-file disc guard: the *arr file is a clip inside a disc structure.
		return fmt.Sprintf("%s file %d (%s) lies inside a full-disc backup; a disc is only ever removed as a whole", name, info.FileID, ref.Path), nil
	}
	if len(v.Parts) != 1 {
		return fmt.Sprintf("%s tracks single files but the version has %d parts", name, len(v.Parts)), nil
	}
	serverPath, size, local := v.Parts[0].Path, v.Parts[0].Size, ""
	if lv != nil && len(lv.parts) == 1 {
		serverPath, size, local = lv.parts[0].path, lv.parts[0].size, lv.parts[0].local
	}
	arrLocal, arrMapped := r.mapper.ToLocal(models.PathSourceArr, info.InstanceID, ref.Path)
	switch {
	case strings.TrimSpace(ref.Path) == "":
		return fmt.Sprintf("%s did not report the path of file %d", name, info.FileID), nil
	case arrMapped && local != "":
		if pathKey(arrLocal) != pathKey(local) && !sameResolved(arrLocal, local) {
			return fmt.Sprintf("%s file %d is now %s, not the version to remove (%s) (was the instance's URL changed, or the file replaced?)",
				name, info.FileID, ref.Path, serverPath), nil
		}
	case pathKey(ref.Path) != pathKey(serverPath):
		return fmt.Sprintf("%s file %d is %s, which is not the path of the version to remove (%s); if %s sees your media under other paths than Plex, add path mappings for both so the file can be confirmed",
			name, info.FileID, ref.Path, serverPath, name), nil
	}
	if ref.Size != size {
		return fmt.Sprintf("%s reports file %d (%s) as %d bytes, but the version to remove has %d", name, info.FileID, ref.Path, ref.Size, size), nil
	}
	return "", nil
}

// pathKey normalizes a path for equality tests (the scanner's rule): pathmap.Normalize, and
// case-folded for Windows-style paths (drive letter or UNC share), which are case-insensitive.
// POSIX paths keep their case.
func pathKey(p string) string {
	n := pathmap.Normalize(strings.TrimSpace(p))
	if n == "" {
		return ""
	}
	if strings.HasPrefix(n, "//") || (len(n) >= 2 && n[1] == ':' && ((n[0] >= 'a' && n[0] <= 'z') || (n[0] >= 'A' && n[0] <= 'Z'))) {
		return strings.ToLower(n)
	}
	return n
}

// notAllowedNow reports why a queued group may no longer be acted on although its stored decisions
// are unchanged: an exclusion created after the approval covers it (by key, title, library or
// path — engine.ExclusionReason), the library of a version to remove was disabled, or the group's
// profile now protects a version to remove (engine.ProtectionReason). Scans apply exclusions only
// when they rebuild the group, and the re-evaluation after a profile change runs in the
// background — both may come after the next queue run.
func (r *run) notAllowedNow(g *models.DuplicateGroup, targets []*target) string {
	if reason := engine.ExclusionReason(r.exclusions, g); reason != "" {
		return "it is excluded now (" + reason + "); remove the exclusion and scan again to act on it"
	}
	// The full-disc settings as they are now (docs/DECISIONS.md D9).
	if problem := discRemovalProblem(g, r.st, true); problem != "" {
		return problem
	}
	prof := r.profileFor(g)
	for _, t := range targets {
		if t.file == nil {
			continue // no longer part of the group: reported as stale by the caller
		}
		v := &t.file.Version
		if l, ok := r.libraries[v.LibraryID]; ok && !l.Enabled {
			return fmt.Sprintf("the library %q of %s is disabled; enable it and scan again to act on it", l.Title, describeVersion(v))
		}
		if reason := engine.ProtectionReason(prof, v, r.libraries); reason != "" {
			return fmt.Sprintf("the profile %q now protects %s (%s)", prof.Name, describeVersion(v), reason)
		}
	}
	return ""
}

// profileFor returns the decision profile that applies to a group now (the scanner's rule: the
// profile of the group's first library when it has one, else the default profile).
func (r *run) profileFor(g *models.DuplicateGroup) models.Profile {
	if len(g.LibraryIDs) > 0 {
		if l, ok := r.libraries[g.LibraryIDs[0]]; ok && l.ProfileID != nil {
			if p, ok := r.profiles[*l.ProfileID]; ok {
				return p
			}
		}
	}
	return r.defaultProfile
}

// keeperArrProblem re-reads the *arr file of every verified kept version the *arr tracks (enabled
// instances only): the id must still name the kept file — same item, same size and the same path
// (mapped, raw, or at least the same file name, since a kept file is never deleted through this
// id). A 404 or another file means the *arr now tracks another copy (it imported the loser, or
// upgraded the keeper): removing the loser through Plex or the filesystem would take the *arr's
// file behind its back, and it would download the title again. err is set when the *arr could not
// be asked.
func (r *run) keeperArrProblem(ctx context.Context, vr *verification) (string, error) {
	for _, k := range vr.keepers {
		info := k.file.Version.Arr
		if info == nil || info.FileID <= 0 {
			continue
		}
		inst, ok := r.arrs[info.InstanceID]
		if !ok || !inst.Enabled {
			continue
		}
		c := r.arrClient(inst)
		if c == nil {
			continue
		}
		kept := describeVersion(&k.file.Version)
		ref, err := c.File(ctx, info.FileID)
		if err == nil && ref != nil && k.file.Version.Disc != nil && !k.file.Version.Disc.IsImage() {
			// A kept disc the *arr tracks a clip of: the file must still lie inside that disc.
			if problem := r.keptDiscArrProblem(inst, info, ref, &k.file.Version); problem != "" {
				return problem, nil
			}
			continue
		}
		switch {
		case errors.Is(err, arr.ErrNotFound):
			return fmt.Sprintf("%s no longer tracks the kept file %s (file %d): it tracks another copy now, or upgraded it", inst.Name, kept, info.FileID), nil
		case err != nil:
			return "", err
		case ref == nil:
			return "", fmt.Errorf("%s returned no data for file %d", inst.Name, info.FileID)
		}
		if info.ItemID > 0 && ref.ItemID != info.ItemID {
			return fmt.Sprintf("%s file %d of the kept version now belongs to item %d instead of %d", inst.Name, info.FileID, ref.ItemID, info.ItemID), nil
		}
		serverPath, size, local := "", k.file.Version.TotalSize(), ""
		if len(k.parts) == 1 {
			serverPath, size, local = k.parts[0].path, k.parts[0].size, k.parts[0].local
		} else if len(k.file.Version.Parts) > 0 {
			serverPath = k.file.Version.Parts[0].Path
		}
		same := pathKey(ref.Path) == pathKey(serverPath)
		if arrLocal, mapped := r.mapper.ToLocal(models.PathSourceArr, info.InstanceID, ref.Path); !same && mapped && local != "" {
			same = pathKey(arrLocal) == pathKey(local) || sameResolved(arrLocal, local)
		}
		if !same && strings.TrimSpace(ref.Path) != "" {
			same = strings.EqualFold(path.Base(pathmap.Normalize(ref.Path)), path.Base(pathmap.Normalize(serverPath)))
		}
		switch {
		case !same:
			return fmt.Sprintf("%s file %d is now %s, not the kept version (%s)", inst.Name, info.FileID, ref.Path, kept), nil
		case ref.Size != size:
			return fmt.Sprintf("%s reports its file %d (%s) as %d bytes, but the kept version has %d", inst.Name, info.FileID, ref.Path, ref.Size, size), nil
		}
	}
	return "", nil
}
