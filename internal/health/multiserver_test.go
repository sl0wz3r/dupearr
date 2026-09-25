package health

import (
	"context"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Several Plex servers (docs/DECISIONS.md D11): each multi-server check fires under its condition
// with two enabled servers, and none fires with one.

var multiServerSources = []string{
	SourceMultiServerFolders, SourceArrServerLinks, SourceMultiServerMapping, SourceSeparateServer, SourceMediaServerIdentity,
}

func (e *env) setStorage(s models.MediaServer, storage string) {
	e.t.Helper()
	s.Storage = storage
	if err := e.db.MediaServers().Update(context.Background(), &s); err != nil {
		e.t.Fatal(err)
	}
}

// multiSetup: A and B (both enabled) index /data/movies; A is mapped, B is not; Radarr's links are
// not confirmed; B is stored without its identity; a disabled C indexes the same folder.
func multiSetup(t *testing.T, e *env, secondEnabled bool) (a, b models.MediaServer) {
	t.Helper()
	a = e.addServer("Plex A", true, healthyServer())
	b = e.addServer("Plex B", secondEnabled, healthyServer())
	b.MachineIdentifier = ""
	if err := e.db.MediaServers().Update(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	c := e.addServer("Plex C", false, nil)
	e.addLibrary(a.ID, "1", "Movies", true, "/data/movies")
	e.addLibrary(b.ID, "1", "Films", true, "/data/movies")
	e.addLibrary(c.ID, "1", "Old", true, "/data/movies")
	e.addMapping(a.ID, "/data/movies", e.dir)
	e.addArr("Radarr", true, nil)
	return a, b
}

func TestMultiServerChecks(t *testing.T) {
	e := newEnv(t)
	_, b := multiSetup(t, e, true)
	got := e.checker().Run(context.Background())

	folders := bySource(got, SourceMultiServerFolders)
	if len(folders) != 2 || folders[0].Type != models.HealthNotice ||
		!strings.Contains(folders[0].Message+folders[1].Message, "Plex A and Plex B") ||
		!strings.Contains(folders[0].Message+folders[1].Message, "Plex C (disabled)") {
		t.Fatalf("folders %+v", folders)
	}
	if links := bySource(got, SourceArrServerLinks); len(links) != 1 || links[0].Type != models.HealthWarning || !strings.Contains(links[0].Message, `"Radarr"`) {
		t.Fatalf("links %+v", links)
	}
	if mapping := bySource(got, SourceMultiServerMapping); len(mapping) != 1 || !strings.Contains(mapping[0].Message, `"Plex B"`) {
		t.Fatalf("mapping %+v", mapping)
	}
	if id := bySource(got, SourceMediaServerIdentity); len(id) != 1 || !strings.Contains(id[0].Message, "Plex B") {
		t.Fatalf("identity %+v", id)
	}
	if sep := bySource(got, SourceSeparateServer); len(sep) != 0 {
		t.Fatalf("separate %+v", sep)
	}

	// B declared separate: not a mapping issue any more; the last full scan's name matches warn.
	e.setStorage(b, models.StorageSeparate)
	run := &models.ScanRun{Status: "completed", Stats: models.ScanStats{SeparateNameMatches: map[int64]int{b.ID: 3}}}
	if err := e.db.ScanRuns().Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := e.db.ScanRuns().Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	got = e.checker().Run(context.Background())
	if mapping := bySource(got, SourceMultiServerMapping); len(mapping) != 0 {
		t.Fatalf("mapping after separate %+v", mapping)
	}
	if sep := bySource(got, SourceSeparateServer); len(sep) != 1 || sep[0].Type != models.HealthWarning || !strings.Contains(sep[0].Message, "found 3 of its files") {
		t.Fatalf("separate %+v", sep)
	}
	// A targeted scan afterwards does not hide the last full scan's count.
	if err := e.db.ScanRuns().Create(context.Background(), &models.ScanRun{Status: "completed", Targeted: true}); err != nil {
		t.Fatal(err)
	}
	if sep := bySource(e.checker().Run(context.Background()), SourceSeparateServer); len(sep) != 1 {
		t.Fatalf("separate after a targeted scan %+v", sep)
	}
}

func TestMultiServerChecksSilentWithOneServer(t *testing.T) {
	e := newEnv(t)
	multiSetup(t, e, false)
	got := e.checker().Run(context.Background())
	for _, src := range multiServerSources {
		if r := bySource(got, src); len(r) != 0 {
			t.Fatalf("%s fired with one enabled server: %+v", src, r)
		}
	}
}

// Links confirmed without any enabled server are not a confirmation (the scanner's rule): warned
// like unconfirmed ones. Confirmed links to an enabled server are not.
func TestArrServerLinksConfirmedWithoutServer(t *testing.T) {
	e := newEnv(t)
	a, _ := multiSetup(t, e, true)
	ctx := context.Background()
	arrs, err := e.db.ArrInstances().List(ctx)
	if err != nil || len(arrs) != 1 {
		t.Fatalf("arrs %+v %v", arrs, err)
	}
	r := arrs[0]
	r.ServerIDs, r.LinksConfirmed = []int64{}, true
	if err := e.db.ArrInstances().Update(ctx, &r); err != nil {
		t.Fatal(err)
	}
	if links := bySource(e.checker().Run(ctx), SourceArrServerLinks); len(links) != 1 || !strings.Contains(links[0].Message, `"Radarr"`) {
		t.Fatalf("confirmed without a server: %+v", links)
	}
	r.ServerIDs = []int64{a.ID}
	if err := e.db.ArrInstances().Update(ctx, &r); err != nil {
		t.Fatal(err)
	}
	if links := bySource(e.checker().Run(ctx), SourceArrServerLinks); len(links) != 0 {
		t.Fatalf("confirmed links warned: %+v", links)
	}
}
