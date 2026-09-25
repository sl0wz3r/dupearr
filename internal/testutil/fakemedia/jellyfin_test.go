package fakemedia

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// jfHeaders authenticate as the API key (or another token).
func jfHeaders(token string) http.Header {
	return http.Header{"Authorization": {`MediaBrowser Token="` + token + `", Client="test", Device="t", DeviceId="t", Version="1"`}}
}

func jfGet(t *testing.T, e *Env, pathQuery string, v any) response {
	t.Helper()
	r := send(t, http.MethodGet, e.Jellyfin.URL+pathQuery, nil, jfHeaders(e.JellyfinAPIKey))
	if r.Status != http.StatusOK {
		t.Fatalf("GET %s: %s", pathQuery, r)
	}
	if v != nil {
		decodeJSON(t, r.Body, v)
	}
	return r
}

type tJFItems struct {
	Items []struct {
		ID               string `json:"Id"`
		Name             string
		Type             string
		Path             string
		IndexNumber      *int
		IndexNumberEnd   *int
		MediaSourceCount *int
		PartCount        *int
		ProviderIds      map[string]string
		MediaSources     []struct {
			ID        string `json:"Id"`
			Type      string
			Path      string
			Size      *int64
			Container string
			Protocol  string
		}
	}
	TotalRecordCount int
}

func (it tJFItems) byPath(t *testing.T, e *Env, rel string) int {
	t.Helper()
	for i, row := range it.Items {
		if row.Path == e.JellyfinMediaPath(rel) {
			return i
		}
	}
	t.Fatalf("no row with path %s", rel)
	return -1
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// TestJellyfinAppendixAGrouping: the fake groups the sample tree like Jellyfin 12.1 did live.
func TestJellyfinAppendixAGrouping(t *testing.T) {
	e := Start(t, JellyfinAppendixA())
	var movies tJFItems
	jfGet(t, e, "/Items?ParentId="+e.JellyfinLibraryID("Movies")+"&Recursive=true&IncludeItemTypes=Movie", &movies)
	// Alpha: one row, two sources, the 2160p primary (resolution names first, descending).
	a := movies.Items[movies.byPath(t, e, Alpha2160)]
	if len(a.MediaSources) != 2 || a.MediaSourceCount == nil || *a.MediaSourceCount != 2 || a.ProviderIds["Tmdb"] != "603" {
		t.Fatalf("alpha = %+v", a)
	}
	if a.ID != md5Hex(jfTypeMovie+RemoteMediaRoot+"/"+Alpha2160) {
		t.Fatalf("ids are MD5(type + path): %s", a.ID)
	}
	// Beta: the file named like the folder is the primary.
	if b := movies.Items[movies.byPath(t, e, BetaMain)]; len(b.MediaSources) != 2 {
		t.Fatalf("beta = %+v", b)
	}
	// Gamma: a stack in the root, PartCount 2, one source with cd1 only.
	if g := movies.Items[movies.byPath(t, e, GammaCD1)]; g.PartCount == nil || *g.PartCount != 2 || len(g.MediaSources) != 1 {
		t.Fatalf("gamma = %+v", g)
	}
	// Eta: the stray is a movie of its own; Marvel: Iron Man and Thor are separate.
	movies.byPath(t, e, EtaMain)
	movies.byPath(t, e, EtaStray)
	movies.byPath(t, e, IronMan)
	movies.byPath(t, e, Thor)
	// Kappa: no PartCount on the row; the alternate is a stack whose cd2 only AdditionalParts shows.
	k := movies.Items[movies.byPath(t, e, KappaMain)]
	if k.PartCount != nil || len(k.MediaSources) != 2 {
		t.Fatalf("kappa = %+v", k)
	}
	var parts tJFItems
	jfGet(t, e, "/Videos/"+k.MediaSources[1].ID+"/AdditionalParts", &parts)
	if len(parts.Items) != 1 || parts.Items[0].Path != e.JellyfinMediaPath(KappaCD2) || parts.Items[0].ID != e.JellyfinPartID(KappaCD2) {
		t.Fatalf("kappa alternate parts = %+v", parts)
	}
	jfGet(t, e, "/Videos/"+k.ID+"/AdditionalParts", &parts)
	if len(parts.Items) != 0 {
		t.Fatalf("kappa primary parts = %+v", parts)
	}
	// Lambda: the .strm is the primary (2160p), a File source of its own bytes.
	l := movies.Items[movies.byPath(t, e, LambdaStrm)]
	if len(l.MediaSources) != 2 || l.MediaSources[0].Container != "strm" || l.MediaSources[0].Protocol != "File" || *l.MediaSources[0].Size > 100 {
		t.Fatalf("lambda = %+v", l)
	}
	// Zeta: merged across the libraries; the Movies row lists the 4K copy as a Grouping source.
	z := movies.Items[movies.byPath(t, e, ZetaMovies)]
	if len(z.MediaSources) != 2 || z.MediaSources[1].Type != "Grouping" || z.MediaSources[1].Path != e.JellyfinMediaPath(Zeta4K) {
		t.Fatalf("zeta = %+v", z)
	}
	// Hidden alternates: ?Ids= of an alternate returns no row.
	var none tJFItems
	jfGet(t, e, "/Items?Ids="+a.MediaSources[1].ID, &none)
	if len(none.Items) != 0 {
		t.Fatalf("an alternate's id returned %+v", none)
	}
	// Episodes: S01E03 with the multi-episode file as a hidden alternate (no IndexNumberEnd); the
	// lone S02E03-E04 with IndexNumberEnd 4; S02E01 with the 720p primary.
	var eps tJFItems
	jfGet(t, e, "/Items?ParentId="+e.JellyfinLibraryID("Shows")+"&Recursive=true&IncludeItemTypes=Episode", &eps)
	e3 := eps.Items[eps.byPath(t, e, ShowS01E03)]
	if e3.IndexNumberEnd != nil || len(e3.MediaSources) != 2 || e3.MediaSources[1].Path != e.JellyfinMediaPath(ShowS01E0304) {
		t.Fatalf("S01E03 = %+v", e3)
	}
	if e34 := eps.Items[eps.byPath(t, e, ShowS02E0304)]; e34.IndexNumberEnd == nil || *e34.IndexNumberEnd != 4 {
		t.Fatalf("S02E03-E04 = %+v", e34)
	}
	eps.byPath(t, e, ShowS02E0172)
	requireNoViolations(t, e)
}

// TestJellyfinMergedTitles: a title merged from two copies of one library and a primary in
// another library is listed there as two rows, each with the other two copies as Grouping sources
// (live on 12.1); the primary's library lists the primary with both.
func TestJellyfinMergedTitles(t *testing.T) {
	remux := "movies/Zeta Remux (2017)/Zeta Remux (2017).mkv"
	sc := JellyfinAppendixA()
	sc.ExtraFiles = append(sc.ExtraFiles, ExtraFile{File: remux, Size: GiB(20)})
	sc.Jellyfin.Merges = [][]string{{Zeta4K, ZetaMovies, remux}}
	e := Start(t, sc)
	var movies, movies4k tJFItems
	jfGet(t, e, "/Items?ParentId="+e.JellyfinLibraryID("Movies")+"&Recursive=true&IncludeItemTypes=Movie", &movies)
	for _, rel := range []string{ZetaMovies, remux} {
		row := movies.Items[movies.byPath(t, e, rel)]
		if len(row.MediaSources) != 3 || row.MediaSources[1].Type != "Grouping" || row.MediaSources[2].Type != "Grouping" {
			t.Fatalf("%s = %+v", rel, row)
		}
	}
	jfGet(t, e, "/Items?ParentId="+e.JellyfinLibraryID("Movies 4K")+"&Recursive=true&IncludeItemTypes=Movie", &movies4k)
	if row := movies4k.Items[movies4k.byPath(t, e, Zeta4K)]; len(row.MediaSources) != 3 {
		t.Fatalf("the primary = %+v", row)
	}
	requireNoViolations(t, e)
}

// TestJellyfinPathSubstitutionsRewritePaths: path substitutions rewrite every path the API reports
// but not the library folders, and not the ids (live on 12.1).
func TestJellyfinPathSubstitutionsRewritePaths(t *testing.T) {
	e := Start(t, JellyfinAppendixA())
	e.SetJellyfinPathSubstitutions(JellyfinPathSubstitution{From: RemoteMediaRoot, To: "//nas/media"})
	var movies tJFItems
	jfGet(t, e, "/Items?ParentId="+e.JellyfinLibraryID("Movies")+"&Recursive=true&IncludeItemTypes=Movie", &movies)
	found := false
	for _, row := range movies.Items {
		if !strings.HasPrefix(row.Path, "//nas/media/movies/") || !strings.HasPrefix(row.MediaSources[0].Path, "//nas/media/movies/") {
			t.Fatalf("a path was not rewritten: %+v", row)
		}
		found = found || row.ID == md5Hex(jfTypeMovie+RemoteMediaRoot+"/"+Alpha2160)
	}
	if !found {
		t.Fatal("the ids changed with the substitution")
	}
	var parts tJFItems
	jfGet(t, e, "/Videos/"+e.JellyfinSourceID(KappaCD1)+"/AdditionalParts", &parts)
	if len(parts.Items) != 1 || !strings.HasPrefix(parts.Items[0].Path, "//nas/media/") {
		t.Fatalf("parts = %+v", parts)
	}
	var folders []struct{ Locations []string }
	jfGet(t, e, "/Library/VirtualFolders", &folders)
	for _, f := range folders {
		for _, l := range f.Locations {
			if !strings.HasPrefix(l, RemoteMediaRoot+"/") {
				t.Fatalf("a library folder was rewritten: %s", l)
			}
		}
	}
}

// TestJellyfinMultiEpisodeForms: the fake parses the multi-episode forms of Jellyfin's naming
// (E03x04 and E03-x04 as well as E03-E04, E03E04 and E03-04).
func TestJellyfinMultiEpisodeForms(t *testing.T) {
	for name, want := range map[string]int{
		"Show S01E03x04": 4, "Show S01E03-x04": 4, "Show S01E03-X04": 4, "Show S01E03xE04": 4, "Show S01E03-E04": 4,
		"Show S01E03E04": 4, "Show S01E03-04": 4, "Show S01E03 - 1080p": 0, "Show S01E03-1080p": 0, "Show S01E03": 0,
	} {
		_, ep, end, ok := jfEpisodeOf(name, "Season 01")
		if !ok || ep != 3 || end != want {
			t.Errorf("%s: episode %d ending %d (%v), want 3 ending %d", name, ep, end, ok, want)
		}
	}
}

// TestJellyfinAuth: the MediaBrowser header works; the ApiKey query parameter works but is a
// violation; legacy forms are refused (401) and recorded; a user token cannot list the libraries
// and sees only its own sessions.
func TestJellyfinAuth(t *testing.T) {
	e := Start(t, JellyfinAppendixA())
	if r := send(t, http.MethodGet, e.Jellyfin.URL+"/System/Info", nil, nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("no credential: %s", r)
	}
	if r := send(t, http.MethodGet, e.Jellyfin.URL+"/System/Info/Public", nil, nil); r.Status != http.StatusOK || !strings.Contains(string(r.Body), "Jellyfin Server") {
		t.Fatalf("public info: %s", r)
	}
	requireNoViolations(t, e)
	for _, h := range []http.Header{{"X-Emby-Token": {e.JellyfinAPIKey}}, {"X-MediaBrowser-Token": {e.JellyfinAPIKey}},
		{"Authorization": {`Emby Token="` + e.JellyfinAPIKey + `"`}}} {
		if r := send(t, http.MethodGet, e.Jellyfin.URL+"/System/Info", nil, h); r.Status != http.StatusUnauthorized {
			t.Fatalf("legacy %v: %s", h, r)
		}
	}
	requireViolation(t, e, RuleJellyfinLegacyAuth)
	if r := send(t, http.MethodGet, e.Jellyfin.URL+"/System/Info?ApiKey="+e.JellyfinAPIKey, nil, nil); r.Status != http.StatusOK {
		t.Fatalf("ApiKey query: %s", r)
	}
	requireViolation(t, e, RuleJellyfinTokenInURL)
	user := jfHeaders(e.JellyfinUserToken)
	if r := send(t, http.MethodGet, e.Jellyfin.URL+"/Library/VirtualFolders", nil, user); r.Status != http.StatusForbidden {
		t.Fatalf("user virtual folders: %s", r)
	}
	e.SetJellyfinSessions(JellyfinSession{ItemID: "a"}, JellyfinSession{ItemID: "b", User: true})
	var sess []map[string]any
	r := send(t, http.MethodGet, e.Jellyfin.URL+"/Sessions", nil, user)
	decodeJSON(t, r.Body, &sess)
	if len(sess) != 1 {
		t.Fatalf("a user saw %d sessions, want only its own", len(sess))
	}
	jfGet(t, e, "/Sessions", &sess)
	if len(sess) != 2 {
		t.Fatalf("the API key saw %d sessions", len(sess))
	}
}

// TestJellyfinDestructiveEndpointsAreFaithful: the deletes do what Jellyfin does (research §3.3)
// and record violations.
func TestJellyfinDestructiveEndpointsAreFaithful(t *testing.T) {
	e := Start(t, JellyfinAppendixA())
	del := func(id string) {
		t.Helper()
		if r := send(t, http.MethodDelete, e.Jellyfin.URL+"/Items/"+id, nil, jfHeaders(e.JellyfinAPIKey)); r.Status != http.StatusNoContent {
			t.Fatalf("DELETE %s: %s", id, r)
		}
	}
	exists := func(rel string) bool { _, err := os.Stat(e.w.local(rel)); return err == nil }
	// Alpha's 1080p alternate: the whole folder goes, marker included.
	del(e.JellyfinSourceID(Alpha1080))
	if exists(Alpha2160) || exists(AlphaMarker) || exists(AlphaDir) {
		t.Fatal("deleting an alternate did not remove the whole movie folder")
	}
	// Iron Man next to Thor's folder: Marvel/ goes, with Thor.
	del(e.JellyfinSourceID(IronMan))
	if exists(Thor) {
		t.Fatal("deleting Iron Man left Thor (Jellyfin removes the parent folder)")
	}
	// Epsilon in the root: its file and every sidecar starting with its name, Epsilon 2's included.
	del(e.JellyfinSourceID(Epsilon))
	if exists(Epsilon) || exists("movies/Epsilon 2.en.srt") || exists("movies/Epsilon 2.nfo") || !exists(Epsilon2) {
		t.Fatal("the root delete did not remove the prefix sidecars")
	}
	// Gamma: part 1 only.
	del(e.JellyfinSourceID(GammaCD1))
	if exists(GammaCD1) || !exists(GammaCD2) {
		t.Fatal("a stack delete must take part 1 only")
	}
	// Bulk: the first id is deleted, then 404.
	r := send(t, http.MethodDelete, e.Jellyfin.URL+"/Items?ids="+e.JellyfinSourceID(Beta720)+",ffffffffffffffffffffffffffffffff", nil, jfHeaders(e.JellyfinAPIKey))
	if r.Status != http.StatusNotFound || exists(BetaMain) {
		t.Fatalf("bulk delete: %s", r)
	}
	for _, rule := range []string{RuleJellyfinItemDelete, RuleJellyfinBulkDelete, RuleJellyfinUnexpectedRequest} {
		requireViolation(t, e, rule)
	}
	// Merge and unlink change links only.
	r = send(t, http.MethodPost, e.Jellyfin.URL+"/Videos/MergeVersions?ids="+e.JellyfinSourceID(EtaMain)+","+e.JellyfinSourceID(EtaStray), nil, jfHeaders(e.JellyfinAPIKey))
	if r.Status != http.StatusNoContent {
		t.Fatalf("merge: %s", r)
	}
	requireViolation(t, e, RuleJellyfinMergeVersions)
	r = send(t, http.MethodDelete, e.Jellyfin.URL+"/Videos/"+e.JellyfinSourceID(ZetaMovies)+"/AlternateSources", nil, jfHeaders(e.JellyfinAPIKey))
	if r.Status != http.StatusNoContent || !exists(ZetaMovies) || !exists(Zeta4K) {
		t.Fatalf("unlink: %s", r)
	}
	requireViolation(t, e, RuleJellyfinUnlinkVersions)
}

// TestJellyfinGhostsAndNotifications: a file removed on disk stays listed until a notification's
// delay has passed on the Env clock (or a library scan); the notification is recorded.
func TestJellyfinGhostsAndNotifications(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	e := StartWithOptions(t, Options{Scenario: JellyfinAppendixA(), Now: clock})
	if err := os.Remove(e.w.local(Alpha1080)); err != nil {
		t.Fatal(err)
	}
	if !e.JellyfinListed(Alpha1080) {
		t.Fatal("the removed file vanished without a scan or notification")
	}
	r := send(t, http.MethodPost, e.Jellyfin.URL+"/Library/Media/Updated",
		`{"Updates":[{"Path":"`+e.JellyfinMediaPath(Alpha1080)+`","UpdateType":"Deleted"}]}`, jfHeaders(e.JellyfinAPIKey))
	if r.Status != http.StatusNoContent {
		t.Fatalf("notify: %s", r)
	}
	jfGet(t, e, "/System/Info", nil)
	if !e.JellyfinListed(Alpha1080) {
		t.Fatal("the ghost left before the monitor delay")
	}
	advance(61 * time.Second)
	jfGet(t, e, "/System/Info", nil)
	if e.JellyfinListed(Alpha1080) || !e.JellyfinListed(Alpha2160) {
		t.Fatal("the notified folder was not re-read after the delay")
	}
	if n := e.JellyfinNotifications(); len(n) != 1 || n[0].Paths[0] != e.JellyfinMediaPath(Alpha1080) || n[0].UpdateType[0] != "Deleted" {
		t.Fatalf("notifications = %+v", n)
	}
	// A restored file comes back under its old id.
	old := e.JellyfinSourceID(Beta720)
	if err := os.Remove(e.w.local(Beta720)); err != nil {
		t.Fatal(err)
	}
	e.JellyfinScan()
	if e.JellyfinListed(Beta720) {
		t.Fatal("a library scan kept a ghost")
	}
	if err := e.CreateFile(Beta720, GiB(4)); err != nil {
		t.Fatal(err)
	}
	e.JellyfinScan()
	if e.JellyfinSourceID(Beta720) != old {
		t.Fatal("a restored file got a new id")
	}
	requireNoViolations(t, e)
}

// TestJellyfinIgnoreCache: an empty .ignore hides its folder, but one written after the folder was
// looked up is only honoured after a library scan (Jellyfin caches the lookup, research S25).
func TestJellyfinIgnoreCache(t *testing.T) {
	e := Start(t, JellyfinAppendixA())
	if err := e.WriteFile(BetaDir+"/.ignore", ""); err != nil {
		t.Fatal(err)
	}
	// A notification re-reads the folder, but the cached "no .ignore" answer still applies.
	e.w.mu.Lock()
	e.w.jfRescan([]string{BetaDir})
	e.w.mu.Unlock()
	if !e.JellyfinListed(BetaMain) {
		t.Fatal("the .ignore was honoured before a library scan")
	}
	e.JellyfinScan()
	if e.JellyfinListed(BetaMain) {
		t.Fatal("the .ignore was not honoured after a library scan")
	}
}

// TestJellyfinConfigurationAndTasks: path substitutions, version and the last library scan.
func TestJellyfinConfigurationAndTasks(t *testing.T) {
	e := Start(t, JellyfinAppendixA())
	e.SetJellyfinPathSubstitutions(JellyfinPathSubstitution{From: "/data/media", To: `\\nas\media`})
	var cfg map[string]any
	jfGet(t, e, "/System/Configuration", &cfg)
	if subst, _ := cfg["PathSubstitutions"].([]any); len(subst) != 1 {
		t.Fatalf("configuration = %v", cfg)
	}
	e.SetJellyfinVersion("12.2.0")
	var info map[string]any
	jfGet(t, e, "/System/Info", &info)
	if info["Version"] != "12.2.0" || info["Id"] != e.JellyfinServerID {
		t.Fatalf("info = %v", info)
	}
	var tasks []map[string]any
	jfGet(t, e, "/ScheduledTasks", &tasks)
	if len(tasks) == 0 || tasks[0]["Key"] != "RefreshLibrary" || tasks[0]["LastExecutionResult"] == nil {
		t.Fatalf("tasks = %v", tasks)
	}
	requireNoViolations(t, e)
}
