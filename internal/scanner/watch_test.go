package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/tautulli"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Play history (docs/DECISIONS.md D10, issue #5): a missing, failing, partial or unattributable
// history must behave like any other unknown value — never like "never watched".

// fakeWatch is an in-memory WatchClient with Tautulli's history semantics (rating-key lists,
// guid prefix matching, section filter).
type fakeWatch struct {
	mu        sync.Mutex
	pms       string
	info      error // returned by Info
	users     []tautulli.User
	usersErr  error
	libs      map[string]*bool
	libErr    error
	rows      []fakePlay
	histErr   error
	onceErr   error // returned by the next call of any method only
	literal   bool  // treat a rating-key list as one literal key (a Tautulli without list support)
	calls     map[string]int
	lastKeys  []string
	guidReads int
	keyRows   int // rows answered to rating-key reads
	seq       int64
}

type fakePlay struct {
	section string
	row     tautulli.HistoryRow
}

func boolRef(b bool) *bool { return &b }

func newFakeWatch(pms string) *fakeWatch {
	return &fakeWatch{
		pms:   pms,
		users: []tautulli.User{{Active: true, KeepHistory: boolRef(true)}, {Active: true, KeepHistory: boolRef(true)}},
		libs:  map[string]*bool{},
		calls: map[string]int{},
	}
}

// play records a play of rating key rk (guid, section) by user at t.
func (f *fakeWatch) play(section, rk, guid string, user int64, t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.rows = append(f.rows, fakePlay{section: section, row: tautulli.HistoryRow{RowID: f.seq, RatingKey: rk, GUID: guid,
		UserID: user, Started: t, Stopped: t.Add(90 * time.Minute)}})
}

func (f *fakeWatch) enter(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	if err := f.onceErr; err != nil {
		f.onceErr = nil
		return err
	}
	return nil
}

func (f *fakeWatch) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeWatch) Info(ctx context.Context) (*tautulli.Info, error) {
	if err := f.enter("info"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.info != nil {
		return nil, f.info
	}
	return &tautulli.Info{Version: "v2.18.1", PMSIdentifier: f.pms, PMSName: "Plex"}, nil
}

func (f *fakeWatch) Users(ctx context.Context) ([]tautulli.User, error) {
	if err := f.enter("users"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tautulli.User(nil), f.users...), f.usersErr
}

func (f *fakeWatch) Library(ctx context.Context, section string) (*tautulli.Library, error) {
	if err := f.enter("library"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.libErr != nil {
		return nil, f.libErr
	}
	return &tautulli.Library{SectionID: section, KeepHistory: f.libs[section]}, nil
}

func (f *fakeWatch) sorted() []fakePlay {
	out := append([]fakePlay(nil), f.rows...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].row.Started.Before(out[j].row.Started) })
	return out
}

func (f *fakeWatch) FirstPlay(ctx context.Context, section string) (*tautulli.HistoryRow, error) {
	if err := f.enter("first"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.sorted() {
		if p.section == section {
			r := p.row
			return &r, nil
		}
	}
	return nil, nil
}

func (f *fakeWatch) History(ctx context.Context, flt tautulli.HistoryFilter) ([]tautulli.HistoryRow, error) {
	if err := f.enter("history"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.histErr != nil {
		return nil, f.histErr
	}
	if flt.GUID != "" {
		f.guidReads++
	}
	keys := map[string]bool{}
	if f.literal && len(flt.RatingKeys) > 1 {
		keys[strings.Join(flt.RatingKeys, ",")] = true
	} else {
		for _, k := range flt.RatingKeys {
			keys[k] = true
		}
	}
	if len(flt.RatingKeys) > 0 {
		f.lastKeys = append([]string(nil), flt.RatingKeys...)
	}
	var out []tautulli.HistoryRow
	for _, p := range f.sorted() {
		if len(flt.RatingKeys) > 0 && !keys[p.row.RatingKey] {
			continue
		}
		if flt.SectionID != "" && p.section != flt.SectionID {
			continue
		}
		if flt.GUID != "" && !strings.HasPrefix(p.row.GUID, flt.GUID) {
			continue
		}
		out = append(out, p.row)
	}
	if flt.MaxRows > 0 && len(out) > flt.MaxRows {
		return nil, fmt.Errorf("%w: more than %d recorded plays match", tautulli.ErrIncomplete, flt.MaxRows)
	}
	if len(flt.RatingKeys) > 0 {
		f.keyRows += len(out)
	}
	return out, nil
}

// watchSetup is a server with two scope-grouped movie libraries ("1" Movies, "2" Movies 4K) whose
// Tautulli keeps history, and "Arrival": a played 1080p copy (rk 10, section 1) and a 4K copy (rk
// 20, section 2) added after the history of its library starts, without plays.
type watchSetup struct {
	h        *harness
	srv      models.MediaServer
	px       *fakePlex
	tw       *fakeWatch
	inst     models.TautulliInstance
	hd, uhd  models.Library
	coverage time.Time
}

const (
	arrivalGUID   = "plex://movie/5d7768284de0ee001fcc8a2f"
	arrivalGroup  = "movie:tmdb:329865"
	hdMedia       = int64(101)
	uhdMedia      = int64(201)
	playedProfile = "Played first"
)

func newWatchSetup(t *testing.T) *watchSetup {
	t.Helper()
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	s := &watchSetup{h: h, srv: srv, px: px, tw: newFakeWatch(srv.MachineIdentifier)}
	s.hd = h.addLibrary(srv.ID, "1", "Movies", "movie", "movies")
	s.uhd = h.addLibrary(srv.ID, "2", "Movies 4K", "movie", "movies")
	s.coverage = time.Date(2025, 3, 1, 20, 0, 0, 0, time.UTC)
	s.tw.libs["1"], s.tw.libs["2"] = boolRef(true), boolRef(true)

	hd := movie("10", 329865, "Arrival", 2016, ver(hdMedia, "/data/1/Arrival (2016)/Arrival.1080p.mkv", 7*gb, 1920))
	hd.GUID, hd.AddedAt = arrivalGUID, time.Date(2024, 11, 2, 0, 0, 0, 0, time.UTC)
	uhd := movie("20", 329865, "Arrival", 2016, ver(uhdMedia, "/data/2/Arrival (2016)/Arrival.2160p.mkv", 40*gb, 3840))
	uhd.GUID, uhd.AddedAt = arrivalGUID, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	px.put("1", hd)
	px.put("2", uhd)
	// Library coverage: the first recorded plays of each library are of other titles.
	px.put("1", movie("11", 1, "Old Film", 1990, ver(111, "/data/1/Old Film (1990)/Old Film.mkv", 5*gb, 1920)))
	px.put("2", movie("21", 2, "Old 4K Film", 1991, ver(211, "/data/2/Old 4K Film (1991)/Old 4K Film.mkv", 30*gb, 3840)))
	s.tw.play("1", "11", "plex://movie/old", 1, s.coverage)
	s.tw.play("2", "21", "plex://movie/old4k", 1, s.coverage.Add(24*time.Hour))
	// Arrival 1080p was played three times by two users.
	s.tw.play("1", "10", arrivalGUID, 1, time.Date(2026, 2, 3, 20, 0, 0, 0, time.UTC))
	s.tw.play("1", "10", arrivalGUID, 2, time.Date(2026, 6, 9, 20, 0, 0, 0, time.UTC))
	s.tw.play("1", "10", arrivalGUID, 1, time.Date(2026, 8, 20, 20, 0, 0, 0, time.UTC))

	s.inst = models.TautulliInstance{Name: "Tautulli", ServerID: srv.ID, URL: "http://tautulli:8181", APIKey: "0123456789abcdef", Enabled: true}
	if err := h.db.Tautullis().Create(h.ctx, &s.inst); err != nil {
		t.Fatal(err)
	}
	h.deps.TautulliFactory = func(models.TautulliInstance) WatchClient { return s.tw }
	h.svc = New(h.deps)
	h.svc.arrRetryDelay = time.Millisecond
	return s
}

// useProfile makes the default profile rank by the given criteria (health first, then crits, then
// resolution).
func (s *watchSetup) useProfile(crits ...models.Criterion) {
	s.h.t.Helper()
	p, err := s.h.db.Profiles().GetDefault(s.h.ctx)
	if err != nil {
		s.h.t.Fatal(err)
	}
	p.Name = playedProfile
	p.Criteria = append([]models.Criterion{{Type: models.CritHealth, Enabled: true}}, crits...)
	p.Criteria = append(p.Criteria, models.Criterion{Type: models.CritResolution, Enabled: true})
	if err := s.h.db.Profiles().Update(s.h.ctx, p); err != nil {
		s.h.t.Fatal(err)
	}
}

func playedCrit() models.Criterion { return models.Criterion{Type: models.CritPlayed, Enabled: true} }

func (s *watchSetup) arrival() *models.DuplicateGroup { return s.h.group(arrivalGroup) }

func watchOf(t *testing.T, g *models.DuplicateGroup, media int64) *models.WatchInfo {
	t.Helper()
	w := fileByMedia(t, g, media).Version.Watch
	if w == nil {
		t.Fatalf("media %d has no play history", media)
	}
	return w
}

// TestWatchAttribution: plays are attributed by rating key; the copy without plays in a library
// whose history covers it is "no plays recorded", and a Played-first profile keeps the played copy.
func TestWatchAttribution(t *testing.T) {
	s := newWatchSetup(t)
	s.useProfile(playedCrit())
	s.h.fullScan()
	g := s.arrival()
	hd, uhd := watchOf(t, g, hdMedia), watchOf(t, g, uhdMedia)
	if hd.Status != models.WatchKnown || hd.Plays != 3 || hd.Users != 2 || hd.SourceName != "Tautulli" ||
		!hd.LastPlayed.Equal(time.Date(2026, 8, 20, 21, 30, 0, 0, time.UTC)) || hd.ReadAt.IsZero() {
		t.Fatalf("1080p history %+v", hd)
	}
	// No plays since the 4K item was added (after its library's history starts on 2025-03-02).
	if uhd.Status != models.WatchKnown || uhd.Plays != 0 || !uhd.Since.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("4K history %+v", uhd)
	}
	if fileByMedia(t, g, hdMedia).Decision != models.DecisionKeep || fileByMedia(t, g, uhdMedia).Decision != models.DecisionRemove {
		t.Fatalf("decisions: the played 1080p copy must be kept")
	}
	f := fileByMedia(t, g, uhdMedia)
	if f.DecidingCriterion != string(models.CritPlayed) ||
		!strings.Contains(strings.Join(f.Reasons, "\n"), "no plays recorded since 2026-05-01 (vs 3 plays, the last on 2026-08-20)") {
		t.Fatalf("4K: deciding %q, reasons %v", f.DecidingCriterion, f.Reasons)
	}
	if g.Status != models.GroupPending || g.HasFlag(models.FlagWatchUnreadable) {
		t.Fatalf("status %s flags %v", g.Status, g.Flags)
	}

	// The same data with a quality-only profile: nothing changes but the display values.
	s.useProfile()
	s.h.fullScan()
	g = s.arrival()
	if fileByMedia(t, g, uhdMedia).Decision != models.DecisionKeep {
		t.Fatal("without a play-history criterion the 4K copy must be kept")
	}
}

// TestWatchPlaysBeforeTheCopyExisted: the played copy's plays all predate the 4K item (a fresh
// download, or a file split out of the played item into a new one): nothing shows that people
// passed the 4K copy over, so Played ties and quality keeps it.
func TestWatchPlaysBeforeTheCopyExisted(t *testing.T) {
	s := newWatchSetup(t)
	s.useProfile(playedCrit(), models.Criterion{Type: models.CritLastPlayed, Enabled: true, MinDelta: 30})
	var keep []fakePlay
	for _, p := range s.tw.rows {
		if p.row.RatingKey != "10" || p.row.Started.Before(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
			keep = append(keep, p)
		}
	}
	s.tw.rows = keep // Arrival 1080p: one play, on 2026-02-03
	s.h.fullScan()
	g := s.arrival()
	if w := watchOf(t, g, uhdMedia); w.Status != models.WatchKnown || w.Plays != 0 {
		t.Fatalf("4K history %+v", w)
	}
	if fileByMedia(t, g, uhdMedia).Decision != models.DecisionKeep {
		t.Fatalf("the 4K copy must be kept: %v", fileByMedia(t, g, uhdMedia).Reasons)
	}
	if f := fileByMedia(t, g, hdMedia); f.DecidingCriterion != string(models.CritResolution) {
		t.Fatalf("1080p decided by %q (%v), want resolution", f.DecidingCriterion, f.Reasons)
	}
	if g.Status != models.GroupPending {
		t.Fatalf("status %s", g.Status)
	}
}

// TestWatchNoPlaysNeedsEveryCondition: zero rows are "no plays recorded" only when nothing could
// hide a play; otherwise the copy is unknown (with the reason) and quality decides.
func TestWatchNoPlaysNeedsEveryCondition(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(s *watchSetup)
		reason string
	}{
		{"library keeps no history", func(s *watchSetup) { s.tw.libs["2"] = boolRef(false) }, "does not keep history for this library"},
		{"library flag not reported", func(s *watchSetup) { s.tw.libs["2"] = nil }, "does not report whether this library keeps history"},
		{"a user keeps no history", func(s *watchSetup) {
			s.tw.users = append(s.tw.users, tautulli.User{Active: true, KeepHistory: boolRef(false)})
		}, "does not keep history for every user"},
		{"a user's flag not reported", func(s *watchSetup) { s.tw.users = append(s.tw.users, tautulli.User{Active: true}) },
			"whether every user's history is kept"},
		{"no users", func(s *watchSetup) { s.tw.users = nil }, "reports no users"},
		{"no history in the library", func(s *watchSetup) {
			var keep []fakePlay
			for _, p := range s.tw.rows {
				if p.section != "2" {
					keep = append(keep, p)
				}
			}
			s.tw.rows = keep
		}, "no play is recorded in this library yet"},
		{"added before the history starts", func(s *watchSetup) {
			it, _ := s.px.Item(context.Background(), "20")
			it.AddedAt = s.coverage.Add(-24 * time.Hour)
			s.px.put("2", it)
		}, "added before the recorded history starts (2025-03-02)"},
		{"unmatched item", func(s *watchSetup) {
			it, _ := s.px.Item(context.Background(), "20")
			it.GUID = ""
			s.px.put("2", it)
		}, "not matched in Plex"},
		{"local guid", func(s *watchSetup) {
			it, _ := s.px.Item(context.Background(), "20")
			it.GUID = "local://20"
			s.px.put("2", it)
		}, "not matched in Plex"},
		{"plays under an earlier Plex item", func(s *watchSetup) {
			// The 4K copy was removed from Plex and added again: its plays stayed under rk 17.
			s.tw.play("2", "17", arrivalGUID, 3, time.Date(2025, 12, 24, 20, 0, 0, 0, time.UTC))
		}, "recorded under an earlier Plex item"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newWatchSetup(t)
			s.useProfile(playedCrit())
			tt.mutate(s)
			s.h.fullScan()
			g := s.arrival()
			w := watchOf(t, g, uhdMedia)
			if w.Status != models.WatchUnknown || !strings.Contains(w.Reason, tt.reason) {
				t.Fatalf("4K history %+v, want unknown (%s)", w, tt.reason)
			}
			if hd := watchOf(t, g, hdMedia); hd.Status != models.WatchKnown || hd.Plays != 3 {
				t.Fatalf("recorded plays are evidence whatever else holds: %+v", hd)
			}
			// Unknown ties: the 4K copy wins on resolution, never loses on Played.
			if fileByMedia(t, g, uhdMedia).Decision != models.DecisionKeep || fileByMedia(t, g, hdMedia).DecidingCriterion != string(models.CritResolution) {
				t.Fatalf("decisions: 4K %s, 1080p decided by %q", fileByMedia(t, g, uhdMedia).Decision, fileByMedia(t, g, hdMedia).DecidingCriterion)
			}
			if g.Status != models.GroupPending || g.HasFlag(models.FlagWatchUnreadable) {
				t.Fatalf("an unknown history is not an unreadable one: %s %v", g.Status, g.Flags)
			}
		})
	}
}

// TestWatchChurnGuardIgnoresListedItemsAndPrefixes: plays of the same guid under a rating key the
// library still lists (another item, e.g. an edition) and plays of a guid that merely starts like
// this one do not block "no plays recorded".
func TestWatchChurnGuardIgnoresListedItemsAndPrefixes(t *testing.T) {
	s := newWatchSetup(t)
	s.useProfile(playedCrit())
	edition := movie("22", 329865, "Arrival", 2016, ver(221, "/data/2/Arrival (2016) {edition-IMAX}/Arrival.mkv", 20*gb, 3840))
	edition.EditionTitle, edition.GUID = "IMAX", arrivalGUID
	s.px.put("2", edition)
	s.tw.play("2", "22", arrivalGUID, 1, time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC))
	s.tw.play("2", "16", arrivalGUID+"0", 1, time.Date(2026, 1, 2, 20, 0, 0, 0, time.UTC))
	s.h.fullScan()
	if w := watchOf(t, s.arrival(), uhdMedia); w.Status != models.WatchKnown || w.Plays != 0 {
		t.Fatalf("4K history %+v, want no plays recorded", w)
	}
	if s.tw.guidReads == 0 {
		t.Fatal("the churn guard did not run")
	}
}

// TestWatchFailuresMarkEveryCopyFailed: whatever goes wrong, every copy of the server is "failed"
// (never "no plays"), the error is counted, and the Played-ranked group goes to review.
func TestWatchFailuresMarkEveryCopyFailed(t *testing.T) {
	transient := &tautulli.HTTPError{Cmd: "get_history", StatusCode: 503}
	tests := []struct {
		name      string
		mutate    func(s *watchSetup)
		reason    string
		infoCalls int // 0 = not checked
	}{
		{"unauthorized", func(s *watchSetup) { s.tw.info = tautulli.ErrUnauthorized }, "unauthorized", 1},
		{"API disabled", func(s *watchSetup) { s.tw.info = tautulli.ErrNotFound }, "API was not found", 1},
		{"too old", func(s *watchSetup) { s.tw.info = fmt.Errorf("%w (found v2.17.2)", tautulli.ErrTooOld) }, "2.18.0 or later", 1},
		{"5xx after one retry", func(s *watchSetup) { s.tw.info = transient }, "HTTP 503", 2},
		{"timeout", func(s *watchSetup) {
			s.tw.histErr = fmt.Errorf("tautulli: get_history: no response within 30s: %w", context.DeadlineExceeded)
		}, "no response within 30s", 0},
		{"another Plex server", func(s *watchSetup) { s.tw.pms = "someone-else" }, "monitors another Plex server", 0},
		{"no identity reported", func(s *watchSetup) { s.tw.pms = "" }, "does not report which Plex server", 0},
		{"error result", func(s *watchSetup) { s.tw.histErr = fmt.Errorf("%w: get_history", tautulli.ErrCommandFailed) }, "command failed", 0},
		{"short page", func(s *watchSetup) {
			s.tw.histErr = fmt.Errorf("%w: get_history returned 1000 of 2400 plays", tautulli.ErrIncomplete)
		}, "could not be read completely", 0},
		{"unrequested key", func(s *watchSetup) {
			s.tw.histErr = fmt.Errorf("%w: get_history returned a play of rating key \"555\", which was not asked for", tautulli.ErrIncomplete)
		}, "not asked for", 0},
		{"users unreadable", func(s *watchSetup) { s.tw.usersErr = tautulli.ErrWrongApp }, "does not answer like Tautulli", 0},
		{"library error result", func(s *watchSetup) { s.tw.libErr = fmt.Errorf("%w: get_library", tautulli.ErrCommandFailed) }, "command failed", 0},
		{"key header dropped", func(s *watchSetup) { s.tw.info = tautulli.ErrKeyHeaderMissing }, "did not receive the X-Api-Key header", 1},
		{"rating-key list ignored", func(s *watchSetup) { s.tw.literal = true }, "rating-key filter cannot be trusted", 0},
		{"rating-key list ignored, canary is a candidate", func(s *watchSetup) {
			// The earliest play of "Movies" is now one of Arrival's: the canary is a candidate key.
			s.tw.play("1", "10", arrivalGUID, 2, s.coverage.Add(-time.Hour))
			s.tw.literal = true
		}, "rating-key filter cannot be trusted", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newWatchSetup(t)
			s.useProfile(playedCrit())
			tt.mutate(s)
			run := s.h.fullScan()
			if run.Stats.Errors != 1 {
				t.Fatalf("errors = %d, want 1", run.Stats.Errors)
			}
			g := s.arrival()
			for _, f := range g.Files {
				w := f.Version.Watch
				if w == nil || w.Status != models.WatchFailed || !strings.Contains(w.Reason, tt.reason) {
					t.Fatalf("media %d history %+v, want failed (%s)", f.Version.MediaID, w, tt.reason)
				}
				if strings.Contains(w.Reason, "0123456789abcdef") {
					t.Fatalf("the reason carries the API key: %q", w.Reason)
				}
			}
			if g.Status != models.GroupReview || !g.HasFlag(models.FlagWatchUnreadable) || g.StableCount != 0 {
				t.Fatalf("status %s flags %v stable %d", g.Status, g.Flags, g.StableCount)
			}
			if !strings.Contains(g.StatusReason, "play history could not be read") {
				t.Fatalf("status reason %q", g.StatusReason)
			}
			// A failed history is a tie: the ranking is the quality ranking, never "not played".
			if fileByMedia(t, g, uhdMedia).Decision != models.DecisionKeep {
				t.Fatal("the 4K copy must be kept (a failed history ties)")
			}
			if tt.infoCalls > 0 && s.tw.callCount("info") != tt.infoCalls {
				t.Fatalf("Info calls = %d, want %d", s.tw.callCount("info"), tt.infoCalls)
			}
		})
	}
}

// TestWatchRowCap: the rows of one server's reads in a scan — the rating-key chunks and the churn
// guard's guid reads together — are capped; beyond the cap the read fails (review), it is never
// decided on a truncated history.
func TestWatchRowCap(t *testing.T) {
	const prefixRows = 3 // rows the churn guard reads and discards (a guid that only starts alike)
	setup := func(t *testing.T) *watchSetup {
		s := newWatchSetup(t)
		s.useProfile(playedCrit())
		for i := range prefixRows {
			s.tw.play("2", "16", arrivalGUID+"0", 1, time.Date(2026, 1, 2+i, 20, 0, 0, 0, time.UTC))
		}
		return s
	}
	s := setup(t)
	s.h.fullScan()
	keyRows := s.tw.keyRows
	if keyRows == 0 || s.tw.guidReads == 0 || watchOf(t, s.arrival(), uhdMedia).Status != models.WatchKnown {
		t.Fatalf("baseline: key rows %d, guid reads %d, 4K %+v", keyRows, s.tw.guidReads, watchOf(t, s.arrival(), uhdMedia))
	}
	for _, tt := range []struct {
		name string
		cap  int
		ok   bool
	}{
		{"exactly enough", keyRows + prefixRows, true},
		{"the churn guard crosses it", keyRows + prefixRows - 1, false},
		{"a rating-key read crosses it", keyRows - 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := setup(t)
			s.h.svc.watchMaxRows = tt.cap
			run := s.h.fullScan()
			w := watchOf(t, s.arrival(), uhdMedia)
			if tt.ok {
				if run.Stats.Errors != 0 || w.Status != models.WatchKnown {
					t.Fatalf("errors %d, 4K %+v", run.Stats.Errors, w)
				}
				return
			}
			if run.Stats.Errors != 1 || w.Status != models.WatchFailed || !strings.Contains(w.Reason, "more than") {
				t.Fatalf("errors %d, 4K %+v, want failed (more than %d plays)", run.Stats.Errors, w, tt.cap)
			}
			if g := s.arrival(); g.Status != models.GroupReview || !g.HasFlag(models.FlagWatchUnreadable) {
				t.Fatalf("status %s flags %v", g.Status, g.Flags)
			}
		})
	}
}

// TestWatchMissingServerIdentity: a media server stored without a machine identifier cannot be
// matched to a Tautulli, so its history is never used — unless the scan learns the identity while
// listing the server: then the same scan already uses it.
func TestWatchMissingServerIdentity(t *testing.T) {
	for _, readable := range []bool{true, false} {
		t.Run(fmt.Sprintf("identity readable %t", readable), func(t *testing.T) {
			s := newWatchSetup(t)
			s.useProfile(playedCrit())
			srv, err := s.h.db.MediaServers().Get(s.h.ctx, s.srv.ID)
			if err != nil {
				t.Fatal(err)
			}
			srv.MachineIdentifier = ""
			if err := s.h.db.MediaServers().Update(s.h.ctx, srv); err != nil {
				t.Fatal(err)
			}
			if !readable {
				s.px.identErr = errors.New("identity unavailable")
			}
			run := s.h.fullScan()
			w := watchOf(t, s.arrival(), hdMedia)
			if !readable {
				if w.Status != models.WatchFailed || !strings.Contains(w.Reason, "no stored machine identifier") {
					t.Fatalf("history %+v", w)
				}
				return
			}
			if run.Stats.Errors != 0 || w.Status != models.WatchKnown || w.Plays != 3 {
				t.Fatalf("errors %d, history %+v: the identity adopted in this scan must be used", run.Stats.Errors, w)
			}
		})
	}
}

// TestWatchRetriesOnce: one transient failure is retried once and the read succeeds.
func TestWatchRetriesOnce(t *testing.T) {
	s := newWatchSetup(t)
	s.useProfile(playedCrit())
	s.tw.onceErr = &tautulli.HTTPError{Cmd: "get_tautulli_info", StatusCode: 502}
	run := s.h.fullScan()
	if run.Stats.Errors != 0 || watchOf(t, s.arrival(), hdMedia).Status != models.WatchKnown {
		t.Fatalf("errors %d, history %+v", run.Stats.Errors, watchOf(t, s.arrival(), hdMedia))
	}
	if s.tw.callCount("info") != 2 {
		t.Fatalf("Info calls = %d, want 2", s.tw.callCount("info"))
	}
}

// TestWatchUnreadableLifecycle: an outage sends the group to review and resets its stability; a
// queued group's approval is dropped; once Tautulli answers again the group is pending and needs
// stable scans again.
func TestWatchUnreadableLifecycle(t *testing.T) {
	s := newWatchSetup(t)
	s.useProfile(playedCrit())
	s.h.fullScan()
	s.h.fullScan()
	g := s.arrival()
	if g.Status != models.GroupPending || g.StableCount != 2 {
		t.Fatalf("status %s stable %d", g.Status, g.StableCount)
	}
	if err := s.h.db.Groups().UpdateStatus(s.h.ctx, g.ID, models.GroupQueued, "approved"); err != nil {
		t.Fatal(err)
	}
	s.tw.info = errors.New("dial tcp 10.0.0.9:8181: connect: connection refused")
	s.h.fullScan()
	g = s.arrival()
	if g.Status != models.GroupReview || !g.HasFlag(models.FlagWatchUnreadable) || g.StableCount != 0 {
		t.Fatalf("outage: status %s flags %v stable %d", g.Status, g.Flags, g.StableCount)
	}
	s.tw.info = nil
	s.h.fullScan()
	g = s.arrival()
	if g.Status != models.GroupPending || g.HasFlag(models.FlagWatchUnreadable) || g.StableCount != 1 {
		t.Fatalf("recovered: status %s flags %v stable %d", g.Status, g.Flags, g.StableCount)
	}
}

// TestWatchReevaluationUsesStoredSnapshot: a profile change re-evaluates from the stored history;
// adding Played to a group whose last read failed sends it to review.
func TestWatchReevaluationUsesStoredSnapshot(t *testing.T) {
	s := newWatchSetup(t)
	s.useProfile()
	s.tw.info = errors.New("connection refused")
	s.h.fullScan()
	g := s.arrival()
	if g.Status != models.GroupPending || g.HasFlag(models.FlagWatchUnreadable) {
		t.Fatalf("quality profile: status %s flags %v", g.Status, g.Flags)
	}
	reads := s.tw.callCount("info")
	s.useProfile(playedCrit())
	if err := s.h.svc.ReevaluateAll(s.h.ctx); err != nil {
		t.Fatal(err)
	}
	g = s.arrival()
	if g.Status != models.GroupReview || !g.HasFlag(models.FlagWatchUnreadable) || g.StableCount != 0 {
		t.Fatalf("after adding Played: status %s flags %v stable %d", g.Status, g.Flags, g.StableCount)
	}
	if s.tw.callCount("info") != reads {
		t.Fatal("a re-evaluation must not read Tautulli")
	}
}

// TestWatchWithoutTautulli: without a connection the versions carry no history and the decisions
// equal those of a profile without play-history criteria.
func TestWatchWithoutTautulli(t *testing.T) {
	s := newWatchSetup(t)
	if err := s.h.db.Tautullis().Delete(s.h.ctx, s.inst.ID); err != nil {
		t.Fatal(err)
	}
	s.useProfile()
	s.h.fullScan()
	base := s.arrival()
	s.useProfile(playedCrit(), models.Criterion{Type: models.CritLastPlayed, Enabled: true, MinDelta: 30})
	s.h.fullScan()
	g := s.arrival()
	for _, f := range g.Files {
		if f.Version.Watch != nil {
			t.Fatalf("media %d has a play history without Tautulli: %+v", f.Version.MediaID, f.Version.Watch)
		}
		if fileByMedia(t, base, f.Version.MediaID).Decision != f.Decision {
			t.Fatalf("media %d: decision changed", f.Version.MediaID)
		}
	}
	if g.Signature != base.Signature || g.Status != base.Status {
		t.Fatalf("signature/status changed: %s/%s vs %s/%s", g.Signature, g.Status, base.Signature, base.Status)
	}
	if s.tw.callCount("info") != 0 {
		t.Fatal("Tautulli was read without a connection")
	}
	// A disabled connection is not read either.
	s.inst.ID = 0
	s.inst.Enabled = false
	if err := s.h.db.Tautullis().Create(s.h.ctx, &s.inst); err != nil {
		t.Fatal(err)
	}
	s.h.fullScan()
	if s.tw.callCount("info") != 0 || watchOfNil(s.arrival()) == false {
		t.Fatal("a disabled Tautulli connection was used")
	}
}

func watchOfNil(g *models.DuplicateGroup) bool {
	for _, f := range g.Files {
		if f.Version.Watch != nil {
			return false
		}
	}
	return true
}

// TestWatchTargetedScan: a targeted scan reads the play history of its items too.
func TestWatchTargetedScan(t *testing.T) {
	s := newWatchSetup(t)
	s.useProfile(playedCrit())
	run, err := s.h.svc.TargetedScan(s.h.ctx, models.TargetedScanBody{RatingKeys: []string{"20"}}, models.TriggerWebhook)
	if err != nil || run.Status != runCompleted {
		t.Fatalf("targeted scan: %v %+v", err, run)
	}
	g := s.arrival()
	if w := watchOf(t, g, hdMedia); w.Status != models.WatchKnown || w.Plays != 3 {
		t.Fatalf("history %+v", w)
	}
	if fileByMedia(t, g, hdMedia).Decision != models.DecisionKeep {
		t.Fatal("the played copy must be kept")
	}
	for _, k := range s.tw.lastKeys {
		if k == "11" || k == "21" {
			continue // the canary
		}
		if k != "10" && k != "20" {
			t.Fatalf("a targeted scan asked for rating key %s (keys %v)", k, s.tw.lastKeys)
		}
	}
}

// TestWatchVersionsShareAndDiscsAreUnknown: every version of an item gets the item's history; a
// full disc sharing the item's rating key does not.
func TestWatchVersionsShareAndDiscsAreUnknown(t *testing.T) {
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	res := &watchRead{
		rows:     map[string][]tautulli.HistoryRow{"10": {{RowID: 1, RatingKey: "10", UserID: 1, Started: since.Add(time.Hour)}}},
		since:    map[string]time.Time{"1": since},
		libKeep:  map[string]*bool{"1": boolRef(true)},
		churnWhy: map[string]string{},
	}
	it := models.MediaItem{ServerID: 1, RatingKey: "10", SectionKey: "1", GUID: arrivalGUID, AddedAt: since.Add(time.Hour),
		Versions: []models.MediaVersion{
			{Key: "plex:1:1"},
			{Key: "plex:1:2"},
			{Key: "disc:1:abc", Disc: &models.DiscInfo{Type: models.DiscBluray, Origin: models.DiscOriginFilesystem}},
			{Key: "disc:1:def", Disc: &models.DiscInfo{Type: models.DiscBlurayClips, Origin: models.DiscOriginPlex}},
		}}
	items := []models.MediaItem{it}
	p := &pipeline{log: slog.New(slog.DiscardHandler)}
	p.applyWatch(models.TautulliInstance{Name: "T"}, items, []int{0}, res, since)
	vs := items[0].Versions
	if vs[0].Watch.Status != models.WatchKnown || vs[0].Watch.Plays != 1 || *vs[0].Watch != *vs[1].Watch {
		t.Fatalf("versions: %+v / %+v", vs[0].Watch, vs[1].Watch)
	}
	if vs[0].Watch == vs[1].Watch {
		t.Fatal("versions must not share one pointer")
	}
	for _, v := range vs[2:] {
		if v.Watch.Status != models.WatchUnknown || !strings.Contains(v.Watch.Reason, "full-disc backup") {
			t.Fatalf("disc %s: %+v", v.Key, v.Watch)
		}
	}
}
