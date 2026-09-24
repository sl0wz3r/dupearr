package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration is one embedded schema change, named NNNN_description.sql.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations returns the embedded migrations sorted by version. Versions must be unique and
// positive; file names must start with digits followed by '_'.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	seen := map[int]string{}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: name must be NNNN_description.sql", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: invalid version prefix %q", e.Name(), prefix)
		}
		if other, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, e.Name(), v)
		}
		seen[v] = e.Name()
		body, err := fs.ReadFile(fsys, path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: strings.TrimSuffix(e.Name(), ".sql"), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// migrate applies every embedded migration that is not recorded in schema_migrations, each in
// its own transaction together with its schema_migrations row, so a failed migration leaves the
// database at the previous version. Re-opening an up-to-date database is a no-op.
//
// A database that already carries a migration this build does not know (it was written by a
// newer Dupearr) is refused rather than used with a schema the code does not understand.
func (d *DB) migrate(ctx context.Context) error {
	migrations, err := loadMigrations(migrationsFS)
	if err != nil {
		return err
	}
	if _, err := d.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY NOT NULL,
		name       TEXT    NOT NULL,
		applied_at TEXT    NOT NULL
	)`); err != nil {
		return wrap(err, "create schema_migrations")
	}

	applied := map[int]bool{}
	rows, err := d.w.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return wrap(err, "read schema_migrations")
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return wrap(err, "read schema_migrations")
		}
		applied[v] = true
	}
	if err := rows.Close(); err != nil {
		return wrap(err, "read schema_migrations")
	}
	if err := rows.Err(); err != nil {
		return wrap(err, "read schema_migrations")
	}

	known := map[int]bool{}
	latest := 0
	for _, m := range migrations {
		known[m.version] = true
		latest = max(latest, m.version)
	}
	for v := range applied {
		if !known[v] {
			return fmt.Errorf("database schema version %d is newer than this build of Dupearr supports (latest %d); upgrade Dupearr or restore a backup", v, latest)
		}
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		didApply, err := d.applyMigration(ctx, m)
		if err != nil {
			return err
		}
		if didApply {
			d.log.Info("Applied database migration", "migration", m.name)
		}
	}
	return nil
}

// applyMigration applies m and records it in schema_migrations, in one write transaction. It
// re-checks schema_migrations under the write lock first, so a migration that another process
// opening the same file (e.g. `dupearr reset-auth` racing the server at startup) applied after
// migrate read the list is skipped (applied=false) instead of failing on "table already exists".
func (d *DB) applyMigration(ctx context.Context, m migration) (applied bool, err error) {
	err = d.write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`,
			m.version).Scan(&n); err != nil {
			return wrap(err, "check migration %s", m.name)
		}
		if n > 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return wrap(err, "apply migration %s", m.name)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, fmtTime(nowUTC())); err != nil {
			return wrap(err, "record migration %s", m.name)
		}
		applied = true
		return nil
	})
	return applied && err == nil, err
}
