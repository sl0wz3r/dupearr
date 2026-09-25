package backup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// A backup from before migration 0004 (several Plex servers, docs/DECISIONS.md D11) restores with
// its rows: the *arr links start unconfirmed (the fail-closed state; with two enabled servers the
// data upgrade stores none) and the groups have no cross-server record.
func TestRestoreBackupBeforeMultiServer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, name := range []string{"Plex A", "Plex B"} {
		ms := &models.MediaServer{Name: name, Kind: models.MediaServerPlex, URL: "http://" + name, Token: "t", Enabled: true}
		if err := f.db.MediaServers().Create(ctx, ms); err != nil {
			t.Fatal(err)
		}
	}
	radarr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878", APIKey: "k", Enabled: true}
	if err := f.db.ArrInstances().Create(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	queued, _, _ := seedRemovals(t, f.db)
	cfg, db := validParts(t, f)
	// The schema of a 0.1.x backup (scratch copy of the snapshot).
	old := modifyDB(t, db,
		`DROP TABLE arr_server_links`,
		`ALTER TABLE media_servers DROP COLUMN storage`,
		`ALTER TABLE arr_instances DROP COLUMN links_confirmed`,
		`ALTER TABLE duplicate_groups DROP COLUMN cross_server`,
		`DELETE FROM settings WHERE key = 'upgrade.arrServerLinks'`,
		`DELETE FROM schema_migrations WHERE version = 4`)
	stageArchive(t, f, cfg, old)
	if err := f.svc.ConfirmRestore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(f.dir); err != nil || !applied {
		t.Fatalf("apply = %v, %v", applied, err)
	}
	rdb, err := database.Open(ctx, filepath.Join(f.dir, dbFileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	arrs, err := rdb.ArrInstances().List(ctx)
	if err != nil || len(arrs) != 1 || arrs[0].LinksConfirmed || len(arrs[0].ServerIDs) != 0 {
		t.Fatalf("restored instances %+v %v", arrs, err)
	}
	servers, err := rdb.MediaServers().List(ctx)
	if err != nil || len(servers) != 2 || servers[0].Storage != "" {
		t.Fatalf("restored servers %+v %v", servers, err)
	}
	g, err := rdb.Groups().Get(ctx, queued)
	if err != nil || g.CrossServer != nil {
		t.Fatalf("restored group %+v %v", g, err)
	}
}

// The restore summary lists a change of the settings that decide cross-server protection (docs/
// DECISIONS.md D11): a server declared separate storage and confirmed *arr links, with two enabled
// servers. A one-server installation, and a backup from before migration 0004, list no link change
// when nothing that matters differs.
func TestRestoreSummaryListsStorageAndLinks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var servers []*models.MediaServer
	for _, name := range []string{"Plex A", "Plex B"} {
		ms := &models.MediaServer{Name: name, Kind: models.MediaServerPlex, URL: "http://" + name, Token: "t", Enabled: true}
		if err := f.db.MediaServers().Create(ctx, ms); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, ms)
	}
	radarr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878", APIKey: "k", Enabled: true,
		ServerIDs: []int64{servers[0].ID, servers[1].ID}, LinksConfirmed: true}
	if err := f.db.ArrInstances().Create(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	servers[1].Storage = models.StorageSeparate
	if err := f.db.MediaServers().Update(ctx, servers[1]); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)
	// The live instance: B shares storage, Radarr's links are not confirmed.
	servers[1].Storage = ""
	if err := f.db.MediaServers().Update(ctx, servers[1]); err != nil {
		t.Fatal(err)
	}
	radarr.LinksConfirmed = false
	if err := f.db.ArrInstances().Update(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	sum := stageArchive(t, f, cfg, db)
	if c := changeOf(sum, "mediaServers"); c == nil || !strings.Contains(c.Backup, "Plex B → http://Plex B (separate storage)") ||
		strings.Contains(c.Current, "separate") {
		t.Fatalf("mediaServers change = %+v (changes %+v)", c, sum.Changes)
	}
	if c := changeOf(sum, "arrInstances"); c == nil || !strings.Contains(c.Backup, "(feeds Plex A, Plex B, confirmed)") ||
		strings.Contains(c.Current, "confirmed") {
		t.Fatalf("arrInstances change = %+v (changes %+v)", c, sum.Changes)
	}
}

func TestRestoreSummaryOneServerAndOldBackupListNoLinks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ms := &models.MediaServer{Name: "Plex", Kind: models.MediaServerPlex, URL: "http://plex:32400", Token: "t", Enabled: true}
	if err := f.db.MediaServers().Create(ctx, ms); err != nil {
		t.Fatal(err)
	}
	radarr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878", APIKey: "k", Enabled: true,
		ServerIDs: []int64{ms.ID}, LinksConfirmed: true}
	if err := f.db.ArrInstances().Create(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)
	old := modifyDB(t, db,
		`DROP TABLE arr_server_links`,
		`ALTER TABLE media_servers DROP COLUMN storage`,
		`ALTER TABLE arr_instances DROP COLUMN links_confirmed`,
		`ALTER TABLE duplicate_groups DROP COLUMN cross_server`,
		`DELETE FROM schema_migrations WHERE version = 4`)
	for _, snapshot := range [][]byte{db, old} {
		sum := stageArchive(t, f, cfg, snapshot)
		for _, name := range []string{"mediaServers", "arrInstances"} {
			if c := changeOf(sum, name); c != nil {
				t.Fatalf("%s change listed: %+v", name, c)
			}
		}
	}
}
