package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestRestoreSummaryCountsSupportedKinds: the restore summary shows confirmed *arr links only with
// two or more enabled servers of a supported kind (models.SupportedMediaServerKinds): kinds "plex"
// and "" count, as before; a stored row of another kind does not.
func TestRestoreSummaryCountsSupportedKinds(t *testing.T) {
	if got := supportedKindsSQL(); got != "'plex', ''" {
		t.Fatalf("supportedKindsSQL = %q, want the legacy list", got)
	}
	for _, tc := range []struct {
		second    models.MediaServerKind
		wantLinks bool
	}{
		{"jellyfin", false},
		{"", true},
		{models.MediaServerPlex, true},
	} {
		t.Run("second server kind "+string(tc.second), func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			var ids []int64
			for _, s := range []models.MediaServer{
				{Name: "Plex A", Kind: models.MediaServerPlex, URL: "http://a", Token: "t", Enabled: true},
				{Name: "Second", Kind: tc.second, URL: "http://b", Token: "t", Enabled: true},
			} {
				if err := f.db.MediaServers().Create(ctx, &s); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, s.ID)
			}
			radarr := &models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr:7878", APIKey: "k", Enabled: true,
				ServerIDs: ids, LinksConfirmed: true}
			if err := f.db.ArrInstances().Create(ctx, radarr); err != nil {
				t.Fatal(err)
			}
			cfg, db := validParts(t, f)
			radarr.LinksConfirmed = false
			if err := f.db.ArrInstances().Update(ctx, radarr); err != nil {
				t.Fatal(err)
			}
			sum := stageArchive(t, f, cfg, db)
			c := changeOf(sum, "arrInstances")
			if got := c != nil && strings.Contains(c.Backup, "confirmed)"); got != tc.wantLinks {
				t.Fatalf("confirmed links listed = %v, want %v (change %+v)", got, tc.wantLinks, c)
			}
		})
	}
}
