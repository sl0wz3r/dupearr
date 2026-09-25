package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// groupRepo persists duplicate groups (duplicate_groups) and their versions (group_files).
//
// List-valued filters are passed to SQLite as one JSON array parameter and expanded with
// json_each, so arbitrarily long key/id lists never hit SQLite's bound-parameter limit.
type groupRepo struct{ d *DB }

// resolvedReason is the status reason MarkUnseenResolved records.
const resolvedReason = "No longer detected as a duplicate by the last full scan"

const groupColumns = `g.id, g.key, g.status, g.status_reason, g.media_type, g.title, g.show_title,
	g.year, g.season, g.episode, g.server_id, g.library_ids, g.flags, g.external_ids, g.thumb,
	g.profile_id, g.reclaimable_bytes, g.signature, g.stable_count, g.first_seen_at, g.last_seen_at,
	g.updated_at, g.last_scan_id, g.cross_server`

const fileColumns = `id, group_id, version_key, decision, engine_decision, override, rank, protected,
	protected_reason, deciding_criterion, reasons, criterion_values, version`

// groupSort is the whitelist of group sort keys (docs/API.md: title, lastSeenAt, firstSeenAt,
// reclaimableBytes, status). "title" orders episodes by show, season and episode.
var groupSort = sortSpec{
	columns: map[string]sortColumn{
		"title": {exprs: []string{
			`CASE WHEN g.show_title <> '' THEN g.show_title ELSE g.title END COLLATE NOCASE`,
			`g.season`, `g.episode`, `g.title COLLATE NOCASE`,
		}, defaultDir: sortAscending},
		"lastSeenAt":       {exprs: []string{`g.last_seen_at`}, defaultDir: sortDescending},
		"firstSeenAt":      {exprs: []string{`g.first_seen_at`}, defaultDir: sortDescending},
		"reclaimableBytes": {exprs: []string{`g.reclaimable_bytes`}, defaultDir: sortDescending},
		"status":           {exprs: []string{`g.status`}, defaultDir: sortAscending},
	},
	defaultKey: "lastSeenAt",
	tiebreak:   "g.id",
}

var knownGroupStatuses = []models.GroupStatus{
	models.GroupPending, models.GroupReview, models.GroupDeferred, models.GroupProtected,
	models.GroupQueued, models.GroupResolved, models.GroupIgnored, models.GroupFailed,
}

func validGroupStatus(s models.GroupStatus) bool { return slices.Contains(knownGroupStatuses, s) }

// noKeeperReason is the status reason Upsert records when a group would keep no version.
const noKeeperReason = "Safety: no version of this group is marked keep; check the decisions"

// statusBlocksRemovals reports whether removals queued for a group must not run while it is in
// status s: the user ignored it, it is no longer a duplicate (resolved), or the engine found a
// reason not to act now (review: suspect data; deferred: min age, playing, *arr queue busy;
// protected: nothing may be removed). Moving a group into such a status cancels its pending
// actions (docs/ARCHITECTURE.md §6: stale data is never acted on). pending, queued and failed
// leave the queue alone (failed: the executor may still be working through the group).
func statusBlocksRemovals(s models.GroupStatus) bool {
	switch s {
	case models.GroupIgnored, models.GroupResolved, models.GroupReview, models.GroupDeferred, models.GroupProtected:
		return true
	}
	return false
}

func validDecision(d models.Decision) bool {
	return d == "" || d == models.DecisionKeep || d == models.DecisionRemove
}

// searchText is the lower-cased (Unicode-aware) text the Search filter matches against.
func searchText(title, showTitle string) string {
	return strings.ToLower(title + "\n" + showTitle)
}

// jsonList encodes a list filter for `IN (SELECT value FROM json_each(?))`.
func jsonList[T any](v []T) (string, error) { return toJSON(nonNil(v)) }

func scanGroup(s scanner) (models.DuplicateGroup, error) {
	var (
		g                                models.DuplicateGroup
		libraryIDs, flags, externalIDs   string
		firstSeen, lastSeen, updatedTime string
		crossServer                      string
	)
	if err := s.Scan(&g.ID, &g.Key, &g.Status, &g.StatusReason, &g.MediaType, &g.Title, &g.ShowTitle,
		&g.Year, &g.Season, &g.Episode, &g.ServerID, &libraryIDs, &flags, &externalIDs, &g.Thumb,
		&g.ProfileID, &g.ReclaimableBytes, &g.Signature, &g.StableCount, &firstSeen, &lastSeen,
		&updatedTime, &g.LastScanID, &crossServer); err != nil {
		return models.DuplicateGroup{}, err
	}
	if strings.TrimSpace(crossServer) != "" {
		// '' is "no record" (a group stored before multi-server support, or by a one-server scan).
		var rec models.CrossServerRecord
		if err := fromJSON(crossServer, &rec); err != nil {
			return models.DuplicateGroup{}, fmt.Errorf("group %d cross-server record: %w", g.ID, err)
		}
		g.CrossServer = &rec
	}
	if err := fromJSON(libraryIDs, &g.LibraryIDs); err != nil {
		return models.DuplicateGroup{}, fmt.Errorf("group %d library ids: %w", g.ID, err)
	}
	if err := fromJSON(flags, &g.Flags); err != nil {
		return models.DuplicateGroup{}, fmt.Errorf("group %d flags: %w", g.ID, err)
	}
	if err := fromJSON(externalIDs, &g.ExternalIDs); err != nil {
		return models.DuplicateGroup{}, fmt.Errorf("group %d external ids: %w", g.ID, err)
	}
	g.LibraryIDs = nonNil(g.LibraryIDs)
	g.Flags = nonNil(g.Flags)
	g.ExternalIDs = nonNilMap(g.ExternalIDs)
	g.Files = []models.GroupFile{}
	var err error
	if g.FirstSeenAt, err = parseTime(firstSeen); err != nil {
		return models.DuplicateGroup{}, err
	}
	if g.LastSeenAt, err = parseTime(lastSeen); err != nil {
		return models.DuplicateGroup{}, err
	}
	if g.UpdatedAt, err = parseTime(updatedTime); err != nil {
		return models.DuplicateGroup{}, err
	}
	return g, nil
}

func scanFile(s scanner) (models.GroupFile, error) {
	var (
		f                     models.GroupFile
		versionKey            string
		reasons, values, vers string
	)
	if err := s.Scan(&f.ID, &f.GroupID, &versionKey, &f.Decision, &f.EngineDecision, &f.Override,
		&f.Rank, &f.Protected, &f.ProtectedReason, &f.DecidingCriterion, &reasons, &values, &vers); err != nil {
		return models.GroupFile{}, err
	}
	if err := fromJSON(reasons, &f.Reasons); err != nil {
		return models.GroupFile{}, fmt.Errorf("group file %d reasons: %w", f.ID, err)
	}
	if err := fromJSON(values, &f.Values); err != nil {
		return models.GroupFile{}, fmt.Errorf("group file %d values: %w", f.ID, err)
	}
	if err := fromJSON(vers, &f.Version); err != nil {
		return models.GroupFile{}, fmt.Errorf("group file %d version: %w", f.ID, err)
	}
	f.Reasons = nonNil(f.Reasons)
	f.Values = nonNilMap(f.Values)
	if f.Version.Key == "" {
		f.Version.Key = versionKey
	}
	normalizeVersion(&f.Version)
	return f, nil
}

// normalizeVersion replaces nil slices of a stored version so API JSON never contains null lists.
func normalizeVersion(v *models.MediaVersion) {
	v.Parts = nonNil(v.Parts)
	v.AudioTracks = nonNil(v.AudioTracks)
	v.SubtitleTracks = nonNil(v.SubtitleTracks)
	if v.Arr != nil {
		v.Arr.EpisodeIDs = nonNil(v.Arr.EpisodeIDs)
		v.Arr.CustomFormats = nonNil(v.Arr.CustomFormats)
		v.Arr.Languages = nonNil(v.Arr.Languages)
		v.Arr.Tags = nonNil(v.Arr.Tags)
	}
}

// loadFiles populates Files of every group with one query (ordered as they were upserted).
func loadFiles(ctx context.Context, q querier, groups []models.DuplicateGroup) error {
	if len(groups) == 0 {
		return nil
	}
	index := make(map[int64]int, len(groups))
	ids := make([]int64, len(groups))
	for i := range groups {
		index[groups[i].ID] = i
		ids[i] = groups[i].ID
		groups[i].Files = []models.GroupFile{}
	}
	idList, err := jsonList(ids)
	if err != nil {
		return err
	}
	files, err := queryAll(ctx, q, scanFile, `SELECT `+fileColumns+` FROM group_files
		WHERE group_id IN (SELECT value FROM json_each(?)) ORDER BY group_id, position, id`, idList)
	if err != nil {
		return fmt.Errorf("load group files: %w", err)
	}
	for _, f := range files {
		if i, ok := index[f.GroupID]; ok {
			groups[i].Files = append(groups[i].Files, f)
		}
	}
	return nil
}

// groupWhere builds the WHERE clause (with leading space, or "") for a filter.
func groupWhere(f store.GroupFilter) (string, []any, error) {
	var (
		conds []string
		args  []any
	)
	if len(f.Statuses) > 0 {
		list, err := jsonList(f.Statuses)
		if err != nil {
			return "", nil, err
		}
		conds = append(conds, `g.status IN (SELECT value FROM json_each(?))`)
		args = append(args, list)
	}
	if f.MediaType != "" {
		conds = append(conds, `g.media_type = ?`)
		args = append(args, string(f.MediaType))
	}
	if f.LibraryID != 0 {
		conds = append(conds, `EXISTS (SELECT 1 FROM json_each(g.library_ids) AS l WHERE l.value = ?)`)
		args = append(args, f.LibraryID)
	}
	if f.ServerID != 0 {
		conds = append(conds, `g.server_id = ?`)
		args = append(args, f.ServerID)
	}
	if f.Flag != "" {
		conds = append(conds, `EXISTS (SELECT 1 FROM json_each(g.flags) AS fl WHERE fl.value = ?)`)
		args = append(args, f.Flag)
	}
	if s := strings.ToLower(strings.TrimSpace(f.Search)); s != "" {
		conds = append(conds, `instr(g.search_text, ?) > 0`)
		args = append(args, s)
	}
	if len(f.Keys) > 0 {
		list, err := jsonList(f.Keys)
		if err != nil {
			return "", nil, err
		}
		conds = append(conds, `g.key IN (SELECT value FROM json_each(?))`)
		args = append(args, list)
	}
	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

// List returns a page of groups matching f, with Files populated.
func (r groupRepo) List(ctx context.Context, f store.GroupFilter, p store.Paging) (store.Page[models.DuplicateGroup], error) {
	rp := groupSort.resolve(p)
	where, args, err := groupWhere(f)
	if err != nil {
		return store.Page[models.DuplicateGroup]{}, fmt.Errorf("list groups: %w", err)
	}
	var page store.Page[models.DuplicateGroup]
	err = r.d.read(ctx, func(tx *sql.Tx) error {
		var total int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM duplicate_groups g`+where, args...).Scan(&total); err != nil {
			return err
		}
		pageArgs := append(slices.Clone(args), rp.size, rp.offset())
		groups, err := queryAll(ctx, tx, scanGroup,
			`SELECT `+groupColumns+` FROM duplicate_groups g`+where+` ORDER BY `+rp.orderBy+` LIMIT ? OFFSET ?`,
			pageArgs...)
		if err != nil {
			return err
		}
		if err := loadFiles(ctx, tx, groups); err != nil {
			return err
		}
		page = newPage(rp, total, groups)
		return nil
	})
	if err != nil {
		return store.Page[models.DuplicateGroup]{}, wrap(err, "list groups")
	}
	return page, nil
}

// getOne loads a single group (with files) matching cond.
func (r groupRepo) getOne(ctx context.Context, desc, cond string, arg any) (*models.DuplicateGroup, error) {
	var g models.DuplicateGroup
	err := r.d.read(ctx, func(tx *sql.Tx) error {
		var err error
		g, err = scanGroup(tx.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM duplicate_groups g WHERE `+cond, arg))
		if err != nil {
			return err
		}
		groups := []models.DuplicateGroup{g}
		if err := loadFiles(ctx, tx, groups); err != nil {
			return err
		}
		g = groups[0]
		return nil
	})
	if err != nil {
		return nil, wrap(err, "get group %s", desc)
	}
	return &g, nil
}

// Get returns a group with Files populated.
func (r groupRepo) Get(ctx context.Context, id int64) (*models.DuplicateGroup, error) {
	return r.getOne(ctx, fmt.Sprint(id), `g.id = ?`, id)
}

// GetByKey returns a group (Files populated) by its identity key.
func (r groupRepo) GetByKey(ctx context.Context, key string) (*models.DuplicateGroup, error) {
	return r.getOne(ctx, fmt.Sprintf("%q", key), `g.key = ?`, key)
}

// ListByRatingKeys returns the groups of serverID (Files populated) containing a version of one
// of ratingKeys, ordered by id.
func (r groupRepo) ListByRatingKeys(ctx context.Context, serverID int64, ratingKeys []string) ([]models.DuplicateGroup, error) {
	if len(ratingKeys) == 0 {
		return []models.DuplicateGroup{}, nil
	}
	list, err := jsonList(ratingKeys)
	if err != nil {
		return nil, fmt.Errorf("list groups by rating keys: %w", err)
	}
	var out []models.DuplicateGroup
	err = r.d.read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = queryAll(ctx, tx, scanGroup, `SELECT `+groupColumns+` FROM duplicate_groups g
			WHERE g.server_id = ? AND g.id IN (
				SELECT f.group_id FROM group_files f WHERE f.rating_key IN (SELECT value FROM json_each(?)))
			ORDER BY g.id`, serverID, list)
		if err != nil {
			return err
		}
		return loadFiles(ctx, tx, out)
	})
	if err != nil {
		return nil, wrap(err, "list groups by rating keys")
	}
	return out, nil
}

// storedFile is the persisted state of a group file that Upsert must carry over.
type storedFile struct {
	id       int64
	override models.Decision
}

// fileResult is the outcome of Upsert for one file, applied to the caller's struct on commit.
type fileResult struct {
	id       int64
	override models.Decision
	decision models.Decision
}

// Upsert inserts or updates a group by Key (and replaces its files, matched by Version.Key),
// preserving: FirstSeenAt, Status=ignored (with its reason), and each file's Override (by
// Version.Key; the stored override wins, so a concurrent SetOverride is never lost). Files keep
// their IDs across re-scans when their Version.Key is unchanged; files whose key vanished are
// deleted, new ones are inserted. The override of a vanished file is retained on the group and
// restored if that version comes back (a version drops out while Plex reports its file
// unavailable, and a user's "keep" must survive that). Sets g.ID, file IDs/GroupIDs and the
// preserved values on g. Returns created=true when the group did not exist before.
//
// Safety net: a file whose effective decision (Override, else EngineDecision) is "keep", or that
// is Protected, is always stored with Decision "keep" — Upsert can turn a stale "remove" into
// "keep" but never the reverse. An empty Status is stored as "review" (never auto-approved), and
// so is a group in which no file ends up with Decision "keep" (unless ignored/resolved). In the
// same transaction, pending actions of the group are cancelled when the stored status blocks
// removals (see statusBlocksRemovals), and otherwise those whose target version is no longer
// stored with Decision "remove" (it vanished, became a keeper, or is protected/overridden), so a
// re-scan can never leave a stale removal queued; a "queued" group left with nothing to run is
// reopened as "pending" (reflected in g). Callers must therefore pass the stored status of a
// queued group through evaluation (engine.Evaluate keeps "queued"); a queued group upserted as
// review/deferred/protected loses its queue.
func (r groupRepo) Upsert(ctx context.Context, g *models.DuplicateGroup) (bool, error) {
	if g == nil {
		return false, errors.New("upsert group: nil group")
	}
	if strings.TrimSpace(g.Key) == "" {
		return false, errors.New("upsert group: empty key")
	}
	status := g.Status
	if status == "" {
		status = models.GroupReview
	}
	if !validGroupStatus(status) {
		return false, fmt.Errorf("upsert group %q: invalid status %q", g.Key, status)
	}
	incoming := make(map[string]bool, len(g.Files))
	for i := range g.Files {
		f := &g.Files[i]
		k := f.Version.Key
		if strings.TrimSpace(k) == "" {
			return false, fmt.Errorf("upsert group %q: file %d has an empty version key", g.Key, i)
		}
		if incoming[k] {
			return false, fmt.Errorf("upsert group %q: duplicate version key %q", g.Key, k)
		}
		incoming[k] = true
		if !validDecision(f.Decision) || !validDecision(f.EngineDecision) || !validDecision(f.Override) {
			return false, fmt.Errorf("upsert group %q: file %q has an invalid decision", g.Key, k)
		}
	}
	libraryIDs, err := toJSON(nonNil(g.LibraryIDs))
	if err != nil {
		return false, fmt.Errorf("upsert group %q: library ids: %w", g.Key, err)
	}
	flags, err := toJSON(nonNil(g.Flags))
	if err != nil {
		return false, fmt.Errorf("upsert group %q: flags: %w", g.Key, err)
	}
	externalIDs, err := toJSON(nonNilMap(g.ExternalIDs))
	if err != nil {
		return false, fmt.Errorf("upsert group %q: external ids: %w", g.Key, err)
	}

	now := nowUTC()
	lastSeen := orNow(g.LastSeenAt, now)

	var (
		created     bool
		groupID     int64
		reason      = g.StatusReason
		firstSeen   = orNow(g.FirstSeenAt, now)
		reclaimable = g.ReclaimableBytes
		results     = make([]fileResult, len(g.Files))
	)
	err = r.d.write(ctx, func(tx *sql.Tx) error {
		var storedStatus, storedReason, storedFirstSeen, storedRetained string
		err := tx.QueryRowContext(ctx, `SELECT id, status, status_reason, first_seen_at, retained_overrides
			FROM duplicate_groups WHERE key = ?`, g.Key).
			Scan(&groupID, &storedStatus, &storedReason, &storedFirstSeen, &storedRetained)
		// retained holds the overrides of versions that left the group (version key → decision).
		retained := map[string]models.Decision{}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			created = true
		case err != nil:
			return wrap(err, "find group")
		default:
			created = false
			if firstSeen, err = parseTime(storedFirstSeen); err != nil {
				return err
			}
			if models.GroupStatus(storedStatus) == models.GroupIgnored {
				status, reason = models.GroupIgnored, storedReason
			}
			if err := fromJSON(storedRetained, &retained); err != nil {
				return fmt.Errorf("retained overrides: %w", err)
			}
			retained = nonNilMap(retained)
		}

		stored := map[string]storedFile{}
		if !created {
			rows, err := tx.QueryContext(ctx, `SELECT id, version_key, override FROM group_files WHERE group_id = ?`, groupID)
			if err != nil {
				return wrap(err, "read group files")
			}
			for rows.Next() {
				var (
					sf  storedFile
					key string
				)
				if err := rows.Scan(&sf.id, &key, &sf.override); err != nil {
					_ = rows.Close()
					return wrap(err, "read group files")
				}
				stored[key] = sf
			}
			if err := rows.Close(); err != nil {
				return wrap(err, "read group files")
			}
			if err := rows.Err(); err != nil {
				return wrap(err, "read group files")
			}
		}

		// Decide the persisted override/decision of every file before writing anything.
		reclaimable = g.ReclaimableBytes
		for i := range g.Files {
			f := &g.Files[i]
			res := fileResult{override: f.Override, decision: f.Decision}
			if sf, ok := stored[f.Version.Key]; ok {
				res.id, res.override = sf.id, sf.override
			} else if ov, ok := retained[f.Version.Key]; ok && ov != "" && validDecision(ov) {
				// The version is back (e.g. it was unavailable for a while): restore the user's choice.
				res.override = ov
			}
			delete(retained, f.Version.Key) // present again: its row carries the override from now on
			effective := f.EngineDecision
			if res.override != "" {
				effective = res.override
			}
			if (effective == models.DecisionKeep || f.Protected) && res.decision != models.DecisionKeep {
				if res.decision == models.DecisionRemove {
					r.d.log.Warn("Stored a version as keep: its override or protection says keep but the decision said remove",
						"group", g.Key, "version", f.Version.Key)
					switch {
					case f.Version.Disc != nil && f.Version.Disc.TotalBytes > 0:
						reclaimable -= f.Version.Disc.FreedBytes // what the engine counted for the disc
					case !hardlinked(&f.Version):
						reclaimable -= f.Version.TotalSize()
					}
				}
				res.decision = models.DecisionKeep
			}
			results[i] = res
		}
		reclaimable = max(reclaimable, 0)

		// Versions leaving the group lose their row but not the user's decision about them.
		for key, sf := range stored {
			if !incoming[key] && sf.override != "" {
				retained[key] = sf.override
			}
		}
		retainedJSON, err := toJSON(retained)
		if err != nil {
			return fmt.Errorf("retained overrides: %w", err)
		}

		// Invariant 1 (docs/ARCHITECTURE.md §6): a group always keeps a version. The engine
		// guarantees it; if a caller still hands over a group without any keeper, never let it be
		// acted on — send it to review (which also cancels its queued removals below).
		if len(g.Files) > 0 && status != models.GroupIgnored && status != models.GroupResolved &&
			!slices.ContainsFunc(results, func(fr fileResult) bool { return fr.decision == models.DecisionKeep }) {
			r.d.log.Warn("Duplicate group has no version marked keep; flagged for review", "group", g.Key)
			status, reason = models.GroupReview, noKeeperReason
		}

		search := searchText(g.Title, g.ShowTitle)
		crossServer := ""
		if g.CrossServer != nil {
			if crossServer, err = toJSON(g.CrossServer); err != nil {
				return fmt.Errorf("cross-server record: %w", err)
			}
		}
		if created {
			res, err := tx.ExecContext(ctx, `INSERT INTO duplicate_groups
				(key, status, status_reason, media_type, title, show_title, year, season, episode, server_id,
				 library_ids, flags, external_ids, thumb, profile_id, reclaimable_bytes, signature, stable_count,
				 search_text, first_seen_at, last_seen_at, updated_at, last_scan_id, retained_overrides, cross_server)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				g.Key, string(status), reason, string(g.MediaType), g.Title, g.ShowTitle, g.Year, g.Season, g.Episode,
				g.ServerID, libraryIDs, flags, externalIDs, g.Thumb, g.ProfileID, reclaimable, g.Signature,
				g.StableCount, search, fmtTime(firstSeen), fmtTime(lastSeen), fmtTime(now), g.LastScanID, retainedJSON, crossServer)
			if err != nil {
				return wrap(err, "insert group")
			}
			if groupID, err = res.LastInsertId(); err != nil {
				return wrap(err, "insert group")
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE duplicate_groups SET
				status = ?, status_reason = ?, media_type = ?, title = ?, show_title = ?, year = ?, season = ?,
				episode = ?, server_id = ?, library_ids = ?, flags = ?, external_ids = ?, thumb = ?, profile_id = ?,
				reclaimable_bytes = ?, signature = ?, stable_count = ?, search_text = ?, last_seen_at = ?,
				updated_at = ?, last_scan_id = ?, retained_overrides = ?, cross_server = ?
				WHERE id = ?`,
				string(status), reason, string(g.MediaType), g.Title, g.ShowTitle, g.Year, g.Season, g.Episode,
				g.ServerID, libraryIDs, flags, externalIDs, g.Thumb, g.ProfileID, reclaimable, g.Signature,
				g.StableCount, search, fmtTime(lastSeen), fmtTime(now), g.LastScanID, retainedJSON, crossServer, groupID); err != nil {
				return wrap(err, "update group")
			}
		}

		// Files whose version disappeared go first (their overrides were retained above).
		for key, sf := range stored {
			if incoming[key] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM group_files WHERE id = ?`, sf.id); err != nil {
				return wrap(err, "delete group file %q", key)
			}
		}
		for i := range g.Files {
			f := &g.Files[i]
			res := &results[i]
			reasons, err := toJSON(nonNil(f.Reasons))
			if err != nil {
				return fmt.Errorf("file %q reasons: %w", f.Version.Key, err)
			}
			values, err := toJSON(nonNilMap(f.Values))
			if err != nil {
				return fmt.Errorf("file %q values: %w", f.Version.Key, err)
			}
			version, err := toJSON(f.Version)
			if err != nil {
				return fmt.Errorf("file %q version: %w", f.Version.Key, err)
			}
			if res.id != 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE group_files SET
					position = ?, rating_key = ?, decision = ?, engine_decision = ?, override = ?, rank = ?,
					protected = ?, protected_reason = ?, deciding_criterion = ?, reasons = ?, criterion_values = ?,
					version = ?
					WHERE id = ?`,
					i, f.Version.RatingKey, string(res.decision), string(f.EngineDecision), string(res.override), f.Rank,
					b2i(f.Protected), f.ProtectedReason, f.DecidingCriterion, reasons, values, version, res.id); err != nil {
					return wrap(err, "update group file %q", f.Version.Key)
				}
				continue
			}
			ins, err := tx.ExecContext(ctx, `INSERT INTO group_files
				(group_id, position, version_key, rating_key, decision, engine_decision, override, rank, protected,
				 protected_reason, deciding_criterion, reasons, criterion_values, version)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				groupID, i, f.Version.Key, f.Version.RatingKey, string(res.decision), string(f.EngineDecision),
				string(res.override), f.Rank, b2i(f.Protected), f.ProtectedReason, f.DecidingCriterion, reasons,
				values, version)
			if err != nil {
				return wrap(err, "insert group file %q", f.Version.Key)
			}
			if res.id, err = ins.LastInsertId(); err != nil {
				return wrap(err, "insert group file %q", f.Version.Key)
			}
		}
		if created {
			return nil // ids are never reused (AUTOINCREMENT): no action can reference a new group
		}
		if statusBlocksRemovals(status) {
			n, err := cancelPendingActions(ctx, tx, groupID, statusCancelMessage(status, reason))
			if err != nil {
				return err
			}
			if n > 0 {
				r.d.log.Info("Cancelled queued removals of a duplicate group that must not be acted on now",
					"group", g.Key, "status", status, "count", n)
			}
		}
		n, err := cancelStaleActions(ctx, tx, groupID)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		r.d.log.Info("Cancelled queued removals whose version is no longer marked for removal",
			"group", g.Key, "count", n)
		// A queued group whose queue is now empty was reopened; report the stored status.
		var st string
		if err := tx.QueryRowContext(ctx, `SELECT status, status_reason FROM duplicate_groups WHERE id = ?`,
			groupID).Scan(&st, &reason); err != nil {
			return wrap(err, "reload group status")
		}
		status = models.GroupStatus(st)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("upsert group %q: %w", g.Key, err)
	}

	// Committed: reflect the persisted state in the caller's struct.
	g.ID, g.Status, g.StatusReason = groupID, status, reason
	g.FirstSeenAt, g.LastSeenAt, g.UpdatedAt = firstSeen, lastSeen, now
	g.ReclaimableBytes = reclaimable
	g.LibraryIDs, g.Flags, g.ExternalIDs = nonNil(g.LibraryIDs), nonNil(g.Flags), nonNilMap(g.ExternalIDs)
	g.Files = nonNil(g.Files)
	for i := range g.Files {
		g.Files[i].ID = results[i].id
		g.Files[i].GroupID = groupID
		g.Files[i].Override = results[i].override
		g.Files[i].Decision = results[i].decision
		g.Files[i].Reasons = nonNil(g.Files[i].Reasons)
		g.Files[i].Values = nonNilMap(g.Files[i].Values)
		normalizeVersion(&g.Files[i].Version)
	}
	return created, nil
}

// hardlinked reports whether removing v may not free space (a part has other hard links); the
// engine counts such files as 0 reclaimable bytes.
func hardlinked(v *models.MediaVersion) bool {
	for _, p := range v.Parts {
		if p.LinkCount > 1 {
			return true
		}
	}
	return false
}

// UpdateStatus sets a group's status and reason. Moving a group into a status that blocks
// removals (ignored, resolved, review, deferred, protected; see statusBlocksRemovals) also cancels
// its pending actions in the same transaction: such a group is never acted on. Running actions
// are left alone (the executor finishes and records them).
func (r groupRepo) UpdateStatus(ctx context.Context, id int64, status models.GroupStatus, reason string) error {
	if !validGroupStatus(status) {
		return fmt.Errorf("update status of group %d: invalid status %q", id, status)
	}
	return r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE duplicate_groups SET status = ?, status_reason = ?, updated_at = ? WHERE id = ?`,
			string(status), reason, fmtTime(nowUTC()), id)
		if err != nil {
			return wrap(err, "update status of group %d", id)
		}
		if err := expectAffected(res, "update status of group %d", id); err != nil {
			return err
		}
		return r.afterStatusChange(ctx, tx, id, status, reason)
	})
}

// UpdateStatusIf is UpdateStatus as an atomic compare-and-set: the status and reason are written
// only while the group's current status is one of from (checked and written by one statement in
// the write transaction, so no concurrent change can slip in between). Reports whether it was
// written; a missing group is store.ErrNotFound. The side effects are UpdateStatus's.
func (r groupRepo) UpdateStatusIf(ctx context.Context, id int64, from []models.GroupStatus, to models.GroupStatus, reason string) (bool, error) {
	if !validGroupStatus(to) {
		return false, fmt.Errorf("update status of group %d: invalid status %q", id, to)
	}
	for _, s := range from {
		if !validGroupStatus(s) {
			return false, fmt.Errorf("update status of group %d: invalid status %q", id, s)
		}
	}
	if len(from) == 0 {
		return false, nil
	}
	fromList, err := jsonList(from)
	if err != nil {
		return false, fmt.Errorf("update status of group %d: %w", id, err)
	}
	written := false
	err = r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE duplicate_groups SET status = ?, status_reason = ?, updated_at = ?
			WHERE id = ? AND status IN (SELECT value FROM json_each(?))`,
			string(to), reason, fmtTime(nowUTC()), id, fromList)
		if err != nil {
			return wrap(err, "update status of group %d", id)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return wrap(err, "update status of group %d", id)
		}
		if n == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM duplicate_groups WHERE id = ?`, id).Scan(&one); err != nil {
				return wrap(err, "update status of group %d", id)
			}
			return nil // the group has another status: the concurrent change wins
		}
		written = true
		return r.afterStatusChange(ctx, tx, id, to, reason)
	})
	if err != nil {
		return false, err
	}
	return written, nil
}

// afterStatusChange applies the side effects of a status change inside tx: a status that blocks
// removals cancels the group's pending actions.
func (r groupRepo) afterStatusChange(ctx context.Context, tx *sql.Tx, id int64, status models.GroupStatus, reason string) error {
	if !statusBlocksRemovals(status) {
		return nil
	}
	n, err := cancelPendingActions(ctx, tx, id, statusCancelMessage(status, reason))
	if n > 0 {
		r.d.log.Info("Cancelled queued removals of a duplicate group that must not be acted on now",
			"groupId", id, "status", status, "count", n)
	}
	return err
}

// SetFlag adds (on) or removes a flag of a group in one write transaction (read, change, write:
// the single writer connection serializes it with every other write), touching nothing but the
// flags and updated_at. Idempotent: a flag already in the wanted state is not rewritten.
func (r groupRepo) SetFlag(ctx context.Context, id int64, flag string, on bool) error {
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return fmt.Errorf("set flag of group %d: empty flag", id)
	}
	return r.d.write(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT flags FROM duplicate_groups WHERE id = ?`, id).Scan(&raw); err != nil {
			return wrap(err, "set flag of group %d", id)
		}
		var flags []string
		if err := fromJSON(raw, &flags); err != nil {
			return fmt.Errorf("set flag of group %d: %w", id, err)
		}
		if slices.Contains(flags, flag) == on {
			return nil
		}
		if on {
			flags = append(flags, flag)
		} else {
			flags = slices.DeleteFunc(flags, func(f string) bool { return f == flag })
		}
		enc, err := toJSON(nonNil(flags))
		if err != nil {
			return fmt.Errorf("set flag of group %d: %w", id, err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE duplicate_groups SET flags = ?, updated_at = ? WHERE id = ?`,
			enc, fmtTime(nowUTC()), id)
		return wrap(err, "set flag of group %d", id)
	})
}

// SetOverride sets/clears ("") a file's user override. It does not re-evaluate the group, with
// one safety exception: an override of "keep" also makes the stored effective decision "keep"
// immediately and cancels a pending removal of that version, so a failed or late re-evaluation
// can never leave a file the user wants kept marked (or queued) for removal. A "queued" group
// left with nothing to run is reopened as "pending".
func (r groupRepo) SetOverride(ctx context.Context, groupID, fileID int64, d models.Decision) error {
	if !validDecision(d) {
		return fmt.Errorf("set override of file %d: invalid decision %q", fileID, d)
	}
	return r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE group_files
			SET override = ?, decision = CASE WHEN ? = ? THEN ? ELSE decision END
			WHERE id = ? AND group_id = ?`,
			string(d), string(d), string(models.DecisionKeep), string(models.DecisionKeep), fileID, groupID)
		if err != nil {
			return wrap(err, "set override of file %d in group %d", fileID, groupID)
		}
		if err := expectAffected(res, "set override of file %d in group %d", fileID, groupID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE duplicate_groups SET updated_at = ? WHERE id = ?`,
			fmtTime(nowUTC()), groupID); err != nil {
			return wrap(err, "touch group %d", groupID)
		}
		if d != models.DecisionKeep {
			return nil
		}
		_, err = cancelStaleActions(ctx, tx, groupID)
		return err
	})
}

// MarkUnseenResolved sets status=resolved for groups (not ignored/resolved) whose LastScanID
// != scanID and whose LibraryIDs intersect libraryIDs. Returns the affected group IDs
// (ascending); an empty libraryIDs affects nothing.
//
// Safety: in the same transaction, pending actions of the resolved groups are cancelled — the
// content is no longer a duplicate, so a removal approved earlier must not run against it.
func (r groupRepo) MarkUnseenResolved(ctx context.Context, scanID int64, libraryIDs []int64) ([]int64, error) {
	if len(libraryIDs) == 0 {
		return []int64{}, nil
	}
	list, err := jsonList(libraryIDs)
	if err != nil {
		return nil, fmt.Errorf("mark unseen groups resolved: %w", err)
	}
	ids := []int64{}
	err = r.d.write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `UPDATE duplicate_groups
			SET status = ?, status_reason = ?, updated_at = ?
			WHERE status NOT IN (?, ?) AND last_scan_id <> ?
			  AND EXISTS (SELECT 1 FROM json_each(duplicate_groups.library_ids) AS l
			              WHERE l.value IN (SELECT value FROM json_each(?)))
			RETURNING id`,
			string(models.GroupResolved), resolvedReason, fmtTime(nowUTC()),
			string(models.GroupIgnored), string(models.GroupResolved), scanID, list)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		idList, err := jsonList(ids)
		if err != nil {
			return err
		}
		_, err = cancelPending(ctx, tx, cancelResolvedMessage, `group_id IN (SELECT value FROM json_each(?))`, idList)
		return err
	})
	if err != nil {
		return nil, wrap(err, "mark unseen groups resolved")
	}
	slices.Sort(ids)
	return ids, nil
}

// Stats returns counters for the Duplicates page header. ByStatus contains every known status
// (0 when absent).
func (r groupRepo) Stats(ctx context.Context) (store.GroupStats, error) {
	st := store.GroupStats{ByStatus: make(map[models.GroupStatus]int, len(knownGroupStatuses))}
	for _, s := range knownGroupStatuses {
		st.ByStatus[s] = 0
	}
	err := r.d.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT status, COUNT(*),
			COALESCE(SUM(CASE WHEN status IN (?, ?, ?) THEN reclaimable_bytes ELSE 0 END), 0)
			FROM duplicate_groups GROUP BY status`,
			string(models.GroupPending), string(models.GroupReview), string(models.GroupQueued))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				status string
				n      int
				bytes  int64
			)
			if err := rows.Scan(&status, &n, &bytes); err != nil {
				return err
			}
			st.ByStatus[models.GroupStatus(status)] = n
			st.Total += n
			st.ReclaimableBytes += bytes
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, reclaimedBytesQuery, reclaimedBytesArgs()...).Scan(&st.ReclaimedBytes)
	})
	if err != nil {
		return store.GroupStats{}, wrap(err, "group stats")
	}
	return st, nil
}

// Delete removes a group and its files. Pending actions of the group are cancelled (not deleted:
// actions are the audit trail) so the executor can never act on a group that no longer exists.
func (r groupRepo) Delete(ctx context.Context, id int64) error {
	return r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM duplicate_groups WHERE id = ?`, id)
		if err != nil {
			return wrap(err, "delete group %d", id)
		}
		if err := expectAffected(res, "delete group %d", id); err != nil {
			return err
		}
		_, err = cancelPendingActions(ctx, tx, id, cancelDeletedMessage)
		return err
	})
}
