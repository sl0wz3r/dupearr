package executor

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Read-only media servers (Jellyfin; docs/DECISIONS.md D12, docs/research/jellyfin-emby.md §5.3.4).
// Dupearr never removes anything through such a server (its client cannot: it has no
// VersionDeleter), and the server never says whether a file exists. A group with a version of one:
//
//   - is approved only by a person, one group at a time (auto mode and bulk approvals never take
//     it), and only while every version of it — kept and removed — has a server path mapping for
//     every part and none is report-only;
//   - is acted on only while the server is stored with its identity (S23), still answers with it,
//     its removal gate reports no problem (path substitutions, a credential that is not an
//     administrator: S12/S14) and every version is still mapped; the kept copy is confirmed on disk
//     only (checkPlexVersion), every part of it;
//   - is removed only non-permanently: through the *arr into its recycle bin, or by the filesystem
//     method into Dupearr's bin; a tracked file whose *arr deletes permanently is not removed at all
//     (never by the filesystem method instead);
//   - is reported to the server afterwards (mediaserver.ChangeNotifier, one notification per
//     group): its library monitor then drops the removed copy, the only way it can.

// readOnlyVersion reports a version of a read-only media server (by its version key).
func readOnlyVersion(v *models.MediaVersion) bool {
	k, ok := models.KindOfVersionKey(v.Key)
	return ok && k.ReadOnly()
}

// readOnlyGroup reports a group with a version of a read-only media server, and the label of its
// kind ("Jellyfin").
func readOnlyGroup(g *models.DuplicateGroup) (bool, string) {
	for i := range g.Files {
		if k, ok := models.KindOfVersionKey(g.Files[i].Version.Key); ok && k.ReadOnly() {
			return true, k.Label()
		}
	}
	return false, ""
}

// ReadOnlyGroup reports whether a group has a version of a read-only media server (Jellyfin): the
// API refuses to approve such a group in bulk (docs/DECISIONS.md D12).
func ReadOnlyGroup(g *models.DuplicateGroup) bool {
	ro, _ := readOnlyGroup(g)
	return ro
}

// readOnlyApprovalProblem explains why a group with a version of a read-only server may not be
// approved ("" when it may, and always for other groups): only a person's approval, only with a
// path mapping for every part of every version, never a report-only version.
func readOnlyApprovalProblem(g *models.DuplicateGroup, mappings []models.PathMapping, manual bool) string {
	ro, label := readOnlyGroup(g)
	if !ro {
		return ""
	}
	if !manual {
		return label + " copies are only removed when a person approves them"
	}
	mapper := pathmap.New(mappings)
	for i := range g.Files {
		v := &g.Files[i].Version
		if len(v.ReportOnly) > 0 {
			return "the duplicate is report-only: " + strings.Join(v.ReportOnly, "; ")
		}
		if p := unmappedPart(mapper, serverOf(g, v), v); p != "" {
			return fmt.Sprintf("no path mapping covers %s: every copy of a %s duplicate needs one, because %s never reports whether a file exists (Settings → Media Management)",
				p, label, label)
		}
	}
	return ""
}

// unmappedPart returns the first part of v no server path mapping covers ("" when all are mapped).
func unmappedPart(mapper *pathmap.Mapper, sid int64, v *models.MediaVersion) string {
	for _, p := range v.Parts {
		if _, ok := mapper.ToLocal(models.PathSourceServer, sid, p.Path); !ok {
			return p.Path
		}
	}
	return ""
}

// readOnlyRunProblem re-checks, right before a group with versions of read-only servers is
// re-verified, what its removals rely on: problem skips the group (rescan: a scan can fix it),
// wait defers it (failure: a read error).
func (r *run) readOnlyRunProblem(g *models.DuplicateGroup) (problem string, rescan bool, wait string, failure bool) {
	servers := map[int64]bool{}
	for i := range g.Files {
		v := &g.Files[i].Version
		sid := serverOf(g, v)
		if !r.servers[sid].Kind.ReadOnly() && !readOnlyVersion(v) {
			continue
		}
		servers[sid] = true
		if p := unmappedPart(r.mapper, sid, v); p != "" {
			return fmt.Sprintf("no path mapping covers %s any more, and %s never reports whether a file exists", p, r.servers[sid].Kind.Label()), true, "", false
		}
	}
	for _, sid := range sortedKeys(servers) {
		srv := r.servers[sid]
		if strings.TrimSpace(srv.MachineIdentifier) == "" {
			// Its identity cannot be confirmed: another server at its URL could answer (S23).
			return fmt.Sprintf("the media server %s is stored without its identity, so its answers cannot be trusted; test and save it (Settings → Media Servers)", srv.Name), false, "", false
		}
		c, reason := r.serverClient(sid)
		if c == nil {
			return "", false, "cannot check " + srv.Name + ": " + reason, true
		}
		gate, ok := c.(mediaserver.RemovalGate)
		if !ok {
			continue
		}
		switch why, err := gate.RemovalProblem(r.ctx); {
		case err != nil:
			return "", false, fmt.Sprintf("could not read whether removals from %s are safe (%v)", srv.Name, err), true
		case why != "":
			return fmt.Sprintf("removals from %s are disabled: %s", srv.Name, why), true, "", false
		}
	}
	return "", false, "", false
}

// readOnlyMethodRule applies the recycle-bin rule to a method chosen for a version of a read-only
// server: a permanent removal is never accepted. stop is set for the *arr method: a tracked file is
// only removed through its *arr, never by the filesystem method instead.
func readOnlyMethodRule(label string, c *methodChoice) (why string, stop bool) {
	if c == nil || !c.permanent {
		return "", false
	}
	base := label + " copies are only removed into a recycle bin (Radarr/Sonarr's or Dupearr's)"
	if c.method == models.MethodArr {
		return fmt.Sprintf("%s; %s deletes permanently (turn on its recycle bin: Settings → Media Management)", base, c.target), true
	}
	return base + "; Dupearr's recycle bin is not set (Settings → Media Management)", false
}

// notifyServers reports the files the group's removals took to every involved server that has a
// change notification (Jellyfin, mediaserver.ChangeNotifier), one notification per server and
// library, after confirming the server's identity (S23). It is the only way such a server drops the
// removed copy (research §3.10); failures are only noted on the removals.
func (r *run) notifyServers(ctx context.Context, g *models.DuplicateGroup, removed []*outcome) {
	type batch struct {
		paths   []string
		actions []*models.Action
	}
	byServer := map[int64]map[string]*batch{}
	for _, oc := range removed {
		v := &oc.t.file.Version
		sid := serverOf(g, v)
		if byServer[sid] == nil {
			byServer[sid] = map[string]*batch{}
		}
		b := byServer[sid][v.SectionKey]
		if b == nil {
			b = &batch{}
			byServer[sid][v.SectionKey] = b
		}
		for _, p := range partPaths(v) {
			if !slices.Contains(b.paths, p) {
				b.paths = append(b.paths, p)
			}
		}
		b.actions = append(b.actions, oc.t.a)
	}
	for _, sid := range sortedKeys(byServer) {
		c, _ := r.serverClient(sid)
		n, ok := c.(mediaserver.ChangeNotifier)
		if c == nil || !ok {
			continue
		}
		srv := r.servers[sid]
		label := srv.Kind.Label()
		note := func(b *batch, s string) {
			for _, a := range b.actions {
				r.appendNote(a, s)
			}
		}
		sections := make([]string, 0, len(byServer[sid]))
		for k := range byServer[sid] {
			sections = append(sections, k)
		}
		slices.Sort(sections)
		problem, err := r.serverIdentityProblem(ctx, sid, c)
		if strings.TrimSpace(srv.MachineIdentifier) == "" || problem != "" || err != nil {
			for _, k := range sections {
				note(byServer[sid][k], fmt.Sprintf("%s was not told about the removal (its identity could not be confirmed); scan its library to drop the removed copy", label))
			}
			continue
		}
		for _, k := range sections {
			b := byServer[sid][k]
			if err := n.NotifyChanged(ctx, k, b.paths); err != nil {
				r.s.d.Log.Warn("Could not report a removal to the media server", "server", srv.Name, "group", g.ID, "error", err)
				note(b, fmt.Sprintf("%s could not be told about the removal (%v); scan its library to drop the removed copy", label, err))
				continue
			}
			note(b, fmt.Sprintf("reported the removed file to %s", label))
		}
	}
}
