package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Several Plex servers (docs/DECISIONS.md D11): migration 0004, the repositories' new fields and
// the *arr link data upgrade.

// openAtVersion builds a database with the migrations up to version (inclusive) applied, the way
// an older release left it.
func openAtVersion(t *testing.T, path string, version int) {
	t.Helper()
	ctx := context.Background()
	migrations, err := loadMigrations(migrationsFS)
	must(t, err)
	raw, err := sql.Open(driverName, dsn(path, false))
	must(t, err)
	defer raw.Close()
	_, err = raw.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	must(t, err)
	_, err = raw.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL,
		name TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	must(t, err)
	for _, m := range migrations {
		if m.version > version {
			break
		}
		_, err = raw.ExecContext(ctx, m.sql)
		must(t, err)
		_, err = raw.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?, ?, ?)`, m.version, m.name, fmtTime(nowUTC()))
		must(t, err)
	}
}

func TestMigration0004OnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dupearr.db")
	openAtVersion(t, path, 3)
	raw, err := sql.Open(driverName, dsn(path, false))
	must(t, err)
	now := fmtTime(nowUTC())
	_, err = raw.ExecContext(ctx, `INSERT INTO media_servers (name, kind, url, created_at, updated_at) VALUES ('A', 'plex', 'http://a', ?, ?)`, now, now)
	must(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO media_servers (name, kind, url, created_at, updated_at) VALUES ('B', 'plex', 'http://b', ?, ?)`, now, now)
	must(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO arr_instances (name, kind, url, created_at, updated_at) VALUES ('Radarr', 'radarr', 'http://r', ?, ?)`, now, now)
	must(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO duplicate_groups (key, status, first_seen_at, last_seen_at, updated_at) VALUES ('movie:tmdb:1', 'pending', ?, ?, ?)`, now, now, now)
	must(t, err)
	must(t, raw.Close())

	d := openAt(t, path)
	servers, err := d.MediaServers().List(ctx)
	must(t, err)
	for _, s := range servers {
		if s.Storage != "" {
			t.Fatalf("server %q storage %q", s.Name, s.Storage)
		}
	}
	arrs, err := d.ArrInstances().List(ctx)
	must(t, err)
	// Two enabled servers: the upgrade stores no links (the fail-closed state).
	if len(arrs) != 1 || arrs[0].LinksConfirmed || arrs[0].ServerIDs == nil || len(arrs[0].ServerIDs) != 0 {
		t.Fatalf("instances %+v", arrs)
	}
	g, err := d.Groups().GetByKey(ctx, "movie:tmdb:1")
	must(t, err)
	if g.CrossServer != nil {
		t.Fatalf("a group stored before 0004 has a record: %+v", g.CrossServer)
	}
}

func TestMultiServerRepositoryRoundTrips(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	a := &models.MediaServer{Name: "A", Kind: models.MediaServerPlex, URL: "http://a", Enabled: true}
	b := &models.MediaServer{Name: "B", Kind: models.MediaServerPlex, URL: "http://b", Enabled: true, Storage: models.StorageSeparate}
	must(t, d.MediaServers().Create(ctx, a))
	must(t, d.MediaServers().Create(ctx, b))
	got, err := d.MediaServers().Get(ctx, b.ID)
	must(t, err)
	if got.Storage != models.StorageSeparate {
		t.Fatalf("storage %q", got.Storage)
	}
	got.Storage = ""
	must(t, d.MediaServers().Update(ctx, got))
	if got, _ = d.MediaServers().Get(ctx, b.ID); got.Storage != "" {
		t.Fatalf("storage after update %q", got.Storage)
	}

	r := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://r", Enabled: true, ServerIDs: []int64{b.ID, a.ID, b.ID}, LinksConfirmed: true}
	must(t, d.ArrInstances().Create(ctx, r))
	if !slices.Equal(r.ServerIDs, []int64{a.ID, b.ID}) {
		t.Fatalf("created links %v", r.ServerIDs)
	}
	gotArr, err := d.ArrInstances().Get(ctx, r.ID)
	must(t, err)
	if !reflect.DeepEqual(*gotArr, *r) {
		t.Fatalf("Get = %+v, want %+v", gotArr, r)
	}
	// nil keeps the links; an empty list clears them.
	gotArr.ServerIDs, gotArr.Name = nil, "Radarr 2"
	must(t, d.ArrInstances().Update(ctx, gotArr))
	if x, _ := d.ArrInstances().Get(ctx, r.ID); !slices.Equal(x.ServerIDs, []int64{a.ID, b.ID}) || x.Name != "Radarr 2" {
		t.Fatalf("nil kept %+v", x)
	}
	gotArr.ServerIDs, gotArr.LinksConfirmed = []int64{a.ID}, false
	must(t, d.ArrInstances().Update(ctx, gotArr))
	if x, _ := d.ArrInstances().Get(ctx, r.ID); !slices.Equal(x.ServerIDs, []int64{a.ID}) || x.LinksConfirmed {
		t.Fatalf("replaced %+v", x)
	}
	// Deleting a server drops its links (cascade); so does deleting the instance.
	must(t, d.MediaServers().Delete(ctx, a.ID))
	if x, _ := d.ArrInstances().Get(ctx, r.ID); len(x.ServerIDs) != 0 {
		t.Fatalf("links after the server was deleted: %v", x.ServerIDs)
	}
	gotArr.ServerIDs = []int64{b.ID}
	must(t, d.ArrInstances().Update(ctx, gotArr))
	must(t, d.ArrInstances().Delete(ctx, r.ID))
	var n int
	must(t, d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM arr_server_links`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d links left", n)
	}

	// The cross-server record: nil ↔ ''.
	g := testGroup("movie:tmdb:5", 1, 2)
	upsert(t, d, g)
	stored, err := d.Groups().Get(ctx, g.ID)
	must(t, err)
	if stored.CrossServer != nil {
		t.Fatal("nil record stored as a record")
	}
	var raw string
	must(t, d.r.QueryRowContext(ctx, `SELECT cross_server FROM duplicate_groups WHERE id = ?`, g.ID).Scan(&raw))
	if raw != "" {
		t.Fatalf("nil record stored as %q", raw)
	}
	rec := &models.CrossServerRecord{Complete: true,
		Servers:   []models.CrossServerServer{{ServerID: 1, MachineIdentifier: "m", Mapped: true}, {ServerID: 2, Separate: true, Unread: "B: down"}},
		Libraries: []models.CrossServerLibrary{{ServerID: 2, LibraryID: 7, SectionKey: "1", Type: "movie", Locations: []string{"/m"}, ScannedAt: 5, ContentChangedAt: 6, Refreshing: true}},
	}
	stored.CrossServer = rec
	upsert(t, d, stored)
	again, err := d.Groups().Get(ctx, g.ID)
	must(t, err)
	if !reflect.DeepEqual(again.CrossServer, rec) {
		t.Fatalf("record %+v, want %+v", again.CrossServer, rec)
	}
}

func TestUpgradeArrLinks(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		servers       int
		instances     int
		wantLinked    bool
		wantMarker    bool
		disableSecond bool
	}{
		{name: "one enabled server", servers: 1, instances: 2, wantLinked: true, wantMarker: true},
		{name: "two enabled servers", servers: 2, instances: 2, wantMarker: true},
		{name: "second server disabled", servers: 2, instances: 1, disableSecond: true, wantLinked: true, wantMarker: true},
		{name: "no instances", servers: 1},
		{name: "no server", instances: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dupearr.db")
			openAtVersion(t, path, 3)
			raw, err := sql.Open(driverName, dsn(path, false))
			must(t, err)
			now := fmtTime(nowUTC())
			for i := 0; i < tc.servers; i++ {
				enabled := 1
				if tc.disableSecond && i == 1 {
					enabled = 0
				}
				_, err = raw.ExecContext(ctx, `INSERT INTO media_servers (name, kind, url, enabled, created_at, updated_at) VALUES (?, 'plex', 'http://x', ?, ?, ?)`,
					"S"+string(rune('A'+i)), enabled, now, now)
				must(t, err)
			}
			for i := 0; i < tc.instances; i++ {
				_, err = raw.ExecContext(ctx, `INSERT INTO arr_instances (name, kind, url, created_at, updated_at) VALUES (?, 'radarr', 'http://r', ?, ?)`,
					"R"+string(rune('A'+i)), now, now)
				must(t, err)
			}
			must(t, raw.Close())
			d := openAt(t, path)
			arrs, err := d.ArrInstances().List(ctx)
			must(t, err)
			for _, a := range arrs {
				linked := a.LinksConfirmed && slices.Equal(a.ServerIDs, []int64{1})
				if linked != tc.wantLinked || (!tc.wantLinked && (a.LinksConfirmed || len(a.ServerIDs) != 0)) {
					t.Fatalf("instance %+v", a)
				}
			}
			if _, ok, err := d.Settings().GetValue(ctx, arrLinksMarker); err != nil || ok != tc.wantMarker {
				t.Fatalf("marker %v %v", ok, err)
			}
			// It runs once: links a person removed are not added again.
			if tc.wantLinked {
				a := arrs[0]
				a.ServerIDs, a.LinksConfirmed = []int64{}, false
				must(t, d.ArrInstances().Update(ctx, &a))
				must(t, d.Close())
				d = openAt(t, path)
				if x, _ := d.ArrInstances().Get(ctx, a.ID); len(x.ServerIDs) != 0 || x.LinksConfirmed {
					t.Fatalf("the upgrade ran twice: %+v", x)
				}
			}
		})
	}
}

// Links confirmed while a server was not enabled (or declared separate storage) do not cover it:
// adding or enabling a Plex server that makes two or more, or declaring one shared storage, makes
// the links of the instances not linked to it unconfirmed (docs/DECISIONS.md D11).
func TestNewServerUnconfirmsArrLinks(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	confirmed := func(id int64) bool {
		t.Helper()
		a, err := d.ArrInstances().Get(ctx, id)
		must(t, err)
		return a.LinksConfirmed
	}
	a := &models.MediaServer{Name: "A", Kind: models.MediaServerPlex, URL: "http://a", Enabled: true}
	must(t, d.MediaServers().Create(ctx, a))
	// Linked to the only server, confirmed (the data upgrade and the API do this automatically).
	radarr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://r", Enabled: true, ServerIDs: []int64{a.ID}, LinksConfirmed: true}
	must(t, d.ArrInstances().Create(ctx, radarr))
	// A disabled server and a separate one change nothing.
	b := &models.MediaServer{Name: "B", Kind: models.MediaServerPlex, URL: "http://b", Enabled: false}
	must(t, d.MediaServers().Create(ctx, b))
	c := &models.MediaServer{Name: "C", Kind: models.MediaServerPlex, URL: "http://c", Enabled: true, Storage: models.StorageSeparate}
	must(t, d.MediaServers().Create(ctx, c))
	if !confirmed(radarr.ID) {
		t.Fatal("a disabled or separate server unconfirmed the links")
	}
	// Sonarr is linked to B (a person chose it while B was disabled).
	sonarr := &models.ArrInstance{Name: "Sonarr", Kind: models.ArrSonarr, URL: "http://s", Enabled: true, ServerIDs: []int64{a.ID, b.ID}, LinksConfirmed: true}
	must(t, d.ArrInstances().Create(ctx, sonarr))
	b.Enabled = true
	must(t, d.MediaServers().Update(ctx, b))
	if confirmed(radarr.ID) || !confirmed(sonarr.ID) {
		t.Fatalf("after enabling B: radarr %v, sonarr %v", confirmed(radarr.ID), confirmed(sonarr.ID))
	}
	if r, _ := d.ArrInstances().Get(ctx, radarr.ID); !slices.Equal(r.ServerIDs, []int64{a.ID}) {
		t.Fatalf("links changed: %v", r.ServerIDs)
	}
	// Saving B again (enabled before) changes nothing; C declared shared storage does.
	radarr.LinksConfirmed = true
	must(t, d.ArrInstances().Update(ctx, radarr))
	b.Name = "B2"
	must(t, d.MediaServers().Update(ctx, b))
	if !confirmed(radarr.ID) {
		t.Fatal("an unrelated server edit unconfirmed the links")
	}
	c.Storage = ""
	must(t, d.MediaServers().Update(ctx, c))
	if confirmed(radarr.ID) || confirmed(sonarr.ID) {
		t.Fatal("declaring C shared storage kept the links confirmed")
	}
	// A new enabled server while only one is enabled changes nothing.
	d2 := newTestDB(t)
	x := &models.MediaServer{Name: "X", Kind: models.MediaServerPlex, URL: "http://x", Enabled: true}
	inst := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://r", Enabled: true, LinksConfirmed: true, ServerIDs: []int64{}}
	must(t, d2.ArrInstances().Create(ctx, inst))
	must(t, d2.MediaServers().Create(ctx, x))
	if got, _ := d2.ArrInstances().Get(ctx, inst.ID); !got.LinksConfirmed {
		t.Fatal("the first server unconfirmed the links")
	}
}
