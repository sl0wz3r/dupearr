package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
)

// notificationTestTimeout bounds POST /notification/test (the provider has its own timeout too).
const notificationTestTimeout = 45 * time.Second

// normalizeNotification fills nil settings/triggers so the resource never encodes null.
func normalizeNotification(n models.NotificationConfig) models.NotificationConfig {
	n.Name = strings.TrimSpace(n.Name)
	n.Kind = strings.ToLower(strings.TrimSpace(n.Kind))
	if len(n.Settings) == 0 || string(n.Settings) == "null" {
		n.Settings = json.RawMessage("{}")
	}
	if n.Triggers == nil {
		n.Triggers = []string{}
	}
	return n
}

func maskNotification(n models.NotificationConfig) models.NotificationConfig {
	return normalizeNotification(notifications.MaskSecrets(normalizeNotification(n)))
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.Notifications().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := make([]models.NotificationConfig, 0, len(list))
	for _, n := range list {
		out = append(out, maskNotification(n))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) loadNotification(r *http.Request) (*models.NotificationConfig, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	n, err := s.d.Store.Notifications().Get(r.Context(), id)
	return n, notFoundAs(err, "Notification")
}

func (s *Server) handleNotification(w http.ResponseWriter, r *http.Request) {
	n, err := s.loadNotification(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, maskNotification(*n))
}

func (s *Server) handleNotificationCreate(w http.ResponseWriter, r *http.Request) {
	var n models.NotificationConfig
	if err := decodeJSON(r, &n); err != nil {
		s.writeErr(w, r, err)
		return
	}
	n = normalizeNotification(n)
	n.ID = 0
	if errs := notifications.ValidateConfig(n); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.Notifications().Create(r.Context(), &n); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("Notification connection added", "id", n.ID, "name", n.Name, "kind", n.Kind)
	s.writeJSON(w, http.StatusCreated, maskNotification(n))
}

// handleNotificationUpdate replaces a connection; masked secrets keep their stored values (only
// for the same destination, see notifications.MergeSecrets).
func (s *Server) handleNotificationUpdate(w http.ResponseWriter, r *http.Request) {
	stored, err := s.loadNotification(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var in models.NotificationConfig
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	in = normalizeNotification(in)
	in.ID = stored.ID
	merged := normalizeNotification(notifications.MergeSecrets(in, *stored))
	if errs := notifications.ValidateConfig(merged); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.Notifications().Update(r.Context(), &merged); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Notification"))
		return
	}
	s.log.Info("Notification connection updated", "id", merged.ID, "name", merged.Name)
	s.writeJSON(w, http.StatusAccepted, maskNotification(merged))
}

func (s *Server) handleNotificationDelete(w http.ResponseWriter, r *http.Request) {
	n, err := s.loadNotification(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Store.Notifications().Delete(r.Context(), n.ID); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Notification"))
		return
	}
	s.writeJSON(w, http.StatusOK, empty)
}

// handleNotificationTest sends a test message with an unsaved configuration (masked secrets are
// restored from the stored connection when an id is given).
func (s *Server) handleNotificationTest(w http.ResponseWriter, r *http.Request) {
	if s.d.Notifier == nil {
		s.writeErr(w, r, errUnavailable("Notifications"))
		return
	}
	var in models.NotificationConfig
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	in = normalizeNotification(in)
	if in.ID > 0 {
		stored, err := s.d.Store.Notifications().Get(r.Context(), in.ID)
		if err != nil {
			s.writeErr(w, r, notFoundAs(err, "Notification"))
			return
		}
		in = normalizeNotification(notifications.MergeSecrets(in, *stored))
	}
	if errs := notifications.ValidateConfig(in); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), notificationTestTimeout)
	defer cancel()
	err := s.d.Notifier.Test(ctx, in)
	var vfe *notifications.ValidationFailedError
	switch {
	case err == nil:
		s.writeJSON(w, http.StatusOK, empty)
	case errors.As(err, &vfe):
		s.writeErr(w, r, errValidation(vfe.Errors...))
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		s.writeErr(w, r, err)
	default:
		// Provider errors never contain secrets (notifications redacts them); redact again anyway.
		s.writeErr(w, r, errBadRequest("%s", truncate(logging.Redact(err.Error()), maxMessageLen)))
	}
}

func (s *Server) handleNotificationSchema(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, notifications.Schema())
}

func (s *Server) handleNotificationTriggers(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, notifications.TriggerOptions())
}
