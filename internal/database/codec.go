package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/sl0wz3r/dupearr/internal/store"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrDefaultProfile is returned (wrapped) when an operation would leave Dupearr without a
// default decision profile, e.g. deleting the default profile.
var ErrDefaultProfile = errors.New("the default profile cannot be deleted; make another profile the default first")

// ErrActionNotPending is returned (wrapped) by ActionRepo.Update when the requested status change
// is not allowed for the action's stored status — starting ("running") an action that is not
// pending, re-queueing ("pending") a cancelled or succeeded one, or cancelling a finished one —
// typically because it was cancelled (group resolved/ignored/deleted/re-evaluated, version
// overridden to keep) after the caller read it. It is also wrapped (with ErrNotRemovable) when
// the store cancels a queued action whose target is no longer marked for removal. The executor
// must skip such an action; it is never executed. The API maps it to 409.
var ErrActionNotPending = errors.New("action is no longer pending")

// ErrNotRemovable is returned (wrapped) when a removal action would be queued (created or moved
// to "pending") or started ("running") for a version that is not currently marked for removal:
// the action names no target, its group was deleted, ignored or resolved, or the version vanished,
// is protected or is stored with decision "keep" (e.g. the user overrode it while the approval was
// in flight). The row is left unchanged (or, for a queued action, cancelled). The API maps it to
// 409; the executor skips the action.
var ErrNotRemovable = errors.New("version is not marked for removal")

// ErrConstraint is wrapped (alongside the driver error) when a write violates a database
// constraint (UNIQUE, FOREIGN KEY, CHECK, NOT NULL). The API maps it to 409 like the *arrs do.
var ErrConstraint = errors.New("database constraint violated")

// Primary SQLite result codes; extended result codes keep them in the low byte.
const (
	sqliteBusy       = 5  // SQLITE_BUSY
	sqliteConstraint = 19 // SQLITE_CONSTRAINT
)

// wrap adds context to err, mapping sql.ErrNoRows to store.ErrNotFound and constraint
// violations to ErrConstraint. It returns nil for a nil err.
func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf(format, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", msg, store.ErrNotFound)
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == sqliteConstraint {
		return fmt.Errorf("%s: %w: %w", msg, ErrConstraint, err)
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// notFound returns a wrapped store.ErrNotFound for the described row.
func notFound(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), store.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Times
// ---------------------------------------------------------------------------

// timeLayout is RFC3339 with a fixed nine-digit fraction. Unlike time.RFC3339Nano (which trims
// trailing zeros) every value has the same width, so SQL comparisons and ORDER BY on the text
// column are chronological. time.Parse(time.RFC3339Nano, ...) reads it back.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// fmtTime formats t as UTC text.
func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// fmtTimePtr formats an optional time; nil becomes SQL NULL.
func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

// parseTime parses a stored time (fixed layout or any RFC3339 variant).
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// parseTimePtr parses a nullable stored time.
func parseTimePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// nowUTC returns the current time in UTC without a monotonic reading.
func nowUTC() time.Time { return time.Now().UTC().Round(0) }

// orNow returns t, or now when t is the zero time.
func orNow(t, now time.Time) time.Time {
	if t.IsZero() {
		return now
	}
	return t.UTC()
}

// ---------------------------------------------------------------------------
// JSON / scalar helpers
// ---------------------------------------------------------------------------

// toJSON marshals v for a JSON text column.
func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal json: %w", err)
	}
	return string(b), nil
}

// fromJSON unmarshals a JSON text column into v; an empty string leaves v untouched.
func fromJSON(s string, v any) error {
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

// rawOrDefault validates a raw JSON document and returns it as text, or def when empty.
func rawOrDefault(raw json.RawMessage, def string) (string, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return def, nil
	}
	if !json.Valid(raw) {
		return "", errors.New("invalid JSON document")
	}
	return string(raw), nil
}

// nonNil returns s, or an empty non-nil slice when s is nil (so JSON emits [] not null).
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// nonNilMap returns m, or an empty non-nil map when m is nil (so JSON emits {} not null).
func nonNilMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

// b2i converts a bool to the INTEGER 0/1 representation.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullInt64 converts an optional id to a nullable SQL value.
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// ptrInt64 converts a nullable SQL integer back to an optional id.
func ptrInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// limitArg converts a caller limit to a SQLite LIMIT value; ≤0 means "no limit" (-1).
func limitArg(limit int) int {
	if limit <= 0 {
		return -1
	}
	return limit
}

// ---------------------------------------------------------------------------
// Paging
// ---------------------------------------------------------------------------

const (
	defaultPageSize = 20
	maxPageSize     = 1000

	sortAscending  = "ascending"
	sortDescending = "descending"
)

// sortColumn maps an API sort key to SQL.
type sortColumn struct {
	// exprs are the ORDER BY expressions (each gets the direction appended).
	exprs []string
	// defaultDir applies when the request names this key but no valid direction.
	defaultDir string
}

// sortSpec is a whitelist of sort keys for one listing.
type sortSpec struct {
	columns    map[string]sortColumn // canonical key → column
	defaultKey string
	// tiebreak is appended (with the same direction) to make paging deterministic, e.g. "g.id".
	tiebreak string
}

// resolvedPaging is a normalized page request.
type resolvedPaging struct {
	page, size int
	key, dir   string
	orderBy    string // without the ORDER BY keyword
}

func (r resolvedPaging) offset() int { return (r.page - 1) * r.size }

// resolve normalizes p against the whitelist: page ≥ 1, 1 ≤ size ≤ 1000 (0 → 20), unknown sort
// keys fall back to the default key, unknown directions to the key's default direction.
func (s sortSpec) resolve(p store.Paging) resolvedPaging {
	r := resolvedPaging{page: p.Page, size: p.PageSize}
	if r.page < 1 {
		r.page = 1
	}
	switch {
	case r.size <= 0:
		r.size = defaultPageSize
	case r.size > maxPageSize:
		r.size = maxPageSize
	}
	// An absurd page number must not overflow the OFFSET computation (a negative OFFSET would
	// silently return the first page).
	if maxPage := math.MaxInt / r.size; r.page > maxPage {
		r.page = maxPage
	}

	key := s.defaultKey
	for k := range s.columns {
		if strings.EqualFold(k, strings.TrimSpace(p.SortKey)) {
			key = k
			break
		}
	}
	col := s.columns[key]
	r.key = key

	switch strings.ToLower(strings.TrimSpace(p.SortDirection)) {
	case sortAscending, "asc":
		r.dir = sortAscending
	case sortDescending, "desc":
		r.dir = sortDescending
	default:
		r.dir = col.defaultDir
	}
	sqlDir := "ASC"
	if r.dir == sortDescending {
		sqlDir = "DESC"
	}
	parts := make([]string, 0, len(col.exprs)+1)
	for _, e := range col.exprs {
		parts = append(parts, e+" "+sqlDir)
	}
	if s.tiebreak != "" {
		parts = append(parts, s.tiebreak+" "+sqlDir)
	}
	r.orderBy = strings.Join(parts, ", ")
	return r
}

// newPage builds a store.Page from a resolved request.
func newPage[T any](r resolvedPaging, total int, records []T) store.Page[T] {
	return store.Page[T]{
		Page:          r.page,
		PageSize:      r.size,
		SortKey:       r.key,
		SortDirection: r.dir,
		TotalRecords:  total,
		Records:       nonNil(records),
	}
}

// ---------------------------------------------------------------------------
// Query plumbing
// ---------------------------------------------------------------------------

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// queryAll runs query and maps every row with scan.
func queryAll[T any](ctx context.Context, q querier, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// expectAffected returns nil when res changed at least one row, else a wrapped store.ErrNotFound.
func expectAffected(res sql.Result, format string, args ...any) error {
	n, err := res.RowsAffected()
	if err != nil {
		return wrap(err, format, args...)
	}
	if n == 0 {
		return notFound(format, args...)
	}
	return nil
}
