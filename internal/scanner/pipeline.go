package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// bookkeepingTimeout bounds the final writes of a scan (run row, history), which run even when
// the scan's context was cancelled.
const bookkeepingTimeout = 30 * time.Second

// maxErrorRunes bounds the error text of a failed scan that is stored, published and notified.
const maxErrorRunes = 300

// errorText renders the error of a failed scan for the scan run, the history, the event stream
// and the notification connections (third parties): secrets masked (logging.Redact), control and
// invisible format characters (line breaks, terminal escapes, bidi overrides) dropped or turned
// into single spaces, and at most maxErrorRunes runes. Much of the text can come from a media
// server or an *arr (paths, messages); the complete error is only logged (the log handler
// redacts it as well).
func errorText(err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	n, space := 0, false
	// No upstream response body (upstreamerr): the text is shown by the API (scan runs, health).
	for _, r := range logging.Redact(upstreamerr.Message(err)) {
		switch {
		case unicode.IsSpace(r) || unicode.IsControl(r):
			space = b.Len() > 0
			continue
		case unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar:
			continue
		}
		need := 1
		if space {
			need = 2
		}
		if n+need > maxErrorRunes {
			b.WriteString("…")
			break
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
		n += need
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Configuration snapshot
// ---------------------------------------------------------------------------

// evalConfig is what evaluating a group needs: settings, libraries, decision profiles and the
// exclusions (re-evaluations keep excluded content from being acted on, see blockedReason).
type evalConfig struct {
	settings       models.Settings
	libraries      map[int64]models.Library
	profiles       map[int64]models.Profile
	defaultProfile models.Profile
	exclusions     []models.Exclusion
}

// profileFor returns the profile of the group's first library, else the default profile.
func (c *evalConfig) profileFor(g *models.DuplicateGroup) models.Profile {
	if len(g.LibraryIDs) > 0 {
		if lib, ok := c.libraries[g.LibraryIDs[0]]; ok && lib.ProfileID != nil {
			if p, ok := c.profiles[*lib.ProfileID]; ok {
				return p
			}
		}
	}
	return c.defaultProfile
}

// evalEnv returns the evaluation environment at time now.
func (c *evalConfig) evalEnv(now time.Time) engine.EvalEnv {
	return engine.EvalEnvFromSettings(c.settings, now, c.libraries)
}

// loadEvalConfig reads settings, libraries and profiles. Without any stored default profile the
// built-in "Keep Highest Quality" template is used (and a warning logged), so a scan never runs
// without the safety protections of a real profile.
func (s *Service) loadEvalConfig(ctx context.Context) (*evalConfig, error) {
	st := s.d.Store
	settings, err := st.Settings().Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	libs, err := st.Libraries().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load libraries: %w", err)
	}
	profiles, err := st.Profiles().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load profiles: %w", err)
	}
	exclusions, err := st.Exclusions().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load exclusions: %w", err)
	}
	c := &evalConfig{
		settings:   settings,
		libraries:  make(map[int64]models.Library, len(libs)),
		profiles:   make(map[int64]models.Profile, len(profiles)),
		exclusions: exclusions,
	}
	for _, l := range libs {
		c.libraries[l.ID] = l
	}
	found := false
	for _, p := range profiles {
		c.profiles[p.ID] = p
		if p.IsDefault && !found {
			c.defaultProfile, found = p, true
		}
	}
	if !found {
		def, err := st.Profiles().GetDefault(ctx)
		switch {
		case err == nil && def != nil:
			c.defaultProfile = *def
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return nil, fmt.Errorf("load default profile: %w", err)
		default:
			s.d.Log.Warn("No default decision profile is configured; using the built-in template")
			if tpl := engine.ProfileTemplates(); len(tpl) > 0 {
				c.defaultProfile = tpl[0]
			}
		}
	}
	return c, nil
}

// scanConfig is the configuration snapshot a scan works with.
type scanConfig struct {
	*evalConfig
	servers map[int64]models.MediaServer // enabled Plex servers
	mapper  *pathmap.Mapper
	arrs    []models.ArrInstance // enabled instances, by id
	// tautulli holds the enabled Tautulli connection of each media server (docs/DECISIONS.md D10).
	tautulli map[int64]models.TautulliInstance
}

// loadScanConfig reads everything a scan needs from the store.
func (s *Service) loadScanConfig(ctx context.Context) (*scanConfig, error) {
	ec, err := s.loadEvalConfig(ctx)
	if err != nil {
		return nil, err
	}
	st := s.d.Store
	servers, err := st.MediaServers().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load media servers: %w", err)
	}
	mappings, err := st.PathMappings().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load path mappings: %w", err)
	}
	arrs, err := st.ArrInstances().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load *arr instances: %w", err)
	}
	tautullis, err := st.Tautullis().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load Tautulli connections: %w", err)
	}
	c := &scanConfig{
		evalConfig: ec,
		servers:    map[int64]models.MediaServer{},
		mapper:     pathmap.New(mappings),
		tautulli:   map[int64]models.TautulliInstance{},
	}
	for _, t := range tautullis {
		if t.Enabled {
			c.tautulli[t.ServerID] = t
		}
	}
	for _, srv := range servers {
		if srv.Enabled && (srv.Kind == models.MediaServerPlex || srv.Kind == "") {
			c.servers[srv.ID] = srv
		}
	}
	for _, a := range arrs {
		if a.Enabled && (a.Kind == models.ArrRadarr || a.Kind == models.ArrSonarr) {
			c.arrs = append(c.arrs, a)
		}
	}
	sort.Slice(c.arrs, func(i, j int) bool { return c.arrs[i].ID < c.arrs[j].ID })
	return c, nil
}

// mediaTypeOf maps a Plex section type to the media type the scanner lists ("" = not scanned).
func mediaTypeOf(sectionType string) models.MediaType {
	switch strings.ToLower(strings.TrimSpace(sectionType)) {
	case "movie":
		return models.MediaTypeMovie
	case "show":
		return models.MediaTypeEpisode
	}
	return ""
}

// normScope normalizes a scope group the way the engine does.
func normScope(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// scannable reports whether a library takes part in scans: enabled, movie/show, on an enabled
// server.
func (c *scanConfig) scannable(l models.Library) bool {
	if !l.Enabled || mediaTypeOf(l.Type) == "" {
		return false
	}
	_, ok := c.servers[l.ServerID]
	return ok
}

// withScopePartners adds, to the given scannable libraries, every scannable library of the same
// server, media type and (non-empty) scope group: cross-library duplicates can only be found —
// and safely resolved — when all libraries of a scope group are listed together.
func (c *scanConfig) withScopePartners(selected map[int64]models.Library) {
	for _, l := range sortedLibraries(selected) {
		scope := normScope(l.ScopeGroup)
		if scope == "" {
			continue
		}
		for _, other := range c.libraries {
			if _, ok := selected[other.ID]; ok || !c.scannable(other) {
				continue
			}
			if other.ServerID == l.ServerID && normScope(other.ScopeGroup) == scope &&
				mediaTypeOf(other.Type) == mediaTypeOf(l.Type) {
				selected[other.ID] = other
			}
		}
	}
}

// scanLibraries selects the libraries of a full scan: the requested ids (or every library) that
// are scannable, plus their scope-group partners.
func (c *scanConfig) scanLibraries(ids []int64) []models.Library {
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	selected := map[int64]models.Library{}
	for _, l := range c.libraries {
		if c.scannable(l) && (len(want) == 0 || want[l.ID]) {
			selected[l.ID] = l
		}
	}
	c.withScopePartners(selected)
	return sortedLibraries(selected)
}

// overlapPartners returns the movie and TV libraries of the selected libraries' servers — enabled
// in Dupearr or not — whose folders overlap a selected library's folders and that are not
// selected themselves. Their items reference the same files, so they must be listed to complete
// the shared-file index: a version whose file another library's item also uses is never
// removable (removing it would take the file from that item too). A library without known
// folders is assumed to overlap every library of its server.
func (c *scanConfig) overlapPartners(selected map[int64]models.Library) map[int64]models.Library {
	out := map[int64]models.Library{}
	for _, l := range sortedLibraries(selected) {
		for _, other := range c.libraries {
			if _, ok := selected[other.ID]; ok || other.ServerID != l.ServerID || mediaTypeOf(other.Type) == "" {
				continue
			}
			if _, ok := c.servers[other.ServerID]; ok && locationsOverlap(l, other) {
				out[other.ID] = other
			}
		}
	}
	return out
}

// locationsOverlap reports whether two libraries of a server may contain the same files: one
// of a's folders equals, contains or lies inside one of b's (case-insensitive, conservative).
// Unknown folders overlap everything.
func locationsOverlap(a, b models.Library) bool {
	la, lb := normLocations(a.Locations), normLocations(b.Locations)
	if len(la) == 0 || len(lb) == 0 {
		return true
	}
	for _, x := range la {
		for _, y := range lb {
			if pathWithin(x, y) || pathWithin(y, x) {
				return true
			}
		}
	}
	return false
}

// normLocations normalizes library folders for comparison (see sharedKey).
func normLocations(locs []string) []string {
	var out []string
	for _, l := range locs {
		if k := sharedKey(l); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// pathWithin reports whether normalized path p is root or lies below it (path-segment aware).
func pathWithin(p, root string) bool {
	root = strings.TrimSuffix(root, "/")
	return root == "" || p == root || strings.HasPrefix(p, root+"/")
}

// sortedLibraries returns the libraries ordered by server, then id.
func sortedLibraries(m map[int64]models.Library) []models.Library {
	out := make([]models.Library, 0, len(m))
	for _, l := range m {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServerID != out[j].ServerID {
			return out[i].ServerID < out[j].ServerID
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// libraryExcluded reports libraries removed from detection by an exclusion rule (their items are
// listed for the shared-file index but never fetched in detail).
func (c *scanConfig) libraryExcluded(id int64) bool {
	for _, e := range c.exclusions {
		if e.Kind == models.ExcludeLibrary && strings.TrimSpace(e.Value) == fmt.Sprint(id) {
			return true
		}
	}
	return false
}

// keyExcluded reports a group key excluded by a group_key exclusion rule.
func (c *scanConfig) keyExcluded(key string) bool {
	for _, e := range c.exclusions {
		if e.Kind == models.ExcludeGroupKey && strings.TrimSpace(e.Value) == key {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Pipeline state
// ---------------------------------------------------------------------------

// refKey identifies a media-server item: server id + rating key.
type refKey struct {
	server int64
	rk     string
}

// pipeline is the state of one scan run.
type pipeline struct {
	s          *Service
	ctx        context.Context
	run        *models.ScanRun
	log        *slog.Logger
	progressFn func(string)
	start      time.Time

	cfg *scanConfig

	// Collection state.
	index      *itemIndex
	listed     map[int64]bool  // libraries listed successfully in this run
	failedLibs map[int64]error // libraries whose listing failed
	failedRKs  map[refKey]error
	incomplete map[refKey]bool   // items grouped with an item whose details failed
	stale      map[string]string // version key → why its data looks out of date
	arrFailed  map[models.MediaType][]string
	// arrUnlookable: items the *arrs could not be asked about (no usable id) → reason.
	arrUnlookable map[refKey]string
	// busyItems: items whose *arr title has active download/import queue entries.
	busyItems map[refKey]bool
	// indexOnly: libraries listed only to complete the shared-file index (their folders overlap
	// a scanned library); they are never candidates and never resolved.
	indexOnly map[int64]bool
	// sharedIncomplete: scanned libraries whose shared-file index is incomplete (an overlapping
	// library could not be listed) → title of that library.
	sharedIncomplete map[int64]string
	// placedAt: version key → when its newest local file was put in place (applyFileAges).
	placedAt map[string]time.Time
	// unavailable: items with ≥2 media of which Plex reports files missing (keepUnavailable).
	unavailable map[refKey]bool
	// unavailableVersions: version keys with a part Plex reports missing that is not confirmed
	// deleted (its local folder is there but the file is not) — an unmounted share, a NAS still
	// starting, a path Plex cannot reach.
	unavailableVersions map[string]bool

	// Full-disc backups (discs.go): the folders looked into and their discs, the TV episodes whose
	// folder holds a disc (their groups get flag full_disc) and the disc roots of listed Plex files.
	discs      *discInventory
	discPlan   *discPlan
	discNearby map[refKey]bool
	discRoots  map[int64]map[string][]string

	// Persistence state.
	ignored    *ignoredIndex  // ignored groups by version key (loaded before persisting)
	protect    map[int64]bool // stored groups that must not be resolved in this run
	unsafeLibs map[int64]bool // libraries whose unseen groups must not be resolved
	autoCands  []*models.DuplicateGroup

	mu          sync.Mutex // guards run.Stats, failedRKs, the maps written by workers and clients
	progMu      sync.Mutex // serializes progress callbacks
	plexClients map[int64]PlexClient
	plexErrs    map[int64]error
}

func newPipeline(ctx context.Context, s *Service, run *models.ScanRun, progress func(string)) *pipeline {
	return &pipeline{
		s:          s,
		ctx:        ctx,
		run:        run,
		log:        s.d.Log.With("scanId", run.ID),
		progressFn: progress,
		start:      run.StartedAt,
		listed:     map[int64]bool{},
		failedLibs: map[int64]error{},
		failedRKs:  map[refKey]error{},
		incomplete: map[refKey]bool{},
		stale:      map[string]string{},
		arrFailed:  map[models.MediaType][]string{},

		arrUnlookable:    map[refKey]string{},
		busyItems:        map[refKey]bool{},
		indexOnly:        map[int64]bool{},
		sharedIncomplete: map[int64]string{},
		placedAt:         map[string]time.Time{},
		unavailable:      map[refKey]bool{},

		unavailableVersions: map[string]bool{},
		discNearby:          map[refKey]bool{},
		protect:             map[int64]bool{},
		unsafeLibs:          map[int64]bool{},
		plexClients:         map[int64]PlexClient{},
		plexErrs:            map[int64]error{},
	}
}

// scanProgress is the resource of a "scan" progress event (docs/API.md).
type scanProgress struct {
	Message string `json:"message"`
	ScanID  int64  `json:"scanId"`
}

// progress reports a human-readable step to the command (progress callback) and the UI (SSE).
// Calls are serialized: the callback never runs concurrently with itself.
func (p *pipeline) progress(msg string) {
	p.log.Debug(msg)
	p.s.publish(events.NameScan, events.ActionProgress, scanProgress{Message: msg, ScanID: p.run.ID})
	if p.progressFn == nil {
		return
	}
	p.progMu.Lock()
	defer p.progMu.Unlock()
	p.progressFn(msg)
}

// safely runs a pipeline stage, converting a panic (a bug, never expected) into an error so the
// run is recorded as failed instead of taking the process down with it.
func (p *pipeline) safely(stage func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("Scan aborted by an internal error", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return stage()
}

// addError counts one non-fatal error of the run.
func (p *pipeline) addError() {
	p.mu.Lock()
	p.run.Stats.Errors++
	p.mu.Unlock()
}

// plexClient returns (and caches) the client of a server. The server's identity is verified
// once per run against the stored machine identifier: a different Plex server answering at the
// configured URL must never be scanned with another server's libraries, profiles and groups.
func (p *pipeline) plexClient(srv models.MediaServer) (PlexClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.plexClients[srv.ID]; ok {
		return c, nil
	}
	if err, ok := p.plexErrs[srv.ID]; ok {
		return nil, err
	}
	c, err := p.newPlexClient(srv)
	if err != nil {
		p.plexErrs[srv.ID] = err
		return nil, err
	}
	p.plexClients[srv.ID] = c
	return c, nil
}

func (p *pipeline) newPlexClient(srv models.MediaServer) (PlexClient, error) {
	if p.s.d.PlexFactory == nil {
		return nil, errors.New("no media server client factory configured")
	}
	c := p.s.d.PlexFactory(srv)
	if c == nil {
		return nil, fmt.Errorf("media server %q: no client available", srv.Name)
	}
	want := strings.TrimSpace(srv.MachineIdentifier)
	if want == "" {
		// Saved without a connection test (forceSave while unreachable): learn the identity
		// now, so the checks below, of later scans and of the executor apply to this server.
		id, err := c.Identity(p.ctx)
		if err != nil || id == nil || strings.TrimSpace(id.MachineIdentifier) == "" {
			p.log.Debug("Could not read the identity of a media server stored without one", "server", srv.Name, "error", err)
			return c, nil
		}
		if err := p.s.adoptIdentity(p.ctx, srv, id.MachineIdentifier); err != nil {
			return nil, fmt.Errorf("media server %q: %w", srv.Name, err)
		}
		return c, nil
	}
	id, err := c.Identity(p.ctx)
	if err != nil {
		return nil, fmt.Errorf("media server %q: %w", srv.Name, err)
	}
	if id == nil || !strings.EqualFold(strings.TrimSpace(id.MachineIdentifier), want) {
		got := ""
		if id != nil {
			got = id.MachineIdentifier
		}
		return nil, fmt.Errorf("media server %q: the server at its URL reports machine identifier %q, but %q is configured (re-save the media server to confirm the change)",
			srv.Name, got, want)
	}
	return c, nil
}

// canceled wraps a context error as the scan's failure.
func canceled(err error) error { return fmt.Errorf("cancelled: %w", err) }

// ---------------------------------------------------------------------------
// Full scan
// ---------------------------------------------------------------------------

// runFull executes a full scan. A non-nil error fails the run.
func (p *pipeline) runFull(body models.DuplicateScanBody) error {
	cfg, err := p.s.loadScanConfig(p.ctx)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	p.cfg = cfg
	libs := cfg.scanLibraries(body.LibraryIDs)
	if len(libs) == 0 {
		if len(body.LibraryIDs) > 0 {
			return errors.New("none of the requested libraries is enabled for scanning")
		}
		p.progress("No enabled movie or TV libraries to scan")
		return nil
	}
	p.progress(fmt.Sprintf("Scanning %d libraries", len(libs)))

	p.listLibraries(libs, cfg.overlapPartners(libraryMap(libs)))
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	if len(p.listed) == 0 {
		return fmt.Errorf("could not list any of the %d libraries: %s", len(libs), p.libraryErrorSummary())
	}
	for _, l := range libs {
		if p.listed[l.ID] {
			p.run.Stats.ItemsExamined += len(p.index.byLibrary[l.ID])
		}
	}

	targets := p.withDiscCandidates(p.index.fullCandidates(cfg.libraryExcluded))
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	items := p.fetchDetails(targets)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.markIncomplete()
	p.attachDiscs(items)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}

	busy := p.enrich(items)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.watch(items)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.applyFileAges(items)
	p.noteUnavailable(items)
	groups := engine.BuildGroups(items, engine.GroupOptionsFromSettings(cfg.settings, cfg.exclusions, cfg.libraries))
	p.persistGroups(groups, busy)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.keepUnavailable()
	p.resolveFull()
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.autoApprove()
	return nil
}

// libraryMap indexes libraries by id.
func libraryMap(libs []models.Library) map[int64]models.Library {
	out := make(map[int64]models.Library, len(libs))
	for _, l := range libs {
		out[l.ID] = l
	}
	return out
}

// libraryErrorSummary lists the listing errors of failed libraries (bounded).
func (p *pipeline) libraryErrorSummary() string {
	ids := make([]int64, 0, len(p.failedLibs))
	for id := range p.failedLibs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var parts []string
	for i, id := range ids {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(ids)-i))
			break
		}
		name := fmt.Sprintf("library %d", id)
		if p.cfg != nil {
			if l, ok := p.cfg.libraries[id]; ok && l.Title != "" {
				name = l.Title
			}
		}
		parts = append(parts, fmt.Sprintf("%s: %v", name, p.failedLibs[id]))
	}
	return strings.Join(parts, "; ")
}

// ---------------------------------------------------------------------------
// Finish: run row, events, history, notifications
// ---------------------------------------------------------------------------

// finish records the outcome of the run. err (from the pipeline) fails the run and is returned
// wrapped; otherwise the completed run is returned with a nil error.
func (p *pipeline) finish(err error) (*models.ScanRun, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), bookkeepingTimeout)
	defer cancel()
	s := p.s
	run := p.run
	finished := s.now()
	run.FinishedAt = &finished
	if err != nil {
		run.Status = runFailed
		run.Error = errorText(err)
	} else {
		run.Status = runCompleted
	}
	if uerr := s.d.Store.ScanRuns().Update(ctx, run); uerr != nil {
		p.log.Error("Failed to record the scan result", "error", uerr)
	}
	s.publish(events.NameScan, events.ActionUpdated, *run)

	st := run.Stats
	kind := "Scan"
	if run.Targeted {
		kind = "Targeted scan"
	}
	summary := fmt.Sprintf("%d duplicate groups found (%d new, %d resolved, %d pending, %d need review), %s reclaimable",
		st.GroupsFound, st.NewGroups, st.ResolvedGroups, st.PendingGroups, st.ReviewGroups, humanBytes(st.ReclaimableBytes))
	if st.AutoApproved > 0 {
		summary += fmt.Sprintf(", %d auto-approved", st.AutoApproved)
	}
	if st.Errors > 0 {
		summary += fmt.Sprintf(", %d errors", st.Errors)
	}
	data, _ := json.Marshal(struct {
		ScanID   int64            `json:"scanId"`
		Trigger  string           `json:"trigger"`
		Targeted bool             `json:"targeted"`
		Stats    models.ScanStats `json:"stats"`
		Error    string           `json:"error,omitempty"`
	}{run.ID, run.Trigger, run.Targeted, st, run.Error})

	ev := &models.HistoryEvent{EventType: models.EventScanCompleted, Title: kind + " completed", Message: summary, Data: data}
	if err != nil {
		ev.EventType, ev.Title, ev.Message = models.EventScanFailed, kind+" failed", run.Error
	}
	s.addHistory(ctx, ev)

	if err == nil {
		p.log.Info(kind+" completed", "groups", st.GroupsFound, "new", st.NewGroups, "resolved", st.ResolvedGroups,
			"pending", st.PendingGroups, "review", st.ReviewGroups, "autoApproved", st.AutoApproved, "errors", st.Errors,
			"duration", finished.Sub(run.StartedAt).Round(time.Millisecond))
	} else {
		p.log.Warn(kind+" failed", "error", upstreamerr.Message(err), "errors", st.Errors)
	}
	p.notify(ctx, err, kind, summary)

	if err != nil {
		return run, fmt.Errorf("%s: %w", strings.ToLower(kind), err)
	}
	return run, nil
}

// notify fires OnDuplicatesFound (new groups) and, for full scans, OnScanCompleted (targeted
// scans run on every *arr/Plex webhook; announcing each would flood the connections).
func (p *pipeline) notify(ctx context.Context, err error, kind, summary string) {
	n := p.s.d.Notifier
	if n == nil {
		return
	}
	st := p.run.Stats
	if err == nil && st.NewGroups > 0 {
		title := fmt.Sprintf("%d new duplicate groups found", st.NewGroups)
		if st.NewGroups == 1 {
			title = "1 new duplicate group found"
		}
		n.Notify(ctx, notifications.Message{
			Event: models.OnDuplicatesFound,
			Title: title,
			Body:  fmt.Sprintf("%s found %d new duplicate groups (%d in total).", kind, st.NewGroups, st.GroupsFound),
			Fields: []notifications.Field{
				{Name: "New", Value: fmt.Sprint(st.NewGroups), Inline: true},
				{Name: "Pending", Value: fmt.Sprint(st.PendingGroups), Inline: true},
				{Name: "Needs review", Value: fmt.Sprint(st.ReviewGroups), Inline: true},
				{Name: "Reclaimable", Value: humanBytes(st.ReclaimableBytes), Inline: true},
			},
			Severity: "info",
		})
	}
	if p.run.Targeted {
		return
	}
	msg := notifications.Message{
		Event:    models.OnScanCompleted,
		Title:    kind + " completed",
		Body:     summary,
		Severity: "info",
		Fields: []notifications.Field{
			{Name: "Groups", Value: fmt.Sprint(st.GroupsFound), Inline: true},
			{Name: "New", Value: fmt.Sprint(st.NewGroups), Inline: true},
			{Name: "Resolved", Value: fmt.Sprint(st.ResolvedGroups), Inline: true},
			{Name: "Errors", Value: fmt.Sprint(st.Errors), Inline: true},
		},
	}
	if err != nil {
		msg.Title, msg.Body, msg.Severity = kind+" failed", p.run.Error, "error"
	} else if st.Errors > 0 {
		msg.Severity = "warning"
	}
	n.Notify(ctx, msg)
}

// addHistory writes a history event and publishes it; failures are logged only.
func (s *Service) addHistory(ctx context.Context, ev *models.HistoryEvent) {
	if err := s.d.Store.History().Add(ctx, ev); err != nil {
		s.d.Log.Warn("Failed to write history event", "eventType", ev.EventType, "error", err)
		return
	}
	s.publish(events.NameHistory, events.ActionUpdated, *ev)
}

// humanBytes formats a byte count with binary units ("1.5 GB").
func humanBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
