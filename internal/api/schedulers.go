package api

import (
	"database/sql"
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

// SchedulerRequest represents the request body for creating/updating a scheduler
type SchedulerRequest struct {
	Domain                string   `json:"domain"`                            // Only used for creation, not update
	ScheduleIntervalHours *int     `json:"schedule_interval_hours,omitempty"` // Pointer for explicit optional updates
	Concurrency           *int     `json:"concurrency,omitempty"`
	FindLinks             *bool    `json:"find_links,omitempty"`
	MaxPages              *int     `json:"max_pages,omitempty"`
	IncludePaths          []string `json:"include_paths,omitempty"`
	ExcludePaths          []string `json:"exclude_paths,omitempty"`
	IsEnabled             *bool    `json:"is_enabled,omitempty"`
	ExpectedIsEnabled     *bool    `json:"expected_is_enabled,omitempty"` // Optional optimistic concurrency hint
}

// SchedulerResponse represents a scheduler in API responses
type SchedulerResponse struct {
	ID                    string   `json:"id"`
	Domain                string   `json:"domain"`
	ScheduleIntervalHours int      `json:"schedule_interval_hours"`
	NextRunAt             string   `json:"next_run_at"`
	IsEnabled             bool     `json:"is_enabled"`
	Concurrency           int      `json:"concurrency"`
	FindLinks             bool     `json:"find_links"`
	MaxPages              int      `json:"max_pages"`
	IncludePaths          []string `json:"include_paths,omitempty"`
	ExcludePaths          []string `json:"exclude_paths,omitempty"`
	CreatedAt             string   `json:"created_at"`
	UpdatedAt             string   `json:"updated_at"`
}

// SchedulersHandler handles requests to /v1/schedulers
func (h *Handler) SchedulersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listSchedulers(w, r)
	case http.MethodPost:
		h.createScheduler(w, r)
	default:
		MethodNotAllowed(w, r)
	}
}

// SchedulerHandler handles requests to /v1/schedulers/:id
func (h *Handler) SchedulerHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/schedulers/")
	if path == "" {
		BadRequest(w, r, "Scheduler ID is required")
		return
	}

	parts := strings.Split(path, "/")
	schedulerID := parts[0]

	// Validate UUID format early
	if _, err := uuid.Parse(schedulerID); err != nil {
		BadRequest(w, r, "Invalid scheduler ID format")
		return
	}

	if len(parts) > 1 && parts[1] == "jobs" {
		if r.Method == http.MethodGet {
			h.getSchedulerJobs(w, r, schedulerID)
			return
		}
		MethodNotAllowed(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getScheduler(w, r, schedulerID)
	case http.MethodPut:
		h.updateScheduler(w, r, schedulerID)
	case http.MethodDelete:
		h.deleteScheduler(w, r, schedulerID)
	default:
		MethodNotAllowed(w, r)
	}
}

// createScheduler handles POST /v1/schedulers
func (h *Handler) createScheduler(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	_ = logger

	var req SchedulerRequest
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

	if req.ScheduleIntervalHours == nil {
		BadRequest(w, r, "schedule_interval_hours is required")
		return
	}
	if *req.ScheduleIntervalHours != 6 && *req.ScheduleIntervalHours != 12 &&
		*req.ScheduleIntervalHours != 24 && *req.ScheduleIntervalHours != 48 {
		BadRequest(w, r, "schedule_interval_hours must be 6, 12, 24, or 48")
		return
	}

	normalisedDomain := util.NormaliseDomain(req.Domain)

	// Get or create domain
	var domainID int
	err := h.DB.GetDB().QueryRowContext(r.Context(), `
		INSERT INTO domains(name) VALUES($1)
		ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name
		RETURNING id
	`, normalisedDomain).Scan(&domainID)
	if err != nil {
		logger.Error("Failed to get or create domain", "error", err, "domain", normalisedDomain)
		InternalError(w, r, err)
		return
	}

	// Check if scheduler already exists for this domain/organisation
	var existingID string
	err = h.DB.GetDB().QueryRowContext(r.Context(), `
		SELECT id FROM schedulers
		WHERE domain_id = $1 AND organisation_id = $2
	`, domainID, orgID).Scan(&existingID)
	if err == nil {
		BadRequest(w, r, "Scheduler already exists for this domain")
		return
	} else if err != sql.ErrNoRows {
		logger.Error("Failed to check for existing scheduler", "error", err)
		InternalError(w, r, err)
		return
	}

	// Set defaults
	concurrency := 20
	if req.Concurrency != nil && *req.Concurrency > 0 {
		concurrency = min(*req.Concurrency, 100)
	}

	findLinks := true
	if req.FindLinks != nil {
		findLinks = *req.FindLinks
	}

	maxPages := 0
	if req.MaxPages != nil {
		maxPages = *req.MaxPages
		if maxPages < 0 {
			BadRequest(w, r, "max_pages cannot be negative")
			return
		}
	}

	isEnabled := true
	if req.IsEnabled != nil {
		isEnabled = *req.IsEnabled
	}

	now := time.Now().UTC()
	scheduler := &db.Scheduler{
		ID:                    uuid.New().String(),
		DomainID:              domainID,
		OrganisationID:        orgID,
		ScheduleIntervalHours: *req.ScheduleIntervalHours,
		NextRunAt:             now.Add(time.Duration(*req.ScheduleIntervalHours) * time.Hour),
		IsEnabled:             isEnabled,
		Concurrency:           concurrency,
		FindLinks:             findLinks,
		MaxPages:              maxPages,
		IncludePaths:          req.IncludePaths,
		ExcludePaths:          req.ExcludePaths,
		RequiredWorkers:       1,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	if err := h.DB.CreateScheduler(r.Context(), scheduler); err != nil {
		logger.Error("Failed to create scheduler", "error", err, "domain", normalisedDomain)
		InternalError(w, r, err)
		return
	}

	response := schedulerToResponse(scheduler, normalisedDomain)
	WriteCreated(w, r, response, "Scheduler created successfully")
}

// listSchedulers handles GET /v1/schedulers
func (h *Handler) listSchedulers(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	schedulers, err := h.DB.ListSchedulers(r.Context(), orgID)
	if err != nil {
		logger.Error("Failed to list schedulers", "error", err, "organisation_id", orgID)
		InternalError(w, r, err)
		return
	}

	// Get domain names for all schedulers in one query (avoid N+1)
	if len(schedulers) == 0 {
		WriteSuccess(w, r, []SchedulerResponse{}, "Schedulers retrieved successfully")
		return
	}

	// Build domain ID list
	domainIDs := make([]int, 0, len(schedulers))
	for _, scheduler := range schedulers {
		domainIDs = append(domainIDs, scheduler.DomainID)
	}

	// Get domain names for all schedulers in one query
	domainNames, err := h.DB.GetDomainNames(r.Context(), domainIDs)
	if err != nil {
		logger.Error("Failed to query domain names", "error", err)
		InternalError(w, r, err)
		return
	}

	// Build responses
	responses := make([]SchedulerResponse, 0, len(schedulers))
	for _, scheduler := range schedulers {
		domainName, ok := domainNames[scheduler.DomainID]
		if !ok {
			logger.Warn("Domain name not found", "domain_id", scheduler.DomainID)
			continue
		}
		responses = append(responses, schedulerToResponse(scheduler, domainName))
	}

	WriteSuccess(w, r, responses, "Schedulers retrieved successfully")
}

// getScheduler handles GET /v1/schedulers/:id
func (h *Handler) getScheduler(w http.ResponseWriter, r *http.Request, schedulerID string) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	scheduler, err := h.DB.GetScheduler(r.Context(), schedulerID)
	if err != nil {
		if errors.Is(err, db.ErrSchedulerNotFound) {
			NotFound(w, r, "Scheduler not found")
		} else {
			logger.Error("Failed to get scheduler", "error", err, "scheduler_id", schedulerID)
			InternalError(w, r, err)
		}
		return
	}

	if orgID != scheduler.OrganisationID {
		Unauthorised(w, r, "Scheduler access denied")
		return
	}

	domainName, err := h.DB.GetDomainNameByID(r.Context(), scheduler.DomainID)
	if err != nil {
		logger.Error("Failed to get domain name", "error", err, "domain_id", scheduler.DomainID)
		InternalError(w, r, err)
		return
	}

	response := schedulerToResponse(scheduler, domainName)
	WriteSuccess(w, r, response, "Scheduler retrieved successfully")
}

// updateScheduler handles PUT /v1/schedulers/:id
// Note: Domain cannot be changed after scheduler creation. Only job configuration
// and schedule settings can be updated.
func (h *Handler) updateScheduler(w http.ResponseWriter, r *http.Request, schedulerID string) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	scheduler, err := h.DB.GetScheduler(r.Context(), schedulerID)
	if err != nil {
		if errors.Is(err, db.ErrSchedulerNotFound) {
			NotFound(w, r, "Scheduler not found")
		} else {
			logger.Error("Failed to get scheduler", "error", err, "scheduler_id", schedulerID)
			InternalError(w, r, err)
		}
		return
	}

	if orgID != scheduler.OrganisationID {
		Unauthorised(w, r, "Scheduler access denied")
		return
	}

	var req SchedulerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, r, "Invalid JSON request body")
		return
	}

	// Update fields if provided
	if req.ScheduleIntervalHours != nil {
		if *req.ScheduleIntervalHours != 6 && *req.ScheduleIntervalHours != 12 &&
			*req.ScheduleIntervalHours != 24 && *req.ScheduleIntervalHours != 48 {
			BadRequest(w, r, "schedule_interval_hours must be 6, 12, 24, or 48")
			return
		}
		scheduler.ScheduleIntervalHours = *req.ScheduleIntervalHours
	}

	if req.Concurrency != nil {
		concurrency := *req.Concurrency
		if concurrency <= 0 {
			BadRequest(w, r, "concurrency must be greater than 0")
			return
		}
		concurrency = min(concurrency, 100)
		scheduler.Concurrency = concurrency
	}

	if req.FindLinks != nil {
		scheduler.FindLinks = *req.FindLinks
	}

	if req.MaxPages != nil {
		maxPages := *req.MaxPages
		if maxPages < 0 {
			BadRequest(w, r, "max_pages cannot be negative")
			return
		}
		scheduler.MaxPages = maxPages
	}

	if req.IncludePaths != nil {
		scheduler.IncludePaths = req.IncludePaths
	}

	if req.ExcludePaths != nil {
		scheduler.ExcludePaths = req.ExcludePaths
	}

	if req.IsEnabled != nil {
		scheduler.IsEnabled = *req.IsEnabled
	}

	if err := h.DB.UpdateScheduler(r.Context(), schedulerID, scheduler, req.ExpectedIsEnabled); err != nil {
		if errors.Is(err, db.ErrSchedulerNotFound) {
			NotFound(w, r, "Scheduler not found")
		} else if errors.Is(err, db.ErrSchedulerStateConflict) {
			WriteErrorMessage(w, r, "Scheduler state changed; refresh and retry", http.StatusConflict, ErrCodeConflict)
		} else {
			logger.Error("Failed to update scheduler", "error", err, "scheduler_id", schedulerID)
			InternalError(w, r, err)
		}
		return
	}

	domainName, err := h.DB.GetDomainNameByID(r.Context(), scheduler.DomainID)
	if err != nil {
		logger.Error("Failed to get domain name", "error", err, "domain_id", scheduler.DomainID)
		InternalError(w, r, err)
		return
	}

	response := schedulerToResponse(scheduler, domainName)
	WriteSuccess(w, r, response, "Scheduler updated successfully")
}

// deleteScheduler handles DELETE /v1/schedulers/:id
func (h *Handler) deleteScheduler(w http.ResponseWriter, r *http.Request, schedulerID string) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	scheduler, err := h.DB.GetScheduler(r.Context(), schedulerID)
	if err != nil {
		if errors.Is(err, db.ErrSchedulerNotFound) {
			NotFound(w, r, "Scheduler not found")
		} else {
			logger.Error("Failed to get scheduler", "error", err, "scheduler_id", schedulerID)
			InternalError(w, r, err)
		}
		return
	}

	if orgID != scheduler.OrganisationID {
		Unauthorised(w, r, "Scheduler access denied")
		return
	}

	if err := h.DB.DeleteScheduler(r.Context(), schedulerID); err != nil {
		if errors.Is(err, db.ErrSchedulerNotFound) {
			NotFound(w, r, "Scheduler not found")
		} else {
			logger.Error("Failed to delete scheduler", "error", err, "scheduler_id", schedulerID)
			InternalError(w, r, err)
		}
		return
	}

	WriteSuccess(w, r, map[string]string{"id": schedulerID, "status": "deleted"}, "Scheduler deleted successfully")
}

// getSchedulerJobs handles GET /v1/schedulers/:id/jobs
func (h *Handler) getSchedulerJobs(w http.ResponseWriter, r *http.Request, schedulerID string) {
	logger := loggerWithRequest(r)

	// Get active organisation (validates auth and membership)
	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return // Error already written
	}

	scheduler, err := h.DB.GetScheduler(r.Context(), schedulerID)
	if err != nil {
		if errors.Is(err, db.ErrSchedulerNotFound) {
			NotFound(w, r, "Scheduler not found")
		} else {
			logger.Error("Failed to get scheduler", "error", err, "scheduler_id", schedulerID)
			InternalError(w, r, err)
		}
		return
	}

	if orgID != scheduler.OrganisationID {
		Unauthorised(w, r, "Scheduler access denied")
		return
	}

	// Parse pagination
	limit := 10
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

	// Query jobs for this scheduler - SECURITY FIX: Added organisation_id filter
	query := `
		SELECT j.id, d.name as domain, j.status, j.total_tasks, j.completed_tasks,
		       j.failed_tasks, j.skipped_tasks, j.progress, j.created_at,
		       j.started_at, j.completed_at
		FROM jobs j
		JOIN domains d ON j.domain_id = d.id
		WHERE j.scheduler_id = $1 AND j.organisation_id = $2
		ORDER BY j.created_at DESC
		LIMIT $3 OFFSET $4
	`

	rows, err := h.DB.GetDB().QueryContext(r.Context(), query, schedulerID, orgID, limit, offset)
	if err != nil {
		logger.Error("Failed to query scheduler jobs", "error", err, "scheduler_id", schedulerID)
		InternalError(w, r, err)
		return
	}
	defer rows.Close()

	var jobs []JobResponse
	for rows.Next() {
		var job JobResponse
		var startedAt, completedAt sql.NullTime

		err := rows.Scan(
			&job.ID, &job.Domain, &job.Status, &job.TotalTasks,
			&job.CompletedTasks, &job.FailedTasks, &job.SkippedTasks,
			&job.Progress, &job.CreatedAt, &startedAt, &completedAt,
		)
		if err != nil {
			logger.Error("Failed to scan job row", "error", err)
			// Continue processing other rows rather than failing entire request
			continue
		}

		if startedAt.Valid {
			started := startedAt.Time.Format(time.RFC3339)
			job.StartedAt = &started
		}
		if completedAt.Valid {
			completed := completedAt.Time.Format(time.RFC3339)
			job.CompletedAt = &completed
		}

		jobs = append(jobs, job)
	}

	// Get total count - SECURITY FIX: Added organisation_id filter
	var total int
	err = h.DB.GetDB().QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM jobs WHERE scheduler_id = $1 AND organisation_id = $2
	`, schedulerID, orgID).Scan(&total)
	if err != nil {
		logger.Error("Failed to count scheduler jobs", "error", err, "scheduler_id", schedulerID)
		InternalError(w, r, err)
		return
	}

	response := map[string]any{
		"jobs": jobs,
		"pagination": map[string]any{
			"limit":    limit,
			"offset":   offset,
			"total":    total,
			"has_next": offset+limit < total,
			"has_prev": offset > 0,
		},
	}

	WriteSuccess(w, r, response, "Scheduler jobs retrieved successfully")
}

// schedulerToResponse converts a db.Scheduler to SchedulerResponse
func schedulerToResponse(scheduler *db.Scheduler, domainName string) SchedulerResponse {
	return SchedulerResponse{
		ID:                    scheduler.ID,
		Domain:                domainName,
		ScheduleIntervalHours: scheduler.ScheduleIntervalHours,
		NextRunAt:             scheduler.NextRunAt.Format(time.RFC3339),
		IsEnabled:             scheduler.IsEnabled,
		Concurrency:           scheduler.Concurrency,
		FindLinks:             scheduler.FindLinks,
		MaxPages:              scheduler.MaxPages,
		IncludePaths:          scheduler.IncludePaths,
		ExcludePaths:          scheduler.ExcludePaths,
		CreatedAt:             scheduler.CreatedAt.Format(time.RFC3339),
		UpdatedAt:             scheduler.UpdatedAt.Format(time.RFC3339),
	}
}
