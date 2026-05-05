package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/good-native/hover/internal/auth"
	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/loops"
	"github.com/good-native/hover/internal/util"
	"github.com/google/uuid"
)

// OrganisationsHandler handles GET and POST /v1/organisations
func (h *Handler) OrganisationsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listUserOrganisations(w, r)
	case http.MethodPost:
		h.createOrganisation(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

// listUserOrganisations returns all organisations the user is a member of
func (h *Handler) listUserOrganisations(w http.ResponseWriter, r *http.Request) {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	// Ensure user exists in database
	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	orgs, err := h.DB.ListUserOrganisations(userClaims.UserID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	// Format organisations for response
	type OrgResponse struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
	}

	formattedOrgs := make([]OrgResponse, len(orgs))
	for i, org := range orgs {
		formattedOrgs[i] = OrgResponse{
			ID:        org.ID,
			Name:      org.Name,
			CreatedAt: org.CreatedAt.Format(time.RFC3339),
		}
	}

	WriteSuccess(w, r, map[string]any{
		"organisations":          formattedOrgs,
		"active_organisation_id": user.ActiveOrganisationID,
	}, "Organisations retrieved successfully")
}

// CreateOrganisationRequest represents the request to create an organisation
type CreateOrganisationRequest struct {
	Name string `json:"name"`
}

// createOrganisation creates a new organisation and adds the user as a member
func (h *Handler) createOrganisation(w http.ResponseWriter, r *http.Request) {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	var req CreateOrganisationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	// Validate name
	name := strings.TrimSpace(req.Name)
	if name == "" {
		BadRequest(w, r, "name is required")
		return
	}
	if len(name) > 100 {
		BadRequest(w, r, "name must be 100 characters or fewer")
		return
	}

	// Create the organisation atomically (duplicate-name check, member insert, and
	// active-org update are all performed inside a single transaction to prevent
	// TOCTOU races when concurrent requests arrive for the same user).
	org, err := h.DB.CreateOrganisationForUser(userClaims.UserID, name)
	if err != nil {
		if errors.Is(err, db.ErrDuplicateOrganisationName) {
			BadRequest(w, r, "An organisation with that name already exists")
			return
		}
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"organisation": map[string]any{
			"id":         org.ID,
			"name":       org.Name,
			"created_at": org.CreatedAt.Format(time.RFC3339),
			"updated_at": org.UpdatedAt.Format(time.RFC3339),
		},
	}, "Organisation created successfully")
}

// SwitchOrganisationHandler handles POST /v1/organisations/switch
func (h *Handler) SwitchOrganisationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}
	h.switchOrganisation(w, r)
}

// SwitchOrganisationRequest represents the request to switch active organisation
type SwitchOrganisationRequest struct {
	OrganisationID string `json:"organisation_id"`
}

func (h *Handler) switchOrganisation(w http.ResponseWriter, r *http.Request) {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	var req SwitchOrganisationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	if req.OrganisationID == "" {
		BadRequest(w, r, "organisation_id is required")
		return
	}

	// Validate membership
	valid, err := h.DB.ValidateOrganisationMembership(userClaims.UserID, req.OrganisationID)
	if err != nil {
		InternalError(w, r, err)
		return
	}
	if !valid {
		Forbidden(w, r, "Not a member of this organisation")
		return
	}

	// Set active organisation
	err = h.DB.SetActiveOrganisation(userClaims.UserID, req.OrganisationID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	// Get organisation details for response
	org, err := h.DB.GetOrganisation(req.OrganisationID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"organisation": map[string]any{
			"id":         org.ID,
			"name":       org.Name,
			"created_at": org.CreatedAt.Format(time.RFC3339),
			"updated_at": org.UpdatedAt.Format(time.RFC3339),
		},
	}, "Organisation switched successfully")
}

// UsageHandler handles GET /v1/usage
// Returns current usage statistics for the user's active organisation
func (h *Handler) UsageHandler(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	// Get user's active organisation using the helper
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written by helper
	}

	// Get usage stats from database
	stats, err := h.DB.GetOrganisationUsageStats(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	logger.Info("Usage statistics retrieved", "organisation_id", orgID, "daily_used", stats.DailyUsed, "daily_limit", stats.DailyLimit)

	WriteSuccess(w, r, map[string]any{
		"usage": stats,
	}, "Usage statistics retrieved successfully")
}

// PublicPlan is a DTO for the public /v1/plans endpoint
// Excludes internal metadata fields (is_active, sort_order, created_at)
type PublicPlan struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	DailyPageLimit    int    `json:"daily_page_limit"`
	MonthlyPriceCents int    `json:"monthly_price_cents"`
}

// PlansHandler handles GET /v1/plans
// Returns available subscription plans (public endpoint for pricing page)
func (h *Handler) PlansHandler(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	plans, err := h.DB.GetActivePlans(r.Context())
	if err != nil {
		InternalError(w, r, err)
		return
	}

	// Transform to public DTOs (filter out internal metadata)
	publicPlans := make([]PublicPlan, len(plans))
	for i, p := range plans {
		publicPlans[i] = PublicPlan{
			ID:                p.ID,
			Name:              p.Name,
			DisplayName:       p.DisplayName,
			DailyPageLimit:    p.DailyPageLimit,
			MonthlyPriceCents: p.MonthlyPriceCents,
		}
	}

	logger.Info("Plans retrieved", "plan_count", len(publicPlans))

	WriteSuccess(w, r, map[string]any{
		"plans": publicPlans,
	}, "Plans retrieved successfully")
}

// OrganisationMembersHandler handles GET /v1/organisations/members
func (h *Handler) OrganisationMembersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	// Ensure user exists in database
	_, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	members, err := h.DB.ListOrganisationMembers(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	currentRole, err := h.DB.GetOrganisationMemberRole(r.Context(), userClaims.UserID, orgID)
	if err != nil {
		Forbidden(w, r, "Not a member of this organisation")
		return
	}

	responseMembers := make([]map[string]any, 0, len(members))
	for _, member := range members {
		displayName := strings.TrimSpace(member.Email)
		if at := strings.Index(displayName, "@"); at > 0 {
			displayName = displayName[:at]
		}
		if derivedFull := composeFullName(member.FirstName, member.LastName); derivedFull != nil {
			displayName = *derivedFull
		}
		if member.FullName != nil && strings.TrimSpace(*member.FullName) != "" {
			displayName = strings.TrimSpace(*member.FullName)
		}

		responseMembers = append(responseMembers, map[string]any{
			"id":         member.UserID,
			"email":      member.Email,
			"first_name": member.FirstName,
			"last_name":  member.LastName,
			"full_name":  displayName,
			"role":       member.Role,
			"created_at": member.CreatedAt.Format(time.RFC3339),
		})
	}

	WriteSuccess(w, r, map[string]any{
		"members":           responseMembers,
		"current_user_id":   userClaims.UserID,
		"current_user_role": currentRole,
	}, "Organisation members retrieved successfully")
}

type UpdateOrganisationMemberRoleRequest struct {
	Role string `json:"role"`
}

// OrganisationMemberHandler handles PATCH/DELETE /v1/organisations/members/:id
func (h *Handler) OrganisationMemberHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPatch:
		h.updateOrganisationMemberRole(w, r)
	case http.MethodDelete:
		h.deleteOrganisationMember(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

func (h *Handler) deleteOrganisationMember(w http.ResponseWriter, r *http.Request) {
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	if ok := h.requireOrganisationAdmin(w, r, orgID, userClaims.UserID); !ok {
		return
	}

	memberID := strings.TrimPrefix(r.URL.Path, "/v1/organisations/members/")
	if memberID == "" {
		BadRequest(w, r, "member ID is required")
		return
	}

	memberRole, err := h.DB.GetOrganisationMemberRole(r.Context(), memberID, orgID)
	if err != nil {
		BadRequest(w, r, "Member not found")
		return
	}

	if memberRole == "admin" {
		adminCount, err := h.DB.CountOrganisationAdmins(r.Context(), orgID)
		if err != nil {
			InternalError(w, r, err)
			return
		}
		if adminCount <= 1 {
			Forbidden(w, r, "Organisation must have at least one admin")
			return
		}
	}

	if err := h.DB.RemoveOrganisationMember(r.Context(), memberID, orgID); err != nil {
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"member_id": memberID,
	}, "Organisation member removed successfully")
}

func (h *Handler) updateOrganisationMemberRole(w http.ResponseWriter, r *http.Request) {
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	if ok := h.requireOrganisationAdmin(w, r, orgID, userClaims.UserID); !ok {
		return
	}

	memberID := strings.TrimPrefix(r.URL.Path, "/v1/organisations/members/")
	if memberID == "" {
		BadRequest(w, r, "member ID is required")
		return
	}
	if memberID == userClaims.UserID {
		BadRequest(w, r, "You cannot change your own role")
		return
	}

	var req UpdateOrganisationMemberRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	role := strings.TrimSpace(strings.ToLower(req.Role))
	if role != "admin" && role != "member" {
		BadRequest(w, r, "role must be admin or member")
		return
	}

	memberRole, err := h.DB.GetOrganisationMemberRole(r.Context(), memberID, orgID)
	if err != nil {
		BadRequest(w, r, "Member not found")
		return
	}

	if memberRole == "admin" && role != "admin" {
		adminCount, err := h.DB.CountOrganisationAdmins(r.Context(), orgID)
		if err != nil {
			InternalError(w, r, err)
			return
		}
		if adminCount <= 1 {
			Forbidden(w, r, "Organisation must have at least one admin")
			return
		}
	}

	if err := h.DB.UpdateOrganisationMemberRole(r.Context(), memberID, orgID, role); err != nil {
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"member_id": memberID,
		"role":      role,
	}, "Organisation member role updated successfully")
}

// OrganisationInvitesHandler handles GET/POST /v1/organisations/invites
func (h *Handler) OrganisationInvitesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listOrganisationInvites(w, r)
	case http.MethodPost:
		h.createOrganisationInvite(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

// OrganisationInvitePreviewHandler handles GET /v1/organisations/invites/preview?token=...
// It is intentionally public so invite recipients can see who invited them before authentication.
func (h *Handler) OrganisationInvitePreviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		BadRequest(w, r, "token is required")
		return
	}

	invite, err := h.DB.GetOrganisationInviteByToken(r.Context(), token)
	if err != nil {
		NotFound(w, r, "Invite not found")
		return
	}

	if invite.AcceptedAt != nil || invite.RevokedAt != nil || time.Now().After(invite.ExpiresAt) {
		BadRequest(w, r, "Invite is no longer valid")
		return
	}

	org, err := h.DB.GetOrganisation(invite.OrganisationID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	inviterLabel := "a team member"
	if invite.CreatedBy != "" {
		if inviter, inviterErr := h.DB.GetUser(invite.CreatedBy); inviterErr == nil {
			if inviter.FullName != nil && strings.TrimSpace(*inviter.FullName) != "" {
				inviterLabel = strings.TrimSpace(*inviter.FullName)
			} else if derivedFull := composeFullName(inviter.FirstName, inviter.LastName); derivedFull != nil {
				inviterLabel = strings.TrimSpace(*derivedFull)
			} else if strings.TrimSpace(inviter.Email) != "" {
				inviterLabel = strings.TrimSpace(inviter.Email)
			}
		}
	}

	WriteSuccess(w, r, map[string]any{
		"invite": map[string]any{
			"email":             invite.Email,
			"role":              invite.Role,
			"organisation_id":   invite.OrganisationID,
			"organisation_name": org.Name,
			"inviter_name":      inviterLabel,
			"expires_at":        invite.ExpiresAt.Format(time.RFC3339),
		},
	}, "Invite preview retrieved successfully")
}

// OrganisationInviteHandler handles DELETE /v1/organisations/invites/:id
func (h *Handler) OrganisationInviteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		MethodNotAllowed(w, r)
		return
	}

	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	if ok := h.requireOrganisationAdmin(w, r, orgID, userClaims.UserID); !ok {
		return
	}

	inviteID := strings.TrimPrefix(r.URL.Path, "/v1/organisations/invites/")
	if inviteID == "" {
		BadRequest(w, r, "invite ID is required")
		return
	}

	if err := h.DB.RevokeOrganisationInvite(r.Context(), inviteID, orgID); err != nil {
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"invite_id": inviteID,
	}, "Invite revoked successfully")
}

// OrganisationInviteAcceptHandler handles POST /v1/organisations/invites/accept
func (h *Handler) OrganisationInviteAcceptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	if req.Token == "" {
		BadRequest(w, r, "token is required")
		return
	}

	// Ensure user exists in database
	_, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	acceptedInvite, err := h.DB.AcceptOrganisationInvite(r.Context(), req.Token, userClaims.UserID)
	if err != nil {
		BadRequest(w, r, err.Error())
		return
	}

	logger := loggerWithRequest(r)
	activeOrganisationSet := false
	activeOrganisationAttempts := 1
	activeOrganisationError := ""
	if err := h.DB.SetActiveOrganisation(userClaims.UserID, acceptedInvite.OrganisationID); err != nil {
		activeOrganisationError = err.Error()
		logger.Warn("Initial set active organisation failed after invite acceptance",
			"error", err,
			"attempt", 1,
			"user_id", userClaims.UserID,
			"organisation_id", acceptedInvite.OrganisationID,
		)

		userID := userClaims.UserID
		orgID := acceptedInvite.OrganisationID
		go func() {
			retryBackoffs := []time.Duration{
				100 * time.Millisecond,
				300 * time.Millisecond,
				700 * time.Millisecond,
			}
			for retryAttempt, backoff := range retryBackoffs {
				time.Sleep(backoff)
				if retryErr := h.DB.SetActiveOrganisation(userID, orgID); retryErr != nil {
					logger.Warn("Background retry failed setting active organisation after invite acceptance",
						"error", retryErr,
						"attempt", retryAttempt+2,
						"max_attempts", len(retryBackoffs)+1,
						"user_id", userID,
						"organisation_id", orgID,
					)
					continue
				}

				logger.Info("Background retry set active organisation after invite acceptance",
					"attempt", retryAttempt+2,
					"user_id", userID,
					"organisation_id", orgID,
				)
				return
			}

			logger.Warn("All attempts failed setting active organisation after invite acceptance",
				"user_id", userID,
				"organisation_id", orgID,
				"attempts", len(retryBackoffs)+1,
			)
		}()
	} else {
		activeOrganisationSet = true
	}

	responseData := map[string]any{
		"organisation_id":              acceptedInvite.OrganisationID,
		"role":                         acceptedInvite.Role,
		"active_organisation_set":      activeOrganisationSet,
		"active_organisation_attempts": activeOrganisationAttempts,
	}
	if !activeOrganisationSet && activeOrganisationError != "" {
		responseData["active_organisation_error"] = activeOrganisationError
	}

	WriteSuccess(w, r, responseData, "Invite accepted successfully")
}

// OrganisationPlanHandler handles PUT /v1/organisations/plan
func (h *Handler) OrganisationPlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		MethodNotAllowed(w, r)
		return
	}

	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	if ok := h.requireOrganisationAdmin(w, r, orgID, userClaims.UserID); !ok {
		return
	}

	var req struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	if req.PlanID == "" {
		BadRequest(w, r, "plan_id is required")
		return
	}

	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, req.PlanID); err != nil {
		BadRequest(w, r, err.Error())
		return
	}

	WriteSuccess(w, r, map[string]any{
		"plan_id": req.PlanID,
	}, "Organisation plan updated successfully")
}

// UsageHistoryHandler handles GET /v1/usage/history
func (h *Handler) UsageHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	queryDays := r.URL.Query().Get("days")
	days := 30
	if queryDays != "" {
		if parsed, err := strconv.Atoi(queryDays); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	startDate := today.AddDate(0, 0, -(days - 1))

	entries, err := h.DB.ListDailyUsage(r.Context(), orgID, startDate, today)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	response := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		response = append(response, map[string]any{
			"usage_date":      entry.UsageDate.Format("2006-01-02"),
			"pages_processed": entry.PagesProcessed,
			"jobs_created":    entry.JobsCreated,
		})
	}

	WriteSuccess(w, r, map[string]any{
		"days":  days,
		"usage": response,
	}, "Usage history retrieved successfully")
}

type organisationInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (h *Handler) listOrganisationInvites(w http.ResponseWriter, r *http.Request) {
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	if ok := h.requireOrganisationAdmin(w, r, orgID, userClaims.UserID); !ok {
		return
	}

	invites, err := h.DB.ListOrganisationInvites(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	responseInvites := make([]map[string]any, 0, len(invites))
	for _, invite := range invites {
		inviteLink := buildInviteWelcomeURL(invite.Token)

		responseInvites = append(responseInvites, map[string]any{
			"id":          invite.ID,
			"email":       invite.Email,
			"role":        invite.Role,
			"invite_link": inviteLink,
			"created_at":  invite.CreatedAt.Format(time.RFC3339),
			"expires_at":  invite.ExpiresAt.Format(time.RFC3339),
		})
	}

	WriteSuccess(w, r, map[string]any{
		"invites": responseInvites,
	}, "Organisation invites retrieved successfully")
}

func (h *Handler) createOrganisationInvite(w http.ResponseWriter, r *http.Request) {
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	if ok := h.requireOrganisationAdmin(w, r, orgID, userClaims.UserID); !ok {
		return
	}

	org, err := h.DB.GetOrganisation(orgID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	var req organisationInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	if email == "" {
		BadRequest(w, r, "Valid email is required")
		return
	}
	parsedEmail, err := mail.ParseAddress(email)
	if err != nil {
		BadRequest(w, r, "Valid email is required")
		return
	}
	if parsedEmail != nil && parsedEmail.Address != "" {
		email = parsedEmail.Address
	}

	role := strings.TrimSpace(strings.ToLower(req.Role))
	if role == "" {
		role = "member"
	}
	if role != "admin" && role != "member" {
		BadRequest(w, r, "Role must be admin or member")
		return
	}

	isMember, err := h.DB.IsOrganisationMemberEmail(r.Context(), orgID, email)
	if err != nil {
		InternalError(w, r, err)
		return
	}
	if isMember {
		BadRequest(w, r, "User is already a member of this organisation")
		return
	}

	inviteToken := uuid.NewString()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	invite, err := h.DB.CreateOrganisationInvite(r.Context(), &db.OrganisationInvite{
		OrganisationID: orgID,
		Email:          email,
		Role:           role,
		Token:          inviteToken,
		CreatedBy:      userClaims.UserID,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		if strings.Contains(err.Error(), "organisation_invites_unique_pending") {
			BadRequest(w, r, "Invite already pending for this email")
			return
		}
		InternalError(w, r, err)
		return
	}

	redirectURL := buildInviteWelcomeURL(inviteToken)

	emailDelivery := "sent"
	responseMsg := "Invite sent successfully"

	inviterFirstName, inviterLastName, inviterFullName := nameFieldsFromClaims(userClaims)
	inviterName := ""
	if inviterFullName != nil {
		inviterName = strings.TrimSpace(*inviterFullName)
	} else if derivedFull := composeFullName(inviterFirstName, inviterLastName); derivedFull != nil {
		inviterName = strings.TrimSpace(*derivedFull)
	}
	if inviterName == "" {
		inviterName = userClaims.Email
	}

	meta := util.ExtractRequestMeta(r)

	// Send invite email via Loops. The invitee signs up or logs in via
	// the normal auth flow, then accepts the invite using the token.
	loopsErr := h.sendInviteViaLoops(r.Context(), email, map[string]any{
		"InviterName":      util.SanitiseForJSON(inviterName),
		"OrganisationName": util.SanitiseForJSON(org.Name),
		"Device":           util.SanitiseForJSON(meta.Device),
		"Location":         util.SanitiseForJSON(meta.Location),
		"IP":               util.SanitiseForJSON(meta.IP),
		"Timestamp":        util.SanitiseForJSON(meta.FormattedTimestamp()),
		"SiteURL":          getAppURL(),
		"ConfirmationURL":  redirectURL,
		"Token":            inviteToken,
	})
	if loopsErr != nil {
		logger := loggerWithRequest(r)
		if errors.Is(loopsErr, errLoopsNotConfigured) {
			emailDelivery = "skipped"
			responseMsg = "Invite created but email delivery is not configured in this environment"
		} else {
			logger.Error("Failed to send invite via Loops", "error", loopsErr)
			emailDelivery = "failed"
			responseMsg = "Invite created but email delivery failed — the user can log in and accept manually"
		}
	}

	WriteCreated(w, r, map[string]any{
		"invite": map[string]any{
			"id":             invite.ID,
			"email":          invite.Email,
			"role":           invite.Role,
			"email_delivery": emailDelivery,
			"created_at":     invite.CreatedAt.Format(time.RFC3339),
			"expires_at":     invite.ExpiresAt.Format(time.RFC3339),
		},
	}, responseMsg)
}

func (h *Handler) requireOrganisationAdmin(w http.ResponseWriter, r *http.Request, organisationID, userID string) bool {
	role, err := h.DB.GetOrganisationMemberRole(r.Context(), userID, organisationID)
	if err != nil {
		Forbidden(w, r, "Not a member of this organisation")
		return false
	}
	if role != "admin" {
		Forbidden(w, r, "Organisation administrator access required")
		return false
	}
	return true
}

// loopsInviteTemplateID is the Loops transactional template for organisation invites.
var loopsInviteTemplateID = func() string {
	if v := strings.TrimSpace(os.Getenv("LOOPS_INVITE_TEMPLATE_ID")); v != "" {
		return v
	}
	return "cmlbixdob0d3v0i34iy1nd6ad"
}()

var errLoopsNotConfigured = errors.New("loops client not configured")

// sendInviteViaLoops sends the invite email through the Loops transactional API.
// Returns errLoopsNotConfigured if the Loops client is not configured.
func (h *Handler) sendInviteViaLoops(ctx context.Context, email string, vars map[string]any) error {
	if h.Loops == nil {
		apiLog.Warn("Loops client not configured; skipping invite email")
		return errLoopsNotConfigured
	}

	return h.Loops.SendTransactional(ctx, &loops.TransactionalRequest{
		Email:           email,
		TransactionalID: loopsInviteTemplateID,
		DataVariables:   vars,
	})
}
