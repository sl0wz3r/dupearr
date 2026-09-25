package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
)

// TestActiveSessions (S13): the ids of every session, playing or paused: the item a client opened,
// its PrimaryVersionId and the version playing, normalized; a session without anything playing
// adds nothing; an unreadable answer is an error, never "nothing plays".
func TestActiveSessions(t *testing.T) {
	f := newFixture(t)
	f.handle("GET /Sessions", file(t, "sessions.json"))
	ids, err := f.client().ActiveSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"10000000000000000000000000000a01", // paused primary
		"10000000000000000000000000000a02", // the alternate version playing
		"10000000000000000000000000000b03", // part 2 of a stacked alternate (dashed, upper-case)
		"10000000000000000000000000000b01", // its PrimaryVersionId
	} {
		if !ids[want] {
			t.Errorf("%s is not reported playing (got %v)", want, ids)
		}
	}
	if len(ids) != 4 {
		t.Fatalf("ids = %v", ids)
	}
	f.handle("GET /Sessions", body(`[]`))
	if ids, err := f.client().ActiveSessions(context.Background()); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("no sessions: %v %v", ids, err)
	}
	for _, bad := range []string{`null`, `{"Items": []}`, `[{"NowPlayingItem": "x"}]`, ``} {
		f.handle("GET /Sessions", body(bad))
		if _, err := f.client().ActiveSessions(context.Background()); err == nil {
			t.Errorf("answer %q gave a session list", bad)
		}
	}
	f.handle("GET /Sessions", status(http.StatusInternalServerError))
	if _, err := f.client().ActiveSessions(context.Background()); err == nil {
		t.Fatal("HTTP 500 gave a session list")
	}
}

// TestNotify: the notification body holds paths and update types only; invalid paths are refused
// before anything is sent.
func TestNotify(t *testing.T) {
	f := newFixture(t)
	var got []mediaUpdatesDTO
	f.handle("POST /Library/Media/Updated", func(w http.ResponseWriter, r *http.Request) {
		var b mediaUpdatesDTO
		raw := json.NewDecoder(r.Body)
		raw.DisallowUnknownFields()
		if err := raw.Decode(&b); err != nil {
			t.Errorf("body: %v", err)
		}
		got = append(got, b)
		w.WriteHeader(http.StatusNoContent)
	})
	c := f.client()
	ctx := context.Background()
	paths := []string{"/media/movies/A/A - 1080p.mkv", `C:\Media\B.mkv`, `\\nas\share\C.mkv`}
	if err := c.NotifyChanged(ctx, moviesLib, paths); err != nil {
		t.Fatal(err)
	}
	if err := c.NotifyCreated(ctx, moviesLib, paths[:1]); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got[0].Updates) != 3 || got[0].Updates[1].Path != paths[1] || got[0].Updates[0].UpdateType != "Deleted" ||
		got[1].Updates[0].UpdateType != "Created" {
		t.Fatalf("bodies = %+v", got)
	}
	for _, bad := range [][]string{nil, {""}, {"relative/path.mkv"}, {"/a\nb.mkv"}, {"/a\x00b"}, {" /lead.mkv"}, make([]string, maxNotifyPaths+1)} {
		if err := c.NotifyChanged(ctx, moviesLib, bad); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if len(got) != 2 {
		t.Fatalf("an invalid notification was sent")
	}
}

// TestRemovalGate (S12, S14): path substitutions or a credential without the administrator proof
// disable removals; an unreadable configuration is an error, never "no problem".
func TestRemovalGate(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.client()
	ctx := context.Background()
	if p, err := c.RemovalProblem(ctx); err != nil || p != "" {
		t.Fatalf("healthy: %q %v", p, err)
	}
	f.handle("GET /System/Configuration", body(`{"PathSubstitutions": [{"From": "/media", "To": "\\\\nas\\media"}]}`))
	if p, err := c.RemovalProblem(ctx); err != nil || !strings.Contains(p, "path substitutions") {
		t.Fatalf("substitutions: %q %v", p, err)
	}
	f.standard()
	f.handle("GET /Library/VirtualFolders", status(http.StatusForbidden))
	if p, err := c.RemovalProblem(ctx); err != nil || !strings.Contains(p, "not an API key or an administrator") {
		t.Fatalf("not admin: %q %v", p, err)
	}
	f.handle("GET /System/Configuration", status(http.StatusBadGateway))
	if p, err := c.RemovalProblem(ctx); err == nil {
		t.Fatalf("unreadable configuration: %q, want an error", p)
	}
}

// TestNoDeletingCapability: the client is a read-only source (research §5.1 point 3): it does not
// implement VersionDeleter, ItemRefresher or FolderScanner, so the "plex" method is structurally
// unavailable for it.
func TestNoDeletingCapability(t *testing.T) {
	var c any = New("http://jellyfin:8096", testKey, Options{})
	if _, ok := c.(mediaserver.VersionDeleter); ok {
		t.Error("the Jellyfin client can delete versions")
	}
	if _, ok := c.(mediaserver.ItemRefresher); ok {
		t.Error("the Jellyfin client can refresh items")
	}
	if _, ok := c.(mediaserver.FolderScanner); ok {
		t.Error("the Jellyfin client can scan folders")
	}
	if _, ok := c.(mediaserver.ChangeNotifier); !ok {
		t.Error("the Jellyfin client cannot notify changes")
	}
	if _, ok := c.(mediaserver.RemovalGate); !ok {
		t.Error("the Jellyfin client has no removal gate")
	}
}

// TestStatus reads the health state: version, administrator proof, substitutions and the last
// completed library scan.
func TestStatus(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.handle("GET /ScheduledTasks", body(`[{"Name": "Scan Media Library", "Key": "RefreshLibrary", "State": "Idle",
		"LastExecutionResult": {"StartTimeUtc": "2026-09-24T10:00:00.0000000Z", "EndTimeUtc": "2026-09-24T10:05:00.0000000Z", "Status": "Completed"}}]`))
	st, err := f.client().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Administrator || st.PathSubstitutions || st.Untested || st.LastLibraryScan.IsZero() || st.LastLibraryScan.Minute() != 5 {
		t.Fatalf("status = %+v", st)
	}
	f.handle("GET /ScheduledTasks", status(http.StatusInternalServerError))
	f.handle("GET /Library/VirtualFolders", status(http.StatusUnauthorized))
	st, err = f.client().Status(context.Background())
	if err != nil || st.Administrator || st.AdminErr != nil || !st.LastLibraryScan.IsZero() {
		t.Fatalf("status = %+v %v", st, err)
	}
}
