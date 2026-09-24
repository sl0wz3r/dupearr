package api

import (
	"net/http"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/models"
)

type profileSchema struct {
	Criteria        []engine.CriterionSchema `json:"criteria"`
	Templates       []models.Profile         `json:"templates"`
	ProtectionTypes []string                 `json:"protectionTypes"`
	KeepPer         []string                 `json:"keepPer"`
}

func (s *Server) handleProfileSchema(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, profileSchema{
		Criteria:  engine.CriteriaSchema(),
		Templates: engine.ProfileTemplates(),
		ProtectionTypes: []string{
			models.ProtectPathGlob, models.ProtectLibrary, models.ProtectArrInstance, models.ProtectArrTag,
		},
		KeepPer: []string{models.KeepPerNone, models.KeepPerResolution, models.KeepPerDynamicRange},
	})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.Profiles().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) loadProfile(r *http.Request) (*models.Profile, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	p, err := s.d.Store.Profiles().Get(r.Context(), id)
	return p, notFoundAs(err, "Profile")
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.loadProfile(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

// decodeProfile reads a profile body (always into a fresh value: profiles hold slices of structs,
// which encoding/json would merge into existing elements).
func decodeProfile(r *http.Request) (models.Profile, error) {
	var p models.Profile
	if err := decodeJSON(r, &p); err != nil {
		return p, err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Criteria == nil {
		p.Criteria = []models.Criterion{}
	}
	if p.Protections == nil {
		p.Protections = []models.Protection{}
	}
	return p, nil
}

func (s *Server) handleProfileCreate(w http.ResponseWriter, r *http.Request) {
	p, err := decodeProfile(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	p.ID = 0
	if errs := engine.ValidateProfile(p); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.Profiles().Create(r.Context(), &p); err != nil {
		s.writeErr(w, r, err)
		return
	}
	saved, err := s.d.Store.Profiles().Get(r.Context(), p.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("Profile created", "id", saved.ID, "name", saved.Name, "default", saved.IsDefault)
	if saved.IsDefault {
		s.reevaluateAllAsync("default profile changed")
	}
	s.checkHealthLater(r.Context(), "profile added") // WatchHistoryCheck
	s.writeJSON(w, http.StatusCreated, saved)
}

// handleProfileUpdate replaces a profile and re-evaluates open groups. The default flag can only
// move to another profile, never be cleared (there must always be a default profile).
func (s *Server) handleProfileUpdate(w http.ResponseWriter, r *http.Request) {
	stored, err := s.loadProfile(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	p, err := decodeProfile(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	p.ID, p.CreatedAt = stored.ID, stored.CreatedAt
	errs := engine.ValidateProfile(p)
	if stored.IsDefault && !p.IsDefault {
		errs = append(errs, invalid("isDefault", "There must be a default profile: make another profile the default instead"))
	}
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.Profiles().Update(r.Context(), &p); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Profile"))
		return
	}
	saved, err := s.d.Store.Profiles().Get(r.Context(), p.ID)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Profile"))
		return
	}
	s.log.Info("Profile updated", "id", saved.ID, "name", saved.Name)
	s.reevaluateAllAsync("profile changed")
	s.checkHealthLater(r.Context(), "profile updated") // WatchHistoryCheck
	s.writeJSON(w, http.StatusAccepted, saved)
}

// handleProfileDelete deletes a non-default profile (409 for the default one); libraries using it
// fall back to the default profile, so open groups are re-evaluated.
func (s *Server) handleProfileDelete(w http.ResponseWriter, r *http.Request) {
	p, err := s.loadProfile(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if p.IsDefault {
		s.writeErr(w, r, errConflict("The default profile cannot be deleted; make another profile the default first"))
		return
	}
	if err := s.d.Store.Profiles().Delete(r.Context(), p.ID); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Profile"))
		return
	}
	s.log.Info("Profile deleted", "id", p.ID, "name", p.Name)
	s.reevaluateAllAsync("profile deleted")
	s.checkHealthLater(r.Context(), "profile deleted") // WatchHistoryCheck
	s.writeJSON(w, http.StatusOK, empty)
}
