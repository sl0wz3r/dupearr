package backup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Tautulli connections receive their API key with every scan (docs/DECISIONS.md D10): a restore
// that changes where one points is listed in the summary like the other connections.
func TestRestoreSummaryListsTautulliDestinations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ms := &models.MediaServer{Name: "Plex", Kind: models.MediaServerPlex, URL: "http://plex:32400", Token: "t",
		MachineIdentifier: "mid", Enabled: true}
	if err := f.db.MediaServers().Create(ctx, ms); err != nil {
		t.Fatal(err)
	}
	tt := &models.TautulliInstance{Name: "Tautulli", ServerID: ms.ID, URL: "http://10.9.9.9:8181", APIKey: "k3y-secret", Enabled: true}
	if err := f.db.Tautullis().Create(ctx, tt); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)
	tt.URL = "http://tautulli.lan:8181"
	if err := f.db.Tautullis().Update(ctx, tt); err != nil {
		t.Fatal(err)
	}
	sum := stageArchive(t, f, cfg, db)
	c := changeOf(sum, "tautulliInstances")
	if c == nil || !strings.Contains(c.Backup, "10.9.9.9") || !strings.Contains(c.Current, "tautulli.lan") || !c.Applied {
		t.Fatalf("tautulliInstances change = %+v (changes %+v)", c, sum.Changes)
	}
	if strings.Contains(c.Backup+c.Current, "k3y-secret") {
		t.Fatalf("the summary shows the API key: %+v", c)
	}
}

// TestRestoreBackupWithoutTautulliTable: a backup from before migration 0003 is restored (the
// table starts empty) and its summary lists the current connection as going away.
func TestRestoreBackupWithoutTautulliTable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	queued, _, _ := seedRemovals(t, f.db)
	cfg, db := validParts(t, f)
	old := modifyDB(t, db, `DROP TABLE tautulli_instances`, `DELETE FROM schema_migrations WHERE version = 3`)
	ms := &models.MediaServer{Name: "Plex", Kind: models.MediaServerPlex, URL: "http://plex:32400", Token: "t", Enabled: true}
	if err := f.db.MediaServers().Create(ctx, ms); err != nil {
		t.Fatal(err)
	}
	live := &models.TautulliInstance{Name: "Live", ServerID: ms.ID, URL: "http://t:8181", APIKey: "k", Enabled: true}
	if err := f.db.Tautullis().Create(ctx, live); err != nil {
		t.Fatal(err)
	}
	sum := stageArchive(t, f, cfg, old)
	if c := changeOf(sum, "tautulliInstances"); c == nil || c.Backup != "" || !strings.Contains(c.Current, "Live") {
		t.Fatalf("tautulliInstances change = %+v", c)
	}
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
	list, err := rdb.Tautullis().List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("restored connections = %+v, %v", list, err)
	}
	if g, err := rdb.Groups().Get(ctx, queued); err != nil || g.Title == "" {
		t.Fatalf("restored group = %+v, %v", g, err)
	}
}
