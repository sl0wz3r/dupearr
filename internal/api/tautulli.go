package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/integrations/tautulli"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Tautulli connections: the play-history source of the played / last_played criteria
// (docs/DECISIONS.md D10, docs/API.md "Watch history (Tautulli)"). They follow the *arr connection
// rules — the API key is masked and only reused for the same endpoint with TLS verification no
// weaker (restoreSecret), a test runs before every save unless ?forceSave=true, upstream bodies are
// never echoed (SEC-031) and every change is audited — with one difference: no re-pointing
// quarantine. Dupearr never acts on Tautulli (no ids of it are stored), and each scan re-checks the
// identity of the Plex server it monitors.

// maxTestedLibraries bounds the libraries a connection test asks about.
const maxTestedLibraries = 50

func maskTautulli(t models.TautulliInstance) models.TautulliInstance {
	if t.APIKey != "" {
		t.APIKey = maskedSecret
	}
	return t
}

// validateTautulli normalises and checks a connection (after the key was restored); the media
// server must exist.
func (s *Server) validateTautulli(ctx context.Context, t *models.TautulliInstance) ([]config.ValidationError, error) {
	t.Name = strings.TrimSpace(t.Name)
	t.APIKey = strings.TrimSpace(t.APIKey)
	errs := validateName("name", t.Name)
	u, uerrs := normalizeBaseURL("url", t.URL)
	t.URL = u
	errs = append(errs, uerrs...)
	switch {
	case t.APIKey == "":
		errs = append(errs, invalid("apiKey", "API key is required"))
	case t.APIKey == maskedSecret:
		errs = append(errs, invalid("apiKey", "Enter the API key"))
	case strings.ContainsAny(t.APIKey, " \t\r\n"):
		errs = append(errs, invalid("apiKey", "API key must not contain whitespace"))
	}
	if t.ServerID <= 0 {
		errs = append(errs, invalid("serverId", "Choose the Plex server this Tautulli monitors"))
	} else if ms, err := s.d.Store.MediaServers().Get(ctx, t.ServerID); errors.Is(err, store.ErrNotFound) {
		errs = append(errs, invalid("serverId", "Media server %d does not exist", t.ServerID))
	} else if err != nil {
		return nil, err
	} else if !ms.Kind.IsPlex() {
		// Tautulli records the plays of a Plex server only (docs/DECISIONS.md D10).
		errs = append(errs, invalid("serverId", "Tautulli monitors Plex servers only; %s is a %s server", ms.Name, ms.Kind.Label()))
	}
	return errs, nil
}

type tautulliTestResult struct {
	Version       string `json:"version"`
	PMSName       string `json:"pmsName"`
	PMSIdentifier string `json:"pmsIdentifier"`
	// ServerMatches: Tautulli monitors the chosen Plex server (a test that finds another server
	// fails, so it is true in every successful answer).
	ServerMatches bool `json:"serverMatches"`
	// HistorySince is the earliest recorded play in the server's enabled libraries (null: none).
	HistorySince *time.Time `json:"historySince"`
	// LibrariesWithoutHistory are the enabled libraries whose history Tautulli does not keep (or
	// does not report keeping): their copies without plays are "unknown", never "no plays".
	LibrariesWithoutHistory []string `json:"librariesWithoutHistory"`
	// UsersWithoutHistory counts the active users whose history is not kept (names are never shown).
	UsersWithoutHistory int `json:"usersWithoutHistory"`
}

// tautulliError maps a Tautulli client error to a client-safe response: configuration problems
// are 400, everything else (unreachable, 5xx) 502. Response bodies are never part of the text.
func tautulliError(err error) error {
	if err == nil {
		return nil
	}
	msg := logging.Redact(strings.TrimPrefix(err.Error(), "tautulli: "))
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, context.DeadlineExceeded):
		return errStatus(http.StatusBadGateway, "Tautulli did not respond in time")
	case errors.Is(err, tautulli.ErrUnauthorized):
		return errBadRequest("Tautulli rejected the API key: %s", msg)
	case errors.Is(err, tautulli.ErrTooOld), errors.Is(err, tautulli.ErrKeyHeaderMissing), errors.Is(err, tautulli.ErrWrongApp), errors.Is(err, tautulli.ErrNotFound),
		errors.Is(err, tautulli.ErrRedirect), errors.Is(err, tautulli.ErrInvalidArgument):
		return errBadRequest("%s", msg)
	}
	return errStatus(http.StatusBadGateway, "Unable to connect to Tautulli: %s", msg)
}

// testTautulli checks the connection answers as Tautulli ≥ 2.18.0 monitoring the chosen Plex
// server, and reports how much of the history can be used.
func (s *Server) testTautulli(ctx context.Context, t models.TautulliInstance) (*tautulliTestResult, error) {
	if s.d.TautulliFactory == nil {
		return nil, errUnavailable("The Tautulli client")
	}
	srv, err := s.d.Store.MediaServers().Get(ctx, t.ServerID)
	if err != nil {
		return nil, notFoundAs(err, "Media server")
	}
	want := strings.TrimSpace(srv.MachineIdentifier)
	if want == "" {
		return nil, errBadRequest("The media server %q has no machine identifier yet: test and save it in Settings → Media Servers first", srv.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	c := s.d.TautulliFactory(t)
	info, err := c.Info(ctx)
	if err != nil {
		return nil, tautulliError(err)
	}
	if !strings.EqualFold(strings.TrimSpace(info.PMSIdentifier), want) {
		name := info.PMSName
		if name == "" {
			name = "an unknown server"
		}
		return nil, errBadRequest("This Tautulli monitors another Plex server (%s), not %s", name, srv.Name)
	}
	res := &tautulliTestResult{Version: info.Version, PMSName: info.PMSName, PMSIdentifier: info.PMSIdentifier,
		ServerMatches: true, LibrariesWithoutHistory: []string{}}
	users, err := c.Users(ctx)
	if err != nil {
		return nil, tautulliError(err)
	}
	for _, u := range users {
		if u.Active && (u.KeepHistory == nil || !*u.KeepHistory) {
			res.UsersWithoutHistory++
		}
	}
	libs, err := s.d.Store.Libraries().ListByServer(ctx, t.ServerID)
	if err != nil {
		return nil, err
	}
	sort.Slice(libs, func(i, j int) bool { return libs[i].Title < libs[j].Title })
	tested := 0
	for _, l := range libs {
		if !l.Enabled || (l.Type != "movie" && l.Type != "show") || tested == maxTestedLibraries {
			continue
		}
		tested++
		lib, err := c.Library(ctx, l.SectionKey)
		if err != nil {
			return nil, tautulliError(err)
		}
		if lib.KeepHistory == nil || !*lib.KeepHistory {
			res.LibrariesWithoutHistory = append(res.LibrariesWithoutHistory, l.Title)
		}
		first, err := c.FirstPlay(ctx, l.SectionKey)
		if err != nil {
			return nil, tautulliError(err)
		}
		if first != nil && !first.Started.IsZero() && (res.HistorySince == nil || first.Started.Before(*res.HistorySince)) {
			since := first.Started
			res.HistorySince = &since
		}
	}
	return res, nil
}

func (s *Server) handleTautullis(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.Tautullis().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := make([]models.TautulliInstance, 0, len(list))
	for _, t := range list {
		out = append(out, maskTautulli(t))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) loadTautulli(r *http.Request) (*models.TautulliInstance, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	t, err := s.d.Store.Tautullis().Get(r.Context(), id)
	return t, notFoundAs(err, "Tautulli connection")
}

func (s *Server) handleTautulli(w http.ResponseWriter, r *http.Request) {
	t, err := s.loadTautulli(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, maskTautulli(*t))
}

// ensureUniqueTautulli refuses a second connection for the same media server (Tautulli monitors
// one Plex server) or to the same Tautulli.
func (s *Server) ensureUniqueTautulli(ctx context.Context, t models.TautulliInstance) error {
	list, err := s.d.Store.Tautullis().List(ctx)
	if err != nil {
		return err
	}
	for _, other := range list {
		if other.ID == t.ID {
			continue
		}
		if other.ServerID == t.ServerID {
			return errConflict("This media server already has a Tautulli connection (%q)", other.Name)
		}
		if !endpointChanged(other.URL, t.URL) {
			return errConflict("This Tautulli is already configured as %q", other.Name)
		}
	}
	return nil
}

// validateTautulliRequest runs validateTautulli and writes the error response when it fails.
func (s *Server) validateTautulliRequest(w http.ResponseWriter, r *http.Request, t *models.TautulliInstance) bool {
	errs, err := s.validateTautulli(r.Context(), t)
	if err != nil {
		s.writeErr(w, r, err)
		return false
	}
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return false
	}
	return true
}

func (s *Server) handleTautulliCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t := models.TautulliInstance{Enabled: true, VerifyTLS: true}
	if err := decodeJSON(r, &t); err != nil {
		s.writeErr(w, r, err)
		return
	}
	t.ID = 0
	if !s.validateTautulliRequest(w, r, &t) {
		return
	}
	if err := s.ensureUniqueTautulli(ctx, t); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if !forceSave(r) {
		if _, err := s.testTautulli(ctx, t); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if err := s.d.Store.Tautullis().Create(ctx, &t); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("Tautulli connection added", "id", t.ID, "name", t.Name, "serverId", t.ServerID)
	s.checkHealthLater(ctx, "Tautulli connection added")
	s.writeJSON(w, http.StatusCreated, maskTautulli(t))
}

func (s *Server) handleTautulliUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stored, err := s.loadTautulli(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	in := *stored
	in.APIKey = maskedSecret
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	in.ID, in.CreatedAt = stored.ID, stored.CreatedAt
	if u, errs := normalizeBaseURL("url", in.URL); len(errs) == 0 {
		in.URL = u
	}
	if e := restoreSecret("apiKey", &in.APIKey, secretEndpoint{in.URL, in.VerifyTLS}, &stored.APIKey, secretEndpoint{stored.URL, stored.VerifyTLS}); e != nil {
		s.writeErr(w, r, errValidation(*e))
		return
	}
	if !s.validateTautulliRequest(w, r, &in) {
		return
	}
	if err := s.ensureUniqueTautulli(ctx, in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if !forceSave(r) {
		if _, err := s.testTautulli(ctx, in); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if err := s.d.Store.Tautullis().Update(ctx, &in); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Tautulli connection"))
		return
	}
	saved, err := s.d.Store.Tautullis().Get(ctx, in.ID)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Tautulli connection"))
		return
	}
	s.log.Info("Tautulli connection updated", "id", saved.ID, "name", saved.Name)
	s.checkHealthLater(ctx, "Tautulli connection updated")
	s.writeJSON(w, http.StatusAccepted, maskTautulli(*saved))
}

func (s *Server) handleTautulliDelete(w http.ResponseWriter, r *http.Request) {
	t, err := s.loadTautulli(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Store.Tautullis().Delete(r.Context(), t.ID); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Tautulli connection"))
		return
	}
	s.log.Info("Tautulli connection deleted", "id", t.ID, "name", t.Name)
	s.checkHealthLater(r.Context(), "Tautulli connection deleted")
	s.writeJSON(w, http.StatusOK, empty)
}

// handleTautulliTest tests an (unsaved) connection; with an id, a masked API key is taken from the
// stored connection (same endpoint, TLS verification no weaker).
func (s *Server) handleTautulliTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	in := models.TautulliInstance{VerifyTLS: true}
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if u, errs := normalizeBaseURL("url", in.URL); len(errs) == 0 {
		in.URL = u
	}
	if in.APIKey == maskedSecret {
		var key *string
		var prev secretEndpoint
		if in.ID > 0 {
			stored, err := s.d.Store.Tautullis().Get(ctx, in.ID)
			if err != nil {
				s.writeErr(w, r, notFoundAs(err, "Tautulli connection"))
				return
			}
			key, prev = &stored.APIKey, secretEndpoint{stored.URL, stored.VerifyTLS}
		}
		if e := restoreSecret("apiKey", &in.APIKey, secretEndpoint{in.URL, in.VerifyTLS}, key, prev); e != nil {
			s.writeErr(w, r, errValidation(*e))
			return
		}
	}
	if !s.validateTautulliRequest(w, r, &in) {
		return
	}
	res, err := s.testTautulli(ctx, in)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}
