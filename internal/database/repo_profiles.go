package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// profileRepo stores decision profiles. Invariant (enforced here and by a partial unique index):
// there is at most one default profile, and once any profile exists exactly one is the default.
type profileRepo struct{ d *DB }

const profileColumns = `id, name, is_default, criteria, keep_count, keep_per, protections, created_at, updated_at`

func scanProfile(s scanner) (models.Profile, error) {
	var (
		p                                       models.Profile
		criteria, protections, created, updated string
	)
	if err := s.Scan(&p.ID, &p.Name, &p.IsDefault, &criteria, &p.KeepCount, &p.KeepPer, &protections,
		&created, &updated); err != nil {
		return models.Profile{}, err
	}
	if err := fromJSON(criteria, &p.Criteria); err != nil {
		return models.Profile{}, fmt.Errorf("profile %d criteria: %w", p.ID, err)
	}
	if err := fromJSON(protections, &p.Protections); err != nil {
		return models.Profile{}, fmt.Errorf("profile %d protections: %w", p.ID, err)
	}
	p.Criteria = nonNil(p.Criteria)
	p.Protections = nonNil(p.Protections)
	var err error
	if p.CreatedAt, err = parseTime(created); err != nil {
		return models.Profile{}, err
	}
	if p.UpdatedAt, err = parseTime(updated); err != nil {
		return models.Profile{}, err
	}
	return p, nil
}

// profileJSON encodes the JSON columns of p.
func profileJSON(p *models.Profile) (criteria, protections string, err error) {
	if criteria, err = toJSON(nonNil(p.Criteria)); err != nil {
		return "", "", fmt.Errorf("criteria: %w", err)
	}
	if protections, err = toJSON(nonNil(p.Protections)); err != nil {
		return "", "", fmt.Errorf("protections: %w", err)
	}
	return criteria, protections, nil
}

// insertProfile inserts p inside tx, clearing the default flag of other profiles first when p is
// the default. It sets ID/CreatedAt/UpdatedAt on p.
func insertProfile(ctx context.Context, tx *sql.Tx, p *models.Profile) error {
	criteria, protections, err := profileJSON(p)
	if err != nil {
		return err
	}
	if p.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE profiles SET is_default = 0 WHERE is_default = 1`); err != nil {
			return wrap(err, "clear default profile")
		}
	}
	now := nowUTC()
	created := orNow(p.CreatedAt, now)
	res, err := tx.ExecContext(ctx, `INSERT INTO profiles
		(name, is_default, criteria, keep_count, keep_per, protections, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, b2i(p.IsDefault), criteria, p.KeepCount, p.KeepPer, protections, fmtTime(created), fmtTime(now))
	if err != nil {
		return wrap(err, "insert profile")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "insert profile")
	}
	p.ID, p.CreatedAt, p.UpdatedAt = id, created, now
	p.Criteria, p.Protections = nonNil(p.Criteria), nonNil(p.Protections)
	return nil
}

func (r profileRepo) List(ctx context.Context) ([]models.Profile, error) {
	out, err := queryAll(ctx, r.d.r, scanProfile, `SELECT `+profileColumns+` FROM profiles ORDER BY id`)
	if err != nil {
		return nil, wrap(err, "list profiles")
	}
	return out, nil
}

func (r profileRepo) Get(ctx context.Context, id int64) (*models.Profile, error) {
	p, err := scanProfile(r.d.r.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM profiles WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get profile %d", id)
	}
	return &p, nil
}

// GetDefault returns the default profile (store.ErrNotFound when no profile exists).
func (r profileRepo) GetDefault(ctx context.Context) (*models.Profile, error) {
	p, err := scanProfile(r.d.r.QueryRowContext(ctx,
		`SELECT `+profileColumns+` FROM profiles WHERE is_default = 1 ORDER BY id LIMIT 1`))
	if err != nil {
		return nil, wrap(err, "get default profile")
	}
	return &p, nil
}

// Create inserts p. When p.IsDefault the previous default loses the flag; when no default exists
// yet (first profile), p becomes the default regardless of p.IsDefault.
func (r profileRepo) Create(ctx context.Context, p *models.Profile) error {
	err := r.d.write(ctx, func(tx *sql.Tx) error {
		var defaults int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles WHERE is_default = 1`).Scan(&defaults); err != nil {
			return wrap(err, "count default profiles")
		}
		if defaults == 0 {
			p.IsDefault = true
		}
		return insertProfile(ctx, tx, p)
	})
	if err != nil {
		return fmt.Errorf("create profile %q: %w", p.Name, err)
	}
	return nil
}

// Update replaces p (by ID) and sets its CreatedAt (the stored one) and UpdatedAt. Setting
// IsDefault clears it on every other profile. Clearing IsDefault on the current default is ignored
// (p stays the default) because Dupearr must always have one; make another profile the default
// instead.
func (r profileRepo) Update(ctx context.Context, p *models.Profile) error {
	criteria, protections, err := profileJSON(p)
	if err != nil {
		return fmt.Errorf("update profile %d: %w", p.ID, err)
	}
	now := nowUTC()
	var created time.Time
	err = r.d.write(ctx, func(tx *sql.Tx) error {
		var (
			isDefault     bool
			storedCreated string
		)
		if err := tx.QueryRowContext(ctx, `SELECT is_default, created_at FROM profiles WHERE id = ?`, p.ID).
			Scan(&isDefault, &storedCreated); err != nil {
			return wrap(err, "get profile")
		}
		var err error
		if created, err = parseTime(storedCreated); err != nil {
			return err
		}
		if isDefault && !p.IsDefault {
			r.d.log.Debug("Ignoring attempt to unset the default flag of the default profile", "profileId", p.ID)
			p.IsDefault = true
		}
		if p.IsDefault && !isDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE profiles SET is_default = 0 WHERE is_default = 1`); err != nil {
				return wrap(err, "clear default profile")
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE profiles SET
			name = ?, is_default = ?, criteria = ?, keep_count = ?, keep_per = ?, protections = ?, updated_at = ?
			WHERE id = ?`,
			p.Name, b2i(p.IsDefault), criteria, p.KeepCount, p.KeepPer, protections, fmtTime(now), p.ID)
		return wrap(err, "update profile")
	})
	if err != nil {
		return fmt.Errorf("update profile %d: %w", p.ID, err)
	}
	p.CreatedAt, p.UpdatedAt = created, now
	p.Criteria, p.Protections = nonNil(p.Criteria), nonNil(p.Protections)
	return nil
}

// Delete removes a non-default profile. Libraries using it fall back to the default profile
// (their ProfileID becomes nil). Deleting the default profile fails with ErrDefaultProfile.
func (r profileRepo) Delete(ctx context.Context, id int64) error {
	return r.d.write(ctx, func(tx *sql.Tx) error {
		var isDefault bool
		err := tx.QueryRowContext(ctx, `SELECT is_default FROM profiles WHERE id = ?`, id).Scan(&isDefault)
		if err != nil {
			return wrap(err, "delete profile %d", id)
		}
		if isDefault {
			return fmt.Errorf("delete profile %d: %w", id, ErrDefaultProfile)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id); err != nil {
			return wrap(err, "delete profile %d", id)
		}
		return nil
	})
}
