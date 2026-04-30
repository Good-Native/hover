package jobs

import (
	"time"
)

// New value: also update ValidateStatusTransition + (if terminal)
// terminalJobStatuses + trigger preserve list + [CRAWL_HANDLING.md] row.
type JobStatus string

const (
	JobStatusPending      JobStatus = "pending"
	JobStatusInitialising JobStatus = "initializing"
	JobStatusRunning      JobStatus = "running"
	JobStatusPaused       JobStatus = "paused"
	JobStatusCompleted    JobStatus = "completed"
	JobStatusFailed       JobStatus = "failed"
	JobStatusCancelled    JobStatus = "cancelled"
	JobStatusArchived     JobStatus = "archived"
	// JobStatusBlocked is set when the WAF detector flags a domain as
	// blocked (issue #365 row 1). The pre-flight probe and the mid-job
	// circuit breaker both transition jobs into this terminal state.
	JobStatusBlocked JobStatus = "blocked"
)

type TaskStatus string

const (
	TaskStatusWaiting   TaskStatus = "waiting"
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusSkipped   TaskStatus = "skipped"
)

// Mirrors tasks.task_type / task_outbox.task_type (migration 20260427000001).
type TaskType string

const (
	TaskTypeCrawl      TaskType = "crawl"
	TaskTypeLighthouse TaskType = "lighthouse"
)

const (
	TaskStaleTimeout = 3 * time.Minute
	MaxTaskRetries   = 5
)

// CHECK: Do all of these currently get utilised somewhere in the app?
type Job struct {
	ID                       string    `json:"id"`
	Domain                   string    `json:"domain"`
	UserID                   *string   `json:"user_id,omitempty"`
	OrganisationID           *string   `json:"organisation_id,omitempty"`
	Status                   JobStatus `json:"status"`
	Progress                 float64   `json:"progress"`
	TotalTasks               int       `json:"total_tasks"`
	CompletedTasks           int       `json:"completed_tasks"`
	FailedTasks              int       `json:"failed_tasks"`
	SkippedTasks             int       `json:"skipped_tasks"`
	FoundTasks               int       `json:"found_tasks"`
	SitemapTasks             int       `json:"sitemap_tasks"`
	CreatedAt                time.Time `json:"created_at"`
	StartedAt                time.Time `json:"started_at"`
	CompletedAt              time.Time `json:"completed_at"`
	Concurrency              int       `json:"concurrency"`
	FindLinks                bool      `json:"find_links"`
	MaxPages                 int       `json:"max_pages"`
	IncludePaths             []string  `json:"include_paths,omitempty"`
	ExcludePaths             []string  `json:"exclude_paths,omitempty"`
	RequiredWorkers          int       `json:"required_workers"`
	AllowCrossSubdomainLinks bool      `json:"allow_cross_subdomain_links"`
	SourceType               *string   `json:"source_type,omitempty"`
	SourceDetail             *string   `json:"source_detail,omitempty"`
	SourceInfo               *string   `json:"source_info,omitempty"`
	ErrorMessage             string    `json:"error_message,omitempty"`
	SchedulerID              *string   `json:"scheduler_id,omitempty"`
	DurationSeconds          *int      `json:"duration_seconds,omitempty"`
	AvgTimePerTaskSeconds    *float64  `json:"avg_time_per_task_seconds,omitempty"`
}

type Task struct {
	ID          string     `json:"id"`
	JobID       string     `json:"job_id"`
	PageID      int        `json:"page_id"`
	Host        string     `json:"host"`
	Path        string     `json:"path"`
	DomainID    int        `json:"domain_id"`
	DomainName  string     `json:"domain_name"`
	Status      TaskStatus `json:"status"`
	TaskType    TaskType   `json:"task_type,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt time.Time  `json:"completed_at"`
	RetryCount  int        `json:"retry_count"`
	Error       string     `json:"error,omitempty"`

	SourceType string `json:"source_type"` // "sitemap", "link", "manual"
	SourceURL  string `json:"source_url,omitempty"`

	StatusCode         int    `json:"status_code,omitempty"`
	ResponseTime       int64  `json:"response_time,omitempty"`
	CacheStatus        string `json:"cache_status,omitempty"`
	ContentType        string `json:"content_type,omitempty"`
	SecondResponseTime int64  `json:"second_response_time,omitempty"`
	SecondCacheStatus  string `json:"second_cache_status,omitempty"`

	PriorityScore float64 `json:"priority_score"`

	FindLinks                bool `json:"-"`
	CrawlDelay               int  `json:"-"` // seconds, from robots.txt
	JobConcurrency           int  `json:"-"`
	AdaptiveDelay            int  `json:"-"`
	AdaptiveDelayFloor       int  `json:"-"`
	AllowCrossSubdomainLinks bool `json:"-"`
}

type JobOptions struct {
	Domain                   string   `json:"domain"`
	UserID                   *string  `json:"user_id,omitempty"`
	OrganisationID           *string  `json:"organisation_id,omitempty"`
	UseSitemap               bool     `json:"use_sitemap"`
	Concurrency              int      `json:"concurrency"`
	FindLinks                bool     `json:"find_links"`
	AllowCrossSubdomainLinks bool     `json:"allow_cross_subdomain_links"`
	MaxPages                 int      `json:"max_pages"`
	IncludePaths             []string `json:"include_paths,omitempty"`
	ExcludePaths             []string `json:"exclude_paths,omitempty"`
	RequiredWorkers          int      `json:"required_workers"`
	SourceType               *string  `json:"source_type,omitempty"`
	SourceDetail             *string  `json:"source_detail,omitempty"`
	SourceInfo               *string  `json:"source_info,omitempty"`
	SchedulerID              *string  `json:"scheduler_id,omitempty"`
}

type QuotaExceededError struct {
	Used     int       `json:"used"`
	Limit    int       `json:"limit"`
	ResetsAt time.Time `json:"resets_at"`
	PlanName string    `json:"plan_name"`
}

func (e *QuotaExceededError) Error() string {
	return "daily quota exceeded"
}
