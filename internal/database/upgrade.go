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

// Data upgrades: changes to stored rows (not the schema) that a new version needs once. Each is
// recorded by a settings entry, so it runs exactly once per database: a user who later undoes the
// change keeps that choice.
//
// A database without any profile records nothing: backup restore rebuilds a backup into an empty
// database created by Open and then copies the backup's rows (settings included) into it, so the
// marker must come with the rows — a backup from an older build has none, and the upgrade then
// runs on its profiles when the restored database is opened.

// discProfilesMarker records the full-disc profile upgrade (docs/DECISIONS.md D9).
const discProfilesMarker = "upgrade.discProfiles"

// arrLinksMarker records the *arr ↔ media server link upgrade (docs/DECISIONS.md D11).
const arrLinksMarker = "upgrade.arrServerLinks"

// upgradeData applies the pending data upgrades (Open calls it after the schema migrations).
func (d *DB) upgradeData(ctx context.Context) error {
	if err := d.upgradeDiscProfiles(ctx); err != nil {
		return err
	}
	return d.upgradeArrLinks(ctx)
}

// upgradeArrLinks links the *arr instances of a one-server installation to that server, once:
// with exactly one enabled Plex server every instance without links gets a link to it and
// links_confirmed = 1 (today's behaviour: an instance feeds the only server). With two or more
// enabled servers nothing is stored: linking every pair would enable raw-path and name matching
// against a server on another host as soon as a person confirmed without editing, so the links
// stay unconfirmed (the fail-closed state) until a person saves them.
//
// Like the profile upgrade, the marker is only recorded when there are instances and an enabled
// server to decide on (see the package comment above): a restored older backup, whose instances
// come without links, is upgraded when it is opened.
func (d *DB) upgradeArrLinks(ctx context.Context) error {
	return d.write(ctx, func(tx *sql.Tx) error {
		var done string
		switch err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, arrLinksMarker).Scan(&done); {
		case err == nil:
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return wrap(err, "upgrade *arr links: read marker")
		}
		var instances int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM arr_instances`).Scan(&instances); err != nil {
			return wrap(err, "upgrade *arr links: count instances")
		}
		// The Plex kinds are a literal on purpose (not models.SupportedMediaServerKinds): this is the
		// frozen one-time D11 upgrade, and whether a later kind takes part in it is decided with that
		// kind, never as a side effect of supporting it.
		servers, err := queryAll(ctx, tx, func(s scanner) (int64, error) {
			var id int64
			err := s.Scan(&id)
			return id, err
		}, `SELECT id FROM media_servers WHERE enabled = 1 AND kind IN (?, '') ORDER BY id`, models.MediaServerPlex)
		if err != nil {
			return wrap(err, "upgrade *arr links: list media servers")
		}
		if instances == 0 || len(servers) == 0 {
			return nil // nothing to decide yet (see the package comment above)
		}
		linked := int64(0)
		if len(servers) == 1 {
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO arr_server_links (arr_id, server_id)
				SELECT a.id, ? FROM arr_instances a
				WHERE a.links_confirmed = 0 AND NOT EXISTS (SELECT 1 FROM arr_server_links l WHERE l.arr_id = a.id)`, servers[0])
			if err != nil {
				return wrap(err, "upgrade *arr links: link instances")
			}
			if linked, err = res.RowsAffected(); err != nil {
				return wrap(err, "upgrade *arr links: link instances")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE arr_instances SET links_confirmed = 1
				WHERE links_confirmed = 0 AND id IN (SELECT arr_id FROM arr_server_links WHERE server_id = ?)`, servers[0]); err != nil {
				return wrap(err, "upgrade *arr links: confirm links")
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)`,
			arrLinksMarker, fmtTime(nowUTC())); err != nil {
			return wrap(err, "upgrade *arr links: record marker")
		}
		switch {
		case linked > 0:
			d.log.Info("Linked the *arr instances to the only media server", "instances", linked)
		case len(servers) > 1:
			d.log.Info("Several media servers are enabled: confirm which Plex servers each *arr instance feeds (Settings → Applications)")
		}
		return nil
	})
}

// upgradeDiscProfiles adds the values full-disc support introduced to the stored profiles, once:
// "disc" to every non-empty source order (right after "remux", ranking a full disc like the new
// default order) and "m2ts" to every non-empty container order (after the common containers). An
// empty order means the default, which has both already.
func (d *DB) upgradeDiscProfiles(ctx context.Context) error {
	return d.write(ctx, func(tx *sql.Tx) error {
		var done string
		switch err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, discProfilesMarker).Scan(&done); {
		case err == nil:
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return wrap(err, "upgrade profiles: read marker")
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, name, criteria FROM profiles ORDER BY id`)
		if err != nil {
			return wrap(err, "upgrade profiles: list")
		}
		type row struct {
			id       int64
			name     string
			criteria string
		}
		var all []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.name, &r.criteria); err != nil {
				_ = rows.Close()
				return wrap(err, "upgrade profiles: read")
			}
			all = append(all, r)
		}
		if err := rows.Close(); err != nil {
			return wrap(err, "upgrade profiles: read")
		}
		if err := rows.Err(); err != nil {
			return wrap(err, "upgrade profiles: read")
		}
		if len(all) == 0 {
			return nil // nothing to upgrade yet (see the package comment above)
		}
		upgraded := 0
		for _, r := range all {
			var criteria []models.Criterion
			if err := fromJSON(r.criteria, &criteria); err != nil {
				// A broken row is left alone (the profile repository reports it); the upgrade of
				// the other profiles must not depend on it.
				d.log.Warn("A decision profile could not be upgraded for full-disc support", "profile", r.name, "error", err)
				continue
			}
			changed := false
			for i := range criteria {
				var ch bool
				switch criteria[i].Type {
				case models.CritSource:
					criteria[i].Order, ch = withDiscSource(criteria[i].Order)
				case models.CritContainer:
					criteria[i].Order, ch = withM2TSContainer(criteria[i].Order)
				}
				changed = changed || ch
			}
			if !changed {
				continue
			}
			doc, err := toJSON(criteria)
			if err != nil {
				return fmt.Errorf("upgrade profile %q: %w", r.name, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE profiles SET criteria = ?, updated_at = ? WHERE id = ?`,
				doc, fmtTime(nowUTC()), r.id); err != nil {
				return wrap(err, "upgrade profile %q", r.name)
			}
			upgraded++
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)`,
			discProfilesMarker, fmtTime(nowUTC())); err != nil {
			return wrap(err, "upgrade profiles: record marker")
		}
		if upgraded > 0 {
			d.log.Info("Upgraded decision profiles for full-disc backups (source \"disc\" after remux, container \"m2ts\")",
				"profiles", upgraded)
		}
		return nil
	})
}

// withDiscSource returns a source order with "disc" added (changed=true) when it is non-empty and
// lacks it: right after "remux", else before "bluray", else before "unknown", else at the end.
func withDiscSource(order []string) ([]string, bool) {
	if len(order) == 0 || orderHas(order, models.SourceDisc) {
		return order, false
	}
	if i := orderIndex(order, models.SourceRemux); i >= 0 {
		return slices.Insert(slices.Clone(order), i+1, models.SourceDisc), true
	}
	for _, before := range []string{models.SourceBluray, models.SourceUnknown} {
		if i := orderIndex(order, before); i >= 0 {
			return slices.Insert(slices.Clone(order), i, models.SourceDisc), true
		}
	}
	return append(slices.Clone(order), models.SourceDisc), true
}

// withM2TSContainer returns a container order with "m2ts" added (changed=true) when it is
// non-empty and lacks it: after the last of mkv, mp4 and m4v, else before "other", else at the end.
func withM2TSContainer(order []string) ([]string, bool) {
	const m2ts = "m2ts"
	if len(order) == 0 || orderHas(order, m2ts) {
		return order, false
	}
	last := -1
	for _, c := range []string{"mkv", "mp4", "m4v"} {
		last = max(last, orderIndex(order, c))
	}
	if last >= 0 {
		return slices.Insert(slices.Clone(order), last+1, m2ts), true
	}
	if i := orderIndex(order, "other"); i >= 0 {
		return slices.Insert(slices.Clone(order), i, m2ts), true
	}
	return append(slices.Clone(order), m2ts), true
}

// orderIndex returns the index of value in order (case-insensitive, trimmed), -1 when absent.
func orderIndex(order []string, value string) int {
	return slices.IndexFunc(order, func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), value) })
}

func orderHas(order []string, value string) bool { return orderIndex(order, value) >= 0 }
