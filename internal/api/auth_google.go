package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/good-native/hover/internal/auth"
	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/logging"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// extractGoogleAccountIDFromPath extracts Google account ID from path like "accounts/accounts/123456/properties"
// Returns empty string if path doesn't match the expected pattern
func extractGoogleAccountIDFromPath(path string) string {
	if !strings.HasPrefix(path, "accounts/") || !strings.HasSuffix(path, "/properties") {
		return ""
	}
	// Path format: accounts/accounts/123456/properties -> accounts/123456
	trimmed := strings.TrimPrefix(path, "accounts/")
	trimmed = strings.TrimSuffix(trimmed, "/properties")
	if trimmed == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(trimmed); err == nil && decoded != "" {
		return decoded
	}
	return trimmed
}

// Pending OAuth sessions - stores properties and tokens temporarily after OAuth callback.
// Key is session ID, value is PendingGASession.
//
// LIMITATION: This in-memory map will not work correctly in multi-instance deployments
// because OAuth callbacks may land on a different process than the one that initiated
// the OAuth flow. For production multi-instance deployments, this should be replaced
// with a shared session store (e.g., Redis cache or DB-backed sessions) or use
// signed/encrypted cookie sessions for portability.
var (
	pendingGASessions   = make(map[string]*PendingGASession)
	pendingGASessionsMu sync.RWMutex
)

// PendingGASession stores OAuth data temporarily until user completes account/property selection
type PendingGASession struct {
	Accounts     []GA4Account  // Accounts fetched during OAuth
	Properties   []GA4Property // Properties fetched when account selected (optional, for backwards compat)
	RefreshToken string
	AccessToken  string
	State        string
	UserID       string
	Email        string
	OrgID        string // Organisation ID from OAuth state
	ExpiresAt    time.Time
}

// storePendingGASession stores a pending session and returns the session ID
func storePendingGASession(session *PendingGASession) string {
	sessionID := uuid.New().String()
	session.ExpiresAt = time.Now().Add(10 * time.Minute)

	pendingGASessionsMu.Lock()
	pendingGASessions[sessionID] = session
	pendingGASessionsMu.Unlock()

	// Cleanup old sessions in background
	go cleanupExpiredGASessions()

	return sessionID
}

// getPendingGASession retrieves and removes a pending session
func getPendingGASession(sessionID string) *PendingGASession {
	pendingGASessionsMu.Lock()
	defer pendingGASessionsMu.Unlock()

	session, ok := pendingGASessions[sessionID]
	if !ok || time.Now().After(session.ExpiresAt) {
		delete(pendingGASessions, sessionID)
		return nil
	}

	// Don't delete yet - user might refresh the page
	return session
}

func cleanupExpiredGASessions() {
	pendingGASessionsMu.Lock()
	defer pendingGASessionsMu.Unlock()

	now := time.Now()
	for id, session := range pendingGASessions {
		if now.After(session.ExpiresAt) {
			delete(pendingGASessions, id)
		}
	}
}

func getGoogleRedirectURI() string {
	return getAppURL() + "/v1/integrations/google/callback"
}

// GoogleTokenResponse represents the response from Google's token endpoint
type GoogleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// GA4Property represents a Google Analytics 4 property
type GA4Property struct {
	PropertyID   string `json:"property_id"`   // e.g., "123456789"
	DisplayName  string `json:"display_name"`  // e.g., "My Website"
	PropertyType string `json:"property_type"` // e.g., "PROPERTY_TYPE_ORDINARY"
}

// GA4Account represents a Google Analytics account
type GA4Account struct {
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
}

// GoogleUserInfo contains user info from Google
type GoogleUserInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// InitiateGoogleOAuth starts the OAuth flow
func (h *Handler) InitiateGoogleOAuth(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	if h.GoogleClientID == "" {
		logger.Error("GOOGLE_CLIENT_ID not configured")
		InternalError(w, r, fmt.Errorf("google integration not configured"))
		return
	}

	if getOAuthStateSecret() == "" {
		logger.Error("SUPABASE_JWT_SECRET not configured for OAuth state signing")
		InternalError(w, r, fmt.Errorf("google integration not configured"))
		return
	}

	// Sign state with JWT Secret
	state, err := h.generateOAuthState(userClaims.UserID, orgID)
	if err != nil {
		logger.Error("Failed to generate OAuth state", "error", err)
		InternalError(w, r, err)
		return
	}

	// Scopes needed:
	// - analytics.readonly: Read GA4 data
	// - userinfo.email: Get user's email for display
	scopes := "https://www.googleapis.com/auth/analytics.readonly https://www.googleapis.com/auth/userinfo.email"

	// Build Google OAuth URL
	authURL := fmt.Sprintf(
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&access_type=offline&prompt=consent&state=%s",
		url.QueryEscape(h.GoogleClientID),
		url.QueryEscape(getGoogleRedirectURI()),
		url.QueryEscape(scopes),
		url.QueryEscape(state),
	)

	WriteSuccess(w, r, map[string]string{"auth_url": authURL}, "Redirect to this URL to connect Google Analytics")
}

// HandleGoogleOAuthCallback processes the OAuth callback from Google
// After successful auth, it fetches the user's GA4 properties and returns them
func (h *Handler) HandleGoogleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	errParam := r.URL.Query().Get("error")

	if errParam != "" {
		logger.Warn("Google OAuth denied", "error", errParam)
		h.redirectToSettingsWithError(w, r, "Google", "Google connection was cancelled", "analytics", "google-analytics")
		return
	}

	if code == "" || stateParam == "" {
		BadRequest(w, r, "Missing code or state parameter")
		return
	}

	// Validate state
	state, err := h.validateOAuthState(stateParam)
	if err != nil {
		logger.Warn("Invalid OAuth state", "error", err)
		h.redirectToSettingsWithError(w, r, "Google", "Invalid or expired state", "analytics", "google-analytics")
		return
	}

	// Exchange code for access token
	tokenResp, err := h.exchangeGoogleCode(code)
	if err != nil {
		logger.Error("Failed to exchange Google OAuth code", "error", err)
		h.redirectToSettingsWithError(w, r, "Google", "Failed to connect to Google", "analytics", "google-analytics")
		return
	}

	// Store tokens temporarily in session/URL params for property selection
	// For now, we'll redirect to a property picker page with the tokens encoded
	// In production, you'd want to store these in a temporary session

	// Fetch user info
	userInfo, err := h.fetchGoogleUserInfo(r.Context(), tokenResp.AccessToken)
	if err != nil {
		logger.Warn("Failed to fetch Google user info", "error", err)
	}

	// Fetch GA4 accounts (fast - single API call)
	accounts, err := h.fetchGA4Accounts(r.Context(), tokenResp.AccessToken)
	if err != nil {
		logger.Error("Failed to fetch GA4 accounts", "error", err)
		h.redirectToSettingsWithError(w, r, "Google", "Failed to fetch Google Analytics accounts. Ensure GA4 is set up.", "analytics", "google-analytics")
		return
	}

	if len(accounts) == 0 {
		h.redirectToSettingsWithError(w, r, "Google", "No Google Analytics accounts found. Please set up GA4 first.", "analytics", "google-analytics")
		return
	}

	// SYNC: Store accounts to DB for persistent display
	now := time.Now().UTC()
	for _, acc := range accounts {
		dbAccount := &db.GoogleAnalyticsAccount{
			ID:                uuid.New().String(),
			OrganisationID:    state.OrgID,
			GoogleAccountID:   acc.AccountID,
			GoogleAccountName: acc.DisplayName,
			InstallingUserID:  state.UserID,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if userInfo != nil {
			dbAccount.GoogleUserID = userInfo.ID
			dbAccount.GoogleEmail = userInfo.Email
		}

		if err := h.DB.UpsertGA4Account(r.Context(), dbAccount); err != nil {
			logger.Warn("Failed to upsert GA4 account to DB", "error", err, "account_id", acc.AccountID, "next_action", "retry_upsert_or_check_db_connectivity")
			// Continue anyway - the pending session flow will still work
			continue
		}

		// Store token against each account for future refresh operations
		if tokenResp.RefreshToken != "" {
			if err := h.DB.StoreGA4AccountToken(r.Context(), dbAccount.ID, tokenResp.RefreshToken); err != nil {
				logger.Warn("Failed to store GA4 account token", "error", err, "account_id", acc.AccountID, "next_action", "retry_store_token_or_check_vault_permissions")
			}
		}
	}

	logger.Info("Synced GA4 accounts to database", "organisation_id", state.OrgID, "account_count", len(accounts))

	// Store session with accounts and tokens (for property selection flow)
	session := &PendingGASession{
		Accounts:     accounts,
		RefreshToken: tokenResp.RefreshToken,
		AccessToken:  tokenResp.AccessToken,
		State:        stateParam,
		OrgID:        state.OrgID,
	}
	if userInfo != nil {
		session.UserID = userInfo.ID
		session.Email = userInfo.Email
	}

	// If single account, auto-fetch properties for it
	if len(accounts) == 1 {
		properties, err := h.fetchPropertiesForAccount(r.Context(), logger, &http.Client{Timeout: 30 * time.Second}, tokenResp.AccessToken, accounts[0].AccountID)
		if err != nil {
			logger.Error("Failed to fetch properties for single account", "error", err)
			h.redirectToSettingsWithError(w, r, "Google", "Failed to fetch properties for account", "analytics", "google-analytics")
			return
		}
		session.Properties = properties
	}

	sessionID := storePendingGASession(session)

	// Do not log the raw session ID — it is a short-lived bearer token for the
	// pending GA session and would enable log-based session replay.
	logger.Info("Stored GA4 session", "account_count", len(accounts), "property_count", len(session.Properties))

	// Redirect with session ID
	params := url.Values{}
	params.Set("ga_session", sessionID)
	redirectURL := buildSettingsURL("analytics", params, "google-analytics")
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// SaveGoogleProperty saves the selected GA4 property
func (h *Handler) SaveGoogleProperty(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	// Parse request body
	var req struct {
		PropertyID   string `json:"property_id"`
		PropertyName string `json:"property_name"`
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
		GoogleEmail  string `json:"google_email"`
		GoogleUserID string `json:"google_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid request body")
		return
	}

	if req.PropertyID == "" || req.RefreshToken == "" {
		BadRequest(w, r, "Property ID and refresh token are required")
		return
	}

	// Create the connection
	now := time.Now().UTC()
	conn := &db.GoogleAnalyticsConnection{
		ID:               uuid.New().String(),
		OrganisationID:   orgID,
		GA4PropertyID:    req.PropertyID,
		GA4PropertyName:  req.PropertyName,
		GoogleUserID:     req.GoogleUserID,
		GoogleEmail:      req.GoogleEmail,
		InstallingUserID: userClaims.UserID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := h.DB.CreateGoogleConnection(r.Context(), conn); err != nil {
		logger.Error("Failed to save Google Analytics connection", "error", err)
		InternalError(w, r, err)
		return
	}

	// Store refresh token in Supabase Vault
	if err := h.DB.StoreGoogleToken(r.Context(), conn.ID, req.RefreshToken); err != nil {
		logger.Error("Failed to store refresh token in vault", "error", err)
		InternalError(w, r, err)
		return
	}

	logger.Info("Google Analytics connection established", "organisation_id", orgID, "ga4_property_id", req.PropertyID)

	WriteSuccess(w, r, map[string]string{
		"connection_id": conn.ID,
		"property_id":   req.PropertyID,
		"property_name": req.PropertyName,
	}, "Google Analytics connected successfully")
}

// SaveGoogleProperties saves all properties from a session, with specified ones marked as active
func (h *Handler) SaveGoogleProperties(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	// Parse request body
	var req struct {
		SessionID         string           `json:"session_id"`
		AccountID         string           `json:"account_id"`
		ActivePropertyIDs []string         `json:"active_property_ids"` // Which properties should be active
		PropertyDomainMap map[string][]int `json:"property_domain_map"` // property_id -> domain_ids
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid request body")
		return
	}

	if req.SessionID == "" {
		BadRequest(w, r, "Session ID is required")
		return
	}

	// Get session data
	session := getPendingGASession(req.SessionID)
	if session == nil {
		BadRequest(w, r, "Session expired or not found. Please reconnect to Google Analytics.")
		return
	}

	if len(session.Properties) == 0 {
		BadRequest(w, r, "No properties in session. Please select an account first.")
		return
	}

	// Build set of active property IDs for quick lookup
	activeSet := make(map[string]bool)
	for _, pid := range req.ActivePropertyIDs {
		activeSet[pid] = true
	}

	organisationDomains, err := h.DB.GetDomainsForOrganisation(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to fetch organisation domains", "error", err, "organisation_id", orgID)
		InternalError(w, r, err)
		return
	}

	allowedDomainIDs := make(map[int]struct{}, len(organisationDomains))
	for _, domain := range organisationDomains {
		allowedDomainIDs[domain.ID] = struct{}{}
	}

	for propertyID, domainIDs := range req.PropertyDomainMap {
		for _, id := range domainIDs {
			if _, ok := allowedDomainIDs[id]; !ok {
				BadRequest(w, r, fmt.Sprintf("Domain ID %d does not belong to organisation for property %s", id, propertyID))
				return
			}
		}
	}

	// Save all properties as connections
	now := time.Now().UTC()
	var savedCount int
	for _, prop := range session.Properties {
		status := "inactive"
		if activeSet[prop.PropertyID] {
			status = "active"
		}

		// Get domain IDs for this property (default to empty array if not provided)
		domainIDs := req.PropertyDomainMap[prop.PropertyID]
		for _, id := range domainIDs {
			if _, ok := allowedDomainIDs[id]; !ok {
				BadRequest(w, r, "Domain does not belong to organisation")
				return
			}
		}

		// Convert []int to pq.Int64Array
		domainIDsArray := make(pq.Int64Array, len(domainIDs))
		for i, id := range domainIDs {
			domainIDsArray[i] = int64(id)
		}

		conn := &db.GoogleAnalyticsConnection{
			ID:               uuid.New().String(),
			OrganisationID:   orgID,
			GA4PropertyID:    prop.PropertyID,
			GA4PropertyName:  prop.DisplayName,
			GoogleAccountID:  req.AccountID,
			GoogleUserID:     session.UserID,
			GoogleEmail:      session.Email,
			InstallingUserID: userClaims.UserID,
			Status:           status,
			DomainIDs:        domainIDsArray,
			CreatedAt:        now,
			UpdatedAt:        now,
		}

		if err := h.DB.CreateGoogleConnection(r.Context(), conn); err != nil {
			logger.Warn("Failed to save property connection", "error", err, "property_id", prop.PropertyID, "next_action", "retry_connection_create_or_check_db_connectivity")
			continue
		}

		// Store token only for active properties (saves vault space)
		if status == "active" && session.RefreshToken != "" {
			if err := h.DB.StoreGoogleToken(r.Context(), conn.ID, session.RefreshToken); err != nil {
				logger.Warn("Failed to store token in vault", "error", err, "connection_id", conn.ID)
			}
		}

		savedCount++
	}

	// Clean up the session after saving
	pendingGASessionsMu.Lock()
	delete(pendingGASessions, req.SessionID)
	pendingGASessionsMu.Unlock()

	logger.Info("Google Analytics properties saved", "organisation_id", orgID, "saved_count", savedCount, "active_count", len(req.ActivePropertyIDs))

	WriteSuccess(w, r, googleSavedPropertiesResponse{
		SavedCount:  savedCount,
		ActiveCount: len(req.ActivePropertyIDs),
	}, "Google Analytics properties saved successfully")
}

// UpdateGooglePropertyStatus updates the status of a Google Analytics connection
func (h *Handler) UpdateGooglePropertyStatus(w http.ResponseWriter, r *http.Request, connectionID string) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPatch {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	// Parse request body
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid request body")
		return
	}

	if req.Status != "active" && req.Status != "inactive" {
		BadRequest(w, r, "Status must be 'active' or 'inactive'")
		return
	}

	if err := h.DB.UpdateGoogleConnectionStatus(r.Context(), connectionID, orgID, req.Status); err != nil {
		if errors.Is(err, db.ErrGoogleConnectionNotFound) {
			NotFound(w, r, "Connection not found")
			return
		}
		logger.Error("Failed to update connection status", "error", err, "connection_id", connectionID)
		InternalError(w, r, err)
		return
	}

	logger.Info("Google Analytics connection status updated", "connection_id", connectionID, "status", req.Status)

	WriteSuccess(w, r, map[string]string{
		"connection_id": connectionID,
		"status":        req.Status,
	}, "Status updated successfully")
}

// UpdateGoogleConnection updates domain mappings for an existing connection
// PATCH /v1/integrations/google/{id}
func (h *Handler) UpdateGoogleConnection(w http.ResponseWriter, r *http.Request, connectionID string) {
	logger := loggerWithRequest(r)

	// Get authenticated user
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	// Parse request body
	type UpdateRequest struct {
		DomainIDs []int `json:"domain_ids"`
	}

	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid request body")
		return
	}

	// Get existing connection to verify ownership
	conn, err := h.DB.GetGoogleConnection(r.Context(), connectionID)
	if err != nil {
		logger.Error("Failed to get connection", "error", err, "connection_id", connectionID)
		if errors.Is(err, db.ErrGoogleConnectionNotFound) {
			NotFound(w, r, "Connection not found")
			return
		}
		InternalError(w, r, err)
		return
	}

	// Verify connection belongs to user's organisation
	if conn.OrganisationID != orgID {
		Forbidden(w, r, "Access denied")
		return
	}

	organisationDomains, err := h.DB.GetDomainsForOrganisation(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to fetch organisation domains", "error", err, "organisation_id", orgID)
		InternalError(w, r, err)
		return
	}

	allowedDomainIDs := make(map[int]struct{}, len(organisationDomains))
	for _, domain := range organisationDomains {
		allowedDomainIDs[domain.ID] = struct{}{}
	}

	for _, id := range req.DomainIDs {
		if _, ok := allowedDomainIDs[id]; !ok {
			Forbidden(w, r, "Domain does not belong to organisation")
			return
		}
	}

	// Update domain_ids
	if err := h.DB.UpdateConnectionDomains(r.Context(), connectionID, req.DomainIDs); err != nil {
		logger.Error("Failed to update connection domains", "error", err, "connection_id", connectionID)
		InternalError(w, r, err)
		return
	}

	logger.Info("Updated connection domain mappings", "connection_id", connectionID, "domain_ids", req.DomainIDs)

	// Return updated connection (sanitised to avoid exposing internal fields)
	updatedConn, err := h.DB.GetGoogleConnection(r.Context(), connectionID)
	if err != nil {
		InternalError(w, r, err)
		return
	}

	response := GoogleConnectionResponse{
		ID:              updatedConn.ID,
		GA4PropertyID:   updatedConn.GA4PropertyID,
		GA4PropertyName: updatedConn.GA4PropertyName,
		GoogleEmail:     updatedConn.GoogleEmail,
		Status:          updatedConn.Status,
		DomainIDs:       updatedConn.DomainIDs,
		CreatedAt:       updatedConn.CreatedAt.Format(time.RFC3339),
	}
	WriteSuccess(w, r, response, "Connection updated")
}

func (h *Handler) exchangeGoogleCode(code string) (*GoogleTokenResponse, error) {
	values := url.Values{}
	values.Set("client_id", h.GoogleClientID)
	values.Set("client_secret", h.GoogleClientSecret)
	values.Set("grant_type", "authorization_code")
	values.Set("code", code)
	values.Set("redirect_uri", getGoogleRedirectURI())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.PostForm("https://oauth2.googleapis.com/token", values)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google API returned status: %d", resp.StatusCode)
	}

	var tokenResp GoogleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

func (h *Handler) fetchGoogleUserInfo(ctx context.Context, accessToken string) (*GoogleUserInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call userinfo endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned status: %d", resp.StatusCode)
	}

	var userInfo GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo response: %w", err)
	}

	return &userInfo, nil
}

// maxPropertiesForURL removed - no longer needed with server-side sessions

// fetchGA4Accounts fetches all Google Analytics accounts for the authenticated user
// This is fast as it's a single API call
func (h *Handler) fetchGA4Accounts(ctx context.Context, accessToken string) ([]GA4Account, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://analyticsadmin.googleapis.com/v1beta/accounts", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create accounts request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("accounts endpoint returned status: %d", resp.StatusCode)
	}

	var accountsResp struct {
		Accounts []struct {
			Name        string `json:"name"` // e.g., "accounts/123456"
			DisplayName string `json:"displayName"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accountsResp); err != nil {
		return nil, fmt.Errorf("failed to decode accounts response: %w", err)
	}

	var accounts []GA4Account
	for _, acc := range accountsResp.Accounts {
		accounts = append(accounts, GA4Account{
			AccountID:   acc.Name, // Keep full format "accounts/123456"
			DisplayName: acc.DisplayName,
		})
	}

	return accounts, nil
}

func (h *Handler) fetchPropertiesForAccount(ctx context.Context, logger *logging.Logger, client *http.Client, accessToken, accountName string) ([]GA4Property, error) {
	// URL-encode the filter value (accountName contains slash like "accounts/123456")
	apiURL := fmt.Sprintf("https://analyticsadmin.googleapis.com/v1beta/properties?filter=parent:%s", url.QueryEscape(accountName))

	logger.Debug("Fetching GA4 properties from API", "account_id", accountName)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil) //nolint:gosec // G704: apiURL targets googleapis.com; user input only in query param
	if err != nil {
		return nil, fmt.Errorf("failed to create properties request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req) //nolint:gosec // G704: request targets googleapis.com
	if err != nil {
		return nil, fmt.Errorf("failed to list properties: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("properties endpoint returned status: %d", resp.StatusCode)
	}

	var propertiesResp struct {
		Properties []struct {
			Name         string `json:"name"` // e.g., "properties/123456789"
			DisplayName  string `json:"displayName"`
			PropertyType string `json:"propertyType"`
		} `json:"properties"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&propertiesResp); err != nil {
		return nil, fmt.Errorf("failed to decode properties response: %w", err)
	}

	var properties []GA4Property
	for _, p := range propertiesResp.Properties {
		// Extract property ID from name (e.g., "properties/123456789" -> "123456789")
		propertyID := strings.TrimPrefix(p.Name, "properties/")
		properties = append(properties, GA4Property{
			PropertyID:   propertyID,
			DisplayName:  p.DisplayName,
			PropertyType: p.PropertyType,
		})
	}

	logger.Debug("Fetched GA4 properties", "account_id", accountName, "property_count", len(properties))

	return properties, nil
}

// GoogleConnectionResponse represents a Google Analytics connection in API responses
type GoogleConnectionResponse struct {
	ID                string        `json:"id"`
	GA4PropertyID     string        `json:"ga4_property_id,omitempty"`
	GA4PropertyName   string        `json:"ga4_property_name,omitempty"`
	GoogleAccountName string        `json:"google_account_name,omitempty"`
	GoogleEmail       string        `json:"google_email,omitempty"`
	Status            string        `json:"status"`
	DomainIDs         pq.Int64Array `json:"domain_ids,omitempty"`
	CreatedAt         string        `json:"created_at"`
}

// GoogleConnectionsHandler handles requests to /v1/integrations/google
func (h *Handler) GoogleConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listGoogleConnections(w, r)
	case http.MethodPost:
		h.InitiateGoogleOAuth(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

// GoogleConnectionHandler handles requests to /v1/integrations/google/:id
func (h *Handler) GoogleConnectionHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/integrations/google/")
	if path == "" {
		BadRequest(w, r, "Connection ID is required")
		return
	}
	if h.handleGoogleSpecialPaths(w, r, path) {
		return
	}

	connectionID := strings.Split(path, "/")[0]
	if _, err := uuid.Parse(connectionID); err != nil {
		BadRequest(w, r, "Invalid connection ID format")
		return
	}

	// Check for /status suffix for PATCH requests
	pathParts := strings.Split(path, "/")
	if len(pathParts) >= 2 && pathParts[1] == "status" {
		if r.Method == http.MethodPatch {
			h.UpdateGooglePropertyStatus(w, r, connectionID)
			return
		}
		MethodNotAllowed(w, r)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		h.deleteGoogleConnection(w, r, connectionID)
	case http.MethodPatch:
		// Check request body to determine which PATCH operation
		// If body contains "domain_ids", update domains
		// If body contains "status", update status
		var bodyCheck map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&bodyCheck); err != nil {
			BadRequest(w, r, "Invalid request body")
			return
		}

		// Restore body for handler to read
		bodyBytes, err := json.Marshal(bodyCheck)
		if err != nil {
			InternalError(w, r, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		_, hasDomainIDs := bodyCheck["domain_ids"]
		_, hasStatus := bodyCheck["status"]
		if hasDomainIDs && hasStatus {
			BadRequest(w, r, "Specify either domain_ids or status, not both")
			return
		}
		if hasDomainIDs {
			h.UpdateGoogleConnection(w, r, connectionID)
		} else {
			h.UpdateGooglePropertyStatus(w, r, connectionID)
		}
	default:
		MethodNotAllowed(w, r)
	}
}

func (h *Handler) handleGoogleSpecialPaths(w http.ResponseWriter, r *http.Request, path string) bool {
	// Handle callback separately (no auth required)
	if path == "callback" {
		if r.Method == http.MethodGet {
			h.HandleGoogleOAuthCallback(w, r)
			return true
		}
		MethodNotAllowed(w, r)
		return true
	}

	// Handle save-property endpoint (single property - legacy)
	if path == "save-property" {
		if r.Method == http.MethodPost {
			h.SaveGoogleProperty(w, r)
			return true
		}
		MethodNotAllowed(w, r)
		return true
	}

	// Handle save-properties endpoint (bulk save all properties from session)
	if path == "save-properties" {
		if r.Method == http.MethodPost {
			h.SaveGoogleProperties(w, r)
			return true
		}
		MethodNotAllowed(w, r)
		return true
	}

	// Handle domains endpoint (get organisation domains for property mapping)
	if path == "domains" {
		if r.Method == http.MethodGet {
			h.getOrganisationDomains(w, r)
			return true
		}
		MethodNotAllowed(w, r)
		return true
	}

	// Handle accounts endpoint (persistent storage of GA accounts)
	if path == "accounts" {
		h.ListGA4Accounts(w, r)
		return true
	}

	// Handle accounts/refresh endpoint (sync from Google API)
	if path == "accounts/refresh" {
		h.RefreshGA4Accounts(w, r)
		return true
	}

	// Handle accounts/{googleAccountId}/save-properties endpoint (bulk save from stored account)
	if strings.HasPrefix(path, "accounts/") && strings.HasSuffix(path, "/save-properties") {
		accountPath := strings.TrimPrefix(path, "accounts/")
		accountID := strings.TrimSuffix(accountPath, "/save-properties")
		if decoded, err := url.PathUnescape(accountID); err == nil && decoded != "" {
			accountID = decoded
		}
		if accountID != "" {
			h.SaveGA4AccountProperties(w, r, accountID)
			return true
		}
	}

	// Handle accounts/{googleAccountId}/properties endpoint (fetch properties using stored token)
	if googleAccountID := extractGoogleAccountIDFromPath(path); googleAccountID != "" {
		h.GetAccountProperties(w, r, googleAccountID)
		return true
	}

	// Handle pending-session endpoint (get accounts/properties from server-side session)
	if strings.HasPrefix(path, "pending-session/") {
		h.handlePendingSession(w, r, path)
		return true
	}

	return false
}

func (h *Handler) handlePendingSession(w http.ResponseWriter, r *http.Request, path string) {
	sessionPath := strings.TrimPrefix(path, "pending-session/")
	if decodedPath, err := url.PathUnescape(sessionPath); err == nil {
		sessionPath = decodedPath
	}
	parts := strings.Split(sessionPath, "/")
	sessionID := parts[0]

	// Check if this is a request for a specific account's properties
	// Format: pending-session/{sessionID}/accounts/{accountID}/properties
	// Note: URL path segments are automatically decoded by the HTTP router,
	// so "accounts%2F123456" becomes ["accounts", "123456"] in the parts array
	isPropertiesRequest := false
	var accountID string

	if len(parts) >= 4 && parts[1] == "accounts" && parts[len(parts)-1] == "properties" {
		accountID = strings.Join(parts[2:len(parts)-1], "/")
		isPropertiesRequest = true
	}

	if isPropertiesRequest {
		if r.Method == http.MethodGet {
			h.fetchAccountProperties(w, r, sessionID, accountID)
			return
		}
		MethodNotAllowed(w, r)
		return
	}

	// Default: return session data
	if r.Method == http.MethodGet {
		h.getPendingSession(w, r, sessionID)
		return
	}
	MethodNotAllowed(w, r)
}

// getPendingSession returns the pending OAuth session data (accounts, properties, tokens)
func (h *Handler) getPendingSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	session := getPendingGASession(sessionID)
	if session == nil {
		BadRequest(w, r, "Session expired or not found. Please reconnect to Google Analytics.")
		return
	}

	// Note: Tokens are intentionally excluded from this response for security.
	// Server-side handlers will retrieve tokens from the session using sessionID.
	WriteSuccess(w, r, pendingGASessionResponse{
		Accounts:   session.Accounts,
		Properties: session.Properties,
		State:      session.State,
		UserID:     session.UserID,
		Email:      session.Email,
	}, "")
}

// fetchAccountProperties fetches properties for a specific account in a pending session
func (h *Handler) fetchAccountProperties(w http.ResponseWriter, r *http.Request, sessionID, accountID string) {
	logger := loggerWithRequest(r)

	session := getPendingGASession(sessionID)
	if session == nil {
		BadRequest(w, r, "Session expired or not found. Please reconnect to Google Analytics.")
		return
	}

	// Verify the account is in the session
	validAccount := false
	for _, acc := range session.Accounts {
		if acc.AccountID == accountID {
			validAccount = true
			break
		}
	}
	if !validAccount {
		BadRequest(w, r, "Account not found in session")
		return
	}

	// Fetch properties for this account
	logger.Info("Fetching GA4 properties", "account_id", accountID)

	client := &http.Client{Timeout: 30 * time.Second}
	properties, err := h.fetchPropertiesForAccount(r.Context(), logger, client, session.AccessToken, accountID)
	if err != nil {
		logger.Error("Failed to fetch properties for account", "error", err, "account_id", accountID)
		InternalError(w, r, fmt.Errorf("failed to fetch properties: %w", err))
		return
	}

	logger.Info("Properties fetched successfully", "account_id", accountID, "property_count", len(properties))

	// Update session with these properties
	pendingGASessionsMu.Lock()
	if s, ok := pendingGASessions[sessionID]; ok {
		s.Properties = properties
	}
	pendingGASessionsMu.Unlock()

	WriteSuccess(w, r, accountPropertiesResponse{
		Properties: properties,
		AccountID:  accountID,
	}, "")
}

// listGoogleConnections lists all Google Analytics connections for the user's organisation
func (h *Handler) listGoogleConnections(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		WriteSuccess(w, r, []GoogleConnectionResponse{}, "No organisation")
		return
	}

	connections, err := h.DB.ListGoogleConnections(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to list Google Analytics connections", "error", err)
		InternalError(w, r, err)
		return
	}

	response := make([]GoogleConnectionResponse, 0, len(connections))
	for _, conn := range connections {
		response = append(response, GoogleConnectionResponse{
			ID:                conn.ID,
			GA4PropertyID:     conn.GA4PropertyID,
			GA4PropertyName:   conn.GA4PropertyName,
			GoogleAccountName: conn.GoogleAccountName,
			GoogleEmail:       conn.GoogleEmail,
			Status:            conn.Status,
			DomainIDs:         conn.DomainIDs,
			CreatedAt:         conn.CreatedAt.Format(time.RFC3339),
		})
	}

	WriteSuccess(w, r, response, "")
}

// getOrganisationDomains returns all domains for the authenticated user's organisation
func (h *Handler) getOrganisationDomains(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		WriteSuccess(w, r, organisationDomainsResponse{Domains: []db.OrganisationDomain{}}, "No organisation")
		return
	}

	domains, err := h.DB.GetDomainsForOrganisation(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to get organisation domains", "error", err, "organisation_id", orgID)
		InternalError(w, r, err)
		return
	}

	logger.Debug("Returning domains for organisation", "organisation_id", orgID, "domain_count", len(domains))

	WriteSuccess(w, r, organisationDomainsResponse{Domains: domains}, "")
}

// deleteGoogleConnection deletes a Google Analytics connection
func (h *Handler) deleteGoogleConnection(w http.ResponseWriter, r *http.Request, connectionID string) {
	logger := loggerWithRequest(r)

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	err = h.DB.DeleteGoogleConnection(r.Context(), connectionID, orgID)
	if err != nil {
		if errors.Is(err, db.ErrGoogleConnectionNotFound) {
			NotFound(w, r, "Google Analytics connection not found")
			return
		}
		logger.Error("Failed to delete Google Analytics connection", "error", err)
		InternalError(w, r, err)
		return
	}

	logger.Info("Google Analytics connection deleted", "connection_id", connectionID)
	WriteNoContent(w, r)
}

// GA4AccountResponse represents a Google Analytics account in API responses
type GA4AccountResponse struct {
	ID                string `json:"id"`
	GoogleAccountID   string `json:"google_account_id"`
	GoogleAccountName string `json:"google_account_name,omitempty"`
	GoogleEmail       string `json:"google_email,omitempty"`
	HasToken          bool   `json:"has_token"`
	CreatedAt         string `json:"created_at"`
}

type googleSavedPropertiesResponse struct {
	SavedCount  int `json:"saved_count"`
	ActiveCount int `json:"active_count"`
}

type pendingGASessionResponse struct {
	Accounts   []GA4Account  `json:"accounts"`
	Properties []GA4Property `json:"properties"`
	State      string        `json:"state"`
	UserID     string        `json:"user_id"`
	Email      string        `json:"email"`
}

type accountPropertiesResponse struct {
	Properties []GA4Property `json:"properties"`
	AccountID  string        `json:"account_id"`
}

type organisationDomainsResponse struct {
	Domains []db.OrganisationDomain `json:"domains"`
}

type ga4AccountsListResponse struct {
	Accounts []GA4AccountResponse `json:"accounts"`
}

type googleReauthResponse struct {
	NeedsReauth bool   `json:"needs_reauth"`
	Message     string `json:"message"`
}

type refreshGA4AccountsResponse struct {
	Accounts    []GA4AccountResponse `json:"accounts"`
	NeedsReauth bool                 `json:"needs_reauth"`
}

type ga4PropertiesResponse struct {
	Properties []GA4Property `json:"properties"`
}

type savedCountResponse struct {
	SavedCount int `json:"saved_count"`
}

// ListGA4Accounts returns stored GA4 accounts from the database
// GET /v1/integrations/google/accounts
func (h *Handler) ListGA4Accounts(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		WriteSuccess(w, r, ga4AccountsListResponse{Accounts: []GA4AccountResponse{}}, "No organisation")
		return
	}

	accounts, err := h.DB.ListGA4Accounts(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to list Google Analytics accounts", "error", err)
		InternalError(w, r, err)
		return
	}

	response := make([]GA4AccountResponse, 0, len(accounts))
	for _, acc := range accounts {
		response = append(response, GA4AccountResponse{
			ID:                acc.ID,
			GoogleAccountID:   acc.GoogleAccountID,
			GoogleAccountName: acc.GoogleAccountName,
			GoogleEmail:       acc.GoogleEmail,
			HasToken:          acc.VaultSecretName != "",
			CreatedAt:         acc.CreatedAt.Format(time.RFC3339),
		})
	}

	WriteSuccess(w, r, ga4AccountsListResponse{Accounts: response}, "")
}

// RefreshGA4Accounts syncs accounts from Google API and updates the database
// POST /v1/integrations/google/accounts/refresh
func (h *Handler) RefreshGA4Accounts(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	accountWithToken, refreshToken, err := h.getGARefreshToken(r.Context(), logger, orgID)
	if err != nil {
		if errors.Is(err, db.ErrGoogleTokenNotFound) || errors.Is(err, db.ErrGoogleAccountNotFound) || errors.Is(err, db.ErrGoogleConnectionNotFound) {
			WriteSuccess(w, r, googleReauthResponse{
				NeedsReauth: true,
				Message:     "No valid Google token found. Please reconnect to Google Analytics.",
			}, "")
			return
		}
		logger.Error("Failed to resolve GA refresh token", "error", err)
		InternalError(w, r, err)
		return
	}

	// Refresh the access token using the refresh token
	accessToken, err := h.refreshGoogleAccessToken(refreshToken)
	if err != nil {
		logger.Warn("Failed to refresh Google access token", "error", err, "next_action", "reauth_required")
		// Token might be revoked
		WriteSuccess(w, r, googleReauthResponse{
			NeedsReauth: true,
			Message:     "Unable to refresh token. Please reconnect to Google Analytics.",
		}, "")
		return
	}

	// Fetch accounts from Google API
	accounts, err := h.fetchGA4Accounts(r.Context(), accessToken)
	if err != nil {
		logger.Error("Failed to fetch GA4 accounts from Google", "error", err)
		InternalError(w, r, fmt.Errorf("failed to fetch accounts from Google: %w", err))
		return
	}

	// Sync accounts to database
	now := time.Now().UTC()
	for _, acc := range accounts {
		dbAccount := &db.GoogleAnalyticsAccount{
			ID:                uuid.New().String(),
			OrganisationID:    orgID,
			GoogleAccountID:   acc.AccountID,
			GoogleAccountName: acc.DisplayName,
			GoogleUserID:      accountWithToken.GoogleUserID,
			GoogleEmail:       accountWithToken.GoogleEmail,
			InstallingUserID:  userClaims.UserID,
			CreatedAt:         now,
			UpdatedAt:         now,
		}

		if err := h.DB.UpsertGA4Account(r.Context(), dbAccount); err != nil {
			logger.Warn("Failed to upsert GA4 account", "error", err, "account_id", acc.AccountID)
		}
	}

	logger.Info("Refreshed GA4 accounts from Google API", "organisation_id", orgID, "account_count", len(accounts))

	// Return fresh list from DB
	dbAccounts, err := h.DB.ListGA4Accounts(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to list refreshed GA4 accounts", "error", err)
		InternalError(w, r, err)
		return
	}

	response := make([]GA4AccountResponse, 0, len(dbAccounts))
	for _, acc := range dbAccounts {
		response = append(response, GA4AccountResponse{
			ID:                acc.ID,
			GoogleAccountID:   acc.GoogleAccountID,
			GoogleAccountName: acc.GoogleAccountName,
			GoogleEmail:       acc.GoogleEmail,
			HasToken:          acc.VaultSecretName != "",
			CreatedAt:         acc.CreatedAt.Format(time.RFC3339),
		})
	}

	WriteSuccess(w, r, refreshGA4AccountsResponse{
		Accounts:    response,
		NeedsReauth: false,
	}, "Accounts refreshed successfully")
}

// refreshGoogleAccessToken exchanges a refresh token for a new access token
func (h *Handler) refreshGoogleAccessToken(refreshToken string) (string, error) {
	values := url.Values{}
	values.Set("client_id", h.GoogleClientID)
	values.Set("client_secret", h.GoogleClientSecret)
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", refreshToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.PostForm("https://oauth2.googleapis.com/token", values)
	if err != nil {
		return "", fmt.Errorf("failed to refresh token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token refresh returned status: %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	return tokenResp.AccessToken, nil
}

// getGARefreshToken resolves a usable refresh token for the organisation.
// It prefers account-level tokens, then falls back to any connection-level token.
func (h *Handler) getGARefreshToken(ctx context.Context, logger *logging.Logger, organisationID string) (*db.GoogleAnalyticsAccount, string, error) {
	accountWithToken, err := h.DB.GetGA4AccountWithToken(ctx, organisationID)
	if err == nil {
		refreshToken, tokenErr := h.DB.GetGA4AccountToken(ctx, accountWithToken.ID)
		if tokenErr == nil {
			return accountWithToken, refreshToken, nil
		}
		if !errors.Is(tokenErr, db.ErrGoogleTokenNotFound) {
			return nil, "", tokenErr
		}
		logger.Warn("GA account token missing in vault, falling back to connection token", "organisation_id", organisationID, "account_id", accountWithToken.ID, "next_action", "retry_token_fetch_or_reauth")
	} else if !errors.Is(err, db.ErrGoogleAccountNotFound) {
		return nil, "", err
	}

	connectionWithToken, err := h.DB.GetGAConnectionWithToken(ctx, organisationID)
	if err == nil {
		refreshToken, tokenErr := h.DB.GetGoogleToken(ctx, connectionWithToken.ID)
		if tokenErr == nil {
			account := &db.GoogleAnalyticsAccount{
				GoogleUserID: connectionWithToken.GoogleUserID,
				GoogleEmail:  connectionWithToken.GoogleEmail,
			}
			return account, refreshToken, nil
		}
		if !errors.Is(tokenErr, db.ErrGoogleTokenNotFound) {
			return nil, "", tokenErr
		}
		logger.Warn("GA connection token missing in vault", "organisation_id", organisationID, "connection_id", connectionWithToken.ID, "next_action", "reauth_required")
	} else if !errors.Is(err, db.ErrGoogleConnectionNotFound) {
		return nil, "", err
	}

	return nil, "", db.ErrGoogleTokenNotFound
}

// GetAccountProperties fetches properties for a Google account using stored refresh token
// GET /v1/integrations/google/accounts/{googleAccountId}/properties
func (h *Handler) GetAccountProperties(w http.ResponseWriter, r *http.Request, googleAccountID string) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	_, err = h.DB.GetGA4AccountByGoogleID(r.Context(), orgID, googleAccountID)
	if err != nil {
		if errors.Is(err, db.ErrGoogleAccountNotFound) {
			BadRequest(w, r, "Google account not found for organisation")
			return
		}
		logger.Error("Failed to resolve Google account", "error", err, "google_account_id", googleAccountID)
		InternalError(w, r, err)
		return
	}

	_, refreshToken, err := h.getGARefreshToken(r.Context(), logger, orgID)
	if err != nil {
		if errors.Is(err, db.ErrGoogleTokenNotFound) || errors.Is(err, db.ErrGoogleAccountNotFound) || errors.Is(err, db.ErrGoogleConnectionNotFound) {
			WriteSuccess(w, r, googleReauthResponse{
				NeedsReauth: true,
				Message:     "No valid Google token found. Please reconnect to Google Analytics.",
			}, "")
			return
		}
		logger.Error("Failed to resolve GA refresh token", "error", err)
		InternalError(w, r, err)
		return
	}

	// Refresh the access token
	accessToken, err := h.refreshGoogleAccessToken(refreshToken)
	if err != nil {
		logger.Warn("Failed to refresh Google access token", "error", err, "next_action", "reauth_required")
		WriteSuccess(w, r, googleReauthResponse{
			NeedsReauth: true,
			Message:     "Unable to refresh token. Please reconnect to Google Analytics.",
		}, "")
		return
	}

	// Fetch properties for this account from Google API
	client := &http.Client{Timeout: 30 * time.Second}
	properties, err := h.fetchPropertiesForAccount(r.Context(), logger, client, accessToken, googleAccountID)
	if err != nil {
		logger.Error("Failed to fetch GA4 properties", "error", err, "google_account_id", googleAccountID)
		InternalError(w, r, fmt.Errorf("failed to fetch properties: %w", err))
		return
	}

	logger.Info("Fetched GA4 properties for account", "google_account_id", googleAccountID, "property_count", len(properties))

	WriteSuccess(w, r, ga4PropertiesResponse{Properties: properties}, "")
}

// SaveGA4AccountProperties saves all properties for a stored GA4 account as inactive
// POST /v1/integrations/google/accounts/{googleAccountId}/save-properties
func (h *Handler) SaveGA4AccountProperties(w http.ResponseWriter, r *http.Request, googleAccountID string) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err, "user_id", userClaims.UserID)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	account, err := h.DB.GetGA4AccountByGoogleID(r.Context(), orgID, googleAccountID)
	if err != nil {
		if errors.Is(err, db.ErrGoogleAccountNotFound) {
			BadRequest(w, r, "Google account not found for organisation")
			return
		}
		logger.Error("Failed to resolve Google account", "error", err, "google_account_id", googleAccountID)
		InternalError(w, r, err)
		return
	}

	refreshToken, err := h.DB.GetGA4AccountToken(r.Context(), account.ID)
	if err != nil {
		if errors.Is(err, db.ErrGoogleTokenNotFound) {
			_, fallbackToken, fallbackErr := h.getGARefreshToken(r.Context(), logger, orgID)
			if fallbackErr != nil {
				if errors.Is(fallbackErr, db.ErrGoogleTokenNotFound) || errors.Is(fallbackErr, db.ErrGoogleAccountNotFound) || errors.Is(fallbackErr, db.ErrGoogleConnectionNotFound) {
					WriteSuccess(w, r, googleReauthResponse{
						NeedsReauth: true,
						Message:     "No valid Google token found. Please reconnect to Google Analytics.",
					}, "")
					return
				}
				logger.Error("Failed to resolve fallback GA refresh token", "error", fallbackErr, "organisation_id", orgID)
				InternalError(w, r, fallbackErr)
				return
			}
			refreshToken = fallbackToken
		} else {
			logger.Error("Failed to resolve GA refresh token", "error", err, "account_id", account.ID)
			InternalError(w, r, err)
			return
		}
	}

	accessToken, err := h.refreshGoogleAccessToken(refreshToken)
	if err != nil {
		logger.Warn("Failed to refresh Google access token", "error", err, "next_action", "reauth_required")
		WriteSuccess(w, r, googleReauthResponse{
			NeedsReauth: true,
			Message:     "Unable to refresh token. Please reconnect to Google Analytics.",
		}, "")
		return
	}

	client := &http.Client{Timeout: 30 * time.Second}
	properties, err := h.fetchPropertiesForAccount(r.Context(), logger, client, accessToken, googleAccountID)
	if err != nil {
		logger.Error("Failed to fetch GA4 properties", "error", err, "google_account_id", googleAccountID)
		InternalError(w, r, fmt.Errorf("failed to fetch properties: %w", err))
		return
	}

	if len(properties) == 0 {
		BadRequest(w, r, "No properties found for this account")
		return
	}

	now := time.Now().UTC()
	savedCount := 0
	for _, prop := range properties {
		if prop.PropertyID == "" {
			logger.Debug("Skipping property with empty ID", "display_name", prop.DisplayName)
			continue
		}

		domainIDsArray := make(pq.Int64Array, 0)

		conn := &db.GoogleAnalyticsConnection{
			ID:               uuid.New().String(),
			OrganisationID:   orgID,
			GA4PropertyID:    prop.PropertyID,
			GA4PropertyName:  prop.DisplayName,
			GoogleAccountID:  googleAccountID,
			GoogleUserID:     account.GoogleUserID,
			GoogleEmail:      account.GoogleEmail,
			InstallingUserID: userClaims.UserID,
			Status:           "inactive",
			DomainIDs:        domainIDsArray,
			CreatedAt:        now,
			UpdatedAt:        now,
		}

		if err := h.DB.CreateGoogleConnection(r.Context(), conn); err != nil {
			logger.Warn("Failed to save property connection", "error", err, "property_id", prop.PropertyID, "next_action", "retry_connection_create_or_check_db_connectivity")
			continue
		}

		savedCount++
	}

	logger.Info("Google Analytics account properties saved", "organisation_id", orgID, "google_account_id", googleAccountID, "saved_count", savedCount)

	WriteSuccess(w, r, savedCountResponse{SavedCount: savedCount}, "Google Analytics properties saved successfully")
}
