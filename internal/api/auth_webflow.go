package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/good-native/hover/internal/auth"
	"github.com/good-native/hover/internal/db"
	"github.com/google/uuid"
)

// Webflow OAuth credentials loaded from environment variables
func getWebflowClientID() string {
	return os.Getenv("WEBFLOW_CLIENT_ID")
}

func getWebflowClientSecret() string {
	return os.Getenv("WEBFLOW_CLIENT_SECRET")
}

func getWebflowRedirectURI() string {
	if override := strings.TrimSpace(os.Getenv("WEBFLOW_REDIRECT_URI")); override != "" {
		return override
	}

	return getAppURL() + "/v1/integrations/webflow/callback"
}

// WebflowTokenResponse represents the response from Webflow's token endpoint
type WebflowTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	// Webflow doesn't always return expires_in or refresh_token in all flows, but normally standard OAuth
}

// InitiateWebflowOAuth starts the OAuth flow
func (h *Handler) InitiateWebflowOAuth(w http.ResponseWriter, r *http.Request) {
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
		logger.Error("Failed to get or create user", "error", err)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	if getWebflowClientID() == "" {
		logger.Error("WEBFLOW_CLIENT_ID not configured")
		InternalError(w, r, fmt.Errorf("webflow integration not configured"))
		return
	}

	if getOAuthStateSecret() == "" {
		logger.Error("SUPABASE_JWT_SECRET not configured for OAuth state signing")
		InternalError(w, r, fmt.Errorf("webflow integration not configured"))
		return
	}

	// Sign state with JWT Secret
	state, err := h.generateOAuthState(userClaims.UserID, orgID)
	if err != nil {
		logger.Error("Failed to generate OAuth state", "error", err)
		InternalError(w, r, err)
		return
	}

	// Scopes: authorized_user:read (for user name), sites:read, sites:write (for webhooks), cms:read
	// Note: workspaces:read is Enterprise-only, so we use authorized_user:read to identify the connection
	scopes := "authorized_user:read sites:read sites:write cms:read"

	// Build Webflow OAuth URL
	authURL := fmt.Sprintf(
		"https://webflow.com/oauth/authorize?client_id=%s&response_type=code&scope=%s&redirect_uri=%s&state=%s",
		url.QueryEscape(getWebflowClientID()),
		url.QueryEscape(scopes),
		url.QueryEscape(getWebflowRedirectURI()),
		url.QueryEscape(state), // Webflow returns this state in callback
	)

	WriteSuccess(w, r, map[string]string{"auth_url": authURL}, "Redirect to this URL to connect Webflow")
}

// HandleWebflowOAuthCallback processes the OAuth callback from Webflow
func (h *Handler) HandleWebflowOAuthCallback(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	errParam := r.URL.Query().Get("error")

	if errParam != "" {
		logger.Warn("Webflow OAuth denied", "error", errParam)
		h.redirectToSettingsWithError(w, r, "Webflow", "Webflow connection was cancelled", "auto-crawl", "webflow")
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
		h.redirectToSettingsWithError(w, r, "Webflow", "Invalid or expired state", "auto-crawl", "webflow")
		return
	}

	// Exchange code for access token
	tokenResp, err := h.exchangeWebflowCode(code)
	if err != nil {
		logger.Error("Failed to exchange Webflow OAuth code", "error", err)
		h.redirectToSettingsWithError(w, r, "Webflow", "Failed to connect to Webflow", "auto-crawl", "webflow")
		return
	}

	// Fetch user/workspace info from Webflow
	authInfo, err := h.fetchWebflowAuthInfo(r.Context(), tokenResp.AccessToken)
	if err != nil {
		// Log but don't fail - we can still create the connection with empty values
		logger.Warn("Failed to fetch Webflow auth info, proceeding with empty values", "error", err)
	}

	// Extract user and workspace info
	authedUserID := ""
	workspaceID := ""
	displayName := ""
	if authInfo != nil {
		authedUserID = authInfo.UserID
		displayName = authInfo.DisplayName // User's name or email
		if len(authInfo.WorkspaceIDs) > 0 {
			workspaceID = authInfo.WorkspaceIDs[0] // Use first workspace
		}
	}
	now := time.Now().UTC()
	conn := &db.WebflowConnection{
		ID:                 uuid.New().String(),
		OrganisationID:     state.OrgID,
		AuthedUserID:       authedUserID,
		WebflowWorkspaceID: workspaceID,
		WorkspaceName:      displayName, // User's name or email from authorized_by endpoint
		InstallingUserID:   state.UserID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if err := h.DB.CreateWebflowConnection(r.Context(), conn); err != nil {
		logger.Error("Failed to save Webflow connection", "error", err)
		h.redirectToSettingsWithError(w, r, "Webflow", "Failed to save connection", "auto-crawl", "webflow")
		return
	}

	// Store access token in Supabase Vault
	if err := h.DB.StoreWebflowToken(r.Context(), conn.ID, tokenResp.AccessToken); err != nil {
		logger.Error("Failed to store access token in vault", "error", err)
		// Clean up the orphan connection since we can't use it without the token
		if delErr := h.DB.DeleteWebflowConnection(r.Context(), conn.ID, state.OrgID); delErr != nil {
			logger.Error("Failed to clean up orphan connection after token storage failure", "error", delErr)
		}
		h.redirectToSettingsWithError(w, r, "Webflow", "Failed to secure connection", "auto-crawl", "webflow")
		return
	}

	// Link workspace to organisation for webhook resolution when available.
	if workspaceID != "" {
		mapping := &db.PlatformOrgMapping{
			Platform:       "webflow",
			PlatformID:     workspaceID,
			OrganisationID: state.OrgID,
			CreatedBy:      &state.UserID,
		}
		if err := h.DB.UpsertPlatformOrgMapping(r.Context(), mapping); err != nil {
			logger.Error("Failed to store Webflow workspace mapping", "error", err)
			h.redirectToSettingsWithError(w, r, "Webflow", "Failed to save Webflow workspace mapping", "auto-crawl", "webflow")
			return
		}
	} else {
		logger.Warn("Webflow connection saved without workspace ID; webhook callbacks may fail until workspace is available", "organisation_id", state.OrgID)
	}

	// Note: Webhooks are now registered per-site via the site settings UI
	// instead of bulk registration during OAuth

	logger.Info("Webflow connection established", "organisation_id", state.OrgID, "webflow_workspace_id", workspaceID)

	// Redirect to settings with success + setup flag to open site configuration
	h.redirectToSettingsWithSetup(w, r, "Webflow", "Webflow Connection", conn.ID, "auto-crawl", "webflow")
}

func (h *Handler) exchangeWebflowCode(code string) (*WebflowTokenResponse, error) {
	values := url.Values{}
	values.Set("client_id", getWebflowClientID())
	values.Set("client_secret", getWebflowClientSecret())
	values.Set("grant_type", "authorization_code")
	values.Set("code", code)
	values.Set("redirect_uri", getWebflowRedirectURI())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.PostForm("https://api.webflow.com/oauth/access_token", values)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webflow API returned status: %d", resp.StatusCode)
	}

	var tokenResp WebflowTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

// WebflowAuthInfo contains user and workspace info from Webflow's API
type WebflowAuthInfo struct {
	UserID       string
	WorkspaceIDs []string
	// User info from authorized_by endpoint
	UserEmail     string
	UserFirstName string
	UserLastName  string
	DisplayName   string // Combined name for display (e.g., "Simon Chua" or email)
}

// fetchWebflowAuthInfo calls Webflow's API to get user and workspace info
func (h *Handler) fetchWebflowAuthInfo(ctx context.Context, token string) (*WebflowAuthInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	// First, get authorisation info (workspace IDs)
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.webflow.com/v2/token/introspect", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create introspect request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call introspect endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspect endpoint returned status: %d", resp.StatusCode)
	}

	var introspectResp struct {
		Authorization struct {
			AuthorizedTo struct {
				UserID       string   `json:"userId"`
				WorkspaceIDs []string `json:"workspaceIds"`
				WorkspaceID  string   `json:"workspaceId"`
			} `json:"authorizedTo"`
		} `json:"authorization"`

		AuthorizedTo struct {
			UserID       string   `json:"userId"`
			WorkspaceIDs []string `json:"workspaceIds"`
			WorkspaceID  string   `json:"workspaceId"`
		} `json:"authorized_to"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read introspect response body: %w", err)
	}
	if err := json.Unmarshal(body, &introspectResp); err != nil {
		return nil, fmt.Errorf("failed to decode introspect response: %w", err)
	}

	authInfo := &WebflowAuthInfo{
		UserID:       introspectResp.Authorization.AuthorizedTo.UserID,
		WorkspaceIDs: introspectResp.Authorization.AuthorizedTo.WorkspaceIDs,
	}

	// Fallback for variations in token response structure.
	if authInfo.UserID == "" {
		authInfo.UserID = introspectResp.AuthorizedTo.UserID
	}
	if len(authInfo.WorkspaceIDs) == 0 {
		authInfo.WorkspaceIDs = append([]string(nil), introspectResp.AuthorizedTo.WorkspaceIDs...)
	}
	if introspectResp.Authorization.AuthorizedTo.WorkspaceID != "" {
		authInfo.WorkspaceIDs = append(authInfo.WorkspaceIDs, introspectResp.Authorization.AuthorizedTo.WorkspaceID)
	}
	if introspectResp.AuthorizedTo.WorkspaceID != "" {
		authInfo.WorkspaceIDs = append(authInfo.WorkspaceIDs, introspectResp.AuthorizedTo.WorkspaceID)
	}

	// Deduplicate workspace IDs and trim whitespace.
	workspaceIDs := make([]string, 0, len(authInfo.WorkspaceIDs))
	seenWorkspaceIDs := map[string]struct{}{}
	for _, workspaceID := range authInfo.WorkspaceIDs {
		workspaceID = strings.TrimSpace(workspaceID)
		if workspaceID == "" {
			continue
		}
		if _, seen := seenWorkspaceIDs[workspaceID]; seen {
			continue
		}
		seenWorkspaceIDs[workspaceID] = struct{}{}
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	authInfo.WorkspaceIDs = workspaceIDs

	// Fallback: if introspect returned no workspace IDs, try the sites API.
	// The v2/sites response includes workspaceId on each site.
	if len(authInfo.WorkspaceIDs) == 0 {
		sites, err := h.fetchWebflowSites(ctx, token)
		if err != nil {
			apiLog.Warn("Failed to fetch Webflow sites for workspace ID fallback", "error", err)
		} else {
			seen := map[string]struct{}{}
			for _, site := range sites {
				wid := strings.TrimSpace(site.WorkspaceID)
				if wid == "" {
					continue
				}
				if _, ok := seen[wid]; ok {
					continue
				}
				seen[wid] = struct{}{}
				authInfo.WorkspaceIDs = append(authInfo.WorkspaceIDs, wid)
			}
			if len(authInfo.WorkspaceIDs) > 0 {
				apiLog.Info("Resolved workspace IDs from Webflow sites API fallback", "workspace_ids", authInfo.WorkspaceIDs)
			}
		}
	}

	// Fetch user info from authorized_by endpoint
	userInfo, err := h.fetchWebflowUserInfo(ctx, client, token)
	if err != nil {
		apiLog.Warn("Failed to fetch Webflow user info", "error", err)
	} else {
		authInfo.UserEmail = userInfo.Email
		authInfo.UserFirstName = userInfo.FirstName
		authInfo.UserLastName = userInfo.LastName
		authInfo.DisplayName = userInfo.DisplayName
	}

	return authInfo, nil
}

// WebflowUserInfo contains user details from the authorized_by endpoint
type WebflowUserInfo struct {
	ID          string
	Email       string
	FirstName   string
	LastName    string
	DisplayName string
}

// fetchWebflowUserInfo fetches the authorising user's info
func (h *Handler) fetchWebflowUserInfo(ctx context.Context, client *http.Client, token string) (*WebflowUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.webflow.com/v2/token/authorized_by", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create authorized_by request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call authorized_by endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authorized_by endpoint returned status: %d", resp.StatusCode)
	}

	var userResp struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userResp); err != nil {
		return nil, fmt.Errorf("failed to decode authorized_by response: %w", err)
	}

	userInfo := &WebflowUserInfo{
		ID:        userResp.ID,
		Email:     userResp.Email,
		FirstName: userResp.FirstName,
		LastName:  userResp.LastName,
	}

	// Build display name: prefer "FirstName LastName", fall back to email
	if userResp.FirstName != "" || userResp.LastName != "" {
		userInfo.DisplayName = strings.TrimSpace(userResp.FirstName + " " + userResp.LastName)
	} else if userResp.Email != "" {
		userInfo.DisplayName = userResp.Email
	} else {
		userInfo.DisplayName = "Webflow User"
	}

	return userInfo, nil
}

// WebflowConnectionResponse represents a Webflow connection in API responses
type WebflowConnectionResponse struct {
	ID                 string `json:"id"`
	WebflowWorkspaceID string `json:"webflow_workspace_id,omitempty"`
	WorkspaceName      string `json:"workspace_name,omitempty"`
	CreatedAt          string `json:"created_at"`
}

// WebflowConnectionsHandler handles requests to /v1/integrations/webflow
func (h *Handler) WebflowConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listWebflowConnections(w, r)
	case http.MethodPost:
		h.InitiateWebflowOAuth(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

// WebflowConnectionHandler handles requests to /v1/integrations/webflow/:id
func (h *Handler) WebflowConnectionHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/integrations/webflow/")
	if path == "" {
		BadRequest(w, r, "Connection ID is required")
		return
	}

	// Handle callback separately (no auth required)
	if path == "callback" {
		if r.Method == http.MethodGet {
			h.HandleWebflowOAuthCallback(w, r)
			return
		}
		MethodNotAllowed(w, r)
		return
	}

	parts := strings.Split(path, "/")
	connectionID := parts[0]
	if _, err := uuid.Parse(connectionID); err != nil {
		BadRequest(w, r, "Invalid connection ID format")
		return
	}

	// Handle /v1/integrations/webflow/{connection_id}/sites
	if len(parts) > 1 && parts[1] == "sites" {
		h.WebflowSitesHandler(w, r)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		h.deleteWebflowConnection(w, r, connectionID)
	default:
		MethodNotAllowed(w, r)
	}
}

// listWebflowConnections lists all Webflow connections for the user's organisation
func (h *Handler) listWebflowConnections(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		WriteSuccess(w, r, []WebflowConnectionResponse{}, "No organisation")
		return
	}

	connections, err := h.DB.ListWebflowConnections(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to list Webflow connections", "error", err)
		InternalError(w, r, err)
		return
	}

	response := make([]WebflowConnectionResponse, 0, len(connections))
	for _, conn := range connections {
		response = append(response, WebflowConnectionResponse{
			ID:                 conn.ID,
			WebflowWorkspaceID: conn.WebflowWorkspaceID,
			WorkspaceName:      conn.WorkspaceName,
			CreatedAt:          conn.CreatedAt.Format(time.RFC3339),
		})
	}

	WriteSuccess(w, r, response, "")
}

// deleteWebflowConnection deletes a Webflow connection
func (h *Handler) deleteWebflowConnection(w http.ResponseWriter, r *http.Request, connectionID string) {
	logger := loggerWithRequest(r)

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		logger.Error("Failed to get or create user", "error", err)
		InternalError(w, r, err)
		return
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return
	}

	err = h.DB.DeleteWebflowConnection(r.Context(), connectionID, orgID)
	if err != nil {
		if errors.Is(err, db.ErrWebflowConnectionNotFound) {
			NotFound(w, r, "Webflow connection not found")
			return
		}
		logger.Error("Failed to delete Webflow connection", "error", err)
		InternalError(w, r, err)
		return
	}

	logger.Info("Webflow connection deleted", "connection_id", connectionID)
	WriteNoContent(w, r)
}

// Trigger deployment
