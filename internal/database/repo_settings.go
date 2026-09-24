package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

type settingsRepo struct{ d *DB }

// normalizeSettings replaces nil slices so the document never serializes null.
func normalizeSettings(s models.Settings) models.Settings {
	s.DeletionMethods = nonNil(s.DeletionMethods)
	return s
}

// Get returns the settings document unmarshalled over models.DefaultSettings(), so fields added
// in newer versions (or missing from the stored document) get their defaults. A missing
// document yields the defaults. An explicit JSON null for deletionMethods yields an empty list
// (no deletion methods), never the defaults.
func (r settingsRepo) Get(ctx context.Context) (models.Settings, error) {
	s := models.DefaultSettings()
	var doc string
	err := r.d.r.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingsKey).Scan(&doc)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s, nil
	case err != nil:
		return models.Settings{}, wrap(err, "get settings")
	}
	if err := fromJSON(doc, &s); err != nil {
		return models.Settings{}, fmt.Errorf("get settings: stored document is invalid: %w", err)
	}
	return normalizeSettings(s), nil
}

// Save replaces the settings document.
func (r settingsRepo) Save(ctx context.Context, s models.Settings) error {
	doc, err := toJSON(normalizeSettings(s))
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	_, err = r.d.w.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		settingsKey, doc)
	return wrap(err, "save settings")
}

// GetValue returns an internal key/value entry; ok is false when the key does not exist.
func (r settingsRepo) GetValue(ctx context.Context, key string) (string, bool, error) {
	if strings.TrimSpace(key) == "" {
		return "", false, errors.New("get setting value: empty key")
	}
	var v string
	err := r.d.r.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, wrap(err, "get setting value %q", key)
	}
	return v, true, nil
}

// SetValue creates or replaces an internal key/value entry. The key holding the settings
// document is reserved (use Save).
func (r settingsRepo) SetValue(ctx context.Context, key, value string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("set setting value: empty key")
	}
	if key == settingsKey {
		return fmt.Errorf("set setting value: key %q is reserved for the settings document", key)
	}
	_, err := r.d.w.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, value)
	return wrap(err, "set setting value %q", key)
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

type userRepo struct{ d *DB }

const userColumns = `id, username, password_hash, created_at`

func scanUser(s scanner) (models.User, error) {
	var (
		u       models.User
		created string
	)
	if err := s.Scan(&u.ID, &u.Username, &u.PasswordHash, &created); err != nil {
		return models.User{}, err
	}
	var err error
	if u.CreatedAt, err = parseTime(created); err != nil {
		return models.User{}, err
	}
	return u, nil
}

func (r userRepo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, wrap(err, "count users")
	}
	return n, nil
}

// GetByUsername looks the user up case-insensitively (like the *arrs).
func (r userRepo) GetByUsername(ctx context.Context, username string) (*models.User, error) {
	u, err := scanUser(r.d.r.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ? COLLATE NOCASE`, username))
	if err != nil {
		return nil, wrap(err, "get user by name")
	}
	return &u, nil
}

func (r userRepo) GetByID(ctx context.Context, id int64) (*models.User, error) {
	u, err := scanUser(r.d.r.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get user %d", id)
	}
	return &u, nil
}

// Upsert creates the single user or replaces username/password of the existing one. Empty
// usernames or password hashes are refused so a broken caller can never create an account
// that accepts an empty password.
func (r userRepo) Upsert(ctx context.Context, username, passwordHash string) (*models.User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("upsert user: empty username")
	}
	if passwordHash == "" {
		return nil, errors.New("upsert user: empty password hash")
	}
	var out models.User
	err := r.d.write(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx,
				`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
				username, passwordHash, fmtTime(nowUTC()))
			if err != nil {
				return wrap(err, "insert user")
			}
			if id, err = res.LastInsertId(); err != nil {
				return wrap(err, "insert user")
			}
		case err != nil:
			return wrap(err, "find user")
		default:
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET username = ?, password_hash = ? WHERE id = ?`,
				username, passwordHash, id); err != nil {
				return wrap(err, "update user")
			}
		}
		out, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
		return wrap(err, "reload user")
	})
	if err != nil {
		return nil, fmt.Errorf("upsert user: %w", err)
	}
	return &out, nil
}

func (r userRepo) DeleteAll(ctx context.Context) error {
	_, err := r.d.w.ExecContext(ctx, `DELETE FROM users`)
	return wrap(err, "delete users")
}
