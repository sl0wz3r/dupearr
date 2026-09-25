package scanner

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/mediainfo"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// queueState maps an *arr instance id to the item ids (Radarr movie ids / Sonarr series ids)
// that have active download/import queue entries.
type queueState map[int64]map[int64]bool

// busy reports whether the *arr item a version is tracked by has queue entries.
func (q queueState) busy(a *models.ArrFileInfo) bool {
	return a != nil && a.ItemID > 0 && q[a.InstanceID][a.ItemID]
}

// arrKindFor maps a media type to the *arr kind that manages it (radarr ↔ movie, sonarr ↔ episode).
func arrKindFor(mt models.MediaType) models.ArrKind {
	if mt == models.MediaTypeEpisode {
		return models.ArrSonarr
	}
	return models.ArrRadarr
}

// itemArrIDs returns the id tokens ("tmdb:603", "imdb:tt0133093", "tvdb:81189") by which the
// *arr of media type mt knows an item: movie-level tmdb/imdb ids for Radarr, show-level tvdb/imdb
// ids for Sonarr (episode listings carry episode-level ids only).
func itemArrIDs(it *models.MediaItem, mt models.MediaType) []string {
	ids, numeric := it.ExternalIDs, "tmdb"
	if mt == models.MediaTypeEpisode {
		ids, numeric = it.ShowIDs, "tvdb"
	}
	var out []string
	if id, err := strconv.Atoi(normID(numeric, ids[numeric])); err == nil && id > 0 {
		out = append(out, numeric+":"+strconv.Itoa(id))
	}
	if v := normID("imdb", ids["imdb"]); validIMDb(v) {
		out = append(out, "imdb:"+v)
	}
	return out
}

// trackedArrIDs returns the id tokens of the *arr item a tracked file belongs to (see itemArrIDs).
func trackedArrIDs(tf *arr.TrackedFile, mt models.MediaType) []string {
	var out []string
	if mt == models.MediaTypeEpisode {
		if tf.TvdbID > 0 {
			out = append(out, "tvdb:"+strconv.Itoa(tf.TvdbID))
		}
	} else if tf.TmdbID > 0 {
		out = append(out, "tmdb:"+strconv.Itoa(tf.TmdbID))
	}
	if v := normID("imdb", tf.ImdbID); validIMDb(v) {
		out = append(out, "imdb:"+v)
	}
	return out
}

// trackedFilter builds the *arr filter for the items of one media type: movie tmdb/imdb ids
// (Radarr) or show-level tvdb/imdb ids (Sonarr). n is the number of items of that type; ok is
// false when none of them carries a usable id (enrichment is then skipped: an unfiltered Sonarr
// read walks every series).
func trackedFilter(items []models.MediaItem, mt models.MediaType) (f arr.TrackedFilter, n int, ok bool) {
	tmdb, tvdb, imdb := map[int]bool{}, map[int]bool{}, map[string]bool{}
	for i := range items {
		it := &items[i]
		if it.MediaType != mt {
			continue
		}
		n++
		for _, tok := range itemArrIDs(it, mt) {
			space, v, _ := strings.Cut(tok, ":")
			switch space {
			case "imdb":
				imdb[v] = true
			case "tmdb":
				id, _ := strconv.Atoi(v)
				tmdb[id] = true
			case "tvdb":
				id, _ := strconv.Atoi(v)
				tvdb[id] = true
			}
		}
	}
	if len(tmdb) > 0 {
		f.TmdbIDs = tmdb
	}
	if len(tvdb) > 0 {
		f.TvdbIDs = tvdb
	}
	if len(imdb) > 0 {
		f.ImdbIDs = imdb
	}
	return f, n, f.TmdbIDs != nil || f.TvdbIDs != nil || f.ImdbIDs != nil
}

// enrich attaches *arr data to the versions of items (in place) and returns the queue state of
// every instance that could be read. For each media type, every enabled instance of the matching
// kind is read (tracked files of the candidate titles + queue); versions are matched to tracked
// files with a Matcher (mapped local path, raw path, then unique file name + size).
//
// Missing *arr data can hide a keep tag or tracked state, so it is never silent: an instance
// that cannot be read sends every group of that media type to review, and so does a candidate
// the *arrs cannot be asked about (no TMDB/IMDb/TVDB id on the media server). Items whose *arr
// title has an active queue entry are recorded as busy even when none of their versions matched
// a tracked file (docs/DECISIONS.md D3 A4).
func (p *pipeline) enrich(items []models.MediaItem) queueState {
	busy := queueState{}
	if len(items) == 0 || len(p.cfg.arrs) == 0 {
		return busy
	}
	for _, mt := range []models.MediaType{models.MediaTypeMovie, models.MediaTypeEpisode} {
		kind := arrKindFor(mt)
		var insts []models.ArrInstance
		for _, a := range p.cfg.arrs {
			if a.Kind == kind {
				insts = append(insts, a)
			}
		}
		if len(insts) == 0 {
			continue
		}
		filter, n, ok := trackedFilter(items, mt)
		if n == 0 {
			continue
		}
		p.noteUnlookable(items, mt, kind)
		if !ok {
			p.log.Info("No candidate carries an external id; skipping *arr enrichment", "mediaType", mt, "items", n)
			continue
		}
		matcher := NewMatcher(p.cfg.mapper)
		if p.cfg.multi {
			matcher.SetServers(p.cfg.matchPolicy())
		}
		unconfirmed := map[int64]*unconfirmedTitles{} // docs/DECISIONS.md D11
		busyIDs := map[string]bool{}
		for _, inst := range insts {
			if p.ctx.Err() != nil {
				return busy
			}
			files, queue, err := p.readArr(inst, filter)
			if err != nil {
				if p.ctx.Err() != nil {
					return busy
				}
				p.arrFailure(mt, inst, err)
				continue
			}
			matcher.Add(inst.ID, files)
			if p.cfg.multi && !p.cfg.confirmed[inst.ID] {
				unconfirmed[inst.ID] = p.unconfirmedTitles(inst, files, mt)
			}
			busy[inst.ID] = queue
			for i := range files {
				if id := files[i].Info.ItemID; id > 0 && queue[id] {
					for _, tok := range trackedArrIDs(&files[i], mt) {
						busyIDs[tok] = true
					}
				}
			}
		}
		p.markBusyItems(items, mt, busyIDs)
		matched := p.applyMatches(items, mt, matcher, unconfirmed)
		p.log.Debug("Matched versions to *arr files", "mediaType", mt, "matched", matched)
	}
	return busy
}

// versionMatch is one version matched to a tracked file.
type versionMatch struct {
	v    *models.MediaVersion
	tf   *arr.TrackedFile
	kind matchKind
}

// unconfirmedTitles are the titles an *arr instance whose media server links are not confirmed
// tracks (by id token), with whether each title's tracked file maps to a local path.
type unconfirmedTitles struct {
	inst   models.ArrInstance
	mapped map[string]bool // id token → every tracked file of the title maps
}

// unconfirmedTitles indexes the tracked files of an instance whose links are not confirmed.
func (p *pipeline) unconfirmedTitles(inst models.ArrInstance, files []arr.TrackedFile, mt models.MediaType) *unconfirmedTitles {
	u := &unconfirmedTitles{inst: inst, mapped: map[string]bool{}}
	for i := range files {
		_, ok := p.cfg.mapper.ToLocal(models.PathSourceArr, inst.ID, files[i].Path)
		for _, tok := range trackedArrIDs(&files[i], mt) {
			if prev, seen := u.mapped[tok]; seen {
				u.mapped[tok] = prev && ok
			} else {
				u.mapped[tok] = ok
			}
		}
	}
	return u
}

// arrTrackingUnknownReason marks the incomplete-data reason of a group with a version whose *arr
// tracking is unknown (see ArrTrackingUnknown).
const arrTrackingUnknownReason = "(whether an *arr tracks a version is unknown)"

// ArrTrackingUnknown reports whether g is in review because the *arr tracking of one of its
// versions is unknown (docs/DECISIONS.md D11): a mapped *arr file and a version of a media server
// without a mapping for it, or an instance whose media server links are not confirmed. It must not
// be approved until a scan could decide (add the path mapping, or confirm the links).
func ArrTrackingUnknown(g *models.DuplicateGroup) bool {
	return g != nil && g.Status == models.GroupReview && strings.HasPrefix(g.StatusReason, incompletePrefix) &&
		strings.Contains(g.StatusReason, arrTrackingUnknownReason)
}

// noteArrUnknown records why the *arr tracking of version v is unknown.
func (p *pipeline) noteArrUnknown(v *models.MediaVersion, reason string) {
	if v.Key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.arrUnknown[v.Key]; !ok {
		p.arrUnknown[v.Key] = reason + " " + arrTrackingUnknownReason
	}
}

// serverName names a media server in messages.
func (p *pipeline) serverName(id int64) string {
	if s, ok := p.cfg.servers[id]; ok && strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	return fmt.Sprintf("media server #%d", id)
}

// applyMatches matches the versions of the items of media type mt and attaches the results. A
// file-name + size match is a heuristic and is dropped when it is not unambiguous on the
// version side too: another candidate version has a part with the same name and size, or the
// tracked file was matched by another version as well (deleting it through the *arr would then
// remove a file other than the version's own). Returns the number of versions matched.
//
// With two or more servers (docs/DECISIONS.md D11), a version that is not matched while its *arr
// tracking cannot be decided is recorded as unknown (arrUnknown): the matcher refused a mapped *arr
// file for a part the server has no mapping for, or an instance whose links are not confirmed
// tracks a file of the same title that rule 1 could not compare (either side unmapped).
func (p *pipeline) applyMatches(items []models.MediaItem, mt models.MediaType, matcher *Matcher, unconfirmed map[int64]*unconfirmedTitles) int {
	names := map[nameSize]int{} // candidate versions having a part with this name + size
	var found []versionMatch
	for i := range items {
		if items[i].MediaType != mt {
			continue
		}
		for j := range items[i].Versions {
			v := &items[i].Versions[j]
			if v.OptimizedVersion {
				continue
			}
			seen := map[nameSize]bool{}
			for _, part := range v.Parts {
				if n := nameKey(part.Path); n != "" && part.Size > 0 {
					ns := nameSize{name: n, size: part.Size}
					if !seen[ns] {
						seen[ns] = true
						names[ns]++
					}
				}
			}
			var (
				tf      *arr.TrackedFile
				kind    matchKind
				unknown string
			)
			if v.Disc != nil {
				tf, kind, unknown = matcher.matchDisc(v.ServerID, v)
			} else {
				tf, kind, unknown = matcher.match(v.ServerID, v)
			}
			if tf != nil {
				found = append(found, versionMatch{v: v, tf: tf, kind: kind})
				continue
			}
			if unknown != "" {
				p.noteArrUnknown(v, fmt.Sprintf("add a path mapping for %s: its version may be a file an *arr tracks", p.serverName(v.ServerID)))
				continue
			}
			p.noteUnconfirmed(&items[i], v, mt, unconfirmed)
		}
	}
	claims := map[trackedRef]int{}
	for _, m := range found {
		claims[refOfTracked(m.tf)]++
	}
	matched := 0
	for _, m := range found {
		if m.kind == matchName {
			ns := nameSize{name: nameKey(m.tf.Path), size: m.tf.Size}
			if names[ns] > 1 || claims[refOfTracked(m.tf)] > 1 {
				p.log.Debug("Ambiguous file name + size match ignored", "version", m.v.Key, "file", path.Base(pathmap.Normalize(m.tf.Path)))
				continue
			}
		}
		v, tf := m.v, m.tf
		if v.Disc != nil {
			// A disc keeps its own attributes: the *arr file is one clip (or an image without
			// media info), and its size never describes the disc.
			discTracked(v, cloneArrInfo(tf.Info), tf.Path)
			matched++
			continue
		}
		applyArr(v, tf)
		matched++
		if sizeMismatch(v, tf) {
			name := strings.TrimSpace(tf.Info.InstanceName)
			if name == "" {
				name = "the *arr"
			}
			p.markStale(v.Key, fmt.Sprintf("%s reports %s for %q but the media server reports %s (one of them has not rescanned it)",
				name, humanBytes(tf.Size), path.Base(pathmap.Normalize(tf.Path)), humanBytes(v.TotalSize())))
		}
	}
	return matched
}

// noteUnconfirmed records an unmatched version whose *arr tracking an instance with unconfirmed
// links could hold: it tracks a file of the same title and rule 1 could not compare the two
// (the version or that file is unmapped).
func (p *pipeline) noteUnconfirmed(it *models.MediaItem, v *models.MediaVersion, mt models.MediaType, unconfirmed map[int64]*unconfirmedTitles) {
	if len(unconfirmed) == 0 {
		return
	}
	versionMapped := len(v.Parts) > 0
	for _, pt := range v.Parts {
		if _, ok := p.cfg.mapper.ToLocal(models.PathSourceServer, v.ServerID, pt.Path); !ok {
			versionMapped = false
		}
	}
	for _, id := range sortedIDs(unconfirmed) {
		u := unconfirmed[id]
		for _, tok := range itemArrIDs(it, mt) {
			fileMapped, tracks := u.mapped[tok]
			if tracks && (!versionMapped || !fileMapped) {
				p.noteArrUnknown(v, fmt.Sprintf("confirm which Plex servers %s feeds (Settings → Applications)", u.inst.Name))
				return
			}
		}
	}
}

// noteUnlookable records the items of media type mt the *arrs of kind cannot be asked about:
// the media server reports none of the ids the *arr filter uses. Their groups go to review — a
// keep tag or tracked state in the *arr would go unnoticed.
func (p *pipeline) noteUnlookable(items []models.MediaItem, mt models.MediaType, kind models.ArrKind) {
	app := "Radarr"
	what := "a TMDB or IMDb id"
	if kind == models.ArrSonarr {
		app, what = "Sonarr", "a show TVDB or IMDb id"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range items {
		it := &items[i]
		if it.MediaType != mt || len(itemArrIDs(it, mt)) > 0 {
			continue
		}
		p.arrUnlookable[refKey{server: it.ServerID, rk: it.RatingKey}] =
			fmt.Sprintf("%q could not be looked up in %s (the media server reports no %s for it)", itemLabel(it), app, what)
	}
}

// markBusyItems records the items of media type mt whose *arr title (by id) has active queue
// entries.
func (p *pipeline) markBusyItems(items []models.MediaItem, mt models.MediaType, busyIDs map[string]bool) {
	if len(busyIDs) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range items {
		it := &items[i]
		if it.MediaType != mt {
			continue
		}
		for _, tok := range itemArrIDs(it, mt) {
			if busyIDs[tok] {
				p.busyItems[refKey{server: it.ServerID, rk: it.RatingKey}] = true
				break
			}
		}
	}
}

// itemLabel is a short human label of an item ("Heat (1995)", "Show S01E02").
func itemLabel(it *models.MediaItem) string {
	switch {
	case it.MediaType == models.MediaTypeEpisode && it.ShowTitle != "":
		return it.ShowTitle + " " + engine.EpisodeLabel(it.Season, it.Episode)
	case it.Title != "" && it.Year > 0:
		return fmt.Sprintf("%s (%d)", it.Title, it.Year)
	case it.Title != "":
		return it.Title
	}
	return "item " + it.RatingKey
}

// readArr reads the tracked files and queue of one instance. A panicking client is converted
// into an error.
func (p *pipeline) readArr(inst models.ArrInstance, filter arr.TrackedFilter) (files []arr.TrackedFile, queue map[int64]bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			files, queue, err = nil, nil, fmt.Errorf("unexpected panic: %v", r)
		}
	}()
	if p.s.d.ArrFactory == nil {
		return nil, nil, errors.New("no *arr client factory configured")
	}
	client := p.s.d.ArrFactory(inst)
	if client == nil {
		return nil, nil, errors.New("no client available")
	}
	p.progress(fmt.Sprintf("Reading tracked files from %s", inst.Name))
	err = p.retryArr(inst, "tracked files", func() (e error) {
		files, e = client.TrackedFiles(p.ctx, filter)
		return e
	})
	if err != nil {
		return nil, nil, fmt.Errorf("tracked files: %w", err)
	}
	err = p.retryArr(inst, "download queue", func() (e error) {
		queue, e = client.QueueItemIDs(p.ctx)
		return e
	})
	if err != nil {
		return nil, nil, fmt.Errorf("download queue: %w", err)
	}
	if queue == nil {
		queue = map[int64]bool{}
	}
	return files, queue, nil
}

// retryArr runs read once more after a pause when it fails with an error that may be transient:
// an *arr title that changed while it was read ("changed during the scan"), a restart, a timeout.
// Configuration errors (credentials, wrong application, redirects, bad input) and cancellation
// are returned at once.
func (p *pipeline) retryArr(inst models.ArrInstance, what string, read func() error) error {
	err := read()
	if err == nil || !retryableArrError(err) || p.ctx.Err() != nil {
		return err
	}
	p.log.Info("Reading an *arr instance failed; retrying once", "instance", inst.Name, "read", what, "error", upstreamerr.Message(err))
	t := time.NewTimer(p.s.arrRetryDelay)
	defer t.Stop()
	select {
	case <-p.ctx.Done():
		return err
	case <-t.C:
	}
	return read()
}

// retryableArrError reports errors worth one more attempt.
func retryableArrError(err error) bool {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, arr.ErrUnauthorized), errors.Is(err, arr.ErrWrongApp),
		errors.Is(err, arr.ErrRedirect), errors.Is(err, arr.ErrInvalidArgument):
		return false
	}
	return true
}

// arrUnreadableReason marks the incomplete-data reason of groups whose *arr data is missing
// because an instance could not be read (see ArrDataMissing).
const arrUnreadableReason = "(keep tags, tracked files and the download queue may be missing)"

// ArrDataMissing reports whether g was sent to review because an *arr instance could not be read
// during its last scan: files that instance tracks then look untracked (a keep tag, tracked state
// or an active download may be missing), so the group must not be approved until a scan read
// every instance (re-scan it, or disable the unreachable instance).
func ArrDataMissing(g *models.DuplicateGroup) bool {
	return g != nil && g.Status == models.GroupReview && strings.HasPrefix(g.StatusReason, incompletePrefix) &&
		strings.Contains(g.StatusReason, arrUnreadableReason)
}

// arrFailure records an *arr instance that could not be read for media type mt.
func (p *pipeline) arrFailure(mt models.MediaType, inst models.ArrInstance, err error) {
	name := inst.Name
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s #%d", inst.Kind, inst.ID)
	}
	p.mu.Lock()
	if !slices.Contains(p.arrFailed[mt], name) {
		p.arrFailed[mt] = append(p.arrFailed[mt], name)
	}
	p.run.Stats.Errors++
	p.mu.Unlock()
	p.log.Warn("Could not read *arr instance; groups of this media type are sent to review",
		"instance", name, "instanceId", inst.ID, "mediaType", mt, "error", upstreamerr.Message(err))
}

// applyArr attaches a tracked file to a version and fills attributes the media server lacks:
// the source (when Plex could not tell), an HDR format the *arr detected on a file Plex reports as
// SDR (or unknown), and the edition.
func applyArr(v *models.MediaVersion, tf *arr.TrackedFile) {
	info := cloneArrInfo(tf.Info)
	v.Arr = &info
	if v.Source == "" || v.Source == models.SourceUnknown {
		if src := mediainfo.SourceFromArr(info.QualitySource, info.QualityModifier); src != "" && src != models.SourceUnknown {
			v.Source = src
		}
	}
	if t := strings.TrimSpace(info.DynamicRangeType); t != "" && (v.DynamicRange == "" || v.DynamicRange == models.DRSDR) {
		if dr := mediainfo.DynamicRangeFromArr(t); dr != "" && (dr != models.DRSDR || v.DynamicRange == "") {
			v.DynamicRange = dr
		}
	}
	if strings.TrimSpace(v.Edition) == "" && strings.TrimSpace(info.Edition) != "" {
		v.Edition = mediainfo.Edition("", "", info.Edition)
	}
}

// cloneArrInfo deep-copies an ArrFileInfo (slices and the score pointer), with non-nil slices.
func cloneArrInfo(in models.ArrFileInfo) models.ArrFileInfo {
	out := in
	out.EpisodeIDs = append([]int64{}, in.EpisodeIDs...)
	out.CustomFormats = append([]string{}, in.CustomFormats...)
	out.Languages = append([]string{}, in.Languages...)
	out.Tags = append([]string{}, in.Tags...)
	if in.CustomFormatScore != nil {
		s := *in.CustomFormatScore
		out.CustomFormatScore = &s
	}
	return out
}

// sizeMismatch reports a tracked file whose size matches none of the version's parts (both
// sizes known): the *arr and the media server describe different contents at the same place.
func sizeMismatch(v *models.MediaVersion, tf *arr.TrackedFile) bool {
	if tf.Size <= 0 {
		return false
	}
	for _, p := range v.Parts {
		if p.Size <= 0 || p.Size == tf.Size {
			return false
		}
	}
	return len(v.Parts) > 0
}
