package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const gib = int64(1) << 30

func testFile(versionKey, ratingKey string, size int64, d models.Decision) models.GroupFile {
	return models.GroupFile{
		Version: models.MediaVersion{
			Key:        versionKey,
			ServerID:   1,
			RatingKey:  ratingKey,
			Parts:      []models.MediaPart{{ID: 1, Path: "/media/" + versionKey + ".mkv", Size: size}},
			Resolution: models.Res1080,
		},
		Decision:       d,
		EngineDecision: d,
		Reasons:        []string{"because"},
		Values:         map[string]string{"resolution": "1080p"},
	}
}

// testGroup builds a pending movie group of two versions (keep 4 GiB, remove 2 GiB).
func testGroup(key string, libraryID, scanID int64) *models.DuplicateGroup {
	return &models.DuplicateGroup{
		Key:              key,
		MediaType:        models.MediaTypeMovie,
		Title:            "Movie " + key,
		Year:             2020,
		ServerID:         1,
		LibraryIDs:       []int64{libraryID},
		ExternalIDs:      map[string]string{"tmdb": "1"},
		Status:           models.GroupPending,
		ProfileID:        1,
		LastScanID:       scanID,
		ReclaimableBytes: 2 * gib,
		Files: []models.GroupFile{
			testFile(key+"/a", "rk-"+key, 4*gib, models.DecisionKeep),
			testFile(key+"/b", "rk-"+key, 2*gib, models.DecisionRemove),
		},
	}
}

func upsert(t *testing.T, d *DB, g *models.DuplicateGroup) bool {
	t.Helper()
	created, err := d.Groups().Upsert(context.Background(), g)
	if err != nil {
		t.Fatalf("Upsert(%q): %v", g.Key, err)
	}
	return created
}

func getGroup(t *testing.T, d *DB, id int64) *models.DuplicateGroup {
	t.Helper()
	g, err := d.Groups().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	return g
}

func fileByKey(t *testing.T, g *models.DuplicateGroup, key string) models.GroupFile {
	t.Helper()
	for _, f := range g.Files {
		if f.Version.Key == key {
			return f
		}
	}
	t.Fatalf("group %q has no file %q", g.Key, key)
	return models.GroupFile{}
}

func groupKeys(groups []models.DuplicateGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Key)
	}
	return out
}

// ---------------------------------------------------------------------------
// Upsert
// ---------------------------------------------------------------------------

func TestGroupUpsert_RoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	seen := time.Date(2025, 3, 4, 5, 6, 7, 890, time.UTC)
	g := &models.DuplicateGroup{
		Key:              "episode:tvdb:42:s01e02",
		MediaType:        models.MediaTypeEpisode,
		Title:            "Pilot",
		Year:             2019,
		ShowTitle:        "Show",
		Season:           1,
		Episode:          2,
		ServerID:         3,
		LibraryIDs:       []int64{4, 5},
		ExternalIDs:      map[string]string{"tvdb": "42", "plex": "plex://episode/abc"},
		Thumb:            "/library/metadata/1/thumb/2",
		Status:           models.GroupReview,
		StatusReason:     "suspect merge",
		Flags:            []string{models.FlagCrossLibrary, models.FlagSuspectMerge},
		ProfileID:        7,
		ReclaimableBytes: 123,
		FirstSeenAt:      seen,
		LastSeenAt:       seen.Add(time.Hour),
		LastScanID:       9,
		Signature:        "sig",
		StableCount:      2,
		Files: []models.GroupFile{
			{
				Version: models.MediaVersion{
					Key: "plex:3:11", ServerID: 3, LibraryID: 4, RatingKey: "100", MediaID: 11,
					Parts:       []models.MediaPart{{ID: 1, Path: "/tv/a.mkv", Size: 100, Exists: ptr(true), LinkCount: 1}},
					AudioTracks: []models.AudioTrack{{Format: models.AudioEAC3, Channels: 6}},
					Arr:         &models.ArrFileInfo{InstanceID: 1, CustomFormatScore: ptr(15)},
					AddedAt:     seen,
				},
				Decision: models.DecisionKeep, EngineDecision: models.DecisionKeep, Rank: 1,
				Reasons: []string{"best"}, DecidingCriterion: "resolution",
				Protected: true, ProtectedReason: "path glob", Values: map[string]string{"resolution": "2160p"},
			},
			{
				Version:  models.MediaVersion{Key: "plex:3:12", ServerID: 3, LibraryID: 5, RatingKey: "101", MediaID: 12},
				Decision: models.DecisionRemove, EngineDecision: models.DecisionRemove, Rank: 2,
			},
		},
	}
	before := time.Now().UTC().Add(-time.Second)
	if !upsert(t, d, g) {
		t.Fatal("first Upsert: created = false")
	}
	if g.ID == 0 || g.Files[0].ID == 0 || g.Files[1].ID == 0 || g.Files[0].GroupID != g.ID {
		t.Fatalf("IDs not set: group %d files %+v", g.ID, []int64{g.Files[0].ID, g.Files[1].ID})
	}
	if g.UpdatedAt.Before(before) {
		t.Fatalf("UpdatedAt = %v", g.UpdatedAt)
	}

	for name, get := range map[string]func() (*models.DuplicateGroup, error){
		"Get":      func() (*models.DuplicateGroup, error) { return d.Groups().Get(ctx, g.ID) },
		"GetByKey": func() (*models.DuplicateGroup, error) { return d.Groups().GetByKey(ctx, g.Key) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := get()
			must(t, err)
			// JSON equality covers every field (times compared as RFC3339 text in UTC).
			want, _ := json.Marshal(g)
			have, _ := json.Marshal(got)
			if string(want) != string(have) {
				t.Fatalf("round trip mismatch\nwant %s\nhave %s", want, have)
			}
			if *got.Files[0].Version.Arr.CustomFormatScore != 15 || !*got.Files[0].Version.Parts[0].Exists {
				t.Fatal("nested version pointers lost")
			}
		})
	}

	_, err := d.Groups().Get(ctx, 999)
	wantNotFound(t, err)
	_, err = d.Groups().GetByKey(ctx, "nope")
	wantNotFound(t, err)
}

func TestGroupUpsert_NilCollectionsBecomeEmpty(t *testing.T) {
	d := newTestDB(t)
	g := &models.DuplicateGroup{
		Key:    "movie:tmdb:1",
		Status: models.GroupPending,
		Files:  []models.GroupFile{{Version: models.MediaVersion{Key: "v1", Arr: &models.ArrFileInfo{}}}},
	}
	upsert(t, d, g)
	got := getGroup(t, d, g.ID)
	b, err := json.Marshal(got)
	must(t, err)
	if strings.Contains(string(b), "null") {
		t.Fatalf("JSON contains null: %s", b)
	}
	for _, field := range []string{`"libraryIds":[]`, `"flags":[]`, `"externalIds":{}`, `"reasons":[]`,
		`"values":{}`, `"parts":[]`, `"audioTracks":[]`, `"subtitleTracks":[]`, `"episodeIds":[]`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("JSON lacks %s: %s", field, b)
		}
	}
	// The caller's struct is normalized too.
	if g.LibraryIDs == nil || g.Flags == nil || g.ExternalIDs == nil {
		t.Fatal("caller's group not normalized")
	}

	// A group without files round-trips with an empty (non-nil) Files slice.
	empty := &models.DuplicateGroup{Key: "movie:tmdb:2", Status: models.GroupPending}
	upsert(t, d, empty)
	if got := getGroup(t, d, empty.ID); got.Files == nil || len(got.Files) != 0 {
		t.Fatalf("Files = %#v", got.Files)
	}
}

func TestGroupUpsert_PreservesFirstSeenAndIgnored(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	g := testGroup("movie:tmdb:1", 1, 1)
	g.FirstSeenAt = first
	if !upsert(t, d, g) {
		t.Fatal("created = false on insert")
	}
	id := g.ID

	// Re-scan: new FirstSeenAt/LastSeenAt proposed, new status.
	g2 := testGroup("movie:tmdb:1", 1, 2)
	g2.FirstSeenAt = first.Add(48 * time.Hour)
	g2.LastSeenAt = first.Add(72 * time.Hour)
	g2.Status = models.GroupReview
	if upsert(t, d, g2) {
		t.Fatal("created = true on update")
	}
	got := getGroup(t, d, id)
	if g2.ID != id || !got.FirstSeenAt.Equal(first) || !g2.FirstSeenAt.Equal(first) {
		t.Fatalf("FirstSeenAt = %v (caller %v), want %v", got.FirstSeenAt, g2.FirstSeenAt, first)
	}
	if !got.LastSeenAt.Equal(first.Add(72*time.Hour)) || got.LastScanID != 2 || got.Status != models.GroupReview {
		t.Fatalf("update not applied: %+v", got)
	}

	// Ignored survives re-scans (with its reason) until un-ignored.
	must(t, d.Groups().UpdateStatus(ctx, id, models.GroupIgnored, "user said so"))
	g3 := testGroup("movie:tmdb:1", 1, 3)
	g3.StatusReason = "engine reason"
	upsert(t, d, g3)
	got = getGroup(t, d, id)
	if got.Status != models.GroupIgnored || got.StatusReason != "user said so" || g3.Status != models.GroupIgnored {
		t.Fatalf("ignored not preserved: status %q reason %q (caller %q)", got.Status, got.StatusReason, g3.Status)
	}
	if got.LastScanID != 3 {
		t.Fatalf("other fields of an ignored group must still update: LastScanID = %d", got.LastScanID)
	}

	// Other statuses are replaced.
	must(t, d.Groups().UpdateStatus(ctx, id, models.GroupResolved, ""))
	upsert(t, d, testGroup("movie:tmdb:1", 1, 4))
	if got := getGroup(t, d, id); got.Status != models.GroupPending {
		t.Fatalf("resolved group re-detected: status = %q, want pending", got.Status)
	}
}

func TestGroupUpsert_Overrides(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	upsert(t, d, g)
	idA, idB := g.Files[0].ID, g.Files[1].ID
	keyA, keyB := g.Files[0].Version.Key, g.Files[1].Version.Key

	// User keeps B (which the engine removes).
	must(t, d.Groups().SetOverride(ctx, g.ID, idB, models.DecisionKeep))

	// Re-scan without overrides (scanner did not carry them): B keeps its override, IDs are stable
	// and B's effective decision is forced to keep.
	re := testGroup("movie:tmdb:1", 1, 2)
	upsert(t, d, re)
	got := getGroup(t, d, g.ID)
	b := fileByKey(t, got, keyB)
	if b.ID != idB || fileByKey(t, got, keyA).ID != idA {
		t.Fatalf("file IDs changed across re-scan: %d/%d -> %d/%d", idA, idB, fileByKey(t, got, keyA).ID, b.ID)
	}
	if b.Override != models.DecisionKeep || b.Decision != models.DecisionKeep || b.EngineDecision != models.DecisionRemove {
		t.Fatalf("B = override %q decision %q engine %q", b.Override, b.Decision, b.EngineDecision)
	}
	if re.Files[1].Override != models.DecisionKeep || re.Files[1].Decision != models.DecisionKeep || re.Files[1].ID != idB {
		t.Fatalf("caller's file not updated: %+v", re.Files[1])
	}
	if got.ReclaimableBytes != 0 || re.ReclaimableBytes != 0 {
		t.Fatalf("ReclaimableBytes = %d, want 0 once nothing is removed", got.ReclaimableBytes)
	}

	// The stored override wins over a stale incoming one.
	re = testGroup("movie:tmdb:1", 1, 3)
	re.Files[1].Override = models.DecisionRemove
	upsert(t, d, re)
	if b := fileByKey(t, getGroup(t, d, g.ID), keyB); b.Override != models.DecisionKeep {
		t.Fatalf("stored override lost: %q", b.Override)
	}

	// B vanishes (e.g. Plex reports its file unavailable): its row goes; a new version C appears
	// with its own override.
	re = testGroup("movie:tmdb:1", 1, 4)
	c := testFile("movie:tmdb:1/c", "rk", gib, models.DecisionRemove)
	c.Override = models.DecisionRemove
	re.Files = []models.GroupFile{re.Files[0], c}
	upsert(t, d, re)
	got = getGroup(t, d, g.ID)
	if len(got.Files) != 2 || got.Files[0].Version.Key != keyA || got.Files[1].Version.Key != "movie:tmdb:1/c" {
		t.Fatalf("files = %v", got.Files)
	}
	if got.Files[1].Override != models.DecisionRemove {
		t.Fatalf("new file's override = %q, want remove", got.Files[1].Override)
	}

	// B comes back (C leaves): it is a new row, but the user's "keep" is restored and wins over the
	// engine's "remove"; C's override is retained in turn.
	re = testGroup("movie:tmdb:1", 1, 5)
	upsert(t, d, re)
	b = fileByKey(t, getGroup(t, d, g.ID), keyB)
	if b.ID == idB || b.Override != models.DecisionKeep || b.Decision != models.DecisionKeep {
		t.Fatalf("returning file: id %d (old %d) override %q decision %q", b.ID, idB, b.Override, b.Decision)
	}
	if re.Files[1].Override != models.DecisionKeep || re.Files[1].Decision != models.DecisionKeep {
		t.Fatalf("caller's returning file not updated: %+v", re.Files[1])
	}
	// Once restored, the row carries the override: clearing it is not undone by the retained copy.
	must(t, d.Groups().SetOverride(ctx, g.ID, b.ID, ""))
	upsert(t, d, testGroup("movie:tmdb:1", 1, 6))
	if b := fileByKey(t, getGroup(t, d, g.ID), keyB); b.Override != "" {
		t.Fatalf("cleared override came back: %q", b.Override)
	}
	// C returns with its retained "remove" override (recorded, never forcing a removal).
	re = testGroup("movie:tmdb:1", 1, 7)
	c = testFile("movie:tmdb:1/c", "rk", gib, models.DecisionKeep)
	re.Files = append(re.Files, c)
	upsert(t, d, re)
	if c := fileByKey(t, getGroup(t, d, g.ID), "movie:tmdb:1/c"); c.Override != models.DecisionRemove || c.Decision != models.DecisionKeep {
		t.Fatalf("C returned with override %q decision %q", c.Override, c.Decision)
	}
	var retained string
	must(t, d.r.QueryRowContext(ctx, `SELECT retained_overrides FROM duplicate_groups WHERE id = ?`, g.ID).Scan(&retained))
	if retained != "{}" {
		t.Fatalf("retained overrides = %s, want {} once every version is back", retained)
	}
}

func TestGroupUpsert_SafetyDecisions(t *testing.T) {
	tests := []struct {
		name            string
		engine, dec     models.Decision
		override        models.Decision
		protected       bool
		hardlinked      bool
		wantDecision    models.Decision
		wantReclaimable int64
	}{
		{"consistent remove", models.DecisionRemove, models.DecisionRemove, "", false, false, models.DecisionRemove, 2 * gib},
		{"engine keep but decision remove", models.DecisionKeep, models.DecisionRemove, "", false, false, models.DecisionKeep, 0},
		{"override keep but decision remove", models.DecisionRemove, models.DecisionRemove, models.DecisionKeep, false, false, models.DecisionKeep, 0},
		{"protected but decision remove", models.DecisionRemove, models.DecisionRemove, "", true, false, models.DecisionKeep, 0},
		{"hardlinked flip keeps reclaimable", models.DecisionKeep, models.DecisionRemove, "", false, true, models.DecisionKeep, 2 * gib},
		{"override remove never forces remove", models.DecisionKeep, models.DecisionKeep, models.DecisionRemove, false, false, models.DecisionKeep, 2 * gib},
		{"unevaluated stays empty", "", "", "", false, false, "", 2 * gib},
		{"unevaluated override keep", "", "", models.DecisionKeep, false, false, models.DecisionKeep, 2 * gib},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDB(t)
			g := testGroup("movie:tmdb:1", 1, 1)
			f := &g.Files[1]
			f.EngineDecision, f.Decision, f.Override, f.Protected = tt.engine, tt.dec, tt.override, tt.protected
			if tt.hardlinked {
				f.Version.Parts[0].LinkCount = 2
			}
			upsert(t, d, g)
			got := getGroup(t, d, g.ID)
			if got.Files[1].Decision != tt.wantDecision || g.Files[1].Decision != tt.wantDecision {
				t.Fatalf("decision = %q (caller %q), want %q", got.Files[1].Decision, g.Files[1].Decision, tt.wantDecision)
			}
			if got.ReclaimableBytes != tt.wantReclaimable {
				t.Fatalf("reclaimable = %d, want %d", got.ReclaimableBytes, tt.wantReclaimable)
			}
		})
	}
}

func TestGroupUpsert_Validation(t *testing.T) {
	d := newTestDB(t)
	tests := []struct {
		name   string
		mutate func(g *models.DuplicateGroup)
	}{
		{"empty key", func(g *models.DuplicateGroup) { g.Key = " " }},
		{"unknown status", func(g *models.DuplicateGroup) { g.Status = "deleted" }},
		{"empty version key", func(g *models.DuplicateGroup) { g.Files[1].Version.Key = "" }},
		{"duplicate version key", func(g *models.DuplicateGroup) { g.Files[1].Version.Key = g.Files[0].Version.Key }},
		{"invalid decision", func(g *models.DuplicateGroup) { g.Files[0].Decision = "delete" }},
		{"invalid override", func(g *models.DuplicateGroup) { g.Files[0].Override = "maybe" }},
		{"invalid engine decision", func(g *models.DuplicateGroup) { g.Files[0].EngineDecision = "x" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testGroup("movie:tmdb:1", 1, 1)
			tt.mutate(g)
			if _, err := d.Groups().Upsert(context.Background(), g); err == nil {
				t.Fatal("Upsert succeeded")
			}
			if g.ID != 0 {
				t.Fatal("ID set on failure")
			}
		})
	}
	if _, err := d.Groups().Upsert(context.Background(), nil); err == nil {
		t.Fatal("Upsert(nil) succeeded")
	}
	page, err := d.Groups().List(context.Background(), store.GroupFilter{}, store.Paging{})
	must(t, err)
	if page.TotalRecords != 0 {
		t.Fatalf("failed upserts left %d groups", page.TotalRecords)
	}

	// An empty status is stored as review (never auto-approved).
	g := testGroup("movie:tmdb:2", 1, 1)
	g.Status = ""
	upsert(t, d, g)
	if got := getGroup(t, d, g.ID); got.Status != models.GroupReview || g.Status != models.GroupReview {
		t.Fatalf("status = %q, want review", got.Status)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

// seedGroups inserts a varied set of groups for filter/sort tests.
func seedGroups(t *testing.T, d *DB) {
	t.Helper()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	specs := []struct {
		key       string
		mt        models.MediaType
		title     string
		show      string
		season    int
		episode   int
		server    int64
		libs      []int64
		status    models.GroupStatus
		flags     []string
		bytes     int64
		firstSeen int // hours after base
		lastSeen  int
	}{
		{"m1", models.MediaTypeMovie, "Alien", "", 0, 0, 1, []int64{1}, models.GroupPending, []string{models.FlagStacked}, 300, 1, 10},
		{"m2", models.MediaTypeMovie, "amélie", "", 0, 0, 1, []int64{1, 2}, models.GroupReview, []string{models.FlagCrossLibrary, models.FlagSuspectMerge}, 100, 2, 30},
		{"m3", models.MediaTypeMovie, "Zodiac", "", 0, 0, 2, []int64{3}, models.GroupIgnored, nil, 200, 3, 20},
		{"e1", models.MediaTypeEpisode, "Pilot", "Breaking Bad", 1, 1, 1, []int64{4}, models.GroupPending, nil, 50, 4, 40},
		{"e2", models.MediaTypeEpisode, "Cat's in the Bag", "Breaking Bad", 1, 2, 1, []int64{4}, models.GroupQueued, []string{models.FlagMultiEpisode}, 60, 5, 5},
		{"e3", models.MediaTypeEpisode, "Winter Is Coming", "Game of Thrones", 1, 1, 2, []int64{5}, models.GroupResolved, nil, 70, 6, 50},
	}
	for _, s := range specs {
		g := testGroup(s.key, 0, 1)
		g.MediaType, g.Title, g.ShowTitle, g.Season, g.Episode = s.mt, s.title, s.show, s.season, s.episode
		g.ServerID, g.LibraryIDs, g.Status, g.Flags, g.ReclaimableBytes = s.server, s.libs, s.status, s.flags, s.bytes
		g.FirstSeenAt = base.Add(time.Duration(s.firstSeen) * time.Hour)
		g.LastSeenAt = base.Add(time.Duration(s.lastSeen) * time.Hour)
		upsert(t, d, g)
	}
}

func TestGroupList_Filters(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	seedGroups(t, d)
	tests := []struct {
		name string
		f    store.GroupFilter
		want []string // sorted keys
	}{
		{"all", store.GroupFilter{}, []string{"e1", "e2", "e3", "m1", "m2", "m3"}},
		{"one status", store.GroupFilter{Statuses: []models.GroupStatus{models.GroupPending}}, []string{"e1", "m1"}},
		{"status IN", store.GroupFilter{Statuses: []models.GroupStatus{models.GroupReview, models.GroupQueued}}, []string{"e2", "m2"}},
		{"media type", store.GroupFilter{MediaType: models.MediaTypeEpisode}, []string{"e1", "e2", "e3"}},
		{"library (json_each)", store.GroupFilter{LibraryID: 2}, []string{"m2"}},
		{"library shared", store.GroupFilter{LibraryID: 1}, []string{"m1", "m2"}},
		{"server", store.GroupFilter{ServerID: 2}, []string{"e3", "m3"}},
		{"flag", store.GroupFilter{Flag: models.FlagCrossLibrary}, []string{"m2"}},
		{"flag absent", store.GroupFilter{Flag: models.FlagHardlinked}, []string{}},
		{"search title case-insensitive", store.GroupFilter{Search: "ALIEN"}, []string{"m1"}},
		{"search unicode case-insensitive", store.GroupFilter{Search: "AMÉLIE"}, []string{"m2"}},
		{"search show title", store.GroupFilter{Search: "breaking"}, []string{"e1", "e2"}},
		{"search is literal", store.GroupFilter{Search: "%"}, []string{}},
		{"search apostrophe", store.GroupFilter{Search: "cat's"}, []string{"e2"}},
		{"search spans title/show boundary never matches", store.GroupFilter{Search: "pilot breaking"}, []string{}},
		{"keys", store.GroupFilter{Keys: []string{"m3", "e1", "missing"}}, []string{"e1", "m3"}},
		{"combined", store.GroupFilter{MediaType: models.MediaTypeEpisode, ServerID: 1, Statuses: []models.GroupStatus{models.GroupPending, models.GroupQueued}, Search: "bad"}, []string{"e1", "e2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := d.Groups().List(ctx, tt.f, store.Paging{PageSize: 100})
			must(t, err)
			got := groupKeys(page.Records)
			slices.Sort(got)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("keys = %v, want %v", got, tt.want)
			}
			if page.TotalRecords != len(tt.want) {
				t.Fatalf("TotalRecords = %d, want %d", page.TotalRecords, len(tt.want))
			}
			if page.Records == nil {
				t.Fatal("Records is nil")
			}
			for _, g := range page.Records {
				if len(g.Files) != 2 {
					t.Fatalf("group %s has %d files, want 2 (Files populated)", g.Key, len(g.Files))
				}
				for _, f := range g.Files {
					if f.GroupID != g.ID {
						t.Fatalf("file %d attached to the wrong group", f.ID)
					}
				}
			}
		})
	}
}

func TestGroupList_SortAndPaging(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	seedGroups(t, d)
	tests := []struct {
		name    string
		p       store.Paging
		want    []string
		key     string
		dir     string
		total   int
		pageNum int
	}{
		{"default lastSeenAt desc", store.Paging{}, []string{"e3", "e1", "m2", "m3", "m1", "e2"}, "lastSeenAt", sortDescending, 6, 1},
		{"lastSeenAt asc", store.Paging{SortKey: "lastSeenAt", SortDirection: "ascending"}, []string{"e2", "m1", "m3", "m2", "e1", "e3"}, "lastSeenAt", sortAscending, 6, 1},
		{"firstSeenAt", store.Paging{SortKey: "firstSeenAt", SortDirection: "ascending"}, []string{"m1", "m2", "m3", "e1", "e2", "e3"}, "firstSeenAt", sortAscending, 6, 1},
		{"reclaimableBytes desc", store.Paging{SortKey: "reclaimableBytes"}, []string{"m1", "m3", "m2", "e3", "e2", "e1"}, "reclaimableBytes", sortDescending, 6, 1},
		{"title groups episodes by show", store.Paging{SortKey: "title"}, []string{"m1", "m2", "e1", "e2", "e3", "m3"}, "title", sortAscending, 6, 1},
		{"status", store.Paging{SortKey: "status", SortDirection: "ascending", PageSize: 2}, []string{"m3", "m1"}, "status", sortAscending, 6, 1},
		{"page 2", store.Paging{Page: 2, PageSize: 4}, []string{"m1", "e2"}, "lastSeenAt", sortDescending, 6, 2},
		{"beyond the end", store.Paging{Page: 5, PageSize: 4}, []string{}, "lastSeenAt", sortDescending, 6, 5},
		{"unknown sort key", store.Paging{SortKey: "title; DROP TABLE duplicate_groups"}, []string{"e3", "e1", "m2", "m3", "m1", "e2"}, "lastSeenAt", sortDescending, 6, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := d.Groups().List(ctx, store.GroupFilter{}, tt.p)
			must(t, err)
			got := groupKeys(page.Records)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("order = %v, want %v", got, tt.want)
			}
			if page.SortKey != tt.key || page.SortDirection != tt.dir || page.TotalRecords != tt.total || page.Page != tt.pageNum {
				t.Fatalf("page meta = %+v", page)
			}
		})
	}
}

func TestGroupListByRatingKeys(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	a := testGroup("a", 1, 1) // rating key rk-a on server 1
	b := testGroup("b", 1, 1)
	b.Files[1].Version.RatingKey = "shared"
	c := testGroup("c", 1, 1)
	c.ServerID = 2
	c.Files[0].Version.RatingKey = "shared"
	for _, g := range []*models.DuplicateGroup{a, b, c} {
		upsert(t, d, g)
	}
	tests := []struct {
		name   string
		server int64
		keys   []string
		want   []string
	}{
		{"single", 1, []string{"rk-a"}, []string{"a"}},
		{"server scoped", 1, []string{"shared"}, []string{"b"}},
		{"other server", 2, []string{"shared"}, []string{"c"}},
		{"several", 1, []string{"rk-a", "rk-b", "nope"}, []string{"a", "b"}},
		{"none", 1, nil, []string{}},
		{"unknown", 1, []string{"x"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.Groups().ListByRatingKeys(ctx, tt.server, tt.keys)
			must(t, err)
			if got == nil || fmt.Sprint(groupKeys(got)) != fmt.Sprint(tt.want) {
				t.Fatalf("groups = %v, want %v", groupKeys(got), tt.want)
			}
			for _, g := range got {
				if len(g.Files) != 2 {
					t.Fatalf("Files not populated for %s", g.Key)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Status / overrides / resolution / stats / delete
// ---------------------------------------------------------------------------

func TestGroupUpdateStatus(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("a", 1, 1)
	upsert(t, d, g)
	must(t, d.Groups().UpdateStatus(ctx, g.ID, models.GroupDeferred, "min age"))
	got := getGroup(t, d, g.ID)
	if got.Status != models.GroupDeferred || got.StatusReason != "min age" {
		t.Fatalf("status = %q/%q", got.Status, got.StatusReason)
	}
	if err := d.Groups().UpdateStatus(ctx, g.ID, "bogus", ""); err == nil {
		t.Fatal("invalid status accepted")
	}
	wantNotFound(t, d.Groups().UpdateStatus(ctx, 999, models.GroupPending, ""))
}

func TestGroupSetOverride(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("a", 1, 1)
	other := testGroup("b", 1, 1)
	upsert(t, d, g)
	upsert(t, d, other)
	keeper, loser := g.Files[0], g.Files[1]

	tests := []struct {
		name         string
		groupID      int64
		fileID       int64
		d            models.Decision
		wantErr      bool
		notFound     bool
		wantOverride models.Decision
		wantDecision models.Decision
	}{
		{"keep forces the decision immediately", g.ID, loser.ID, models.DecisionKeep, false, false, models.DecisionKeep, models.DecisionKeep},
		{"remove is only recorded (no re-evaluation)", g.ID, keeper.ID, models.DecisionRemove, false, false, models.DecisionRemove, models.DecisionKeep},
		{"clear", g.ID, keeper.ID, "", false, false, "", models.DecisionKeep},
		{"invalid decision", g.ID, keeper.ID, "nuke", true, false, "", ""},
		{"file of another group", other.ID, keeper.ID, models.DecisionKeep, true, true, "", ""},
		{"missing file", g.ID, 999, models.DecisionKeep, true, true, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := d.Groups().SetOverride(ctx, tt.groupID, tt.fileID, tt.d)
			if tt.wantErr {
				if err == nil {
					t.Fatal("SetOverride succeeded")
				}
				if tt.notFound {
					wantNotFound(t, err)
				}
				return
			}
			must(t, err)
			var f models.GroupFile
			for _, x := range getGroup(t, d, tt.groupID).Files {
				if x.ID == tt.fileID {
					f = x
				}
			}
			if f.Override != tt.wantOverride || f.Decision != tt.wantDecision {
				t.Fatalf("override %q decision %q, want %q %q", f.Override, f.Decision, tt.wantOverride, tt.wantDecision)
			}
		})
	}
}

func TestGroupMarkUnseenResolved(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	type spec struct {
		key    string
		libs   []int64
		scan   int64
		status models.GroupStatus
	}
	specs := []spec{
		{"seen", []int64{1}, 5, models.GroupPending},            // seen in this scan
		{"unseen", []int64{1}, 4, models.GroupPending},          // → resolved
		{"unseen-review", []int64{2, 9}, 3, models.GroupReview}, // intersects via 2 → resolved
		{"unseen-queued", []int64{1}, 4, models.GroupQueued},    // → resolved
		{"other-library", []int64{3}, 4, models.GroupPending},   // not scanned
		{"ignored", []int64{1}, 4, models.GroupIgnored},         // kept
		{"already-resolved", []int64{1}, 4, models.GroupResolved},
		{"no-libraries", nil, 4, models.GroupPending},
	}
	ids := map[string]int64{}
	groups := map[string]*models.DuplicateGroup{}
	for _, s := range specs {
		g := testGroup(s.key, 0, s.scan)
		g.LibraryIDs, g.Status = s.libs, s.status
		upsert(t, d, g)
		ids[s.key], groups[s.key] = g.ID, g
	}
	resolvedBefore := getGroup(t, d, ids["already-resolved"]).UpdatedAt
	approved := queueRemoval(t, d, groups["unseen-queued"], 1)
	running := queueRemoval(t, d, groups["unseen-queued"], 1)
	running.Status = models.ActionRunning
	must(t, d.Actions().Update(ctx, running))
	unrelated := queueRemoval(t, d, groups["seen"], 1)

	got, err := d.Groups().MarkUnseenResolved(ctx, 5, []int64{1, 2})
	must(t, err)
	want := []int64{ids["unseen"], ids["unseen-review"], ids["unseen-queued"]}
	slices.Sort(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("affected = %v, want %v", got, want)
	}
	wantStatus := map[string]models.GroupStatus{
		"seen": models.GroupPending, "unseen": models.GroupResolved, "unseen-review": models.GroupResolved,
		"unseen-queued": models.GroupResolved, "other-library": models.GroupPending, "ignored": models.GroupIgnored,
		"already-resolved": models.GroupResolved, "no-libraries": models.GroupPending,
	}
	for key, st := range wantStatus {
		g := getGroup(t, d, ids[key])
		if g.Status != st {
			t.Errorf("%s: status = %q, want %q", key, g.Status, st)
		}
		if key == "unseen" && g.StatusReason == "" {
			t.Errorf("%s: no status reason", key)
		}
	}
	if !getGroup(t, d, ids["already-resolved"]).UpdatedAt.Equal(resolvedBefore) {
		t.Error("already-resolved group was touched")
	}
	// Pending removals of groups that are no longer duplicates are cancelled; nothing else is.
	for a, want := range map[*models.Action]models.ActionStatus{
		approved: models.ActionCancelled, running: models.ActionRunning, unrelated: models.ActionPending,
	} {
		got, err := d.Actions().Get(ctx, a.ID)
		must(t, err)
		if got.Status != want {
			t.Errorf("action %d status = %q, want %q", a.ID, got.Status, want)
		}
	}

	// Second run: nothing left to resolve; empty library list is a no-op.
	got, err = d.Groups().MarkUnseenResolved(ctx, 5, []int64{1, 2})
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("second run affected %v", got)
	}
	got, err = d.Groups().MarkUnseenResolved(ctx, 6, nil)
	must(t, err)
	if got == nil || len(got) != 0 {
		t.Fatalf("empty library list affected %v", got)
	}
}

func TestGroupStats(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)

	st, err := d.Groups().Stats(ctx)
	must(t, err)
	if st.Total != 0 || len(st.ByStatus) != len(knownGroupStatuses) || st.ByStatus[models.GroupPending] != 0 {
		t.Fatalf("empty stats = %+v", st)
	}

	seedGroups(t, d) // bytes: pending m1=300 e1=50, review m2=100, queued e2=60, ignored 200, resolved 70
	must(t, d.Actions().Create(ctx, &models.Action{GroupID: 1, Size: 1000, Status: models.ActionSucceeded}))
	must(t, d.Actions().Create(ctx, &models.Action{GroupID: 1, Size: 50, Status: models.ActionSucceeded, DryRun: true}))
	must(t, d.Actions().Create(ctx, &models.Action{GroupID: 1, Size: 70, Status: models.ActionFailed}))

	st, err = d.Groups().Stats(ctx)
	must(t, err)
	if st.Total != 6 {
		t.Fatalf("Total = %d", st.Total)
	}
	want := map[models.GroupStatus]int{
		models.GroupPending: 2, models.GroupReview: 1, models.GroupQueued: 1, models.GroupIgnored: 1,
		models.GroupResolved: 1, models.GroupDeferred: 0, models.GroupProtected: 0, models.GroupFailed: 0,
	}
	for s, n := range want {
		if st.ByStatus[s] != n {
			t.Errorf("ByStatus[%s] = %d, want %d", s, st.ByStatus[s], n)
		}
	}
	if st.ReclaimableBytes != 300+50+100+60 {
		t.Errorf("ReclaimableBytes = %d, want 510", st.ReclaimableBytes)
	}
	if st.ReclaimedBytes != 1000 {
		t.Errorf("ReclaimedBytes = %d, want 1000", st.ReclaimedBytes)
	}
}

func TestGroupDelete(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("a", 1, 1)
	keep := testGroup("b", 1, 1)
	upsert(t, d, g)
	upsert(t, d, keep)

	pending := &models.Action{GroupID: g.ID, GroupFileID: g.Files[1].ID}
	done := &models.Action{GroupID: g.ID, Status: models.ActionSucceeded, Size: 5}
	otherGroup := &models.Action{GroupID: keep.ID, GroupFileID: keep.Files[1].ID}
	for _, a := range []*models.Action{pending, done, otherGroup} {
		must(t, d.Actions().Create(ctx, a))
	}

	must(t, d.Groups().Delete(ctx, g.ID))
	_, err := d.Groups().Get(ctx, g.ID)
	wantNotFound(t, err)
	var files int
	must(t, d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_files WHERE group_id = ?`, g.ID).Scan(&files))
	if files != 0 {
		t.Fatalf("%d orphan files", files)
	}

	check := func(a *models.Action, want models.ActionStatus) {
		t.Helper()
		got, err := d.Actions().Get(ctx, a.ID)
		must(t, err)
		if got.Status != want {
			t.Fatalf("action %d status = %q, want %q", a.ID, got.Status, want)
		}
	}
	check(pending, models.ActionCancelled) // never executed for a vanished group
	check(done, models.ActionSucceeded)    // audit trail kept
	check(otherGroup, models.ActionPending)
	if n, err := d.Actions().ReclaimedBytes(ctx); err != nil || n != 5 {
		t.Fatalf("ReclaimedBytes = %d, %v", n, err)
	}
	if len(getGroup(t, d, keep.ID).Files) != 2 {
		t.Fatal("other group's files touched")
	}

	if err := d.Groups().Delete(ctx, g.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stale queued removals
// ---------------------------------------------------------------------------

// queueRemoval creates a pending action for file i of g (as the API does on approval).
func queueRemoval(t *testing.T, d *DB, g *models.DuplicateGroup, i int) *models.Action {
	t.Helper()
	a := &models.Action{GroupID: g.ID, GroupFileID: g.Files[i].ID, VersionKey: g.Files[i].Version.Key,
		Title: g.Title, Size: g.Files[i].Version.TotalSize()}
	must(t, d.Actions().Create(context.Background(), a))
	return a
}

func actionStatus(t *testing.T, d *DB, a *models.Action) models.ActionStatus {
	t.Helper()
	got, err := d.Actions().Get(context.Background(), a.ID)
	must(t, err)
	return got.Status
}

// TestGroupUpsert_CancelsStaleActions: a re-scan that changes what is removed cancels queued
// removals of versions that are no longer marked "remove"; still-valid ones are kept.
func TestGroupUpsert_CancelsStaleActions(t *testing.T) {
	tests := []struct {
		name   string
		rescan func(g *models.DuplicateGroup) // mutates a fresh copy of the original group
		want   models.ActionStatus
	}{
		{"unchanged decisions keep the removal", func(g *models.DuplicateGroup) {}, models.ActionPending},
		{"loser became the keeper", func(g *models.DuplicateGroup) {
			g.Files[0].Decision, g.Files[0].EngineDecision = models.DecisionRemove, models.DecisionRemove
			g.Files[1].Decision, g.Files[1].EngineDecision = models.DecisionKeep, models.DecisionKeep
		}, models.ActionCancelled},
		{"loser now protected", func(g *models.DuplicateGroup) { g.Files[1].Protected = true }, models.ActionCancelled},
		{"loser vanished", func(g *models.DuplicateGroup) { g.Files = g.Files[:1] }, models.ActionCancelled},
		{"loser not evaluated", func(g *models.DuplicateGroup) {
			g.Files[1].Decision, g.Files[1].EngineDecision = "", ""
		}, models.ActionCancelled},
		// A queued group re-evaluated into a status that says "do not act now" loses its queue.
		{"re-evaluated to review", func(g *models.DuplicateGroup) {
			g.Status, g.StatusReason = models.GroupReview, "possible mismatched merge"
		}, models.ActionCancelled},
		{"re-evaluated to deferred", func(g *models.DuplicateGroup) {
			g.Status, g.StatusReason = models.GroupDeferred, "min age"
		}, models.ActionCancelled},
		{"re-evaluated to protected", func(g *models.DuplicateGroup) { g.Status = models.GroupProtected }, models.ActionCancelled},
		{"status lost by the caller", func(g *models.DuplicateGroup) { g.Status = "" }, models.ActionCancelled}, // stored as review
		{"re-evaluated to pending", func(g *models.DuplicateGroup) { g.Status = models.GroupPending }, models.ActionPending},
		{"no keeper left", func(g *models.DuplicateGroup) {
			g.Files[0].Decision, g.Files[0].EngineDecision = models.DecisionRemove, models.DecisionRemove
		}, models.ActionCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d := newTestDB(t)
			g := testGroup("movie:tmdb:1", 1, 1)
			g.Status = models.GroupQueued
			upsert(t, d, g)
			removal := queueRemoval(t, d, g, 1)
			running := queueRemoval(t, d, g, 1)
			running.Status = models.ActionRunning
			must(t, d.Actions().Update(ctx, running))
			// Another group's queue is never touched.
			other := testGroup("movie:tmdb:2", 1, 1)
			upsert(t, d, other)
			otherRemoval := queueRemoval(t, d, other, 1)

			re := testGroup("movie:tmdb:1", 1, 2)
			re.Status = models.GroupQueued
			tt.rescan(re)
			if upsert(t, d, re) {
				t.Fatal("created = true on re-scan")
			}
			if got := actionStatus(t, d, removal); got != tt.want {
				t.Fatalf("queued removal = %q, want %q", got, tt.want)
			}
			if got := actionStatus(t, d, running); got != models.ActionRunning {
				t.Fatalf("running action = %q, want running", got)
			}
			if got := actionStatus(t, d, otherRemoval); got != models.ActionPending {
				t.Fatalf("other group's removal = %q, want pending", got)
			}
			if tt.want == models.ActionCancelled {
				got, err := d.Actions().Get(ctx, removal.ID)
				must(t, err)
				if got.FinishedAt == nil || got.Message == "" {
					t.Fatalf("cancelled action lacks FinishedAt/Message: %+v", got)
				}
				// The executor can no longer start it.
				got.Status = models.ActionRunning
				if err := d.Actions().Update(ctx, got); !errors.Is(err, ErrActionNotPending) {
					t.Fatalf("starting a cancelled action: %v", err)
				}
			}
		})
	}
}

// TestGroup_EmptiedQueueReopensGroup: when the store cancels the last queued removal of a
// "queued" group, the group goes back to "pending" (the engine would otherwise keep it queued
// forever with nothing to run); while anything is still pending or running it stays queued, and
// a status that blocks removals is never overwritten.
func TestGroup_EmptiedQueueReopensGroup(t *testing.T) {
	ctx := context.Background()
	swapKeeper := func(g *models.DuplicateGroup) {
		g.Files[0].Decision, g.Files[0].EngineDecision = models.DecisionRemove, models.DecisionRemove
		g.Files[1].Decision, g.Files[1].EngineDecision = models.DecisionKeep, models.DecisionKeep
	}
	tests := []struct {
		name       string
		extra      models.ActionStatus // "" = no second action of the group
		cancel     func(t *testing.T, d *DB, g *models.DuplicateGroup, a *models.Action) *models.DuplicateGroup
		wantStatus models.GroupStatus
	}{
		{"re-scan makes the loser a keeper", "", func(t *testing.T, d *DB, g *models.DuplicateGroup, a *models.Action) *models.DuplicateGroup {
			re := testGroup(g.Key, 1, 2)
			re.Status = models.GroupQueued
			swapKeeper(re)
			upsert(t, d, re)
			return re
		}, models.GroupPending},
		{"override to keep", "", func(t *testing.T, d *DB, g *models.DuplicateGroup, a *models.Action) *models.DuplicateGroup {
			must(t, d.Groups().SetOverride(ctx, g.ID, g.Files[1].ID, models.DecisionKeep))
			return nil
		}, models.GroupPending},
		{"user cancels the queue entry", "", func(t *testing.T, d *DB, g *models.DuplicateGroup, a *models.Action) *models.DuplicateGroup {
			a.Status = models.ActionCancelled
			must(t, d.Actions().Update(ctx, a))
			return nil
		}, models.GroupPending},
		{"CancelPendingForGroup", "", func(t *testing.T, d *DB, g *models.DuplicateGroup, a *models.Action) *models.DuplicateGroup {
			_, err := d.Actions().CancelPendingForGroup(ctx, g.ID)
			must(t, err)
			return nil
		}, models.GroupPending},
		{"another removal still running", models.ActionRunning, func(t *testing.T, d *DB, g *models.DuplicateGroup, a *models.Action) *models.DuplicateGroup {
			must(t, d.Groups().SetOverride(ctx, g.ID, g.Files[1].ID, models.DecisionKeep))
			return nil
		}, models.GroupQueued},
		{"another removal still pending", models.ActionPending, func(t *testing.T, d *DB, g *models.DuplicateGroup, a *models.Action) *models.DuplicateGroup {
			must(t, d.Groups().SetOverride(ctx, g.ID, g.Files[1].ID, models.DecisionKeep))
			return nil
		}, models.GroupQueued},
		{"blocking status is kept", "", func(t *testing.T, d *DB, g *models.DuplicateGroup, a *models.Action) *models.DuplicateGroup {
			must(t, d.Groups().UpdateStatus(ctx, g.ID, models.GroupReview, "suspect merge"))
			return nil
		}, models.GroupReview},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDB(t)
			g := testGroup("movie:tmdb:1", 1, 1)
			g.Files = append(g.Files, testFile("movie:tmdb:1/c", "rk", gib, models.DecisionRemove))
			g.Status = models.GroupQueued
			upsert(t, d, g)
			a := queueRemoval(t, d, g, 1)
			if tt.extra != "" {
				other := queueRemoval(t, d, g, 2)
				if tt.extra == models.ActionRunning {
					other.Status = models.ActionRunning
					must(t, d.Actions().Update(ctx, other))
				}
			}

			returned := tt.cancel(t, d, g, a)
			if got := actionStatus(t, d, a); got != models.ActionCancelled {
				t.Fatalf("removal = %q, want cancelled", got)
			}
			stored := getGroup(t, d, g.ID)
			if stored.Status != tt.wantStatus {
				t.Fatalf("group status = %q (%q), want %q", stored.Status, stored.StatusReason, tt.wantStatus)
			}
			if tt.wantStatus == models.GroupPending && stored.StatusReason != reopenedReason {
				t.Fatalf("reopened group reason = %q", stored.StatusReason)
			}
			if returned != nil && (returned.Status != stored.Status || returned.StatusReason != stored.StatusReason) {
				t.Fatalf("Upsert returned %q (%q), stored %q (%q)", returned.Status, returned.StatusReason,
					stored.Status, stored.StatusReason)
			}
		})
	}
}

// TestGroupUpsert_NoKeeperGoesToReview: invariant 1 (a group always keeps a version) is also
// enforced at the persistence boundary: a group handed over without any keeper is stored for
// review (never acted on) unless it is ignored or resolved.
func TestGroupUpsert_NoKeeperGoesToReview(t *testing.T) {
	allRemove := func(g *models.DuplicateGroup) {
		for i := range g.Files {
			g.Files[i].Decision, g.Files[i].EngineDecision = models.DecisionRemove, models.DecisionRemove
		}
	}
	tests := []struct {
		name       string
		mutate     func(g *models.DuplicateGroup)
		wantStatus models.GroupStatus
		wantReason string
	}{
		{"all removed", func(g *models.DuplicateGroup) { allRemove(g) }, models.GroupReview, noKeeperReason},
		{"all removed while queued", func(g *models.DuplicateGroup) {
			allRemove(g)
			g.Status = models.GroupQueued
		}, models.GroupReview, noKeeperReason},
		{"all removed but one overridden to keep", func(g *models.DuplicateGroup) {
			allRemove(g)
			g.Files[0].Override = models.DecisionKeep
		}, models.GroupPending, ""},
		{"all removed but one protected", func(g *models.DuplicateGroup) {
			allRemove(g)
			g.Files[1].Protected = true
		}, models.GroupPending, ""},
		{"all removed while resolved", func(g *models.DuplicateGroup) {
			allRemove(g)
			g.Status = models.GroupResolved
		}, models.GroupResolved, ""},
		{"no files", func(g *models.DuplicateGroup) { g.Files = nil }, models.GroupPending, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDB(t)
			g := testGroup("movie:tmdb:1", 1, 1)
			tt.mutate(g)
			upsert(t, d, g)
			if g.Status != tt.wantStatus || g.StatusReason != tt.wantReason {
				t.Fatalf("returned status %q (%q), want %q (%q)", g.Status, g.StatusReason, tt.wantStatus, tt.wantReason)
			}
			got := getGroup(t, d, g.ID)
			if got.Status != tt.wantStatus || got.StatusReason != tt.wantReason {
				t.Fatalf("stored status %q (%q), want %q (%q)", got.Status, got.StatusReason, tt.wantStatus, tt.wantReason)
			}
		})
	}

	// An ignored group stays ignored (the user's choice wins; it is never acted on anyway).
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	g.Status = models.GroupIgnored
	upsert(t, d, g)
	re := testGroup("movie:tmdb:1", 1, 2)
	allRemove(re)
	upsert(t, d, re)
	if re.Status != models.GroupIgnored {
		t.Fatalf("ignored group became %q", re.Status)
	}
}

// TestGroupUpsert_ActionTargetByFileID: actions without a version key are matched by file ID;
// an action without any target can never be queued in the first place.
func TestGroupUpsert_ActionTargetByFileID(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	upsert(t, d, g)
	byID := &models.Action{GroupID: g.ID, GroupFileID: g.Files[1].ID}
	must(t, d.Actions().Create(ctx, byID))
	if err := d.Actions().Create(ctx, &models.Action{GroupID: g.ID}); !errors.Is(err, ErrNotRemovable) {
		t.Fatalf("queueing a removal without a target: %v, want ErrNotRemovable", err)
	}

	upsert(t, d, testGroup("movie:tmdb:1", 1, 2))
	if got := actionStatus(t, d, byID); got != models.ActionPending {
		t.Fatalf("removal targeted by file id = %q, want pending", got)
	}
	// An untargeted row that predates the check (or was written behind the store's back) is
	// still treated as stale by the next re-scan.
	_, err := d.w.ExecContext(ctx, `INSERT INTO actions (group_id, status, created_at) VALUES (?, 'pending', ?)`,
		g.ID, fmtTime(nowUTC()))
	must(t, err)
	var legacy int64
	must(t, d.w.QueryRowContext(ctx, `SELECT MAX(id) FROM actions`).Scan(&legacy))
	upsert(t, d, testGroup("movie:tmdb:1", 1, 3))
	if got := actionStatus(t, d, &models.Action{ID: legacy}); got != models.ActionCancelled {
		t.Fatalf("removal without a target = %q, want cancelled", got)
	}
}

func TestGroupSetOverride_CancelsQueuedRemoval(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	g.Files = append(g.Files, testFile("movie:tmdb:1/c", "rk", gib, models.DecisionRemove))
	upsert(t, d, g)
	removeB := queueRemoval(t, d, g, 1)
	removeC := queueRemoval(t, d, g, 2)

	// "remove" is only recorded: the queue is untouched.
	must(t, d.Groups().SetOverride(ctx, g.ID, g.Files[2].ID, models.DecisionRemove))
	if actionStatus(t, d, removeB) != models.ActionPending || actionStatus(t, d, removeC) != models.ActionPending {
		t.Fatal("override remove cancelled a removal")
	}
	// "keep" cancels exactly the removal of that version.
	must(t, d.Groups().SetOverride(ctx, g.ID, g.Files[1].ID, models.DecisionKeep))
	if got := actionStatus(t, d, removeB); got != models.ActionCancelled {
		t.Fatalf("removal of the version kept by the user = %q, want cancelled", got)
	}
	if got := actionStatus(t, d, removeC); got != models.ActionPending {
		t.Fatalf("unrelated removal = %q, want pending", got)
	}
}

// TestGroupUpdateStatus_BlockingStatusesCancelQueue: moving a group into a status that says "do
// not act now" cancels its queued removals (running ones are left to the executor); pending,
// queued and failed leave the queue alone.
func TestGroupUpdateStatus_BlockingStatusesCancelQueue(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		status models.GroupStatus
		want   models.ActionStatus
	}{
		{models.GroupPending, models.ActionPending},
		{models.GroupQueued, models.ActionPending},
		{models.GroupFailed, models.ActionPending},
		{models.GroupReview, models.ActionCancelled},
		{models.GroupDeferred, models.ActionCancelled},
		{models.GroupProtected, models.ActionCancelled},
		{models.GroupIgnored, models.ActionCancelled},
		{models.GroupResolved, models.ActionCancelled},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			d := newTestDB(t)
			g := testGroup("movie:tmdb:1", 1, 1)
			g.Status = models.GroupQueued
			upsert(t, d, g)
			a := queueRemoval(t, d, g, 1)
			running := queueRemoval(t, d, g, 1)
			running.Status = models.ActionRunning
			must(t, d.Actions().Update(ctx, running))
			other := testGroup("movie:tmdb:2", 1, 1)
			upsert(t, d, other)
			otherRemoval := queueRemoval(t, d, other, 1)

			must(t, d.Groups().UpdateStatus(ctx, g.ID, tt.status, "because"))
			if got := actionStatus(t, d, a); got != tt.want {
				t.Fatalf("queued removal after status %q = %q, want %q", tt.status, got, tt.want)
			}
			if got := actionStatus(t, d, running); got != models.ActionRunning {
				t.Fatalf("running action = %q, want running", got)
			}
			if got := actionStatus(t, d, otherRemoval); got != models.ActionPending {
				t.Fatalf("other group's removal = %q, want pending", got)
			}
			if tt.want == models.ActionCancelled {
				got, err := d.Actions().Get(ctx, a.ID)
				must(t, err)
				if got.FinishedAt == nil || !strings.HasPrefix(got.Message, "Cancelled") {
					t.Fatalf("cancelled action lacks FinishedAt/Message: %+v", got)
				}
			}
		})
	}
}
