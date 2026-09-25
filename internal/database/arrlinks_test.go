package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Links to Radarr/Sonarr and explained queue deferrals: migration 0005,
// arr_instances.external_url and duplicate_groups.arr_items.

func TestMigration0005OnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dupearr.db")
	openAtVersion(t, path, 4)
	raw, err := sql.Open(driverName, dsn(path, false))
	must(t, err)
	now := fmtTime(nowUTC())
	_, err = raw.ExecContext(ctx, `INSERT INTO arr_instances (name, kind, url, created_at, updated_at) VALUES ('Radarr', 'radarr', 'http://r', ?, ?)`, now, now)
	must(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO duplicate_groups (key, status, first_seen_at, last_seen_at, updated_at) VALUES ('movie:tmdb:1', 'deferred', ?, ?, ?)`, now, now, now)
	must(t, err)
	must(t, raw.Close())

	d := openAt(t, path)
	arrs, err := d.ArrInstances().List(ctx)
	must(t, err)
	if len(arrs) != 1 || arrs[0].ExternalURL != "" || arrs[0].URL != "http://r" {
		t.Fatalf("instances %+v", arrs)
	}
	g, err := d.Groups().GetByKey(ctx, "movie:tmdb:1")
	must(t, err)
	if g.ArrItems != nil {
		t.Fatalf("a group stored before 0005 has arr items: %+v", g.ArrItems)
	}
}

func TestArrInstanceExternalURLRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	a := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878", ExternalURL: "https://radarr.example.com", Enabled: true}
	must(t, d.ArrInstances().Create(ctx, a))
	got, err := d.ArrInstances().Get(ctx, a.ID)
	must(t, err)
	if got.ExternalURL != "https://radarr.example.com" {
		t.Fatalf("created external URL %q", got.ExternalURL)
	}
	got.ExternalURL = ""
	must(t, d.ArrInstances().Update(ctx, got))
	list, err := d.ArrInstances().List(ctx)
	must(t, err)
	if len(list) != 1 || list[0].ExternalURL != "" {
		t.Fatalf("after clearing: %+v", list)
	}
}

func TestGroupArrItemsRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	items := []models.ArrItemRef{
		{InstanceID: 1, InstanceName: "Radarr", Kind: models.ArrRadarr, ItemID: 9, TitleSlug: "1084244", QueueCount: 2,
			Queue: []models.ArrQueueEntry{{Title: "Toy.Story.5.2026.2160p", Status: "completed", TrackedDownloadState: "importPending",
				TrackedDownloadStatus: "warning", Label: "Downloaded - Waiting to Import", Messages: []string{"Not an upgrade"}}}},
		{InstanceID: 2, InstanceName: "Radarr 4K", Kind: models.ArrRadarr, ItemID: 4},
	}
	g := testGroup("movie:tmdb:1", 1, 1)
	g.ArrItems = items
	upsert(t, d, g)
	got := getGroup(t, d, g.ID)
	want := append([]models.ArrItemRef(nil), items...)
	if !reflect.DeepEqual(got.ArrItems, want) {
		t.Fatalf("Get arrItems = %+v, want %+v", got.ArrItems, want)
	}
	page, err := d.Groups().List(ctx, store.GroupFilter{}, store.Paging{Page: 1, PageSize: 10})
	must(t, err)
	if len(page.Records) != 1 || !reflect.DeepEqual(page.Records[0].ArrItems, want) {
		t.Fatalf("List arrItems = %+v", page.Records)
	}

	// nil clears them (a scan that found no *arr item).
	got.ArrItems = nil
	upsert(t, d, got)
	if g2 := getGroup(t, d, g.ID); g2.ArrItems != nil {
		t.Fatalf("after clearing: %+v", g2.ArrItems)
	}
	var stored string
	must(t, d.r.QueryRowContext(ctx, `SELECT arr_items FROM duplicate_groups WHERE id = ?`, g.ID).Scan(&stored))
	if stored != "" {
		t.Fatalf("stored %q, want ''", stored)
	}
}

// arr_items is display only: a value that cannot be read (a tampered or corrupt restored backup)
// never fails reading the group, and a readable one is capped again.
func TestGroupArrItemsFromUntrustedDatabase(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	upsert(t, d, g)
	for _, bad := range []string{`{not json`, `"a string"`, `[{"instanceId":"x"}]`, `[1,2,3]`} {
		_, err := d.w.ExecContext(ctx, `UPDATE duplicate_groups SET arr_items = ? WHERE id = ?`, bad, g.ID)
		must(t, err)
		got, err := d.Groups().Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("arr_items %q failed the read: %v", bad, err)
		}
		if got.ArrItems != nil || len(got.Files) != 2 {
			t.Fatalf("arr_items %q: items %+v, files %d", bad, got.ArrItems, len(got.Files))
		}
	}
	huge := `[{"instanceId":1,"itemId":2,"instanceName":"` + strings.Repeat("n", 5000) + `","queue":[{"title":"` + strings.Repeat("t", 5000) + `"}]}]`
	_, err := d.w.ExecContext(ctx, `UPDATE duplicate_groups SET arr_items = ? WHERE id = ?`, huge, g.ID)
	must(t, err)
	got := getGroup(t, d, g.ID)
	if len(got.ArrItems) != 1 || len([]rune(got.ArrItems[0].InstanceName)) > 100 ||
		len([]rune(got.ArrItems[0].Queue[0].Title)) != models.MaxArrQueueTitleRunes || got.ArrItems[0].QueueCount != 1 {
		t.Fatalf("capped items = %+v", got.ArrItems)
	}
}
