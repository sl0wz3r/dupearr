package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const (
	maxBulkIDs      = 1000
	maxSearchLength = 200
	maxMessageLen   = 1000
)

var knownGroupStatuses = []models.GroupStatus{
	models.GroupPending, models.GroupReview, models.GroupDeferred, models.GroupProtected,
	models.GroupQueued, models.GroupResolved, models.GroupIgnored, models.GroupFailed,
}

// resolutionRank orders resolution tiers (higher is better).
var resolutionRank = map[string]int{
	models.Res2160: 7, models.Res1440: 6, models.Res1080: 5, models.Res720: 4,
	models.Res576: 3, models.Res480: 2, models.ResSD: 1,
}

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// duplicateFileSummary is the per-file part of a DuplicateGroupSummary.
type duplicateFileSummary struct {
	ID              int64               `json:"id"`
	Decision        models.Decision     `json:"decision"`
	Resolution      string              `json:"resolution"`
	DynamicRange    models.DynamicRange `json:"dynamicRange"`
	VideoCodec      string              `json:"videoCodec"`
	Size            int64               `json:"size"`
	LibraryTitle    string              `json:"libraryTitle"`
	ArrInstanceName string              `json:"arrInstanceName,omitempty"`
	// Disc summarizes a full-disc backup; absent for a regular file (the API never sends null).
	Disc *duplicateFileDisc `json:"disc,omitempty"`
	// DiscClip marks a regular version whose file is a file of a disc — a loose clip
	// ("…/00174.m2ts", disc.IsLooseClipPath) or a file inside BDMV/, VIDEO_TS/ … (disc.IsDiscPath):
	// it is never removed on its own, so the list never offers to approve its removal.
	DiscClip bool `json:"discClip,omitempty"`
}

// duplicateFileDisc is the disc part of a DuplicateGroupSummary file (the list shows no paths).
type duplicateFileDisc struct {
	Type      string `json:"type"`
	FileCount int    `json:"fileCount"`
	Discs     int    `json:"discs"`
	// ClipCount is the number of clips of a loose clip set (bluray_clips, dvd_clips); absent
	// otherwise.
	ClipCount int `json:"clipCount,omitempty"`
}

// duplicateSummary is the DuplicateGroupSummary list row (docs/API.md).
type duplicateSummary struct {
	ID               int64              `json:"id"`
	Key              string             `json:"key"`
	MediaType        models.MediaType   `json:"mediaType"`
	Title            string             `json:"title"`
	Year             int                `json:"year"`
	ShowTitle        string             `json:"showTitle,omitempty"`
	Season           int                `json:"season,omitempty"`
	Episode          int                `json:"episode,omitempty"`
	ServerID         int64              `json:"serverId"`
	LibraryIDs       []int64            `json:"libraryIds"`
	Thumb            string             `json:"thumb,omitempty"`
	Status           models.GroupStatus `json:"status"`
	StatusReason     string             `json:"statusReason,omitempty"`
	Flags            []string           `json:"flags"`
	ProfileID        int64              `json:"profileId"`
	FileCount        int                `json:"fileCount"`
	KeepCount        int                `json:"keepCount"`
	RemoveCount      int                `json:"removeCount"`
	ReclaimableBytes int64              `json:"reclaimableBytes"`
	BestResolution   string             `json:"bestResolution"`
	FirstSeenAt      time.Time          `json:"firstSeenAt"`
	LastSeenAt       time.Time          `json:"lastSeenAt"`
	// Signature identifies the group's current versions + decisions; a client may send it back
	// on approve so that a decision changed by a scan since it was displayed is not approved.
	Signature string                 `json:"signature"`
	Files     []duplicateFileSummary `json:"files"`
}

// summarize converts a group into its list DTO.
func summarize(g *models.DuplicateGroup) duplicateSummary {
	s := duplicateSummary{
		ID: g.ID, Key: g.Key, MediaType: g.MediaType, Title: g.Title, Year: g.Year,
		ShowTitle: g.ShowTitle, Season: g.Season, Episode: g.Episode, ServerID: g.ServerID,
		LibraryIDs: g.LibraryIDs, Thumb: g.Thumb, Status: g.Status, StatusReason: g.StatusReason,
		Flags: g.Flags, ProfileID: g.ProfileID, FileCount: len(g.Files),
		ReclaimableBytes: g.ReclaimableBytes, FirstSeenAt: g.FirstSeenAt, LastSeenAt: g.LastSeenAt,
		Signature: g.Signature, Files: make([]duplicateFileSummary, 0, len(g.Files)),
	}
	best := 0
	for i := range g.Files {
		f := &g.Files[i]
		v := &f.Version
		switch f.Decision {
		case models.DecisionKeep:
			s.KeepCount++
		case models.DecisionRemove:
			s.RemoveCount++
		}
		if rank := resolutionRank[v.Resolution]; rank > best {
			best, s.BestResolution = rank, v.Resolution
		}
		fs := duplicateFileSummary{
			ID: f.ID, Decision: f.Decision, Resolution: v.Resolution, DynamicRange: v.DynamicRange,
			VideoCodec: v.VideoCodec, Size: v.TotalSize(), LibraryTitle: v.LibraryTitle,
		}
		if v.Arr != nil {
			fs.ArrInstanceName = v.Arr.InstanceName
		}
		if d := v.Disc; d != nil {
			fs.Disc = &duplicateFileDisc{Type: d.Type, FileCount: d.FileCount, Discs: max(d.Discs, 1), ClipCount: d.ClipCount}
		} else {
			fs.DiscClip = isDiscFile(v)
		}
		s.Files = append(s.Files, fs)
	}
	return s
}

// duplicateDetail is GET /api/v1/duplicate/{id}: the full group plus its actions and the links to
// its Radarr/Sonarr items (never null).
type duplicateDetail struct {
	models.DuplicateGroup
	Actions  []models.Action `json:"actions"`
	ArrLinks []arrWebLink    `json:"arrLinks"`
}

// displayTitle is a human title for history entries ("Show - S01E02 - Title" / "Title (Year)").
func displayTitle(g *models.DuplicateGroup) string {
	if g.MediaType == models.MediaTypeEpisode && g.ShowTitle != "" {
		t := g.ShowTitle + " - " + engine.EpisodeLabel(g.Season, g.Episode)
		if g.Title != "" {
			t += " - " + g.Title
		}
		return t
	}
	if g.Year > 0 {
		return fmt.Sprintf("%s (%d)", g.Title, g.Year)
	}
	return g.Title
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

// groupFilter parses the list filters: status (list), mediaType, libraryId, serverId, flag, search.
func groupFilter(r *http.Request) (store.GroupFilter, error) {
	var f store.GroupFilter
	var errs []config.ValidationError
	for _, st := range queryList(r, "status") {
		gs := models.GroupStatus(strings.ToLower(st))
		if !slices.Contains(knownGroupStatuses, gs) {
			errs = append(errs, invalid("status", "Unknown status '%s'", truncate(st, 32)))
			continue
		}
		if !slices.Contains(f.Statuses, gs) {
			f.Statuses = append(f.Statuses, gs)
		}
	}
	switch mt := models.MediaType(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mediaType")))); mt {
	case "", models.MediaTypeMovie, models.MediaTypeEpisode:
		f.MediaType = mt
	default:
		errs = append(errs, invalid("mediaType", "Must be movie or episode"))
	}
	var err error
	if f.LibraryID, err = queryInt(r, "libraryId"); err != nil {
		errs = append(errs, invalid("libraryId", "Must be a non-negative integer"))
	}
	if f.ServerID, err = queryInt(r, "serverId"); err != nil {
		errs = append(errs, invalid("serverId", "Must be a non-negative integer"))
	}
	f.Flag = strings.TrimSpace(r.URL.Query().Get("flag"))
	if len(f.Flag) > 64 {
		errs = append(errs, invalid("flag", "Unknown flag"))
	}
	f.Search = strings.TrimSpace(r.URL.Query().Get("search"))
	if utf8.RuneCountInString(f.Search) > maxSearchLength {
		errs = append(errs, invalid("search", "Must be at most %d characters", maxSearchLength))
	}
	if len(errs) > 0 {
		return f, errValidation(errs...)
	}
	return f, nil
}

func (s *Server) handleDuplicates(w http.ResponseWriter, r *http.Request) {
	p, err := parsePaging(r, "lastSeenAt", "")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	f, err := groupFilter(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	page, err := s.d.Store.Groups().List(r.Context(), f, p)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := store.Page[duplicateSummary]{
		Page: page.Page, PageSize: page.PageSize, SortKey: page.SortKey, SortDirection: page.SortDirection,
		TotalRecords: page.TotalRecords, Records: make([]duplicateSummary, 0, len(page.Records)),
	}
	for i := range page.Records {
		out.Records = append(out.Records, summarize(&page.Records[i]))
	}
	s.writeJSON(w, http.StatusOK, out)
}

type duplicateStats struct {
	store.GroupStats
	LastScan *models.ScanRun `json:"lastScan"`
}

func (s *Server) handleDuplicateStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.Store.Groups().Stats(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if st.ByStatus == nil {
		st.ByStatus = map[models.GroupStatus]int{}
	}
	for _, gs := range knownGroupStatuses {
		if _, ok := st.ByStatus[gs]; !ok {
			st.ByStatus[gs] = 0
		}
	}
	out := duplicateStats{GroupStats: st}
	last, err := s.d.Store.ScanRuns().Latest(r.Context())
	switch {
	case err == nil:
		out.LastScan = last
	case !errors.Is(err, store.ErrNotFound):
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// loadGroup loads the group named by the {id} path value.
func (s *Server) loadGroup(r *http.Request) (*models.DuplicateGroup, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return s.getGroup(r.Context(), id)
}

func (s *Server) getGroup(ctx context.Context, id int64) (*models.DuplicateGroup, error) {
	g, err := s.d.Store.Groups().Get(ctx, id)
	return g, notFoundAs(err, "Duplicate group")
}

func (s *Server) handleDuplicate(w http.ResponseWriter, r *http.Request) {
	g, err := s.loadGroup(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	actions, err := s.d.Store.Actions().ListByGroup(r.Context(), g.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	links, err := s.arrWebLinks(r.Context(), g)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, duplicateDetail{DuplicateGroup: *g, Actions: actions, ArrLinks: links})
}

// ---------------------------------------------------------------------------
// Approve / ignore / unignore
// ---------------------------------------------------------------------------

// unreadServers names the media servers a group's last scan could not read ("A media server" when
// the record does not say).
func unreadServers(g *models.DuplicateGroup) string {
	var names []string
	if g.CrossServer != nil {
		for _, sv := range g.CrossServer.Servers {
			if sv.Unread != "" {
				name, _, _ := strings.Cut(sv.Unread, ":")
				names = append(names, "The media server "+strings.TrimSpace(name))
			}
		}
	}
	if len(names) == 0 {
		return "A media server"
	}
	return strings.Join(names, " and ")
}

// invariantProblem is a client-facing description of a safety-invariant violation.
func invariantProblem(err error) string {
	return truncate(err.Error(), maxMessageLen)
}

// errStaleDecision is the 409 message when an approval names decisions that changed meanwhile.
const errStaleDecision = "This duplicate changed since it was displayed (a scan or another user updated its decisions); review it again before approving"

// approveGroup validates the group's decisions and queues its removals (without starting
// ProcessQueue). When expectedSignature is set (the signature of the group the user reviewed),
// the approval is refused with 409 if the group's versions or decisions changed since. bulk
// approvals refuse groups in review: those are flagged as possibly not duplicates at all
// (suspect merge, unanalyzed or same file…) and must be checked and approved one at a time.
//
// The executor approves exactly the state checked here (executor.Service.ApproveReviewed with the
// reviewed signature, else the signature read at the start of the request): a scan that changes
// the group's decisions between these checks and the executor's read makes it refuse (409).
func (s *Server) approveGroup(ctx context.Context, id int64, expectedSignature string, bulk bool) ([]models.Action, error) {
	if s.executor == nil {
		return nil, errUnavailable("The executor")
	}
	g, err := s.getGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	if expectedSignature != "" && expectedSignature != g.Signature {
		return nil, errConflict(errStaleDecision)
	}
	if bulk && g.Status == models.GroupReview {
		return nil, errConflict("This duplicate needs a review: open it, check the copies and approve it on its own")
	}
	if bulk && removesDisc(g) {
		// docs/DECISIONS.md D9: a whole disc (hundreds of files) is only removed after a person
		// looked at it on its own page.
		return nil, errConflict("This duplicate removes a full-disc backup: open it and approve it on its own")
	}
	if bulk && executor.ReadOnlyGroup(g) {
		// docs/DECISIONS.md D12: Jellyfin copies are only removed by a person's single approval.
		return nil, errConflict("This duplicate is on a Jellyfin server: open it and approve it on its own")
	}
	switch g.Status {
	case models.GroupIgnored:
		return nil, errConflict("This duplicate is ignored; un-ignore it first")
	case models.GroupResolved:
		return nil, errConflict("This duplicate is already resolved")
	case models.GroupQueued:
		return nil, errConflict("Removals of this duplicate are already queued")
	case models.GroupProtected:
		return nil, errBadRequest("Nothing to remove: every other copy is protected")
	}
	if scanner.ArrDataMissing(g) {
		// Files the unreadable instance tracks look untracked in this data: a keep tag, the
		// tracked state or an active download may be missing, so they must never be removed on it.
		return nil, errConflict("A Radarr/Sonarr instance could not be read when this duplicate was last scanned, so " +
			"files it tracks look untracked here. Re-scan it once the application is reachable (or disable that " +
			"application in Settings) before approving")
	}
	if scanner.ArrTrackingUnknown(g) {
		// docs/DECISIONS.md D11: a version may be a file an *arr tracks on this host, or not.
		return nil, errConflict("Dupearr could not tell whether Radarr/Sonarr tracks a file of this duplicate (%s). "+
			"Add a path mapping for that media server, or confirm which Plex servers the application feeds "+
			"(Settings → Applications), then re-scan it before approving", strings.TrimPrefix(g.StatusReason, "Incomplete data: "))
	}
	if scanner.CrossServerDataMissing(g) {
		// docs/DECISIONS.md D11: a server that could not be read may list these files.
		return nil, errConflict("%s could not be read when this duplicate was last scanned, and it may list these files. "+
			"Make it reachable, disable it, or declare it separate storage (Settings → Media Servers), then re-scan this duplicate before approving",
			unreadServers(g))
	}
	if err := engine.ValidateDecisions(g); err != nil {
		return nil, errBadRequest("Cannot approve: %s", invariantProblem(err))
	}
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	if err := s.checkEndpointsCurrent(ctx, g); err != nil {
		return nil, err
	}
	signature := expectedSignature
	if signature == "" {
		signature = g.Signature
	}
	actions, err := s.executor.ApproveReviewed(ctx, id, models.TriggerManual, signature)
	switch {
	case errors.Is(err, executor.ErrNothingToRemove):
		return nil, errBadRequest("Nothing to remove in this duplicate group")
	case errors.Is(err, engine.ErrInvariant):
		return nil, errBadRequest("Cannot approve: %s", invariantProblem(err))
	case err != nil:
		return nil, notFoundAs(err, "Duplicate group")
	}
	s.log.Info("Duplicate approved", "groupId", id, "title", displayTitle(g), "actions", len(actions))
	return actions, nil
}

// startProcessing queues ProcessQueue after approvals (deduplicated by the command manager).
func (s *Server) startProcessing(ctx context.Context) {
	if _, err := s.enqueue(context.WithoutCancel(ctx), models.CmdProcessQueue, struct{}{}, models.TriggerManual); err != nil {
		s.log.Warn("Could not queue ProcessQueue after approval; it runs on its schedule", "error", err)
	}
}

// approveRequest is the optional body of POST /duplicate/{id}/approve.
type approveRequest struct {
	// Signature of the group as reviewed (DuplicateGroup.signature); optional.
	Signature string `json:"signature"`
}

func (s *Server) handleDuplicateApprove(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req approveRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil && !isEmptyBody(err) {
			s.writeErr(w, r, err)
			return
		}
	}
	actions, err := s.approveGroup(r.Context(), id, strings.TrimSpace(req.Signature), false)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.startProcessing(r.Context())
	s.writeJSON(w, http.StatusOK, actions)
}

// addGroupHistory records an audit event for a group and publishes it.
func (s *Server) addGroupHistory(ctx context.Context, eventType string, g *models.DuplicateGroup, msg string) {
	gid := g.ID
	e := &models.HistoryEvent{EventType: eventType, GroupID: &gid, Title: displayTitle(g), Message: msg, CreatedAt: s.now().UTC()}
	if err := s.d.Store.History().Add(context.WithoutCancel(ctx), e); err != nil {
		s.log.Warn("Could not record a history event", "eventType", eventType, "groupId", gid, "error", err)
		return
	}
	s.publish(events.NameHistory, events.ActionUpdated, e)
}

// ignoreGroup marks a group ignored (cancelling queued removals) and optionally excludes its key
// from future scans.
func (s *Server) ignoreGroup(ctx context.Context, id int64, addExclusion bool) (*models.DuplicateGroup, error) {
	g, err := s.getGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	if g.Status == models.GroupResolved {
		return nil, errConflict("This duplicate is already resolved")
	}
	changed := g.Status != models.GroupIgnored
	if changed {
		if err := s.d.Store.Groups().UpdateStatus(ctx, id, models.GroupIgnored, "Ignored by user"); err != nil {
			return nil, notFoundAs(err, "Duplicate group")
		}
	}
	msg := "Ignored"
	if addExclusion {
		added, err := s.excludeKey(ctx, g)
		if err != nil {
			return nil, err
		}
		changed = changed || added
		msg = "Ignored and excluded from future scans"
	}
	if changed {
		s.addGroupHistory(ctx, models.EventGroupIgnored, g, msg)
	}
	updated, err := s.getGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	s.log.Info("Duplicate ignored", "groupId", id, "title", displayTitle(g), "excluded", addExclusion)
	s.publish(events.NameDuplicate, events.ActionUpdated, updated)
	return updated, nil
}

// excludeKey adds a group_key exclusion for the group (idempotent); added reports whether a new
// exclusion was created.
func (s *Server) excludeKey(ctx context.Context, g *models.DuplicateGroup) (added bool, err error) {
	list, err := s.d.Store.Exclusions().List(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range list {
		if e.Kind == models.ExcludeGroupKey && e.Value == g.Key {
			return false, nil
		}
	}
	ex := &models.Exclusion{Kind: models.ExcludeGroupKey, Value: g.Key, Title: displayTitle(g), Reason: "Ignored from the duplicates list"}
	if err := s.d.Store.Exclusions().Create(ctx, ex); err != nil {
		return false, err
	}
	return true, nil
}

// unignoreGroup reopens an ignored group and re-evaluates it. Group-key exclusions of the group
// are removed (otherwise the next scan would drop it again). When re-evaluation is impossible the
// group is put in review, so stale decisions are never approved blindly.
func (s *Server) unignoreGroup(ctx context.Context, id int64) (*models.DuplicateGroup, error) {
	g, err := s.getGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	if g.Status != models.GroupIgnored {
		return nil, errConflict("This duplicate is not ignored")
	}
	if err := s.d.Store.Groups().UpdateStatus(ctx, id, models.GroupPending, ""); err != nil {
		return nil, notFoundAs(err, "Duplicate group")
	}
	if list, err := s.d.Store.Exclusions().List(ctx); err == nil {
		for _, e := range list {
			if e.Kind == models.ExcludeGroupKey && e.Value == g.Key {
				if err := s.d.Store.Exclusions().Delete(ctx, e.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
					s.log.Warn("Could not remove the exclusion of an un-ignored duplicate", "groupId", id, "error", err)
				}
			}
		}
	}
	s.addGroupHistory(ctx, models.EventGroupUnignored, g, "Un-ignored")
	updated, err := s.reevaluate(ctx, id)
	if err != nil {
		return nil, err
	}
	s.log.Info("Duplicate un-ignored", "groupId", id, "title", displayTitle(g))
	return updated, nil
}

// reevaluate re-runs the engine for one group. On failure the group is moved to review.
func (s *Server) reevaluate(ctx context.Context, id int64) (*models.DuplicateGroup, error) {
	var err error
	if s.scanner == nil {
		err = errors.New("the scanner is not available")
	} else {
		var g *models.DuplicateGroup
		if g, err = s.scanner.Reevaluate(ctx, id); err == nil && g != nil {
			return g, nil
		}
		if err == nil {
			err = errors.New("re-evaluation returned no group")
		}
	}
	if uerr := s.d.Store.Groups().UpdateStatus(context.WithoutCancel(ctx), id, models.GroupReview,
		"Re-evaluation failed; re-scan this item"); uerr != nil {
		s.log.Warn("Could not mark a duplicate for review", "groupId", id, "error", uerr)
	}
	return nil, &apiError{status: http.StatusInternalServerError,
		msg: "The duplicate could not be re-evaluated; it was marked for review", cause: err}
}

type ignoreRequest struct {
	AddExclusion bool `json:"addExclusion"`
}

func (s *Server) handleDuplicateIgnore(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req ignoreRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil && !isEmptyBody(err) {
			s.writeErr(w, r, err)
			return
		}
	}
	g, err := s.ignoreGroup(r.Context(), id, req.AddExclusion)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, g)
}

func isEmptyBody(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.msg == "Request body can't be empty"
}

func (s *Server) handleDuplicateUnignore(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	g, err := s.unignoreGroup(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, g)
}

// ---------------------------------------------------------------------------
// Per-file override
// ---------------------------------------------------------------------------

type overrideRequest struct {
	Decision *string `json:"decision"`
}

// handleDuplicateOverride sets or clears (null) a file's keep/remove override and re-evaluates
// the group. A "remove" override that breaks a safety invariant (e.g. nothing left to keep) is
// refused with 400 and reverted.
func (s *Server) handleDuplicateOverride(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, err := s.loadGroup(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	fileID, err := pathID(r, "fileId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req overrideRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	var d models.Decision
	if req.Decision != nil {
		d = models.Decision(strings.ToLower(strings.TrimSpace(*req.Decision)))
		if d != models.DecisionKeep && d != models.DecisionRemove {
			s.writeErr(w, r, errValidation(invalid("decision", "Must be keep, remove or null")))
			return
		}
	}
	idx := slices.IndexFunc(g.Files, func(f models.GroupFile) bool { return f.ID == fileID })
	if idx < 0 {
		s.writeErr(w, r, errNotFound("File"))
		return
	}
	if g.Status == models.GroupResolved {
		s.writeErr(w, r, errConflict("This duplicate is already resolved"))
		return
	}
	prev := g.Files[idx].Override
	if prev == d {
		s.writeJSON(w, http.StatusOK, g)
		return
	}
	before := engine.ValidateDecisions(g)
	if d == models.DecisionRemove {
		// Refuse up front when the removal alone breaks an invariant.
		cp := *g
		cp.Files = slices.Clone(g.Files)
		cp.Files[idx].Override, cp.Files[idx].Decision = d, d
		if err := newInvariantProblem(before, engine.ValidateDecisions(&cp)); err != nil {
			s.writeErr(w, r, errBadRequest("This override is not allowed: %s", invariantProblem(err)))
			return
		}
	}
	if err := s.d.Store.Groups().SetOverride(ctx, g.ID, fileID, d); err != nil {
		s.writeErr(w, r, notFoundAs(err, "File"))
		return
	}
	updated, err := s.reevaluateAfterOverride(ctx, g, fileID, prev, d, before)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	msg := "Override cleared"
	if d != "" {
		msg = "File marked " + string(d)
	}
	s.addGroupHistory(ctx, models.EventOverrideChanged, g, msg)
	s.writeJSON(w, http.StatusOK, updated)
}

// reevaluateAfterOverride re-evaluates the group; a "remove" override is reverted when the
// re-evaluation fails or the result violates an invariant that did not exist before.
func (s *Server) reevaluateAfterOverride(ctx context.Context, g *models.DuplicateGroup, fileID int64, prev, d models.Decision, before error) (*models.DuplicateGroup, error) {
	revert := func() {
		bg := context.WithoutCancel(ctx)
		if err := s.d.Store.Groups().SetOverride(bg, g.ID, fileID, prev); err != nil {
			s.log.Error("Could not revert a rejected override", "groupId", g.ID, "fileId", fileID, "error", err)
			return
		}
		if s.scanner != nil {
			if _, err := s.scanner.Reevaluate(bg, g.ID); err != nil {
				s.log.Warn("Could not re-evaluate after reverting an override", "groupId", g.ID, "error", err)
			}
		}
	}
	if s.scanner == nil {
		if d == models.DecisionRemove {
			revert()
			return nil, errUnavailable("The scanner")
		}
		return s.getGroup(ctx, g.ID) // a keep/clear override is applied by the store immediately
	}
	updated, err := s.scanner.Reevaluate(ctx, g.ID)
	if err != nil || updated == nil {
		if d == models.DecisionRemove {
			revert()
		}
		if err == nil {
			err = errors.New("re-evaluation returned no group")
		}
		return nil, &apiError{status: http.StatusInternalServerError, msg: "The duplicate could not be re-evaluated", cause: err}
	}
	if d == models.DecisionRemove {
		if err := newInvariantProblem(before, engine.ValidateDecisions(updated)); err != nil {
			revert()
			return nil, errBadRequest("This override is not allowed: %s", invariantProblem(err))
		}
	}
	return updated, nil
}

// newInvariantProblem returns after when it reports problems that before did not.
func newInvariantProblem(before, after error) error {
	if after == nil {
		return nil
	}
	if before != nil && before.Error() == after.Error() {
		return nil
	}
	return after
}

// ---------------------------------------------------------------------------
// Rescan and bulk actions
// ---------------------------------------------------------------------------

// handleDuplicateRescan queues a TargetedScan of the group's Plex items.
func (s *Server) handleDuplicateRescan(w http.ResponseWriter, r *http.Request) {
	g, err := s.loadGroup(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var keys []string
	for _, f := range g.Files {
		rk := f.Version.RatingKey
		if ratingKeyRe.MatchString(rk) && !slices.Contains(keys, rk) {
			keys = append(keys, rk)
		}
	}
	if len(keys) == 0 {
		s.writeErr(w, r, errBadRequest("This duplicate has no Plex items to re-scan"))
		return
	}
	cmd, err := s.enqueue(r.Context(), models.CmdTargetedScan,
		models.TargetedScanBody{ServerID: g.ServerID, RatingKeys: keys}, models.TriggerManual)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeCommandCreated(w, cmd)
}

type bulkRequest struct {
	IDs    []int64 `json:"ids"`
	Action string  `json:"action"`
	// Signatures optionally maps group ids to the signature the user reviewed (approve only):
	// a group whose decisions changed since is not approved.
	Signatures map[int64]string `json:"signatures,omitempty"`
}

type bulkFailure struct {
	ID      int64  `json:"id"`
	Message string `json:"message"`
}

type bulkResponse struct {
	Succeeded []int64       `json:"succeeded"`
	Failed    []bulkFailure `json:"failed"`
}

// handleDuplicateBulk applies approve/ignore/unignore to many groups; each group succeeds or
// fails on its own.
func (s *Server) handleDuplicateBulk(w http.ResponseWriter, r *http.Request) {
	var req bulkRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	var errs []config.ValidationError
	switch action {
	case "approve", "ignore", "unignore":
	default:
		errs = append(errs, invalid("action", "Must be approve, ignore or unignore"))
	}
	switch {
	case len(req.IDs) == 0:
		errs = append(errs, invalid("ids", "At least one id is required"))
	case len(req.IDs) > maxBulkIDs:
		errs = append(errs, invalid("ids", "At most %d ids per request", maxBulkIDs))
	}
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	ctx := r.Context()
	res := bulkResponse{Succeeded: []int64{}, Failed: []bulkFailure{}}
	seen := make(map[int64]bool, len(req.IDs))
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if ctx.Err() != nil {
			res.Failed = append(res.Failed, bulkFailure{ID: id, Message: "Request cancelled"})
			continue
		}
		var err error
		switch {
		case id <= 0:
			err = errBadRequest("Invalid id")
		case action == "approve":
			_, err = s.approveGroup(ctx, id, strings.TrimSpace(req.Signatures[id]), true)
		case action == "ignore":
			_, err = s.ignoreGroup(ctx, id, false)
		default:
			_, err = s.unignoreGroup(ctx, id)
		}
		if err != nil {
			res.Failed = append(res.Failed, bulkFailure{ID: id, Message: s.clientMessage(r, err)})
			continue
		}
		res.Succeeded = append(res.Succeeded, id)
	}
	if action == "approve" && len(res.Succeeded) > 0 {
		s.startProcessing(ctx)
	}
	s.writeJSON(w, http.StatusOK, res)
}

// clientMessage is the client-safe message of err, mapped exactly like a single request's error
// response (unexpected errors are logged and hidden).
func (s *Server) clientMessage(r *http.Request, err error) string {
	_, msg, verrs := s.errorResponse(r, err)
	if len(verrs) > 0 {
		return joinValidation(verrs)
	}
	return msg
}

func joinValidation(errs []config.ValidationError) string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.ErrorMessage)
	}
	return strings.Join(msgs, "; ")
}

// isDiscFile reports a regular version with a file (server or local path) that belongs to a disc:
// inside a disc structure or a loose clip (disc.IsDiscPath). The engine and every removal method
// refuse to remove it on its own.
func isDiscFile(v *models.MediaVersion) bool {
	for _, p := range v.Parts {
		if disc.IsDiscPath(p.Path) || (p.LocalPath != "" && disc.IsDiscPath(p.LocalPath)) {
			return true
		}
	}
	return false
}

// removesDisc reports whether a group decides to remove a full-disc version.
func removesDisc(g *models.DuplicateGroup) bool {
	for i := range g.Files {
		if f := &g.Files[i]; f.Decision == models.DecisionRemove && f.Version.Disc != nil {
			return true
		}
	}
	return false
}
