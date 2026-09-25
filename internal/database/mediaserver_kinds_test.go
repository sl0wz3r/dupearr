package database

import (
	"context"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestSupportedKindsSQLParity: the media server kinds the repository counts and compares come from
// models.SupportedMediaServerKinds: the legacy pair ("plex" and the empty kind, in the legacy
// order) plus "jellyfin" since issue #4 Phase 1; every other kind (emby, a mis-cased kind) is
// neither compared nor counted.
func TestSupportedKindsSQLParity(t *testing.T) {
	ctx := context.Background()

	in, args := supportedKindsIn()
	if in != "kind IN (?, ?, ?)" || len(args) != 3 || args[0] != "plex" || args[1] != "" || args[2] != "jellyfin" {
		t.Fatalf("supportedKindsIn = %q %v", in, args)
	}

	// The legacy rows plus the jellyfin ones, whatever other kinds are stored.
	d := newTestDB(t)
	for i, kind := range []models.MediaServerKind{"plex", "", "jellyfin", "Plex", "plex", "emby"} {
		s := &models.MediaServer{Name: string(rune('A' + i)), Kind: kind, URL: "http://s", Enabled: i != 4}
		must(t, d.MediaServers().Create(ctx, s))
	}
	var got, legacy int
	must(t, d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_servers WHERE enabled = 1 AND `+in, args...).Scan(&got))
	must(t, d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_servers WHERE enabled = 1 AND kind IN (?, '', ?)`,
		models.MediaServerPlex, models.MediaServerJellyfin).Scan(&legacy))
	if got != legacy || got != 3 {
		t.Fatalf("count = %d, expected query %d, want 3 (the enabled 'plex', '' and 'jellyfin' rows)", got, legacy)
	}

	for _, tc := range []struct {
		kind     models.MediaServerKind
		enabled  bool
		storage  string
		compared bool
	}{
		{"plex", true, "", true},
		{"", true, "", true},
		{"plex", false, "", false},
		{"plex", true, models.StorageSeparate, false},
		{"jellyfin", true, "", true},
		{"jellyfin", true, models.StorageSeparate, false},
		{"emby", true, "", false},
		{"Plex", true, "", false},
	} {
		if c := comparedServer(tc.kind, tc.enabled, tc.storage); c != tc.compared {
			t.Errorf("comparedServer(%q, %v, %q) = %v", tc.kind, tc.enabled, tc.storage, c)
		}
	}

	confirmed := func(d *DB, id int64) bool {
		t.Helper()
		a, err := d.ArrInstances().Get(ctx, id)
		must(t, err)
		return a.LinksConfirmed
	}

	// A server of an unsupported kind is not counted: with it and one Plex server enabled, adding
	// the Plex server keeps the links confirmed (one compared server).
	d = newTestDB(t)
	inst := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://r", Enabled: true, LinksConfirmed: true, ServerIDs: []int64{}}
	must(t, d.ArrInstances().Create(ctx, inst))
	must(t, d.MediaServers().Create(ctx, &models.MediaServer{Name: "E", Kind: "emby", URL: "http://e", Enabled: true}))
	must(t, d.MediaServers().Create(ctx, &models.MediaServer{Name: "P", Kind: models.MediaServerPlex, URL: "http://p", Enabled: true}))
	if !confirmed(d, inst.ID) {
		t.Fatal("a server of an unsupported kind was counted as a second compared server")
	}
	// A second server of kind "" is a Plex server: the links are unconfirmed, as before.
	must(t, d.MediaServers().Create(ctx, &models.MediaServer{Name: "E", Kind: "", URL: "http://e", Enabled: true}))
	if confirmed(d, inst.ID) {
		t.Fatal("a second Plex server (kind \"\") kept the links confirmed")
	}
	// A Jellyfin server next to a Plex server is a second compared server: the links are
	// unconfirmed until a person confirms which servers the instance feeds (docs/DECISIONS.md D11).
	d = newTestDB(t)
	inst = &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://r", Enabled: true, LinksConfirmed: true, ServerIDs: []int64{}}
	must(t, d.ArrInstances().Create(ctx, inst))
	must(t, d.MediaServers().Create(ctx, &models.MediaServer{Name: "P", Kind: models.MediaServerPlex, URL: "http://p", Enabled: true}))
	must(t, d.MediaServers().Create(ctx, &models.MediaServer{Name: "J", Kind: models.MediaServerJellyfin, URL: "http://j", Enabled: true}))
	if confirmed(d, inst.ID) {
		t.Fatal("adding a Jellyfin server kept the links confirmed")
	}
}
