package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // "sqlite" database/sql driver (verification of restored databases)

	"github.com/sl0wz3r/dupearr/internal/models"
)

const (
	// applyingMarker is written into the staged restore when ApplyPendingRestore starts swapping
	// files; its content is the timestamp used for the .bak names, so an interrupted apply resumes
	// with the same names.
	applyingMarker = "APPLYING"
	// unconfirmedMarker marks a restore staged by StageRestore/StageRestoreUpload that
	// ConfirmRestore has not confirmed yet: ApplyPendingRestore discards it instead of applying it.
	unconfirmedMarker = "UNCONFIRMED"
	// securityStateFile holds, in a restore staged for review, the fingerprint of the running
	// instance's security state when it was staged (securityFingerprint): the staged files carry
	// the values of that moment for everything the restore keeps, so ConfirmRestore refuses once
	// they changed (a password or API key change between staging and confirming would otherwise
	// be reverted silently).
	securityStateFile = "SECURITY_STATE"
	// recycleBinMarkerName is the marker file of Dupearr's recycle bin (internal/executor
	// binMarkerName): a staged restore folder never contains it.
	recycleBinMarkerName = ".dupearr-recycle-bin"
	bakTimeLayout        = "20060102T150405Z"
	// keepRestoreGenerations is how many generations of files replaced by restores
	// (<name>.bak-<timestamp>) are kept: the last restore can be undone, and the one before.
	// Older ones are removed, since they hold replaced secrets (API keys, tokens, passwords).
	keepRestoreGenerations = 2
	// untrustedDBName is the database as extracted from the archive, inside the staging folder.
	untrustedDBName = "backup.db"

	restoreTmpPrefix    = ".restore-tmp-"
	restoreUploadPrefix = ".restore-upload-"
	restoreCheckPrefix  = ".restore-cfgcheck-"
	restoreOldPrefix    = RestoreDirName + ".old-"

	sqliteHeader = "SQLite format 3\x00"
)

// requiredTables must exist in a restored database (it must be a Dupearr database).
var requiredTables = []string{"schema_migrations", "settings", "commands", "duplicate_groups"}

var bakStampRe = regexp.MustCompile(`^\d{8}T\d{6}Z$`)

// bakFileRe matches the files ApplyPendingRestore keeps (<name>.bak-<stamp>[-N][-wal|-journal]);
// group 1 is the stamp.
var bakFileRe = regexp.MustCompile(`^(?:dupearr\.db|config\.xml)\.bak-(\d{8}T\d{6}Z)(?:-\d{1,2})?(?:-wal|-journal)?$`)

// ErrNoStagedRestore is returned by ConfirmRestore when no restore is waiting for confirmation.
// It matches ErrNotFound with errors.Is.
var ErrNoStagedRestore error = noStagedError{}

// ErrStaleRestore is returned by ConfirmRestore when the running instance's security settings
// (authentication, API key, user, webhook token, listener) changed after the restore was staged;
// the staged restore is discarded and must be staged again.
var ErrStaleRestore = errors.New("the security settings changed after the backup was staged for restore; stage the restore again")

type noStagedError struct{}

func (noStagedError) Error() string { return "no restore is waiting for confirmation" }
func (noStagedError) Is(target error) bool {
	return target == ErrNotFound || notFoundError{}.Is(target)
}

// Restore validates the zip and stages it to dataDir/.restore; ApplyPendingRestore (called by main
// at startup before opening the DB) swaps the files in. Caller then restarts the process.
//
// The running instance's security settings (authentication, API key, user, listener) are kept;
// see RestoreOptions. Nothing in the running data directory is modified. A previously staged
// restore is replaced.
func (s *Service) Restore(ctx context.Context, id int64) error {
	_, err := s.restoreByID(ctx, id, RestoreOptions{}, true)
	return err
}

// RestoreUpload is Restore for an uploaded zip.
//
// size is the declared upload size (≤ 0 when unknown); archives larger than MaxUploadSize are
// rejected, and so is an upload whose length differs from a declared size (truncated upload).
func (s *Service) RestoreUpload(ctx context.Context, r io.Reader, size int64) error {
	_, err := s.restoreUpload(ctx, r, size, RestoreOptions{}, true)
	return err
}

// StageRestore validates backup id and stages it like Restore, with opts, but the staged restore
// waits for ConfirmRestore: until then it is discarded, not applied, at the next start. The
// summary lists the security-relevant differences for the admin to review first.
func (s *Service) StageRestore(ctx context.Context, id int64, opts RestoreOptions) (*RestoreSummary, error) {
	return s.restoreByID(ctx, id, opts, false)
}

// StageRestoreUpload is StageRestore for an uploaded zip (see RestoreUpload for size).
func (s *Service) StageRestoreUpload(ctx context.Context, r io.Reader, size int64, opts RestoreOptions) (*RestoreSummary, error) {
	return s.restoreUpload(ctx, r, size, opts, false)
}

// ConfirmRestore confirms the restore staged by StageRestore or StageRestoreUpload, so the next
// start applies it; the caller then restarts the process. ErrNoStagedRestore when nothing is
// staged.
//
// The running instance's security state must still be what it was when the restore was staged
// (see securityStateFile); otherwise the staged restore is discarded and ErrStaleRestore returned.
func (s *Service) ConfirmRestore(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.dataDir, RestoreDirName)
	if !isRegular(filepath.Join(dir, configFileName)) || !isRegular(filepath.Join(dir, dbFileName)) ||
		!isRegular(filepath.Join(dir, unconfirmedMarker)) {
		return ErrNoStagedRestore
	}
	staged, err := readLimited(filepath.Join(dir, securityStateFile), 256)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("confirm restore: %w", err)
	}
	now, ferr := s.securityFingerprint(ctx)
	if ferr != nil {
		return fmt.Errorf("confirm restore: %w", ferr)
	}
	if err != nil || strings.TrimSpace(string(staged)) != now {
		if rerr := os.RemoveAll(dir); rerr != nil {
			s.log.Warn("Could not discard a stale staged restore", "error", rerr)
		}
		syncDir(s.dataDir)
		s.log.Warn("Staged restore discarded: the security settings changed after it was staged")
		return ErrStaleRestore
	}
	if err := os.Remove(filepath.Join(dir, unconfirmedMarker)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("confirm restore: %w", err)
	}
	syncDir(dir)
	s.log.Warn("Staged restore confirmed; it is applied at the next start")
	return nil
}

// DiscardRestore removes a staged restore, confirmed or not (a no-op when nothing is staged).
func (s *Service) DiscardRestore() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.dataDir, RestoreDirName)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("discard staged restore: %w", err)
	}
	syncDir(s.dataDir)
	s.log.Info("Staged restore discarded")
	return nil
}

func (s *Service) restoreByID(ctx context.Context, id int64, opts RestoreOptions, confirmed bool) (*RestoreSummary, error) {
	b, err := s.find(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.File(b.Type, b.Name)
	if err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("restore %s: %w: not a readable zip archive: %v", b.Name, ErrInvalidBackup, err)
	}
	defer zr.Close()
	sum, err := s.stage(ctx, &zr.Reader, opts, confirmed)
	if err != nil {
		return nil, fmt.Errorf("restore %s: %w", b.Name, err)
	}
	s.log.Warn("Backup staged for restore", "name", b.Name, "confirmed", confirmed,
		"securitySettingsRestored", opts.RestoreSecuritySettings)
	return sum, nil
}

func (s *Service) restoreUpload(ctx context.Context, r io.Reader, size int64, opts RestoreOptions, confirmed bool) (*RestoreSummary, error) {
	if r == nil {
		return nil, fmt.Errorf("restore upload: %w: no file", ErrInvalidBackup)
	}
	if size > MaxUploadSize {
		return nil, fmt.Errorf("restore upload: %w: larger than %d MB", ErrInvalidBackup, MaxUploadSize>>20)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tmp, err := os.CreateTemp(s.dataDir, restoreUploadPrefix+"*.zip")
	if err != nil {
		return nil, fmt.Errorf("restore upload: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	src := &errReader{r: ctxReader{ctx: ctx, r: r}}
	n, err := io.Copy(tmp, io.LimitReader(src, MaxUploadSize+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("restore upload: %w", ctx.Err())
		}
		if src.err != nil {
			return nil, fmt.Errorf("restore upload: read upload: %w", err)
		}
		return nil, fmt.Errorf("restore upload: save upload: %w", err)
	}
	switch {
	case n > MaxUploadSize:
		return nil, fmt.Errorf("restore upload: %w: larger than %d MB", ErrInvalidBackup, MaxUploadSize>>20)
	case size > 0 && n != size:
		return nil, fmt.Errorf("restore upload: %w: received %d of %d bytes", ErrInvalidBackup, n, size)
	}
	var magic [4]byte
	if _, err := tmp.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "PK\x03\x04" {
		return nil, fmt.Errorf("restore upload: %w: not a zip archive (only Dupearr backup .zip files can be restored)", ErrInvalidBackup)
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return nil, fmt.Errorf("restore upload: %w: not a readable zip archive: %v", ErrInvalidBackup, err)
	}
	sum, err := s.stage(ctx, zr, opts, confirmed)
	if err != nil {
		return nil, fmt.Errorf("restore upload: %w", err)
	}
	s.log.Warn("Uploaded backup staged for restore", "size", n, "confirmed", confirmed,
		"securitySettingsRestored", opts.RestoreSecuritySettings)
	return sum, nil
}

// stage validates the archive and atomically (re)places dataDir/.restore with the config.xml and
// dupearr.db to restore. s.mu must be held.
//
// The archive's database is untrusted: after the checks of verifyDatabase its rows are copied
// into a new database with this build's schema (rebuild), in which removals that were queued or
// running are then cancelled (neutralizeRemovals). The running instance's security settings and
// user are kept unless opts says otherwise (planRestore), and the session signing key is dropped.
// An unconfirmed restore carries unconfirmedMarker.
func (s *Service) stage(ctx context.Context, zr *zip.Reader, opts RestoreOptions, confirmed bool) (*RestoreSummary, error) {
	cfgEntry, dbEntry, err := pickEntries(zr)
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(s.dataDir, restoreTmpPrefix+"*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir) // no-op once promoted
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		return nil, err
	}

	cfgPath := filepath.Join(tmpDir, configFileName)
	dbPath := filepath.Join(tmpDir, dbFileName)
	untrusted := filepath.Join(tmpDir, untrustedDBName)
	if err := extract(ctx, cfgEntry, cfgPath, maxConfigSize); err != nil {
		return nil, err
	}
	if err := extract(ctx, dbEntry, untrusted, maxDBSize); err != nil {
		return nil, err
	}
	if err := verifyDatabase(ctx, untrusted); err != nil {
		return nil, err
	}
	if err := createReference(ctx, dbPath); err != nil {
		return nil, err
	}
	if err := checkSchemaCompatible(ctx, dbPath, untrusted); err != nil {
		return nil, err
	}

	live := ""
	if s.st != nil {
		live = strings.TrimSpace(s.st.Path())
	}
	sum := &RestoreSummary{SecuritySettingsRestored: opts.RestoreSecuritySettings, Changes: []RestoreChange{}}
	if err := s.rebuildInto(ctx, dbPath, untrusted, live, cfgPath, opts, sum); err != nil {
		return nil, err
	}
	if err := os.Remove(untrusted); err != nil {
		return nil, err
	}
	n, err := neutralizeRemovals(ctx, dbPath, time.Now())
	if err != nil {
		return nil, err
	}
	sum.CancelledRemovals, sum.InterruptedRemovals, sum.ReopenedGroups = n.cancelled, n.interrupted, n.reopened

	if n.cancelled+n.interrupted+n.reopened > 0 {
		s.log.Warn("Removals that were queued or running when the backup was taken will not resume after the restore",
			"cancelledActions", n.cancelled, "interruptedActions", n.interrupted, "reopenedGroups", n.reopened)
	}
	if !confirmed {
		fp, err := s.securityFingerprint(ctx)
		if err != nil {
			return nil, err
		}
		if err := writeFileAtomic(filepath.Join(tmpDir, securityStateFile), []byte(fp+"\n")); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(filepath.Join(tmpDir, unconfirmedMarker), []byte("staged restore awaiting confirmation\n")); err != nil {
			return nil, err
		}
	}
	syncDir(tmpDir)

	final := filepath.Join(s.dataDir, RestoreDirName)
	if _, err := os.Lstat(final); err == nil {
		old := filepath.Join(s.dataDir, restoreOldPrefix+strconv.FormatInt(time.Now().UnixNano(), 36))
		if err := os.Rename(final, old); err != nil {
			return nil, fmt.Errorf("replace staged restore: %w", err)
		}
		defer os.RemoveAll(old)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(tmpDir, final); err != nil {
		return nil, fmt.Errorf("stage restore: %w", err)
	}
	syncDir(s.dataDir)
	for _, c := range sum.Changes {
		s.log.Warn("Restore: setting differs from the backup", "setting", c.Setting, "restored", c.Applied)
	}
	s.log.Warn("Every session is signed out when the restore is applied (the session key is not restored)")
	return sum, nil
}

// rebuildInto copies the untrusted database into dbPath (see rebuild), deciding with planRestore
// what is taken from the backup, and replaces the staged config.xml at cfgPath with the planned one.
func (s *Service) rebuildInto(ctx context.Context, dbPath, untrusted, live, cfgPath string, opts RestoreOptions, sum *RestoreSummary) error {
	r, err := openRebuild(ctx, dbPath, untrusted, live)
	if err != nil {
		return err
	}
	defer r.close()
	backupUsers, err := r.users(ctx, stagedSchema)
	if err != nil {
		return invalidDB("cannot read the users: %v", err)
	}
	if len(backupUsers) > 1 {
		// Dupearr keeps a single account (UserRepo.Upsert): a second one can only have been added
		// to the archive, as a hidden login that password changes would not touch.
		return invalidDB("it holds %d user accounts but Dupearr keeps only one; the backup was modified "+
			"and cannot be restored", len(backupUsers))
	}
	var liveUsers []userRow
	if r.hasLive {
		if liveUsers, err = r.users(ctx, liveSchema); err != nil {
			return fmt.Errorf("read the current user: %w", err)
		}
	} else if !opts.RestoreSecuritySettings {
		return errors.New("the current user cannot be kept: the current database is unavailable")
	}
	data, err := readLimited(cfgPath, maxConfigSize)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidBackup, configFileName, err)
	}
	plan, err := s.planRestore(data, backupUsers, liveUsers, opts, sum)
	if err != nil {
		return err
	}
	if err := settingsChanges(ctx, r, sum); err != nil {
		return err
	}
	restoreToken, err := webhookTokenChange(ctx, r, opts, sum)
	if err != nil {
		return err
	}
	var liveKeys []string
	if !plan.restoreUsers {
		liveKeys = append(liveKeys, userIDSetting)
	}
	if !restoreToken {
		liveKeys = append(liveKeys, webhookTokenSetting)
	}
	if err := r.copyRows(ctx, plan.restoreUsers, liveKeys); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if err := r.finish(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return writeFileAtomic(cfgPath, plan.config)
}

// pickEntries validates the archive layout and returns its config.xml and dupearr.db entries.
func pickEntries(zr *zip.Reader) (cfg, db *zip.File, err error) {
	invalid := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidBackup, fmt.Sprintf(format, a...))
	}
	if len(zr.File) == 0 {
		return nil, nil, invalid("the archive is empty")
	}
	if len(zr.File) > maxEntries {
		return nil, nil, invalid("the archive has %d entries; a Dupearr backup has 3", len(zr.File))
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		if err := checkEntryName(name); err != nil {
			return nil, nil, err
		}
		if m := f.Mode(); m.IsDir() || !m.IsRegular() {
			return nil, nil, invalid("entry %q is not a regular file", name)
		}
		if f.Flags&0x1 != 0 {
			return nil, nil, invalid("entry %q is encrypted", name)
		}
		lower := strings.ToLower(name)
		if seen[lower] {
			return nil, nil, invalid("duplicate entry %q", name)
		}
		seen[lower] = true
		switch lower {
		case configFileName:
			cfg = f
		case dbFileName:
			db = f
		}
	}
	if cfg == nil || db == nil {
		return nil, nil, invalid("the archive must contain %s and %s", configFileName, dbFileName)
	}
	if cfg.UncompressedSize64 > uint64(maxConfigSize) {
		return nil, nil, invalid("%s is too large", configFileName)
	}
	if db.UncompressedSize64 > uint64(maxDBSize) {
		return nil, nil, invalid("%s is too large", dbFileName)
	}
	return cfg, db, nil
}

// checkEntryName accepts only plain file names: no directories, traversal, absolute paths or
// drive letters.
func checkEntryName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\\:\x00") {
		return fmt.Errorf("%w: entry %q is not a plain file name (folders and paths are not allowed)", ErrInvalidBackup, name)
	}
	return nil
}

// extract copies one archive entry to dst (created exclusively, 0600, fsynced), enforcing limit
// on the decompressed size and the entry checksum.
func extract(ctx context.Context, f *zip.File, dst string, limit int64) (err error) {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%w: cannot read %s: %v", ErrInvalidBackup, f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	src := &errReader{r: ctxReader{ctx: ctx, r: rc}}
	n, err := io.Copy(out, io.LimitReader(src, limit+1))
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil && src.err != nil:
		return fmt.Errorf("%w: cannot read %s: %v", ErrInvalidBackup, f.Name, err)
	case err != nil:
		return fmt.Errorf("extract %s: %w", f.Name, err)
	case n > limit:
		return fmt.Errorf("%w: %s is larger than %d bytes", ErrInvalidBackup, f.Name, limit)
	}
	return out.Sync()
}

// verifyDatabase checks that path is a healthy Dupearr SQLite database without modifying it.
func verifyDatabase(ctx context.Context, path string) error {
	invalid := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s: %s", ErrInvalidBackup, dbFileName, fmt.Sprintf(format, a...))
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	header := make([]byte, len(sqliteHeader))
	_, rerr := io.ReadFull(f, header)
	_ = f.Close()
	if rerr != nil || !bytes.Equal(header, []byte(sqliteHeader)) {
		return invalid("not an SQLite database")
	}

	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return invalid("cannot open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return invalid("integrity check failed: %v", err)
	}
	var problems []string
	for rows.Next() && len(problems) < 5 {
		var line string
		if err := rows.Scan(&line); err != nil {
			_ = rows.Close()
			return invalid("integrity check failed: %v", err)
		}
		problems = append(problems, line)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return invalid("integrity check failed: %v", err)
	}
	if len(problems) != 1 || problems[0] != "ok" {
		return invalid("integrity check failed: %s", strings.Join(problems, "; "))
	}

	for _, table := range requiredTables {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			return invalid("cannot read the schema: %v", err)
		}
		if n != 1 {
			return invalid("not a Dupearr database (table %s is missing)", table)
		}
	}
	return nil
}

// readOnlyDSN opens an untrusted database file read-only and immutable (SQLite neither writes
// nor creates -wal/-shm files next to it), with the protections of untrustedParams.
func readOnlyDSN(path string) string {
	return sqliteURI(path, "mode=ro&immutable=1&"+untrustedParams)
}

// sqliteURI returns a file: URI for path with the given SQLite URI parameters, so characters
// such as '?', '#' or '%' in the path cannot be mistaken for parameters.
func sqliteURI(path, rawQuery string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows volume paths: file:///C:/...
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: rawQuery}
	return u.String()
}

// migrationVersions returns the schema migrations recorded in a database.
func migrationVersions(ctx context.Context, dsn string) (map[int64]bool, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// checkSchemaCompatible compares the schema migrations recorded in the backup's database with
// those of reference, a database with this build's schema (createReference). It refuses a
// database written by a newer Dupearr (database.Open would refuse it, leaving Dupearr unable to
// start) and one that lacks a migration its rows cannot be upgraded past by the rebuild (see
// upgradableMigrations).
func checkSchemaCompatible(ctx context.Context, reference, stagedPath string) error {
	known, err := migrationVersions(ctx, sqliteURI(reference, "mode=ro"))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("read the schema version: %w", err)
	}
	staged, err := migrationVersions(ctx, readOnlyDSN(stagedPath))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s: cannot read the schema version: %v", ErrInvalidBackup, dbFileName, err)
	}
	var newest, supported int64
	for v := range known {
		supported = max(supported, v)
	}
	for v := range staged {
		if !known[v] {
			newest = max(newest, v)
		}
	}
	if newest > 0 {
		return fmt.Errorf("%w: the backup was made by a newer version of Dupearr (database schema %d; this version "+
			"supports up to %d). Update Dupearr before restoring it", ErrInvalidBackup, newest, supported)
	}
	for v := range known {
		if !staged[v] && !upgradableMigrations[v] {
			return fmt.Errorf("%w: %s: the database schema is incomplete or too old to be upgraded (migration %d is missing)",
				ErrInvalidBackup, dbFileName, v)
		}
	}
	return nil
}

// Messages recorded by neutralizeRemovals.
const (
	restoreCancelledMessage   = "Cancelled: Dupearr was restored from a backup taken while this removal was queued; approve the group again to remove it"
	restoreInterruptedMessage = "Interrupted: the backup Dupearr was restored from was taken while this removal was running; check whether the file still exists"
	restoreReopenedReason     = "Queued removals were cancelled because Dupearr was restored from a backup"
	dbTimeLayout              = "2006-01-02T15:04:05.000000000Z07:00" // internal/database's time layout
)

// neutralized counts what neutralizeRemovals changed.
type neutralized struct{ cancelled, interrupted, reopened int64 }

// forceSafeSettings turns dry run on, full-disc removal off and "always keep a Plex-playable copy"
// on in the settings document of the staged database (inside tx): a restore never makes Dupearr
// remove files before the admin has reviewed what it restored (the backup's deletion methods,
// mappings, recycle bin and connections; see settingsChanges), and never arms whole-disc removals
// by itself (GAP-04). Settings the document lacks take their defaults, which are these safe values.
func forceSafeSettings(ctx context.Context, tx *sql.Tx) error {
	var doc sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT CAST(value AS TEXT) FROM settings WHERE key = ?`, settingsDocKey).Scan(&doc)
	fields := map[string]json.RawMessage{}
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case strings.TrimSpace(doc.String) != "":
		if err := json.Unmarshal([]byte(doc.String), &fields); err != nil {
			return fmt.Errorf("the settings document is invalid: %v", err)
		}
	}
	def := models.DefaultSettings()
	changed := false
	for _, f := range []struct {
		name       string
		safe, dflt bool
	}{
		{"dryRun", true, def.DryRun},
		{"allowDiscRemoval", false, def.AllowDiscRemoval},
		{"keepPlayableCopy", true, def.KeepPlayableCopy},
	} {
		raw, set := fields[f.name]
		var v bool
		switch {
		case set && json.Unmarshal(raw, &v) == nil && v == f.safe:
			continue
		case !set && f.dflt == f.safe:
			continue // a missing setting takes its default
		}
		fields[f.name] = json.RawMessage(strconv.FormatBool(f.safe))
		changed = true
	}
	if !changed {
		return nil
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, settingsDocKey, string(out))
	return err
}

// neutralizeRemovals makes sure restoring a backup never resumes deletions: in the staged
// database (never the live one) removals that were pending when the backup was taken are
// cancelled, removals that were running are marked failed (their outcome is unknown), and groups
// that were queued are re-opened for approval, like CancelGroup does. Approvals are decisions
// about files as they were then; after a restore they must be made again against the library as
// it is now. The file is switched to rollback-journal mode first so that no -wal/-shm side files
// are left in the staging folder (database.Open switches it back to WAL).
func neutralizeRemovals(ctx context.Context, path string, now time.Time) (n neutralized, err error) {
	fail := func(what string, err error) (neutralized, error) {
		if ctx.Err() != nil {
			return neutralized{}, ctx.Err()
		}
		return neutralized{}, fmt.Errorf("cancel queued removals in the restored %s (%s): %w", dbFileName, what, err)
	}
	db, err := sql.Open("sqlite", sqliteURI(path, "mode=rw"))
	if err != nil {
		return fail("open", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			n, err = fail("close", cerr)
		}
		if err == nil {
			for _, suffix := range []string{"-journal", "-wal", "-shm"} {
				if _, serr := os.Lstat(path + suffix); serr == nil {
					n, err = fail("side files", fmt.Errorf("%s%s was left behind", dbFileName, suffix))
					return
				}
			}
		}
	}()
	db.SetMaxOpenConns(1)

	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return fail("journal mode", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return fail("journal mode", fmt.Errorf("journal mode is %q", mode))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fail("begin", err)
	}
	defer func() { _ = tx.Rollback() }()
	stamp := now.UTC().Format(dbTimeLayout)
	exec := func(q string, args ...any) (int64, error) {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	const updateActions = `UPDATE actions SET status = ?, message = ?, finished_at = ? WHERE status = ?`
	if n.cancelled, err = exec(updateActions,
		string(models.ActionCancelled), restoreCancelledMessage, stamp, string(models.ActionPending)); err != nil {
		return fail("pending actions", err)
	}
	if n.interrupted, err = exec(updateActions,
		string(models.ActionFailed), restoreInterruptedMessage, stamp, string(models.ActionRunning)); err != nil {
		return fail("running actions", err)
	}
	if n.reopened, err = exec(`UPDATE duplicate_groups SET status = ?, status_reason = ?, updated_at = ? WHERE status = ?`,
		string(models.GroupPending), restoreReopenedReason, stamp, string(models.GroupQueued)); err != nil {
		return fail("queued groups", err)
	}
	if err := forceSafeSettings(ctx, tx); err != nil {
		return fail("safe settings", err)
	}
	if err := tx.Commit(); err != nil {
		return fail("commit", err)
	}
	// Check the result rather than trusting the counts above.
	var left int64
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM actions WHERE status IN (?, ?)) +
		(SELECT count(*) FROM duplicate_groups WHERE status = ?)`,
		string(models.ActionPending), string(models.ActionRunning), string(models.GroupQueued)).Scan(&left); err != nil {
		return fail("verify", err)
	}
	if left != 0 {
		return fail("verify", fmt.Errorf("%d removals or queued groups are left", left))
	}
	return n, nil
}

// ApplyPendingRestore swaps a staged restore (dataDir/.restore) into place. It must be atomic
// enough that a crash never leaves a half-restored data dir. applied=false when nothing is staged.
//
// It must run before the database is opened. The current dupearr.db and config.xml are renamed
// to <name>.bak-<UTC timestamp>; the old database's -wal and -journal files move along with it
// (dupearr.db.bak-<ts>-wal, so the backup copy stays complete and SQLite never replays them into
// the restored database) and its -shm file is removed. An APPLYING marker written before the
// first rename makes the swap resumable: if the process dies half-way, the next call finishes
// the same restore. A staged folder missing a file (never started) is discarded, and so is a
// restore that was staged for review (StageRestore) but never confirmed (ConfirmRestore). Only
// the keepRestoreGenerations newest generations of .bak files are kept. Leftover temporary files
// of interrupted backups/restores are removed as well.
func ApplyPendingRestore(dataDir string) (applied bool, err error) {
	return applyPendingRestore(dataDir, time.Now())
}

func applyPendingRestore(dataDir string, now time.Time) (bool, error) {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	cleanupTemporaries(dataDir)

	dir := filepath.Join(dataDir, RestoreDirName)
	st, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("staged restore: %w", err)
	}
	if !st.IsDir() {
		return false, fmt.Errorf("staged restore %s is not a folder; remove it to start Dupearr", dir)
	}
	if err := checkStagedDir(dir, st); err != nil {
		return false, err
	}

	marker := filepath.Join(dir, applyingMarker)
	stamp := ""
	if b, err := readLimited(marker, 64); err == nil {
		stamp = strings.TrimSpace(string(b))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("staged restore: %w", err)
	}

	if stamp == "" || !bakStampRe.MatchString(stamp) {
		// Not started yet: both files must be present or the staging never completed, and a
		// restore staged for review must have been confirmed.
		_, uerr := os.Lstat(filepath.Join(dir, unconfirmedMarker))
		if uerr == nil || !isRegular(filepath.Join(dir, configFileName)) || !isRegular(filepath.Join(dir, dbFileName)) {
			if err := os.RemoveAll(dir); err != nil {
				return false, fmt.Errorf("discard incomplete staged restore: %w", err)
			}
			return false, nil
		}
		stamp = now.UTC().Format(bakTimeLayout)
		if err := writeFileAtomic(marker, []byte(stamp+"\n")); err != nil {
			return false, fmt.Errorf("staged restore: %w", err)
		}
	}

	if err := swapIn(dataDir, dir, dbFileName, stamp, true); err != nil {
		return false, err
	}
	if err := swapIn(dataDir, dir, configFileName, stamp, false); err != nil {
		return false, err
	}
	syncDir(dataDir)
	if err := os.RemoveAll(dir); err != nil {
		return true, fmt.Errorf("remove staged restore: %w", err)
	}
	pruneRestoreBackups(dataDir, keepRestoreGenerations, stamp)
	syncDir(dataDir)
	return true, nil
}

// checkStagedDir refuses a staged restore folder Dupearr did not create: Dupearr stages restores
// in a private folder (0700, its own user) that never holds the recycle bin's marker. Anything else
// is neither applied nor removed — it may be a misplaced recycle bin (removing it would delete the
// recycled files for good), or files planted by another user of the host (a config.xml with
// authentication None would be adopted without any of the checks of a restore).
func checkStagedDir(dir string, st fs.FileInfo) error {
	if _, err := os.Lstat(filepath.Join(dir, recycleBinMarkerName)); err == nil {
		return fmt.Errorf("%s is Dupearr's recycle bin, not a staged restore: move the recycle bin out of the data folder "+
			"(Settings → Media Management) and move its files elsewhere, then start Dupearr again", dir)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("staged restore %s is accessible to other users (mode %04o), so it was not created by Dupearr; "+
			"remove it (or move it elsewhere) to start Dupearr", dir, perm)
	}
	if !ownedByCurrentUser(st) {
		return fmt.Errorf("staged restore %s belongs to another user, so it was not created by Dupearr; "+
			"remove it (or move it elsewhere) to start Dupearr", dir)
	}
	return nil
}

// pruneRestoreBackups removes the files replaced by restores (<name>.bak-<stamp>…) except the
// keep newest generations (by stamp) and the generation current, the one just created (it is the
// only way to undo the restore, even when the clock went back and older stamps sort after it).
// Best effort: only names ApplyPendingRestore creates are touched, and only regular files.
func pruneRestoreBackups(dataDir string, keep int, current string) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return
	}
	byStamp := map[string][]string{}
	for _, e := range entries {
		m := bakFileRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		byStamp[m[1]] = append(byStamp[m[1]], e.Name())
	}
	stamps := make([]string, 0, len(byStamp))
	for st := range byStamp {
		stamps = append(stamps, st)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(stamps))) // the layout sorts chronologically
	if i := slices.Index(stamps, current); i > 0 {
		stamps = slices.Insert(slices.Delete(stamps, i, i+1), 0, current)
	}
	for i, st := range stamps {
		if i < keep {
			continue
		}
		for _, name := range byStamp[st] {
			_ = os.Remove(filepath.Join(dataDir, name))
		}
	}
}

// swapIn moves the live file aside (to name.bak-<stamp>) and the staged file into place. It is
// idempotent: a file already swapped in by an interrupted earlier attempt is left alone.
func swapIn(dataDir, stageDir, name, stamp string, isDB bool) error {
	staged := filepath.Join(stageDir, name)
	if _, err := os.Lstat(staged); errors.Is(err, fs.ErrNotExist) {
		return nil // done by an earlier, interrupted attempt
	} else if err != nil {
		return fmt.Errorf("apply restore of %s: %w", name, err)
	}
	live := filepath.Join(dataDir, name)
	bak, err := freeName(live + ".bak-" + stamp)
	if err != nil {
		return fmt.Errorf("apply restore of %s: %w", name, err)
	}
	if isDB {
		// The old database's side files must never be combined with the restored database.
		for _, suffix := range []string{"-journal", "-wal"} {
			if err := moveIfExists(live+suffix, bak+suffix); err != nil {
				return fmt.Errorf("apply restore of %s: %w", name, err)
			}
		}
		if err := os.Remove(live + "-shm"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("apply restore of %s: %w", name, err)
		}
	}
	if err := moveIfExists(live, bak); err != nil {
		return fmt.Errorf("apply restore of %s: %w", name, err)
	}
	if err := os.Rename(staged, live); err != nil {
		return fmt.Errorf("apply restore of %s: %w", name, err)
	}
	return nil
}

// freeName returns p, or p-N when p already exists (only the base name is checked, so a resumed
// apply that already moved side files keeps using the same name).
func freeName(p string) (string, error) {
	for i := 0; i < 100; i++ {
		candidate := p
		if i > 0 {
			candidate = p + "-" + strconv.Itoa(i)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no free backup name for %s", p)
}

// moveIfExists renames src to dst unless src does not exist. dst must not exist.
func moveIfExists(src, dst string) error {
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	return os.Rename(src, dst)
}

func isRegular(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

// writeFileAtomic writes data to path via a synced temporary file and a rename.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// cleanupTemporaries removes leftovers of interrupted backups and restores (best effort). Only
// names created by this package are touched.
func cleanupTemporaries(dataDir string) {
	sweep := func(dir string, prefixes ...string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			for _, p := range prefixes {
				if strings.HasPrefix(e.Name(), p) {
					_ = os.RemoveAll(filepath.Join(dir, e.Name()))
					break
				}
			}
		}
	}
	sweep(dataDir, restoreTmpPrefix, restoreUploadPrefix, restoreCheckPrefix, restoreOldPrefix)
	sweep(filepath.Join(dataDir, BackupsDirName), tmpBackupPrefix)
}

// errReader remembers the first read error so callers can tell corrupt input from write errors.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}
