package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// radarrStuckImportJSON is a Radarr v5 queue page with a completed download Radarr refuses to
// import (QueueResource as Radarr serializes it: camelCase enums, statusMessages titled with the
// release title), a download in progress for another movie and a record of an unknown item.
const radarrStuckImportJSON = `{"page":1,"pageSize":200,"sortKey":"timeleft","sortDirection":"ascending","totalRecords":3,"records":[
  {"id":11,"movieId":42,"title":"Toy.Story.5.2026.2160p.WEB-DL.DDP5.1.H.265-GRP","status":"completed",
   "trackedDownloadStatus":"warning","trackedDownloadState":"importPending",
   "statusMessages":[{"title":"Toy.Story.5.2026.2160p.WEB-DL.DDP5.1.H.265-GRP","messages":["Not an upgrade for existing movie file. Existing quality: Remux-2160p. New Quality WEBDL-2160p."]}],
   "downloadId":"SABnzbd_nzo_abc","downloadClient":"SABnzbd","indexer":"NZBgeek","outputPath":"/downloads/complete/Toy.Story.5.2026.2160p.WEB-DL.DDP5.1.H.265-GRP",
   "protocol":"usenet","size":12000000000,"sizeleft":0},
  {"id":12,"movieId":44,"title":"The.Matrix.1999.1080p.BluRay-GRP","status":"downloading","trackedDownloadStatus":"ok",
   "trackedDownloadState":"downloading","statusMessages":[],"sizeleft":100},
  {"id":13,"title":"Unknown.Release","status":"downloading","trackedDownloadState":"downloading"}
]}`

func TestQueueSummarizesEntries(t *testing.T) {
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/queue", http.StatusOK, radarrStuckImportJSON)
	items, err := newTestClient(t, models.ArrRadarr, f.URL()).Queue(context.Background())
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(items) != 2 || items[42] == nil || items[44] == nil {
		t.Fatalf("items = %v, want movies 42 and 44 (the unknown item is skipped)", items)
	}
	want := QueueItem{Count: 1, Entries: []QueueEntry{{
		Title: "Toy.Story.5.2026.2160p.WEB-DL.DDP5.1.H.265-GRP", Status: "completed",
		TrackedDownloadState: "importPending", TrackedDownloadStatus: "warning",
		Messages: []string{"Not an upgrade for existing movie file. Existing quality: Remux-2160p. New Quality WEBDL-2160p."},
	}}}
	if !reflect.DeepEqual(*items[42], want) {
		t.Fatalf("movie 42:\n got %+v\nwant %+v", *items[42], want)
	}
	if got := items[42].Entries[0].Label(); got != "Downloaded - Waiting to Import" {
		t.Errorf("label = %q", got)
	}
	if got := fmt.Sprintf("%+v", items); strings.Contains(got, "SABnzbd") || strings.Contains(got, "NZBgeek") || strings.Contains(got, "/downloads") {
		t.Errorf("the download id, client, indexer or output path was kept: %s", got)
	}
	// The ids are exactly the busy items (QueueItemIDs is Queue reduced to them).
	ids, err := newTestClient(t, models.ArrRadarr, f.URL()).QueueItemIDs(context.Background())
	if err != nil || !reflect.DeepEqual(ids, map[int64]bool{42: true, 44: true}) {
		t.Fatalf("QueueItemIDs = %v, %v", ids, err)
	}
}

func TestQueueSonarrGroupsBySeries(t *testing.T) {
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/queue", http.StatusOK, `{"page":1,"pageSize":200,"totalRecords":3,"records":[
	  {"seriesId":7,"episodeId":1001,"title":"The.Expanse.S01E01.1080p","status":"completed","trackedDownloadStatus":"warning",
	   "trackedDownloadState":"importBlocked","statusMessages":[
	     {"title":"The.Expanse.S01E01.1080p.mkv","messages":["Not an upgrade for existing episode file(s)"]},
	     {"title":"Sample.mkv","messages":[]}]},
	  {"seriesId":7,"episodeId":1002,"title":"The.Expanse.S01E02.1080p","status":"downloading","trackedDownloadState":"downloading"},
	  {"movieId":99,"title":"not a series record","status":"downloading"}]}`)
	items, err := newTestClient(t, models.ArrSonarr, f.URL()).Queue(context.Background())
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	it := items[7]
	if len(items) != 1 || it == nil || it.Count != 2 || len(it.Entries) != 2 {
		t.Fatalf("items = %v, want series 7 with two entries", items)
	}
	e := it.Entries[0]
	// Messages titled with a file name keep it; a title without messages is listed on its own.
	if want := []string{"The.Expanse.S01E01.1080p.mkv: Not an upgrade for existing episode file(s)", "Sample.mkv"}; !reflect.DeepEqual(e.Messages, want) {
		t.Errorf("messages = %q, want %q", e.Messages, want)
	}
	if got := e.Label(); got != "Downloaded - Unable to Import Automatically" {
		t.Errorf("label = %q", got)
	}
	if got := it.Entries[1].Label(); got != "Downloading" {
		t.Errorf("label = %q", got)
	}
}

func TestQueueCapsSummaries(t *testing.T) {
	f := newFakeArr(t, "")
	long := strings.Repeat("x", 1000)
	var recs []string
	for i := 0; i < 14; i++ {
		msgs := make([]string, 0, 8)
		for j := 0; j < 8; j++ {
			msgs = append(msgs, fmt.Sprintf(`"message %d\u0007 with\nnew line %s"`, j, long))
		}
		recs = append(recs, fmt.Sprintf(`{"movieId":42,"title":"%s\u202e","status":"completed","errorMessage":"%s",
		  "statusMessages":[{"title":"Release","messages":[%s]}]}`, long, long, strings.Join(msgs, ",")))
	}
	f.json(http.MethodGet, "/api/v3/queue", http.StatusOK, `{"page":1,"pageSize":200,"totalRecords":14,"records":[`+strings.Join(recs, ",")+`]}`)
	items, err := newTestClient(t, models.ArrRadarr, f.URL()).Queue(context.Background())
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	it := items[42]
	if it == nil || it.Count != 14 || len(it.Entries) != models.MaxArrQueueEntries {
		t.Fatalf("item = %+v, want 14 counted and %d kept", it, models.MaxArrQueueEntries)
	}
	e := it.Entries[0]
	if n := len([]rune(e.Title)); n != models.MaxArrQueueTitleRunes || !strings.HasSuffix(e.Title, "…") {
		t.Errorf("title: %d runes", n)
	}
	if n := len([]rune(e.ErrorMessage)); n != models.MaxArrQueueTextRunes {
		t.Errorf("error message: %d runes", n)
	}
	if len(e.Messages) != models.MaxArrQueueMessages {
		t.Fatalf("messages = %d, want %d", len(e.Messages), models.MaxArrQueueMessages)
	}
	for _, m := range e.Messages {
		if n := len([]rune(m)); n > models.MaxArrQueueTextRunes || strings.ContainsAny(m, "\a\n\u202e") {
			t.Errorf("message not cleaned: %d runes %q", n, m[:40])
		}
		if !strings.HasPrefix(m, "Release: message ") {
			t.Errorf("message = %q, want the message title kept", m[:40])
		}
	}
}

// Secrets are masked before a text is shortened: a cap that cut the '@' after URL user info or the
// closing quote of a JSON field would otherwise leave the secret unmasked (the rules need them).
func TestQueueMasksSecretsBeforeCapping(t *testing.T) {
	// Each secret ends exactly one rune before its cap, so the cap cuts the token right after it.
	userinfo := func(limit int, secret string) string {
		return strings.Repeat("a", limit-1-len("http://admin:")-len(secret)) + "http://admin:" + secret + "@sab:8080/api"
	}
	jsonField := func(limit int, secret string) string {
		return strings.Repeat("b", limit-1-len(`"password":"`)-len(secret)) + `"password":"` + secret + `"}`
	}
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	title := userinfo(models.MaxArrQueueTitleRunes, "TitlePassw0rd")
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/queue", http.StatusOK, fmt.Sprintf(`{"page":1,"pageSize":200,"totalRecords":1,"records":[
	  {"movieId":42,"title":%s,"status":"completed","trackedDownloadStatus":"warning","trackedDownloadState":"importPending",
	   "errorMessage":%s,
	   "statusMessages":[{"title":%s,"messages":[%s]},{"title":%s,"messages":["Not an upgrade"]}]}]}`,
		quote(title), quote(userinfo(models.MaxArrQueueTextRunes, "ErrorPassw0rd")),
		quote(title), quote(jsonField(models.MaxArrQueueTextRunes, "MessagePassw0rd")),
		quote(userinfo(models.MaxArrQueueTitleRunes, "FilePassw0rd"))))
	items, err := newTestClient(t, models.ArrRadarr, f.URL()).Queue(context.Background())
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	e := items[42].Entries[0]
	got := fmt.Sprintf("%+v", e)
	for _, secret := range []string{"TitlePass", "ErrorPass", "MessagePass", "FilePass"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %s survived the cap: %s", secret, got)
		}
	}
	if !strings.Contains(e.ErrorMessage, "http://admin:(removed)@") || len(e.Messages) != 2 ||
		!strings.Contains(e.Messages[0], `"password":"(removed)"`) {
		t.Errorf("entry = %+v, want the secrets masked", e)
	}
	if n := len([]rune(e.ErrorMessage)); n > models.MaxArrQueueTextRunes {
		t.Errorf("error message: %d runes", n)
	}
}

// A summary field of an unexpected shape is dropped: it never fails the read, which would send
// every group of the media type to review.
func TestQueueToleratesMalformedSummaries(t *testing.T) {
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/queue", http.StatusOK, `{"page":1,"pageSize":200,"totalRecords":4,"records":[
	  {"movieId":1,"title":5,"status":{"x":1},"trackedDownloadState":null,"trackedDownloadStatus":[1],"statusMessages":"oops","errorMessage":true},
	  {"movieId":2,"title":"B","statusMessages":[1,"x",{"title":7,"messages":"not a list"},{"title":"B","messages":[3,"kept",null]}]},
	  {"movieId":3,"statusMessages":{"title":"x"}},
	  {"movieId":4,"title":"D","statusMessages":null}]}`)
	items, err := newTestClient(t, models.ArrRadarr, f.URL()).Queue(context.Background())
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %v, want all four busy", items)
	}
	if e := items[1].Entries[0]; e.Title != "" || e.Status != "" || e.Messages != nil || e.ErrorMessage != "" || e.Label() != "Downloading" {
		t.Errorf("malformed entry = %+v", e)
	}
	if got := items[2].Entries[0].Messages; !reflect.DeepEqual(got, []string{"kept"}) {
		t.Errorf("messages = %q", got)
	}
	// Only the item ids are decoded strictly: a wrong id type still fails the read, as before.
	f.json(http.MethodGet, "/api/v3/queue", http.StatusOK, `{"page":1,"pageSize":200,"totalRecords":1,"records":[{"movieId":"1"}]}`)
	if _, err := newTestClient(t, models.ArrRadarr, f.URL()).Queue(context.Background()); err == nil {
		t.Fatal("a movie id of the wrong type was accepted")
	}
}

func TestQueueEntryLabel(t *testing.T) {
	cases := []struct {
		status, state, tracked, want string
	}{
		{"downloading", "downloading", "ok", "Downloading"},
		{"", "", "", "Downloading"},
		{"paused", "downloading", "ok", "Paused"},
		{"queued", "downloading", "ok", "Queued"},
		{"completed", "importPending", "warning", "Downloaded - Waiting to Import"},
		{"completed", "importBlocked", "warning", "Downloaded - Unable to Import Automatically"},
		{"completed", "importing", "ok", "Downloaded - Importing"},
		{"completed", "failedPending", "warning", "Downloaded - Waiting to Process"},
		{"completed", "imported", "ok", "Downloaded"},
		{"delay", "downloading", "ok", "Pending"},
		{"downloadClientUnavailable", "downloading", "warning", "Pending - Download client is unavailable"},
		{"failed", "failed", "ok", "Download failed"},
		{"warning", "downloading", "warning", "Download warning"},
		{"completed", "importPending", "error", "Import failed"},
		{"downloading", "downloading", "error", "Download failed"},
		{"COMPLETED", "IMPORTPENDING", "Warning", "Downloaded - Waiting to Import"},
	}
	for _, tc := range cases {
		e := QueueEntry{Status: tc.status, TrackedDownloadState: tc.state, TrackedDownloadStatus: tc.tracked}
		if got := e.Label(); got != tc.want {
			t.Errorf("Label(%s, %s, %s) = %q, want %q", tc.status, tc.state, tc.tracked, got, tc.want)
		}
	}
}

// A huge queue keeps at most maxQueueEntriesKept summaries in total; every record still counts.
func TestQueueKeepsBoundedSummaries(t *testing.T) {
	f := newFakeArr(t, "")
	queueServer(t, f, models.ArrRadarr, 4500)
	items, err := newTestClient(t, models.ArrRadarr, f.URL()).Queue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	counted, kept := 0, 0
	for _, it := range items {
		counted += it.Count
		kept += len(it.Entries)
	}
	// 4500 records, every 50th of an unknown item (see queueServer).
	if counted != 4410 || kept != maxQueueEntriesKept {
		t.Fatalf("counted %d, kept %d; want 4410 and %d", counted, kept, maxQueueEntriesKept)
	}
	if e := items[1000].Entries[0]; e.TrackedDownloadState != "importPending" || e.Label() != "Downloading" {
		t.Errorf("entry = %+v", e)
	}
}

func TestTitleSlugs(t *testing.T) {
	t.Run("radarr", func(t *testing.T) {
		f := newRadarrFake(t)
		files, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if got := byFileID(files)[118].TitleSlug; got != "335984" {
			t.Errorf("titleSlug = %q, want Radarr's (the TMDB id)", got)
		}
		if got := byFileID(files)[200].TitleSlug; got != "" {
			t.Errorf("a movie without titleSlug got %q", got)
		}
	})
	t.Run("sonarr", func(t *testing.T) {
		f := newSonarrFake(t)
		f.json(http.MethodGet, "/api/v3/series", http.StatusOK,
			strings.Replace(sonarrSeriesJSON, `"title": "The Expanse",`, `"title": "The Expanse", "titleSlug": " the-expanse ",`, 1))
		f.json(http.MethodGet, "/api/v3/series/7", http.StatusOK,
			strings.Replace(jsonElem(t, sonarrSeriesJSON, 0), `"title": "The Expanse",`, `"title": "The Expanse", "titleSlug": "the-expanse",`, 1))
		f.json(http.MethodGet, "/api/v3/episodefile/501", http.StatusOK, jsonElem(t, sonarrEpisodeFiles7JSON, 0))
		c := newTestClient(t, models.ArrSonarr, f.URL())
		files, err := c.TrackedFiles(context.Background(), TrackedFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if got := byFileID(files)[501].TitleSlug; got != "the-expanse" {
			t.Errorf("titleSlug = %q", got)
		}
		f.handle(http.MethodGet, "/api/v3/episode", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, sonarrEpisodes7JSON)
		})
		tf, err := c.FileByID(context.Background(), 501)
		if err != nil {
			t.Fatal(err)
		}
		if tf.TitleSlug != "the-expanse" {
			t.Errorf("FileByID titleSlug = %q", tf.TitleSlug)
		}
	})
}
