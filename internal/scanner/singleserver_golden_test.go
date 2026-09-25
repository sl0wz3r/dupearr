package scanner

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/integrations/tautulli"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// updateGoldens rewrites testdata/singleserver/*.golden.json. The goldens were generated once on
// the tree before multi-server support (issue #8) and are never regenerated: they prove that an
// installation with one Plex server decides, stores and requests exactly what it did before.
var updateGoldens = flag.Bool("update", false, "rewrite the single-server golden files (never after they were first generated)")

// goldenClock is the fixed clock of the golden runs. It lies after the moment the fixture files
// are created, so the files' change times never decide a date added (applyFileAges keeps Plex's
// addedAt, which fakemedia derives from the same clock).
var goldenClock = time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC)

// goldenScan is one scan of a golden run.
type goldenScan struct {
	Name string `json:"name"`
	// Plex lists the media server requests in order (the golden runs fetch one item at a time).
	Plex []string `json:"plex"`
	// Others lists the *arr and Tautulli requests sorted: their clients read concurrently.
	Others []string `json:"others"`
}

type goldenFile struct {
	Key               string          `json:"key"`
	Decision          models.Decision `json:"decision"`
	EngineDecision    models.Decision `json:"engineDecision"`
	Protected         bool            `json:"protected"`
	ProtectedReason   string          `json:"protectedReason,omitempty"`
	Rank              int             `json:"rank"`
	Reasons           []string        `json:"reasons"`
	DecidingCriterion string          `json:"decidingCriterion"`
	Version           json.RawMessage `json:"version"`
}

type goldenGroup struct {
	Key              string             `json:"key"`
	Status           models.GroupStatus `json:"status"`
	StatusReason     string             `json:"statusReason,omitempty"`
	Flags            []string           `json:"flags"`
	Signature        string             `json:"signature"`
	StableCount      int                `json:"stableCount"`
	ReclaimableBytes int64              `json:"reclaimableBytes"`
	ProfileID        int64              `json:"profileId"`
	// CrossServer must stay absent with one server (it is compared as raw JSON).
	CrossServer json.RawMessage `json:"crossServer,omitempty"`
	Files       []goldenFile    `json:"files"`
}

type goldenState struct {
	After  string        `json:"after"`
	Groups []goldenGroup `json:"groups"`
}

type goldenRun struct {
	Scenario string        `json:"scenario"`
	Scans    []goldenScan  `json:"scans"`
	States   []goldenState `json:"states"`
}

// goldenCase is one scenario of the single-server goldens.
type goldenCase struct {
	name string
	opts fakemedia.Options
}

func singleServerGoldenCases() []goldenCase {
	return []goldenCase{
		{name: "default", opts: fakemedia.Options{Scenario: fakemedia.Default()}},
		{name: "watch", opts: fakemedia.Options{Scenario: fakemedia.Watch()}},
		{name: "discs", opts: fakemedia.Options{Scenario: fakemedia.Discs()}},
		{name: "discs-imagescanner", opts: fakemedia.Options{Scenario: fakemedia.Discs(), DiscImageScanner: true}},
		{name: "looseclips", opts: fakemedia.Options{Scenario: fakemedia.LooseClips()}},
	}
}

// TestSingleServerGolden runs full scans, a targeted scan and a re-evaluation with one Plex
// server (real clients against fakemedia) and compares every stored decision, reason, signature,
// version and request with the goldens generated before multi-server support.
func TestSingleServerGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	for _, c := range singleServerGoldenCases() {
		t.Run(c.name, func(t *testing.T) {
			got := runSingleServerGolden(t, c)
			path := filepath.Join("testdata", "singleserver", c.name+".golden.json")
			if *updateGoldens {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if !bytes.Equal(got, want) {
				diffPath := filepath.Join(t.TempDir(), c.name+".got.json")
				_ = os.WriteFile(diffPath, got, 0o644)
				t.Fatalf("single-server results differ from %s (got written to %s): %s", path, diffPath, firstDiff(want, got))
			}
		})
	}
}

// firstDiff describes the first differing line of two texts.
func firstDiff(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < max(len(wl), len(gl)); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n want %s\n  got %s", i+1, w, g)
		}
	}
	return "equal lines, different bytes"
}

func runSingleServerGolden(t *testing.T, c goldenCase) []byte {
	t.Helper()
	opts := c.opts
	opts.Now = func() time.Time { return goldenClock }
	env := fakemedia.StartWithOptions(t, opts)
	h := newHarness(t)
	deps := h.deps
	deps.Now = func() time.Time { return goldenClock }
	deps.Concurrency = 1 // one item at a time: the Plex request order is deterministic
	deps.AutoApprove = nil
	deps.PlexFactory = func(s models.MediaServer) PlexClient {
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-golden", Product: "Dupearr", Version: "test"})
	}
	deps.ArrFactory = func(a models.ArrInstance) ArrClient { return arr.New(a, arr.Options{}) }
	deps.TautulliFactory = func(ti models.TautulliInstance) WatchClient { return tautulli.New(ti, tautulli.Options{}) }
	svc := New(deps)
	svc.arrRetryDelay = time.Millisecond
	ctx := h.ctx

	srv := models.MediaServer{Name: "Fake Plex", Kind: models.MediaServerPlex, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
	if err := h.db.MediaServers().Create(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncLibraries(ctx, srv.ID); err != nil {
		t.Fatalf("sync libraries: %v", err)
	}
	libs, err := h.db.Libraries().ListByServer(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range libs {
		if l.Type == "movie" {
			l.ScopeGroup = "movies"
			if err := h.db.Libraries().Update(ctx, &l); err != nil {
				t.Fatal(err)
			}
		}
	}
	names := make([]string, 0, len(env.Instances))
	for name := range env.Instances {
		names = append(names, name)
	}
	sort.Strings(names)
	insts := map[string]models.ArrInstance{}
	for _, name := range names {
		s := env.Instances[name]
		kind := models.ArrRadarr
		if s.Kind == fakemedia.KindSonarr {
			kind = models.ArrSonarr
		}
		a := models.ArrInstance{Name: name, Kind: kind, URL: s.URL, APIKey: s.APIKey, Enabled: true}
		if err := h.db.ArrInstances().Create(ctx, &a); err != nil {
			t.Fatal(err)
		}
		insts[name] = a
	}
	for _, m := range env.PathMappings() {
		pm := models.PathMapping{SourceType: models.PathSourceServer, SourceID: srv.ID, RemotePath: m.Remote, LocalPath: m.Local}
		if m.Server != fakemedia.ServerPlex {
			pm.SourceType, pm.SourceID = models.PathSourceArr, insts[m.Server].ID
		}
		if err := h.db.PathMappings().Create(ctx, &pm); err != nil {
			t.Fatal(err)
		}
	}
	if c.name == "watch" {
		ti := models.TautulliInstance{Name: "Tautulli", ServerID: srv.ID, URL: env.Tautulli.URL, APIKey: env.TautulliAPIKey, Enabled: true}
		if err := h.db.Tautullis().Create(ctx, &ti); err != nil {
			t.Fatal(err)
		}
	}

	norm := newGoldenNormalizer(env)
	out := goldenRun{Scenario: c.name}
	scan := func(name string, do func() error) {
		t.Helper()
		env.ResetRequests()
		if err := do(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out.Scans = append(out.Scans, norm.requests(name, env.Requests()))
	}
	state := func(after string) {
		t.Helper()
		out.States = append(out.States, goldenState{After: after, Groups: norm.groups(t, h)})
	}

	full := func() error {
		_, err := svc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
		return err
	}
	scan("full scan 1", full)
	state("full scan 1")
	scan("full scan 2", full)
	state("full scan 2")
	if tmdb := firstMovieTmdb(t, h); tmdb > 0 {
		scan(fmt.Sprintf("targeted scan tmdb %d", tmdb), func() error {
			_, err := svc.TargetedScan(ctx, models.TargetedScanBody{TmdbID: tmdb}, models.TriggerWebhook)
			return err
		})
		state("targeted scan")
	}
	if err := svc.ReevaluateAll(ctx); err != nil {
		t.Fatalf("re-evaluate: %v", err)
	}
	state("re-evaluation")
	env.AssertNoViolations(t)

	// One space per level: the loose-clips golden stays below the public export's 1 MiB file
	// limit. Re-indenting is lossless (the data is the same), so the goldens keep proving the
	// behaviour recorded on the unmodified tree.
	data, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

// firstMovieTmdb returns the tmdb id of the first stored movie group (by key), or 0.
func firstMovieTmdb(t *testing.T, h *harness) int {
	t.Helper()
	gs, err := listGroups(h.ctx, h.db, store.GroupFilter{}, append(append([]models.GroupStatus{}, openStatuses...), models.GroupIgnored))
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(gs))
	for _, g := range gs {
		keys = append(keys, g.Key)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if rest, ok := strings.CutPrefix(k, "movie:tmdb:"); ok {
			if i := strings.IndexAny(rest, "@#~"); i >= 0 {
				rest = rest[:i]
			}
			if id, err := strconv.Atoi(rest); err == nil {
				return id
			}
		}
	}
	return 0
}

// goldenNormalizer replaces what differs between runs (the temporary directory, device and inode
// numbers, disc fingerprints and modification times, keys and signatures derived from them) with
// stable placeholders.
type goldenNormalizer struct {
	dir    string
	inodes map[string]string
}

func newGoldenNormalizer(env *fakemedia.Env) *goldenNormalizer {
	return &goldenNormalizer{dir: env.Dir, inodes: map[string]string{}}
}

var discKeyRe = regexp.MustCompile(`disc:(\d+):[0-9a-f]{40}`)

func (n *goldenNormalizer) requests(name string, reqs []fakemedia.Request) goldenScan {
	gs := goldenScan{Name: name, Plex: []string{}, Others: []string{}}
	for _, r := range reqs {
		line := r.Server + " " + r.Method + " " + r.Path
		if r.Server == fakemedia.ServerPlex {
			gs.Plex = append(gs.Plex, line)
		} else {
			gs.Others = append(gs.Others, line)
		}
	}
	sort.Strings(gs.Others)
	return gs
}

func (n *goldenNormalizer) groups(t *testing.T, h *harness) []goldenGroup {
	t.Helper()
	statuses := append(append([]models.GroupStatus{}, openStatuses...), models.GroupIgnored, models.GroupResolved)
	gs, err := listGroups(h.ctx, h.db, store.GroupFilter{}, statuses)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Key < gs[j].Key })
	out := make([]goldenGroup, 0, len(gs))
	for i := range gs {
		g := &gs[i]
		// Disc versions: their keys hash the local root and the signature hashes the disc's
		// fingerprint (absolute paths, modification times), which differ on every run. The
		// decisions and the normalized disc data below are what the signature is computed from.
		sig := g.Signature
		for _, f := range g.Files {
			if f.Version.Disc != nil {
				sig = "(depends on disc paths and times: see the files)"
				break
			}
		}
		gg := goldenGroup{
			Key: g.Key, Status: g.Status, StatusReason: n.text(g.StatusReason), Flags: g.Flags, Signature: sig,
			StableCount: g.StableCount, ReclaimableBytes: g.ReclaimableBytes, ProfileID: g.ProfileID,
		}
		if raw, err := json.Marshal(g); err == nil {
			var m map[string]json.RawMessage
			if json.Unmarshal(raw, &m) == nil {
				gg.CrossServer = m["crossServer"]
			}
		}
		for _, f := range g.Files {
			reasons := make([]string, len(f.Reasons))
			for k, r := range f.Reasons {
				reasons[k] = n.text(r)
			}
			gg.Files = append(gg.Files, goldenFile{
				Key: n.text(f.Version.Key), Decision: f.Decision, EngineDecision: f.EngineDecision, Protected: f.Protected,
				ProtectedReason: n.text(f.ProtectedReason), Rank: f.Rank, Reasons: reasons, DecidingCriterion: f.DecidingCriterion,
				Version: n.version(t, &f.Version),
			})
		}
		out = append(out, gg)
	}
	return out
}

// text normalizes a string: the data directory and disc keys.
func (n *goldenNormalizer) text(s string) string {
	s = strings.ReplaceAll(s, n.dir, "$DIR")
	return discKeyRe.ReplaceAllString(s, "disc:$1:<root hash>")
}

// version renders the stored version JSON with addedAt (dated from file change times), the
// run-specific disc data and inode numbers replaced.
func (n *goldenNormalizer) version(t *testing.T, v *models.MediaVersion) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "addedAt")
	if parts, ok := m["parts"].([]any); ok {
		for _, p := range parts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if ino, ok := pm["inode"].(string); ok && ino != "" {
				pm["inode"] = n.inode(ino)
			}
		}
	}
	if d, ok := m["disc"].(map[string]any); ok {
		for _, k := range []string{"fingerprint", "newestModTime"} {
			if _, ok := d[k]; ok {
				d[k] = "<run-specific>"
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(n.text(string(out)))
}

// inode maps a "<device>:<inode>" to a stable token (in order of appearance; equal inodes keep
// equal tokens, so hard links stay visible).
func (n *goldenNormalizer) inode(s string) string {
	if tok, ok := n.inodes[s]; ok {
		return tok
	}
	tok := fmt.Sprintf("inode-%d", len(n.inodes)+1)
	n.inodes[s] = tok
	return tok
}
