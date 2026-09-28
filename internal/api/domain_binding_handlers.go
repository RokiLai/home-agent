package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"homeagent/internal/auth"
	"homeagent/internal/domainbinding"
)

type domainBindingRequest struct {
	FQDN             string `json:"fqdn"`
	ExpectedRevision uint64 `json:"expected_revision"`
	ExistingRecord   bool   `json:"existing_record"`
}
type domainBindingTransitionRequest struct {
	ExpectedRevision           uint64 `json:"expected_revision"`
	PublisherDisabledConfirmed bool   `json:"publisher_disabled_confirmed"`
}

func (s *Server) listDeviceDomainBindings(w http.ResponseWriter, r *http.Request) {
	s.listDomainBindings(w, r, domainbinding.SourceDevice, r.PathValue("id"))
}
func (s *Server) listServerDomainBindings(w http.ResponseWriter, r *http.Request) {
	s.listDomainBindings(w, r, domainbinding.SourceServer, domainbinding.LocalServerSourceID)
}
func (s *Server) listDomainBindings(w http.ResponseWriter, r *http.Request, sourceType domainbinding.SourceType, sourceID string) {
	if s.DomainBindings == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "domain_bindings_unavailable"})
		return
	}
	bindings, err := s.DomainBindings.List(r.Context(), sourceType, sourceID)
	if err != nil {
		statusError(w, err)
		return
	}
	revision, err := s.DomainBindings.ControlPlaneRevision(r.Context())
	if err != nil {
		statusError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": bindings, "revision": revision, "provider": map[string]any{"enabled": s.DomainBindingConfig.Enabled, "state": s.DomainBindingConfig.State, "diagnostic": s.DomainBindingConfig.Diagnostic}})
}
func (s *Server) preflightDeviceDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.preflightDomainBinding(w, r, domainbinding.SourceDevice, r.PathValue("id"))
}
func (s *Server) preflightServerDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.preflightDomainBinding(w, r, domainbinding.SourceServer, domainbinding.LocalServerSourceID)
}
func (s *Server) preflightDomainBinding(w http.ResponseWriter, r *http.Request, sourceType domainbinding.SourceType, sourceID string) {
	request, ok := decodeDomainBindingRequest(w, r)
	if !ok {
		return
	}
	preflight, err := s.DomainBindings.Preflight(r.Context(), request.FQDN)
	if err != nil {
		writeDomainBindingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fqdn": preflight.FQDN, "source_type": sourceType, "source_id": sourceID, "available": preflight.Available, "writes_dns": false, "provider_configured": s.DomainBindingConfig.Enabled, "record": preflight.Observation})
}
func (s *Server) createDeviceDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.createDomainBinding(w, r, domainbinding.SourceDevice, r.PathValue("id"))
}
func (s *Server) createServerDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.createDomainBinding(w, r, domainbinding.SourceServer, domainbinding.LocalServerSourceID)
}
func (s *Server) createDomainBinding(w http.ResponseWriter, r *http.Request, sourceType domainbinding.SourceType, sourceID string) {
	request, ok := decodeDomainBindingRequest(w, r)
	if !ok {
		return
	}
	ownerUserID := "legacy-admin"
	if actor := auth.GetActorFromContext(r.Context()); actor != nil && actor.UserID != "" {
		ownerUserID = actor.UserID
	}
	binding, err := s.DomainBindings.Create(r.Context(), domainbinding.CreateCommand{SourceType: sourceType, SourceID: sourceID, OwnerUserID: ownerUserID, FQDN: request.FQDN, ExpectedRevision: request.ExpectedRevision, ExistingRecord: request.ExistingRecord})
	if err != nil {
		writeDomainBindingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, binding)
}
func (s *Server) enableDeviceDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.enableScopedDomainBinding(w, r, domainbinding.SourceDevice, r.PathValue("id"))
}
func (s *Server) enableServerDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.enableScopedDomainBinding(w, r, domainbinding.SourceServer, domainbinding.LocalServerSourceID)
}
func (s *Server) enableScopedDomainBinding(w http.ResponseWriter, r *http.Request, sourceType domainbinding.SourceType, sourceID string) {
	request, ok := decodeDomainBindingTransition(w, r)
	if !ok {
		return
	}
	if !s.bindingBelongsToSource(r, r.PathValue("binding_id"), sourceType, sourceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	binding, err := s.DomainBindings.Enable(r.Context(), domainbinding.EnableCommand{BindingID: r.PathValue("binding_id"), ExpectedRevision: request.ExpectedRevision, PublisherDisabledConfirmed: request.PublisherDisabledConfirmed})
	if err != nil {
		writeDomainBindingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, binding)
}
func (s *Server) disableDeviceDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.disableScopedDomainBinding(w, r, domainbinding.SourceDevice, r.PathValue("id"))
}
func (s *Server) disableServerDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.disableScopedDomainBinding(w, r, domainbinding.SourceServer, domainbinding.LocalServerSourceID)
}
func (s *Server) disableScopedDomainBinding(w http.ResponseWriter, r *http.Request, sourceType domainbinding.SourceType, sourceID string) {
	request, ok := decodeDomainBindingTransition(w, r)
	if !ok {
		return
	}
	if !s.bindingBelongsToSource(r, r.PathValue("binding_id"), sourceType, sourceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	binding, err := s.DomainBindings.Disable(r.Context(), r.PathValue("binding_id"), request.ExpectedRevision)
	if err != nil {
		writeDomainBindingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, binding)
}
func (s *Server) deleteDeviceDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.deleteScopedDomainBinding(w, r, domainbinding.SourceDevice, r.PathValue("id"))
}
func (s *Server) deleteServerDomainBinding(w http.ResponseWriter, r *http.Request) {
	s.deleteScopedDomainBinding(w, r, domainbinding.SourceServer, domainbinding.LocalServerSourceID)
}
func (s *Server) deleteScopedDomainBinding(w http.ResponseWriter, r *http.Request, sourceType domainbinding.SourceType, sourceID string) {
	request, ok := decodeDomainBindingTransition(w, r)
	if !ok {
		return
	}
	if !s.bindingBelongsToSource(r, r.PathValue("binding_id"), sourceType, sourceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	binding, err := s.DomainBindings.Delete(r.Context(), r.PathValue("binding_id"), request.ExpectedRevision)
	if err != nil {
		writeDomainBindingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, binding)
}

func (s *Server) bindingBelongsToSource(r *http.Request, bindingID string, sourceType domainbinding.SourceType, sourceID string) bool {
	bindings, err := s.DomainBindings.List(r.Context(), sourceType, sourceID)
	if err != nil {
		return false
	}
	for _, binding := range bindings {
		if binding.BindingID == bindingID {
			return true
		}
	}
	return false
}
func decodeDomainBindingRequest(w http.ResponseWriter, r *http.Request) (domainBindingRequest, bool) {
	defer r.Body.Close()
	var request domainBindingRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return request, false
	}
	return request, true
}
func decodeDomainBindingTransition(w http.ResponseWriter, r *http.Request) (domainBindingTransitionRequest, bool) {
	defer r.Body.Close()
	var request domainBindingTransitionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return request, false
	}
	return request, true
}
func writeDomainBindingError(w http.ResponseWriter, err error) {
	var providerError domainbinding.ClassifiedError
	if errors.As(err, &providerError) {
		status := http.StatusBadGateway
		if providerError.CanRetry() {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"error": "provider_" + providerError.Category(), "retryable": providerError.CanRetry()})
		return
	}
	switch {
	case errors.Is(err, domainbinding.ErrInvalidFQDN):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fqdn"})
	case errors.Is(err, domainbinding.ErrFQDNConflict), errors.Is(err, domainbinding.ErrBindingRevisionConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
	case errors.Is(err, domainbinding.ErrBindingNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, domainbinding.ErrPublisherConfirmationRequired):
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "publisher_confirmation_required"})
	default:
		statusError(w, err)
	}
}
