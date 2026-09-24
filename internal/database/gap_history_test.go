package database

// GAP-12 (docs/SECURITY.md): security events are not pruned with the history retention (a
// restored backup or a session that shortens it must not erase the record of what it did); only
// DeleteSecurityOlderThan removes them.

import (
	"context"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

func TestSecurityEventsOutliveTheHistoryRetention(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	for _, typ := range []string{models.EventFileDeleted, models.EventSecurity} {
		if err := d.History().Add(ctx, &models.HistoryEvent{EventType: typ, Title: typ, CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := d.History().DeleteOlderThan(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("DeleteOlderThan = %d, %v; want only the regular event", n, err)
	}
	page, err := d.History().List(ctx, nil, 0, store.Paging{})
	if err != nil || page.TotalRecords != 1 || page.Records[0].EventType != models.EventSecurity {
		t.Fatalf("left: %+v, %v", page.Records, err)
	}
	n, err = d.History().DeleteSecurityOlderThan(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("DeleteSecurityOlderThan = %d, %v", n, err)
	}
}
