package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// ---------------------------------------------------------------------------
// Media servers
// ---------------------------------------------------------------------------

type mediaServerRepo struct{ d *DB }

const mediaServerColumns = `id, name, kind, url, token, machine_identifier, verify_tls, enabled, storage, created_at, updated_at`

func scanMediaServer(s scanner) (models.MediaServer, error) {
	var (
		m                models.MediaServer
		created, updated string
	)
	if err := s.Scan(&m.ID, &m.Name, &m.Kind, &m.URL, &m.Token, &m.MachineIdentifier,
		&m.VerifyTLS, &m.Enabled, &m.Storage, &created, &updated); err != nil {
		return models.MediaServer{}, err
	}
	var err error
	if m.CreatedAt, err = parseTime(created); err != nil {
		return models.MediaServer{}, err
	}
	if m.UpdatedAt, err = parseTime(updated); err != nil {
		return models.MediaServer{}, err
	}
	return m, nil
}

func (r mediaServerRepo) List(ctx context.Context) ([]models.MediaServer, error) {
	out, err := queryAll(ctx, r.d.r, scanMediaServer, `SELECT `+mediaServerColumns+` FROM media_servers ORDER BY id`)
	if err != nil {
		return nil, wrap(err, "list media servers")
	}
	return out, nil
}

func (r mediaServerRepo) Get(ctx context.Context, id int64) (*models.MediaServer, error) {
	m, err := scanMediaServer(r.d.r.QueryRowContext(ctx, `SELECT `+mediaServerColumns+` FROM media_servers WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get media server %d", id)
	}
	return &m, nil
}

// Create inserts s and sets its ID, CreatedAt (when zero) and UpdatedAt. See unconfirmArrLinks
// for the *arr links a new media server affects.
func (r mediaServerRepo) Create(ctx context.Context, s *models.MediaServer) error {
	now := nowUTC()
	created := orNow(s.CreatedAt, now)
	var id int64
	err := r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO media_servers
			(name, kind, url, token, machine_identifier, verify_tls, enabled, storage, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.Name, s.Kind, s.URL, s.Token, s.MachineIdentifier, b2i(s.VerifyTLS), b2i(s.Enabled), s.Storage,
			fmtTime(created), fmtTime(now))
		if err != nil {
			return wrap(err, "create media server")
		}
		if id, err = res.LastInsertId(); err != nil {
			return wrap(err, "create media server")
		}
		if comparedServer(s.Kind, s.Enabled, s.Storage) {
			return r.unconfirmArrLinks(ctx, tx, id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.ID, s.CreatedAt, s.UpdatedAt = id, created, now
	return nil
}

// Update replaces the mutable fields of s (by ID) and sets UpdatedAt. See unconfirmArrLinks for
// the *arr links enabling a media server (or declaring it shared storage) affects.
func (r mediaServerRepo) Update(ctx context.Context, s *models.MediaServer) error {
	now := nowUTC()
	err := r.d.write(ctx, func(tx *sql.Tx) error {
		var (
			kind    models.MediaServerKind
			storage string
			enabled bool
		)
		switch err := tx.QueryRowContext(ctx, `SELECT kind, enabled, storage FROM media_servers WHERE id = ?`, s.ID).
			Scan(&kind, &enabled, &storage); {
		case errors.Is(err, sql.ErrNoRows):
			// The UPDATE below reports the missing row.
		case err != nil:
			return wrap(err, "update media server %d", s.ID)
		}
		res, err := tx.ExecContext(ctx, `UPDATE media_servers SET
			name = ?, kind = ?, url = ?, token = ?, machine_identifier = ?, verify_tls = ?, enabled = ?, storage = ?, updated_at = ?
			WHERE id = ?`,
			s.Name, s.Kind, s.URL, s.Token, s.MachineIdentifier, b2i(s.VerifyTLS), b2i(s.Enabled), s.Storage, fmtTime(now), s.ID)
		if err != nil {
			return wrap(err, "update media server %d", s.ID)
		}
		if err := expectAffected(res, "update media server %d", s.ID); err != nil {
			return err
		}
		if comparedServer(s.Kind, s.Enabled, s.Storage) && !comparedServer(kind, enabled, storage) {
			return r.unconfirmArrLinks(ctx, tx, s.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.UpdatedAt = now
	return nil
}

// comparedServer reports an enabled Plex server that is not declared separate storage: one whose
// versions *arr files may be matched to by raw path or by name and size (docs/DECISIONS.md D11).
func comparedServer(kind models.MediaServerKind, enabled bool, storage string) bool {
	return enabled && (kind == models.MediaServerPlex || kind == "") &&
		!strings.EqualFold(strings.TrimSpace(storage), models.StorageSeparate)
}

// unconfirmArrLinks runs in the write transaction that added, enabled or declared shared storage a
// media server (id): when two or more Plex servers are then enabled, the *arr instances whose links
// were confirmed without this server were confirmed while a person could not choose it (or while
// only one server was enabled, when the links are confirmed automatically). "Not linked" would
// then mean "untracked" for its versions, so their links count as unconfirmed again until a person
// saves them: rules 2 and 3 stay off for the instance and the versions it may track go to review.
func (r mediaServerRepo) unconfirmArrLinks(ctx context.Context, tx *sql.Tx, id int64) error {
	var enabled int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_servers WHERE enabled = 1 AND kind IN (?, '')`,
		models.MediaServerPlex).Scan(&enabled); err != nil {
		return wrap(err, "count media servers")
	}
	if enabled < 2 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE arr_instances SET links_confirmed = 0
		WHERE links_confirmed = 1 AND id NOT IN (SELECT arr_id FROM arr_server_links WHERE server_id = ?)`, id)
	if err != nil {
		return wrap(err, "unconfirm the media server links of the *arr instances")
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		r.d.log.Info("A media server was added or enabled: confirm again which Plex servers each *arr instance feeds (Settings → Applications)",
			"instances", n)
	}
	return nil
}

// Delete removes the server, its libraries (foreign key cascade) and its path mappings. Its
// duplicate groups are kept (they carry user decisions such as ignores), but their pending
// actions are cancelled: nothing may be removed through a server Dupearr no longer manages.
func (r mediaServerRepo) Delete(ctx context.Context, id int64) error {
	return r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM media_servers WHERE id = ?`, id)
		if err != nil {
			return wrap(err, "delete media server %d", id)
		}
		if err := expectAffected(res, "delete media server %d", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM path_mappings WHERE source_type = ? AND source_id = ?`,
			models.PathSourceServer, id); err != nil {
			return wrap(err, "delete path mappings of media server %d", id)
		}
		if _, err := cancelPending(ctx, tx, cancelServerMessage,
			`group_id IN (SELECT id FROM duplicate_groups WHERE server_id = ?)`, id); err != nil {
			return fmt.Errorf("media server %d: %w", id, err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Libraries
// ---------------------------------------------------------------------------

type libraryRepo struct{ d *DB }

const libraryColumns = `id, server_id, section_key, title, type, locations, enabled, profile_id, scope_group, updated_at`

// libraryOrder lists libraries grouped by server, then by title.
const libraryOrder = ` ORDER BY server_id, title COLLATE NOCASE, id`

func scanLibrary(s scanner) (models.Library, error) {
	var (
		l         models.Library
		locations string
		profileID sql.NullInt64
		updated   string
	)
	if err := s.Scan(&l.ID, &l.ServerID, &l.SectionKey, &l.Title, &l.Type, &locations, &l.Enabled,
		&profileID, &l.ScopeGroup, &updated); err != nil {
		return models.Library{}, err
	}
	if err := fromJSON(locations, &l.Locations); err != nil {
		return models.Library{}, fmt.Errorf("library %d locations: %w", l.ID, err)
	}
	l.Locations = nonNil(l.Locations)
	l.ProfileID = ptrInt64(profileID)
	var err error
	if l.UpdatedAt, err = parseTime(updated); err != nil {
		return models.Library{}, err
	}
	return l, nil
}

func (r libraryRepo) List(ctx context.Context) ([]models.Library, error) {
	out, err := queryAll(ctx, r.d.r, scanLibrary, `SELECT `+libraryColumns+` FROM libraries`+libraryOrder)
	if err != nil {
		return nil, wrap(err, "list libraries")
	}
	return out, nil
}

func (r libraryRepo) ListByServer(ctx context.Context, serverID int64) ([]models.Library, error) {
	out, err := queryAll(ctx, r.d.r, scanLibrary,
		`SELECT `+libraryColumns+` FROM libraries WHERE server_id = ?`+libraryOrder, serverID)
	if err != nil {
		return nil, wrap(err, "list libraries of server %d", serverID)
	}
	return out, nil
}

func (r libraryRepo) Get(ctx context.Context, id int64) (*models.Library, error) {
	l, err := scanLibrary(r.d.r.QueryRowContext(ctx, `SELECT `+libraryColumns+` FROM libraries WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get library %d", id)
	}
	return &l, nil
}

// Sync upserts the given sections for a server (matched by SectionKey), preserving user fields
// (Enabled, ProfileID, ScopeGroup) of existing rows, and deletes rows not present. New libraries
// default to Enabled=true (ProfileID/ScopeGroup are taken from the section). Returns the
// server's libraries after the sync.
func (r libraryRepo) Sync(ctx context.Context, serverID int64, sections []models.Library) ([]models.Library, error) {
	seen := make(map[string]bool, len(sections))
	for _, s := range sections {
		if strings.TrimSpace(s.SectionKey) == "" {
			return nil, fmt.Errorf("sync libraries of server %d: section %q has an empty section key", serverID, s.Title)
		}
		if seen[s.SectionKey] {
			return nil, fmt.Errorf("sync libraries of server %d: duplicate section key %q", serverID, s.SectionKey)
		}
		seen[s.SectionKey] = true
	}

	var out []models.Library
	err := r.d.write(ctx, func(tx *sql.Tx) error {
		existing := map[string]int64{}
		rows, err := tx.QueryContext(ctx, `SELECT id, section_key FROM libraries WHERE server_id = ?`, serverID)
		if err != nil {
			return wrap(err, "read libraries")
		}
		for rows.Next() {
			var (
				id  int64
				key string
			)
			if err := rows.Scan(&id, &key); err != nil {
				_ = rows.Close()
				return wrap(err, "read libraries")
			}
			existing[key] = id
		}
		if err := rows.Close(); err != nil {
			return wrap(err, "read libraries")
		}
		if err := rows.Err(); err != nil {
			return wrap(err, "read libraries")
		}

		now := fmtTime(nowUTC())
		for _, s := range sections {
			locations, err := toJSON(nonNil(s.Locations))
			if err != nil {
				return fmt.Errorf("library %q: %w", s.SectionKey, err)
			}
			if id, ok := existing[s.SectionKey]; ok {
				if _, err := tx.ExecContext(ctx,
					`UPDATE libraries SET title = ?, type = ?, locations = ?, updated_at = ? WHERE id = ?`,
					s.Title, s.Type, locations, now, id); err != nil {
					return wrap(err, "update library %q", s.SectionKey)
				}
				delete(existing, s.SectionKey)
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO libraries
				(server_id, section_key, title, type, locations, enabled, profile_id, scope_group, updated_at)
				VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
				serverID, s.SectionKey, s.Title, s.Type, locations, nullInt64(s.ProfileID), s.ScopeGroup, now); err != nil {
				return wrap(err, "insert library %q", s.SectionKey)
			}
		}
		// Whatever is left in existing no longer exists on the server.
		for key, id := range existing {
			if _, err := tx.ExecContext(ctx, `DELETE FROM libraries WHERE id = ?`, id); err != nil {
				return wrap(err, "delete library %q", key)
			}
		}

		out, err = queryAll(ctx, tx, scanLibrary,
			`SELECT `+libraryColumns+` FROM libraries WHERE server_id = ?`+libraryOrder, serverID)
		return wrap(err, "reload libraries")
	})
	if err != nil {
		return nil, fmt.Errorf("sync libraries of server %d: %w", serverID, err)
	}
	return out, nil
}

// Update replaces the mutable fields of l (by ID); ServerID and SectionKey are immutable.
func (r libraryRepo) Update(ctx context.Context, l *models.Library) error {
	locations, err := toJSON(nonNil(l.Locations))
	if err != nil {
		return fmt.Errorf("update library %d: %w", l.ID, err)
	}
	now := nowUTC()
	res, err := r.d.w.ExecContext(ctx, `UPDATE libraries SET
		title = ?, type = ?, locations = ?, enabled = ?, profile_id = ?, scope_group = ?, updated_at = ?
		WHERE id = ?`,
		l.Title, l.Type, locations, b2i(l.Enabled), nullInt64(l.ProfileID), l.ScopeGroup, fmtTime(now), l.ID)
	if err != nil {
		return wrap(err, "update library %d", l.ID)
	}
	if err := expectAffected(res, "update library %d", l.ID); err != nil {
		return err
	}
	l.UpdatedAt = now
	return nil
}

// ---------------------------------------------------------------------------
// *arr instances
// ---------------------------------------------------------------------------

type arrInstanceRepo struct{ d *DB }

const arrInstanceColumns = `id, name, kind, url, api_key, verify_tls, enabled, tags, links_confirmed, created_at, updated_at`

func scanArrInstance(s scanner) (models.ArrInstance, error) {
	var (
		a                      models.ArrInstance
		tags, created, updated string
	)
	if err := s.Scan(&a.ID, &a.Name, &a.Kind, &a.URL, &a.APIKey, &a.VerifyTLS, &a.Enabled, &tags,
		&a.LinksConfirmed, &created, &updated); err != nil {
		return models.ArrInstance{}, err
	}
	a.ServerIDs = []int64{} // filled by loadArrLinks
	if err := fromJSON(tags, &a.Tags); err != nil {
		return models.ArrInstance{}, fmt.Errorf("arr instance %d tags: %w", a.ID, err)
	}
	a.Tags = nonNil(a.Tags)
	var err error
	if a.CreatedAt, err = parseTime(created); err != nil {
		return models.ArrInstance{}, err
	}
	if a.UpdatedAt, err = parseTime(updated); err != nil {
		return models.ArrInstance{}, err
	}
	return a, nil
}

func (r arrInstanceRepo) List(ctx context.Context) ([]models.ArrInstance, error) {
	out, err := queryAll(ctx, r.d.r, scanArrInstance, `SELECT `+arrInstanceColumns+` FROM arr_instances ORDER BY id`)
	if err != nil {
		return nil, wrap(err, "list arr instances")
	}
	if err := loadArrLinks(ctx, r.d.r, out, 0); err != nil {
		return nil, err
	}
	return out, nil
}

func (r arrInstanceRepo) Get(ctx context.Context, id int64) (*models.ArrInstance, error) {
	a, err := scanArrInstance(r.d.r.QueryRowContext(ctx, `SELECT `+arrInstanceColumns+` FROM arr_instances WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get arr instance %d", id)
	}
	out := []models.ArrInstance{a}
	if err := loadArrLinks(ctx, r.d.r, out, id); err != nil {
		return nil, err
	}
	return &out[0], nil
}

// loadArrLinks fills the ServerIDs (sorted) of insts from arr_server_links with one query (for the
// instance id only, when it is not 0).
func loadArrLinks(ctx context.Context, q *sql.DB, insts []models.ArrInstance, id int64) error {
	if len(insts) == 0 {
		return nil
	}
	query, args := `SELECT arr_id, server_id FROM arr_server_links ORDER BY arr_id, server_id`, []any{}
	if id != 0 {
		query, args = `SELECT arr_id, server_id FROM arr_server_links WHERE arr_id = ? ORDER BY server_id`, []any{id}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return wrap(err, "list arr server links")
	}
	links := map[int64][]int64{}
	for rows.Next() {
		var arrID, serverID int64
		if err := rows.Scan(&arrID, &serverID); err != nil {
			_ = rows.Close()
			return wrap(err, "list arr server links")
		}
		links[arrID] = append(links[arrID], serverID)
	}
	if err := rows.Close(); err != nil {
		return wrap(err, "list arr server links")
	}
	if err := rows.Err(); err != nil {
		return wrap(err, "list arr server links")
	}
	for i := range insts {
		if ids := links[insts[i].ID]; ids != nil {
			insts[i].ServerIDs = ids
		}
	}
	return nil
}

// replaceArrLinks stores exactly serverIDs as the media server links of instance id, in the
// caller's write transaction: an instance is never stored with only part of its links.
func replaceArrLinks(ctx context.Context, tx *sql.Tx, id int64, serverIDs []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM arr_server_links WHERE arr_id = ?`, id); err != nil {
		return wrap(err, "replace the media server links of arr instance %d", id)
	}
	for _, sid := range serverIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO arr_server_links (arr_id, server_id) VALUES (?, ?)`, id, sid); err != nil {
			return wrap(err, "link arr instance %d to media server %d", id, sid)
		}
	}
	return nil
}

// sortedLinks returns the distinct server ids, sorted (never nil).
func sortedLinks(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Create inserts a with its media server links (ServerIDs, LinksConfirmed) and sets its ID,
// CreatedAt (when zero) and UpdatedAt.
func (r arrInstanceRepo) Create(ctx context.Context, a *models.ArrInstance) error {
	tags, err := toJSON(nonNil(a.Tags))
	if err != nil {
		return fmt.Errorf("create arr instance: %w", err)
	}
	now := nowUTC()
	created := orNow(a.CreatedAt, now)
	links := sortedLinks(a.ServerIDs)
	var id int64
	err = r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO arr_instances
			(name, kind, url, api_key, verify_tls, enabled, tags, links_confirmed, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.Name, a.Kind, a.URL, a.APIKey, b2i(a.VerifyTLS), b2i(a.Enabled), tags, b2i(a.LinksConfirmed),
			fmtTime(created), fmtTime(now))
		if err != nil {
			return wrap(err, "create arr instance")
		}
		if id, err = res.LastInsertId(); err != nil {
			return wrap(err, "create arr instance")
		}
		return replaceArrLinks(ctx, tx, id, links)
	})
	if err != nil {
		return err
	}
	a.ID, a.CreatedAt, a.UpdatedAt, a.Tags, a.ServerIDs = id, created, now, nonNil(a.Tags), links
	return nil
}

// Update replaces the mutable fields of a (by ID) and sets UpdatedAt. Its media server links are
// replaced by ServerIDs in the same transaction; a nil ServerIDs keeps the stored links (callers
// that never loaded them cannot drop them by accident), while LinksConfirmed is always written.
func (r arrInstanceRepo) Update(ctx context.Context, a *models.ArrInstance) error {
	tags, err := toJSON(nonNil(a.Tags))
	if err != nil {
		return fmt.Errorf("update arr instance %d: %w", a.ID, err)
	}
	now := nowUTC()
	err = r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE arr_instances SET
			name = ?, kind = ?, url = ?, api_key = ?, verify_tls = ?, enabled = ?, tags = ?, links_confirmed = ?, updated_at = ?
			WHERE id = ?`,
			a.Name, a.Kind, a.URL, a.APIKey, b2i(a.VerifyTLS), b2i(a.Enabled), tags, b2i(a.LinksConfirmed), fmtTime(now), a.ID)
		if err != nil {
			return wrap(err, "update arr instance %d", a.ID)
		}
		if err := expectAffected(res, "update arr instance %d", a.ID); err != nil {
			return err
		}
		if a.ServerIDs == nil {
			return nil
		}
		return replaceArrLinks(ctx, tx, a.ID, sortedLinks(a.ServerIDs))
	})
	if err != nil {
		return err
	}
	a.UpdatedAt = now
	if a.ServerIDs != nil {
		a.ServerIDs = sortedLinks(a.ServerIDs)
	}
	return nil
}

// Delete removes the instance and its path mappings. Pending actions of every group containing a
// version tracked by the instance are cancelled in the same transaction: those decisions (and the
// removal method, re-link rescans and unmonitoring) were planned with the instance's data, and a
// removal behind the back of an *arr that still tracks the file can cause a re-download loop.
func (r arrInstanceRepo) Delete(ctx context.Context, id int64) error {
	return r.d.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM arr_instances WHERE id = ?`, id)
		if err != nil {
			return wrap(err, "delete arr instance %d", id)
		}
		if err := expectAffected(res, "delete arr instance %d", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM path_mappings WHERE source_type = ? AND source_id = ?`,
			models.PathSourceArr, id); err != nil {
			return wrap(err, "delete path mappings of arr instance %d", id)
		}
		n, err := cancelPending(ctx, tx, cancelArrMessage, `group_id IN (SELECT f.group_id FROM group_files f
			WHERE json_extract(f.version, '$.arr.instanceId') = ?)`, id)
		if err != nil {
			return fmt.Errorf("arr instance %d: %w", id, err)
		}
		if n > 0 {
			r.d.log.Info("Cancelled queued removals planned with a deleted *arr instance", "instanceId", id, "count", n)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Tautulli connections
// ---------------------------------------------------------------------------

type tautulliRepo struct{ d *DB }

const tautulliColumns = `id, name, server_id, url, api_key, verify_tls, enabled, created_at, updated_at`

func scanTautulli(s scanner) (models.TautulliInstance, error) {
	var (
		t                models.TautulliInstance
		created, updated string
	)
	if err := s.Scan(&t.ID, &t.Name, &t.ServerID, &t.URL, &t.APIKey, &t.VerifyTLS, &t.Enabled, &created, &updated); err != nil {
		return models.TautulliInstance{}, err
	}
	var err error
	if t.CreatedAt, err = parseTime(created); err != nil {
		return models.TautulliInstance{}, err
	}
	if t.UpdatedAt, err = parseTime(updated); err != nil {
		return models.TautulliInstance{}, err
	}
	return t, nil
}

func (r tautulliRepo) List(ctx context.Context) ([]models.TautulliInstance, error) {
	out, err := queryAll(ctx, r.d.r, scanTautulli, `SELECT `+tautulliColumns+` FROM tautulli_instances ORDER BY id`)
	if err != nil {
		return nil, wrap(err, "list tautulli connections")
	}
	return out, nil
}

func (r tautulliRepo) Get(ctx context.Context, id int64) (*models.TautulliInstance, error) {
	t, err := scanTautulli(r.d.r.QueryRowContext(ctx, `SELECT `+tautulliColumns+` FROM tautulli_instances WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get tautulli connection %d", id)
	}
	return &t, nil
}

// Create inserts t and sets its ID, CreatedAt (when zero) and UpdatedAt.
func (r tautulliRepo) Create(ctx context.Context, t *models.TautulliInstance) error {
	now := nowUTC()
	created := orNow(t.CreatedAt, now)
	res, err := r.d.w.ExecContext(ctx, `INSERT INTO tautulli_instances
		(name, server_id, url, api_key, verify_tls, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.ServerID, t.URL, t.APIKey, b2i(t.VerifyTLS), b2i(t.Enabled), fmtTime(created), fmtTime(now))
	if err != nil {
		return wrap(err, "create tautulli connection")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "create tautulli connection")
	}
	t.ID, t.CreatedAt, t.UpdatedAt = id, created, now
	return nil
}

// Update replaces the mutable fields of t (by ID) and sets UpdatedAt.
func (r tautulliRepo) Update(ctx context.Context, t *models.TautulliInstance) error {
	now := nowUTC()
	res, err := r.d.w.ExecContext(ctx, `UPDATE tautulli_instances SET
		name = ?, server_id = ?, url = ?, api_key = ?, verify_tls = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		t.Name, t.ServerID, t.URL, t.APIKey, b2i(t.VerifyTLS), b2i(t.Enabled), fmtTime(now), t.ID)
	if err != nil {
		return wrap(err, "update tautulli connection %d", t.ID)
	}
	if err := expectAffected(res, "update tautulli connection %d", t.ID); err != nil {
		return err
	}
	t.UpdatedAt = now
	return nil
}

// Delete removes the connection. Nothing refers to it: the play history it provided stays in the
// stored versions until the next scan replaces it (as unknown: no source).
func (r tautulliRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.d.w.ExecContext(ctx, `DELETE FROM tautulli_instances WHERE id = ?`, id)
	if err != nil {
		return wrap(err, "delete tautulli connection %d", id)
	}
	return expectAffected(res, "delete tautulli connection %d", id)
}

// ---------------------------------------------------------------------------
// Path mappings
// ---------------------------------------------------------------------------

type pathMappingRepo struct{ d *DB }

const pathMappingColumns = `id, source_type, source_id, remote_path, local_path`

func scanPathMapping(s scanner) (models.PathMapping, error) {
	var m models.PathMapping
	err := s.Scan(&m.ID, &m.SourceType, &m.SourceID, &m.RemotePath, &m.LocalPath)
	return m, err
}

func validatePathMapping(m *models.PathMapping) error {
	if m.SourceType != models.PathSourceServer && m.SourceType != models.PathSourceArr {
		return fmt.Errorf("invalid path mapping source type %q (want %q or %q)", m.SourceType, models.PathSourceServer, models.PathSourceArr)
	}
	return nil
}

func (r pathMappingRepo) List(ctx context.Context) ([]models.PathMapping, error) {
	out, err := queryAll(ctx, r.d.r, scanPathMapping, `SELECT `+pathMappingColumns+` FROM path_mappings ORDER BY id`)
	if err != nil {
		return nil, wrap(err, "list path mappings")
	}
	return out, nil
}

func (r pathMappingRepo) Get(ctx context.Context, id int64) (*models.PathMapping, error) {
	m, err := scanPathMapping(r.d.r.QueryRowContext(ctx, `SELECT `+pathMappingColumns+` FROM path_mappings WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get path mapping %d", id)
	}
	return &m, nil
}

func (r pathMappingRepo) Create(ctx context.Context, m *models.PathMapping) error {
	if err := validatePathMapping(m); err != nil {
		return fmt.Errorf("create path mapping: %w", err)
	}
	res, err := r.d.w.ExecContext(ctx,
		`INSERT INTO path_mappings (source_type, source_id, remote_path, local_path) VALUES (?, ?, ?, ?)`,
		m.SourceType, m.SourceID, m.RemotePath, m.LocalPath)
	if err != nil {
		return wrap(err, "create path mapping")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "create path mapping")
	}
	m.ID = id
	return nil
}

func (r pathMappingRepo) Update(ctx context.Context, m *models.PathMapping) error {
	if err := validatePathMapping(m); err != nil {
		return fmt.Errorf("update path mapping %d: %w", m.ID, err)
	}
	res, err := r.d.w.ExecContext(ctx,
		`UPDATE path_mappings SET source_type = ?, source_id = ?, remote_path = ?, local_path = ? WHERE id = ?`,
		m.SourceType, m.SourceID, m.RemotePath, m.LocalPath, m.ID)
	if err != nil {
		return wrap(err, "update path mapping %d", m.ID)
	}
	return expectAffected(res, "update path mapping %d", m.ID)
}

func (r pathMappingRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.d.w.ExecContext(ctx, `DELETE FROM path_mappings WHERE id = ?`, id)
	if err != nil {
		return wrap(err, "delete path mapping %d", id)
	}
	return expectAffected(res, "delete path mapping %d", id)
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

type notificationRepo struct{ d *DB }

const notificationColumns = `id, name, kind, settings, triggers, enabled`

func scanNotification(s scanner) (models.NotificationConfig, error) {
	var (
		n                  models.NotificationConfig
		settings, triggers string
	)
	if err := s.Scan(&n.ID, &n.Name, &n.Kind, &settings, &triggers, &n.Enabled); err != nil {
		return models.NotificationConfig{}, err
	}
	if settings == "" {
		settings = "{}"
	}
	n.Settings = []byte(settings)
	if err := fromJSON(triggers, &n.Triggers); err != nil {
		return models.NotificationConfig{}, fmt.Errorf("notification %d triggers: %w", n.ID, err)
	}
	n.Triggers = nonNil(n.Triggers)
	return n, nil
}

// notificationArgs validates and encodes the JSON columns of n.
func notificationArgs(n *models.NotificationConfig) (settings, triggers string, err error) {
	if settings, err = rawOrDefault(n.Settings, "{}"); err != nil {
		return "", "", fmt.Errorf("settings: %w", err)
	}
	if triggers, err = toJSON(nonNil(n.Triggers)); err != nil {
		return "", "", fmt.Errorf("triggers: %w", err)
	}
	return settings, triggers, nil
}

func (r notificationRepo) List(ctx context.Context) ([]models.NotificationConfig, error) {
	out, err := queryAll(ctx, r.d.r, scanNotification, `SELECT `+notificationColumns+` FROM notifications ORDER BY id`)
	if err != nil {
		return nil, wrap(err, "list notifications")
	}
	return out, nil
}

func (r notificationRepo) Get(ctx context.Context, id int64) (*models.NotificationConfig, error) {
	n, err := scanNotification(r.d.r.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM notifications WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get notification %d", id)
	}
	return &n, nil
}

func (r notificationRepo) Create(ctx context.Context, n *models.NotificationConfig) error {
	settings, triggers, err := notificationArgs(n)
	if err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	res, err := r.d.w.ExecContext(ctx,
		`INSERT INTO notifications (name, kind, settings, triggers, enabled) VALUES (?, ?, ?, ?, ?)`,
		n.Name, n.Kind, settings, triggers, b2i(n.Enabled))
	if err != nil {
		return wrap(err, "create notification")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "create notification")
	}
	n.ID = id
	return nil
}

func (r notificationRepo) Update(ctx context.Context, n *models.NotificationConfig) error {
	settings, triggers, err := notificationArgs(n)
	if err != nil {
		return fmt.Errorf("update notification %d: %w", n.ID, err)
	}
	res, err := r.d.w.ExecContext(ctx,
		`UPDATE notifications SET name = ?, kind = ?, settings = ?, triggers = ?, enabled = ? WHERE id = ?`,
		n.Name, n.Kind, settings, triggers, b2i(n.Enabled), n.ID)
	if err != nil {
		return wrap(err, "update notification %d", n.ID)
	}
	return expectAffected(res, "update notification %d", n.ID)
}

func (r notificationRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.d.w.ExecContext(ctx, `DELETE FROM notifications WHERE id = ?`, id)
	if err != nil {
		return wrap(err, "delete notification %d", id)
	}
	return expectAffected(res, "delete notification %d", id)
}
