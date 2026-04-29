package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/util"
	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

var jobsLog = logging.Component("jobs")

// errBlockJobRaceLost is the sentinel a BlockJob transaction returns
// when its CAS guard finds the job already in a terminal status set by
// a concurrent writer. It is not a real failure — callers up-stack
// translate it to nil success.
var errBlockJobRaceLost = errors.New("block_job: lost race to terminal transition")

type DbQueueProvider interface {
	Execute(ctx context.Context, fn func(*sql.Tx) error) error
	EnqueueURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error
	CleanupStuckJobs(ctx context.Context) error
}

type JobManagerInterface interface {
	CreateJob(ctx context.Context, options *JobOptions) (*Job, error)
	CancelJob(ctx context.Context, jobID string) error
	BlockJob(ctx context.Context, jobID string, vendor, reason string) error
	GetJobStatus(ctx context.Context, jobID string) (*Job, error)

	GetJob(ctx context.Context, jobID string) (*Job, error)
	EnqueueJobURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error

	IsJobComplete(job *Job) bool
	CalculateJobProgress(job *Job) float64
	ValidateStatusTransition(from, to JobStatus) error
	UpdateJobStatus(ctx context.Context, jobID string, status JobStatus) error
	MarkJobRunning(ctx context.Context, jobID string) error

	// Returns nil rules (not error) when crawler is unavailable; callers treat that as "no restriction".
	GetRobotsRules(ctx context.Context, domain string) (*crawler.RobotsRules, error)
}

// Nil callback means legacy DB-queue mode (no external broker scheduling).
type TaskScheduleCallback func(ctx context.Context, jobID string, entries []TaskScheduleEntry)

// RunAt is the earliest dispatch time; for waiting/retry rows it carries
// the backoff deadline so callbacks don't schedule them as ready-now.
type TaskScheduleEntry struct {
	TaskID     string
	PageID     int
	Host       string
	Path       string
	Status     string // "pending", "waiting", "skipped"
	Priority   float64
	RetryCount int
	SourceType string
	SourceURL  string
	RunAt      time.Time
}

type JobManager struct {
	db      *sql.DB
	dbQueue DbQueueProvider
	crawler CrawlerInterface

	// Fire-and-forget; nil is allowed (legacy DB-queue mode, tests).
	OnTasksEnqueued TaskScheduleCallback

	// Fire-and-forget; must return promptly — runs on the batch flusher's goroutine.
	OnProgressMilestone ProgressMilestoneCallback

	// Fire-and-forget; nil allowed for tests and deploys without REDIS_URL.
	OnJobTerminated JobTerminatedCallback

	// At-most-once-per-replica; downstream dedupes (e.g. lighthouse_runs UNIQUE).
	milestoneMu        sync.Mutex
	lastMilestoneFired map[string]int // jobID -> milestone (0,10,20...100)

	processedPages map[string]struct{} // key: "jobID_pageID"
	pagesMutex     sync.RWMutex

	sitemapSem chan struct{} // caps concurrent sitemap batch insertions across all jobs
}

type ProgressMilestoneCallback func(ctx context.Context, jobID string, oldPct, newPct int)

type JobTerminatedCallback func(ctx context.Context, jobID string)

func NewJobManager(db *sql.DB, dbQueue DbQueueProvider, crawler CrawlerInterface) *JobManager {
	sitemapConcurrency := sitemapInsertConcurrency()
	return &JobManager{
		db:                 db,
		dbQueue:            dbQueue,
		crawler:            crawler,
		processedPages:     make(map[string]struct{}),
		lastMilestoneFired: make(map[string]int),
		sitemapSem:         make(chan struct{}, sitemapConcurrency),
	}
}

// Wired as the BatchFlushCallback on db.BatchManager. Multi-replica safe:
// downstream dedupe handles concurrent fires from sibling replicas.
func (jm *JobManager) MaybeFireMilestones(ctx context.Context, jobIDs []string) {
	if jm == nil || jm.OnProgressMilestone == nil || len(jobIDs) == 0 {
		return
	}

	rows, err := jm.db.QueryContext(ctx, `
		SELECT id, total_tasks,
		       COALESCE(completed_tasks, 0) + COALESCE(failed_tasks, 0) + COALESCE(skipped_tasks, 0)
		  FROM jobs
		 WHERE id = ANY($1)
	`, pq.Array(jobIDs))
	if err != nil {
		jobsLog.Warn("milestone progress read failed", "error", err, "job_count", len(jobIDs))
		return
	}
	defer rows.Close()

	type pending struct {
		jobID  string
		oldPct int
		newPct int
	}
	var fires []pending

	for rows.Next() {
		var (
			jobID                  string
			total, finishedTaskCnt int
		)
		if err := rows.Scan(&jobID, &total, &finishedTaskCnt); err != nil {
			jobsLog.Warn("milestone progress scan failed", "error", err)
			continue
		}
		if total <= 0 {
			continue
		}
		// Saturate at 100 in case triggers ever over-count.
		newPct := finishedTaskCnt * 100 / total
		if newPct > 100 {
			newPct = 100
		}
		newMilestone := (newPct / 10) * 10

		jm.milestoneMu.Lock()
		oldMilestone, ok := jm.lastMilestoneFired[jobID]
		if ok && newMilestone <= oldMilestone {
			jm.milestoneMu.Unlock()
			continue
		}
		// 0% is not a milestone — suppresses a spurious (0,0) callback on first observation.
		if newMilestone == 0 {
			jm.lastMilestoneFired[jobID] = 0
			jm.milestoneMu.Unlock()
			continue
		}
		jm.lastMilestoneFired[jobID] = newMilestone
		jm.milestoneMu.Unlock()

		fires = append(fires, pending{
			jobID:  jobID,
			oldPct: oldMilestone,
			newPct: newMilestone,
		})
	}
	if err := rows.Err(); err != nil {
		jobsLog.Warn("milestone progress iterate failed", "error", err)
	}

	for _, f := range fires {
		jobsLog.Debug("milestone crossed",
			"job_id", f.jobID, "old_pct", f.oldPct, "new_pct", f.newPct)
		jm.OnProgressMilestone(ctx, f.jobID, f.oldPct, f.newPct)
	}
}

func (jm *JobManager) handleExistingJobs(ctx context.Context, domain string, userID *string, organisationID *string) error {
	if (userID == nil || *userID == "") && (organisationID == nil || *organisationID == "") {
		return nil
	}

	var existingJobID string
	var existingJobStatus string
	var existingOrgID sql.NullString
	var existingUserID sql.NullString

	var query string
	var args []any

	if organisationID != nil && *organisationID != "" {
		// Prefer organisation-level duplicate check so multi-user orgs share a single active job.
		query = `
			SELECT j.id, j.status, j.organisation_id, j.user_id
			FROM jobs j
			JOIN domains d ON j.domain_id = d.id
			WHERE d.name = $1
			AND j.organisation_id = $2
			AND j.status IN ('pending', 'initializing', 'running', 'paused')
			ORDER BY j.created_at DESC
			LIMIT 1
		`
		args = []any{domain, *organisationID}
	} else {
		query = `
			SELECT j.id, j.status, j.organisation_id, j.user_id
			FROM jobs j
			JOIN domains d ON j.domain_id = d.id
			WHERE d.name = $1
			AND j.user_id = $2
			AND j.status IN ('pending', 'initializing', 'running', 'paused')
			ORDER BY j.created_at DESC
			LIMIT 1
		`
		args = []any{domain, *userID}
	}

	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, query, args...).Scan(
			&existingJobID, &existingJobStatus, &existingOrgID, &existingUserID)
	})

	if err == nil && existingJobID != "" {
		args := []any{
			"existing_job_id", existingJobID,
			"existing_job_status", existingJobStatus,
			"domain", domain,
		}
		if existingOrgID.Valid {
			args = append(args, "organisation_id", existingOrgID.String)
		}
		if existingUserID.Valid {
			args = append(args, "user_id", existingUserID.String)
		}
		jobsLog.Info("Found existing active job for domain, cancelling it", args...)

		if err := jm.CancelJob(ctx, existingJobID); err != nil {
			jobsLog.Error("Failed to cancel existing job", "error", err, "job_id", existingJobID)
		}
	} else if err != nil && err != sql.ErrNoRows {
		jobsLog.Warn("Error checking for existing jobs", "error", err, "domain", domain)
	}

	// Always return nil — duplicate-check failures must not block new job creation.
	return nil
}

func createJobObject(options *JobOptions, normalisedDomain string) *Job {
	return &Job{
		ID:                       uuid.New().String(),
		Domain:                   normalisedDomain,
		UserID:                   options.UserID,
		OrganisationID:           options.OrganisationID,
		Status:                   JobStatusPending,
		Progress:                 0,
		TotalTasks:               0,
		CompletedTasks:           0,
		FoundTasks:               0,
		SitemapTasks:             0,
		FailedTasks:              0,
		CreatedAt:                time.Now().UTC(),
		Concurrency:              options.Concurrency,
		FindLinks:                options.FindLinks,
		MaxPages:                 options.MaxPages,
		IncludePaths:             options.IncludePaths,
		ExcludePaths:             options.ExcludePaths,
		RequiredWorkers:          options.RequiredWorkers,
		AllowCrossSubdomainLinks: options.AllowCrossSubdomainLinks,
		SourceType:               options.SourceType,
		SourceDetail:             options.SourceDetail,
		SourceInfo:               options.SourceInfo,
		SchedulerID:              options.SchedulerID,
	}
}

func (jm *JobManager) setupJobDatabase(ctx context.Context, job *Job, normalisedDomain string) (int, error) {
	var domainID int
	var cachedWAFBlocked bool
	var cachedWAFVendor sql.NullString
	var wafBlockedAt sql.NullTime

	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// Single round-trip: upsert the domain and read its WAF cache
		// flags so we can decide synchronously whether to skip discovery.
		err := tx.QueryRow(`
			INSERT INTO domains(name) VALUES($1)
			ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name
			RETURNING id, waf_blocked, waf_vendor, waf_blocked_at`,
			normalisedDomain).Scan(&domainID, &cachedWAFBlocked, &cachedWAFVendor, &wafBlockedAt)
		if err != nil {
			return fmt.Errorf("failed to get or create domain: %w", err)
		}

		// Cache hit (within the 24h freshness window) means the previous
		// pre-flight or circuit breaker already verified this domain is
		// blocked. Skip discovery and stamp the job in the same tx.
		if cachedWAFBlocked && wafBlockedAt.Valid && time.Since(wafBlockedAt.Time) < wafCacheTTL {
			job.Status = JobStatusBlocked
			completedAt := time.Now().UTC()
			job.CompletedAt = completedAt
			vendor := cachedWAFVendor.String
			job.ErrorMessage = buildWAFBlockMessage(vendor, "cached pre-flight verdict")

			_, err = tx.Exec(
				`INSERT INTO jobs (
					id, domain_id, user_id, organisation_id, status, progress, total_tasks, completed_tasks, failed_tasks, skipped_tasks,
					created_at, completed_at, concurrency, find_links, include_paths, exclude_paths,
					required_workers, max_pages, allow_cross_subdomain_links,
					found_tasks, sitemap_tasks, source_type, source_detail, source_info, scheduler_id, error_message
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)`,
				job.ID, domainID, job.UserID, job.OrganisationID, string(job.Status), job.Progress,
				job.TotalTasks, job.CompletedTasks, job.FailedTasks, job.SkippedTasks,
				job.CreatedAt, completedAt, job.Concurrency, job.FindLinks,
				db.Serialise(job.IncludePaths), db.Serialise(job.ExcludePaths),
				job.RequiredWorkers, job.MaxPages, job.AllowCrossSubdomainLinks,
				job.FoundTasks, job.SitemapTasks, job.SourceType, job.SourceDetail, job.SourceInfo,
				job.SchedulerID, job.ErrorMessage,
			)
			return err
		}

		_, err = tx.Exec(
			`INSERT INTO jobs (
				id, domain_id, user_id, organisation_id, status, progress, total_tasks, completed_tasks, failed_tasks, skipped_tasks,
				created_at, concurrency, find_links, include_paths, exclude_paths,
				required_workers, max_pages, allow_cross_subdomain_links,
				found_tasks, sitemap_tasks, source_type, source_detail, source_info, scheduler_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)`,
			job.ID, domainID, job.UserID, job.OrganisationID, string(job.Status), job.Progress,
			job.TotalTasks, job.CompletedTasks, job.FailedTasks, job.SkippedTasks,
			job.CreatedAt, job.Concurrency, job.FindLinks,
			db.Serialise(job.IncludePaths), db.Serialise(job.ExcludePaths),
			job.RequiredWorkers, job.MaxPages, job.AllowCrossSubdomainLinks,
			job.FoundTasks, job.SitemapTasks, job.SourceType, job.SourceDetail, job.SourceInfo,
			job.SchedulerID,
		)
		return err
	})

	if err != nil {
		return 0, fmt.Errorf("failed to create job: %w", err)
	}

	return domainID, nil
}

// wafCacheTTL bounds how long the cached domains.waf_blocked verdict is
// trusted before the next CreateJob re-probes. Akamai/Cloudflare
// policies do change — a once-blocked domain can be allowlisted later
// — so the cache must expire. 24h matches the rate at which site
// owners typically respond to allowlist requests.
const wafCacheTTL = 24 * time.Hour

// Returns nil (no error) when the crawler is unavailable; callers treat
// nil rules as "no restriction".
func (jm *JobManager) GetRobotsRules(ctx context.Context, domain string) (*crawler.RobotsRules, error) {
	if jm.crawler == nil {
		return nil, nil
	}
	result, err := jm.crawler.DiscoverSitemapsAndRobots(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("jobs: fetch robots for %s: %w", domain, err)
	}
	if result == nil {
		return nil, nil
	}
	return result.RobotsRules, nil
}

func (jm *JobManager) validateRootURLAccess(ctx context.Context, job *Job, normalisedDomain string, rootPath string) (*crawler.RobotsRules, error) {
	var robotsRules *crawler.RobotsRules

	if jm.crawler != nil {
		discoveryResult, err := jm.crawler.DiscoverSitemapsAndRobots(ctx, normalisedDomain)
		if err != nil {
			jobsLog.Error("Failed to fetch robots.txt for manual URL", "error", err, "domain", normalisedDomain)

			if updateErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `
					UPDATE jobs
					SET status = $1, error_message = $2, completed_at = $3
					WHERE id = $4
				`, JobStatusFailed, fmt.Sprintf("Failed to fetch robots.txt: %v", err), time.Now().UTC(), job.ID)
				return err
			}); updateErr != nil {
				jobsLog.Error("Failed to update job status", "error", updateErr, "job_id", job.ID)
			}
			return nil, fmt.Errorf("failed to fetch robots.txt: %w", err)
		}

		robotsRules = discoveryResult.RobotsRules

		if robotsRules != nil && robotsRules.CrawlDelay > 0 {
			jm.updateDomainCrawlDelay(ctx, normalisedDomain, robotsRules.CrawlDelay)
		}
	}

	if robotsRules != nil && !crawler.IsPathAllowed(robotsRules, rootPath) {
		jobsLog.Warn("Root path is disallowed by robots.txt, job cannot proceed",
			"job_id", job.ID, "domain", normalisedDomain, "path", rootPath)

		if updateErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE jobs
				SET status = $1, error_message = $2, completed_at = $3
				WHERE id = $4
			`, JobStatusFailed, "Root path (/) is disallowed by robots.txt", time.Now().UTC(), job.ID)
			return err
		}); updateErr != nil {
			jobsLog.Error("Failed to update job status", "error", updateErr, "job_id", job.ID)
		}
		return nil, fmt.Errorf("root path is disallowed by robots.txt")
	}

	return robotsRules, nil
}

func (jm *JobManager) createManualRootTask(ctx context.Context, job *Job, domainID int, rootPath string) error {
	var taskID string
	var pageID int
	var taskRunAt time.Time

	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
				INSERT INTO pages (domain_id, host, path)
				VALUES ($1, $2, $3)
				ON CONFLICT (domain_id, host, path) DO UPDATE SET path = EXCLUDED.path
				RETURNING id
			`, domainID, job.Domain, rootPath).Scan(&pageID)

		if err != nil {
			return fmt.Errorf("failed to create page record for root path: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO domain_hosts (domain_id, host, is_primary, last_seen_at)
			VALUES ($1, $2, TRUE, NOW())
			ON CONFLICT (domain_id, host) DO UPDATE
			SET is_primary = TRUE,
				last_seen_at = NOW()
		`, domainID, job.Domain); err != nil {
			return fmt.Errorf("failed to upsert domain host for root path: %w", err)
		}

		// Priority 1.0 so downstream link discovery (×0.9) yields non-zero child priorities.
		const rootPriority = 1.0
		taskID = uuid.New().String()
		now := time.Now().UTC()
		taskRunAt = now
		_, err = tx.ExecContext(ctx, `
			INSERT INTO tasks (
				id, job_id, page_id, host, path, status, created_at, retry_count,
				source_type, source_url, priority_score
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`, taskID, job.ID, pageID, job.Domain, rootPath, "pending", now, 0, "manual", "", rootPriority)

		if err != nil {
			return fmt.Errorf("failed to enqueue task for root path: %w", err)
		}

		// Outbox mirror so the sweeper can durably push to Redis if OnTasksEnqueued fails.
		if err := db.InsertOutboxRow(ctx, tx, db.OutboxEntry{
			TaskID:     taskID,
			JobID:      job.ID,
			PageID:     pageID,
			Host:       job.Domain,
			Path:       rootPath,
			Priority:   rootPriority,
			RetryCount: 0,
			SourceType: "manual",
			RunAt:      now,
		}); err != nil {
			return fmt.Errorf("failed to insert outbox row for root task: %w", err)
		}

		jm.markPageProcessed(job.ID, pageID)
		return nil
	})

	if err != nil {
		jobsLog.Error("Failed to create and enqueue root URL", "error", err)
		return err
	}

	if jm.OnTasksEnqueued != nil {
		jm.OnTasksEnqueued(ctx, job.ID, []TaskScheduleEntry{{
			TaskID:     taskID,
			PageID:     pageID,
			Host:       job.Domain,
			Path:       rootPath,
			Status:     "pending",
			Priority:   1.0,
			SourceType: "manual",
			RunAt:      taskRunAt,
		}})
	}

	jobsLog.Info("Added root URL to job queue", "job_id", job.ID, "domain", job.Domain)

	return nil
}

func (jm *JobManager) setupJobURLDiscovery(ctx context.Context, job *Job, options *JobOptions, domainID int, normalisedDomain string) error {
	if options.UseSitemap {
		jobID := job.ID
		go func() {
			// Detached from request ctx so the long sitemap fetch survives the HTTP handler returning.
			procCtx, procCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
			defer procCancel()

			if jm.runWAFPreflight(procCtx, job, normalisedDomain) {
				return
			}

			jm.processSitemap(procCtx, jobID, normalisedDomain, options.IncludePaths, options.ExcludePaths)
		}()
		return nil
	}

	backgroundCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	go func() {
		defer cancel()

		if jm.runWAFPreflight(backgroundCtx, job, normalisedDomain) {
			return
		}

		rootPath := "/"
		_, err := jm.validateRootURLAccess(backgroundCtx, job, normalisedDomain, rootPath)
		if err != nil {
			jobsLog.Error("Failed to validate root URL access", "error", err, "job_id", job.ID)
			return
		}

		if err := jm.createManualRootTask(backgroundCtx, job, domainID, rootPath); err != nil {
			jobsLog.Error("Failed to create manual root task", "error", err, "job_id", job.ID)
			return
		}
	}()

	return nil
}

// runWAFPreflight issues the live probe and, on a positive verdict,
// transitions the job to JobStatusBlocked via BlockJob. Returns true
// when the caller should stop (job is now terminal). A network /
// timeout error from the probe is logged as a warning and returns
// false — the mid-job circuit breaker is the safety net for those
// cases, and a transient probe failure must not block legitimate jobs.
func (jm *JobManager) runWAFPreflight(ctx context.Context, job *Job, normalisedDomain string) bool {
	if jm.crawler == nil {
		return false
	}

	det, err := jm.crawler.Probe(ctx, normalisedDomain)
	if err != nil {
		jobsLog.Warn("WAF pre-flight probe failed; continuing to discovery",
			"error", err, "job_id", job.ID, "domain", normalisedDomain)
		return false
	}
	if !det.Blocked {
		return false
	}

	jobsLog.Info("WAF pre-flight detected block",
		"job_id", job.ID,
		"domain", normalisedDomain,
		"vendor", det.Vendor,
		"reason", det.Reason)

	if err := jm.BlockJob(ctx, job.ID, det.Vendor, det.Reason); err != nil {
		jobsLog.Error("Failed to block job after WAF pre-flight detection; falling back to failed status",
			"error", err, "job_id", job.ID, "domain", normalisedDomain,
			"vendor", det.Vendor, "reason", det.Reason)
		// Fail-safe: returning true after a failed BlockJob would
		// strand the job in 'pending' with no tasks forever, because
		// the caller skips discovery on the strength of our return
		// value alone. Transition the job to failed via a separate
		// path so the customer sees a terminal state either way.
		// The customer-facing error_message stays stable; the raw
		// underlying error is captured in the structured log above
		// (with vendor/reason/domain context) for ops debugging.
		const wafFallbackMsg = "WAF detected but block transition failed"
		if failErr := jm.failJobWithMessage(ctx, job.ID, wafFallbackMsg); failErr != nil {
			jobsLog.Error("Fallback failJob after BlockJob error also failed; allowing discovery to proceed",
				"error", failErr, "job_id", job.ID)
			return false
		}
	}
	return true
}

// isJobInTerminalStatus reports whether a job's current row status is
// one the discovery / link-extraction paths must stop adding tasks
// for. Used between sitemap batches as a cheap pre-flight before each
// EnqueueJobURLs round-trip; the DB-side guard in
// dbQueue.EnqueueURLs is the race-free safety net.
//
// Read errors are treated as "not terminal" — a transient query
// failure must not silently abort a healthy crawl.
func (jm *JobManager) isJobInTerminalStatus(ctx context.Context, jobID string) bool {
	var status string
	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&status)
	})
	if err != nil {
		jobsLog.Warn("Could not read job status during terminal-state check; continuing",
			"error", err, "job_id", jobID)
		return false
	}
	return db.IsTerminalJobStatus(status)
}

// failJobWithMessage transitions a job to JobStatusFailed with an
// explanatory message. Used as the fallback path when a more specific
// terminal transition (BlockJob) couldn't complete. The status guard
// keeps a concurrent terminal write safe — we never overwrite a
// completed/failed/cancelled/blocked row.
func (jm *JobManager) failJobWithMessage(ctx context.Context, jobID, message string) error {
	return jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			   SET status = $1, error_message = $2, completed_at = $3
			 WHERE id = $4
			   AND status IN ($5, $6, $7, $8)
		`, JobStatusFailed, message, time.Now().UTC(), jobID,
			JobStatusRunning, JobStatusPending, JobStatusPaused, JobStatusInitialising)
		return err
	})
}

func (jm *JobManager) CreateJob(ctx context.Context, options *JobOptions) (*Job, error) {
	span := sentry.StartSpan(ctx, "manager.create_job")
	defer span.Finish()

	span.SetTag("domain", options.Domain)

	normalisedDomain := util.NormaliseDomain(options.Domain)

	if options.Concurrency <= 0 {
		defaultConcurrency := jobDefaultConcurrency()
		jobsLog.Info("Concurrency not specified; using default",
			"domain", normalisedDomain,
			"default_concurrency", defaultConcurrency,
			"source", "GNH_MAX_WORKERS")
		options.Concurrency = defaultConcurrency
	}

	if err := jm.handleExistingJobs(ctx, normalisedDomain, options.UserID, options.OrganisationID); err != nil {
		return nil, fmt.Errorf("failed to handle existing jobs: %w", err)
	}

	job := createJobObject(options, normalisedDomain)

	domainID, err := jm.setupJobDatabase(ctx, job, normalisedDomain)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return nil, err
	}

	jobsLog.Info("Created new job",
		"job_id", job.ID,
		"domain", job.Domain,
		"use_sitemap", options.UseSitemap,
		"find_links", options.FindLinks,
		"max_pages", options.MaxPages,
		"status", string(job.Status),
	)

	// Cached WAF verdict: setupJobDatabase already stamped the job
	// blocked, so don't kick off discovery. The job is already terminal.
	if job.Status == JobStatusBlocked {
		span.SetTag("waf_cache_hit", "true")
		return job, nil
	}

	if err := jm.setupJobURLDiscovery(ctx, job, options, domainID, normalisedDomain); err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		// Return job even on discovery error so the caller can surface partial state.
		return job, err
	}

	return job, nil
}

func (jm *JobManager) isPageProcessed(jobID string, pageID int) bool {
	key := fmt.Sprintf("%s_%d", jobID, pageID)
	jm.pagesMutex.RLock()
	defer jm.pagesMutex.RUnlock()
	_, exists := jm.processedPages[key]
	return exists
}

func (jm *JobManager) markPageProcessed(jobID string, pageID int) {
	key := fmt.Sprintf("%s_%d", jobID, pageID)
	jm.pagesMutex.Lock()
	defer jm.pagesMutex.Unlock()
	jm.processedPages[key] = struct{}{}
}

func (jm *JobManager) clearProcessedPages(jobID string) {
	jm.pagesMutex.Lock()
	defer jm.pagesMutex.Unlock()

	prefix := jobID + "_"
	for key := range jm.processedPages {
		if strings.HasPrefix(key, prefix) {
			delete(jm.processedPages, key)
		}
	}
}

// Idempotent; called on terminal-state to bound the per-job map on long-running workers.
func (jm *JobManager) clearMilestoneState(jobID string) {
	jm.milestoneMu.Lock()
	defer jm.milestoneMu.Unlock()
	delete(jm.lastMilestoneFired, jobID)
}

// Wraps dbQueue.EnqueueURLs with duplicate-page filtering against the in-process processedPages map.
func (jm *JobManager) EnqueueJobURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error {
	span := sentry.StartSpan(ctx, "manager.enqueue_job_urls")
	defer span.Finish()

	span.SetTag("job_id", jobID)
	span.SetTag("url_count", fmt.Sprintf("%d", len(pages)))

	if len(pages) == 0 {
		return nil
	}

	var filteredPages []db.Page
	for _, page := range pages {
		if !jm.isPageProcessed(jobID, page.ID) {
			// Don't mark yet — only after a successful enqueue.
			filteredPages = append(filteredPages, page)
		}
	}

	if len(filteredPages) == 0 {
		jobsLog.Debug("All URLs already processed, skipping", "job_id", jobID, "skipped_urls", len(pages))
		return nil
	}

	jobsLog.Debug("Enqueueing filtered URLs",
		"job_id", jobID,
		"total_urls", len(pages),
		"new_urls", len(filteredPages),
		"skipped_urls", len(pages)-len(filteredPages),
	)

	err := jm.dbQueue.EnqueueURLs(ctx, jobID, filteredPages, sourceType, sourceURL)

	if err == nil {
		for _, page := range filteredPages {
			jm.markPageProcessed(jobID, page.ID)
		}

		if jm.OnTasksEnqueued != nil {
			jm.scheduleEnqueuedTasks(ctx, jobID, filteredPages, sourceType, sourceURL)
		}
	} else {
		jobsLog.Error("Failed to enqueue URLs, not marking pages as processed",
			"error", err, "job_id", jobID, "url_count", len(filteredPages))
	}

	return err
}

// Re-queries by page_id; safe because EnqueueURLs' ON CONFLICT only updates
// pending/waiting/skipped, the same states filtered here. A cleaner contract
// (returning IDs from EnqueueURLs) is deferred to Stage 2.
func (jm *JobManager) scheduleEnqueuedTasks(ctx context.Context, jobID string, pages []db.Page, sourceType, sourceURL string) {
	if jm.OnTasksEnqueued == nil || len(pages) == 0 {
		return
	}

	pageIDs := make([]int, 0, len(pages))
	for _, p := range pages {
		if p.ID != 0 {
			pageIDs = append(pageIDs, p.ID)
		}
	}
	if len(pageIDs) == 0 {
		return
	}

	var entries []TaskScheduleEntry
	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, page_id, host, path, status, priority_score, retry_count, source_type, source_url, run_at
			FROM tasks
			WHERE job_id = $1 AND page_id = ANY($2) AND status IN ('pending', 'waiting')
		`, jobID, pq.Array(pageIDs))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e TaskScheduleEntry
			if err := rows.Scan(&e.TaskID, &e.PageID, &e.Host, &e.Path, &e.Status, &e.Priority, &e.RetryCount, &e.SourceType, &e.SourceURL, &e.RunAt); err != nil {
				return err
			}
			entries = append(entries, e)
		}
		return rows.Err()
	})
	if err != nil {
		jobsLog.Error("failed to query tasks for Redis scheduling", "error", err, "job_id", jobID)
		return
	}

	if len(entries) > 0 {
		jm.OnTasksEnqueued(ctx, jobID, entries)
	}
}

func (jm *JobManager) CancelJob(ctx context.Context, jobID string) error {
	span := sentry.StartSpan(ctx, "manager.cancel_job")
	defer span.Finish()

	span.SetTag("job_id", jobID)

	job, err := jm.GetJob(ctx, jobID)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return fmt.Errorf("failed to get job: %w", err)
	}

	// Already-cancelled is no-op success so duplicate clicks don't surface as red toasts.
	if job.Status == JobStatusCancelled {
		jobsLog.Debug("Cancel requested on already-cancelled job", "job_id", job.ID)
		return nil
	}

	if job.Status != JobStatusRunning && job.Status != JobStatusPending && job.Status != JobStatusPaused && job.Status != JobStatusInitialising {
		return fmt.Errorf("job cannot be canceled: %s", job.Status)
	}

	job.Status = JobStatusCancelled
	job.CompletedAt = time.Now().UTC()

	// Lock order: tasks before jobs. The AFTER STATEMENT counter trigger on
	// task UPDATEs takes jobs row locks in id order (migrations 20260425000001,
	// 20260426013451). Reversing this order deadlocks against worker batches
	// on the same job (HOVER: 40P01 on 30k-page cancels).
	err = jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		// ORDER BY id keeps the row-lock graph acyclic vs promote_waiting_with_outbox.
		_, err := tx.ExecContext(ctx, `
			WITH picked AS (
				SELECT id FROM tasks
				 WHERE job_id = $1
				   AND status IN ($3, $4)
				 ORDER BY id
				 FOR UPDATE
			)
			UPDATE tasks t
			   SET status = $2
			  FROM picked
			 WHERE t.id = picked.id
		`, job.ID, TaskStatusSkipped, TaskStatusPending, TaskStatusWaiting)
		if err != nil {
			return err
		}

		// Trigger preserves jobs.status when already 'cancelled'/'failed', so no race with a late completion fire.
		_, err = tx.ExecContext(ctx, `
			UPDATE jobs
			SET status = $1, completed_at = $2
			WHERE id = $3
		`, job.Status, job.CompletedAt, job.ID)
		if err != nil {
			return err
		}

		// Drop outbox rows so the sweeper doesn't ZADD tasks already flipped to skipped.
		_, err = tx.ExecContext(ctx, `
			DELETE FROM task_outbox WHERE job_id = $1
		`, job.ID)

		return err
	})

	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		jobsLog.Error("Failed to cancel job", "error", err, "job_id", job.ID)
		return fmt.Errorf("failed to cancel job: %w", err)
	}

	jm.clearProcessedPages(job.ID)
	jm.clearMilestoneState(job.ID)

	// Without this the per-job Redis keys (schedule ZSET, streams, consumer
	// groups, running-counter HASH field) leak — the dispatcher only scans
	// active jobs.
	if jm.OnJobTerminated != nil {
		jm.OnJobTerminated(ctx, job.ID)
	}

	jobsLog.Debug("Cancelled job", "job_id", job.ID, "domain", job.Domain)

	return nil
}

// BlockJob transitions a job to JobStatusBlocked when the WAF detector
// (pre-flight probe or mid-job circuit breaker) recognises bot
// protection on the domain. The shape mirrors CancelJob: tasks UPDATE
// → jobs UPDATE → task_outbox DELETE, with the same ORDER BY id lock
// order to keep the AFTER STATEMENT counter trigger from deadlocking
// against worker batches. domains.waf_blocked is set in the same
// transaction so a follow-up CreateJob short-circuits without
// re-probing.
func (jm *JobManager) BlockJob(ctx context.Context, jobID string, vendor, reason string) error {
	span := sentry.StartSpan(ctx, "manager.block_job")
	defer span.Finish()

	span.SetTag("job_id", jobID)
	span.SetTag("waf_vendor", vendor)

	job, err := jm.GetJob(ctx, jobID)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return fmt.Errorf("failed to get job: %w", err)
	}

	// Already-blocked is a no-op so a circuit-breaker fire racing the
	// pre-flight probe doesn't surface as a duplicate error.
	if job.Status == JobStatusBlocked {
		jobsLog.Debug("Block requested on already-blocked job", "job_id", job.ID)
		return nil
	}

	if job.Status != JobStatusRunning && job.Status != JobStatusPending && job.Status != JobStatusPaused && job.Status != JobStatusInitialising {
		return fmt.Errorf("job cannot be blocked: %s", job.Status)
	}

	job.Status = JobStatusBlocked
	job.CompletedAt = time.Now().UTC()
	errorMessage := buildWAFBlockMessage(vendor, reason)

	err = jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			WITH picked AS (
				SELECT id FROM tasks
				 WHERE job_id = $1
				   AND status IN ($3, $4)
				 ORDER BY id
				 FOR UPDATE
			)
			UPDATE tasks t
			   SET status = $2
			  FROM picked
			 WHERE t.id = picked.id
		`, job.ID, TaskStatusSkipped, TaskStatusPending, TaskStatusWaiting)
		if err != nil {
			return err
		}

		// CAS guard: GetJob ran outside this tx, so another worker
		// could have written a terminal state in between. Restrict the
		// UPDATE to pre-terminal statuses and bail if zero rows match.
		// Without this, a freshly-completed/failed/cancelled row would
		// be silently overwritten with `blocked`, and worse, we'd
		// stamp domains.waf_blocked off a verdict that didn't actually
		// land for this run.
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET status = $1, completed_at = $2, error_message = $3
			WHERE id = $4
			  AND status IN ($5, $6, $7, $8)
		`, job.Status, job.CompletedAt, errorMessage, job.ID,
			JobStatusRunning, JobStatusPending, JobStatusPaused, JobStatusInitialising)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return errBlockJobRaceLost
		}

		_, err = tx.ExecContext(ctx, `
			DELETE FROM task_outbox WHERE job_id = $1
		`, job.ID)
		if err != nil {
			return err
		}

		// Domain row last — nothing else takes a domain lock in this
		// transaction so the lock graph stays acyclic vs other writers.
		_, err = tx.ExecContext(ctx, `
			UPDATE domains
			   SET waf_blocked    = TRUE,
			       waf_vendor     = $1,
			       waf_blocked_at = NOW()
			 WHERE id = (SELECT domain_id FROM jobs WHERE id = $2)
		`, vendor, job.ID)
		return err
	})

	// errBlockJobRaceLost is not a real failure — a concurrent writer
	// reached terminal first. The whole transaction rolled back, so no
	// stale state landed; report success-equivalent so callers don't
	// surface a red error to the customer.
	if errors.Is(err, errBlockJobRaceLost) {
		jobsLog.Info("BlockJob lost race to another terminal transition; treating as no-op",
			"job_id", job.ID, "domain", job.Domain)
		return nil
	}

	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		jobsLog.Error("Failed to block job", "error", err, "job_id", job.ID)
		return fmt.Errorf("failed to block job: %w", err)
	}

	jm.clearProcessedPages(job.ID)
	jm.clearMilestoneState(job.ID)

	if jm.OnJobTerminated != nil {
		jm.OnJobTerminated(ctx, job.ID)
	}

	jobsLog.Info("Blocked job (WAF detected)",
		"job_id", job.ID,
		"domain", job.Domain,
		"waf_vendor", vendor,
		"reason", reason)

	return nil
}

func buildWAFBlockMessage(vendor, reason string) string {
	if vendor == "" {
		vendor = "unknown"
	}
	if reason == "" {
		return fmt.Sprintf("WAF blocked (%s) — site bot protection refused our crawler", vendor)
	}
	return fmt.Sprintf("WAF blocked (%s) — %s", vendor, reason)
}

func (jm *JobManager) GetJob(ctx context.Context, jobID string) (*Job, error) {
	span := sentry.StartSpan(ctx, "jobs.get_job")
	defer span.Finish()

	span.SetTag("job_id", jobID)

	var job Job
	var includePaths, excludePaths []byte
	var startedAt, completedAt sql.NullTime
	var errorMessage, userID, organisationID sql.NullString

	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT
				j.id, d.name, j.status, j.progress, j.total_tasks, j.completed_tasks, j.failed_tasks, j.skipped_tasks,
				j.created_at, j.started_at, j.completed_at, j.concurrency, j.find_links,
				j.include_paths, j.exclude_paths, j.error_message, j.required_workers,
				j.found_tasks, j.sitemap_tasks, j.duration_seconds, j.avg_time_per_task_seconds,
				j.user_id, j.organisation_id
			FROM jobs j
			JOIN domains d ON j.domain_id = d.id
			WHERE j.id = $1
		`, jobID).Scan(
			&job.ID, &job.Domain, &job.Status, &job.Progress, &job.TotalTasks, &job.CompletedTasks,
			&job.FailedTasks, &job.SkippedTasks, &job.CreatedAt, &startedAt, &completedAt, &job.Concurrency,
			&job.FindLinks, &includePaths, &excludePaths, &errorMessage, &job.RequiredWorkers,
			&job.FoundTasks, &job.SitemapTasks, &job.DurationSeconds, &job.AvgTimePerTaskSeconds,
			&userID, &organisationID,
		)
		return err
	})

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("job %s not found: %w", jobID, err)
	} else if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	if startedAt.Valid {
		job.StartedAt = startedAt.Time
	}

	if completedAt.Valid {
		job.CompletedAt = completedAt.Time
	}

	if errorMessage.Valid {
		job.ErrorMessage = errorMessage.String
	}

	if userID.Valid {
		job.UserID = &userID.String
	}

	if organisationID.Valid {
		job.OrganisationID = &organisationID.String
	}

	if len(includePaths) > 0 {
		err = json.Unmarshal(includePaths, &job.IncludePaths)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal include paths: %w", err)
		}
	}

	if len(excludePaths) > 0 {
		err = json.Unmarshal(excludePaths, &job.ExcludePaths)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal exclude paths: %w", err)
		}
	}

	return &job, nil
}

func (jm *JobManager) GetJobStatus(ctx context.Context, jobID string) (*Job, error) {
	if err := jm.dbQueue.CleanupStuckJobs(ctx); err != nil {
		jobsLog.Error("Failed to cleanup stuck jobs during status check", "error", err)
	}

	span := sentry.StartSpan(ctx, "manager.get_job_status")
	defer span.Finish()

	span.SetTag("job_id", jobID)

	job, err := jm.GetJob(ctx, jobID)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	return job, nil
}

func (jm *JobManager) discoverAndParseSitemaps(ctx context.Context, domain string) ([]string, *crawler.RobotsRules, error) {
	var sitemapCrawler CrawlerInterface
	if jm.crawler != nil {
		sitemapCrawler = jm.crawler
	} else {
		crawlerConfig := crawler.DefaultConfig()
		crawlerConfig.SkipCachedURLs = false
		sitemapCrawler = crawler.New(crawlerConfig)
	}

	discoveryResult, err := sitemapCrawler.DiscoverSitemapsAndRobots(ctx, domain)
	if err != nil {
		jobsLog.Error("Failed to discover sitemaps and robots rules", "error", err, "domain", domain)
		return []string{}, &crawler.RobotsRules{}, err
	}

	sitemaps := discoveryResult.Sitemaps
	robotsRules := discoveryResult.RobotsRules

	jobsLog.Info("Sitemaps discovered", "domain", domain, "sitemap_count", len(sitemaps))

	var urls []string
	for _, sitemapURL := range sitemaps {
		jobsLog.Info("Processing sitemap", "sitemap_url", sitemapURL)

		sitemapURLs, err := sitemapCrawler.ParseSitemap(ctx, sitemapURL)
		if err != nil {
			jobsLog.Warn("Error parsing sitemap", "error", err, "sitemap_url", sitemapURL)
			continue
		}

		jobsLog.Info("Parsed URLs from sitemap", "sitemap_url", sitemapURL, "url_count", len(sitemapURLs))

		urls = append(urls, sitemapURLs...)
	}

	return urls, robotsRules, nil
}

func (jm *JobManager) filterURLsAgainstRobots(urls []string, robotsRules *crawler.RobotsRules, includePaths, excludePaths []string) []string {
	var filteredURLs []string
	if jm.crawler != nil && (len(includePaths) > 0 || len(excludePaths) > 0) {
		filteredURLs = jm.crawler.FilterURLs(urls, includePaths, excludePaths)
	} else {
		filteredURLs = urls
	}

	if robotsRules != nil && len(robotsRules.DisallowPatterns) > 0 {
		var allowedURLs []string
		for _, urlStr := range filteredURLs {
			if parsedURL, err := url.Parse(urlStr); err == nil {
				path := parsedURL.Path
				if crawler.IsPathAllowed(robotsRules, path) {
					allowedURLs = append(allowedURLs, urlStr)
				} else {
					jobsLog.Debug("URL blocked by robots.txt", "url", urlStr, "path", path)
				}
			}
		}
		jobsLog.Info("Filtered URLs against robots.txt rules",
			"original_count", len(filteredURLs),
			"allowed_count", len(allowedURLs),
			"blocked_count", len(filteredURLs)-len(allowedURLs),
		)
		return allowedURLs
	}

	return filteredURLs
}

func (jm *JobManager) enqueueURLsForJob(ctx context.Context, jobID, domain string, urls []string, sourceType string) error {
	if len(urls) == 0 {
		return nil
	}

	var domainID int
	err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT domain_id FROM jobs WHERE id = $1
		`, jobID).Scan(&domainID)
	})
	if err != nil {
		return fmt.Errorf("failed to get domain ID: %w", err)
	}

	pageIDs, hosts, paths, err := db.CreatePageRecords(ctx, jm.dbQueue, domainID, domain, urls)
	if err != nil {
		return fmt.Errorf("failed to create page records: %w", err)
	}

	pagesWithPriority := make([]db.Page, len(pageIDs))
	for i, pageID := range pageIDs {
		pagesWithPriority[i] = db.Page{
			ID:       pageID,
			Host:     hosts[i],
			Path:     paths[i],
			Priority: 0.1,
		}
		if paths[i] == "/" {
			pagesWithPriority[i].Priority = 1.000
			jobsLog.Info("Set homepage priority to 1.000", "job_id", jobID)
		}
	}

	baseURL := fmt.Sprintf("https://%s", domain)
	if err := jm.EnqueueJobURLs(ctx, jobID, pagesWithPriority, sourceType, baseURL); err != nil {
		return fmt.Errorf("failed to enqueue URLs: %w", err)
	}

	jobsLog.Info("Added URLs to job queue",
		"job_id", jobID, "domain", domain, "url_count", len(urls), "source_type", sourceType)

	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `SELECT recalculate_job_stats($1)`, jobID)
		return err
	}); err != nil {
		jobsLog.Error("Failed to recalculate job stats", "error", err, "job_id", jobID)
	}

	return nil
}

func (jm *JobManager) updateDomainCrawlDelay(ctx context.Context, domain string, crawlDelay int) {
	if crawlDelay <= 0 {
		return
	}

	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE domains
			SET crawl_delay_seconds = $1
			WHERE name = $2
		`, crawlDelay, domain)
		return err
	}); err != nil {
		jobsLog.Error("Failed to update domain with crawl delay", "error", err, "domain", domain, "crawl_delay", crawlDelay)
	} else {
		jobsLog.Info("Updated domain with crawl delay from robots.txt", "domain", domain, "crawl_delay", crawlDelay)
	}
}

func (jm *JobManager) IsJobComplete(job *Job) bool {
	if job.Status != JobStatusRunning {
		return false
	}

	processedTasks := job.CompletedTasks + job.FailedTasks + job.SkippedTasks
	return processedTasks >= job.TotalTasks
}

func (jm *JobManager) CalculateJobProgress(job *Job) float64 {
	if job.TotalTasks == 0 {
		return 0.0
	}

	processedTasks := job.CompletedTasks + job.FailedTasks + job.SkippedTasks
	return float64(processedTasks) / float64(job.TotalTasks) * 100.0
}

func (jm *JobManager) ValidateStatusTransition(from, to JobStatus) error {
	// Allow restart from terminal states.
	if to == JobStatusRunning && (from == JobStatusCompleted || from == JobStatusCancelled || from == JobStatusFailed || from == JobStatusBlocked) {
		return nil
	}

	validTransitions := map[JobStatus][]JobStatus{
		JobStatusPending:      {JobStatusRunning, JobStatusCancelled, JobStatusBlocked},
		JobStatusInitialising: {JobStatusRunning, JobStatusCancelled, JobStatusBlocked, JobStatusFailed},
		JobStatusRunning:      {JobStatusCompleted, JobStatusFailed, JobStatusCancelled, JobStatusBlocked},
		JobStatusCompleted:    {JobStatusRunning, JobStatusArchived},
		JobStatusFailed:       {JobStatusRunning, JobStatusArchived},
		JobStatusCancelled:    {JobStatusRunning, JobStatusArchived},
		JobStatusBlocked:      {JobStatusRunning, JobStatusArchived},
	}

	allowed, exists := validTransitions[from]
	if !exists {
		return fmt.Errorf("invalid status transition from %s to %s", from, to)
	}

	if slices.Contains(allowed, to) {
		return nil
	}

	return fmt.Errorf("invalid status transition from %s to %s", from, to)
}

// Idempotent. Guard accepts 'initializing' too: sitemap jobs spend a real
// window in that state before first dispatch, and a narrower match would
// strand them on the "Starting up" pill.
func (jm *JobManager) MarkJobRunning(ctx context.Context, jobID string) error {
	return jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			   SET status = 'running',
			       started_at = COALESCE(started_at, NOW())
			 WHERE id = $1
			   AND status IN ('pending', 'initializing')
		`, jobID)
		return err
	})
}

func (jm *JobManager) UpdateJobStatus(ctx context.Context, jobID string, status JobStatus) error {
	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		var query string
		var args []any

		switch status {
		case JobStatusCompleted:
			query = "UPDATE jobs SET status = $1, completed_at = $2 WHERE id = $3"
			args = []any{string(status), time.Now().UTC(), jobID}
		case JobStatusRunning:
			query = "UPDATE jobs SET status = $1, started_at = $2 WHERE id = $3"
			args = []any{string(status), time.Now().UTC(), jobID}
		default:
			query = "UPDATE jobs SET status = $1 WHERE id = $2"
			args = []any{string(status), jobID}
		}

		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		return err
	}

	// Drop in-process state on terminal status so long-running workers don't accumulate per-job map entries.
	switch status {
	case JobStatusCompleted, JobStatusFailed, JobStatusCancelled, JobStatusArchived, JobStatusBlocked:
		jm.clearProcessedPages(jobID)
		jm.clearMilestoneState(jobID)
	}
	return nil
}

func (jm *JobManager) updateJobWithError(ctx context.Context, jobID, errorMessage string) {
	if updateErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET error_message = $1
			WHERE id = $2
		`, errorMessage, jobID)
		return err
	}); updateErr != nil {
		jobsLog.Error("Failed to update job with error message", "error", updateErr, "job_id", jobID)
	}
}

func (jm *JobManager) enqueueFallbackURL(ctx context.Context, jobID, domain string) error {
	jobsLog.Info("No URLs found in sitemap, falling back to root page", "job_id", jobID, "domain", domain)

	rootURL := fmt.Sprintf("https://%s/", domain)
	fallbackURLs := []string{rootURL}

	if err := jm.enqueueURLsForJob(ctx, jobID, domain, fallbackURLs, "fallback"); err != nil {
		jobsLog.Error("Failed to enqueue fallback root URL", "error", err, "job_id", jobID, "domain", domain)

		jm.updateJobWithError(ctx, jobID, fmt.Sprintf("Failed to create fallback task: %v", err))
		return err
	}

	jobsLog.Info("Created fallback root page task - job will proceed with link discovery",
		"job_id", jobID, "domain", domain)

	return nil
}

func (jm *JobManager) enqueueSitemapURLs(ctx context.Context, jobID, domain string, urls []string) error {
	for i, url := range urls {
		jobsLog.Debug("URL from sitemap", "job_id", jobID, "domain", domain, "index", i, "url", url)
	}

	if err := jm.enqueueURLsForJob(ctx, jobID, domain, urls, "sitemap"); err != nil {
		jobsLog.Error("Failed to enqueue sitemap URLs", "error", err, "job_id", jobID, "domain", domain)
		return err
	}

	return nil
}

func (jm *JobManager) processSitemap(ctx context.Context, jobID, domain string, includePaths, excludePaths []string) {
	if jm.crawler == nil || jm.dbQueue == nil || jm.db == nil {
		jobsLog.Warn("Skipping sitemap processing due to missing dependencies", "job_id", jobID, "domain", domain)
		return
	}

	span := sentry.StartSpan(ctx, "manager.process_sitemap")
	defer span.Finish()

	span.SetTag("job_id", jobID)
	span.SetTag("domain", domain)

	// Initialising state stops the worker pool from prematurely completing the job before URLs are enqueued.
	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs SET status = $1
			WHERE id = $2 AND status = $3
		`, JobStatusInitialising, jobID, JobStatusPending)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("job %s not in expected status %s", jobID, JobStatusPending)
		}
		return nil
	}); err != nil {
		jobsLog.Warn("Aborting sitemap processing: failed to enter initialising", "error", err, "job_id", jobID)
		return
	}

	jobsLog.Info("Starting sitemap processing", "job_id", jobID, "domain", domain)

	urls, robotsRules, err := jm.discoverAndParseSitemaps(ctx, domain)
	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		jobsLog.Error("Failed to discover sitemaps", "error", err, "job_id", jobID, "domain", domain)

		jm.updateJobWithError(ctx, jobID, fmt.Sprintf("Failed to discover sitemaps: %v", err))
		if markErr := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE jobs SET status = $1, completed_at = $2
				WHERE id = $3 AND status IN ($4, $5)
			`, JobStatusFailed, time.Now().UTC(), jobID, JobStatusInitialising, JobStatusPending)
			return err
		}); markErr != nil {
			jobsLog.Warn("Failed to mark job as failed after sitemap error", "error", markErr, "job_id", jobID)
		}
		return
	}

	jm.updateDomainCrawlDelay(ctx, domain, robotsRules.CrawlDelay)

	urls = jm.filterURLsAgainstRobots(urls, robotsRules, includePaths, excludePaths)

	// Snapshot before any tasks are inserted so progress denominator is stable.
	if err := jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs SET sitemap_tasks_found = $1 WHERE id = $2
		`, len(urls), jobID)
		return err
	}); err != nil {
		jobsLog.Warn("Failed to record sitemap_tasks_found", "error", err, "job_id", jobID, "sitemap_tasks_found", len(urls))
	}

	if len(urls) > 0 {
		// Homepage in the first batch guarantees its 1.0 priority regardless of sitemap order.
		urls = hoistHomepageToFront(urls, domain)

		// Throttled batches avoid a write burst that spikes Supabase DB pressure.
		batchSize := sitemapBatchSize()
		batchDelay := sitemapBatchDelay()
		totalBatches := (len(urls) + batchSize - 1) / batchSize

		jobsLog.Info("Enqueueing sitemap URLs in batches",
			"job_id", jobID,
			"total_urls", len(urls),
			"batch_size", batchSize,
			"total_batches", totalBatches,
			"batch_delay_ms", batchDelay,
		)

		firstBatchDone := false

		for i := 0; i < len(urls); i += batchSize {
			end := min(i+batchSize, len(urls))
			batch := urls[i:end]
			batchNum := (i / batchSize) + 1

			// Cheap status read between batches: a concurrent BlockJob
			// (pre-flight or circuit breaker) or CancelJob may have
			// flipped the job terminal mid-discovery. The DB guard in
			// dbQueue.EnqueueURLs is the load-bearing safety net, but
			// stopping here saves the per-batch sitemap parsing + DB
			// round-trip that would otherwise be wasted work for every
			// remaining batch (kmart.com.au-class jobs have hundreds).
			if jm.isJobInTerminalStatus(ctx, jobID) {
				jobsLog.Info("Sitemap discovery aborting: job reached terminal status mid-loop",
					"job_id", jobID, "batch_number", batchNum,
					"batches_remaining", totalBatches-batchNum+1,
					"urls_remaining", len(urls)-i)
				return
			}

			select {
			case jm.sitemapSem <- struct{}{}:
			case <-ctx.Done():
				jm.clearInitialisingAfterSitemapCancel(jobID, batchNum)
				jobsLog.Warn("Stopped waiting for sitemap insert slot", "error", ctx.Err(), "job_id", jobID, "batch_number", batchNum)
				return
			}

			err := jm.enqueueSitemapURLs(ctx, jobID, domain, batch)
			<-jm.sitemapSem
			if err != nil {
				jobsLog.Warn("Failed to enqueue URL batch, continuing with next batch",
					"error", err, "job_id", jobID, "batch_number", batchNum,
					"batch_start", i, "batch_size", len(batch),
				)
				continue
			}

			jobsLog.Info("Enqueued URL batch",
				"job_id", jobID,
				"batch_number", batchNum,
				"total_batches", totalBatches,
				"urls_enqueued", end,
				"total_urls", len(urls),
			)

			// Open for workers after the first batch lands; remaining batches insert in the background.
			if !firstBatchDone {
				firstBatchDone = true
				if err := jm.transitionInitialisingToPending(ctx, jobID); err != nil {
					jobsLog.Warn("Failed to open job after first batch", "error", err, "job_id", jobID)
				}
			}

			if end < len(urls) {
				time.Sleep(batchDelay)
			}
		}

		// If every batch failed, still exit initialising so the job isn't stuck.
		if !firstBatchDone {
			if err := jm.transitionInitialisingToPending(ctx, jobID); err != nil {
				jobsLog.Warn("Failed to transition job from initialising to pending after all batches failed",
					"error", err, "job_id", jobID)
				return
			}
		}
	} else {
		if err := jm.enqueueFallbackURL(ctx, jobID, domain); err != nil {
			return
		}
		if err := jm.transitionInitialisingToPending(ctx, jobID); err != nil {
			jobsLog.Warn("Failed to transition job from initialising to pending after fallback URL",
				"error", err, "job_id", jobID)
			return
		}
	}
}

func (jm *JobManager) clearInitialisingAfterSitemapCancel(jobID string, batchNum int) {
	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := jm.dbQueue.Execute(cleanupCtx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(cleanupCtx, `
				UPDATE jobs
				SET status = $1
				WHERE id = $2 AND status = $3
			`, JobStatusPending, jobID, JobStatusInitialising)
			return err
		}); err != nil {
			jobsLog.Warn("Failed to clear initialising state after sitemap cancellation",
				"error", err, "job_id", jobID, "batch_number", batchNum)
		}
	}()
}

func (jm *JobManager) transitionInitialisingToPending(ctx context.Context, jobID string) error {
	return jm.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs SET status = $1
			WHERE id = $2 AND status = $3
		`, JobStatusPending, jobID, JobStatusInitialising)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("job %s no longer in initialising state (may have been cancelled)", jobID)
		}
		return nil
	})
}

// Sitemaps don't guarantee ordering, so the homepage is hoisted explicitly.
func hoistHomepageToFront(urls []string, domain string) []string {
	if len(urls) == 0 {
		return urls
	}
	homepages := []string{
		"https://" + domain + "/",
		"https://" + domain,
		"http://" + domain + "/",
		"http://" + domain,
	}
	for i, u := range urls {
		for _, hp := range homepages {
			if u == hp {
				if i == 0 {
					return urls
				}
				result := make([]string, len(urls))
				result[0] = urls[i]
				copy(result[1:], urls[:i])
				copy(result[1+i:], urls[i+1:])
				return result
			}
		}
	}
	return urls
}

// GNH_SITEMAP_CONCURRENCY caps concurrent sitemap-batch inserters across all jobs; default 3.
func sitemapInsertConcurrency() int {
	const defaultConcurrency = 3
	if val := strings.TrimSpace(os.Getenv("GNH_SITEMAP_CONCURRENCY")); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			jobsLog.Warn("Invalid GNH_SITEMAP_CONCURRENCY — using default", "GNH_SITEMAP_CONCURRENCY", val, "error", err)
		} else if n <= 0 {
			jobsLog.Warn("GNH_SITEMAP_CONCURRENCY must be positive — using default", "GNH_SITEMAP_CONCURRENCY", val)
		} else {
			return n
		}
	}
	return defaultConcurrency
}

// GNH_SITEMAP_BATCH_SIZE — URLs per sitemap insert batch; default 100.
func sitemapBatchSize() int {
	const defaultSize = 100
	if val := strings.TrimSpace(os.Getenv("GNH_SITEMAP_BATCH_SIZE")); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			jobsLog.Warn("Invalid GNH_SITEMAP_BATCH_SIZE — using default", "GNH_SITEMAP_BATCH_SIZE", val, "error", err)
		} else if n <= 0 {
			jobsLog.Warn("GNH_SITEMAP_BATCH_SIZE must be positive — using default", "GNH_SITEMAP_BATCH_SIZE", val)
		} else {
			return n
		}
	}
	return defaultSize
}

// GNH_SITEMAP_BATCH_DELAY_MS — pause between sitemap batches; default 200ms.
func sitemapBatchDelay() time.Duration {
	const defaultDelay = 200 * time.Millisecond
	if val := strings.TrimSpace(os.Getenv("GNH_SITEMAP_BATCH_DELAY_MS")); val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			jobsLog.Warn("Invalid GNH_SITEMAP_BATCH_DELAY_MS — using default", "GNH_SITEMAP_BATCH_DELAY_MS", val, "error", err)
		} else if n < 0 {
			jobsLog.Warn("GNH_SITEMAP_BATCH_DELAY_MS must be non-negative — using default", "GNH_SITEMAP_BATCH_DELAY_MS", val)
		} else {
			return time.Duration(n) * time.Millisecond
		}
	}
	return defaultDelay
}
