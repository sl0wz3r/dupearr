package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// ---------------------------------------------------------------------------
// Media servers
// ---------------------------------------------------------------------------

func newServer(t *testing.T, d *DB, name string) *models.MediaServer {
	t.Helper()
	s := &models.MediaServer{Name: name, Kind: models.MediaServerPlex, URL: "http://plex:32400", Token: "tok",
		MachineIdentifier: "mid", VerifyTLS: true, Enabled: true}
	must(t, d.MediaServers().Create(context.Background(), s))
	return s
}

func TestMediaServers_CRUD(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.MediaServers()

	list, err := r.List(ctx)
	must(t, err)
	if list == nil || len(list) != 0 {
		t.Fatalf("empty List = %#v", list)
	}

	s := newServer(t, d, "Plex")
	if s.ID == 0 || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		t.Fatalf("Create did not set ID/times: %+v", s)
	}
	got, err := r.Get(ctx, s.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, *s) {
		t.Fatalf("Get = %+v, want %+v", got, s)
	}

	s.Name, s.Token, s.VerifyTLS, s.Enabled = "Plex 2", "tok2", false, false
	created := s.CreatedAt
	must(t, r.Update(ctx, s))
	got, err = r.Get(ctx, s.ID)
	must(t, err)
	if got.Name != "Plex 2" || got.Token != "tok2" || got.VerifyTLS || got.Enabled || !got.CreatedAt.Equal(created) {
		t.Fatalf("after Update: %+v", got)
	}

	newServer(t, d, "Second")
	list, err = r.List(ctx)
	must(t, err)
	if len(list) != 2 || list[0].ID != s.ID {
		t.Fatalf("List = %+v", list)
	}

	wantNotFound(t, r.Update(ctx, &models.MediaServer{ID: 999}))
	_, err = r.Get(ctx, 999)
	wantNotFound(t, err)
	wantNotFound(t, r.Delete(ctx, 999))
}

func TestMediaServers_DeleteCascades(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	s1 := newServer(t, d, "one")
	s2 := newServer(t, d, "two")
	arr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878"}
	must(t, d.ArrInstances().Create(ctx, arr))
	if arr.ID != s1.ID {
		t.Fatalf("test assumes equal ids for the arr instance and server (got %d/%d)", arr.ID, s1.ID)
	}

	_, err := d.Libraries().Sync(ctx, s1.ID, []models.Library{{SectionKey: "1", Title: "Movies"}})
	must(t, err)
	_, err = d.Libraries().Sync(ctx, s2.ID, []models.Library{{SectionKey: "1", Title: "Movies"}})
	must(t, err)
	mappings := []*models.PathMapping{
		{SourceType: models.PathSourceServer, SourceID: s1.ID, RemotePath: "/a", LocalPath: "/b"},
		{SourceType: models.PathSourceServer, SourceID: s2.ID, RemotePath: "/a", LocalPath: "/b"},
		{SourceType: models.PathSourceArr, SourceID: arr.ID, RemotePath: "/a", LocalPath: "/b"}, // same numeric id
	}
	for _, m := range mappings {
		must(t, d.PathMappings().Create(ctx, m))
	}
	g1, g2 := testGroup("movie:1", 1, 1), testGroup("movie:2", 1, 1)
	g1.ServerID, g2.ServerID = s1.ID, s2.ID
	upsert(t, d, g1)
	upsert(t, d, g2)
	queued1, queued2 := queueRemoval(t, d, g1, 1), queueRemoval(t, d, g2, 1)

	must(t, d.MediaServers().Delete(ctx, s1.ID))

	libs, err := d.Libraries().List(ctx)
	must(t, err)
	if len(libs) != 1 || libs[0].ServerID != s2.ID {
		t.Fatalf("libraries after delete = %+v", libs)
	}
	left, err := d.PathMappings().List(ctx)
	must(t, err)
	if len(left) != 2 || left[0].ID != mappings[1].ID || left[1].ID != mappings[2].ID {
		t.Fatalf("path mappings after delete = %+v", left)
	}
	// Groups survive (user decisions), but nothing is removed through a deleted server.
	if _, err := d.Groups().Get(ctx, g1.ID); err != nil {
		t.Fatalf("group of the deleted server: %v", err)
	}
	if got := actionStatus(t, d, queued1); got != models.ActionCancelled {
		t.Fatalf("removal via the deleted server = %q, want cancelled", got)
	}
	if got := actionStatus(t, d, queued2); got != models.ActionPending {
		t.Fatalf("removal via the other server = %q, want pending", got)
	}
}

// ---------------------------------------------------------------------------
// Libraries
// ---------------------------------------------------------------------------

func TestLibraries_Sync(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	s := newServer(t, d, "Plex")
	p := &models.Profile{Name: "P"}
	must(t, d.Profiles().Create(ctx, p))
	r := d.Libraries()

	libs, err := r.Sync(ctx, s.ID, []models.Library{
		{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{"/data/movies"}},
		{SectionKey: "2", Title: "TV", Type: "show", Enabled: false},
		{SectionKey: "3", Title: "4K", Type: "movie", ScopeGroup: "movies"},
	})
	must(t, err)
	if len(libs) != 3 {
		t.Fatalf("Sync returned %d libraries", len(libs))
	}
	byKey := map[string]models.Library{}
	for _, l := range libs {
		byKey[l.SectionKey] = l
		if !l.Enabled || l.ServerID != s.ID || l.Locations == nil {
			t.Fatalf("new library %+v: want enabled, server %d, non-nil locations", l, s.ID)
		}
	}
	if byKey["3"].ScopeGroup != "movies" {
		t.Fatalf("new library lost its scope group: %+v", byKey["3"])
	}

	// User customizes library 1.
	lib1 := byKey["1"]
	lib1.Enabled, lib1.ProfileID, lib1.ScopeGroup = false, &p.ID, "movies"
	must(t, r.Update(ctx, &lib1))

	// Plex renames 1, drops 2, keeps 3, adds 4.
	libs, err = r.Sync(ctx, s.ID, []models.Library{
		{SectionKey: "1", Title: "Films", Type: "movie", Locations: []string{"/data/films"}, Enabled: true, ScopeGroup: "x"},
		{SectionKey: "3", Title: "4K", Type: "movie"},
		{SectionKey: "4", Title: "Anime", Type: "show"},
	})
	must(t, err)
	byKey = map[string]models.Library{}
	for _, l := range libs {
		byKey[l.SectionKey] = l
	}
	if len(libs) != 3 || byKey["2"].ID != 0 {
		t.Fatalf("after re-sync: %+v", libs)
	}
	got := byKey["1"]
	if got.ID != lib1.ID || got.Title != "Films" || !reflect.DeepEqual(got.Locations, []string{"/data/films"}) {
		t.Fatalf("server fields not updated: %+v", got)
	}
	if got.Enabled || got.ProfileID == nil || *got.ProfileID != p.ID || got.ScopeGroup != "movies" {
		t.Fatalf("user fields not preserved: %+v", got)
	}
	if byKey["3"].ScopeGroup != "movies" {
		t.Fatalf("existing scope group overwritten: %+v", byKey["3"])
	}
	if !byKey["4"].Enabled {
		t.Fatal("new library not enabled")
	}
	// Returned order is by title.
	if libs[0].Title != "4K" || libs[1].Title != "Anime" || libs[2].Title != "Films" {
		t.Fatalf("order = %v %v %v", libs[0].Title, libs[1].Title, libs[2].Title)
	}

	// Another server's libraries are unaffected; an empty sync removes all of this server's.
	s2 := newServer(t, d, "Other")
	_, err = r.Sync(ctx, s2.ID, []models.Library{{SectionKey: "1", Title: "Other"}})
	must(t, err)
	libs, err = r.Sync(ctx, s.ID, nil)
	must(t, err)
	if libs == nil || len(libs) != 0 {
		t.Fatalf("empty sync returned %#v", libs)
	}
	all, err := r.List(ctx)
	must(t, err)
	if len(all) != 1 || all[0].ServerID != s2.ID {
		t.Fatalf("List = %+v", all)
	}
	byServer, err := r.ListByServer(ctx, s2.ID)
	must(t, err)
	if len(byServer) != 1 {
		t.Fatalf("ListByServer = %+v", byServer)
	}
}

func TestLibraries_SyncValidation(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	s := newServer(t, d, "Plex")
	tests := []struct {
		name     string
		serverID int64
		sections []models.Library
	}{
		{"empty section key", s.ID, []models.Library{{SectionKey: " "}}},
		{"duplicate section key", s.ID, []models.Library{{SectionKey: "1"}, {SectionKey: "1"}}},
		{"unknown server", 999, []models.Library{{SectionKey: "1"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := d.Libraries().Sync(ctx, tt.serverID, tt.sections); err == nil {
				t.Fatal("Sync succeeded")
			}
		})
	}
	libs, err := d.Libraries().List(ctx)
	must(t, err)
	if len(libs) != 0 {
		t.Fatalf("failed syncs left %d libraries", len(libs))
	}
}

func TestLibraries_GetUpdate(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	s := newServer(t, d, "Plex")
	libs, err := d.Libraries().Sync(ctx, s.ID, []models.Library{{SectionKey: "1", Title: "Movies"}})
	must(t, err)
	l := libs[0]
	got, err := d.Libraries().Get(ctx, l.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, l) {
		t.Fatalf("Get = %+v, want %+v", got, l)
	}

	missingProfile := int64(999)
	l.ProfileID = &missingProfile
	if err := d.Libraries().Update(ctx, &l); !errors.Is(err, ErrConstraint) {
		t.Fatalf("Update with unknown profile: %v, want ErrConstraint", err)
	}
	_, err = d.Libraries().Get(ctx, 999)
	wantNotFound(t, err)
	wantNotFound(t, d.Libraries().Update(ctx, &models.Library{ID: 999}))

	// Deleting a profile resets libraries to the default profile.
	p := &models.Profile{Name: "Default"}
	must(t, d.Profiles().Create(ctx, p))
	other := &models.Profile{Name: "Other"}
	must(t, d.Profiles().Create(ctx, other))
	l.ProfileID = &other.ID
	must(t, d.Libraries().Update(ctx, &l))
	must(t, d.Profiles().Delete(ctx, other.ID))
	got, err = d.Libraries().Get(ctx, l.ID)
	must(t, err)
	if got.ProfileID != nil {
		t.Fatalf("ProfileID = %v after profile delete, want nil", *got.ProfileID)
	}
}

// ---------------------------------------------------------------------------
// *arr instances / path mappings / notifications
// ---------------------------------------------------------------------------

// TestArrInstances_DeleteCancelsQueue: removals planned with the data of an *arr instance are
// cancelled when the instance is removed from Dupearr (only groups with a version it tracks).
func TestArrInstances_DeleteCancelsQueue(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	radarr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878"}
	radarr4k := &models.ArrInstance{Name: "Radarr 4K", Kind: models.ArrRadarr, URL: "http://radarr4k:7878"}
	must(t, d.ArrInstances().Create(ctx, radarr))
	must(t, d.ArrInstances().Create(ctx, radarr4k))

	// keeperTracked: the keeper (not the removed version) is tracked by the deleted instance.
	keeperTracked := testGroup("movie:tmdb:1", 1, 1)
	keeperTracked.Files[0].Version.Arr = &models.ArrFileInfo{InstanceID: radarr.ID, Kind: models.ArrRadarr, FileID: 7}
	otherInstance := testGroup("movie:tmdb:2", 1, 1)
	otherInstance.Files[1].Version.Arr = &models.ArrFileInfo{InstanceID: radarr4k.ID, Kind: models.ArrRadarr, FileID: 8}
	untracked := testGroup("movie:tmdb:3", 1, 1)
	for _, g := range []*models.DuplicateGroup{keeperTracked, otherInstance, untracked} {
		upsert(t, d, g)
	}
	cancelled := queueRemoval(t, d, keeperTracked, 1)
	keptOther := queueRemoval(t, d, otherInstance, 1)
	keptUntracked := queueRemoval(t, d, untracked, 1)
	done := &models.Action{GroupID: keeperTracked.ID, Status: models.ActionSucceeded}
	must(t, d.Actions().Create(ctx, done))

	must(t, d.ArrInstances().Delete(ctx, radarr.ID))
	for a, want := range map[*models.Action]models.ActionStatus{
		cancelled: models.ActionCancelled, keptOther: models.ActionPending, keptUntracked: models.ActionPending,
		done: models.ActionSucceeded,
	} {
		got, err := d.Actions().Get(ctx, a.ID)
		must(t, err)
		if got.Status != want {
			t.Errorf("action %d (group %d) = %q, want %q", a.ID, a.GroupID, got.Status, want)
		}
		if want == models.ActionCancelled && got.Message != cancelArrMessage {
			t.Errorf("cancelled action message = %q", got.Message)
		}
	}
	// Groups themselves are kept.
	if _, err := d.Groups().Get(ctx, keeperTracked.ID); err != nil {
		t.Fatalf("group deleted with the instance: %v", err)
	}
}

func TestArrInstances_CRUDAndCascade(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.ArrInstances()
	a := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878", APIKey: "k",
		VerifyTLS: true, Enabled: true, Tags: []string{"4k"}}
	must(t, r.Create(ctx, a))
	b := &models.ArrInstance{Name: "Sonarr", Kind: models.ArrSonarr, URL: "http://sonarr:8989"}
	must(t, r.Create(ctx, b))
	if b.Tags == nil {
		t.Fatal("Create left Tags nil")
	}

	got, err := r.Get(ctx, a.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, *a) {
		t.Fatalf("Get = %+v, want %+v", got, a)
	}
	a.Tags, a.Enabled = nil, false
	must(t, r.Update(ctx, a))
	got, err = r.Get(ctx, a.ID)
	must(t, err)
	if got.Tags == nil || len(got.Tags) != 0 || got.Enabled {
		t.Fatalf("after Update: %+v", got)
	}

	s := newServer(t, d, "Plex") // server id 1 == arr id 1
	for _, m := range []*models.PathMapping{
		{SourceType: models.PathSourceArr, SourceID: a.ID, RemotePath: "/movies", LocalPath: "/data/movies"},
		{SourceType: models.PathSourceArr, SourceID: b.ID, RemotePath: "/tv", LocalPath: "/data/tv"},
		{SourceType: models.PathSourceServer, SourceID: s.ID, RemotePath: "/x", LocalPath: "/y"},
	} {
		must(t, d.PathMappings().Create(ctx, m))
	}
	must(t, r.Delete(ctx, a.ID))
	left, err := d.PathMappings().List(ctx)
	must(t, err)
	if len(left) != 2 || left[0].SourceID != b.ID || left[1].SourceType != models.PathSourceServer {
		t.Fatalf("mappings after delete = %+v", left)
	}
	list, err := r.List(ctx)
	must(t, err)
	if len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("List = %+v", list)
	}
	wantNotFound(t, r.Delete(ctx, a.ID))
	wantNotFound(t, r.Update(ctx, a))
}

func TestPathMappings_CRUD(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.PathMappings()
	m := &models.PathMapping{SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/media", LocalPath: "/data"}
	must(t, r.Create(ctx, m))
	got, err := r.Get(ctx, m.ID)
	must(t, err)
	if *got != *m {
		t.Fatalf("Get = %+v", got)
	}
	m.LocalPath = "/mnt/data"
	must(t, r.Update(ctx, m))
	got, err = r.Get(ctx, m.ID)
	must(t, err)
	if got.LocalPath != "/mnt/data" {
		t.Fatalf("after Update: %+v", got)
	}
	for _, bad := range []string{"", "plex", "Server"} {
		if err := r.Create(ctx, &models.PathMapping{SourceType: bad}); err == nil {
			t.Errorf("Create with source type %q succeeded", bad)
		}
		if err := r.Update(ctx, &models.PathMapping{ID: m.ID, SourceType: bad}); err == nil {
			t.Errorf("Update with source type %q succeeded", bad)
		}
	}
	must(t, r.Delete(ctx, m.ID))
	_, err = r.Get(ctx, m.ID)
	wantNotFound(t, err)
	wantNotFound(t, r.Delete(ctx, m.ID))
	wantNotFound(t, r.Update(ctx, m))
}

func TestNotifications_CRUD(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Notifications()
	n := &models.NotificationConfig{Name: "Discord", Kind: "discord",
		Settings: json.RawMessage(`{"webhookUrl":"https://example.invalid/x"}`),
		Triggers: []string{models.OnFileDeleted}, Enabled: true}
	must(t, r.Create(ctx, n))
	got, err := r.Get(ctx, n.ID)
	must(t, err)
	if got.Name != "Discord" || string(got.Settings) != `{"webhookUrl":"https://example.invalid/x"}` ||
		!reflect.DeepEqual(got.Triggers, n.Triggers) || !got.Enabled {
		t.Fatalf("Get = %+v", got)
	}

	// Nil settings/triggers are stored as {} / [].
	n.Settings, n.Triggers, n.Enabled = nil, nil, false
	must(t, r.Update(ctx, n))
	got, err = r.Get(ctx, n.ID)
	must(t, err)
	b, _ := json.Marshal(got)
	if want := fmt.Sprintf(`{"id":%d,"name":"Discord","kind":"discord","settings":{},"triggers":[],"enabled":false}`, n.ID); string(b) != want {
		t.Fatalf("JSON = %s, want %s", b, want)
	}

	if err := r.Create(ctx, &models.NotificationConfig{Name: "bad", Settings: json.RawMessage(`{nope`)}); err == nil {
		t.Fatal("invalid settings JSON accepted")
	}
	list, err := r.List(ctx)
	must(t, err)
	if len(list) != 1 {
		t.Fatalf("List = %+v", list)
	}
	must(t, r.Delete(ctx, n.ID))
	wantNotFound(t, r.Delete(ctx, n.ID))
	wantNotFound(t, r.Update(ctx, n))
	_, err = r.Get(ctx, n.ID)
	wantNotFound(t, err)
}

// ---------------------------------------------------------------------------
// Profiles
// ---------------------------------------------------------------------------

func defaultName(t *testing.T, d *DB) string {
	t.Helper()
	p, err := d.Profiles().GetDefault(context.Background())
	must(t, err)
	list, err := d.Profiles().List(context.Background())
	must(t, err)
	n := 0
	for _, x := range list {
		if x.IsDefault {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d default profiles, want exactly 1", n)
	}
	return p.Name
}

func TestProfiles_DefaultRules(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Profiles()

	_, err := r.GetDefault(ctx)
	wantNotFound(t, err)

	// The first profile becomes the default even when not flagged.
	a := &models.Profile{Name: "A", KeepCount: 1,
		Criteria:    []models.Criterion{{Type: models.CritResolution, Enabled: true, Order: []string{"2160"}}},
		Protections: []models.Protection{{Type: models.ProtectArrTag, Value: "dupearr-keep"}}}
	must(t, r.Create(ctx, a))
	if !a.IsDefault || a.ID == 0 || a.CreatedAt.IsZero() {
		t.Fatalf("first profile = %+v", a)
	}
	if defaultName(t, d) != "A" {
		t.Fatal("A is not the default")
	}

	// A non-default profile does not steal the flag.
	b := &models.Profile{Name: "B"}
	must(t, r.Create(ctx, b))
	if b.IsDefault || defaultName(t, d) != "A" {
		t.Fatal("B became default")
	}
	if b.Criteria == nil || b.Protections == nil {
		t.Fatal("Create left nil slices on the caller's profile")
	}

	// Creating a default profile clears the previous one.
	c := &models.Profile{Name: "C", IsDefault: true}
	must(t, r.Create(ctx, c))
	if defaultName(t, d) != "C" {
		t.Fatal("C is not the default")
	}

	// Updating IsDefault=true clears the others.
	b.IsDefault = true
	b.KeepCount = 2
	must(t, r.Update(ctx, b))
	if defaultName(t, d) != "B" {
		t.Fatal("B is not the default after Update")
	}
	got, err := r.Get(ctx, b.ID)
	must(t, err)
	if got.KeepCount != 2 {
		t.Fatalf("Update not applied: %+v", got)
	}

	// Clearing IsDefault on the default is ignored: there must always be one.
	b.IsDefault = false
	must(t, r.Update(ctx, b))
	if !b.IsDefault || defaultName(t, d) != "B" {
		t.Fatal("the default profile lost its flag")
	}

	// The default cannot be deleted; others can.
	err = r.Delete(ctx, b.ID)
	if !errors.Is(err, ErrDefaultProfile) {
		t.Fatalf("Delete(default) = %v, want ErrDefaultProfile", err)
	}
	must(t, r.Delete(ctx, a.ID))
	wantNotFound(t, r.Delete(ctx, a.ID))
	wantNotFound(t, r.Update(ctx, &models.Profile{ID: a.ID, Name: "gone"}))
	_, err = r.Get(ctx, a.ID)
	wantNotFound(t, err)

	list, err := r.List(ctx)
	must(t, err)
	if len(list) != 2 || list[0].Name != "B" || list[1].Name != "C" {
		t.Fatalf("List = %+v", list)
	}
}

func TestProfiles_RoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	created := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	p := &models.Profile{
		Name:      "Keep Highest Quality",
		IsDefault: true,
		KeepCount: 1,
		KeepPer:   models.KeepPerResolution,
		CreatedAt: created,
		Criteria: []models.Criterion{
			{Type: models.CritHealth, Enabled: true},
			{Type: models.CritCustomFormatScore, Enabled: true, Direction: models.DirectionHigher, MinDelta: 10},
			{Type: models.CritVideoBitrate, Enabled: false, Direction: models.DirectionHigher, TolerancePercent: 15},
			{Type: models.CritFilenameScore, Enabled: true, Patterns: []models.PatternScore{{Pattern: "**/*REMUX*", Score: 10}}},
		},
		Protections: []models.Protection{{Type: models.ProtectPathGlob, Value: "/data/keep/**"}},
	}
	must(t, d.Profiles().Create(ctx, p))
	if !p.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt overwritten: %v", p.CreatedAt)
	}
	got, err := d.Profiles().Get(ctx, p.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, *p) {
		t.Fatalf("Get = %+v\nwant  %+v", got, p)
	}

	// Update reports the stored CreatedAt, whatever the caller sent (e.g. an API body without it).
	upd := *got
	upd.CreatedAt, upd.Name = time.Time{}, "Renamed"
	must(t, d.Profiles().Update(ctx, &upd))
	if !upd.CreatedAt.Equal(created) || upd.UpdatedAt.Before(got.UpdatedAt) {
		t.Fatalf("after Update CreatedAt = %v, UpdatedAt = %v", upd.CreatedAt, upd.UpdatedAt)
	}
	got, err = d.Profiles().Get(ctx, p.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, upd) {
		t.Fatalf("Get after Update = %+v\nwant  %+v", got, upd)
	}
}

// TestProfiles_DeleteResetsLibraries: libraries using a deleted profile fall back to the default
// profile (ProfileID nil) instead of blocking the delete or pointing at a missing profile.
func TestProfiles_DeleteResetsLibraries(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	def := &models.Profile{Name: "Default", IsDefault: true}
	custom := &models.Profile{Name: "Custom"}
	must(t, d.Profiles().Create(ctx, def))
	must(t, d.Profiles().Create(ctx, custom))
	s := newServer(t, d, "Plex")
	libs, err := d.Libraries().Sync(ctx, s.ID, []models.Library{{SectionKey: "1"}, {SectionKey: "2"}})
	must(t, err)
	libs[0].ProfileID, libs[1].ProfileID = &custom.ID, &def.ID
	must(t, d.Libraries().Update(ctx, &libs[0]))
	must(t, d.Libraries().Update(ctx, &libs[1]))

	must(t, d.Profiles().Delete(ctx, custom.ID))
	got, err := d.Libraries().Get(ctx, libs[0].ID)
	must(t, err)
	if got.ProfileID != nil {
		t.Fatalf("library still references the deleted profile: %d", *got.ProfileID)
	}
	got, err = d.Libraries().Get(ctx, libs[1].ID)
	must(t, err)
	if got.ProfileID == nil || *got.ProfileID != def.ID {
		t.Fatalf("unrelated library lost its profile: %v", got.ProfileID)
	}
}
