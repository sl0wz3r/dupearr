package database

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestSettings_GetDefaultsWhenMissing(t *testing.T) {
	d := newTestDB(t)
	got, err := d.Settings().Get(context.Background())
	must(t, err)
	if !reflect.DeepEqual(got, models.DefaultSettings()) {
		t.Fatalf("Get() = %+v, want defaults", got)
	}
}

func TestSettings_SaveGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	s := models.DefaultSettings()
	s.DryRun = false
	s.Mode = models.ModeAuto
	s.MinAgeHours = 1
	s.DeletionMethods = []string{models.MethodPlex}
	s.RecycleBinPath = "/data/.recycle"
	s.DurationTolerancePercent = 12.5
	must(t, d.Settings().Save(ctx, s))
	got, err := d.Settings().Get(ctx)
	must(t, err)
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("Get() = %+v, want %+v", got, s)
	}

	// Save replaces the whole document.
	s.Mode = models.ModeManual
	must(t, d.Settings().Save(ctx, s))
	got, err = d.Settings().Get(ctx)
	must(t, err)
	if got.Mode != models.ModeManual {
		t.Fatalf("Mode = %q after second save", got.Mode)
	}
}

func TestSettings_StoredDocumentMergedOverDefaults(t *testing.T) {
	defaults := models.DefaultSettings()
	tests := []struct {
		name  string
		doc   string
		check func(t *testing.T, s models.Settings)
	}{
		{
			name: "missing fields get defaults",
			doc:  `{"dryRun": false, "maxDeletionsPerRun": 3}`,
			check: func(t *testing.T, s models.Settings) {
				if s.DryRun || s.MaxDeletionsPerRun != 3 {
					t.Fatalf("stored values lost: %+v", s)
				}
				if s.MinAgeHours != defaults.MinAgeHours || s.StableScansRequired != defaults.StableScansRequired ||
					!reflect.DeepEqual(s.DeletionMethods, defaults.DeletionMethods) {
					t.Fatalf("defaults not applied: %+v", s)
				}
			},
		},
		{
			name: "unknown (removed) keys are ignored",
			doc:  `{"emptyPlexTrashAfterDelete": true, "mode": "auto"}`,
			check: func(t *testing.T, s models.Settings) {
				if s.Mode != models.ModeAuto {
					t.Fatalf("Mode = %q", s.Mode)
				}
			},
		},
		{
			name: "explicit null deletion methods means none, not defaults",
			doc:  `{"deletionMethods": null}`,
			check: func(t *testing.T, s models.Settings) {
				if s.DeletionMethods == nil || len(s.DeletionMethods) != 0 {
					t.Fatalf("DeletionMethods = %#v, want empty", s.DeletionMethods)
				}
			},
		},
		{
			name: "empty object is all defaults",
			doc:  `{}`,
			check: func(t *testing.T, s models.Settings) {
				if !reflect.DeepEqual(s, defaults) {
					t.Fatalf("got %+v", s)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d := newTestDB(t)
			_, err := d.w.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)`, settingsKey, tt.doc)
			must(t, err)
			s, err := d.Settings().Get(ctx)
			must(t, err)
			tt.check(t, s)
		})
	}
}

func TestSettings_CorruptDocument(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	_, err := d.w.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, '{not json')`, settingsKey)
	must(t, err)
	if _, err := d.Settings().Get(ctx); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("Get() err = %v, want an invalid-document error", err)
	}
}

func TestSettings_NilDeletionMethodsSavedAsEmptyList(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	s := models.DefaultSettings()
	s.DeletionMethods = nil
	must(t, d.Settings().Save(ctx, s))
	raw, ok, err := d.Settings().GetValue(ctx, settingsKey)
	must(t, err)
	if !ok || !strings.Contains(raw, `"deletionMethods":[]`) {
		t.Fatalf("stored document = %s", raw)
	}
	var m map[string]any
	must(t, json.Unmarshal([]byte(raw), &m))
}

func TestSettings_Values(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Settings()

	v, ok, err := r.GetValue(ctx, "auth.sessionKey")
	must(t, err)
	if ok || v != "" {
		t.Fatalf("missing key: %q %v", v, ok)
	}
	must(t, r.SetValue(ctx, "auth.sessionKey", "secret"))
	must(t, r.SetValue(ctx, "auth.sessionKey", "rotated"))
	must(t, r.SetValue(ctx, "empty", ""))
	for key, want := range map[string]string{"auth.sessionKey": "rotated", "empty": ""} {
		v, ok, err = r.GetValue(ctx, key)
		must(t, err)
		if !ok || v != want {
			t.Fatalf("GetValue(%q) = %q %v, want %q", key, v, ok, want)
		}
	}

	for _, key := range []string{"", "  ", settingsKey} {
		if err := r.SetValue(ctx, key, "x"); err == nil {
			t.Errorf("SetValue(%q) succeeded", key)
		}
	}
	if _, _, err := r.GetValue(ctx, ""); err == nil {
		t.Error("GetValue(\"\") succeeded")
	}
	// The settings document is untouched by SetValue attempts.
	s, err := r.Get(ctx)
	must(t, err)
	if !reflect.DeepEqual(s, models.DefaultSettings()) {
		t.Fatal("settings document changed")
	}
}

func TestUsers(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Users()

	n, err := r.Count(ctx)
	must(t, err)
	if n != 0 {
		t.Fatalf("Count = %d", n)
	}
	_, err = r.GetByUsername(ctx, "admin")
	wantNotFound(t, err)

	u, err := r.Upsert(ctx, " Admin ", "hash1")
	must(t, err)
	if u.ID == 0 || u.Username != "Admin" || u.PasswordHash != "hash1" || u.CreatedAt.IsZero() {
		t.Fatalf("created user = %+v", u)
	}

	// Case-insensitive lookup.
	for _, name := range []string{"Admin", "admin", "ADMIN"} {
		got, err := r.GetByUsername(ctx, name)
		must(t, err)
		if got.ID != u.ID {
			t.Fatalf("GetByUsername(%q) = %+v", name, got)
		}
	}
	got, err := r.GetByID(ctx, u.ID)
	must(t, err)
	if got.Username != "Admin" || !got.CreatedAt.Equal(u.CreatedAt) {
		t.Fatalf("GetByID = %+v", got)
	}
	_, err = r.GetByID(ctx, u.ID+1)
	wantNotFound(t, err)

	// Upsert replaces the single user's credentials.
	u2, err := r.Upsert(ctx, "root", "hash2")
	must(t, err)
	if u2.ID != u.ID || u2.Username != "root" || u2.PasswordHash != "hash2" || !u2.CreatedAt.Equal(u.CreatedAt) {
		t.Fatalf("replaced user = %+v (original %+v)", u2, u)
	}
	if n, _ := r.Count(ctx); n != 1 {
		t.Fatalf("Count = %d after replace", n)
	}
	_, err = r.GetByUsername(ctx, "admin")
	wantNotFound(t, err)

	for _, tc := range []struct{ user, hash string }{{"", "h"}, {"  ", "h"}, {"x", ""}} {
		if _, err := r.Upsert(ctx, tc.user, tc.hash); err == nil {
			t.Errorf("Upsert(%q, %q) succeeded", tc.user, tc.hash)
		}
	}

	must(t, r.DeleteAll(ctx))
	if n, _ := r.Count(ctx); n != 0 {
		t.Fatalf("Count = %d after DeleteAll", n)
	}
	must(t, r.DeleteAll(ctx)) // idempotent
}
