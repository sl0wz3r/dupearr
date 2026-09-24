package plex

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Resource bounds against a hostile or broken PMS (SEC-020): a server (or a MITM on the usual
// plain-http LAN connection) must not be able to make Dupearr allocate gigabytes.

// TestResponseBodyCap: one response larger than maxJSONBody is refused before decoding.
func TestResponseBodyCap(t *testing.T) {
	pad := strings.Repeat("a", maxJSONBody+1024)
	if maxJSONBody > 16<<20 {
		t.Fatalf("maxJSONBody = %d MiB; a page of 100 rows is well under 1 MiB, keep the cap ≤ 16 MiB", maxJSONBody>>20)
	}
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"MediaContainer":{"size":0,"friendlyName":"`+pad+`"}}`)
	})
	if _, err := newTestClient(f, "").Sections(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want a body size error", err)
	}
}

// TestResponseValueBudget: a small body can still decode into millions of structs ("{}," is three
// bytes, a metadataDTO hundreds). Such a response is rejected before it is decoded.
func TestResponseValueBudget(t *testing.T) {
	for name, elem := range map[string]string{"objects": "{}", "scalars": "1"} {
		t.Run(name, func(t *testing.T) {
			rows := strings.TrimSuffix(strings.Repeat(elem+",", maxJSONValues+10), ",")
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, `{"MediaContainer":{"size":1,"Metadata":[`+rows+`]}}`)
			})
			_, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
			if err == nil || !strings.Contains(err.Error(), "JSON values") {
				t.Fatalf("err = %v, want a value budget error", err)
			}
		})
	}
}

func TestTooManyJSONValues(t *testing.T) {
	tests := []struct {
		json  string
		limit int
		want  bool
	}{
		{`{"a":1,"b":[1,2,3]}`, 10, false},
		{`{"a":1,"b":[1,2,3]}`, 4, true},
		// Structural characters inside strings (escaped quotes included) are not counted.
		{`{"a":"{[,,,,,,,,]}","b":"\"{,,,,,,\""}`, 3, false},
		{`[` + strings.Repeat(`{},`, 99) + `{}]`, 200, false},
		{`[` + strings.Repeat(`{},`, 99) + `{}]`, 150, true},
	}
	for _, tt := range tests {
		if got := tooManyJSONValues([]byte(tt.json), tt.limit); got != tt.want {
			t.Errorf("tooManyJSONValues(%s, %d) = %v, want %v", tt.json, tt.limit, got, tt.want)
		}
	}
}

// endlessServer returns pageSize fresh rows for every request, reporting totalSize when total ≥ 0,
// and stops (empty page) after maxPages so a regression cannot loop for millions of pages.
func endlessServer(t *testing.T, total, maxPages int, row func(i int) string) *fakeServer {
	t.Helper()
	pages := 0
	return newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size"))
		mc := ""
		if total >= 0 {
			mc = fmt.Sprintf(`"totalSize":%d,`, total)
		}
		if pages > maxPages {
			writeJSON(w, 200, `{"MediaContainer":{`+mc+`"size":0}}`)
			return
		}
		rows := make([]string, 0, size)
		for i := start; i < start+size; i++ {
			rows = append(rows, row(i))
		}
		writeJSON(w, 200, `{"MediaContainer":{`+mc+`"size":`+strconv.Itoa(size)+`,"Metadata":[`+strings.Join(rows, ",")+`]}}`)
	})
}

func movieRow(i int) string {
	return fmt.Sprintf(`{"ratingKey":"%d","type":"movie","Media":[{"id":%d,"Part":[{"id":%d,"file":"/m/%d.mkv"}]}]}`, i+1, i+1, i+1, i)
}

// TestAllItemsFailsFarBeyondReportedTotal: a server that reports 5 items but keeps returning fresh
// rating keys is broken or hostile; the listing must fail instead of accumulating rows until the
// global item cap.
func TestAllItemsFailsFarBeyondReportedTotal(t *testing.T) {
	f := endlessServer(t, 5, 200, movieRow)
	c := newTestClient(f, "")
	c.pageSize = 10
	_, err := c.AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err == nil || !strings.Contains(err.Error(), "reported") {
		t.Fatalf("err = %v, want an error about the reported total", err)
	}
	if n := f.count(); n > 20 {
		t.Errorf("requests = %d; the listing should stop soon after exceeding the reported total", n)
	}
}

// TestAllItemsItemCap: without any total the listing stops at the item cap.
func TestAllItemsItemCap(t *testing.T) {
	if maxListedItems > 500_000 {
		t.Fatalf("maxListedItems = %d, want a home-library ceiling (≤ 500k)", maxListedItems)
	}
	f := endlessServer(t, -1, 200, movieRow)
	c := newTestClient(f, "")
	c.pageSize = 10
	c.maxItems = 95
	_, err := c.AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err == nil || !strings.Contains(err.Error(), "more than 95 items") {
		t.Fatalf("err = %v, want the item cap error", err)
	}
	if n := f.count(); n > 11 {
		t.Errorf("requests = %d, want ≤ 11", n)
	}
}

// TestAllItemsRetainedBudget: every page is small, but the accumulated rows (long titles and file
// paths) exceed the listing's memory budget.
func TestAllItemsRetainedBudget(t *testing.T) {
	long := strings.Repeat("x", 4096)
	f := endlessServer(t, -1, 200, func(i int) string {
		return fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"%s","Media":[{"id":%d,"Part":[{"id":%d,"file":"/m/%s"}]}]}`,
			i+1, long, i+1, i+1, long)
	})
	c := newTestClient(f, "")
	c.pageSize = 10
	c.maxListingBytes = 1 << 20
	_, err := c.AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("err = %v, want the listing size error", err)
	}
	if n := f.count(); n > 20 {
		t.Errorf("requests = %d; the listing should stop once 1 MiB of rows is held", n)
	}
}

// TestAllItemsBudgetsFitLargeLibraries: the default budgets hold a large, realistic library.
func TestAllItemsBudgetsFitLargeLibraries(t *testing.T) {
	ref, ok := toItemRef(&metadataDTO{
		RatingKey: "123456", Type: "episode", Title: "An Episode Title Of Average Length",
		GUID: "plex://episode/5d9c0a1b2c3d4e5f6a7b8c9d", GrandparentTitle: "A Show",
		Guids: list[guidDTO]{{ID: "imdb://tt1234567"}, {ID: "tmdb://1234567"}, {ID: "tvdb://12345678"}},
		Media: list[mediaDTO]{{ID: 1, Parts: list[partDTO]{{ID: 2, File: "/data/media/tv/A Show (2010)/Season 01/A Show - S01E01 - An Episode Title Of Average Length [WEBDL-1080p][EAC3 5.1][h264]-GROUP.mkv"}}}},
	}, models.MediaTypeEpisode)
	if !ok {
		t.Fatal("row rejected")
	}
	per := ref.approxBytes() + seenEntryBytes + int64(len(ref.RatingKey))
	if per <= 0 || per > 4096 {
		t.Fatalf("approxBytes = %d, want a plausible estimate", per)
	}
	if lib := int64(maxListedItems) * per; lib > defaultMaxListingBytes {
		t.Errorf("a %d-item library (~%d MiB) does not fit the %d MiB listing budget", maxListedItems, lib>>20, defaultMaxListingBytes>>20)
	}
}

// TestAllItemsBudgetCountsRejectedRows: rows that are not kept (another type, …) still leave their
// rating key in the de-duplication set; huge keys on such rows must count against the listing
// budget instead of growing it without bound.
func TestAllItemsBudgetCountsRejectedRows(t *testing.T) {
	big := strings.Repeat("9", 256<<10)
	f := endlessServer(t, -1, 200, func(i int) string {
		return fmt.Sprintf(`{"ratingKey":"%d%s","type":"show","title":"x"}`, i+1, big)
	})
	c := newTestClient(f, "")
	c.pageSize = 1
	c.maxListingBytes = 1 << 20
	_, err := c.AllItems(context.Background(), "1", models.MediaTypeMovie)
	if err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("err = %v, want the listing size error", err)
	}
	if n := f.count(); n > 8 {
		t.Errorf("requests = %d; the listing should stop once 1 MiB of rating keys is held", n)
	}
}
