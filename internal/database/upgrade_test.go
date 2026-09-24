package database

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// oldProfile is a profile as stored before full-disc support: no "disc" source, no "m2ts"
// container, with a customized source order and an empty (= default) video codec order.
func oldProfile(name string, def bool) *models.Profile {
	return &models.Profile{
		Name: name, IsDefault: def, KeepCount: 1,
		Criteria: []models.Criterion{
			{Type: models.CritHealth, Enabled: true},
			{Type: models.CritSource, Enabled: true, Order: []string{"remux", "bluray", "webdl", "webrip", "hdtv", "dvd", "sdtv", "unknown"}},
			{Type: models.CritContainer, Enabled: true, Order: []string{"mkv", "mp4", "m4v", "other", "avi", "ts"}},
			{Type: models.CritVideoCodec, Enabled: true},
		},
	}
}

func orderOf(t *testing.T, p *models.Profile, ct models.CriterionType) []string {
	t.Helper()
	for _, c := range p.Criteria {
		if c.Type == ct {
			return c.Order
		}
	}
	t.Fatalf("profile %q has no %s criterion", p.Name, ct)
	return nil
}

func TestUpgradeAddsDiscSourceAndM2TSContainer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dupearr.db")
	d, err := Open(ctx, path, testLogger())
	must(t, err)
	// Created by an older build: the profiles lack the new values and no marker is stored.
	hq, custom := oldProfile("Keep Highest Quality", true), oldProfile("Custom", false)
	custom.Criteria[1].Order = []string{"bluray", "webdl"} // no remux: disc goes before bluray
	custom.Criteria[2].Order = []string{"mp4", "mkv"}      // no m4v: m2ts goes after mkv
	must(t, d.Profiles().Create(ctx, hq))
	must(t, d.Profiles().Create(ctx, custom))
	if _, ok, err := d.Settings().GetValue(ctx, discProfilesMarker); err != nil || ok {
		t.Fatalf("an empty database must not record the upgrade (a restore copies its rows in later): ok=%v err=%v", ok, err)
	}
	must(t, d.Close())

	d = openAt(t, path)
	got, err := d.Profiles().Get(ctx, hq.ID)
	must(t, err)
	if o := orderOf(t, got, models.CritSource); !slices.Equal(o, []string{"remux", "disc", "bluray", "webdl", "webrip", "hdtv", "dvd", "sdtv", "unknown"}) {
		t.Errorf("source order %v", o)
	}
	if o := orderOf(t, got, models.CritContainer); !slices.Equal(o, []string{"mkv", "mp4", "m4v", "m2ts", "other", "avi", "ts"}) {
		t.Errorf("container order %v", o)
	}
	if o := orderOf(t, got, models.CritVideoCodec); len(o) != 0 {
		t.Errorf("an empty (default) order was changed: %v", o)
	}
	got, err = d.Profiles().Get(ctx, custom.ID)
	must(t, err)
	if o := orderOf(t, got, models.CritSource); !slices.Equal(o, []string{"disc", "bluray", "webdl"}) {
		t.Errorf("custom source order %v", o)
	}
	if o := orderOf(t, got, models.CritContainer); !slices.Equal(o, []string{"mp4", "mkv", "m2ts"}) {
		t.Errorf("custom container order %v", o)
	}
	if v, ok, err := d.Settings().GetValue(ctx, discProfilesMarker); err != nil || !ok || v == "" {
		t.Fatalf("marker not recorded: %q %v %v", v, ok, err)
	}

	// The upgrade runs once: a user who then removes "disc" keeps that choice.
	got.Criteria[1].Order = []string{"bluray", "webdl"}
	must(t, d.Profiles().Update(ctx, got))
	must(t, d.Close())
	d = openAt(t, path)
	got, err = d.Profiles().Get(ctx, custom.ID)
	must(t, err)
	if o := orderOf(t, got, models.CritSource); !slices.Equal(o, []string{"bluray", "webdl"}) {
		t.Errorf("the upgrade ran twice: %v", o)
	}
}

func TestUpgradeDiscOrders(t *testing.T) {
	cases := []struct {
		name        string
		in          []string
		insert      string
		want        []string
		wantChanged bool
	}{
		{"source after remux", []string{"remux", "bluray"}, models.SourceDisc, []string{"remux", "disc", "bluray"}, true},
		{"source case/spaces", []string{" Remux ", "BluRay"}, models.SourceDisc, []string{" Remux ", "disc", "BluRay"}, true},
		{"source already there", []string{"disc", "remux"}, models.SourceDisc, []string{"disc", "remux"}, false},
		{"source before unknown", []string{"webdl", "unknown"}, models.SourceDisc, []string{"webdl", "disc", "unknown"}, true},
		{"source appended", []string{"webdl"}, models.SourceDisc, []string{"webdl", "disc"}, true},
		{"container after m4v", []string{"m4v", "ts"}, "m2ts", []string{"m4v", "m2ts", "ts"}, true},
		{"container before other", []string{"avi", "other"}, "m2ts", []string{"avi", "m2ts", "other"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			var changed bool
			if tc.insert == models.SourceDisc {
				got, changed = withDiscSource(tc.in)
			} else {
				got, changed = withM2TSContainer(tc.in)
			}
			if !slices.Equal(got, tc.want) || changed != tc.wantChanged {
				t.Fatalf("got %v changed=%v, want %v changed=%v", got, changed, tc.want, tc.wantChanged)
			}
		})
	}
	// Empty orders mean "the default" (which already has both values): untouched.
	if got, changed := withDiscSource(nil); changed || got != nil {
		t.Fatalf("empty order changed: %v", got)
	}
}
