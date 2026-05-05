package api

import (
	"encoding/json"
	"net/http"

	"github.com/good-native/hover/internal/util"
)

// CreateDomainRequest represents the request body for POST /v1/domains
type CreateDomainRequest struct {
	Domain string `json:"domain"`
}

// DomainResponse represents a domain in API responses
type DomainResponse struct {
	DomainID int    `json:"domain_id"`
	Domain   string `json:"domain"`
}

// DomainsHandler handles requests to /v1/domains
func (h *Handler) DomainsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.createDomain(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

// createDomain handles POST /v1/domains - creates domain without job side effects
func (h *Handler) createDomain(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	// Validate auth and get organisation
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	var req CreateDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	if req.Domain == "" {
		BadRequest(w, r, "Domain is required")
		return
	}

	// Normalise and validate domain format
	normalisedDomain := util.NormaliseDomain(req.Domain)
	if err := util.ValidateDomain(normalisedDomain); err != nil {
		BadRequest(w, r, err.Error())
		return
	}

	// Get or create domain ID (no job creation)
	domainID, err := h.DB.GetOrCreateDomainID(r.Context(), normalisedDomain)
	if err != nil {
		logger.Error("Failed to get or create domain", "error", err, "domain", normalisedDomain)
		InternalError(w, r, err)
		return
	}

	if err := h.DB.UpsertOrganisationDomain(r.Context(), orgID, domainID); err != nil {
		logger.Error("Failed to associate domain with organisation", "error", err, "organisation_id", orgID, "domain_id", domainID)
		InternalError(w, r, err)
		return
	}

	response := DomainResponse{
		DomainID: domainID,
		Domain:   normalisedDomain,
	}

	WriteCreated(w, r, response, "Domain registered successfully")
}
