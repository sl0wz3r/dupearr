package plex

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestAllItemsEmptyPageBeforeTotalFails: an empty page while fewer rows than the reported total
// were listed is a truncated listing (a PMS hiccup, a proxy), never the end of the library. A
// silently short listing leaves the shared-file (multi-episode) index incomplete, so a file
// another item still uses could look removable.
func TestAllItemsEmptyPageBeforeTotalFails(t *testing.T) {
	for _, mode := range []string{"totalSize", "header"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				total := `,"totalSize":5`
				if mode == "header" {
					total = ""
					w.Header().Set("X-Plex-Container-Total-Size", "5")
				}
				if calls == 1 {
					writeJSON(w, 200, `{"MediaContainer":{"size":2`+total+`,"Metadata":[{"ratingKey":"1","type":"movie"},{"ratingKey":"2","type":"movie"}]}}`)
					return
				}
				writeJSON(w, 200, `{"MediaContainer":{"size":0`+total+`}}`)
			})
			items, err := newTestClient(f, "").AllItems(context.Background(), "1", models.MediaTypeMovie)
			if err == nil || !strings.Contains(err.Error(), "ended early") {
				t.Fatalf("items = %d, err = %v; want a truncated-listing error", len(items), err)
			}
			if items != nil {
				t.Fatalf("a failed listing must not return items: %d", len(items))
			}
		})
	}
}
