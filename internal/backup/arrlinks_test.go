package backup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// A backup from before migration 0005 (links to Radarr/Sonarr) restores with its rows: no External
// URL (links use the connection URL) and groups without *arr items until their next scan.
func TestRestoreBackupBeforeArrLinks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	radarr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878", APIKey: "k", Enabled: true}
	if err := f.db.ArrInstances().Create(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	queued, _, _ := seedRemovals(t, f.db)
	cfg, db := validParts(t, f)
	old := modifyDB(t, db,
		`ALTER TABLE arr_instances DROP COLUMN external_url`,
		`ALTER TABLE duplicate_groups DROP COLUMN arr_items`,
		`DELETE FROM schema_migrations WHERE version = 5`)
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
	if err != nil || len(arrs) != 1 || arrs[0].ExternalURL != "" || arrs[0].URL != "http://radarr:7878" {
		t.Fatalf("restored instances %+v %v", arrs, err)
	}
	g, err := rdb.Groups().Get(ctx, queued)
	if err != nil || g.ArrItems != nil {
		t.Fatalf("restored group %+v %v", g, err)
	}
}

// A restore that changes where the "Open in Radarr" links lead is listed in the summary (the
// External URL is never requested, but a link to another site must not arrive unnoticed).
func TestRestoreSummaryListsExternalURL(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	radarr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878", APIKey: "k", Enabled: true,
		ExternalURL: "https://elsewhere.example.com"}
	if err := f.db.ArrInstances().Create(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)
	radarr.ExternalURL = ""
	if err := f.db.ArrInstances().Update(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	sum := stageArchive(t, f, cfg, db)
	c := changeOf(sum, "arrInstances")
	if c == nil || !strings.Contains(c.Backup, "radarr Radarr → http://radarr:7878 (links open https://elsewhere.example.com)") ||
		strings.Contains(c.Current, "links open") {
		t.Fatalf("arrInstances change = %+v (changes %+v)", c, sum.Changes)
	}
	// The same External URL on both sides lists nothing.
	radarr.ExternalURL = "https://elsewhere.example.com"
	if err := f.db.ArrInstances().Update(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	if c := changeOf(stageArchive(t, f, cfg, db), "arrInstances"); c != nil {
		t.Fatalf("unchanged instance listed: %+v", c)
	}
}
