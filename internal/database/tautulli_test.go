package database

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Tautulli connections (docs/DECISIONS.md D10): one per media server, deleted with it.
func TestTautullis_CRUDAndCascade(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Tautullis()
	srv := newServer(t, d, "Plex")
	other := newServer(t, d, "Other")

	list, err := r.List(ctx)
	must(t, err)
	if list == nil || len(list) != 0 {
		t.Fatalf("empty List = %#v", list)
	}
	tt := &models.TautulliInstance{Name: "Tautulli", ServerID: srv.ID, URL: "http://tautulli:8181", APIKey: "k1",
		VerifyTLS: true, Enabled: true}
	must(t, r.Create(ctx, tt))
	if tt.ID == 0 || tt.CreatedAt.IsZero() || tt.UpdatedAt.IsZero() {
		t.Fatalf("Create did not set ID/times: %+v", tt)
	}
	got, err := r.Get(ctx, tt.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, *tt) {
		t.Fatalf("Get = %+v, want %+v", got, tt)
	}

	// A second connection for the same server is refused by the schema.
	dup := &models.TautulliInstance{Name: "Again", ServerID: srv.ID, URL: "http://other:8181", APIKey: "k2", Enabled: true}
	if err := r.Create(ctx, dup); err == nil {
		t.Fatal("a second Tautulli for the same media server was stored")
	}

	tt.Name, tt.APIKey, tt.VerifyTLS, tt.Enabled, tt.URL = "Renamed", "k3", false, false, "https://t.example/tautulli"
	must(t, r.Update(ctx, tt))
	got, err = r.Get(ctx, tt.ID)
	must(t, err)
	if got.Name != "Renamed" || got.APIKey != "k3" || got.VerifyTLS || got.Enabled || got.URL != "https://t.example/tautulli" {
		t.Fatalf("after Update: %+v", got)
	}
	if err := r.Update(ctx, &models.TautulliInstance{ID: 999, Name: "x", ServerID: srv.ID, URL: "http://x"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update of a missing row: %v", err)
	}

	second := &models.TautulliInstance{Name: "Other", ServerID: other.ID, URL: "http://t2:8181", APIKey: "k4", Enabled: true}
	must(t, r.Create(ctx, second))

	// Deleting the media server deletes its Tautulli connection, and only that one.
	must(t, d.MediaServers().Delete(ctx, srv.ID))
	if _, err := r.Get(ctx, tt.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("connection of the deleted server: %v", err)
	}
	list, err = r.List(ctx)
	must(t, err)
	if len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("List = %+v", list)
	}
	must(t, r.Delete(ctx, second.ID))
	if err := r.Delete(ctx, second.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Delete: %v", err)
	}
}

// TestWatchInfoRoundTrips: the play history a scan read is stored with the version, which is what
// re-evaluations work from until the next scan.
func TestWatchInfoRoundTrips(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 2, 1)
	g.Files[0].Version.Watch = &models.WatchInfo{Source: models.WatchSourceTautulli, SourceName: "Tautulli",
		Status: models.WatchKnown, Plays: 3, Users: 2}
	g.Files[1].Version.Watch = &models.WatchInfo{Source: models.WatchSourceTautulli, Status: models.WatchFailed, Reason: "HTTP 503"}
	if _, err := d.Groups().Upsert(ctx, g); err != nil {
		t.Fatal(err)
	}
	got, err := d.Groups().Get(ctx, g.ID)
	must(t, err)
	for i := range got.Files {
		if !reflect.DeepEqual(got.Files[i].Version.Watch, g.Files[i].Version.Watch) {
			t.Fatalf("file %d: watch %+v, want %+v", i, got.Files[i].Version.Watch, g.Files[i].Version.Watch)
		}
	}
}
