package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/jobs"
	"github.com/good-native/hover/internal/logging"
	"github.com/good-native/hover/internal/util"
)

// JobsHandler handles requests to /v1/jobs
func (h *Handler) JobsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listJobs(w, r)
	case http.MethodPost:
		h.createJob(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

// JobHandler handles requests to /v1/jobs/:id
func (h *Handler) JobHandler(w http.ResponseWriter, r *http.Request) {
	// Extract job ID from path
	path := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
	if path == "" {
		BadRequest(w, r, "Job ID is required")
		return
	}

	// Handle sub-routes like /v1/jobs/:id/tasks
	parts := strings.Split(path, "/")
	jobID := parts[0]

	if len(parts) > 1 {
		// Handle sub-routes
		switch parts[1] {
		case "tasks":
			h.getJobTasks(w, r, jobID)
			return
		case "export":
			h.exportJobTasks(w, r, jobID)
			return
		case "cancel":
			if r.Method == http.MethodPost {
				h.cancelJob(w, r, jobID)
				return
			}
			MethodNotAllowed(w, r)
			return
		case "share-links":
			if len(parts) == 2 {
				switch r.Method {
				case http.MethodPost:
					h.createJobShareLink(w, r, jobID)
					return
				case http.MethodGet:
					h.getJobShareLink(w, r, jobID)
					return
				}
				MethodNotAllowed(w, r)
				return
			}

			if len(parts) == 3 {
				token := parts[2]
				if r.Method == http.MethodDelete {
					h.revokeJobShareLink(w, r, jobID, token)
					return
				}
				MethodNotAllowed(w, r)
				return
			}

			NotFound(w, r, "Endpoint not found")
			return
		default:
			NotFound(w, r, "Endpoint not found")
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		h.getJob(w, r, jobID)
	case http.MethodPut:
		h.updateJob(w, r, jobID)
	case http.MethodDelete:
		h.cancelJob(w, r, jobID)
	default:
		MethodNotAllowed(w, r)
	}
}

// CreateJobRequest represents the request body for creating a job
type CreateJobRequest struct {
	Domain                   string  `json:"domain"`
	UseSitemap               *bool   `json:"use_sitemap,omitempty"`
	FindLinks                *bool   `json:"find_links,omitempty"`
	AllowCrossSubdomainLinks *bool   `json:"allow_cross_subdomain_links,omitempty"`
	Concurrency              *int    `json:"concurrency,omitempty"`
	MaxPages                 *int    `json:"max_pages,omitempty"`
	SourceType               *string `json:"source_type,omitempty"`
	SourceDetail             *string `json:"source_detail,omitempty"`
	SourceInfo               *string `json:"source_info,omitempty"`
}

// JobResponse represents a job in API responses
type JobResponse struct {
	ID             string  `json:"id"`
	DomainID       int     `json:"domain_id"`
	Domain         string  `json:"domain"`
	Status         string  `json:"status"`
	TotalTasks     int     `json:"total_tasks"`
	CompletedTasks int     `json:"completed_tasks"`
	FailedTasks    int     `json:"failed_tasks"`
	SkippedTasks   int     `json:"skipped_tasks"`
	Progress       float64 `json:"progress"`
	CreatedAt      string  `json:"created_at"`
	StartedAt      *string `json:"started_at,omitempty"`
	CompletedAt    *string `json:"completed_at,omitempty"`
	// Additional fields for dashboard
	DurationSeconds       *int           `json:"duration_seconds,omitempty"`
	AvgTimePerTaskSeconds *float64       `json:"avg_time_per_task_seconds,omitempty"`
	Stats                 map[string]any `json:"stats,omitempty"`
	SchedulerID           *string        `json:"scheduler_id,omitempty"`
	// Job configuration fields
	Concurrency          int     `json:"concurrency"`
	MaxPages             int     `json:"max_pages"`
	SourceType           *string `json:"source_type,omitempty"`
	CrawlDelaySeconds    *int    `json:"crawl_delay_seconds,omitempty"`
	AdaptiveDelaySeconds int     `json:"adaptive_delay_seconds"`
}

// listJobs handles GET /v1/jobs
func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	// Parse query parameters
	limit := 10 // default
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 100 {
			limit = parsedLimit
		}
	}

	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if parsedOffset, err := strconv.Atoi(offsetStr); err == nil && parsedOffset >= 0 {
			offset = parsedOffset
		}
	}

	status := r.URL.Query().Get("status")        // Optional status filter
	dateRange := r.URL.Query().Get("range")      // Optional date range filter
	tzOffsetStr := r.URL.Query().Get("tzOffset") // Optional timezone offset in minutes
	include := r.URL.Query().Get("include")      // Optional includes (domain, progress, stats, etc.)

	// Parse timezone offset (default to 0 for UTC)
	tzOffset := 0
	if tzOffsetStr != "" {
		if parsed, err := strconv.Atoi(tzOffsetStr); err == nil {
			tzOffset = parsed
		}
	}

	// Get jobs from database
	includeStats := includeContains(include, "stats")

	jobs, total, err := h.DB.ListJobsWithOffset(orgID, limit, offset, status, dateRange, tzOffset, includeStats)
	if err != nil {
		if HandlePoolSaturation(w, r, err) {
			return
		}
		logger.Error("Failed to list jobs", "error", err, "organisation_id", orgID)
		DatabaseError(w, r, err)
		return
	}

	// Calculate pagination info
	hasNext := offset+limit < total
	hasPrev := offset > 0

	// Prepare response
	response := map[string]any{
		"jobs": jobs,
		"pagination": map[string]any{
			"limit":    limit,
			"offset":   offset,
			"total":    total,
			"has_next": hasNext,
			"has_prev": hasPrev,
		},
	}

	if include != "" {
		// Add additional data based on include parameter
		response["include"] = include
	}

	WriteSuccess(w, r, response, "Jobs retrieved successfully")
}

func includeContains(include, key string) bool {
	if include == "" || key == "" {
		return false
	}

	for _, part := range strings.Split(include, ",") {
		if strings.TrimSpace(strings.ToLower(part)) == strings.ToLower(key) {
			return true
		}
	}

	return false
}

// createJobFromRequest creates a job from a CreateJobRequest with user context
func (h *Handler) createJobFromRequest(ctx context.Context, user *db.User, req CreateJobRequest, logger *logging.Logger) (*jobs.Job, error) {
	// Set defaults
	useSitemap := true
	if req.UseSitemap != nil {
		useSitemap = *req.UseSitemap
	}

	findLinks := true
	if req.FindLinks != nil {
		findLinks = *req.FindLinks
	}

	allowCrossSubdomainLinks := true
	if req.AllowCrossSubdomainLinks != nil {
		allowCrossSubdomainLinks = *req.AllowCrossSubdomainLinks
	}

	concurrency := 20 // Default concurrency
	if req.Concurrency != nil && *req.Concurrency > 0 {
		concurrency = min(*req.Concurrency, 100)
	}

	maxPages := 0
	if req.MaxPages != nil {
		maxPages = *req.MaxPages
	}

	// Use effective organisation (active org takes precedence over legacy org)
	effectiveOrgID := h.DB.GetEffectiveOrganisationID(user)
	var orgIDPtr *string
	if effectiveOrgID != "" {
		orgIDPtr = &effectiveOrgID
	}

	opts := &jobs.JobOptions{
		Domain:                   req.Domain,
		UserID:                   &user.ID,
		OrganisationID:           orgIDPtr,
		UseSitemap:               useSitemap,
		Concurrency:              concurrency,
		FindLinks:                findLinks,
		AllowCrossSubdomainLinks: allowCrossSubdomainLinks,
		MaxPages:                 maxPages,
		SourceType:               req.SourceType,
		SourceDetail:             req.SourceDetail,
		SourceInfo:               req.SourceInfo,
	}

	// Trigger GA4 data fetch in background if findLinks is enabled and organisation has GA4 connection
	// GA4 data will be fetched and pages table updated, then tasks will be reprioritised
	if findLinks && effectiveOrgID != "" && h.GoogleClientID != "" && h.GoogleClientSecret != "" {
		go func() { //nolint:gosec // G118: intentionally outlives request; background GA4 data fetch
			logger.Info("Triggering GA4 data fetch in background", "organisation_id", effectiveOrgID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			h.fetchGA4DataBeforeJob(ctx, logger, effectiveOrgID, req.Domain)
		}()
	} else {
		logger.Debug("Skipping GA4 fetch - conditions not met",
			"find_links", findLinks,
			"organisation_id", effectiveOrgID,
			"has_client_id", h.GoogleClientID != "",
			"has_client_secret", h.GoogleClientSecret != "",
		)
	}

	return h.JobsManager.CreateJob(ctx, opts)
}

// createJob handles POST /v1/jobs
func (h *Handler) createJob(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	// Get user and active organisation (validates auth and membership)
	user, _, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return // Error already written
	}

	_ = logger

	var req CreateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	if req.Domain == "" {
		BadRequest(w, r, "Domain is required")
		return
	}

	// Validate domain format
	if err := util.ValidateDomain(req.Domain); err != nil {
		BadRequest(w, r, fmt.Sprintf("Invalid domain: %s", err.Error()))
		return
	}

	// Set source information if not provided (dashboard creation)
	if req.SourceType == nil {
		sourceType := "dashboard"
		req.SourceType = &sourceType
	}
	if req.SourceDetail == nil {
		sourceDetail := "create_job"
		req.SourceDetail = &sourceDetail
	}
	if req.SourceInfo == nil {
		sourceInfoData := map[string]any{
			"ip":        util.GetClientIP(r),
			"userAgent": r.UserAgent(),
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"endpoint":  r.URL.Path,
			"method":    r.Method,
		}
		sourceInfoBytes, _ := json.Marshal(sourceInfoData)
		sourceInfo := string(sourceInfoBytes)
		req.SourceInfo = &sourceInfo
	}

	job, err := h.createJobFromRequest(r.Context(), user, req, logger)
	if err != nil {
		if HandlePoolSaturation(w, r, err) {
			return
		}
		logger.Error("Failed to create job", "error", err)
		InternalError(w, r, err)
		return
	}

	// Look up the domain ID for the response
	domainID, err := h.DB.GetOrCreateDomainID(r.Context(), job.Domain)
	if err != nil {
		logger.Error("Failed to get domain ID", "error", err, "job_id", job.ID, "domain_id", domainID)
		// Continue without domain_id rather than failing the whole request
		domainID = 0
	}

	response := JobResponse{
		ID:             job.ID,
		DomainID:       domainID,
		Domain:         job.Domain,
		Status:         string(job.Status),
		TotalTasks:     job.TotalTasks,
		CompletedTasks: job.CompletedTasks,
		FailedTasks:    job.FailedTasks,
		SkippedTasks:   job.SkippedTasks,
		Progress:       0.0,
		CreatedAt:      job.CreatedAt.Format(time.RFC3339),
	}

	WriteCreated(w, r, response, "Job created successfully")
}

// getJob handles GET /v1/jobs/:id
func (h *Handler) getJob(w http.ResponseWriter, r *http.Request, jobID string) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	_ = logger // logger available for future use

	response, err := h.fetchJobResponse(r.Context(), jobID, &orgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			NotFound(w, r, "Job not found")
			return
		}
		if HandlePoolSaturation(w, r, err) {
			return
		}

		logger.Error("Failed to fetch job details", "error", err, "job_id", jobID)
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, response, "Job retrieved successfully")
}

func (h *Handler) fetchJobResponse(ctx context.Context, jobID string, organisationID *string) (JobResponse, error) {
	var total, completed, failed, skipped int
	var status, domain string
	var domainID int
	var createdAt, startedAt, completedAt sql.NullTime
	var durationSeconds sql.NullInt64
	var avgTimePerTaskSeconds sql.NullFloat64
	var statsJSON []byte
	var schedulerID sql.NullString
	var concurrency, maxPages, adaptiveDelaySeconds int
	var sourceType sql.NullString
	var crawlDelaySeconds sql.NullInt64

	query := `
		SELECT j.total_tasks, j.completed_tasks, j.failed_tasks, j.skipped_tasks, j.status,
		       d.id as domain_id, d.name as domain, j.created_at, j.started_at, j.completed_at,
		       EXTRACT(EPOCH FROM (j.completed_at - j.started_at))::INTEGER as duration_seconds,
		       CASE WHEN j.completed_tasks > 0 THEN
		           EXTRACT(EPOCH FROM (j.completed_at - j.started_at)) / j.completed_tasks
		       END as avg_time_per_task_seconds,
		       j.stats, j.scheduler_id,
		       j.concurrency, j.max_pages, j.source_type,
		       d.crawl_delay_seconds, d.adaptive_delay_seconds
		FROM jobs j
		JOIN domains d ON j.domain_id = d.id
		WHERE j.id = $1`

	args := []any{jobID}
	if organisationID != nil {
		query += ` AND j.organisation_id = $2`
		args = append(args, *organisationID)
	}

	row := h.DB.GetDB().QueryRowContext(ctx, query, args...)
	err := row.Scan(
		// Task counts
		&total, &completed, &failed, &skipped,
		// Job info
		&status, &domainID, &domain, &createdAt, &startedAt, &completedAt,
		// Computed metrics
		&durationSeconds, &avgTimePerTaskSeconds, &statsJSON, &schedulerID,
		// Job config
		&concurrency, &maxPages, &sourceType,
		// Domain delays
		&crawlDelaySeconds, &adaptiveDelaySeconds,
	)
	if err != nil {
		return JobResponse{}, err
	}

	progress := 0.0
	if total > skipped {
		progress = float64(completed+failed) / float64(total-skipped) * 100
	}

	response := JobResponse{
		ID:                   jobID,
		DomainID:             domainID,
		Domain:               domain,
		Status:               status,
		TotalTasks:           total,
		CompletedTasks:       completed,
		FailedTasks:          failed,
		SkippedTasks:         skipped,
		Progress:             progress,
		Concurrency:          concurrency,
		MaxPages:             maxPages,
		AdaptiveDelaySeconds: adaptiveDelaySeconds,
	}
	if sourceType.Valid {
		response.SourceType = &sourceType.String
	}
	if crawlDelaySeconds.Valid {
		delay := int(crawlDelaySeconds.Int64)
		response.CrawlDelaySeconds = &delay
	}
	if schedulerID.Valid {
		response.SchedulerID = &schedulerID.String
	}

	if durationSeconds.Valid {
		duration := int(durationSeconds.Int64)
		response.DurationSeconds = &duration
	}
	if avgTimePerTaskSeconds.Valid {
		avgTime := avgTimePerTaskSeconds.Float64
		response.AvgTimePerTaskSeconds = &avgTime
	}

	if len(statsJSON) > 0 {
		var stats map[string]any
		if err := json.Unmarshal(statsJSON, &stats); err == nil {
			response.Stats = stats
		}
	}

	if createdAt.Valid {
		response.CreatedAt = createdAt.Time.Format(time.RFC3339)
	} else {
		response.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if startedAt.Valid {
		started := startedAt.Time.Format(time.RFC3339)
		response.StartedAt = &started
	}
	if completedAt.Valid {
		completed := completedAt.Time.Format(time.RFC3339)
		response.CompletedAt = &completed
	}

	return response, nil
}

// JobActionRequest represents actions that can be performed on jobs
type JobActionRequest struct {
	Action string `json:"action"`
}

// updateJob handles PUT /v1/jobs/:id for job actions
func (h *Handler) updateJob(w http.ResponseWriter, r *http.Request, jobID string) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	activeOrgID := h.GetActiveOrganisation(w, r)
	if activeOrgID == "" {
		return // Error already written
	}

	var req JobActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	// Verify job belongs to user's active organisation
	var jobOrgID string
	err := h.DB.GetDB().QueryRowContext(r.Context(), `
		SELECT organisation_id FROM jobs WHERE id = $1
	`, jobID).Scan(&jobOrgID)

	if err != nil {
		NotFound(w, r, "Job not found")
		return
	}

	if activeOrgID != jobOrgID {
		Unauthorised(w, r, "Job access denied")
		return
	}

	_ = logger // logger available for future use

	resultJobID := jobID
	switch req.Action {
	case "cancel":
		err = h.JobsManager.CancelJob(r.Context(), jobID)
	default:
		BadRequest(w, r, "Invalid action. Supported actions: cancel")
		return
	}

	if err != nil {
		logger.Error("Failed to perform job action", "error", err, "job_id", jobID, "action", req.Action)
		InternalError(w, r, err)
		return
	}

	response, err := h.fetchJobResponse(r.Context(), resultJobID, &activeOrgID)
	if err != nil {
		logger.Error("Failed to fetch job after action", "error", err, "job_id", resultJobID)
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, response, "Job cancelled successfully")
}

// cancelJob handles DELETE /v1/jobs/:id
func (h *Handler) cancelJob(w http.ResponseWriter, r *http.Request, jobID string) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	activeOrgID := h.GetActiveOrganisation(w, r)
	if activeOrgID == "" {
		return // Error already written
	}

	// Verify job belongs to user's active organisation
	var jobOrgID string
	err := h.DB.GetDB().QueryRowContext(r.Context(), `
		SELECT organisation_id FROM jobs WHERE id = $1
	`, jobID).Scan(&jobOrgID)

	if err != nil {
		NotFound(w, r, "Job not found")
		return
	}

	if activeOrgID != jobOrgID {
		Unauthorised(w, r, "Job access denied")
		return
	}

	_ = logger // logger available for future use

	err = h.JobsManager.CancelJob(r.Context(), jobID)
	if err != nil {
		logger.Error("Failed to cancel job", "error", err, "job_id", jobID)
		InternalError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]string{"id": jobID, "status": "cancelled"}, "Job cancelled successfully")
}

// TaskQueryParams holds parameters for task listing queries
type TaskQueryParams struct {
	Limit             int
	Offset            int
	Status            string
	CacheFilter       string
	PathFilter        string
	PerformanceFilter string // "slow" (>1500ms) or "very_slow" (>4000ms)
	OrderBy           string
}

// parseTaskQueryParams extracts and validates query parameters for task listing
func parseTaskQueryParams(r *http.Request) TaskQueryParams {
	// Parse limit parameter
	limit := 50 // default
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 200 {
			limit = parsedLimit
		}
	}

	// Parse offset parameter
	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if parsedOffset, err := strconv.Atoi(offsetStr); err == nil && parsedOffset >= 0 {
			offset = parsedOffset
		}
	}

	// Parse status filter
	status := r.URL.Query().Get("status")     // Optional status filter
	cacheFilter := r.URL.Query().Get("cache") // Optional cache filter (hit/miss)
	pathFilter := r.URL.Query().Get("path")   // Optional path keyword filter

	// Parse performance filter: "slow" (>1500ms) or "very_slow" (>4000ms)
	performanceFilter := r.URL.Query().Get("performance")
	if performanceFilter != "slow" && performanceFilter != "very_slow" {
		performanceFilter = ""
	}

	// Parse sort parameter
	sortParam := r.URL.Query().Get("sort") // Optional sort parameter
	orderBy := "t.created_at DESC"         // default
	if sortParam != "" {
		// Handle sort direction prefix
		var direction string
		var column string
		if strings.HasPrefix(sortParam, "-") {
			direction = "DESC"
			column = strings.TrimPrefix(sortParam, "-")
		} else {
			direction = "ASC"
			column = sortParam
		}

		// Map column names to actual SQL columns
		switch column {
		case "path":
			orderBy = "p.path " + direction
		case "status":
			orderBy = "t.status " + direction
		case "response_time":
			orderBy = "t.response_time " + direction + " NULLS LAST"
		case "cache_status":
			orderBy = "t.cache_status " + direction + " NULLS LAST"
		case "second_response_time":
			orderBy = "t.second_response_time " + direction + " NULLS LAST"
		case "status_code":
			orderBy = "t.status_code " + direction + " NULLS LAST"
		case "page_views_7d":
			orderBy = "pa.page_views_7d " + direction + " NULLS LAST"
		case "page_views_28d":
			orderBy = "pa.page_views_28d " + direction + " NULLS LAST"
		case "page_views_180d":
			orderBy = "pa.page_views_180d " + direction + " NULLS LAST"
		case "created_at":
			orderBy = "t.created_at " + direction
		default:
			orderBy = "t.created_at DESC" // fallback to default
		}
	}

	return TaskQueryParams{
		Limit:             limit,
		Offset:            offset,
		Status:            status,
		CacheFilter:       cacheFilter,
		PathFilter:        pathFilter,
		PerformanceFilter: performanceFilter,
		OrderBy:           orderBy,
	}
}

// validateJobAccess validates user authentication and job access permissions
// Returns the user if validation succeeds, or writes HTTP error and returns nil
func (h *Handler) validateJobAccess(w http.ResponseWriter, r *http.Request, jobID string) *db.User {
	// Get user and active organisation (validates auth and membership)
	user, activeOrgID, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return nil // Error already written
	}

	// Verify job belongs to user's active organisation
	var jobOrgID string
	err := h.DB.GetDB().QueryRowContext(r.Context(), `
		SELECT organisation_id FROM jobs WHERE id = $1
	`, jobID).Scan(&jobOrgID)

	if err != nil {
		NotFound(w, r, "Job not found")
		return nil
	}

	if activeOrgID != jobOrgID {
		Unauthorised(w, r, "Job access denied")
		return nil
	}

	return user
}

// TaskQueryBuilder holds the SQL queries and arguments for task retrieval
type TaskQueryBuilder struct {
	SelectQuery string
	CountQuery  string
	Args        []any
}

// buildTaskQuery constructs SQL queries for task retrieval with filters and pagination
func buildTaskQuery(jobID string, params TaskQueryParams) TaskQueryBuilder {
	baseQuery := `
		SELECT t.id, t.job_id, p.path, COALESCE(t.host, d.name) as host, d.name as domain, t.status, t.status_code, t.response_time,
		       t.cache_status, t.second_response_time, t.second_cache_status, t.content_type, t.error, t.source_type, t.source_url,
		       t.created_at, t.started_at, t.completed_at, t.retry_count,
		       pa.page_views_7d, pa.page_views_28d, pa.page_views_180d
		FROM tasks t
		JOIN pages p ON t.page_id = p.id
		JOIN jobs j ON t.job_id = j.id
		JOIN domains d ON j.domain_id = d.id
		LEFT JOIN page_analytics pa ON pa.organisation_id = j.organisation_id
			AND pa.domain_id = p.domain_id
			AND pa.path = p.path
		WHERE t.job_id = $1`

	countQuery := `
		SELECT COUNT(*) 
		FROM tasks t 
		WHERE t.job_id = $1`

	args := []any{jobID}

	// Add status filter if provided
	if params.Status != "" {
		baseQuery += ` AND t.status = $` + strconv.Itoa(len(args)+1)
		countQuery += ` AND t.status = $` + strconv.Itoa(len(args)+1)
		args = append(args, params.Status)
	}

	// Add cache filter if provided
	switch params.CacheFilter {
	case "miss":
		// MISS or EXPIRED: pages that could benefit from cache warming
		baseQuery += ` AND (t.cache_status = 'MISS' OR t.cache_status = 'EXPIRED')`
		countQuery += ` AND (t.cache_status = 'MISS' OR t.cache_status = 'EXPIRED')`
	case "hit":
		// HIT or DYNAMIC: cache performing optimally (cached, or inherently uncacheable)
		baseQuery += ` AND (t.cache_status = 'HIT' OR t.cache_status = 'DYNAMIC')`
		countQuery += ` AND (t.cache_status = 'HIT' OR t.cache_status = 'DYNAMIC')`
	}

	// Add performance filter: slow (>1500ms) or very_slow (>4000ms).
	// NULLIF treats a stored 0 as absent so pages that were warmed but recorded
	// 0 ms for second_response_time fall back to response_time correctly.
	switch params.PerformanceFilter {
	case "slow":
		baseQuery += ` AND COALESCE(NULLIF(t.second_response_time, 0), t.response_time) > 1500`
		countQuery += ` AND COALESCE(NULLIF(t.second_response_time, 0), t.response_time) > 1500`
	case "very_slow":
		baseQuery += ` AND COALESCE(NULLIF(t.second_response_time, 0), t.response_time) > 4000`
		countQuery += ` AND COALESCE(NULLIF(t.second_response_time, 0), t.response_time) > 4000`
	}

	// Add path filter if provided (case-insensitive partial match)
	if params.PathFilter != "" {
		baseQuery += ` AND p.path ILIKE $` + strconv.Itoa(len(args)+1)
		countQuery += ` AND p.path ILIKE $` + strconv.Itoa(len(args)+1)
		args = append(args, "%"+params.PathFilter+"%")
	}

	// Add ordering, limit, and offset
	baseQuery += ` ORDER BY ` + params.OrderBy + ` LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)
	args = append(args, params.Limit, params.Offset)

	return TaskQueryBuilder{
		SelectQuery: baseQuery,
		CountQuery:  countQuery,
		Args:        args,
	}
}

// formatTasksFromRows converts database rows into TaskResponse slice
func formatTasksFromRows(rows *sql.Rows) ([]TaskResponse, error) {
	var tasks []TaskResponse

	for rows.Next() {
		var task TaskResponse
		var host string
		var domain string
		var startedAt, completedAt, createdAt sql.NullTime
		var statusCode, responseTime, secondResponseTime sql.NullInt32
		var pageViews7d, pageViews28d, pageViews180d sql.NullInt64
		var cacheStatus, secondCacheStatus, contentType, errorMsg, sourceType, sourceURL sql.NullString

		err := rows.Scan(
			&task.ID, &task.JobID, &task.Path, &host, &domain, &task.Status,
			&statusCode, &responseTime, &cacheStatus, &secondResponseTime, &secondCacheStatus, &contentType, &errorMsg, &sourceType, &sourceURL,
			&createdAt, &startedAt, &completedAt, &task.RetryCount,
			&pageViews7d, &pageViews28d, &pageViews180d,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan task row: %w", err)
		}

		if canonicalHostForComparison(host) != canonicalHostForComparison(domain) {
			task.Host = &host
		}

		// Construct full URL from host and path
		task.URL = fmt.Sprintf("https://%s%s", host, task.Path)

		// Handle nullable fields
		if statusCode.Valid {
			sc := int(statusCode.Int32)
			task.StatusCode = &sc
		}
		if responseTime.Valid {
			rt := int(responseTime.Int32)
			task.ResponseTime = &rt
		}
		if cacheStatus.Valid {
			task.CacheStatus = &cacheStatus.String
		}
		if secondResponseTime.Valid {
			srt := int(secondResponseTime.Int32)
			task.SecondResponseTime = &srt
		}
		if secondCacheStatus.Valid {
			task.SecondCacheStatus = &secondCacheStatus.String
		}
		if contentType.Valid {
			task.ContentType = &contentType.String
		}
		if errorMsg.Valid {
			task.Error = &errorMsg.String
		}
		if sourceType.Valid {
			task.SourceType = &sourceType.String
		}
		if sourceURL.Valid {
			task.SourceURL = &sourceURL.String
		}
		if startedAt.Valid {
			sa := startedAt.Time.Format(time.RFC3339)
			task.StartedAt = &sa
		}
		if completedAt.Valid {
			ca := completedAt.Time.Format(time.RFC3339)
			task.CompletedAt = &ca
		}
		if pageViews7d.Valid {
			pv := int(pageViews7d.Int64)
			task.PageViews7d = &pv
		}
		if pageViews28d.Valid {
			pv := int(pageViews28d.Int64)
			task.PageViews28d = &pv
		}
		if pageViews180d.Valid {
			pv := int(pageViews180d.Int64)
			task.PageViews180d = &pv
		}

		// Format created_at
		if createdAt.Valid {
			task.CreatedAt = createdAt.Time.Format(time.RFC3339)
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}

func canonicalHostForComparison(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	return strings.TrimPrefix(host, "www.")
}

// TaskResponse represents a task in API responses
type TaskResponse struct {
	ID                 string  `json:"id"`
	JobID              string  `json:"job_id"`
	Host               *string `json:"host,omitempty"`
	Path               string  `json:"path"`
	URL                string  `json:"url"`
	Status             string  `json:"status"`
	StatusCode         *int    `json:"status_code,omitempty"`
	ResponseTime       *int    `json:"response_time,omitempty"`
	CacheStatus        *string `json:"cache_status,omitempty"`
	SecondResponseTime *int    `json:"second_response_time,omitempty"`
	SecondCacheStatus  *string `json:"second_cache_status,omitempty"`
	ContentType        *string `json:"content_type,omitempty"`
	Error              *string `json:"error,omitempty"`
	SourceType         *string `json:"source_type,omitempty"`
	SourceURL          *string `json:"source_url,omitempty"`
	CreatedAt          string  `json:"created_at"`
	StartedAt          *string `json:"started_at,omitempty"`
	CompletedAt        *string `json:"completed_at,omitempty"`
	RetryCount         int     `json:"retry_count"`
	PageViews7d        *int    `json:"page_views_7d,omitempty"`
	PageViews28d       *int    `json:"page_views_28d,omitempty"`
	PageViews180d      *int    `json:"page_views_180d,omitempty"`
}

// ExportColumn describes a column in exported task datasets
type ExportColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

func taskExportColumns(exportType string, includeAnalytics bool) []ExportColumn {
	switch exportType {
	case "broken-links":
		columns := []ExportColumn{
			{Key: "source_url", Label: "Found on"},
			{Key: "url", Label: "Broken link"},
			{Key: "status", Label: "Status"},
			{Key: "created_at", Label: "Date"},
			{Key: "source_type", Label: "Source Type"},
		}
		if includeAnalytics {
			columns = append(columns,
				ExportColumn{Key: "page_views_7d", Label: "Views (7d)"},
				ExportColumn{Key: "page_views_28d", Label: "Views (28d)"},
				ExportColumn{Key: "page_views_180d", Label: "Views (180d)"},
			)
		}
		return columns
	case "slow-pages":
		columns := []ExportColumn{
			{Key: "url", Label: "Page"},
			{Key: "content_type", Label: "Content Type"},
			{Key: "cache_status", Label: "Cache Status"},
			{Key: "response_time", Label: "Load Time (ms)"},
			{Key: "second_response_time", Label: "Load Time 2nd try (ms)"},
			{Key: "created_at", Label: "Date"},
		}
		if includeAnalytics {
			columns = append(columns,
				ExportColumn{Key: "page_views_7d", Label: "Views (7d)"},
				ExportColumn{Key: "page_views_28d", Label: "Views (28d)"},
				ExportColumn{Key: "page_views_180d", Label: "Views (180d)"},
			)
		}
		return columns
	default: // "job" (all tasks)
		columns := []ExportColumn{
			{Key: "id", Label: "Task ID"},
			{Key: "job_id", Label: "Job ID"},
			{Key: "path", Label: "Page path"},
			{Key: "url", Label: "Page URL"},
			{Key: "content_type", Label: "Content Type"},
			{Key: "status", Label: "Status"},
			{Key: "cache_status", Label: "Cache Status"},
			{Key: "status_code", Label: "Status Code"},
			{Key: "response_time", Label: "Load Time (ms)"},
			{Key: "second_cache_status", Label: "Second Cache Status"},
			{Key: "second_response_time", Label: "Load Response Time (ms)"},
			{Key: "retry_count", Label: "Retry Count"},
			{Key: "error", Label: "Error"},
			{Key: "source_type", Label: "Source"},
			{Key: "source_url", Label: "Source page"},
			{Key: "created_at", Label: "Created At"},
			{Key: "started_at", Label: "Started At"},
			{Key: "completed_at", Label: "Completed At"},
		}
		if includeAnalytics {
			columns = append(columns,
				ExportColumn{Key: "page_views_7d", Label: "Views (7d)"},
				ExportColumn{Key: "page_views_28d", Label: "Views (28d)"},
				ExportColumn{Key: "page_views_180d", Label: "Views (180d)"},
			)
		}
		return columns
	}
}

// getJobTasks handles GET /v1/jobs/:id/tasks
func (h *Handler) getJobTasks(w http.ResponseWriter, r *http.Request, jobID string) {
	logger := loggerWithRequest(r)

	// Validate user authentication and job access
	user := h.validateJobAccess(w, r, jobID)
	if user == nil {
		return // validateJobAccess already wrote the error response
	}

	// Parse query parameters and build queries
	params := parseTaskQueryParams(r)
	queries := buildTaskQuery(jobID, params)

	// Get total count
	var total int
	countArgs := queries.Args[:len(queries.Args)-2] // Remove limit and offset for count query
	err := h.DB.GetDB().QueryRowContext(r.Context(), queries.CountQuery, countArgs...).Scan(&total)
	if err != nil {
		if HandlePoolSaturation(w, r, err) {
			return
		}
		logger.Error("Failed to count tasks", "error", err, "job_id", jobID)
		DatabaseError(w, r, err)
		return
	}

	// Get tasks
	rows, err := h.DB.GetDB().QueryContext(r.Context(), queries.SelectQuery, queries.Args...)
	if err != nil {
		if HandlePoolSaturation(w, r, err) {
			return
		}
		logger.Error("Failed to get tasks", "error", err, "job_id", jobID)
		DatabaseError(w, r, err)
		return
	}
	defer rows.Close()

	// Format tasks from database rows
	tasks, err := formatTasksFromRows(rows)
	if err != nil {
		logger.Error("Failed to format tasks", "error", err, "job_id", jobID)
		DatabaseError(w, r, err)
		return
	}

	// Calculate pagination info
	hasNext := params.Offset+params.Limit < total
	hasPrev := params.Offset > 0

	// Prepare response
	response := map[string]any{
		"tasks": tasks,
		"pagination": map[string]any{
			"limit":    params.Limit,
			"offset":   params.Offset,
			"total":    total,
			"has_next": hasNext,
			"has_prev": hasPrev,
		},
	}

	WriteSuccess(w, r, response, "Tasks retrieved successfully")
}

// exportJobTasks handles GET /v1/jobs/:id/export
func (h *Handler) exportJobTasks(w http.ResponseWriter, r *http.Request, jobID string) {
	h.serveJobExport(w, r, jobID, true)
}

func (h *Handler) serveJobExport(w http.ResponseWriter, r *http.Request, jobID string, requireAuth bool) {
	logger := loggerWithRequest(r)

	if requireAuth {
		if h.validateJobAccess(w, r, jobID) == nil {
			return
		}
	}

	// Get export type from query parameter
	exportType := r.URL.Query().Get("type")
	if exportType == "" {
		exportType = "job" // Default to all tasks
	}

	// Build query based on export type
	var whereClause string

	switch exportType {
	case "broken-links":
		whereClause = " AND t.status = 'failed'"
	case "slow-pages":
		// Use second_response_time (cache HIT) when available, fallback to response_time
		whereClause = " AND COALESCE(t.second_response_time, t.response_time) > 3000"
	case "job":
		// Export all tasks
		whereClause = ""
	default:
		BadRequest(w, r, fmt.Sprintf("Invalid export type: %s", exportType))
		return
	}

	// Query tasks
	query := fmt.Sprintf(`
		SELECT
			t.id, t.job_id, p.path, COALESCE(t.host, d.name) as host, d.name as domain,
			t.status, t.status_code, t.response_time, t.cache_status,
			t.second_response_time, t.second_cache_status,
			t.content_type, t.error, t.source_type, t.source_url,
			t.created_at, t.started_at, t.completed_at, t.retry_count,
			pa.page_views_7d, pa.page_views_28d, pa.page_views_180d
		FROM tasks t
		JOIN pages p ON t.page_id = p.id
		JOIN domains d ON p.domain_id = d.id
		JOIN jobs j ON t.job_id = j.id
		LEFT JOIN page_analytics pa ON pa.organisation_id = j.organisation_id
			AND pa.domain_id = p.domain_id
			AND pa.path = p.path
		WHERE t.job_id = $1%s
		ORDER BY t.created_at DESC
		LIMIT 10000
	`, whereClause)

	rows, err := h.DB.GetDB().QueryContext(r.Context(), query, jobID)
	if err != nil {
		logger.Error("Failed to export tasks", "error", err, "job_id", jobID)
		DatabaseError(w, r, err)
		return
	}
	defer rows.Close()

	// Format tasks from database rows
	tasks, err := formatTasksFromRows(rows)
	if err != nil {
		logger.Error("Failed to format export tasks", "error", err, "job_id", jobID)
		DatabaseError(w, r, err)
		return
	}

	// Get job details
	var domain, status string
	var createdAt time.Time
	var completedAt sql.NullTime
	err = h.DB.GetDB().QueryRowContext(r.Context(), `
		SELECT d.name, j.status, j.created_at, j.completed_at
		FROM jobs j
		JOIN domains d ON j.domain_id = d.id
		WHERE j.id = $1
	`, jobID).Scan(&domain, &status, &createdAt, &completedAt)

	if err != nil {
		logger.Error("Failed to get job details for export", "error", err, "job_id", jobID)
		DatabaseError(w, r, err)
		return
	}

	includeAnalytics := false
	for _, task := range tasks {
		if task.PageViews7d != nil || task.PageViews28d != nil || task.PageViews180d != nil {
			includeAnalytics = true
			break
		}
	}

	// Prepare export response
	response := map[string]any{
		"job_id":      jobID,
		"domain":      domain,
		"status":      status,
		"created_at":  createdAt.Format(time.RFC3339),
		"export_type": exportType,
		"export_time": time.Now().UTC().Format(time.RFC3339),
		"total_tasks": len(tasks),
		"columns":     taskExportColumns(exportType, includeAnalytics),
		"tasks":       tasks,
	}
	if completedAt.Valid {
		response["completed_at"] = completedAt.Time.Format(time.RFC3339)
	} else {
		response["completed_at"] = nil
	}

	WriteSuccess(w, r, response, fmt.Sprintf("Exported %d tasks for job %s", len(tasks), jobID))
}

// fetchGA4DataBeforeJob fetches GA4 analytics data before job creation
// This runs in the foreground (blocking) for phase 1, with phases 2-3 in background
func (h *Handler) fetchGA4DataBeforeJob(ctx context.Context, logger *logging.Logger, organisationID, domain string) {
	// Normalise domain to match database format
	normalisedDomain := util.NormaliseDomain(domain)

	// Get domain ID from database
	domainID, err := h.DB.GetOrCreateDomainID(ctx, normalisedDomain)
	if err != nil {
		logger.Warn("Failed to get domain ID for GA4 fetch, skipping analytics",
			"error", err,
			"organisation_id", organisationID,
			"next_action", "analytics_skipped",
		)
		return
	}

	// Create progressive fetcher
	fetcher := NewProgressiveFetcher(h.DB, h.GoogleClientID, h.GoogleClientSecret)

	// Fetch GA4 data (phase 1 blocks, phases 2-3 run in background)
	if err := fetcher.FetchAndUpdatePages(ctx, organisationID, domainID); err != nil {
		// Log error but don't fail job creation
		logger.Warn("Failed to fetch GA4 data, continuing without analytics",
			"error", err,
			"organisation_id", organisationID,
			"domain_id", domainID,
			"next_action", "job_continues_without_ga4",
		)
	}
}
