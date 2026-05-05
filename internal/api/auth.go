package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	emailverifier "github.com/AfterShip/email-verifier"
	"github.com/good-native/hover/internal/auth"
)

var (
	verifier = emailverifier.NewVerifier()
)

// AuthRegisterRequest represents a user registration request
type AuthRegisterRequest struct {
	UserID    string  `json:"user_id"`
	Email     string  `json:"email"`
	FirstName *string `json:"first_name,omitempty"`
	LastName  *string `json:"last_name,omitempty"`
	FullName  *string `json:"full_name,omitempty"`
	OrgName   *string `json:"org_name,omitempty"`
}

// AuthSessionRequest represents a session validation request
type AuthSessionRequest struct {
	Token string `json:"token"`
}

// UserResponse represents a user in API responses
type UserResponse struct {
	ID             string  `json:"id"`
	Email          string  `json:"email"`
	FirstName      *string `json:"first_name"`
	LastName       *string `json:"last_name"`
	FullName       *string `json:"full_name"`
	OrganisationID *string `json:"organisation_id"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// OrganisationResponse represents an organisation in API responses
type OrganisationResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AuthRegister handles POST /v1/auth/register
func (h *Handler) AuthRegister(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	var req AuthRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	if req.UserID == "" || req.Email == "" {
		BadRequest(w, r, "user_id and email are required")
		return
	}

	firstName, err := normaliseNamePart(req.FirstName, 80)
	if err != nil {
		BadRequest(w, r, "first_name must be 80 characters or fewer")
		return
	}
	lastName, err := normaliseNamePart(req.LastName, 80)
	if err != nil {
		BadRequest(w, r, "last_name must be 80 characters or fewer")
		return
	}
	fullName, err := normaliseNamePart(req.FullName, 120)
	if err != nil {
		BadRequest(w, r, "full_name must be 120 characters or fewer")
		return
	}

	firstName, lastName, fullName = fillMissingNameParts(firstName, lastName, fullName)

	var orgName string

	// 1. Org name if explicitly provided
	if req.OrgName != nil && *req.OrgName != "" {
		orgName = *req.OrgName
	}

	// 2. Domain name if not generic (and org name not already set)
	if orgName == "" {
		result, err := verifier.Verify(req.Email)
		if err != nil {
			logger.Warn("Email verifier failed", "error", err)
		} else if !result.Free {
			// Not a free provider, so use the domain name
			if emailParts := strings.Split(req.Email, "@"); len(emailParts) == 2 {
				domain := emailParts[1]
				domainName := strings.Split(domain, ".")[0]
				if len(domainName) > 0 {
					// Capitalise first letter of domain name
					orgName = strings.ToUpper(string(domainName[0])) + domainName[1:]
				}
			}
		}
	}

	// 3. Person's full name as fallback
	if orgName == "" && fullName != nil && *fullName != "" {
		orgName = *fullName
	}

	// 4. Final default if nothing else worked
	if orgName == "" {
		orgName = "Personal Organisation"
	}

	// Create user with organisation automatically
	user, org, err := h.DB.CreateUser(req.UserID, req.Email, firstName, lastName, fullName, orgName)
	if err != nil {
		logger.Error("Failed to create user with organisation", "error", err)
		InternalError(w, r, err)
		return
	}

	userResp := UserResponse{
		ID:             user.ID,
		Email:          user.Email,
		FirstName:      user.FirstName,
		LastName:       user.LastName,
		FullName:       user.FullName,
		OrganisationID: user.OrganisationID,
		CreatedAt:      user.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:      user.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	orgResp := OrganisationResponse{
		ID:        org.ID,
		Name:      org.Name,
		CreatedAt: org.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: org.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	WriteCreated(w, r, map[string]any{
		"user":         userResp,
		"organisation": orgResp,
	}, "User registered successfully")
}

// AuthSession handles POST /v1/auth/session
func (h *Handler) AuthSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	var req AuthSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	if req.Token == "" {
		BadRequest(w, r, "token is required")
		return
	}

	sessionInfo := auth.ValidateSession(req.Token)
	WriteSuccess(w, r, sessionInfo, "Session validated")
}

type AuthProfileUpdateRequest struct {
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	FullName  *string `json:"full_name"`
}

// AuthProfile handles GET/PATCH /v1/auth/profile
func (h *Handler) AuthProfile(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getAuthProfile(w, r)
	case http.MethodPatch:
		h.updateAuthProfile(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

func (h *Handler) getAuthProfile(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	claimsFirstName, claimsLastName, claimsFullName := nameFieldsFromClaims(userClaims)

	// Auto-create user if they don't exist
	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, claimsFullName)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err)
		InternalError(w, r, err)
		return
	}

	firstName := user.FirstName
	lastName := user.LastName
	fullName := user.FullName

	if isBlankName(firstName) {
		firstName = claimsFirstName
	}
	if isBlankName(lastName) {
		lastName = claimsLastName
	}
	if isBlankName(fullName) {
		fullName = claimsFullName
	}

	firstName, lastName, fullName = fillMissingNameParts(firstName, lastName, fullName)

	shouldSyncNames := !sameNameValue(user.FirstName, firstName) ||
		!sameNameValue(user.LastName, lastName) ||
		!sameNameValue(user.FullName, fullName)
	if shouldSyncNames {
		if err := h.DB.UpdateUserNames(userClaims.UserID, firstName, lastName, fullName); err != nil {
			logger.Warn("Failed to sync user names from claims", "error", err, "user_id", userClaims.UserID)
		} else {
			user.FirstName = firstName
			user.LastName = lastName
			user.FullName = fullName
		}
	}

	userResp := UserResponse{
		ID:             user.ID,
		Email:          user.Email,
		FirstName:      user.FirstName,
		LastName:       user.LastName,
		FullName:       user.FullName,
		OrganisationID: user.OrganisationID,
		CreatedAt:      user.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:      user.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	response := map[string]any{
		"user":         userResp,
		"auth_methods": authMethodsFromClaims(userClaims),
	}

	// Get organisation if user has one
	if user.OrganisationID != nil {
		org, err := h.DB.GetOrganisation(*user.OrganisationID)
		if err != nil {
			logger.Warn("Failed to get organisation", "error", err, "organisation_id", *user.OrganisationID)
		} else {
			orgResp := OrganisationResponse{
				ID:        org.ID,
				Name:      org.Name,
				CreatedAt: org.CreatedAt.Format("2006-01-02T15:04:05Z"),
				UpdatedAt: org.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}
			response["organisation"] = orgResp
		}
	}

	WriteSuccess(w, r, response, "Profile retrieved successfully")
}

func (h *Handler) updateAuthProfile(w http.ResponseWriter, r *http.Request) {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	var req AuthProfileUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	if req.FirstName == nil && req.LastName == nil && req.FullName == nil {
		BadRequest(w, r, "first_name, last_name, or full_name is required")
		return
	}

	firstName, err := normaliseNamePart(req.FirstName, 80)
	if err != nil {
		BadRequest(w, r, "first_name must be 80 characters or fewer")
		return
	}
	lastName, err := normaliseNamePart(req.LastName, 80)
	if err != nil {
		BadRequest(w, r, "last_name must be 80 characters or fewer")
		return
	}
	fullName, err := normaliseNamePart(req.FullName, 120)
	if err != nil {
		BadRequest(w, r, "full_name must be 120 characters or fewer")
		return
	}

	firstName, lastName, fullName = fillMissingNameParts(firstName, lastName, fullName)

	if _, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, fullName); err != nil {
		InternalError(w, r, err)
		return
	}

	if err := h.DB.UpdateUserNames(userClaims.UserID, firstName, lastName, fullName); err != nil {
		InternalError(w, r, err)
		return
	}

	user, err := h.DB.GetUser(userClaims.UserID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	userResp := UserResponse{
		ID:             user.ID,
		Email:          user.Email,
		FirstName:      user.FirstName,
		LastName:       user.LastName,
		FullName:       user.FullName,
		OrganisationID: user.OrganisationID,
		CreatedAt:      user.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:      user.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	WriteSuccess(w, r, map[string]any{
		"user":         userResp,
		"auth_methods": authMethodsFromClaims(userClaims),
	}, "Profile updated successfully")
}

func nameFieldsFromClaims(userClaims *auth.UserClaims) (*string, *string, *string) {
	if userClaims == nil {
		return nil, nil, nil
	}

	readName := func(keys ...string) *string {
		for _, key := range keys {
			value, ok := userClaims.UserMetadata[key]
			if !ok {
				continue
			}
			name, ok := value.(string)
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			if name != "" {
				return &name
			}
		}
		return nil
	}

	firstName, err := normaliseNamePart(readName("given_name", "first_name"), 80)
	if err != nil {
		apiLog.Debug("Ignoring oversized first name claim", "error", err)
	}
	lastName, err := normaliseNamePart(readName("family_name", "last_name"), 80)
	if err != nil {
		apiLog.Debug("Ignoring oversized last name claim", "error", err)
	}
	fullName, err := normaliseNamePart(readName("full_name", "name"), 120)
	if err != nil {
		apiLog.Debug("Ignoring oversized full name claim", "error", err)
	}

	firstName, lastName, fullName = fillMissingNameParts(firstName, lastName, fullName)

	return firstName, lastName, fullName
}

func normaliseNamePart(value *string, maxLen int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > maxLen {
		return nil, fmt.Errorf("name part exceeds %d characters", maxLen)
	}
	return &trimmed, nil
}

func composeFullName(firstName, lastName *string) *string {
	parts := make([]string, 0, 2)
	if firstName != nil {
		parts = append(parts, strings.TrimSpace(*firstName))
	}
	if lastName != nil {
		parts = append(parts, strings.TrimSpace(*lastName))
	}
	joined := strings.TrimSpace(strings.Join(parts, " "))
	if joined == "" {
		return nil
	}
	return &joined
}

// fillMissingNameParts composes fullName from name parts when missing, then
// derives missing split parts from fullName for consistency.
func fillMissingNameParts(firstName, lastName, fullName *string) (*string, *string, *string) {
	if fullName == nil {
		fullName = composeFullName(firstName, lastName)
	}
	derivedFirst, derivedLast := deriveNameParts(fullName)
	if firstName == nil {
		firstName = derivedFirst
	}
	if lastName == nil {
		lastName = derivedLast
	}
	return firstName, lastName, fullName
}

// deriveNameParts intentionally uses a simple split strategy:
// first name is the first whitespace-delimited token, and last name is the
// remaining tokens. This can misclassify titles/prefixes (for example,
// "Dr. John Smith" -> first="Dr.", last="John Smith") and names with multi-word
// given names. If name accuracy becomes critical, use a more sophisticated
// parser.
func deriveNameParts(fullName *string) (*string, *string) {
	if fullName == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*fullName)
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return nil, nil
	}
	first := parts[0]
	if len(parts) == 1 {
		return &first, nil
	}
	last := strings.Join(parts[1:], " ")
	return &first, &last
}

func isBlankName(value *string) bool {
	return value == nil || strings.TrimSpace(*value) == ""
}

func sameNameValue(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return strings.TrimSpace(*a) == strings.TrimSpace(*b)
	}
}

func authMethodsFromClaims(userClaims *auth.UserClaims) []string {
	if userClaims == nil {
		return []string{"email"}
	}

	var methods []string
	if providersRaw, ok := userClaims.AppMetadata["providers"]; ok {
		if providers, ok := providersRaw.([]any); ok {
			for _, providerRaw := range providers {
				provider, ok := providerRaw.(string)
				if !ok {
					continue
				}
				provider = strings.TrimSpace(strings.ToLower(provider))
				if provider != "" && !slices.Contains(methods, provider) {
					methods = append(methods, provider)
				}
			}
		}
	}

	if providerRaw, ok := userClaims.AppMetadata["provider"]; ok {
		if provider, ok := providerRaw.(string); ok {
			provider = strings.TrimSpace(strings.ToLower(provider))
			if provider != "" && !slices.Contains(methods, provider) {
				methods = append(methods, provider)
			}
		}
	}

	if len(methods) == 0 {
		methods = append(methods, "email")
	}

	return methods
}
