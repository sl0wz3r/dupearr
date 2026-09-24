package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

func TestRecordWritesABoundedSecurityEvent(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "dupearr.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	Record(ctx, nil, nil, KindLogin, "ignored without a store")

	cancelled, cancel := context.WithCancel(ctx)
	cancel() // a request that has ended still records what it did
	Record(cancelled, db, nil, KindHostSettings, "Host settings changed", "changes", strings.Repeat("x", 500)+"\nforged line", "kind", "spoofed", 7)

	page, err := db.History().List(ctx, []string{models.EventSecurity}, 0, store.Paging{})
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("events = %+v, %v", page.Records, err)
	}
	e := page.Records[0]
	var d map[string]string
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d["kind"] != KindHostSettings || len([]rune(d["changes"])) > maxValue+1 || strings.ContainsAny(e.Message, "\n\r") {
		t.Fatalf("event = %+v, data %v", e, d)
	}
}
