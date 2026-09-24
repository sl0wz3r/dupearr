package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Full-disc backups (docs/DECISIONS.md D9).

func TestSettingsDiscRules(t *testing.T) {
	ts := newTestServer(t)
	var st models.Settings
	expect(t, ts.do(http.MethodGet, "/api/v1/config/settings", nil), http.StatusOK, &st)
	if !st.DetectDiscs || st.AllowDiscRemoval || !st.KeepPlayableCopy {
		t.Fatalf("disc defaults %+v", st)
	}
	// Disc removal needs a recycle bin (a disc is only ever moved there).
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"allowDiscRemoval": true})); !hasProp(props, "allowDiscRemoval") {
		t.Fatalf("props %v", props)
	}
	bin := filepath.Join(t.TempDir(), "recycle")
	rr := ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"allowDiscRemoval": true, "recycleBinPath": bin, "keepPlayableCopy": false})
	expect(t, rr, http.StatusAccepted, &st)
	if !st.AllowDiscRemoval || st.KeepPlayableCopy {
		t.Fatalf("saved %+v", st)
	}
	// Removing the recycle bin while disc removal is on is refused too.
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"recycleBinPath": ""})); !hasProp(props, "allowDiscRemoval") {
		t.Fatalf("props %v", props)
	}
	// Disc removal without the filesystem method: saved, with a warning.
	rr = ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"deletionMethods": []string{"arr", "plex"}})
	expect(t, rr, http.StatusAccepted, nil)
	if w := rr.Header().Get("X-Dupearr-Warning"); !strings.Contains(w, "filesystem method") {
		t.Fatalf("warning %q", w)
	}
}

func TestSettingsDiscDetectionNeedsMappings(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	srv := ts.seedServer("Plex")
	lib := ts.seedLibrary(srv.ID, "1", "Movies")
	lib.Type, lib.Locations, lib.Enabled = "movie", []string{"/data/movies"}, true
	if err := ts.db.Libraries().Update(ctx, &lib); err != nil {
		t.Fatal(err)
	}
	rr := ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"detectDiscs": true})
	expect(t, rr, http.StatusAccepted, nil)
	if w := rr.Header().Get("X-Dupearr-Warning"); !strings.Contains(w, "path mapping") {
		t.Fatalf("warning %q", w)
	}
	m := models.PathMapping{SourceType: models.PathSourceServer, SourceID: srv.ID, RemotePath: "/data", LocalPath: t.TempDir()}
	if err := ts.db.PathMappings().Create(ctx, &m); err != nil {
		t.Fatal(err)
	}
	rr = ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"detectDiscs": true})
	expect(t, rr, http.StatusAccepted, nil)
	if w := rr.Header().Get("X-Dupearr-Warning"); w != "" {
		t.Fatalf("warning with a mapping: %q", w)
	}
}

// seedDiscGroup stores a pending group whose loser is a full disc.
func (ts *testServer) seedDiscGroup(key string) *models.DuplicateGroup {
	ts.t.Helper()
	g := ts.seedGroup(key, "Heat", models.GroupPending)
	f := &g.Files[1]
	f.Version.Key = "disc:1:abcdef"
	f.Version.MediaID = 0
	f.Version.Disc = &models.DiscInfo{Type: models.DiscUHDBluray, Root: "/data/movies/Heat (1995)", LocalRoot: "/mnt/movies/Heat (1995)",
		Discs: 1, FileCount: 312, Readable: true, Origin: models.DiscOriginFilesystem, TotalBytes: 60 << 30, FeatureBytes: 50 << 30,
		FreedBytes: 60 << 30, Removable: true, OwnedEntries: []string{"/mnt/movies/Heat (1995)/BDMV"}}
	g.Flags = []string{models.FlagFullDisc}
	if _, err := ts.db.Groups().Upsert(context.Background(), g); err != nil {
		ts.t.Fatal(err)
	}
	got, err := ts.db.Groups().Get(context.Background(), g.ID)
	if err != nil {
		ts.t.Fatal(err)
	}
	return got
}

func TestDuplicateSummaryDisc(t *testing.T) {
	ts := newTestServer(t)
	g := ts.seedDiscGroup("movie:tmdb:949")
	rr := ts.do(http.MethodGet, "/api/v1/duplicate", nil)
	var page struct {
		Records []struct {
			Files []struct {
				ID   int64           `json:"id"`
				Size int64           `json:"size"`
				Disc json.RawMessage `json:"disc"`
			} `json:"files"`
		} `json:"records"`
	}
	expect(t, rr, http.StatusOK, &page)
	if len(page.Records) != 1 || len(page.Records[0].Files) != 2 {
		t.Fatalf("records %+v", page.Records)
	}
	var discs int
	for _, f := range page.Records[0].Files {
		if len(f.Disc) == 0 {
			continue
		}
		discs++
		var d struct {
			Type      string `json:"type"`
			FileCount int    `json:"fileCount"`
			Discs     int    `json:"discs"`
		}
		if err := json.Unmarshal(f.Disc, &d); err != nil || d.Type != "uhd_bluray" || d.FileCount != 312 || d.Discs != 1 || f.Size != 60<<30 {
			t.Fatalf("disc summary %s size %d (%v)", f.Disc, f.Size, err)
		}
	}
	if discs != 1 {
		t.Fatalf("%d disc summaries", discs)
	}
	// The detail carries the full DiscInfo.
	var detail models.DuplicateGroup
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate/"+itoa64(g.ID), nil), http.StatusOK, &detail)
	if d := detail.Files[1].Version.Disc; d == nil || d.Origin != models.DiscOriginFilesystem || d.Type != models.DiscUHDBluray || d.FileCount != 312 {
		t.Fatalf("detail disc %+v", detail.Files[1].Version.Disc)
	}
}

func TestBulkApproveSkipsDiscRemovals(t *testing.T) {
	ts := newTestServer(t)
	g := ts.seedDiscGroup("movie:tmdb:949")
	var res bulkResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"action": "approve", "ids": []int64{g.ID}}), http.StatusOK, &res)
	if len(res.Succeeded) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "full-disc backup") {
		t.Fatalf("bulk %+v", res)
	}
}

func TestSchemaDiscOptions(t *testing.T) {
	ts := newTestServer(t)
	var schema struct {
		Criteria []struct {
			Type    string `json:"type"`
			Options []struct {
				Value string `json:"value"`
				Label string `json:"label"`
			} `json:"options"`
		} `json:"criteria"`
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/profile/schema", nil), http.StatusOK, &schema)
	found := map[string]string{}
	for _, c := range schema.Criteria {
		for _, o := range c.Options {
			found[c.Type+"/"+o.Value] = o.Label
		}
	}
	if found["source/disc"] != "Full disc (BDMV/VIDEO_TS/ISO)" || found["container/m2ts"] != "M2TS" {
		t.Fatalf("schema options: disc %q m2ts %q", found["source/disc"], found["container/m2ts"])
	}
}

// Loose clip sets (docs/DECISIONS.md D9 "Loose clip sets").

func TestLooseClipSummaryAndApproval(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	// A group stored by the old scanner: a loose clip of a flattened Blu-ray marked for removal.
	g := ts.seedGroup("movie:tmdb:1022789", "Elemental", models.GroupPending)
	g.Files[1].Version.Parts[0].Path = "/data/movies/Elemental (2023)/00174.m2ts"
	g.Files[1].Version.Container = "ts"
	if _, err := ts.db.Groups().Upsert(ctx, g); err != nil {
		t.Fatal(err)
	}
	// A group with a clip set (the merged disc version).
	cs := ts.seedGroup("movie:tmdb:87101", "Terminator Genisys", models.GroupProtected)
	f := &cs.Files[1]
	f.Version.Key, f.Version.MediaID = "disc:1:0123456789abcdef0123456789abcdef01234567", 0
	f.Version.Disc = &models.DiscInfo{Type: models.DiscBlurayClips, Root: "/data/movies/Terminator Genisys (2015)", Discs: 1,
		FileCount: 67, ClipCount: 67, MainClip: "00010.m2ts", MainFeature: "00010.m2ts", Origin: models.DiscOriginPlex,
		TotalBytes: 70 << 30, FeatureBytes: 68 << 30, PlexMediaIDs: []int64{11, 12}}
	f.Decision, f.EngineDecision, f.Protected = models.DecisionKeep, models.DecisionKeep, true
	if _, err := ts.db.Groups().Upsert(ctx, cs); err != nil {
		t.Fatal(err)
	}

	var page struct {
		Records []struct {
			ID    int64 `json:"id"`
			Files []struct {
				ID       int64 `json:"id"`
				DiscClip bool  `json:"discClip"`
				Disc     *struct {
					Type      string `json:"type"`
					ClipCount int    `json:"clipCount"`
				} `json:"disc"`
			} `json:"files"`
		} `json:"records"`
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate?pageSize=50", nil), http.StatusOK, &page)
	seen := 0
	for _, r := range page.Records {
		for i, file := range r.Files {
			switch {
			case r.ID == g.ID:
				seen++
				if file.DiscClip != (i == 1) || file.Disc != nil {
					t.Fatalf("file %d: discClip %v disc %v", i, file.DiscClip, file.Disc)
				}
			case r.ID == cs.ID && i == 1:
				seen++
				if file.DiscClip || file.Disc == nil || file.Disc.Type != "bluray_clips" || file.Disc.ClipCount != 67 {
					t.Fatalf("clip set summary %+v", file)
				}
			}
		}
	}
	if seen != 3 {
		t.Fatalf("saw %d files", seen)
	}
	var detail models.DuplicateGroup
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate/"+itoa64(cs.ID), nil), http.StatusOK, &detail)
	if d := detail.Files[1].Version.Disc; d == nil || d.ClipCount != 67 || d.MainClip != "00010.m2ts" || len(d.PlexMediaIDs) != 2 {
		t.Fatalf("detail disc %+v", d)
	}

	// Approving the removal of a clip is refused (single and bulk); nothing is queued.
	rr := ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "full-disc backup") {
		t.Fatalf("approve: %d %s", rr.Code, rr.Body.String())
	}
	var res bulkResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"action": "approve", "ids": []int64{g.ID}}), http.StatusOK, &res)
	if len(res.Succeeded) != 0 || len(res.Failed) != 1 {
		t.Fatalf("bulk %+v", res)
	}
	if acts, err := ts.db.Actions().ListByGroup(ctx, g.ID); err != nil || len(acts) != 0 {
		t.Fatalf("actions %+v %v", acts, err)
	}
}
