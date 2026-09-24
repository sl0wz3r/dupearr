package scanner

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/tautulli"
	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Play history (docs/DECISIONS.md D10, docs/research/watch-history.md): read from each media
// server's Tautulli after the *arr data and stored with every version (MediaVersion.Watch), so
// evaluations — also the re-evaluations after a profile change — work from this scan's snapshot.
//
// Tautulli records plays per Plex item (rating key), for every user, never per file: the versions
// of one item share one history. The history is only ever used as evidence of plays; its absence
// proves nothing unless every condition below holds, and every failure is "failed", never "no plays":
//
//   - the connection monitors this very Plex server (its machine identifier), and every answer was
//     read completely (the client refuses cut-short, inconsistent or unexpected answers);
//   - "no plays recorded" additionally needs: the library and every active user keep history, the
//     item was added on or after the library's earliest recorded play, the item is matched (a guid)
//     and no play of the same guid in the library lies under a rating key the library no longer
//     lists (Plex re-created the item: its plays stayed under the old key). It is recorded since
//     the item's date added, and loses only to a play from then on (the engine's floor).

// watchChunk is the number of candidate rating keys per history read (plus the canary key).
const watchChunk = 49

// DefaultWatchMaxRows caps the rows one server's reads may return in a scan; beyond it the read
// fails (review) rather than deciding on a truncated history.
const DefaultWatchMaxRows = tautulli.DefaultMaxRows

// watchGUIDPage is the page size of the per-title guid reads (the churn guard).
const watchGUIDPage = 100

var reWatchKey = regexp.MustCompile(`^[0-9A-Za-z]{1,32}$`)

// watchRead is what one successful read of a server's Tautulli established.
type watchRead struct {
	rows     map[string][]tautulli.HistoryRow // candidate rating key → its recorded plays
	since    map[string]time.Time             // section key → earliest recorded play
	libKeep  map[string]*bool                 // section key → keeps history (nil: not reported)
	usersWhy string                           // why not every user keeps history ("" = all do)
	churnWhy map[string]string                // candidate rating key → why a zero is not provable
}

// watch attaches the play history of each item's media server to its versions (in place).
// Servers without an enabled Tautulli connection are left alone (Watch stays nil).
func (p *pipeline) watch(items []models.MediaItem) {
	if len(items) == 0 || len(p.cfg.tautulli) == 0 {
		return
	}
	byServer := map[int64][]int{}
	for i := range items {
		if _, ok := p.cfg.tautulli[items[i].ServerID]; ok {
			byServer[items[i].ServerID] = append(byServer[items[i].ServerID], i)
		}
	}
	servers := make([]int64, 0, len(byServer))
	for id := range byServer {
		servers = append(servers, id)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i] < servers[j] })
	for _, id := range servers {
		if p.ctx.Err() != nil {
			return
		}
		inst, srv, idx := p.cfg.tautulli[id], p.cfg.servers[id], byServer[id]
		if strings.TrimSpace(srv.MachineIdentifier) == "" {
			srv.MachineIdentifier = p.storedIdentity(srv)
		}
		p.progress(fmt.Sprintf("Reading play history from %s", inst.Name))
		res, err := p.readWatch(inst, srv, items, idx)
		now := p.s.now()
		if err != nil {
			if p.ctx.Err() != nil {
				return // the scan is aborted; nothing is stored
			}
			p.watchFailed(inst, srv, items, idx, err, now)
			continue
		}
		p.applyWatch(inst, items, idx, res, now)
	}
}

// storedIdentity returns the machine identifier stored for a server loaded without one: this scan
// may have just learned it (adoptIdentity, while listing the server). Only for the same URL and
// token, i.e. the very server the scan listed ("" otherwise, and the read fails as unmatched).
func (p *pipeline) storedIdentity(srv models.MediaServer) string {
	cur, err := p.s.d.Store.MediaServers().Get(p.ctx, srv.ID)
	if err != nil || cur == nil || cur.URL != srv.URL || cur.Token != srv.Token {
		return ""
	}
	return strings.TrimSpace(cur.MachineIdentifier)
}

// watchFailed marks every version of the server's items "failed": an unreadable history never
// counts as "no plays", and a group ranked by it goes to review (engine flag watch_unreadable).
func (p *pipeline) watchFailed(inst models.TautulliInstance, srv models.MediaServer, items []models.MediaItem, idx []int, err error, now time.Time) {
	reason := strings.TrimPrefix(errorText(err), "tautulli: ")
	p.addError()
	p.log.Warn("Could not read the play history; groups ranked by it are sent to review",
		"tautulli", inst.Name, "server", srv.Name, "error", upstreamerr.Message(err))
	for _, i := range idx {
		for j := range items[i].Versions {
			items[i].Versions[j].Watch = &models.WatchInfo{Source: models.WatchSourceTautulli, SourceName: inst.Name,
				Status: models.WatchFailed, Reason: reason, ReadAt: now}
		}
	}
}

// applyWatch resolves the history of each item and attaches it to its versions.
func (p *pipeline) applyWatch(inst models.TautulliInstance, items []models.MediaItem, idx []int, res *watchRead, now time.Time) {
	known, zero := 0, 0
	for _, i := range idx {
		it := &items[i]
		w := resolveWatch(it, res)
		w.Source, w.SourceName, w.ReadAt = models.WatchSourceTautulli, inst.Name, now
		switch {
		case w.Status == models.WatchKnown && w.Plays > 0:
			known++
		case w.Status == models.WatchKnown:
			zero++
		}
		for j := range it.Versions {
			v := &it.Versions[j]
			vw := w
			if why := discWatchReason(v); why != "" {
				// A disc shares the movie's rating key: the item's plays are not its plays.
				vw = models.WatchInfo{Source: w.Source, SourceName: w.SourceName, Status: models.WatchUnknown, Reason: why, ReadAt: now}
			}
			v.Watch = &vw
		}
	}
	p.log.Debug("Read the play history", "tautulli", inst.Name, "items", len(idx), "played", known, "noPlaysRecorded", zero)
}

// discWatchReason explains why the item's plays cannot be a full disc's ("" for a regular version).
func discWatchReason(v *models.MediaVersion) string {
	switch {
	case v.Disc == nil:
		return ""
	case v.Disc.Origin == models.DiscOriginFilesystem:
		return "full-disc backup found on disk; Plex cannot play it"
	}
	return "full-disc backup; the plays of its Plex item cannot be attributed to it"
}

// resolveWatch derives an item's play history from a complete read (docs/DECISIONS.md D10):
// recorded plays are evidence; zero plays is "no plays recorded" only when nothing could have
// hidden a play, otherwise unknown with the first reason that applies.
func resolveWatch(it *models.MediaItem, res *watchRead) models.WatchInfo {
	section := strings.TrimSpace(it.SectionKey)
	since := res.since[section]
	if rows := res.rows[it.RatingKey]; len(rows) > 0 {
		users := map[int64]bool{}
		var last time.Time
		for _, r := range rows {
			users[r.UserID] = true
			if t := r.PlayedAt(); t.After(last) {
				last = t
			}
		}
		return models.WatchInfo{Status: models.WatchKnown, Plays: len(rows), Users: len(users), LastPlayed: last, Since: since}
	}
	unknown := func(reason string) models.WatchInfo {
		return models.WatchInfo{Status: models.WatchUnknown, Reason: reason}
	}
	keep, keepKnown := res.libKeep[section]
	guid := strings.TrimSpace(it.GUID)
	switch {
	case !reWatchKey.MatchString(it.RatingKey) || !reWatchKey.MatchString(section):
		return unknown("the item cannot be looked up in Tautulli")
	case !keepKnown || keep == nil:
		return unknown("Tautulli does not report whether this library keeps history")
	case !*keep:
		return unknown("Tautulli does not keep history for this library")
	case res.usersWhy != "":
		return unknown(res.usersWhy)
	case since.IsZero():
		return unknown("no play is recorded in this library yet")
	case it.AddedAt.IsZero():
		return unknown("the media server reports no date added for the item")
	case it.AddedAt.Before(since):
		return unknown("added before the recorded history starts (" + since.UTC().Format("2006-01-02") + ")")
	case guid == "" || strings.HasPrefix(strings.ToLower(guid), "local://"):
		return unknown("the item is not matched in Plex, so plays under an earlier Plex item cannot be ruled out")
	}
	if why, ok := res.churnWhy[it.RatingKey]; !ok || why != "" {
		if why == "" {
			why = "the plays of this title in its library could not be checked"
		}
		return unknown(why)
	}
	// No plays since the item was added (it was added after the history starts): the date that
	// counts is when this copy's item appeared — a play of another copy from before then does not
	// show that people passed this one over (docs/DECISIONS.md D10).
	return models.WatchInfo{Status: models.WatchKnown, Since: it.AddedAt}
}

// retryWatch runs read once more after a pause when it fails with an error that may be transient
// (a restart, a timeout, a 5xx). Configuration errors, incomplete answers and cancellation are
// returned at once.
func (p *pipeline) retryWatch(ctx context.Context, inst models.TautulliInstance, what string, read func() error) error {
	err := read()
	if err == nil || !tautulli.Transient(err) || ctx.Err() != nil {
		return err
	}
	p.log.Info("Reading Tautulli failed; retrying once", "tautulli", inst.Name, "read", what, "error", upstreamerr.Message(err))
	t := time.NewTimer(p.s.arrRetryDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return err
	case <-t.C:
	}
	return read()
}

// readWatch reads everything the items of one server need, or fails as a whole.
func (p *pipeline) readWatch(inst models.TautulliInstance, srv models.MediaServer, items []models.MediaItem, idx []int) (*watchRead, error) {
	if p.s.d.TautulliFactory == nil {
		return nil, errors.New("no Tautulli client is available")
	}
	c := p.s.d.TautulliFactory(inst)
	if c == nil {
		return nil, errors.New("no Tautulli client is available")
	}
	want := strings.TrimSpace(srv.MachineIdentifier)
	if want == "" {
		// Without the server's identity, a Tautulli of another Plex server cannot be told apart.
		return nil, fmt.Errorf("media server %q has no stored machine identifier to check Tautulli against (re-save it with a connection test)", srv.Name)
	}
	ctx := p.ctx
	var info *tautulli.Info
	if err := p.retryWatch(ctx, inst, "server info", func() (e error) { info, e = c.Info(ctx); return e }); err != nil {
		return nil, err
	}
	if info == nil || !strings.EqualFold(strings.TrimSpace(info.PMSIdentifier), want) {
		got := ""
		if info != nil {
			got = info.PMSIdentifier
		}
		if got == "" {
			return nil, fmt.Errorf("the Tautulli connection does not report which Plex server it monitors, so its history cannot be matched to %s", srv.Name)
		}
		return nil, fmt.Errorf("the Tautulli connection monitors another Plex server (machine identifier %s), not %s: check its URL in Settings → Applications",
			got, srv.Name)
	}

	res := &watchRead{rows: map[string][]tautulli.HistoryRow{}, since: map[string]time.Time{}, libKeep: map[string]*bool{},
		churnWhy: map[string]string{}}
	var users []tautulli.User
	if err := p.retryWatch(ctx, inst, "users", func() (e error) { users, e = c.Users(ctx); return e }); err != nil {
		return nil, err
	}
	res.usersWhy = usersKeepHistory(users)

	sections := map[string]bool{}
	keys := map[string]bool{}
	for _, i := range idx {
		it := &items[i]
		if s := strings.TrimSpace(it.SectionKey); reWatchKey.MatchString(s) {
			sections[s] = true
		}
		if reWatchKey.MatchString(it.RatingKey) {
			keys[it.RatingKey] = true
		}
	}
	canary := ""
	for _, s := range sortedSet(sections) {
		var lib *tautulli.Library
		if err := p.retryWatch(ctx, inst, "library", func() (e error) { lib, e = c.Library(ctx, s); return e }); err != nil {
			return nil, err
		}
		if lib != nil {
			res.libKeep[s] = lib.KeepHistory
		}
		var first *tautulli.HistoryRow
		if err := p.retryWatch(ctx, inst, "history start", func() (e error) { first, e = c.FirstPlay(ctx, s); return e }); err != nil {
			return nil, err
		}
		if first != nil && !first.Started.IsZero() {
			res.since[s] = first.Started
			if canary == "" {
				canary = first.RatingKey
			}
		}
	}

	// Plays by rating key, in chunks. Every read also asks for the canary — the rating key of a
	// recorded play — so a Tautulli that does not read the comma-separated list (UNVERIFIED,
	// docs/research/watch-history.md) is caught: its answer lacks the canary's plays, instead of
	// looking like an empty history. A canary that is itself a candidate counts only in its own
	// chunk.
	maxRows := p.s.watchMaxRows
	if maxRows <= 0 {
		maxRows = DefaultWatchMaxRows
	}
	budget := maxRows
	list := sortedSet(keys)
	for start := 0; start < len(list); start += watchChunk {
		chunk := list[start:min(len(list), start+watchChunk)]
		ask := append([]string(nil), chunk...)
		added := canary != "" && !slices.Contains(chunk, canary)
		if added {
			ask = append(ask, canary)
		}
		if budget <= 0 { // a MaxRows of 0 would mean the client's default, not "none left"
			return nil, fmt.Errorf("%w: more than %d recorded plays", tautulli.ErrIncomplete, maxRows)
		}
		var rows []tautulli.HistoryRow
		if err := p.retryWatch(ctx, inst, "history", func() (e error) {
			rows, e = c.History(ctx, tautulli.HistoryFilter{RatingKeys: ask, MaxRows: budget})
			return e
		}); err != nil {
			return nil, err
		}
		if budget -= len(rows); budget < 0 {
			return nil, fmt.Errorf("%w: more than %d recorded plays", tautulli.ErrIncomplete, maxRows)
		}
		sawCanary := false
		for _, r := range rows {
			if r.RatingKey == canary {
				sawCanary = true
				if added {
					continue // asked for the check only
				}
			}
			res.rows[r.RatingKey] = append(res.rows[r.RatingKey], r)
		}
		if canary != "" && !sawCanary {
			return nil, fmt.Errorf("%w: Tautulli did not return the plays of rating key %s, which it listed a moment ago (its rating-key filter cannot be trusted)",
				tautulli.ErrIncomplete, canary)
		}
	}
	if err := p.churnGuard(ctx, inst, c, items, idx, res, budget, maxRows); err != nil {
		return nil, err
	}
	return res, nil
}

// usersKeepHistory explains why "no plays recorded" cannot be concluded for the whole server
// because of Tautulli's users ("" when every active user keeps history).
func usersKeepHistory(users []tautulli.User) string {
	if len(users) == 0 {
		return "Tautulli reports no users"
	}
	for _, u := range users {
		switch {
		case !u.Active:
		case u.KeepHistory == nil:
			return "Tautulli does not report whether every user's history is kept"
		case !*u.KeepHistory:
			return "Tautulli does not keep history for every user"
		}
	}
	return ""
}

// churnGuard checks, for every item whose zero plays would otherwise be concluded, that no play of
// its guid in its library lies under a rating key the library no longer lists: Plex re-creates an
// item (new rating key) when it is removed and added again, and Tautulli keeps the old plays under
// the old key. Rows under another key the library still lists (another edition, a version split
// into its own item) are other items' plays. Reads run with at most Deps.Concurrency in flight.
func (p *pipeline) churnGuard(ctx context.Context, inst models.TautulliInstance, c WatchClient, items []models.MediaItem, idx []int, res *watchRead, budget, maxRows int) error {
	type job struct {
		rk, guid, section string
		lib               int64
		server            int64
	}
	var jobs []job
	seen := map[string]bool{}
	for _, i := range idx {
		it := &items[i]
		section := strings.TrimSpace(it.SectionKey)
		if len(res.rows[it.RatingKey]) > 0 || seen[it.RatingKey] {
			continue
		}
		probe := resolveWatch(it, &watchRead{rows: res.rows, since: res.since, libKeep: res.libKeep, usersWhy: res.usersWhy,
			churnWhy: map[string]string{it.RatingKey: ""}})
		if probe.Status != models.WatchKnown {
			continue // unknown anyway: no need to ask
		}
		seen[it.RatingKey] = true
		if !p.listed[it.LibraryID] {
			res.churnWhy[it.RatingKey] = "its library could not be listed completely, so plays under an earlier Plex item cannot be ruled out"
			continue
		}
		jobs = append(jobs, job{rk: it.RatingKey, guid: strings.TrimSpace(it.GUID), section: section, lib: it.LibraryID, server: it.ServerID})
	}
	if len(jobs) == 0 {
		return nil
	}
	if budget <= 0 {
		return fmt.Errorf("%w: more than %d recorded plays", tautulli.ErrIncomplete, maxRows)
	}
	// The rating keys each library lists in this scan (read-only while the reads run).
	listing := map[int64]map[string]bool{}
	for _, j := range jobs {
		if listing[j.lib] != nil {
			continue
		}
		set := map[string]bool{}
		for _, k := range p.index.byLibrary[j.lib] {
			if k.server == j.server {
				set[k.rk] = true
			}
		}
		listing[j.lib] = set
	}
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
		used     int
	)
	sem := make(chan struct{}, p.s.d.Concurrency)
	for _, j := range jobs {
		if gctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("unexpected panic: %v", r)
					}
					mu.Unlock()
					cancel()
				}
			}()
			var rows []tautulli.HistoryRow
			err := p.retryWatch(gctx, inst, "title history", func() (e error) {
				rows, e = c.History(gctx, tautulli.HistoryFilter{GUID: j.guid, SectionID: j.section, PageSize: watchGUIDPage, MaxRows: budget})
				return e
			})
			why := ""
			for _, r := range rows {
				if r.GUID != j.guid {
					continue // Tautulli matches the guid as a prefix
				}
				switch {
				case r.RatingKey == j.rk:
					why = "the play history changed while it was read"
				case !listing[j.lib][r.RatingKey]:
					why = "plays of this title in this library are recorded under an earlier Plex item"
				}
				if why != "" {
					break
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				cancel()
				return
			}
			if used += len(rows); used > budget {
				if firstErr == nil {
					firstErr = fmt.Errorf("%w: more than %d recorded plays", tautulli.ErrIncomplete, maxRows)
				}
				cancel()
				return
			}
			res.churnWhy[j.rk] = why
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// sortedSet returns the members of a set in order.
func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
