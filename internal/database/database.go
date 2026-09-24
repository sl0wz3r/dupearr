// Package database implements store.Store on SQLite (modernc.org/sqlite, pure Go, no CGO) with
// embedded, ordered migrations.
//
// Tables: settings(key TEXT PK, value TEXT), users, media_servers, libraries, arr_instances,
// path_mappings, profiles(criteria/protections JSON), duplicate_groups, group_files(version JSON,
// decision columns), actions, history, exclusions, notifications, scan_runs, commands, tasks,
// schema_migrations. Indexes on duplicate_groups(key UNIQUE, status, last_scan_id),
// group_files(group_id), actions(status, group_id), history(created_at, event_type).
//
// # Concurrency model
//
// SQLite allows one writer at a time. Instead of letting concurrent API handlers, the scanner and
// the executor race for the write lock (and fail with SQLITE_BUSY once busy_timeout expires), the
// DB keeps two connection pools on the same file:
//
//   - a write pool limited to exactly one connection. Every INSERT/UPDATE/DELETE and every write
//     transaction goes through it, so writers queue inside database/sql (honouring their context)
//     rather than inside SQLite. Transactions start with BEGIN IMMEDIATE so a write lock is taken
//     up front and can never fail to upgrade mid-transaction.
//   - a read pool of several query_only connections. In WAL mode readers never block the writer
//     and never block each other, so the UI stays responsive during long scans.
//
// busy_timeout (10 s) remains as a safety net for the rare cases SQLite still needs a lock
// between the pools (WAL checkpoints, recovery) or when an external process holds the file.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite" // also registers the "sqlite" database/sql driver

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// settingsKey is the settings-table key holding the models.Settings JSON document.
const settingsKey = "settings"

// DB is the SQLite implementation of store.Store. It is safe for concurrent use.
type DB struct {
	w    *sql.DB // single-connection write pool (BEGIN IMMEDIATE)
	r    *sql.DB // query_only read pool
	path string
	log  *slog.Logger

	closeOnce sync.Once
	closeErr  error
}

var _ store.Store = (*DB)(nil)

// Open opens (creating if needed) the database at path with WAL, foreign_keys=ON and a
// busy_timeout, then applies pending migrations and data upgrades (upgrade.go).
func Open(ctx context.Context, path string, log *slog.Logger) (*DB, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("open database: empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("open database: resolve path %q: %w", path, err)
	}
	if err := ensureFile(abs); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	d := &DB{path: abs, log: log}

	d.w, err = sql.Open(driverName, dsn(abs, false))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", abs, err)
	}
	// One writer connection; keep it open forever (idle writers are cheap and re-opening would
	// re-run the connection pragmas).
	d.w.SetMaxOpenConns(1)
	d.w.SetMaxIdleConns(1)
	d.w.SetConnMaxLifetime(0)
	d.w.SetConnMaxIdleTime(0)
	if err := pingRetryBusy(ctx, d.w); err != nil {
		_ = d.w.Close()
		return nil, fmt.Errorf("open database %s: %w", abs, err)
	}
	if err := d.migrate(ctx); err != nil {
		_ = d.w.Close()
		return nil, err
	}
	if err := d.upgradeData(ctx); err != nil {
		_ = d.w.Close()
		return nil, err
	}

	// The read pool is opened after migrations so every reader sees the final schema.
	d.r, err = sql.Open(driverName, dsn(abs, true))
	if err != nil {
		_ = d.w.Close()
		return nil, fmt.Errorf("open database %s (read pool): %w", abs, err)
	}
	readers := max(4, runtime.NumCPU())
	d.r.SetMaxOpenConns(readers)
	d.r.SetMaxIdleConns(readers)
	if err := pingRetryBusy(ctx, d.r); err != nil {
		_ = d.r.Close()
		_ = d.w.Close()
		return nil, fmt.Errorf("open database %s (read pool): %w", abs, err)
	}
	return d, nil
}

// openBusyRetry bounds how long Open retries a connection whose setup reports SQLITE_BUSY.
const openBusyRetry = 10 * time.Second

// pingRetryBusy opens a first connection of db, retrying while SQLite reports the file busy.
// The DSN pragmas run while the connection is being set up, and switching a brand-new database
// to WAL mode fails with SQLITE_BUSY at once — without waiting for busy_timeout — when another
// process (e.g. `dupearr reset-auth` during a first start) is doing the same at that moment.
func pingRetryBusy(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(openBusyRetry)
	delay := 5 * time.Millisecond
	for {
		err := db.PingContext(ctx)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		delay = min(2*delay, 250*time.Millisecond)
	}
}

// isBusy reports whether err is SQLITE_BUSY (any extended code).
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqliteBusy
}

// ensureFile creates the database file with owner-only permissions when it does not exist yet:
// it holds API keys and tokens. SQLite gives the -wal/-shm files the same mode.
func ensureFile(path string) error {
	dir := filepath.Dir(path)
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("database directory %s: %w", dir, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("database directory %s is not a directory", dir)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case err == nil:
		return f.Close()
	case errors.Is(err, fs.ErrExist):
		return nil
	default:
		return fmt.Errorf("create database file %s: %w", path, err)
	}
}

// dsn builds a modernc.org/sqlite DSN for path. The path is passed as a file: URI so that
// characters such as '?', '#' or '%' in directory names cannot be mistaken for parameters.
// modernc applies busy_timeout before the other pragmas, so they wait for locks held by other
// connections; the one exception (a brand-new file being switched to WAL by two processes at
// once fails immediately) is retried by pingRetryBusy in Open.
func dsn(path string, readOnly bool) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows volume paths: file:///C:/...
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Set("_query_only", "1")
	} else {
		q.Set("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	return u.String()
}

// Seed inserts ProfileTemplates when no profile exists and default Settings when missing.
//
// It also repairs a database that has profiles but no default by promoting the oldest profile,
// so store.ProfileRepo.GetDefault always succeeds once any profile exists. Seed is idempotent.
func (d *DB) Seed(ctx context.Context, templates []models.Profile) error {
	return d.write(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles`).Scan(&count); err != nil {
			return wrap(err, "seed: count profiles")
		}
		if count == 0 && len(templates) > 0 {
			defaultIdx := 0
			for i, t := range templates {
				if t.IsDefault {
					defaultIdx = i
					break
				}
			}
			for i := range templates {
				p := templates[i] // copy: never mutate the caller's templates
				p.ID = 0
				p.IsDefault = i == defaultIdx
				if err := insertProfile(ctx, tx, &p); err != nil {
					return fmt.Errorf("seed profile %q: %w", p.Name, err)
				}
			}
			// The templates are this build's: the data upgrades for profiles of older builds
			// (upgrade.go) must never run on them later, or a user's edit made before the next
			// start would be undone. (A backup restore never seeds: its profiles stay upgradable.)
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`,
				discProfilesMarker, fmtTime(nowUTC())); err != nil {
				return wrap(err, "seed: record the profile upgrades")
			}
			d.log.Info("Seeded decision profiles", "count", len(templates), "default", templates[defaultIdx].Name)
		} else if count > 0 {
			var defaults int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles WHERE is_default = 1`).Scan(&defaults); err != nil {
				return wrap(err, "seed: count default profiles")
			}
			if defaults == 0 {
				if _, err := tx.ExecContext(ctx,
					`UPDATE profiles SET is_default = 1 WHERE id = (SELECT MIN(id) FROM profiles)`); err != nil {
					return wrap(err, "seed: promote default profile")
				}
				d.log.Warn("No default decision profile found; promoted the oldest profile")
			}
		}

		doc, err := toJSON(normalizeSettings(models.DefaultSettings()))
		if err != nil {
			return fmt.Errorf("seed settings: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`, settingsKey, doc); err != nil {
			return wrap(err, "seed settings")
		}
		return nil
	})
}

// Settings implements store.Store.
func (d *DB) Settings() store.SettingsRepo { return settingsRepo{d} }

// Users implements store.Store.
func (d *DB) Users() store.UserRepo { return userRepo{d} }

// MediaServers implements store.Store.
func (d *DB) MediaServers() store.MediaServerRepo { return mediaServerRepo{d} }

// Libraries implements store.Store.
func (d *DB) Libraries() store.LibraryRepo { return libraryRepo{d} }

// ArrInstances implements store.Store.
func (d *DB) ArrInstances() store.ArrInstanceRepo { return arrInstanceRepo{d} }

// PathMappings implements store.Store.
func (d *DB) PathMappings() store.PathMappingRepo { return pathMappingRepo{d} }

// Profiles implements store.Store.
func (d *DB) Profiles() store.ProfileRepo { return profileRepo{d} }

// Groups implements store.Store.
func (d *DB) Groups() store.GroupRepo { return groupRepo{d} }

// Actions implements store.Store.
func (d *DB) Actions() store.ActionRepo { return actionRepo{d} }

// History implements store.Store.
func (d *DB) History() store.HistoryRepo { return historyRepo{d} }

// Exclusions implements store.Store.
func (d *DB) Exclusions() store.ExclusionRepo { return exclusionRepo{d} }

// Notifications implements store.Store.
func (d *DB) Notifications() store.NotificationRepo { return notificationRepo{d} }

// ScanRuns implements store.Store.
func (d *DB) ScanRuns() store.ScanRunRepo { return scanRunRepo{d} }

// Commands implements store.Store.
func (d *DB) Commands() store.CommandRepo { return commandRepo{d} }

// Tasks implements store.Store.
func (d *DB) Tasks() store.TaskRepo { return taskRepo{d} }

// Ping implements store.Store. It runs a query that reads the schema, which proves the file is
// readable, not merely that a connection could be opened.
func (d *DB) Ping(ctx context.Context) error {
	var n int
	if err := d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&n); err != nil {
		return wrap(err, "ping database")
	}
	return nil
}

// Vacuum implements store.Store. It rebuilds the file and then truncates the WAL.
func (d *DB) Vacuum(ctx context.Context) error {
	if _, err := d.w.ExecContext(ctx, `VACUUM`); err != nil {
		return wrap(err, "vacuum database")
	}
	// Best effort: a concurrent reader can keep the WAL from being truncated; that is harmless.
	if _, err := d.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		d.log.Debug("WAL checkpoint after vacuum failed", "error", err)
	}
	return nil
}

// Path implements store.Store (absolute path of the database file).
func (d *DB) Path() string { return d.path }

// BackupTo implements store.Store (VACUUM INTO dst). dst must not exist; it is created with
// owner-only permissions and removed again if the snapshot fails.
func (d *DB) BackupTo(ctx context.Context, dst string) error {
	if strings.TrimSpace(dst) == "" {
		return errors.New("backup database: empty destination path")
	}
	abs, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("backup database: resolve %q: %w", dst, err)
	}
	// O_EXCL both enforces "must not exist" atomically and sets the file mode; VACUUM INTO
	// accepts an existing empty file.
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup database to %s: %w", abs, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(abs)
		return fmt.Errorf("backup database to %s: %w", abs, err)
	}
	if err := d.vacuumInto(ctx, abs); err != nil {
		_ = os.Remove(abs)
		return wrap(err, "backup database to %s", abs)
	}
	return nil
}

// vacuumInto runs VACUUM INTO on a short-lived dedicated connection. VACUUM INTO only reads the
// source inside one read transaction, so the snapshot is consistent and, in WAL mode, the writer
// is not blocked while the copy is written. It cannot use the read pool: SQLite refuses VACUUM
// INTO on query_only connections.
func (d *DB) vacuumInto(ctx context.Context, dst string) error {
	// Fails once the DB is closed, so a closed store cannot be backed up behind its back.
	if err := d.r.PingContext(ctx); err != nil {
		return err
	}
	db, err := sql.Open(driverName, dsn(d.path, false))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// Close implements store.Store. It is safe to call more than once.
func (d *DB) Close() error {
	d.closeOnce.Do(func() {
		var errs []error
		if d.r != nil {
			errs = append(errs, d.r.Close())
		}
		if d.w != nil {
			// Let SQLite refresh planner statistics it found useful during this session. Bounded so
			// a writer stuck in a long transaction cannot hold up shutdown.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := d.w.ExecContext(ctx, `PRAGMA optimize`)
			cancel()
			if err != nil {
				d.log.Debug("PRAGMA optimize on close failed", "error", err)
			}
			errs = append(errs, d.w.Close())
		}
		if err := errors.Join(errs...); err != nil {
			d.closeErr = fmt.Errorf("close database: %w", err)
		}
	})
	return d.closeErr
}

// write runs fn in a write transaction (BEGIN IMMEDIATE on the single writer connection).
// fn must use tx for every statement: the writer pool has exactly one connection, so any
// statement on d.w inside fn would wait for itself.
//
// The transaction is always finished, even when fn panics: a leaked transaction would pin the
// only writer connection and hang every later write of the process.
func (d *DB) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write transaction: %w", err)
	}
	// After a successful Commit this is a harmless no-op (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// read runs fn in a read-only transaction on the read pool, giving it one consistent snapshot.
func (d *DB) read(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin read transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}
