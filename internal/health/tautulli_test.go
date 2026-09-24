package health

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/tautulli"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// fakeTautulli is a scriptable TautulliClient.
type fakeTautulli struct {
	pms   string
	err   error
	users []tautulli.User
	libs  map[string]*bool
}

func (f *fakeTautulli) Info(context.Context) (*tautulli.Info, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &tautulli.Info{Version: "v2.18.1", PMSIdentifier: f.pms}, nil
}

func (f *fakeTautulli) Users(context.Context) ([]tautulli.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.users, nil
}

func (f *fakeTautulli) Library(_ context.Context, section string) (*tautulli.Library, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &tautulli.Library{SectionID: section, KeepHistory: f.libs[section]}, nil
}

func keep(b bool) *bool { return &b }

func healthyTautulli() *fakeTautulli {
	return &fakeTautulli{pms: "abc123", users: []tautulli.User{{Active: true, KeepHistory: keep(true)}},
		libs: map[string]*bool{"1": keep(true), "2": keep(true)}}
}

// tautulliEnv is a server with two movie libraries and, optionally, a Tautulli connection.
func tautulliEnv(t *testing.T, client *fakeTautulli) (*env, *Checker, models.MediaServer) {
	t.Helper()
	e := newEnv(t)
	def := models.Profile{Name: "Keep Highest Quality", IsDefault: true, KeepCount: 1,
		Criteria: []models.Criterion{{Type: models.CritHealth, Enabled: true}, {Type: models.CritResolution, Enabled: true}}}
	if err := e.db.Profiles().Create(context.Background(), &def); err != nil {
		t.Fatal(err)
	}
	srv := e.addServer("Plex", true, healthyServer())
	e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
	e.addLibrary(srv.ID, "2", "Movies 4K", true, "/data/movies4k")
	if client != nil {
		tt := models.TautulliInstance{Name: "Tautulli", ServerID: srv.ID, URL: "http://tautulli:8181", APIKey: "k", Enabled: true}
		if err := e.db.Tautullis().Create(context.Background(), &tt); err != nil {
			t.Fatal(err)
		}
	}
	c := e.checker()
	c.d.TautulliFactory = func(models.TautulliInstance) TautulliClient {
		if client == nil {
			return nil
		}
		return client
	}
	return e, c, srv
}

// usePlayed adds Played to the default profile.
func usePlayed(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	p, err := e.db.Profiles().GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Criteria = append([]models.Criterion{{Type: models.CritPlayed, Enabled: true}}, p.Criteria...)
	if err := e.db.Profiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
}

func TestTautulliConnectivityCheck(t *testing.T) {
	tests := []struct {
		name   string
		client *fakeTautulli
		want   string // "" = no issue
	}{
		{"healthy", healthyTautulli(), ""},
		{"unreachable", &fakeTautulli{err: errors.New("tautulli: get_tautulli_info: connection refused")}, `Unable to read the Tautulli connection "Tautulli": get_tautulli_info: connection refused`},
		{"key rejected", &fakeTautulli{err: tautulli.ErrUnauthorized}, "unauthorized"},
		{"too old", &fakeTautulli{err: tautulli.ErrTooOld}, "2.18.0"},
		{"another server", &fakeTautulli{pms: "zzz"}, "monitors another Plex server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, c, _ := tautulliEnv(t, tt.client)
			got := bySource(c.Run(context.Background()), SourceTautulliConnectivity)
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatalf("issues %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Type != models.HealthError || !strings.Contains(got[0].Message, tt.want) {
				t.Fatalf("issues %+v, want an error containing %q", got, tt.want)
			}
		})
	}
	// A media server without a stored identity cannot be matched.
	e, c, srv := tautulliEnv(t, healthyTautulli())
	srv.MachineIdentifier = ""
	if err := e.db.MediaServers().Update(context.Background(), &srv); err != nil {
		t.Fatal(err)
	}
	got := bySource(c.Run(context.Background()), SourceTautulliConnectivity)
	if len(got) != 1 || !strings.Contains(got[0].Message, "no machine identifier") {
		t.Fatalf("issues %+v", got)
	}
}

func TestWatchHistoryCheck(t *testing.T) {
	// Without a profile ranking by play history there is nothing to say, Tautulli or not.
	_, c, _ := tautulliEnv(t, nil)
	if got := bySource(c.Run(context.Background()), SourceWatchHistory); len(got) != 0 {
		t.Fatalf("no watch criteria: %+v", got)
	}

	// Played in the default profile, no Tautulli: warning.
	e, c, _ := tautulliEnv(t, nil)
	usePlayed(t, e)
	got := bySource(c.Run(context.Background()), SourceWatchHistory)
	if len(got) != 1 || got[0].Type != models.HealthWarning || !strings.Contains(got[0].Message, "has no enabled Tautulli") ||
		!strings.Contains(got[0].Message, `"Keep Highest Quality"`) {
		t.Fatalf("issues %+v", got)
	}

	// With a Tautulli that keeps everything: nothing.
	e, c, _ = tautulliEnv(t, healthyTautulli())
	usePlayed(t, e)
	if got := bySource(c.Run(context.Background()), SourceWatchHistory); len(got) != 0 {
		t.Fatalf("healthy: %+v", got)
	}

	// Libraries and users without history: a notice naming the libraries and counting users (no names).
	client := healthyTautulli()
	client.libs["2"] = keep(false)
	client.users = append(client.users, tautulli.User{Active: true, KeepHistory: keep(false)}, tautulli.User{Active: true},
		tautulli.User{Active: false, KeepHistory: keep(false)})
	e, c, _ = tautulliEnv(t, client)
	usePlayed(t, e)
	got = bySource(c.Run(context.Background()), SourceWatchHistory)
	if len(got) != 1 || got[0].Type != models.HealthNotice || !strings.Contains(got[0].Message, "Movies 4K") ||
		!strings.Contains(got[0].Message, "2 users") || strings.Contains(got[0].Message, "Movies,") {
		t.Fatalf("issues %+v", got)
	}

	// Only libraries whose profile ranks by play history count: here only "Movies 4K" has its own
	// profile with Last played; the default profile does not rank by play history.
	e, c, srv := tautulliEnv(t, nil)
	ctx := context.Background()
	p := models.Profile{Name: "Watched", KeepCount: 1, Criteria: []models.Criterion{{Type: models.CritLastPlayed, Enabled: true, MinDelta: 30}}}
	if err := e.db.Profiles().Create(ctx, &p); err != nil {
		t.Fatal(err)
	}
	libs, _ := e.db.Libraries().ListByServer(ctx, srv.ID)
	for i := range libs {
		if libs[i].SectionKey == "2" {
			libs[i].ProfileID = &p.ID
			if err := e.db.Libraries().Update(ctx, &libs[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	got = bySource(c.Run(context.Background()), SourceWatchHistory)
	if len(got) != 1 || !strings.Contains(got[0].Message, `"Watched"`) || strings.Contains(got[0].Message, "Keep Highest Quality") {
		t.Fatalf("issues %+v", got)
	}
}
