package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// A restored database is untrusted input: an archive can be modified before it is uploaded. Its
// schema (sqlite_master) is never adopted. Instead, its rows are copied into a fresh database
// created with this build's migrations (rebuild, Service.rebuildInto), so triggers, views, extra tables and
// indexes and altered table definitions can neither run while the restore is prepared nor after
// it. Triggers and views are refused outright: Dupearr never creates any, so they can only be the
// sign of a tampered archive.

const (
	// sessionKeySetting is the settings entry holding the session signing key
	// (internal/auth sessionKeySetting). It is never restored nor written into a backup: whoever
	// holds it (and the password hash next to it) could forge session cookies. A missing key is
	// regenerated at startup, which signs every session out.
	sessionKeySetting = "auth.sessionKey"
	// webhookTokenSetting is the settings entry holding the webhook token (internal/auth
	// webhookTokenSetting). It is a credential like the API key: a restore keeps the running
	// instance's unless the security settings are restored (RestoreOptions), so an old or planted
	// token never comes back silently.
	webhookTokenSetting = "auth.webhookToken"
	// userIDSetting records the id of the single user (internal/auth userIDSetting); it is taken
	// from wherever the users are taken from.
	userIDSetting = "auth.userId"
	// settingsDocKey is the settings entry holding the models.Settings JSON document
	// (internal/database settingsKey).
	settingsDocKey = "settings"

	stagedSchema = "staged"
	liveSchema   = "live"

	// untrustedParams are the SQLite settings of every connection that reads an untrusted
	// database: no functions or virtual tables with side effects may run from its schema
	// (trusted_schema), corrupt cells are detected (cell_size_check) and the schema can never be
	// written directly (defensive mode).
	untrustedParams = "_defensive=1&_pragma=trusted_schema(0)&_pragma=cell_size_check(1)"
)

// upgradableMigrations are the schema migrations whose effect on a database written before them
// is reproduced by rebuild copying its rows into this build's schema: they only add
// tables, indexes or columns with a default. A backup that lacks any other migration this build
// knows is refused (its rows would need transforming). TestUpgradableMigrationsCoverSchema fails
// when a migration is added, so this is decided for each new one.
var upgradableMigrations = map[int64]bool{
	2: true, // 0002_retained_overrides: ADD COLUMN duplicate_groups.retained_overrides DEFAULT '{}'
}

// createReference creates path as an empty database with this build's schema.
func createReference(ctx context.Context, path string) error {
	db, err := database.Open(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

// schemaObject is one row of sqlite_master.
type schemaObject struct{ typ, name, sql string }

func (o schemaObject) key() string { return o.typ + "\x00" + strings.ToLower(o.name) }

// readObjects returns the schema objects of the attached database schema (main, staged, live).
func readObjects(ctx context.Context, q queryer, schema string) ([]schemaObject, error) {
	rows, err := q.QueryContext(ctx, `SELECT type, name, coalesce(sql, '') FROM `+schema+`.sqlite_master ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.typ, &o.name, &o.sql); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// foreignObjects returns the triggers and views of objs that ref (this build's schema) does not
// define identically.
func foreignObjects(ref, objs []schemaObject) []schemaObject {
	known := map[string]string{}
	for _, o := range ref {
		known[o.key()] = o.sql
	}
	var out []schemaObject
	for _, o := range objs {
		if o.typ != "trigger" && o.typ != "view" {
			continue
		}
		if sqlText, ok := known[o.key()]; !ok || sqlText != o.sql {
			out = append(out, o)
		}
	}
	return out
}

// quoteIdent quotes an SQL identifier.
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// shortName makes an object name from an untrusted schema safe to show.
func shortName(s string) string {
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60]) + "…"
	}
	return fmt.Sprintf("%q", s)
}

// userRow is one row of the users table.
type userRow struct{ username, hash string }

// rebuild copies an untrusted backup database into a fresh database with this build's schema.
type rebuild struct {
	db      *sql.DB
	conn    *sql.Conn
	hasLive bool
	tables  []string            // this build's tables in creation order (schema_migrations excluded)
	staged  map[string]string   // lower-case table name → name, the backup's plain tables
	columns map[string][]string // table → this build's columns
}

// openRebuild opens out (created by createReference) with untrusted attached read-only as
// "staged" and, when live is not empty, the running instance's database attached read-only as
// "live". It refuses a backup whose schema holds triggers or views, or whose tables Dupearr
// copies are not plain tables.
func openRebuild(ctx context.Context, out, untrusted, live string) (_ *rebuild, err error) {
	db, err := sql.Open("sqlite", sqliteURI(out, "mode=rw&"+untrustedParams))
	if err != nil {
		return nil, err
	}
	r := &rebuild{db: db, staged: map[string]string{}, columns: map[string][]string{}}
	defer func() {
		if err != nil {
			_ = r.close()
		}
	}()
	db.SetMaxOpenConns(1)
	if r.conn, err = db.Conn(ctx); err != nil {
		return nil, err
	}
	var mode string
	if err := r.conn.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return nil, fmt.Errorf("journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return nil, fmt.Errorf("journal mode is %q", mode)
	}
	if _, err := r.conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return nil, err
	}
	if _, err := r.conn.ExecContext(ctx, `ATTACH DATABASE ? AS `+stagedSchema, untrustedURI(untrusted)); err != nil {
		return nil, invalidDB("cannot open: %v", err)
	}
	if live != "" {
		if _, err := r.conn.ExecContext(ctx, `ATTACH DATABASE ? AS `+liveSchema, sqliteURI(live, "mode=ro")); err != nil {
			return nil, fmt.Errorf("open the current database: %w", err)
		}
		r.hasLive = true
	}

	ref, err := readObjects(ctx, r.conn, "main")
	if err != nil {
		return nil, err
	}
	objs, err := readObjects(ctx, r.conn, stagedSchema)
	if err != nil {
		return nil, invalidDB("cannot read the schema: %v", err)
	}
	if bad := foreignObjects(ref, objs); len(bad) > 0 {
		return nil, invalidDB("it contains a %s named %s that Dupearr does not create; the backup was modified "+
			"and cannot be restored", bad[0].typ, shortName(bad[0].name))
	}
	for _, o := range ref {
		if o.typ == "table" && !strings.HasPrefix(o.name, "sqlite_") && o.name != "schema_migrations" {
			r.tables = append(r.tables, o.name)
		}
	}
	for _, o := range objs {
		if o.typ != "table" {
			continue
		}
		lower := strings.ToLower(o.name)
		if !isRefTable(r.tables, lower) && lower != "sqlite_sequence" {
			continue // not copied: extra tables do not survive the restore
		}
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(o.sql)), "CREATE TABLE") {
			return nil, invalidDB("table %s is not a plain table; the backup was modified and cannot be restored", shortName(o.name))
		}
		r.staged[lower] = o.name
	}
	for _, t := range r.tables {
		if r.columns[t], err = tableColumns(ctx, r.conn, "main", t); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func isRefTable(tables []string, lower string) bool {
	for _, t := range tables {
		if strings.ToLower(t) == lower {
			return true
		}
	}
	return false
}

// untrustedURI opens an untrusted database file read-only, without side files.
func untrustedURI(path string) string { return readOnlyDSN(path) }

// invalidDB reports a restored database that cannot be used.
func invalidDB(format string, a ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidBackup, dbFileName, fmt.Sprintf(format, a...))
}

// tableColumns returns the (visible) columns of table in schema.
func tableColumns(ctx context.Context, q queryer, schema, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?, ?)`, table, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// users returns the users of schema (staged or live) in id order.
func (r *rebuild) users(ctx context.Context, schema string) ([]userRow, error) {
	if schema == stagedSchema && r.staged["users"] == "" {
		return nil, nil
	}
	rows, err := r.conn.QueryContext(ctx, `SELECT CAST(username AS TEXT), CAST(password_hash AS TEXT) FROM `+schema+`.users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []userRow
	for rows.Next() {
		var u userRow
		var name, hash sql.NullString
		if err := rows.Scan(&name, &hash); err != nil {
			return nil, err
		}
		u.username, u.hash = name.String, hash.String
		out = append(out, u)
	}
	return out, rows.Err()
}

// settingsDoc returns the settings document of schema (staged or live) decoded over the defaults
// (like database's Settings().Get). ok is false when the database holds none.
func (r *rebuild) settingsDoc(ctx context.Context, schema string) (s models.Settings, ok bool, err error) {
	s = models.DefaultSettings()
	var doc sql.NullString
	err = r.conn.QueryRowContext(ctx, `SELECT CAST(value AS TEXT) FROM `+schema+`.settings WHERE key = ?`, settingsDocKey).Scan(&doc)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s, false, nil
	case err != nil:
		return s, false, err
	}
	if doc.String == "" {
		return s, true, nil
	}
	if err := json.Unmarshal([]byte(doc.String), &s); err != nil {
		return s, false, fmt.Errorf("the settings document is invalid: %v", err)
	}
	return s, true, nil
}

// settingValue returns the internal settings entry key of schema (staged or live); ok is false
// when there is none.
func (r *rebuild) settingValue(ctx context.Context, schema, key string) (v string, ok bool, err error) {
	var s sql.NullString
	err = r.conn.QueryRowContext(ctx, `SELECT CAST(value AS TEXT) FROM `+schema+`.settings WHERE key = ?`, key).Scan(&s)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return s.String, true, nil
}

// copyRows copies every table of this build's schema from the backup into out, column by column
// (columns the backup lacks get their defaults, so an older backup is upgraded the way
// upgradableMigrations do), in one transaction. The users come from the running instance unless
// restoreUsers is set, and so do the settings entries liveKeys (the backup's are dropped; they
// are left out when the running instance has none). The session signing key is dropped (a new
// one is generated at startup).
func (r *rebuild) copyRows(ctx context.Context, restoreUsers bool, liveKeys []string) (err error) {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, t := range r.tables {
		src := stagedSchema
		if strings.EqualFold(t, "users") && !restoreUsers {
			if !r.hasLive {
				return errors.New("the current user cannot be kept: no current database")
			}
			src = liveSchema
		} else if r.staged[strings.ToLower(t)] == "" {
			continue // the backup predates this table
		}
		srcCols, err := tableColumns(ctx, tx, src, t)
		if err != nil {
			return fmt.Errorf("table %s: %w", t, err)
		}
		var cols []string
		for _, c := range r.columns[t] {
			for _, sc := range srcCols {
				if strings.EqualFold(c, sc) {
					cols = append(cols, quoteIdent(c))
					break
				}
			}
		}
		if len(cols) == 0 {
			continue
		}
		list := strings.Join(cols, ", ")
		q := `INSERT INTO main.` + quoteIdent(t) + ` (` + list + `) SELECT ` + list + ` FROM ` + src + `.` + quoteIdent(t)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			if src == liveSchema {
				return fmt.Errorf("keep the current user: %w", err)
			}
			return invalidDB("table %s cannot be restored: %v", t, err)
		}
	}
	if err := r.copySequences(ctx, tx, restoreUsers); err != nil {
		return err
	}
	for _, key := range liveKeys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM main.settings WHERE key = ?`, key); err != nil {
			return err
		}
		if !r.hasLive {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO main.settings (key, value)
			SELECT key, value FROM `+liveSchema+`.settings WHERE key = ?`, key); err != nil {
			return fmt.Errorf("keep the current %s: %w", key, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM main.settings WHERE key = ?`, sessionKeySetting); err != nil {
		return err
	}
	return tx.Commit()
}

// copySequences carries the backup's AUTOINCREMENT counters over, so ids of deleted rows are not
// handed out again.
func (r *rebuild) copySequences(ctx context.Context, tx *sql.Tx, restoreUsers bool) error {
	if r.staged["sqlite_sequence"] == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT CAST(name AS TEXT), seq FROM `+stagedSchema+`.sqlite_sequence
		WHERE typeof(seq) = 'integer' AND seq > 0`)
	if err != nil {
		return invalidDB("cannot read sqlite_sequence: %v", err)
	}
	type seq struct {
		name string
		n    int64
	}
	var seqs []seq
	for rows.Next() {
		var s seq
		var name sql.NullString
		if err := rows.Scan(&name, &s.n); err != nil {
			_ = rows.Close()
			return invalidDB("cannot read sqlite_sequence: %v", err)
		}
		s.name = name.String
		seqs = append(seqs, s)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return invalidDB("cannot read sqlite_sequence: %v", err)
	}
	for _, s := range seqs {
		var table string
		for _, t := range r.tables {
			if strings.EqualFold(t, s.name) {
				table = t
			}
		}
		if table == "" || (table == "users" && !restoreUsers) {
			continue
		}
		res, err := tx.ExecContext(ctx, `UPDATE main.sqlite_sequence SET seq = max(seq, ?) WHERE name = ?`, s.n, table)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO main.sqlite_sequence (name, seq) VALUES (?, ?)`, table, s.n); err != nil {
				return err
			}
		}
	}
	return nil
}

// finish detaches the source databases, checks the copy's foreign keys and closes it.
func (r *rebuild) finish(ctx context.Context) error {
	for _, schema := range []string{stagedSchema, liveSchema} {
		if schema == liveSchema && !r.hasLive {
			continue
		}
		if _, err := r.conn.ExecContext(ctx, `DETACH DATABASE `+schema); err != nil {
			return err
		}
	}
	r.hasLive = false
	rows, err := r.conn.QueryContext(ctx, `PRAGMA main.foreign_key_check`)
	if err != nil {
		return err
	}
	var table, parent string
	found := false
	if rows.Next() {
		var rowid sql.NullInt64
		var fkid int64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			_ = rows.Close()
			return err
		}
		found = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if found {
		return invalidDB("it is inconsistent: rows of %s refer to missing %s", table, parent)
	}
	return r.close()
}

func (r *rebuild) close() error {
	var errs []error
	if r.conn != nil {
		errs = append(errs, r.conn.Close())
		r.conn = nil
	}
	if r.db != nil {
		errs = append(errs, r.db.Close())
		r.db = nil
	}
	return errors.Join(errs...)
}

// RemoveForeignSchemaObjects drops the triggers and views of the database at dbPath that this
// build's schema does not define (Dupearr creates none). They can only come from tampering, for
// example a backup restored by an earlier version, which adopted the archive's schema: a trigger
// runs hidden logic on every write (re-creating a deleted user, re-queuing removals), which the
// application cannot see. It returns a description of each object removed. A database that does
// not exist yet is left alone. tmpParent hosts a temporary reference database.
//
// It must run before the database is opened for use (at startup and by `dupearr reset-auth`).
func RemoveForeignSchemaObjects(ctx context.Context, tmpParent, dbPath string) ([]string, error) {
	if _, err := os.Lstat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(tmpParent, restoreTmpPrefix+"*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	refPath := filepath.Join(tmp, dbFileName)
	if err := createReference(ctx, refPath); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", sqliteURI(dbPath, "mode=rw&_pragma=busy_timeout(10000)&_pragma=trusted_schema(0)"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS reference`, sqliteURI(refPath, "mode=ro")); err != nil {
		return nil, err
	}
	ref, err := readObjects(ctx, conn, "reference")
	if err != nil {
		return nil, err
	}
	objs, err := readObjects(ctx, conn, "main")
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `DETACH DATABASE reference`); err != nil {
		return nil, err
	}
	var removed []string
	for _, o := range foreignObjects(ref, objs) {
		stmt := `DROP TRIGGER IF EXISTS main.`
		if o.typ == "view" {
			stmt = `DROP VIEW IF EXISTS main.`
		}
		if _, err := conn.ExecContext(ctx, stmt+quoteIdent(o.name)); err != nil {
			return removed, fmt.Errorf("remove %s %s: %w", o.typ, shortName(o.name), err)
		}
		removed = append(removed, o.typ+" "+shortName(o.name))
	}
	return removed, nil
}
