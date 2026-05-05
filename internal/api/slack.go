package api

import (
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
	"github.com/slack-go/slack"
)

// SlackConnectionResponse represents a Slack connection in API responses
type SlackConnectionResponse struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	CreatedAt     string `json:"created_at"`
}

// SlackUserLinkResponse represents a user link in API responses
type SlackUserLinkResponse struct {
	ID              string `json:"id"`
	SlackUserID     string `json:"slack_user_id"`
	DMNotifications bool   `json:"dm_notifications"`
	CreatedAt       string `json:"created_at"`
}

// SlackConnectRequest represents the request to initiate OAuth
type SlackConnectRequest struct {
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// SlackConnectResponse returns the OAuth URL to redirect to
type SlackConnectResponse struct {
	AuthURL string `json:"auth_url"`
}

// SlackLinkUserRequest represents the request to link a Slack user
type SlackLinkUserRequest struct {
	ConnectionID    string `json:"connection_id"`
	SlackUserID     string `json:"slack_user_id,omitempty"` // Optional if using email match
	DMNotifications bool   `json:"dm_notifications"`
}

// SlackUpdateNotificationsRequest updates notification preferences
type SlackUpdateNotificationsRequest struct {
	DMNotifications bool `json:"dm_notifications"`
}

// OAuthState type moved to oauth_utils.go

const (
	// slackOAuthScopes defines the permissions requested from Slack during OAuth
	slackOAuthScopes = "chat:write,im:write,users:read,users:read.email"
	// slackAPITimeout is the timeout for Slack API calls
	slackAPITimeout = 30 * time.Second
)

// getSlackClientID returns the Slack OAuth client ID
func getSlackClientID() string {
	return os.Getenv("SLACK_CLIENT_ID")
}

// getSlackClientSecret returns the Slack OAuth client secret
func getSlackClientSecret() string {
	return os.Getenv("SLACK_CLIENT_SECRET")
}

// getSlackStateSecret returns the secret used for HMAC signing OAuth state
// Returns empty string if not configured - callers should validate
func getSlackStateSecret() string {
	return os.Getenv("SUPABASE_JWT_SECRET")
}

// SlackConnectionsHandler handles requests to /v1/integrations/slack
func (h *Handler) SlackConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listSlackConnections(w, r)
	case http.MethodPost:
		h.initiateSlackOAuth(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

// SlackConnectionHandler handles requests to /v1/integrations/slack/:id and sub-routes
func (h *Handler) SlackConnectionHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/integrations/slack/")
	if path == "" {
		BadRequest(w, r, "Path is required")
		return
	}

	parts := strings.Split(path, "/")

	// Handle special routes first (non-connection-specific)
	switch parts[0] {
	case "callback":
		if r.Method == http.MethodGet {
			h.handleSlackOAuthCallback(w, r)
			return
		}
		MethodNotAllowed(w, r)
		return
	case "connect":
		if r.Method == http.MethodPost {
			h.initiateSlackOAuth(w, r)
			return
		}
		MethodNotAllowed(w, r)
		return
	}

	// Validate connection ID
	connectionID := parts[0]
	if _, err := uuid.Parse(connectionID); err != nil {
		BadRequest(w, r, "Invalid connection ID format")
		return
	}

	// Handle sub-routes for a specific connection
	if len(parts) > 1 {
		switch parts[1] {
		case "link-user":
			switch r.Method {
			case http.MethodPost:
				h.linkSlackUser(w, r, connectionID)
			case http.MethodDelete:
				h.unlinkSlackUser(w, r, connectionID)
			case http.MethodPut:
				h.updateSlackUserNotifications(w, r, connectionID)
			default:
				MethodNotAllowed(w, r)
			}
			return
		case "user-link":
			if r.Method == http.MethodGet {
				h.getSlackUserLink(w, r, connectionID)
				return
			}
			MethodNotAllowed(w, r)
			return
		case "users":
			if r.Method == http.MethodGet {
				h.listSlackWorkspaceUsers(w, r, connectionID)
				return
			}
			MethodNotAllowed(w, r)
			return
		}
	}

	// Handle connection-level operations
	switch r.Method {
	case http.MethodGet:
		h.getSlackConnection(w, r, connectionID)
	case http.MethodDelete:
		h.deleteSlackConnection(w, r, connectionID)
	default:
		MethodNotAllowed(w, r)
	}
}

// SlackOAuthCallback handles the OAuth callback (no auth middleware)
func (h *Handler) SlackOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}
	h.handleSlackOAuthCallback(w, r)
}

// initiateSlackOAuth starts the OAuth flow
func (h *Handler) initiateSlackOAuth(w http.ResponseWriter, r *http.Request) {
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

	if getSlackClientID() == "" {
		logger.Error("SLACK_CLIENT_ID not configured")
		InternalError(w, r, fmt.Errorf("slack integration not configured"))
		return
	}

	if getSlackStateSecret() == "" {
		logger.Error("SUPABASE_JWT_SECRET not configured for OAuth state signing")
		InternalError(w, r, fmt.Errorf("slack integration not configured"))
		return
	}

	// Generate state token
	state, err := h.generateOAuthState(userClaims.UserID, orgID)
	if err != nil {
		logger.Error("Failed to generate OAuth state", "error", err)
		InternalError(w, r, err)
		return
	}

	// Build Slack OAuth URL
	authURL := fmt.Sprintf(
		"https://slack.com/oauth/v2/authorize?client_id=%s&scope=%s&redirect_uri=%s&state=%s",
		url.QueryEscape(getSlackClientID()),
		url.QueryEscape(slackOAuthScopes),
		url.QueryEscape(getSlackRedirectURI()),
		url.QueryEscape(state),
	)

	WriteSuccess(w, r, SlackConnectResponse{AuthURL: authURL}, "Redirect to this URL to connect Slack")
}

// handleSlackOAuthCallback processes the OAuth callback from Slack
func (h *Handler) handleSlackOAuthCallback(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	// Check for error from Slack
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		logger.Warn("Slack OAuth denied", "error", errParam)
		h.redirectToSettingsWithError(w, r, "Slack", "Slack connection was cancelled", "notifications", "slack")
		return
	}

	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")

	if code == "" || stateParam == "" {
		BadRequest(w, r, "Missing code or state parameter")
		return
	}

	// Validate state
	state, err := h.validateOAuthState(stateParam)
	if err != nil {
		logger.Warn("Invalid OAuth state", "error", err)
		h.redirectToSettingsWithError(w, r, "Slack", "Invalid or expired state", "notifications", "slack")
		return
	}

	// Exchange code for access token
	httpClient := &http.Client{Timeout: slackAPITimeout}
	resp, err := slack.GetOAuthV2Response(
		httpClient,
		getSlackClientID(),
		getSlackClientSecret(),
		code,
		getSlackRedirectURI(),
	)
	if err != nil {
		logger.Error("Failed to exchange OAuth code", "error", err)
		h.redirectToSettingsWithError(w, r, "Slack", "Failed to connect to Slack", "notifications", "slack")
		return
	}

	// Store connection
	now := time.Now().UTC()
	conn := &db.SlackConnection{
		ID:               uuid.New().String(),
		OrganisationID:   state.OrgID,
		WorkspaceID:      resp.Team.ID,
		WorkspaceName:    resp.Team.Name,
		BotUserID:        resp.BotUserID,
		InstallingUserID: &state.UserID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := h.DB.CreateSlackConnection(r.Context(), conn); err != nil {
		logger.Error("Failed to save Slack connection", "error", err)
		h.redirectToSettingsWithError(w, r, "Slack", "Failed to save connection", "notifications", "slack")
		return
	}

	// Store access token in Supabase Vault
	if err := h.DB.StoreSlackToken(r.Context(), conn.ID, resp.AccessToken); err != nil {
		logger.Error("Failed to store access token in vault", "error", err)
		h.redirectToSettingsWithError(w, r, "Slack", "Failed to secure connection", "notifications", "slack")
		return
	}

	logger.Info("Slack workspace connected",
		"workspace_id", resp.Team.ID,
		"workspace_name", resp.Team.Name,
		"organisation_id", state.OrgID,
	)

	// Redirect to settings with success (includes connection ID for auto-linking)
	h.redirectToSettingsWithSuccess(w, r, "Slack", resp.Team.Name, conn.ID, "notifications", "slack")
}

// listSlackConnections lists all Slack connections for the user's organisation
func (h *Handler) listSlackConnections(w http.ResponseWriter, r *http.Request) {
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
		WriteSuccess(w, r, []SlackConnectionResponse{}, "No organisation")
		return
	}

	connections, err := h.DB.ListSlackConnections(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to list Slack connections", "error", err)
		InternalError(w, r, err)
		return
	}

	response := make([]SlackConnectionResponse, 0, len(connections))
	for _, conn := range connections {
		response = append(response, SlackConnectionResponse{
			ID:            conn.ID,
			WorkspaceID:   conn.WorkspaceID,
			WorkspaceName: conn.WorkspaceName,
			CreatedAt:     conn.CreatedAt.Format(time.RFC3339),
		})
	}

	WriteSuccess(w, r, response, "")
}

// getSlackConnection retrieves a specific Slack connection
func (h *Handler) getSlackConnection(w http.ResponseWriter, r *http.Request, connectionID string) {
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
	conn, err := h.DB.GetSlackConnection(r.Context(), connectionID)
	if err != nil {
		if errors.Is(err, db.ErrSlackConnectionNotFound) {
			NotFound(w, r, "Slack connection not found")
			return
		}
		logger.Error("Failed to get Slack connection", "error", err)
		InternalError(w, r, err)
		return
	}

	// Verify org ownership
	if orgID == "" || orgID != conn.OrganisationID {
		Forbidden(w, r, "You don't have access to this connection")
		return
	}

	// Get user link for this connection if it exists
	var userLink *SlackUserLinkResponse
	link, err := h.DB.GetSlackUserLink(r.Context(), userClaims.UserID, connectionID)
	if err == nil {
		userLink = &SlackUserLinkResponse{
			ID:              link.ID,
			SlackUserID:     link.SlackUserID,
			DMNotifications: link.DMNotifications,
			CreatedAt:       link.CreatedAt.Format(time.RFC3339),
		}
	}

	response := struct {
		Connection SlackConnectionResponse `json:"connection"`
		UserLink   *SlackUserLinkResponse  `json:"user_link,omitempty"`
	}{
		Connection: SlackConnectionResponse{
			ID:            conn.ID,
			WorkspaceID:   conn.WorkspaceID,
			WorkspaceName: conn.WorkspaceName,
			CreatedAt:     conn.CreatedAt.Format(time.RFC3339),
		},
		UserLink: userLink,
	}

	WriteSuccess(w, r, response, "")
}

// deleteSlackConnection disconnects a Slack workspace
func (h *Handler) deleteSlackConnection(w http.ResponseWriter, r *http.Request, connectionID string) {
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
		Forbidden(w, r, "User must belong to an organisation")
		return
	}

	err = h.DB.DeleteSlackConnection(r.Context(), connectionID, orgID)
	if err != nil {
		if errors.Is(err, db.ErrSlackConnectionNotFound) {
			NotFound(w, r, "Slack connection not found")
			return
		}
		logger.Error("Failed to delete Slack connection", "error", err)
		InternalError(w, r, err)
		return
	}

	logger.Info("Slack connection deleted", "connection_id", connectionID)
	WriteNoContent(w, r)
}

// linkSlackUser links the current user to their Slack identity
// Note: Users are auto-linked when they sign in with Slack OIDC, so this endpoint
// is mainly used for manual overrides or when the auto-link didn't apply
func (h *Handler) linkSlackUser(w http.ResponseWriter, r *http.Request, connectionID string) {
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
	// Verify connection belongs to user's org
	conn, err := h.DB.GetSlackConnection(r.Context(), connectionID)
	if err != nil {
		if errors.Is(err, db.ErrSlackConnectionNotFound) {
			NotFound(w, r, "Slack connection not found")
			return
		}
		logger.Error("Failed to get Slack connection", "error", err)
		InternalError(w, r, err)
		return
	}

	if orgID == "" || orgID != conn.OrganisationID {
		Forbidden(w, r, "You don't have access to this connection")
		return
	}

	// Get slack_user_id from user profile (populated via Slack OIDC login)
	// or from request body for manual override
	// or look up by email as fallback
	var req SlackLinkUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		// Log but continue - body is optional
		logger.Debug("Failed to decode link-user request body", "error", err)
	}

	slackUserID := req.SlackUserID
	if slackUserID == "" && user.SlackUserID != nil {
		slackUserID = *user.SlackUserID
	}

	// If still no Slack user ID, try to find by email
	if slackUserID == "" && userClaims.Email != "" {
		token, err := h.DB.GetSlackToken(r.Context(), connectionID)
		if err != nil {
			logger.Error("Failed to get Slack token for user lookup", "error", err)
			InternalError(w, r, err)
			return
		}

		httpClient := &http.Client{Timeout: slackAPITimeout}
		client := slack.New(token, slack.OptionHTTPClient(httpClient))
		slackUser, err := client.GetUserByEmail(userClaims.Email)
		if err != nil {
			logger.Warn("Could not find Slack user by email", "error", err)
			BadRequest(w, r, "Could not find your Slack user. Make sure your email matches your Slack account.")
			return
		}
		slackUserID = slackUser.ID
		logger.Info("Found Slack user by email lookup", "slack_user_id", slackUserID)
	}

	if slackUserID == "" {
		BadRequest(w, r, "No Slack user ID available and no email to look up")
		return
	}

	// Create link
	now := time.Now().UTC()
	link := &db.SlackUserLink{
		ID:                uuid.New().String(),
		UserID:            userClaims.UserID,
		SlackConnectionID: connectionID,
		SlackUserID:       slackUserID,
		DMNotifications:   true, // Default to enabled
		CreatedAt:         now,
	}

	if err := h.DB.CreateSlackUserLink(r.Context(), link); err != nil {
		logger.Error("Failed to create Slack user link", "error", err)
		InternalError(w, r, err)
		return
	}

	logger.Info("Slack user linked",
		"user_id", userClaims.UserID,
		"slack_user_id", slackUserID,
		"connection_id", connectionID,
	)

	WriteCreated(w, r, SlackUserLinkResponse{
		ID:              link.ID,
		SlackUserID:     link.SlackUserID,
		DMNotifications: link.DMNotifications,
		CreatedAt:       link.CreatedAt.Format(time.RFC3339),
	}, "Slack user linked successfully")
}

// unlinkSlackUser removes the link between a user and their Slack identity
func (h *Handler) unlinkSlackUser(w http.ResponseWriter, r *http.Request, connectionID string) {
	logger := loggerWithRequest(r)

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	err := h.DB.DeleteSlackUserLink(r.Context(), userClaims.UserID, connectionID)
	if err != nil {
		if errors.Is(err, db.ErrSlackUserLinkNotFound) {
			NotFound(w, r, "User link not found")
			return
		}
		logger.Error("Failed to delete Slack user link", "error", err)
		InternalError(w, r, err)
		return
	}

	logger.Info("Slack user unlinked", "user_id", userClaims.UserID, "connection_id", connectionID)
	WriteNoContent(w, r)
}

// updateSlackUserNotifications updates notification preferences
func (h *Handler) updateSlackUserNotifications(w http.ResponseWriter, r *http.Request, connectionID string) {
	logger := loggerWithRequest(r)

	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	var req SlackUpdateNotificationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	err := h.DB.UpdateSlackUserLinkNotifications(r.Context(), userClaims.UserID, connectionID, req.DMNotifications)
	if err != nil {
		if errors.Is(err, db.ErrSlackUserLinkNotFound) {
			NotFound(w, r, "User link not found")
			return
		}
		logger.Error("Failed to update notification preferences", "error", err)
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, nil, "Notification preferences updated")
}

// listSlackWorkspaceUsers lists users in a Slack workspace for linking
func (h *Handler) listSlackWorkspaceUsers(w http.ResponseWriter, r *http.Request, connectionID string) {
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
	conn, err := h.DB.GetSlackConnection(r.Context(), connectionID)
	if err != nil {
		if errors.Is(err, db.ErrSlackConnectionNotFound) {
			NotFound(w, r, "Slack connection not found")
			return
		}
		logger.Error("Failed to get Slack connection", "error", err)
		InternalError(w, r, err)
		return
	}

	if orgID == "" || orgID != conn.OrganisationID {
		Forbidden(w, r, "You don't have access to this connection")
		return
	}

	// Get token from Vault
	token, err := h.DB.GetSlackToken(r.Context(), connectionID)
	if err != nil {
		logger.Error("Failed to get access token from vault", "error", err)
		InternalError(w, r, err)
		return
	}

	// List users from Slack with pagination for large workspaces
	httpClient := &http.Client{Timeout: slackAPITimeout}
	client := slack.New(token, slack.OptionHTTPClient(httpClient))

	var users []slack.User
	pager := client.GetUsersPaginated(slack.GetUsersOptionLimit(200))
	for {
		page, err := pager.Next(r.Context())
		if pager.Done(err) {
			break
		}
		if err != nil {
			logger.Error("Failed to fetch Slack users", "error", err)
			InternalError(w, r, fmt.Errorf("failed to fetch Slack users: %w", err))
			return
		}
		users = append(users, page.Users...)
		pager = page
	}

	// Filter to real users (not bots, not deleted)
	type SlackUserInfo struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		RealName    string `json:"real_name"`
		Email       string `json:"email,omitempty"`
		DisplayName string `json:"display_name"`
	}

	result := make([]SlackUserInfo, 0)
	for _, u := range users {
		if u.Deleted || u.IsBot || u.ID == "USLACKBOT" {
			continue
		}
		result = append(result, SlackUserInfo{
			ID:          u.ID,
			Name:        u.Name,
			RealName:    u.RealName,
			Email:       u.Profile.Email,
			DisplayName: u.Profile.DisplayName,
		})
	}

	WriteSuccess(w, r, result, "")
}

// getSlackUserLink returns the current user's link to a Slack connection
func (h *Handler) getSlackUserLink(w http.ResponseWriter, r *http.Request, connectionID string) {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return
	}

	link, err := h.DB.GetSlackUserLink(r.Context(), userClaims.UserID, connectionID)
	if err != nil {
		if errors.Is(err, db.ErrSlackUserLinkNotFound) {
			NotFound(w, r, "User not linked to this Slack connection")
			return
		}
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, SlackUserLinkResponse{
		ID:              link.ID,
		SlackUserID:     link.SlackUserID,
		DMNotifications: link.DMNotifications,
		CreatedAt:       link.CreatedAt.Format(time.RFC3339),
	}, "")
}

// Helper functions

// generateOAuthState and validateOAuthState moved to oauth_utils.go

// getSlackRedirectURI returns the OAuth callback URL for Slack
func getSlackRedirectURI() string {
	if uri := os.Getenv("SLACK_REDIRECT_URI"); uri != "" {
		return uri
	}
	return getAppURL() + "/v1/integrations/slack/callback"
}
