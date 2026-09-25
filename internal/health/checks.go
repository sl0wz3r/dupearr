package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const (
	// maxErrLen caps an error quoted in a health message.
	maxErrLen = 300
	// maxListed caps how many paths a single message lists.
	maxListed = 5
	// plexIgnoreName is Plex's per-folder ignore file.
	plexIgnoreName = ".plexignore"
)

// snapshot holds the inputs shared by the checks of one Run. Nil/false fields mean "could not be
// read" (DatabaseCheck reports database problems; the other checks then skip).
type snapshot struct {
	settings  *models.Settings
	cfg       *config.Config
	servers   []models.MediaServer
	serversOK bool
	arrs      []models.ArrInstance
	arrsOK    bool
	libraries []models.Library
	libsOK    bool
	mappings  []models.PathMapping
	mapsOK    bool
	tautullis []models.TautulliInstance
	tautOK    bool
	profiles  []models.Profile
	profOK    bool
}

// load reads the shared inputs of a Run.
func (c *Checker) load(ctx context.Context) *snapshot {
	s := &snapshot{}
	if c.d.Config != nil {
		cfg := c.d.Config.Get()
		s.cfg = &cfg
	}
	st := c.d.Store
	if st == nil {
		return s
	}
	lctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	if v, err := st.Settings().Get(lctx); err == nil {
		s.settings = &v
	} else {
		c.log.Debug("Health: cannot read settings", "error", err)
	}
	var err error
	if s.servers, err = st.MediaServers().List(lctx); err == nil {
		s.serversOK = true
	} else {
		c.log.Debug("Health: cannot list media servers", "error", err)
	}
	if s.arrs, err = st.ArrInstances().List(lctx); err == nil {
		s.arrsOK = true
	} else {
		c.log.Debug("Health: cannot list *arr instances", "error", err)
	}
	if s.libraries, err = st.Libraries().List(lctx); err == nil {
		s.libsOK = true
	} else {
		c.log.Debug("Health: cannot list libraries", "error", err)
	}
	if s.mappings, err = st.PathMappings().List(lctx); err == nil {
		s.mapsOK = true
	} else {
		c.log.Debug("Health: cannot list path mappings", "error", err)
	}
	if s.tautullis, err = st.Tautullis().List(lctx); err == nil {
		s.tautOK = true
	} else {
		c.log.Debug("Health: cannot list Tautulli connections", "error", err)
	}
	if s.profiles, err = st.Profiles().List(lctx); err == nil {
		s.profOK = true
	} else {
		c.log.Debug("Health: cannot list profiles", "error", err)
	}
	return s
}

// methodEnabled reports whether a deletion method is enabled in the settings.
func (s *snapshot) methodEnabled(method string) bool {
	return s.settings != nil && slices.Contains(s.settings.DeletionMethods, method)
}

// enabledServers returns the enabled media servers of a supported kind (those scans read).
func (s *snapshot) enabledServers() []models.MediaServer {
	var out []models.MediaServer
	for _, srv := range s.servers {
		if srv.Enabled && srv.Kind.Supported() {
			out = append(out, srv)
		}
	}
	return out
}

// enabledPlexServers returns the enabled Plex servers (the Plex-only checks).
func (s *snapshot) enabledPlexServers() []models.MediaServer {
	var out []models.MediaServer
	for _, srv := range s.servers {
		if srv.Enabled && srv.Kind.IsPlex() {
			out = append(out, srv)
		}
	}
	return out
}

// serverClient returns the client of a media server: from Deps.MediaServerFactory when it is set,
// else from Deps.PlexFactory, else nil. A factory's nil stays a nil interface (the server is
// skipped).
func (c *Checker) serverClient(srv models.MediaServer) MediaServerClient {
	switch {
	case c.d.MediaServerFactory != nil:
		if cl := c.d.MediaServerFactory(srv); cl != nil {
			return cl
		}
	case c.d.PlexFactory != nil:
		if cl := c.d.PlexFactory(srv); cl != nil {
			return cl
		}
	}
	return nil
}

// hasServerFactory reports whether a media server client factory is configured.
func (c *Checker) hasServerFactory() bool {
	return c.d.MediaServerFactory != nil || c.d.PlexFactory != nil
}

// enabledArrs returns the enabled *arr instances.
func (s *snapshot) enabledArrs() []models.ArrInstance {
	var out []models.ArrInstance
	for _, a := range s.arrs {
		if a.Enabled {
			out = append(out, a)
		}
	}
	return out
}

// libraryFolder is an enabled library location of an enabled server.
type libraryFolder struct {
	lib      models.Library
	server   string
	location string // as the server sees it
}

func (s *snapshot) enabledLibraryFolders() []libraryFolder {
	servers := map[int64]string{}
	for _, srv := range s.servers {
		if srv.Enabled {
			servers[srv.ID] = srv.Name
		}
	}
	var out []libraryFolder
	for _, l := range s.libraries {
		name, ok := servers[l.ServerID]
		if !l.Enabled || !ok {
			continue
		}
		for _, loc := range l.Locations {
			if strings.TrimSpace(loc) != "" {
				out = append(out, libraryFolder{lib: l, server: name, location: loc})
			}
		}
	}
	return out
}

// forEach runs fn for every item concurrently and returns the non-nil results in item order.
// target names an item: its issue subject and its display name. A panicking probe is reported as
// a warning for that item (under the same key as its regular issue) rather than dropped, which
// would make a broken probe look healthy.
func forEach[T any](c *Checker, source string, items []T, target func(T) (subject, name string), fn func(T) *result) []result {
	res := make([]*result, len(items))
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					subject, name := target(it)
					c.log.Error("Health probe panicked", "check", source, "subject", subject, "panic", fmt.Sprint(r))
					res[i] = issue(source, subject, models.HealthWarning,
						fmt.Sprintf("The %s health check failed unexpectedly for %s; see the logs", source, name))
				}
			}()
			res[i] = fn(it)
		}()
	}
	wg.Wait()
	var out []result
	for _, r := range res {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

func issue(source, subject string, t models.HealthType, msg string) *result {
	key := source
	if subject != "" {
		key += ":" + subject
	}
	return &result{key: key, check: models.HealthCheck{Source: source, Type: t, Message: msg}}
}

// errText renders an error for a user-facing message: secrets redacted, no upstream response body
// (upstreamerr: a connection URL may point at any internal service), length capped.
func errText(err error) string {
	s := strings.TrimSpace(logging.Redact(upstreamerr.Message(err)))
	if len(s) > maxErrLen {
		cut := maxErrLen
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// listText joins up to maxListed items ("a, b and 3 more").
func listText(items []string) string {
	if len(items) <= maxListed {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:maxListed], ", ") + " and " + strconv.Itoa(len(items)-maxListed) + " more"
}

func serverSubject(id int64) string   { return "server:" + strconv.FormatInt(id, 10) }
func arrSubject(id int64) string      { return "arr:" + strconv.FormatInt(id, 10) }
func tautulliSubject(id int64) string { return "tautulli:" + strconv.FormatInt(id, 10) }

// serverTarget / arrTarget name a probed connection for forEach.
func serverTarget(s models.MediaServer) (string, string) { return serverSubject(s.ID), s.Name }
func arrTarget(a models.ArrInstance) (string, string)    { return arrSubject(a.ID), a.Name }

// ---------------------------------------------------------------------------
// Checks
// ---------------------------------------------------------------------------

func (c *Checker) checkNoMediaServer(_ context.Context, s *snapshot) []result {
	if !s.serversOK {
		return nil
	}
	if len(s.servers) == 0 {
		return []result{*issue(SourceNoMediaServer, "", models.HealthWarning,
			"No media server is configured. Add your Plex or Jellyfin server in Settings → Media Servers so Dupearr can look for duplicates.")}
	}
	for _, srv := range s.servers {
		if srv.Enabled {
			return nil
		}
	}
	return []result{*issue(SourceNoMediaServer, "", models.HealthWarning,
		"All media servers are disabled, so Dupearr cannot look for duplicates. Enable one in Settings → Media Servers.")}
}

func (c *Checker) checkMediaServerConnectivity(ctx context.Context, s *snapshot) []result {
	if !c.hasServerFactory() {
		return nil
	}
	return forEach(c, SourceMediaServerConnectivity, s.enabledServers(), serverTarget, func(srv models.MediaServer) *result {
		client := c.serverClient(srv)
		if client == nil {
			return nil
		}
		id, err := client.Identity(ctx)
		if err != nil {
			return issue(SourceMediaServerConnectivity, serverSubject(srv.ID), models.HealthError,
				fmt.Sprintf("Unable to connect to media server %s: %s", srv.Name, errText(err)))
		}
		want := strings.TrimSpace(srv.MachineIdentifier)
		if id != nil && want != "" && id.MachineIdentifier != "" && !strings.EqualFold(id.MachineIdentifier, want) {
			if !srv.Kind.IsPlex() {
				return issue(SourceMediaServerConnectivity, serverSubject(srv.ID), models.HealthError,
					fmt.Sprintf("Media server %s answers as a different %s server (server id %s, expected %s). "+
						"Check its URL in Settings → Media Servers.", srv.Name, srv.Kind.Label(), id.MachineIdentifier, want))
			}
			return issue(SourceMediaServerConnectivity, serverSubject(srv.ID), models.HealthError,
				fmt.Sprintf("Media server %s answers as a different Plex server (machine identifier %s, expected %s). "+
					"Check its URL in Settings → Media Servers.", srv.Name, id.MachineIdentifier, want))
		}
		return nil
	})
}

func (c *Checker) checkPlexMediaDeletion(ctx context.Context, s *snapshot) []result {
	if !c.hasServerFactory() || !s.methodEnabled(models.MethodPlex) {
		return nil
	}
	return forEach(c, SourcePlexMediaDeletion, s.enabledPlexServers(), serverTarget, func(srv models.MediaServer) *result {
		client, ok := c.serverClient(srv).(PlexDeletionSetting)
		if !ok || client == nil {
			return nil
		}
		allowed, err := client.MediaDeletionAllowed(ctx)
		if err != nil || allowed { // unreachable servers are reported by MediaServerConnectivityCheck
			return nil
		}
		return issue(SourcePlexMediaDeletion, serverSubject(srv.ID), models.HealthWarning,
			fmt.Sprintf("Deleting through Plex is enabled, but %s does not allow media deletion. "+
				"Turn on \"Allow media deletion\" in Plex (Settings → Library); Plex also only accepts deletions "+
				"made with the server owner's token. Until then Dupearr uses the next deletion method.", srv.Name))
	})
}

func (c *Checker) checkPlexOwner(ctx context.Context, s *snapshot) []result {
	if !c.hasServerFactory() || !s.methodEnabled(models.MethodPlex) {
		return nil
	}
	return forEach(c, SourcePlexOwner, s.enabledPlexServers(), serverTarget, func(srv models.MediaServer) *result {
		machineID := strings.TrimSpace(srv.MachineIdentifier)
		if machineID == "" {
			return nil
		}
		po, ok := c.serverClient(srv).(PlexOwnership)
		if !ok || po == nil {
			return nil
		}
		pctx, cancel := context.WithTimeout(ctx, c.plexTVTimeout)
		defer cancel()
		owned, known, err := po.Ownership(pctx, machineID)
		if err != nil || !known || owned {
			// plex.tv unreachable (LAN-only setups) or the token is not listed there: unknown.
			if err != nil {
				c.log.Debug("Health: cannot ask plex.tv who owns the server", "server", srv.Name, "error", errText(err))
			}
			return nil
		}
		return issue(SourcePlexOwner, serverSubject(srv.ID), models.HealthWarning,
			fmt.Sprintf("Deleting through Plex is enabled, but the token of %s belongs to a user the server is shared "+
				"with, not to its owner: Plex only accepts deletions made with the server owner's token. Sign in with "+
				"the owner's Plex account in Settings → Media Servers. Until then Dupearr uses the next deletion method.", srv.Name))
	})
}

func (c *Checker) checkArrConnectivity(ctx context.Context, s *snapshot) []result {
	if c.d.ArrFactory == nil {
		return nil
	}
	return forEach(c, SourceArrConnectivity, s.enabledArrs(), arrTarget, func(a models.ArrInstance) *result {
		client := c.d.ArrFactory(a)
		if client == nil {
			return nil
		}
		if _, err := client.Status(ctx); err != nil {
			return issue(SourceArrConnectivity, arrSubject(a.ID), models.HealthError,
				fmt.Sprintf("Unable to connect to %s: %s", a.Name, errText(err)))
		}
		return nil
	})
}

func (c *Checker) checkArrRecycleBin(ctx context.Context, s *snapshot) []result {
	if c.d.ArrFactory == nil || !s.methodEnabled(models.MethodArr) {
		return nil
	}
	return forEach(c, SourceArrRecycleBin, s.enabledArrs(), arrTarget, func(a models.ArrInstance) *result {
		client := c.d.ArrFactory(a)
		mm, ok := client.(ArrMediaManagement)
		if !ok || mm == nil {
			return nil
		}
		cfg, err := mm.MediaManagement(ctx)
		if err != nil || cfg == nil { // unreachable instances are reported by ArrConnectivityCheck
			return nil
		}
		if strings.TrimSpace(cfg.RecycleBin) != "" {
			return nil
		}
		return issue(SourceArrRecycleBin, arrSubject(a.ID), models.HealthNotice,
			fmt.Sprintf("%s has no recycling bin configured: deletes through %s are permanent. "+
				"Set Settings → Media Management → Recycling Bin in %s to be able to undo removals.", a.Name, a.Name, a.Name))
	})
}

// enabledTautullis returns the enabled Tautulli connections of enabled media servers, with their
// server.
func (s *snapshot) enabledTautullis() []tautulliTarget {
	servers := map[int64]models.MediaServer{}
	for _, srv := range s.enabledPlexServers() {
		servers[srv.ID] = srv
	}
	var out []tautulliTarget
	for _, t := range s.tautullis {
		if srv, ok := servers[t.ServerID]; ok && t.Enabled {
			out = append(out, tautulliTarget{t: t, server: srv})
		}
	}
	return out
}

// tautulliTarget is a Tautulli connection with the media server it records.
type tautulliTarget struct {
	t      models.TautulliInstance
	server models.MediaServer
}

func tautulliTargetOf(x tautulliTarget) (string, string) { return tautulliSubject(x.t.ID), x.t.Name }

// checkTautulliConnectivity reports Tautulli connections a scan cannot use: unreachable, key
// rejected, too old, or monitoring another Plex server (docs/DECISIONS.md D10). Groups ranked by
// play history go to review while it lasts.
func (c *Checker) checkTautulliConnectivity(ctx context.Context, s *snapshot) []result {
	if c.d.TautulliFactory == nil || !s.tautOK || !s.serversOK {
		return nil
	}
	return forEach(c, SourceTautulliConnectivity, s.enabledTautullis(), tautulliTargetOf, func(x tautulliTarget) *result {
		client := c.d.TautulliFactory(x.t)
		if client == nil {
			return nil
		}
		info, err := client.Info(ctx)
		if err != nil {
			return issue(SourceTautulliConnectivity, tautulliSubject(x.t.ID), models.HealthError,
				fmt.Sprintf("Unable to read the Tautulli connection %q: %s. Groups ranked by play history go to review until it answers.",
					x.t.Name, strings.TrimPrefix(errText(err), "tautulli: ")))
		}
		want := strings.TrimSpace(x.server.MachineIdentifier)
		switch {
		case want == "":
			return issue(SourceTautulliConnectivity, tautulliSubject(x.t.ID), models.HealthError,
				fmt.Sprintf("The Tautulli connection %q cannot be matched to %s: the media server has no machine identifier yet. "+
					"Test and save it in Settings → Media Servers.", x.t.Name, x.server.Name))
		case !strings.EqualFold(strings.TrimSpace(info.PMSIdentifier), want):
			return issue(SourceTautulliConnectivity, tautulliSubject(x.t.ID), models.HealthError,
				fmt.Sprintf("The Tautulli connection %q monitors another Plex server (machine identifier %s), not %s: its play history is "+
					"not used. Check its URL in Settings → Applications.", x.t.Name, info.PMSIdentifier, x.server.Name))
		}
		return nil
	})
}

// watchProfiles returns the profiles (by id, and whether the default one does) that rank by play
// history.
func watchProfiles(profiles []models.Profile) (byID map[int64]models.Profile, def *models.Profile) {
	byID = map[int64]models.Profile{}
	for _, p := range profiles {
		for _, cr := range p.Criteria {
			if cr.Enabled && (cr.Type == models.CritPlayed || cr.Type == models.CritLastPlayed) {
				byID[p.ID] = p
				if p.IsDefault {
					pp := p
					def = &pp
				}
				break
			}
		}
	}
	return byID, def
}

// checkWatchHistory warns when a profile ranks by play history for a server without an enabled
// Tautulli (every copy's history is then unknown and those criteria never decide), and notes the
// libraries and users whose history Tautulli does not keep (their copies without plays are unknown).
func (c *Checker) checkWatchHistory(ctx context.Context, s *snapshot) []result {
	if !s.profOK || !s.tautOK || !s.serversOK || !s.libsOK {
		return nil
	}
	using, def := watchProfiles(s.profiles)
	if len(using) == 0 {
		return nil
	}
	servers := map[int64]models.MediaServer{}
	for _, srv := range s.enabledPlexServers() {
		servers[srv.ID] = srv
	}
	withTautulli := map[int64]tautulliTarget{}
	for _, x := range s.enabledTautullis() {
		withTautulli[x.server.ID] = x
	}
	missing := map[int64][]string{}      // server → profile names
	libs := map[int64][]models.Library{} // server → libraries ranked by play history
	for _, l := range s.libraries {
		if _, ok := servers[l.ServerID]; !ok || !l.Enabled || (l.Type != "movie" && l.Type != "show") {
			continue
		}
		var p *models.Profile
		if l.ProfileID != nil {
			if pp, ok := using[*l.ProfileID]; ok {
				p = &pp
			}
		} else {
			p = def
		}
		if p == nil {
			continue
		}
		libs[l.ServerID] = append(libs[l.ServerID], l)
		if _, ok := withTautulli[l.ServerID]; !ok && !slices.Contains(missing[l.ServerID], p.Name) {
			missing[l.ServerID] = append(missing[l.ServerID], p.Name)
		}
	}
	var out []result
	for _, id := range sortedIDs(missing) {
		names := missing[id]
		slices.Sort(names)
		out = append(out, *issue(SourceWatchHistory, serverSubject(id), models.HealthWarning,
			fmt.Sprintf("The profile %s ranks by play history (Played / Last played), but %s has no enabled Tautulli "+
				"connection: every copy's play history is unknown, so those criteria never decide. Add Tautulli in "+
				"Settings → Applications, or remove the criteria.", quoteList(names), servers[id].Name)))
	}
	if c.d.TautulliFactory == nil {
		return out
	}
	var targets []tautulliTarget
	for _, id := range sortedIDs(libs) {
		if x, ok := withTautulli[id]; ok {
			targets = append(targets, x)
		}
	}
	out = append(out, forEach(c, SourceWatchHistory, targets, tautulliTargetOf, func(x tautulliTarget) *result {
		client := c.d.TautulliFactory(x.t)
		if client == nil {
			return nil
		}
		// Unreachable connections are reported by TautulliConnectivityCheck.
		users, err := client.Users(ctx)
		if err != nil {
			return nil
		}
		noUsers := 0
		for _, u := range users {
			if u.Active && (u.KeepHistory == nil || !*u.KeepHistory) {
				noUsers++
			}
		}
		var noLibs []string
		for _, l := range libs[x.server.ID] {
			lib, err := client.Library(ctx, l.SectionKey)
			if err != nil {
				return nil
			}
			if lib.KeepHistory == nil || !*lib.KeepHistory {
				noLibs = append(noLibs, l.Title)
			}
		}
		var parts []string
		if len(noLibs) > 0 {
			parts = append(parts, "the libraries "+listText(noLibs))
		}
		if noUsers == 1 {
			parts = append(parts, "1 user")
		} else if noUsers > 1 {
			parts = append(parts, strconv.Itoa(noUsers)+" users")
		}
		if len(parts) == 0 {
			return nil
		}
		return issue(SourceWatchHistory, tautulliSubject(x.t.ID), models.HealthNotice,
			fmt.Sprintf("The Tautulli connection %q keeps no play history for %s: in those libraries (or, for users, on the whole server) "+
				"a copy without recorded plays counts as unknown, not as \"no plays recorded\". Turn on \"Keep History\" in Tautulli "+
				"if you want Played / Last played to decide there.", x.t.Name, strings.Join(parts, " and ")))
	})...)
	return out
}

// sortedIDs returns the keys of m in order.
func sortedIDs[T any](m map[int64]T) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// quoteList renders names quoted ("A", "B").
func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return listText(q)
}

func (c *Checker) checkPathMapping(_ context.Context, s *snapshot) []result {
	if !s.serversOK || !s.libsOK || !s.mapsOK {
		return nil
	}
	mapper := pathmap.New(s.mappings)
	// Jellyfin folders need a mapping whatever the deletion methods (jellyfin.go).
	out := jellyfinPathMappingIssues(s, mapper)
	if !s.methodEnabled(models.MethodFilesystem) {
		return out
	}
	kinds := s.serverKinds()
	var unmapped, missing []string
	for _, f := range s.enabledLibraryFolders() {
		if kinds[f.lib.ServerID].ReadOnly() {
			continue // reported above
		}
		local, ok := mapper.ToLocal(models.PathSourceServer, f.lib.ServerID, f.location)
		if !ok {
			unmapped = append(unmapped, fmt.Sprintf("%s (%s on %s)", f.location, f.lib.Title, f.server))
			continue
		}
		if st, err := os.Stat(local); err != nil || !st.IsDir() {
			missing = append(missing, fmt.Sprintf("%s (mapped from %s)", local, f.location))
		}
	}
	if len(unmapped) > 0 {
		out = append(out, *issue(SourcePathMapping, "unmapped", models.HealthWarning,
			"Filesystem deletion is enabled, but no path mapping covers these library folders: "+listText(unmapped)+
				". Add a Plex path mapping in Settings → Media Management (use the same path on both sides when "+
				"Dupearr sees the files at the same path)."))
	}
	if len(missing) > 0 {
		out = append(out, *issue(SourcePathMapping, "missing", models.HealthWarning,
			"Filesystem deletion is enabled, but these mapped library folders do not exist inside Dupearr: "+
				listText(missing)+". Check the volume mounts and path mappings."))
	}
	return out
}

// checkDiscDetection reports full-disc detection that cannot work: it is on, there are enabled
// movie libraries, but none of their folders maps to a local path (the scan looks for discs in the
// local copies of the movie folders; Plex's own scanners never show a disc).
func (c *Checker) checkDiscDetection(_ context.Context, s *snapshot) []result {
	if s.settings == nil || !s.settings.DetectDiscs || !s.serversOK || !s.libsOK || !s.mapsOK {
		return nil
	}
	mapper := pathmap.New(s.mappings)
	kinds := s.serverKinds()
	movies := 0
	for _, f := range s.enabledLibraryFolders() {
		// Discs are looked for next to Plex movies only (a disc on Jellyfin is only reported).
		if !strings.EqualFold(strings.TrimSpace(f.lib.Type), "movie") || kinds[f.lib.ServerID].ReadOnly() {
			continue
		}
		movies++
		if _, ok := mapper.ToLocal(models.PathSourceServer, f.lib.ServerID, f.location); ok {
			return nil
		}
	}
	if movies == 0 {
		return nil
	}
	return []result{*issue(SourceDiscDetection, "", models.HealthNotice,
		"Full-disc detection is on, but no movie library folder has a path mapping: Dupearr cannot look for "+
			"Blu-ray/DVD folders and disc images next to your movies (Plex does not show them). Add a Plex path "+
			"mapping in Settings → Media Management, or turn \"Detect Full-Disc Backups\" off.")}
}

func (c *Checker) checkRecycleBin(ctx context.Context, s *snapshot) []result {
	if s.settings == nil {
		return nil
	}
	bin := strings.TrimSpace(s.settings.RecycleBinPath)
	if bin == "" {
		return nil
	}
	fail := func(msg string) []result {
		return []result{*issue(SourceRecycleBin, "", models.HealthError, msg)}
	}
	if !filepath.IsAbs(bin) {
		return fail(fmt.Sprintf("The recycle bin path %q is not an absolute path. Fix it in Settings → Media Management.", bin))
	}
	bin = filepath.Clean(bin)

	var mapper *pathmap.Mapper
	var libRoots []string  // local folders of the library locations (mapped)
	var plexRoots []string // … of Plex servers (the .plexignore check)
	if s.serversOK && s.libsOK && s.mapsOK {
		mapper = pathmap.New(s.mappings)
		kinds := s.serverKinds()
		for _, f := range s.enabledLibraryFolders() {
			if root, ok := mapper.ToLocal(models.PathSourceServer, f.lib.ServerID, f.location); ok {
				libRoots = append(libRoots, root)
				if kinds[f.lib.ServerID].IsPlex() {
					plexRoots = append(plexRoots, root)
				}
			}
		}
	}
	// The executor refuses a bin that is (or contains) a mapped media folder: its cleanup would
	// permanently delete media.
	if s.mapsOK {
		for _, m := range s.mappings {
			if lp := strings.TrimSpace(m.LocalPath); lp != "" && filepath.IsAbs(lp) && within(lp, bin) {
				return fail(fmt.Sprintf("The recycle bin folder %s is or contains the mapped media folder %s, so Dupearr "+
					"refuses to use it (its cleanup would delete media). Choose a dedicated folder in Settings → Media Management.",
					bin, filepath.Clean(lp)))
			}
		}
	}
	for _, root := range libRoots {
		if within(root, bin) {
			return fail(fmt.Sprintf("The recycle bin folder %s is or contains the library folder %s, so Dupearr refuses "+
				"to use it. Choose a dedicated folder in Settings → Media Management.", bin, root))
		}
	}

	st, err := os.Stat(bin)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		// Created (with a .plexignore) on first use: fine when its parent folder exists and is
		// writable. A missing parent usually means a volume is not mounted: creating the bin
		// would then fill the container's own filesystem.
		parent := filepath.Dir(bin)
		pst, perr := os.Stat(parent)
		switch {
		case perr != nil:
			return fail(fmt.Sprintf("The recycle bin folder %s does not exist and neither does its parent folder %s. "+
				"Check the volume mounts, or change the path in Settings → Media Management.", bin, parent))
		case !pst.IsDir():
			return fail(fmt.Sprintf("The recycle bin folder %s cannot be created: %s is not a folder.", bin, parent))
		}
		if err := probeWritable(parent); err != nil {
			return fail(fmt.Sprintf("The recycle bin folder %s does not exist and Dupearr cannot create it in %s: %s",
				bin, parent, errText(err)))
		}
		// Not an issue: the executor creates it the first time it moves a file there (a notice
		// would stay on display after that until the next health run).
		return nil
	case err != nil:
		return fail(fmt.Sprintf("The recycle bin folder %s is not accessible: %s", bin, errText(err)))
	case !st.IsDir():
		return fail(fmt.Sprintf("The recycle bin path %s is not a folder.", bin))
	}
	if err := probeWritable(bin); err != nil {
		return fail(fmt.Sprintf("Dupearr cannot write to the recycle bin folder %s: %s", bin, errText(err)))
	}

	var out []result
	if mapper != nil {
		out = c.jellyfinBinIssues(ctx, s, bin, mapper)
	}
	for _, root := range plexRoots {
		if !within(bin, root) {
			continue
		}
		ignoreFile := filepath.Join(bin, plexIgnoreName)
		switch all, err := ignoresEverything(ignoreFile); {
		case all:
			return out
		case err == nil:
			// A .plexignore exists (e.g. created by the user) but does not exclude everything, and
			// the executor never overwrites an existing one.
			return append(out, *issue(SourceRecycleBin, "plexignore", models.HealthWarning,
				fmt.Sprintf("The recycle bin folder %s is inside the Plex library folder %s, but its %s does not "+
					"exclude everything, so Plex may add recycled files back to the library. Add a line \"*\" to %s.",
					bin, root, plexIgnoreName, ignoreFile)))
		}
		return append(out, *issue(SourceRecycleBin, "plexignore", models.HealthWarning,
			fmt.Sprintf("The recycle bin folder %s is inside the Plex library folder %s and has no %s, so Plex may "+
				"add recycled files back to the library. Create %s containing a single line \"*\".",
				bin, root, plexIgnoreName, filepath.Join(bin, plexIgnoreName))))
	}
	return out
}

// maxPlexIgnoreSize caps how much of a .plexignore is read.
const maxPlexIgnoreSize = 64 << 10

// ignoresEverything reports whether the .plexignore at path has a "*" pattern line (what the
// executor writes), which makes Plex skip every file of that folder. err is non-nil when the file
// is missing or unreadable.
func ignoresEverything(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPlexIgnoreSize))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "*" {
			return true, nil
		}
	}
	return false, nil
}

// probeWritable creates and removes a temporary file in dir.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".dupearr-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	cerr := f.Close()
	rerr := os.Remove(name)
	return errors.Join(cerr, rerr)
}

// within reports whether path equals root or lies below it (symlinks resolved when possible).
func within(path, root string) bool {
	path, root = resolve(path), resolve(root)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// resolve cleans p and resolves symlinks in its longest existing prefix (the rest, which does
// not exist yet, is appended unchanged), so paths compare equal whichever alias they use.
func resolve(p string) string {
	p = filepath.Clean(p)
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

func (c *Checker) checkDryRun(_ context.Context, s *snapshot) []result {
	if s.settings == nil || !s.settings.DryRun {
		return nil
	}
	return []result{*issue(SourceDryRun, "", models.HealthNotice,
		"Dry run is enabled: Dupearr only records what it would delete. Turn it off in Settings → Media Management "+
			"once you have reviewed the results.")}
}

func (c *Checker) checkAuthentication(_ context.Context, s *snapshot) []result {
	if s.cfg == nil || !strings.EqualFold(s.cfg.AuthenticationMethod, config.AuthNone) {
		return nil
	}
	return []result{*issue(SourceAuthentication, "", models.HealthWarning,
		"Authentication is disabled: anyone who can reach Dupearr can delete your media. Enable Forms authentication "+
			"in Settings → General.")}
}

// checkWebhookAPIKey warns when a webhook authenticated with the master API key: webhook URLs are
// stored in Radarr, Sonarr (whose API and backups return them unmasked) and plex.tv, so they must
// carry the webhook token, which can only queue scans.
func (c *Checker) checkWebhookAPIKey(_ context.Context, _ *snapshot) []result {
	if c.d.WebhookMasterKeyUsed == nil {
		return nil
	}
	last := c.d.WebhookMasterKeyUsed()
	if last.IsZero() {
		return nil
	}
	return []result{*issue(SourceWebhookAPIKey, "", models.HealthWarning,
		"A webhook authenticated with the master API key (last at "+last.UTC().Format(time.RFC3339)+"). Webhook URLs are "+
			"stored in Radarr, Sonarr and plex.tv: replace them with the URLs from Settings → Connections → Webhooks "+
			"(they carry the webhook token, which can only queue scans), then regenerate the API key.")}
}

// checkExternalAuth checks External authentication. Dupearr trusts a request as authenticated by
// the reverse proxy only when its TCP peer is one of the trusted proxies; without trusted proxies
// it trusts every request that names it by an IP address or a private host name (only the
// DNS-rebinding guard of None) — or, with allowed hosts, by one of those — so anyone who reaches
// the port directly gets full access (warning). With trusted proxies the port must still only be
// reachable through the proxy (notice).
func (c *Checker) checkExternalAuth(_ context.Context, s *snapshot) []result {
	if s.cfg == nil || !strings.EqualFold(s.cfg.AuthenticationMethod, config.AuthExternal) {
		return nil
	}
	if c.d.ProxyTrust != nil {
		if proxies, hosts := c.d.ProxyTrust(); proxies == 0 {
			named := "addresses it by an IP address or a local host name"
			if hosts > 0 {
				named = "names one of the allowed hosts" // any client can send that Host header
			}
			return []result{*issue(SourceExternalAuth, "", models.HealthWarning,
				"Authentication is left to an external reverse proxy (External), but no trusted proxy is configured: "+
					"anyone who reaches Dupearr's port directly and "+named+" gets full access, without credentials. Set "+
					"Trusted Proxies in Settings → General (or DUPEARR__AUTH__TRUSTEDPROXIES) to the address of your "+
					"authenticating reverse proxy, and do not publish Dupearr's port.")}
		}
	}
	return []result{*issue(SourceExternalAuth, "", models.HealthNotice,
		"Authentication is left to an external reverse proxy (External): make sure Dupearr's port is only reachable through "+
			"your authenticating reverse proxy — anyone who reaches it directly can delete your media.")}
}

// checkReverseProxy warns about a reverse proxy that is missing from the trusted proxies: a local
// peer that sends forwarding headers. Its clients all share the proxy's login-throttling key (an
// attacker can keep the owner's first login throttled), and External authentication and the
// local-address checks cannot see them.
func (c *Checker) checkReverseProxy(_ context.Context, _ *snapshot) []result {
	if c.d.UntrustedProxySeen == nil {
		return nil
	}
	last, peer := c.d.UntrustedProxySeen()
	if last.IsZero() {
		return nil
	}
	return []result{*issue(SourceReverseProxy, "", models.HealthWarning,
		"Requests with forwarding headers (X-Forwarded-For, Forwarded or X-Real-IP) come from "+peer+
			" (last at "+last.UTC().Format(time.RFC3339)+"), which is not a trusted proxy. If that is your reverse proxy, "+
			"add its address to Trusted Proxies in Settings → General (Show Advanced), or set DUPEARR__AUTH__TRUSTEDPROXIES: "+
			"until then Dupearr ignores those headers, so every client behind the proxy shares one login-throttling limit "+
			"and cannot be told apart.")}
}

func (c *Checker) checkLastScan(ctx context.Context, _ *snapshot) []result {
	if c.d.Store == nil {
		return nil
	}
	run, err := c.d.Store.ScanRuns().Latest(ctx)
	if err != nil || run == nil { // none yet (store.ErrNotFound) or DB trouble (DatabaseCheck)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			c.log.Debug("Health: cannot read the latest scan", "error", err)
		}
		return nil
	}
	if run.Status != "failed" {
		return nil
	}
	msg := "The last duplicate scan failed; see System → Logs for details."
	if strings.TrimSpace(run.Error) != "" {
		msg = "The last duplicate scan failed: " + errText(errors.New(run.Error))
	}
	return []result{*issue(SourceLastScan, "", models.HealthWarning, msg)}
}

func (c *Checker) checkDatabase(ctx context.Context, _ *snapshot) []result {
	if c.d.Store == nil {
		return nil
	}
	if err := c.d.Store.Ping(ctx); err != nil {
		return []result{*issue(SourceDatabase, "", models.HealthError, "The database is not accessible: "+errText(err))}
	}
	return nil
}
