package database

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestSeededProfilesAreNeverUpgradedLater: a new database is seeded with the current templates
// (which already rank "disc" and "m2ts"). A user who removes "disc" from a profile before the next
// start keeps that choice: the one-time upgrade is for profiles of older builds only.
func TestSeededProfilesAreNeverUpgradedLater(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dupearr.db")
	d, err := Open(ctx, path, testLogger())
	must(t, err)
	tmpl := oldProfile("Keep Highest Quality", true)
	tmpl.Criteria[1].Order = []string{"remux", "disc", "bluray", "webdl"}
	tmpl.Criteria[2].Order = []string{"mkv", "m2ts", "other"}
	must(t, d.Seed(ctx, []models.Profile{*tmpl}))

	ps, err := d.Profiles().List(ctx)
	must(t, err)
	if len(ps) != 1 {
		t.Fatalf("%d profiles seeded", len(ps))
	}
	p := ps[0]
	p.Criteria[1].Order = []string{"remux", "bluray", "webdl"} // the user drops "disc"
	must(t, d.Profiles().Update(ctx, &p))
	must(t, d.Close())

	d = openAt(t, path)
	got, err := d.Profiles().Get(ctx, p.ID)
	must(t, err)
	if o := orderOf(t, got, models.CritSource); !slices.Equal(o, []string{"remux", "bluray", "webdl"}) {
		t.Fatalf("the upgrade changed a freshly seeded profile the user edited: %v", o)
	}
}
