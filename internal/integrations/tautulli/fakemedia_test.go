package tautulli

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// TestClientAgainstFakeTautulli cross-checks the client with fakemedia's Tautulli (which emulates
// the real server's defaults: grouping and live activity on, 25-row pages, HTML-escaped strings,
// ?apikey= winning over the header).
func TestClientAgainstFakeTautulli(t *testing.T) {
	env := fakemedia.Start(t, fakemedia.Watch())
	c := New(models.TautulliInstance{URL: env.Tautulli.URL, APIKey: env.TautulliAPIKey}, Options{Timeout: 5 * time.Second})
	ctx := context.Background()

	info, err := c.Info(ctx)
	if err != nil || info.PMSIdentifier != env.MachineIdentifier || info.Version != fakemedia.DefaultTautulliVersion {
		t.Fatalf("info %+v, %v", info, err)
	}
	users, err := c.Users(ctx)
	if err != nil || len(users) != 3 || users[0].KeepHistory == nil || !*users[0].KeepHistory {
		t.Fatalf("users %+v, %v", users, err)
	}
	lib, err := c.Library(ctx, fakemedia.SectionMovies)
	if err != nil || lib.KeepHistory == nil || !*lib.KeepHistory {
		t.Fatalf("library %+v, %v", lib, err)
	}
	first, err := c.FirstPlay(ctx, fakemedia.SectionMovies)
	if err != nil || first == nil || first.RatingKey != env.RatingKey(fakemedia.SectionMovies, "Heat") {
		t.Fatalf("first play %+v, %v", first, err)
	}

	sicario := env.RatingKey(fakemedia.SectionMovies, "Sicario")
	arrival := env.RatingKey(fakemedia.SectionMovies, "Arrival")
	env.SetPlaying(sicario) // a session in progress is not a recorded play
	rows, err := c.History(ctx, HistoryFilter{RatingKeys: []string{sicario, arrival}, PageSize: 2})
	if err != nil || len(rows) != 5 {
		t.Fatalf("history: %d rows, %v", len(rows), err)
	}
	env.SetPlaying()

	for _, tc := range []struct {
		name string
		mode fakemedia.TautulliMode
		call func() error
		want error
	}{
		{"old Tautulli", fakemedia.TautulliMode{OldVersion: true}, func() error { _, err := c.Info(ctx); return err }, ErrTooOld},
		{"result error", fakemedia.TautulliMode{ResultError: true}, func() error {
			_, err := c.History(ctx, HistoryFilter{SectionID: fakemedia.SectionMovies})
			return err
		}, ErrCommandFailed},
		{"short page", fakemedia.TautulliMode{ShortPage: true}, func() error {
			_, err := c.History(ctx, HistoryFilter{SectionID: fakemedia.SectionMovies, PageSize: 2})
			return err
		}, ErrIncomplete},
		{"key list ignored", fakemedia.TautulliMode{IgnoreKeyList: true}, func() error {
			rows, err := c.History(ctx, HistoryFilter{RatingKeys: []string{sicario, arrival}})
			if err == nil && len(rows) == 0 {
				return errNoRows // what the scanner's canary check catches
			}
			return err
		}, errNoRows},
	} {
		env.SetTautulliMode(tc.mode)
		if err := tc.call(); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	env.SetTautulliMode(fakemedia.TautulliMode{Down: true})
	var he *HTTPError
	if _, err := c.Info(ctx); !errors.As(err, &he) || he.StatusCode != 503 || !Transient(err) {
		t.Errorf("down: %v", err)
	}
	env.SetTautulliMode(fakemedia.TautulliMode{WrongIdentity: true})
	if info, err := c.Info(ctx); err != nil || info.PMSIdentifier == env.MachineIdentifier {
		t.Errorf("wrong identity: %+v, %v", info, err)
	}
	env.SetTautulliMode(fakemedia.TautulliMode{})
	// A reverse proxy that drops the header: Tautulli 2.18+ says no key arrived, which is not
	// "too old" (the advice would be wrong).
	dropping := New(models.TautulliInstance{URL: env.Tautulli.URL, APIKey: env.TautulliAPIKey}, Options{
		Timeout: 5 * time.Second, HTTPClient: &http.Client{Transport: dropHeader{http.DefaultTransport}},
	})
	if _, err := dropping.Info(ctx); !errors.Is(err, ErrKeyHeaderMissing) || errors.Is(err, ErrTooOld) {
		t.Errorf("header dropped: %v", err)
	}
	env.AssertNoViolations(t) // the key never reached a URL
}

var errNoRows = errors.New("no rows")

// dropHeader is a transport that loses the X-Api-Key header on the way, like a misconfigured proxy.
type dropHeader struct{ next http.RoundTripper }

func (d dropHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Del("X-Api-Key")
	return d.next.RoundTrip(r)
}
