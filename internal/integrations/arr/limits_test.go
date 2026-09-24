package arr

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Resource bounds against a hostile or broken *arr (SEC-020): a compromised Radarr/Sonarr, a
// container answering on its URL or a MITM on a plain-http connection must not be able to make
// Dupearr allocate gigabytes while decoding a response.

func TestResponseLimitDefaults(t *testing.T) {
	l := defaultLimits
	switch {
	case l.body > 16<<20:
		t.Errorf("single-resource cap = %d MiB, want a few MiB", l.body>>20)
	case l.listBody > 256<<20:
		t.Errorf("listing cap = %d MiB, want ≤ 256 MiB", l.listBody>>20)
	case l.element > 4<<20:
		t.Errorf("element cap = %d MiB, want ≤ 4 MiB", l.element>>20)
	case l.elements > 500_000:
		t.Errorf("element count cap = %d, want ≤ 500k", l.elements)
	case l.listMemory > 256<<20:
		t.Errorf("listing memory budget = %d MiB, want ≤ 256 MiB", l.listMemory>>20)
	}
}

// moviesClient serves body as GET /api/v3/movie and returns a Radarr client for it.
func moviesClient(t *testing.T, body string) *Client {
	t.Helper()
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/movie", http.StatusOK, body)
	f.json(http.MethodGet, "/api/v3/tag", http.StatusOK, `[]`)
	return newTestClient(t, models.ArrRadarr, f.URL())
}

func movieJSON(id int, extra string) string {
	return fmt.Sprintf(`{"id":%d,"title":"M%d","tmdbId":%d,"movieFileId":%d,"hasFile":true%s,
		"movieFile":{"id":%d,"movieId":%d,"path":"/m/%d.mkv","size":1,"dateAdded":"2024-01-01T00:00:00Z"}}`,
		id, id, id, id, extra, id, id, id)
}

// TestListingElementCap: one element inflated with a huge array (a few bytes per entry, 8+ bytes
// once decoded) is refused instead of being buffered and decoded.
func TestListingElementCap(t *testing.T) {
	tags := strings.TrimSuffix(strings.Repeat("1,", int(defaultLimits.element)), ",")
	c := moviesClient(t, "["+movieJSON(1, "")+","+movieJSON(2, `,"tags":[`+tags+`]`)+"]")
	_, err := c.TrackedFiles(context.Background(), TrackedFilter{})
	if err == nil || !strings.Contains(err.Error(), "element") {
		t.Fatalf("err = %v, want an element size error", err)
	}
}

// TestListingElementCount: a listing of more elements than any library has ("{}," decodes into a
// struct of hundreds of bytes) is refused.
func TestListingElementCount(t *testing.T) {
	body := "[" + strings.TrimSuffix(strings.Repeat("{},", 1001), ",") + "]"
	c := moviesClient(t, body)
	c.limits.elements = 1000
	_, err := c.TrackedFiles(context.Background(), TrackedFilter{})
	if err == nil || !strings.Contains(err.Error(), "more than 1000") {
		t.Fatalf("err = %v, want an element count error", err)
	}
}

// TestListingMemoryBudget: many small elements, each within the element cap, whose decoded size
// adds up past the listing budget.
func TestListingMemoryBudget(t *testing.T) {
	tags := strings.TrimSuffix(strings.Repeat("7,", 20_000), ",") // ~40 KB → ~160 KB decoded
	var rows []string
	for i := 1; i <= 50; i++ {
		rows = append(rows, movieJSON(i, `,"tags":[`+tags+`]`))
	}
	c := moviesClient(t, "["+strings.Join(rows, ",")+"]")
	c.limits.listMemory = 2 << 20
	_, err := c.TrackedFiles(context.Background(), TrackedFilter{})
	if err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("err = %v, want the listing memory error", err)
	}
}

// TestSingleResourceBodyCap: responses other than library listings are capped at a few MiB.
func TestSingleResourceBodyCap(t *testing.T) {
	f := newFakeArr(t, "")
	pad := strings.Repeat("a", int(defaultLimits.body)+1024)
	f.json(http.MethodGet, "/api/v3/system/status", http.StatusOK, `{"appName":"Radarr","version":"5.0","pad":"`+pad+`"}`)
	_, err := newTestClient(t, models.ArrRadarr, f.URL()).Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want a body size error", err)
	}
}

// TestListingDecodingStillWorks: streaming decode keeps the listing semantics (order, null, empty,
// malformed and truncated bodies).
func TestListingDecodingStillWorks(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		c := moviesClient(t, "["+movieJSON(1, `,"tags":[]`)+","+movieJSON(2, "")+"]")
		files, err := c.TrackedFiles(context.Background(), TrackedFilter{})
		if err != nil || len(files) != 2 || files[0].Path != "/m/1.mkv" || files[1].Path != "/m/2.mkv" {
			t.Fatalf("files = %+v, err = %v", files, err)
		}
	})
	for name, body := range map[string]string{"null": "null", "empty array": "[]"} {
		t.Run(name, func(t *testing.T) {
			files, err := moviesClient(t, body).TrackedFiles(context.Background(), TrackedFilter{})
			if err != nil || len(files) != 0 {
				t.Fatalf("files = %+v, err = %v", files, err)
			}
		})
	}
	for name, body := range map[string]string{
		"empty":      "",
		"truncated":  "[" + movieJSON(1, ""),
		"object":     `{"id":1}`,
		"wrong type": `[{"id":"one"}]`,
		"scalar":     `[1,2]`,
		"no close":   "[" + movieJSON(1, "") + ",",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := moviesClient(t, body).TrackedFiles(context.Background(), TrackedFilter{}); err == nil {
				t.Fatal("expected a decoding error")
			}
		})
	}
}

func TestApproxSize(t *testing.T) {
	small := approxSize(&movieResource{Title: "x"})
	withTags := approxSize(&movieResource{Title: "x", Tags: make([]int64, 1000)})
	if small <= 0 || withTags-small < 8000 {
		t.Errorf("approxSize: %d without tags, %d with 1000 tags", small, withTags)
	}
	withFile := approxSize(&movieResource{MovieFile: &fileResource{Path: strings.Repeat("p", 500),
		Languages: []namedRef{{Name: "English"}}, MediaInfo: &mediaInfoResource{}}})
	if withFile < 500+int64(len("English")) {
		t.Errorf("approxSize with file = %d", withFile)
	}
}
