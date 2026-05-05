package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/util"
	"github.com/google/uuid"
)

// WebflowCustomDomain represents a custom domain from the Webflow API v2
type WebflowCustomDomain struct {
	ID            string  `json:"id"`
	URL           string  `json:"url"`
	LastPublished *string `json:"lastPublished,omitempty"`
}

// WebflowSite represents a site from the Webflow API v2
type WebflowSite struct {
	ID            string                `json:"id"`
	WorkspaceID   string                `json:"workspaceId,omitempty"`
	DisplayName   string                `json:"displayName"`
	ShortName     string                `json:"shortName"`
	LastPublished string                `json:"lastPublished,omitempty"`
	LastUpdated   string                `json:"lastUpdated,omitempty"`
	CustomDomains []WebflowCustomDomain `json:"customDomains,omitempty"`
}

// WebflowSitesResponse represents the response from Webflow's list sites API
type WebflowSitesResponse struct {
	Sites []WebflowSite `json:"sites"`
}

// WebflowSiteSettingResponse represents a site with its local settings
type WebflowSiteSettingResponse struct {
	WebflowSiteID         string  `json:"webflow_site_id"`
	SiteName              string  `json:"site_name"`
	PrimaryDomain         string  `json:"primary_domain"`
	LastPublished         string  `json:"last_published,omitempty"`
	ScheduleIntervalHours *int    `json:"schedule_interval_hours"`
	AutoPublishEnabled    bool    `json:"auto_publish_enabled"`
	SchedulerID           *string `json:"scheduler_id,omitempty"`
}

// WebflowSitesListResponse represents the paginated sites list response
type WebflowSitesListResponse struct {
	Sites      []WebflowSiteSettingResponse `json:"sites"`
	Pagination struct {
		Total   int  `json:"total"`
		Page    int  `json:"page"`
		Limit   int  `json:"limit"`
		HasNext bool `json:"has_next"`
	} `json:"pagination"`
}

// UpdateScheduleRequest represents the request body for updating a site's schedule
type UpdateScheduleRequest struct {
	ConnectionID          string `json:"connection_id"`
	ScheduleIntervalHours *int   `json:"schedule_interval_hours"` // nil to remove schedule
}

// UpdateAutoPublishRequest represents the request body for toggling auto-publish
type UpdateAutoPublishRequest struct {
	ConnectionID string `json:"connection_id"`
	Enabled      bool   `json:"enabled"`
}

// webflowSitesRouter routes requests under /v1/integrations/webflow/sites/
func (h *Handler) webflowSitesRouter(w http.ResponseWriter, r *http.Request) {
	// Routes:
	// PUT /v1/integrations/webflow/sites/{site_id}/schedule
	// PUT /v1/integrations/webflow/sites/{site_id}/auto-publish
	path := strings.TrimPrefix(r.URL.Path, "/v1/integrations/webflow/sites/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 {
		BadRequest(w, r, "Invalid path")
		return
	}

	siteID := parts[0]
	action := parts[1]

	// Validate method
	if r.Method != http.MethodPut {
		MethodNotAllowed(w, r)
		return
	}

	switch action {
	case "schedule":
		h.updateSiteSchedule(w, r, siteID)
	case "auto-publish":
		h.toggleSiteAutoPublish(w, r, siteID)
	default:
		NotFound(w, r, "Endpoint not found")
	}
}

// WebflowSitesHandler handles requests to /v1/integrations/webflow/{connection_id}/sites
func (h *Handler) WebflowSitesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	// Extract connection_id from path
	path := strings.TrimPrefix(r.URL.Path, "/v1/integrations/webflow/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "sites" {
		BadRequest(w, r, "Invalid path")
		return
	}

	connectionID := parts[0]
	if _, err := uuid.Parse(connectionID); err != nil {
		BadRequest(w, r, "Invalid connection ID format")
		return
	}

	h.listWebflowSites(w, r, connectionID)
}

// listWebflowSites fetches sites from Webflow API and merges with local settings
func (h *Handler) listWebflowSites(w http.ResponseWriter, r *http.Request, connectionID string) {
	logger := loggerWithRequest(r)
	ctx := r.Context()

	// Get active organisation
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	// Get connection and verify ownership
	conn, err := h.DB.GetWebflowConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, db.ErrWebflowConnectionNotFound) {
			NotFound(w, r, "Webflow connection not found")
			return
		}
		logger.Error("Failed to get Webflow connection", "error", err, "connection_id", connectionID)
		InternalError(w, r, err)
		return
	}

	if conn.OrganisationID != orgID {
		NotFound(w, r, "Webflow connection not found")
		return
	}

	// Get token from vault
	token, err := h.DB.GetWebflowToken(ctx, connectionID)
	if err != nil {
		logger.Error("Failed to get Webflow token", "error", err, "connection_id", connectionID)
		InternalError(w, r, fmt.Errorf("failed to retrieve Webflow token"))
		return
	}

	// Parse query params
	search := r.URL.Query().Get("search")
	page := 1
	limit := 10
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	// Fetch sites from Webflow API
	webflowSites, err := h.fetchWebflowSites(ctx, token)
	if err != nil {
		logger.Error("Failed to fetch Webflow sites", "error", err, "connection_id", connectionID)
		InternalError(w, r, fmt.Errorf("failed to fetch sites from Webflow"))
		return
	}

	// Get local settings for all sites
	localSettings, err := h.DB.ListSiteSettingsByConnection(ctx, connectionID)
	if err != nil {
		logger.Error("Failed to list site settings", "error", err, "connection_id", connectionID)
		InternalError(w, r, err)
		return
	}

	// Build settings map for quick lookup
	settingsMap := make(map[string]*db.WebflowSiteSetting)
	for _, s := range localSettings {
		settingsMap[s.WebflowSiteID] = s
	}

	// Filter by search if provided
	var filteredSites []WebflowSite
	for _, site := range webflowSites {
		if search != "" {
			searchLower := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(site.DisplayName), searchLower) &&
				!strings.Contains(strings.ToLower(site.ShortName), searchLower) &&
				!containsDomain(site.CustomDomains, searchLower) {
				continue
			}
		}
		filteredSites = append(filteredSites, site)
	}

	// Sort by last updated (most recent first) - Webflow API typically returns sorted
	// but we can add explicit sorting if needed

	// Paginate
	total := len(filteredSites)
	start := (page - 1) * limit
	end := start + limit
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	paginatedSites := filteredSites[start:end]

	// Build response with merged settings
	responseItems := make([]WebflowSiteSettingResponse, 0, len(paginatedSites))
	for _, site := range paginatedSites {
		item := WebflowSiteSettingResponse{
			WebflowSiteID: site.ID,
			SiteName:      site.DisplayName,
			PrimaryDomain: getPrimaryDomain(site),
			LastPublished: site.LastPublished,
		}

		// Merge local settings if exist
		if setting, ok := settingsMap[site.ID]; ok {
			item.ScheduleIntervalHours = setting.ScheduleIntervalHours
			item.AutoPublishEnabled = setting.AutoPublishEnabled
			if setting.SchedulerID != "" {
				item.SchedulerID = &setting.SchedulerID
			}
		}

		responseItems = append(responseItems, item)
	}

	response := WebflowSitesListResponse{
		Sites: responseItems,
	}
	response.Pagination.Total = total
	response.Pagination.Page = page
	response.Pagination.Limit = limit
	response.Pagination.HasNext = end < total

	WriteSuccess(w, r, response, "Sites retrieved successfully")
}

// updateSiteSchedule handles PUT /v1/integrations/webflow/sites/{site_id}/schedule
func (h *Handler) updateSiteSchedule(w http.ResponseWriter, r *http.Request, siteID string) {
	logger := loggerWithRequest(r)
	ctx := r.Context()

	// Get active organisation
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	// Parse request body
	var req UpdateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid request body")
		return
	}

	if req.ConnectionID == "" {
		BadRequest(w, r, "connection_id is required")
		return
	}

	// Validate schedule interval if provided
	if req.ScheduleIntervalHours != nil {
		hours := *req.ScheduleIntervalHours
		if hours != 6 && hours != 12 && hours != 24 && hours != 48 {
			BadRequest(w, r, "schedule_interval_hours must be 6, 12, 24, or 48")
			return
		}
	}

	// Verify connection ownership
	conn, err := h.DB.GetWebflowConnection(ctx, req.ConnectionID)
	if err != nil {
		if errors.Is(err, db.ErrWebflowConnectionNotFound) {
			NotFound(w, r, "Webflow connection not found")
			return
		}
		logger.Error("Failed to get Webflow connection", "error", err, "connection_id", req.ConnectionID)
		InternalError(w, r, err)
		return
	}

	if conn.OrganisationID != orgID {
		NotFound(w, r, "Webflow connection not found")
		return
	}

	// Get token and fetch site info from Webflow
	token, err := h.DB.GetWebflowToken(ctx, req.ConnectionID)
	if err != nil {
		logger.Error("Failed to get Webflow token", "error", err, "connection_id", req.ConnectionID)
		InternalError(w, r, fmt.Errorf("failed to retrieve Webflow token"))
		return
	}

	siteInfo, err := h.fetchWebflowSiteByID(ctx, token, siteID)
	if err != nil {
		logger.Error("Failed to fetch site info from Webflow", "error", err, "site_id", siteID)
		InternalError(w, r, fmt.Errorf("failed to fetch site from Webflow"))
		return
	}

	primaryDomain := getPrimaryDomain(*siteInfo)
	if primaryDomain == "" {
		BadRequest(w, r, "Site has no domain configured")
		return
	}

	// Get or create site setting
	setting, err := h.DB.GetSiteSetting(ctx, orgID, siteID)
	if err != nil && !errors.Is(err, db.ErrWebflowSiteSettingNotFound) {
		logger.Error("Failed to get site setting", "error", err, "site_id", siteID)
		InternalError(w, r, err)
		return
	}

	var schedulerID string

	// Handle scheduler creation/update/deletion
	if req.ScheduleIntervalHours != nil {
		// Create or update scheduler
		normalizedDomain := util.NormaliseDomain(primaryDomain)

		// Check if scheduler exists for this domain
		schedulers, err := h.DB.ListSchedulers(ctx, orgID)
		if err != nil {
			logger.Error("Failed to list schedulers", "error", err)
			InternalError(w, r, err)
			return
		}

		var existingScheduler *db.Scheduler
		for _, s := range schedulers {
			domainName, _ := h.DB.GetDomainNameByID(ctx, s.DomainID)
			if domainName == normalizedDomain {
				existingScheduler = s
				break
			}
		}

		if existingScheduler != nil {
			// Update existing scheduler
			existingScheduler.ScheduleIntervalHours = *req.ScheduleIntervalHours
			existingScheduler.NextRunAt = time.Now().Add(time.Duration(*req.ScheduleIntervalHours) * time.Hour)
			if err := h.DB.UpdateScheduler(ctx, existingScheduler.ID, existingScheduler, nil); err != nil {
				logger.Error("Failed to update scheduler", "error", err, "scheduler_id", existingScheduler.ID)
				InternalError(w, r, err)
				return
			}
			schedulerID = existingScheduler.ID
		} else {
			// Get or create domain for the scheduler
			var domainID int
			err := h.DB.GetDB().QueryRowContext(ctx, `
				INSERT INTO domains(name) VALUES($1)
				ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name
				RETURNING id
			`, normalizedDomain).Scan(&domainID)
			if err != nil {
				logger.Error("Failed to get or create domain", "error", err, "domain", normalizedDomain)
				InternalError(w, r, err)
				return
			}

			// Create new scheduler
			newScheduler := &db.Scheduler{
				ID:                    uuid.New().String(),
				OrganisationID:        orgID,
				DomainID:              domainID,
				ScheduleIntervalHours: *req.ScheduleIntervalHours,
				NextRunAt:             time.Now().Add(time.Duration(*req.ScheduleIntervalHours) * time.Hour),
				IsEnabled:             true,
				Concurrency:           20,
				FindLinks:             true,
				MaxPages:              0,
				RequiredWorkers:       1,
			}

			if err := h.DB.CreateScheduler(ctx, newScheduler); err != nil {
				logger.Error("Failed to create scheduler", "error", err, "domain", normalizedDomain)
				InternalError(w, r, err)
				return
			}
			schedulerID = newScheduler.ID
		}
	} else if setting != nil && setting.SchedulerID != "" {
		// Remove schedule - delete scheduler
		if err := h.DB.DeleteScheduler(ctx, setting.SchedulerID); err != nil {
			logger.Warn("Failed to delete scheduler (may already be deleted)", "error", err, "scheduler_id", setting.SchedulerID)
		}
		schedulerID = ""
	}

	// Create or update site setting
	if setting == nil {
		setting = &db.WebflowSiteSetting{
			ConnectionID:   req.ConnectionID,
			OrganisationID: orgID,
			WebflowSiteID:  siteID,
			SiteName:       siteInfo.DisplayName,
			PrimaryDomain:  primaryDomain,
		}
	}

	setting.ScheduleIntervalHours = req.ScheduleIntervalHours
	setting.SchedulerID = schedulerID

	if err := h.DB.CreateOrUpdateSiteSetting(ctx, setting); err != nil {
		logger.Error("Failed to save site setting", "error", err, "site_id", siteID)
		InternalError(w, r, err)
		return
	}

	// Trigger immediate job when creating or updating a schedule
	if req.ScheduleIntervalHours != nil {
		logger.Info("Creating immediate job for schedule setup",
			"site_id", siteID,
			"domain", primaryDomain,
			"interval_hours", *req.ScheduleIntervalHours,
		)

		// Get user from context (already validated by auth middleware)
		user, _, ok := h.GetActiveOrganisationWithUser(w, r)
		if !ok {
			// Auth already handled, but safety check
			logger.Warn("Could not get user for immediate job creation")
		} else {
			sourceType := "schedule_setup"
			sourceDetail := fmt.Sprintf("Schedule enabled (%dh)", *req.ScheduleIntervalHours)
			sourceInfoPayload := struct {
				Trigger       string `json:"trigger"`
				SiteID        string `json:"site_id"`
				SiteName      string `json:"site_name"`
				IntervalHours int    `json:"interval_hours"`
				SchedulerID   string `json:"scheduler_id"`
				EnabledAt     string `json:"enabled_at"`
			}{
				Trigger:       "schedule_enabled",
				SiteID:        siteID,
				SiteName:      siteInfo.DisplayName,
				IntervalHours: *req.ScheduleIntervalHours,
				SchedulerID:   schedulerID,
				EnabledAt:     time.Now().UTC().Format(time.RFC3339),
			}
			sourceInfoBytes, err := json.Marshal(sourceInfoPayload)
			if err != nil {
				logger.Warn("Failed to marshal source info for immediate job", "error", err)
			}
			sourceInfo := string(sourceInfoBytes)

			jobReq := CreateJobRequest{
				Domain:       primaryDomain,
				SourceType:   &sourceType,
				SourceDetail: &sourceDetail,
				SourceInfo:   &sourceInfo,
			}

			_, err = h.createJobFromRequest(ctx, user, jobReq, loggerWithRequest(r))
			if err != nil {
				logger.Warn("Failed to create immediate job, user can trigger manually", "error", err)
				// Don't fail the entire request - scheduler is configured successfully
			} else {
				logger.Info("Immediate job created successfully", "domain", primaryDomain, "scheduler_id", schedulerID)
			}
		}
	}

	// Build response
	response := WebflowSiteSettingResponse{
		WebflowSiteID:         siteID,
		SiteName:              siteInfo.DisplayName,
		PrimaryDomain:         primaryDomain,
		ScheduleIntervalHours: setting.ScheduleIntervalHours,
		AutoPublishEnabled:    setting.AutoPublishEnabled,
	}
	if schedulerID != "" {
		response.SchedulerID = &schedulerID
	}

	WriteSuccess(w, r, response, "Schedule updated successfully")
}

// toggleSiteAutoPublish handles PUT /v1/integrations/webflow/sites/{site_id}/auto-publish
func (h *Handler) toggleSiteAutoPublish(w http.ResponseWriter, r *http.Request, siteID string) {
	logger := loggerWithRequest(r)
	ctx := r.Context()

	// Get active organisation
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	// Parse request body
	var req UpdateAutoPublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid request body")
		return
	}

	if req.ConnectionID == "" {
		BadRequest(w, r, "connection_id is required")
		return
	}

	// Verify connection ownership
	conn, err := h.DB.GetWebflowConnection(ctx, req.ConnectionID)
	if err != nil {
		if errors.Is(err, db.ErrWebflowConnectionNotFound) {
			NotFound(w, r, "Webflow connection not found")
			return
		}
		logger.Error("Failed to get Webflow connection", "error", err, "connection_id", req.ConnectionID)
		InternalError(w, r, err)
		return
	}

	if conn.OrganisationID != orgID {
		NotFound(w, r, "Webflow connection not found")
		return
	}

	// Get token
	token, err := h.DB.GetWebflowToken(ctx, req.ConnectionID)
	if err != nil {
		logger.Error("Failed to get Webflow token", "error", err, "connection_id", req.ConnectionID)
		InternalError(w, r, fmt.Errorf("failed to retrieve Webflow token"))
		return
	}

	// Get site info from Webflow
	siteInfo, err := h.fetchWebflowSiteByID(ctx, token, siteID)
	if err != nil {
		logger.Error("Failed to fetch site info from Webflow", "error", err, "site_id", siteID)
		InternalError(w, r, fmt.Errorf("failed to fetch site from Webflow"))
		return
	}

	primaryDomain := getPrimaryDomain(*siteInfo)

	// Get existing setting
	setting, err := h.DB.GetSiteSetting(ctx, orgID, siteID)
	if err != nil && !errors.Is(err, db.ErrWebflowSiteSettingNotFound) {
		logger.Error("Failed to get site setting", "error", err, "site_id", siteID)
		InternalError(w, r, err)
		return
	}

	var webhookID string

	if req.Enabled {
		if strings.TrimSpace(conn.WebflowWorkspaceID) == "" {
			logger.Warn("Blocked enabling auto-publish: missing workspace ID on connection", "connection_id", req.ConnectionID)
			BadRequest(w, r, "Webflow workspace not available for this connection. Reconnect to Webflow and ensure your workspace is accessible.")
			return
		}
		// Register webhook with Webflow
		webhookURL := fmt.Sprintf("%s/v1/webhooks/webflow/workspaces/%s", getAppURL(), conn.WebflowWorkspaceID)
		newWebhookID, err := h.registerWebflowWebhook(ctx, token, siteID, webhookURL)
		if err != nil {
			logger.Error("Failed to register webhook with Webflow", "error", err, "site_id", siteID)
			InternalError(w, r, fmt.Errorf("failed to register webhook with Webflow"))
			return
		}
		webhookID = newWebhookID
	} else if setting != nil && setting.WebhookID != "" {
		// Delete webhook from Webflow
		if err := h.deleteWebflowWebhook(ctx, token, setting.WebhookID); err != nil {
			logger.Warn("Failed to delete webhook from Webflow (may already be deleted)", "error", err, "webhook_id", setting.WebhookID)
			// Continue anyway - we'll clear our local record
		}
		webhookID = ""
	}

	// Create or update site setting
	if setting == nil {
		setting = &db.WebflowSiteSetting{
			ConnectionID:   req.ConnectionID,
			OrganisationID: orgID,
			WebflowSiteID:  siteID,
			SiteName:       siteInfo.DisplayName,
			PrimaryDomain:  primaryDomain,
		}
	}

	setting.AutoPublishEnabled = req.Enabled
	setting.WebhookID = webhookID
	if req.Enabled {
		now := time.Now()
		setting.WebhookRegisteredAt = &now
	} else {
		setting.WebhookRegisteredAt = nil
	}

	if err := h.DB.CreateOrUpdateSiteSetting(ctx, setting); err != nil {
		logger.Error("Failed to save site setting", "error", err, "site_id", siteID)
		InternalError(w, r, err)
		return
	}

	// Trigger immediate job when enabling auto-publish
	if req.Enabled && webhookID != "" {
		logger.Info("Creating immediate job for newly enabled auto-publish",
			"site_id", siteID,
			"domain", primaryDomain,
		)

		// Get user from context (already validated by auth middleware)
		user, _, ok := h.GetActiveOrganisationWithUser(w, r)
		if !ok {
			// Auth already handled, but safety check
			logger.Warn("Could not get user for immediate job creation")
		} else {
			sourceType := "auto_publish_setup"
			sourceDetail := "Auto-publish enabled"
			sourceInfoPayload := struct {
				Trigger   string `json:"trigger"`
				SiteID    string `json:"site_id"`
				SiteName  string `json:"site_name"`
				EnabledAt string `json:"enabled_at"`
			}{
				Trigger:   "auto_publish_enabled",
				SiteID:    siteID,
				SiteName:  siteInfo.DisplayName,
				EnabledAt: time.Now().UTC().Format(time.RFC3339),
			}
			sourceInfoBytes, err := json.Marshal(sourceInfoPayload)
			if err != nil {
				logger.Warn("Failed to marshal source info for immediate job", "error", err)
			}
			sourceInfo := string(sourceInfoBytes)

			jobReq := CreateJobRequest{
				Domain:       primaryDomain,
				SourceType:   &sourceType,
				SourceDetail: &sourceDetail,
				SourceInfo:   &sourceInfo,
			}

			_, err = h.createJobFromRequest(ctx, user, jobReq, loggerWithRequest(r))
			if err != nil {
				logger.Warn("Failed to create immediate job, user can trigger manually", "error", err)
				// Don't fail the entire request - webhook is registered successfully
			} else {
				logger.Info("Immediate job created successfully", "domain", primaryDomain)
			}
		}
	}

	// Build response
	response := WebflowSiteSettingResponse{
		WebflowSiteID:         siteID,
		SiteName:              siteInfo.DisplayName,
		PrimaryDomain:         primaryDomain,
		ScheduleIntervalHours: setting.ScheduleIntervalHours,
		AutoPublishEnabled:    setting.AutoPublishEnabled,
	}
	if setting.SchedulerID != "" {
		response.SchedulerID = &setting.SchedulerID
	}

	WriteSuccess(w, r, response, "Auto-publish updated successfully")
}

// fetchWebflowSites fetches all sites from the Webflow API
func (h *Handler) fetchWebflowSites(ctx context.Context, token string) ([]WebflowSite, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.webflow.com/v2/sites", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call Webflow API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("webflow token expired or invalid")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webflow API returned status %d", resp.StatusCode)
	}

	var sitesResp WebflowSitesResponse
	if err := json.NewDecoder(resp.Body).Decode(&sitesResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return sitesResp.Sites, nil
}

// fetchWebflowSiteByID fetches a single site from the Webflow API
func (h *Handler) fetchWebflowSiteByID(ctx context.Context, token, siteID string) (*WebflowSite, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	reqURL := fmt.Sprintf("https://api.webflow.com/v2/sites/%s", siteID)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil) //nolint:gosec // G704: reqURL targets api.webflow.com
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req) //nolint:gosec // G704: request targets api.webflow.com
	if err != nil {
		return nil, fmt.Errorf("failed to call Webflow API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("webflow token expired or invalid")
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("site not found")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webflow API returned status %d", resp.StatusCode)
	}

	var site WebflowSite
	if err := json.NewDecoder(resp.Body).Decode(&site); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &site, nil
}

// registerWebflowWebhook registers a webhook with Webflow and returns the webhook ID
func (h *Handler) registerWebflowWebhook(ctx context.Context, token, siteID, webhookURL string) (string, error) {
	payload := map[string]string{
		"triggerType": "site_publish",
		"url":         webhookURL,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	reqURL := fmt.Sprintf("https://api.webflow.com/v2/sites/%s/webhooks", siteID)
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(body)) //nolint:gosec // G704: reqURL targets api.webflow.com
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // G704: request targets api.webflow.com
	if err != nil {
		return "", fmt.Errorf("failed to call Webflow API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// Try to parse error for conflict (webhook already exists)
		var errResp struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.NewDecoder(resp.Body).Decode(&errResp) == nil {
			if strings.Contains(strings.ToLower(errResp.Message), "already exists") ||
				strings.Contains(strings.ToLower(errResp.Message), "duplicate") {
				// Webhook already exists - try to find it
				return h.findExistingWebhook(ctx, token, siteID, webhookURL)
			}
		}
		return "", fmt.Errorf("webflow API returned status %d", resp.StatusCode)
	}

	var webhookResp struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&webhookResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return webhookResp.ID, nil
}

// findExistingWebhook finds an existing webhook for a site
func (h *Handler) findExistingWebhook(ctx context.Context, token, siteID, webhookURL string) (string, error) {
	reqURL := fmt.Sprintf("https://api.webflow.com/v2/sites/%s/webhooks", siteID)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil) //nolint:gosec // G704: reqURL targets api.webflow.com
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // G704: request targets api.webflow.com
	if err != nil {
		return "", fmt.Errorf("failed to call Webflow API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("webflow API returned status %d", resp.StatusCode)
	}

	var webhooksResp struct {
		Webhooks []struct {
			ID          string `json:"id"`
			TriggerType string `json:"triggerType"`
			URL         string `json:"url"`
		} `json:"webhooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&webhooksResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	for _, wh := range webhooksResp.Webhooks {
		if wh.TriggerType == "site_publish" && wh.URL == webhookURL {
			return wh.ID, nil
		}
	}

	return "", fmt.Errorf("webhook not found")
}

// deleteWebflowWebhook deletes a webhook from Webflow
func (h *Handler) deleteWebflowWebhook(ctx context.Context, token, webhookID string) error {
	reqURL := fmt.Sprintf("https://api.webflow.com/v2/webhooks/%s", webhookID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", reqURL, nil) //nolint:gosec // G704: reqURL targets api.webflow.com
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // G704: request targets api.webflow.com
	if err != nil {
		return fmt.Errorf("failed to call Webflow API: %w", err)
	}
	defer resp.Body.Close()

	// 200, 202, or 204 are all success
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	// 404 means already deleted - that's fine
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	return fmt.Errorf("webflow API returned status %d", resp.StatusCode)
}

// Helper functions

func containsDomain(domains []WebflowCustomDomain, search string) bool {
	for _, d := range domains {
		if strings.Contains(strings.ToLower(d.URL), search) {
			return true
		}
	}
	return false
}

func getPrimaryDomain(site WebflowSite) string {
	// Prefer custom domains over default webflow.io subdomain
	for _, d := range site.CustomDomains {
		if d.URL != "" && !strings.Contains(d.URL, "webflow.io") {
			return d.URL
		}
	}
	// Fall back to first custom domain (may be webflow.io subdomain)
	if len(site.CustomDomains) > 0 && site.CustomDomains[0].URL != "" {
		return site.CustomDomains[0].URL
	}
	// Fall back to constructed default domain from shortName
	if site.ShortName != "" {
		return site.ShortName + ".webflow.io"
	}
	return ""
}
