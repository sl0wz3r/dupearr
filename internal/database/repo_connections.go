package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// ---------------------------------------------------------------------------
// Media servers
// ---------------------------------------------------------------------------

type mediaServerRepo struct{ d *DB }

const mediaServerColumns = `id, name, kind, url, token, machine_identifier, verify_tls, enabled, created_at, updated_at`

func scanMediaServer(s scanner) (models.MediaServer, error) {
	var (
		m                models.MediaServer
		created, updated string
	)
	if err := s.Scan(&m.ID, &m.Name, &m.Kind, &m.URL, &m.Token, &m.MachineIdentifier,
		&m.VerifyTLS, &m.Enabled, &created, &updated); err != nil {
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

// Create inserts s and sets its ID, CreatedAt (when zero) and UpdatedAt.
func (r mediaServerRepo) Create(ctx context.Context, s *models.MediaServer) error {
	now := nowUTC()
	created := orNow(s.CreatedAt, now)
	res, err := r.d.w.ExecContext(ctx, `INSERT INTO media_servers
		(name, kind, url, token, machine_identifier, verify_tls, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Name, s.Kind, s.URL, s.Token, s.MachineIdentifier, b2i(s.VerifyTLS), b2i(s.Enabled),
		fmtTime(created), fmtTime(now))
	if err != nil {
		return wrap(err, "create media server")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "create media server")
	}
	s.ID, s.CreatedAt, s.UpdatedAt = id, created, now
	return nil
}

// Update replaces the mutable fields of s (by ID) and sets UpdatedAt.
func (r mediaServerRepo) Update(ctx context.Context, s *models.MediaServer) error {
	now := nowUTC()
	res, err := r.d.w.ExecContext(ctx, `UPDATE media_servers SET
		name = ?, kind = ?, url = ?, token = ?, machine_identifier = ?, verify_tls = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		s.Name, s.Kind, s.URL, s.Token, s.MachineIdentifier, b2i(s.VerifyTLS), b2i(s.Enabled), fmtTime(now), s.ID)
	if err != nil {
		return wrap(err, "update media server %d", s.ID)
	}
	if err := expectAffected(res, "update media server %d", s.ID); err != nil {
		return err
	}
	s.UpdatedAt = now
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

const arrInstanceColumns = `id, name, kind, url, api_key, verify_tls, enabled, tags, created_at, updated_at`

func scanArrInstance(s scanner) (models.ArrInstance, error) {
	var (
		a                      models.ArrInstance
		tags, created, updated string
	)
	if err := s.Scan(&a.ID, &a.Name, &a.Kind, &a.URL, &a.APIKey, &a.VerifyTLS, &a.Enabled, &tags,
		&created, &updated); err != nil {
		return models.ArrInstance{}, err
	}
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
	return out, nil
}

func (r arrInstanceRepo) Get(ctx context.Context, id int64) (*models.ArrInstance, error) {
	a, err := scanArrInstance(r.d.r.QueryRowContext(ctx, `SELECT `+arrInstanceColumns+` FROM arr_instances WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get arr instance %d", id)
	}
	return &a, nil
}

// Create inserts a and sets its ID, CreatedAt (when zero) and UpdatedAt.
func (r arrInstanceRepo) Create(ctx context.Context, a *models.ArrInstance) error {
	tags, err := toJSON(nonNil(a.Tags))
	if err != nil {
		return fmt.Errorf("create arr instance: %w", err)
	}
	now := nowUTC()
	created := orNow(a.CreatedAt, now)
	res, err := r.d.w.ExecContext(ctx, `INSERT INTO arr_instances
		(name, kind, url, api_key, verify_tls, enabled, tags, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Name, a.Kind, a.URL, a.APIKey, b2i(a.VerifyTLS), b2i(a.Enabled), tags, fmtTime(created), fmtTime(now))
	if err != nil {
		return wrap(err, "create arr instance")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "create arr instance")
	}
	a.ID, a.CreatedAt, a.UpdatedAt, a.Tags = id, created, now, nonNil(a.Tags)
	return nil
}

// Update replaces the mutable fields of a (by ID) and sets UpdatedAt.
func (r arrInstanceRepo) Update(ctx context.Context, a *models.ArrInstance) error {
	tags, err := toJSON(nonNil(a.Tags))
	if err != nil {
		return fmt.Errorf("update arr instance %d: %w", a.ID, err)
	}
	now := nowUTC()
	res, err := r.d.w.ExecContext(ctx, `UPDATE arr_instances SET
		name = ?, kind = ?, url = ?, api_key = ?, verify_tls = ?, enabled = ?, tags = ?, updated_at = ?
		WHERE id = ?`,
		a.Name, a.Kind, a.URL, a.APIKey, b2i(a.VerifyTLS), b2i(a.Enabled), tags, fmtTime(now), a.ID)
	if err != nil {
		return wrap(err, "update arr instance %d", a.ID)
	}
	if err := expectAffected(res, "update arr instance %d", a.ID); err != nil {
		return err
	}
	a.UpdatedAt = now
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
