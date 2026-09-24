package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/backup"
	"github.com/sl0wz3r/dupearr/internal/commands"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// recentCommands is how many commands GET /api/v1/command returns.
const recentCommands = 50

// ratingKeyRe validates Plex rating keys accepted from clients (docs/DECISIONS.md D2).
var ratingKeyRe = regexp.MustCompile(`^[0-9A-Za-z]{1,64}$`)

// imdbRe validates IMDb ids.
var imdbRe = regexp.MustCompile(`^tt[0-9]{1,12}$`)

// commandMetaKeys are *arr command envelope fields that are not part of a command body.
var commandMetaKeys = map[string]bool{
	"name": true, "trigger": true, "sendUpdatesToClient": true, "updateScheduledTask": true,
	"suppressMessages": true, "clientUserAgent": true, "priority": true,
}

// pageOf returns an empty page echoing p.
func pageOf[T any](p store.Paging) store.Page[T] {
	return store.Page[T]{Page: p.Page, PageSize: p.PageSize, SortKey: p.SortKey, SortDirection: p.SortDirection, Records: []T{}}
}

// enqueue queues a command through the command manager.
func (s *Server) enqueue(ctx context.Context, name string, body any, trigger string) (*models.Command, error) {
	if s.d.Commands == nil {
		return nil, errUnavailable("The command queue")
	}
	cmd, err := s.d.Commands.Enqueue(ctx, name, body, trigger)
	switch {
	case errors.Is(err, commands.ErrUnknownCommand):
		return nil, errValidation(invalid("name", "Unknown command '%s'", truncate(name, 64)))
	case errors.Is(err, commands.ErrInvalidBody):
		return nil, errBadRequest("Invalid command body")
	case errors.Is(err, commands.ErrStopped):
		return nil, errStatus(http.StatusServiceUnavailable, "Dupearr is shutting down")
	case err != nil:
		return nil, err
	}
	return cmd, nil
}

// writeCommandCreated answers 201 with the command and its Location.
func (s *Server) writeCommandCreated(w http.ResponseWriter, cmd *models.Command) {
	w.Header().Set("Location", s.urlBase()+"/api/v1/command/"+strconv.FormatInt(cmd.ID, 10))
	s.writeJSON(w, http.StatusCreated, cmd)
}

// handleCommandCreate queues {"name": "...", ...body fields}. Bodies of the known commands are
// validated and reduced to their documented fields (so equivalent requests deduplicate); an
// unknown or unregistered name is a 400.
func (s *Server) handleCommandCreate(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		s.writeErr(w, r, err)
		return
	}
	var name string
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &name); err != nil {
			s.writeErr(w, r, errValidation(invalid("name", "Must be a string")))
			return
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		s.writeErr(w, r, errValidation(invalid("name", "Name is required")))
		return
	}
	rest := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		if !commandMetaKeys[k] {
			rest[k] = v
		}
	}
	body, err := commandBody(name, rest)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	cmd, err := s.enqueue(r.Context(), name, body, models.TriggerManual)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeCommandCreated(w, cmd)
}

// commandBody decodes and validates the body of a known command; other names keep their fields.
func commandBody(name string, fields map[string]json.RawMessage) (any, error) {
	decode := func(dst any) error {
		b, err := json.Marshal(fields)
		if err != nil {
			return errBadRequest("Invalid command body")
		}
		if err := json.Unmarshal(b, dst); err != nil {
			return errBadRequest("Invalid %s body: %s", name, jsonProblem(err))
		}
		return nil
	}
	switch {
	case strings.EqualFold(name, models.CmdDuplicateScan):
		var b models.DuplicateScanBody
		if err := decode(&b); err != nil {
			return nil, err
		}
		for _, id := range b.LibraryIDs {
			if id <= 0 {
				return nil, errValidation(invalid("libraryIds", "Library ids must be positive"))
			}
		}
		return b, nil
	case strings.EqualFold(name, models.CmdTargetedScan):
		var b models.TargetedScanBody
		if err := decode(&b); err != nil {
			return nil, err
		}
		b.ImdbID = strings.ToLower(strings.TrimSpace(b.ImdbID))
		if errs := validateTargetedScan(b); len(errs) > 0 {
			return nil, errValidation(errs...)
		}
		return b, nil
	case strings.EqualFold(name, models.CmdSyncLibraries):
		var b struct {
			ServerID int64 `json:"serverId,omitempty"`
		}
		if err := decode(&b); err != nil {
			return nil, err
		}
		if b.ServerID < 0 {
			return nil, errValidation(invalid("serverId", "Must be a positive id"))
		}
		return b, nil
	case strings.EqualFold(name, models.CmdBackup):
		var b struct {
			Type string `json:"type,omitempty"`
		}
		if err := decode(&b); err != nil {
			return nil, err
		}
		switch b.Type {
		case "", backup.TypeManual, backup.TypeScheduled, backup.TypeUpdate:
		default:
			return nil, errValidation(invalid("type", "Must be one of: manual, scheduled, update"))
		}
		return b, nil
	case strings.EqualFold(name, models.CmdProcessQueue), strings.EqualFold(name, models.CmdCheckHealth),
		strings.EqualFold(name, models.CmdHousekeeping), strings.EqualFold(name, models.CmdCleanRecycleBin):
		return struct{}{}, nil
	}
	return fields, nil
}

// validateTargetedScan requires at least one target: a targeted scan must never widen to the
// whole library.
func validateTargetedScan(b models.TargetedScanBody) []config.ValidationError {
	var errs []config.ValidationError
	if b.ServerID < 0 {
		errs = append(errs, invalid("serverId", "Must be a positive id"))
	}
	for _, rk := range b.RatingKeys {
		if !ratingKeyRe.MatchString(rk) {
			errs = append(errs, invalid("ratingKeys", "Invalid rating key '%s'", truncate(rk, 64)))
			break
		}
	}
	if b.TmdbID < 0 {
		errs = append(errs, invalid("tmdbId", "Must be a positive id"))
	}
	if b.TvdbID < 0 {
		errs = append(errs, invalid("tvdbId", "Must be a positive id"))
	}
	if b.ImdbID != "" && !imdbRe.MatchString(b.ImdbID) {
		errs = append(errs, invalid("imdbId", "Must look like tt1234567"))
	}
	if len(b.RatingKeys) == 0 && b.TmdbID <= 0 && b.TvdbID <= 0 && b.ImdbID == "" {
		errs = append(errs, invalid("ratingKeys", "A targeted scan needs ratingKeys, tmdbId, tvdbId or imdbId"))
	}
	return errs
}

func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if s.d.Commands == nil {
		s.writeJSON(w, http.StatusOK, []models.Command{})
		return
	}
	list, err := s.d.Commands.Recent(r.Context(), recentCommands)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if s.d.Commands == nil {
		s.writeErr(w, r, errNotFound("Command"))
		return
	}
	cmd, err := s.d.Commands.Get(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Command"))
		return
	}
	s.writeJSON(w, http.StatusOK, cmd)
}
