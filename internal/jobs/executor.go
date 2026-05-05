package jobs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/good-native/hover/internal/archive"
	"github.com/good-native/hover/internal/crawler"
	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/observability"
	"github.com/good-native/hover/internal/util"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type RetryDecision struct {
	ShouldRetry        bool
	NextRunAt          time.Time
	Reason             string // "blocking", "retryable", "domain_delay"
	IsPermanentFailure bool
}

type TaskOutcome struct {
	Task            *db.Task
	CrawlResult     *crawler.CrawlResult
	Retry           *RetryDecision
	DiscoveredLinks map[string][]string
	HTMLUpload      *TaskHTMLUpload
	RateLimited     bool
	Success         bool
}

type TaskHTMLUpload struct {
	Path                string
	ContentType         string
	UploadContentType   string
	ContentEncoding     string
	SizeBytes           int64
	CompressedSizeBytes int64
	SHA256              string
	CapturedAt          time.Time
	Payload             []byte
}

type ExecutorConfig struct {
	MaxBlockingRetries int
	MaxTaskRetries     int
	BaseDelayMS        int
	MaxDelayMS         int
}

func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		MaxBlockingRetries: 3,
		MaxTaskRetries:     MaxTaskRetries,
		BaseDelayMS:        envIntWithDefault("GNH_RATE_LIMIT_BASE_DELAY_MS", 50),
		MaxDelayMS:         envIntWithDefault("GNH_RATE_LIMIT_MAX_DELAY_MS", 60000),
	}
}

// Non-positive values fall through to fallback; retry delays must be > 0.
func envIntWithDefault(key string, fallback int) int {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

// TaskExecutor runs crawl tasks and returns outcomes; it has no side effects
// on counters, schedulers, or persistence — the caller must act on the outcome.
type TaskExecutor struct {
	crawler CrawlerInterface
	cfg     ExecutorConfig
}

func NewTaskExecutor(c CrawlerInterface, cfg ExecutorConfig) *TaskExecutor {
	return &TaskExecutor{
		crawler: c,
		cfg:     cfg,
	}
}

// Execute does not modify external state; all decisions are returned in TaskOutcome.
func (e *TaskExecutor) Execute(ctx context.Context, task *Task) *TaskOutcome {
	start := time.Now()
	status := "success"
	queueWait := time.Duration(0)
	if !task.CreatedAt.IsZero() {
		if !task.StartedAt.IsZero() {
			queueWait = task.StartedAt.Sub(task.CreatedAt)
		} else {
			queueWait = time.Since(task.CreatedAt)
		}
	}

	ctx, span := observability.StartWorkerTaskSpan(ctx, observability.WorkerTaskSpanInfo{
		JobID:     task.JobID,
		TaskID:    task.ID,
		Domain:    task.DomainName,
		Path:      task.Path,
		FindLinks: task.FindLinks,
	})
	defer span.End()

	defer func() {
		totalDuration := time.Duration(0)
		if !task.CreatedAt.IsZero() {
			totalDuration = time.Since(task.CreatedAt)
		}

		observability.RecordWorkerTask(ctx, observability.WorkerTaskMetrics{
			JobID:         task.JobID,
			Status:        status,
			Duration:      time.Since(start),
			QueueWait:     queueWait,
			TotalDuration: totalDuration,
		})
	}()

	urlStr := ConstructTaskURL(task.Path, task.Host, task.DomainName)

	jobsLog.Debug("Starting URL warm", "url", urlStr, "task_id", task.ID)

	result, err := e.crawler.WarmURL(ctx, urlStr, task.FindLinks)
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		rateLimited := false
		if result != nil {
			switch result.StatusCode {
			case http.StatusTooManyRequests, http.StatusForbidden, http.StatusServiceUnavailable:
				rateLimited = true
			}
		}
		if !rateLimited {
			rateLimited = IsRateLimitError(err)
		}

		jobsLog.Debug("Crawler failed", "error", err, "task_id", task.ID, "rate_limited", rateLimited, "status_code", statusCodeOrZero(result))

		return e.buildErrorOutcome(ctx, task, result, err, rateLimited)
	}

	if result == nil {
		status = "error"
		nilErr := fmt.Errorf("crawler returned nil result for %s", urlStr)
		span.RecordError(nilErr)
		span.SetStatus(codes.Error, nilErr.Error())
		return e.buildErrorOutcome(ctx, task, nil, nilErr, false)
	}

	span.SetAttributes(
		attribute.Int("http.status_code", result.StatusCode),
		attribute.Int("task.links_found", len(result.Links)),
		attribute.String("task.content_type", result.ContentType),
	)
	span.SetStatus(codes.Ok, "completed")

	jobsLog.Debug("Crawler completed", "status_code", result.StatusCode, "task_id", task.ID, "links_found", len(result.Links), "content_type", result.ContentType)

	return e.buildSuccessOutcome(task, result)
}

func taskToDBTask(t *Task) *db.Task {
	return &db.Task{
		ID:            t.ID,
		JobID:         t.JobID,
		PageID:        t.PageID,
		Host:          t.Host,
		Path:          t.Path,
		Status:        string(t.Status),
		CreatedAt:     t.CreatedAt,
		StartedAt:     t.StartedAt,
		RetryCount:    t.RetryCount,
		Error:         t.Error,
		SourceType:    t.SourceType,
		SourceURL:     t.SourceURL,
		PriorityScore: t.PriorityScore,
	}
}

func (e *TaskExecutor) buildSuccessOutcome(task *Task, result *crawler.CrawlResult) *TaskOutcome {
	now := time.Now().UTC()

	dbTask := taskToDBTask(task)
	dbTask.Status = string(TaskStatusCompleted)
	dbTask.CompletedAt = now
	// Clear stale error text from a prior failed attempt.
	dbTask.Error = ""

	populateResponseFields(dbTask, result)
	populateJSONBFields(dbTask, result)

	outcome := &TaskOutcome{
		Task:        dbTask,
		CrawlResult: result,
		Success:     true,
	}

	if len(result.Links) > 0 {
		outcome.DiscoveredLinks = cloneDiscoveredLinks(result.Links)
	}

	// html_* columns are stamped by the persister after a successful R2 upload,
	// so the batch-persisted task row never references a missing object.
	if upload, ok := buildHTMLUpload(dbTask, result, now); ok {
		outcome.HTMLUpload = upload
	}

	return outcome
}

func (e *TaskExecutor) buildErrorOutcome(ctx context.Context, task *Task, result *crawler.CrawlResult, taskErr error, rateLimited bool) *TaskOutcome {
	now := time.Now().UTC()
	dbTask := taskToDBTask(task)
	// Preserve response diagnostics on rate-limit errors (403/429/503) that
	// still carry a result.
	if result != nil {
		populateResponseFields(dbTask, result)
		populateJSONBFields(dbTask, result)
	} else {
		// Seed JSONB defaults so every persisted row has the same shape.
		dbTask.Headers = []byte("{}")
		dbTask.SecondHeaders = []byte("{}")
		dbTask.CacheCheckAttempts = []byte("[]")
		populateRequestDiagnostics(dbTask, nil)
	}

	outcome := &TaskOutcome{
		Task:        dbTask,
		CrawlResult: result,
		RateLimited: rateLimited,
	}

	// rateLimited forces the blocking path so 429/403/503 with a generic error
	// string can't fall through to permanent failure and defeat domain pacing.
	isBlocking := rateLimited || isBlockingError(taskErr)
	isRetryable := isRetryableError(taskErr)

	if isBlocking {
		if dbTask.RetryCount < e.cfg.MaxBlockingRetries {
			dbTask.RetryCount++
			dbTask.Error = taskErr.Error()
			dbTask.Status = string(TaskStatusWaiting)
			dbTask.StartedAt = time.Time{}

			backoff := e.blockingBackoff(dbTask.RetryCount)
			outcome.Retry = &RetryDecision{
				ShouldRetry: true,
				NextRunAt:   now.Add(backoff),
				Reason:      "blocking",
			}
			observability.RecordWorkerTaskRetry(ctx, dbTask.JobID, "blocking")
		} else {
			dbTask.Status = string(TaskStatusFailed)
			dbTask.CompletedAt = now
			dbTask.Error = taskErr.Error()
			outcome.Retry = &RetryDecision{IsPermanentFailure: true}
			observability.RecordWorkerTaskFailure(ctx, dbTask.JobID, "blocking")
		}
	} else if isRetryable && dbTask.RetryCount < e.cfg.MaxTaskRetries {
		dbTask.RetryCount++
		dbTask.Error = taskErr.Error()
		dbTask.Status = string(TaskStatusWaiting)
		dbTask.StartedAt = time.Time{}

		backoff := e.retryableBackoff(dbTask.RetryCount)
		outcome.Retry = &RetryDecision{
			ShouldRetry: true,
			NextRunAt:   now.Add(backoff),
			Reason:      "retryable",
		}
		observability.RecordWorkerTaskRetry(ctx, dbTask.JobID, "retryable")
	} else {
		dbTask.Status = string(TaskStatusFailed)
		dbTask.CompletedAt = now
		dbTask.Error = taskErr.Error()
		outcome.Retry = &RetryDecision{IsPermanentFailure: true}

		failureReason := "non_retryable"
		if isRetryable {
			failureReason = "retryable_exhausted"
		} else if isBlocking {
			failureReason = "blocking"
		}
		observability.RecordWorkerTaskFailure(ctx, dbTask.JobID, failureReason)
	}

	return outcome
}

func (e *TaskExecutor) blockingBackoff(retryCount int) time.Duration {
	base := time.Duration(e.cfg.BaseDelayMS) * time.Millisecond
	delay := base * (1 << retryCount) // exponential
	max := time.Duration(e.cfg.MaxDelayMS) * time.Millisecond
	if delay > max {
		delay = max
	}
	return delay
}

func (e *TaskExecutor) retryableBackoff(retryCount int) time.Duration {
	delay := time.Duration(retryCount) * time.Second // linear
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

func ConstructTaskURL(path, host, domainName string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return util.NormaliseURL(path)
	} else if host != "" {
		return util.ConstructURL(host, path)
	} else if domainName != "" {
		return util.ConstructURL(domainName, path)
	}
	return util.NormaliseURL(path)
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errorStr := strings.ToLower(err.Error())
	networkErrors := strings.Contains(errorStr, "timeout") ||
		strings.Contains(errorStr, "deadline exceeded") ||
		strings.Contains(errorStr, "connection") ||
		strings.Contains(errorStr, "network") ||
		strings.Contains(errorStr, "temporary") ||
		strings.Contains(errorStr, "reset by peer") ||
		strings.Contains(errorStr, "broken pipe") ||
		strings.Contains(errorStr, "unexpected eof")
	serverErrors := strings.Contains(errorStr, "internal server error") ||
		strings.Contains(errorStr, "bad gateway") ||
		strings.Contains(errorStr, "gateway timeout") ||
		strings.Contains(errorStr, "502") ||
		strings.Contains(errorStr, "504") ||
		strings.Contains(errorStr, "500")
	return networkErrors || serverErrors
}

func isBlockingError(err error) bool {
	if err == nil {
		return false
	}
	errorStr := strings.ToLower(err.Error())
	return strings.Contains(errorStr, "403") ||
		strings.Contains(errorStr, "forbidden") ||
		strings.Contains(errorStr, "429") ||
		strings.Contains(errorStr, "too many requests") ||
		strings.Contains(errorStr, "rate limit") ||
		strings.Contains(errorStr, "503") ||
		strings.Contains(errorStr, "service unavailable")
}

func populateResponseFields(task *db.Task, result *crawler.CrawlResult) {
	if task == nil || result == nil {
		return
	}

	task.StatusCode = result.StatusCode
	task.ResponseTime = result.ResponseTime
	task.CacheStatus = result.CacheStatus
	task.ContentType = result.ContentType
	task.ContentLength = result.ContentLength

	if util.IsSignificantRedirect(result.URL, result.RedirectURL) {
		task.RedirectURL = result.RedirectURL
	}

	task.DNSLookupTime = result.Performance.DNSLookupTime
	task.TCPConnectionTime = result.Performance.TCPConnectionTime
	task.TLSHandshakeTime = result.Performance.TLSHandshakeTime
	task.TTFB = result.Performance.TTFB
	task.ContentTransferTime = result.Performance.ContentTransferTime

	// SecondContentLength is independent of SecondPerformance; persist
	// unconditionally. Timing fields require the performance pointer.
	task.SecondResponseTime = result.SecondResponseTime
	task.SecondCacheStatus = result.SecondCacheStatus
	task.SecondContentLength = result.SecondContentLength
	if result.SecondPerformance != nil {
		task.SecondDNSLookupTime = result.SecondPerformance.DNSLookupTime
		task.SecondTCPConnectionTime = result.SecondPerformance.TCPConnectionTime
		task.SecondTLSHandshakeTime = result.SecondPerformance.TLSHandshakeTime
		task.SecondTTFB = result.SecondPerformance.TTFB
		task.SecondContentTransferTime = result.SecondPerformance.ContentTransferTime
	}
}

func populateJSONBFields(task *db.Task, result *crawler.CrawlResult) {
	task.Headers = []byte("{}")
	task.SecondHeaders = []byte("{}")
	task.CacheCheckAttempts = []byte("[]")
	task.RequestDiagnostics = []byte("{}")

	if len(result.Headers) > 0 {
		if b, err := json.Marshal(result.Headers); err == nil && json.Valid(b) {
			task.Headers = b
		}
	}
	if len(result.SecondHeaders) > 0 {
		if b, err := json.Marshal(result.SecondHeaders); err == nil && json.Valid(b) {
			task.SecondHeaders = b
		}
	}
	if len(result.CacheCheckAttempts) > 0 {
		if b, err := json.Marshal(result.CacheCheckAttempts); err == nil && json.Valid(b) {
			task.CacheCheckAttempts = b
		}
	}

	populateRequestDiagnostics(task, result)
}

func populateRequestDiagnostics(task *db.Task, result *crawler.CrawlResult) {
	task.RequestDiagnostics = []byte("{}")
	if result == nil || result.RequestDiagnostics == nil {
		return
	}
	if b, err := json.Marshal(result.RequestDiagnostics); err == nil && json.Valid(b) {
		task.RequestDiagnostics = b
	}
}

const (
	htmlContentEncoding = "gzip"

	// Caps body size staged for R2 upload; crawler does not bound CrawlResult.Body
	// and a wide worker pool can inflate RSS. 8 MiB covers modern HTML.
	maxHTMLUploadBodyBytes = 8 * 1024 * 1024
)

func buildHTMLUpload(task *db.Task, result *crawler.CrawlResult, capturedAt time.Time) (*TaskHTMLUpload, bool) {
	if task == nil || result == nil || len(result.Body) == 0 {
		return nil, false
	}

	mediaType := canonicalHTMLContentType(result.ContentType)
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return nil, false
	}

	if len(result.Body) > maxHTMLUploadBodyBytes {
		// Debug, not Warn: a single large site can emit thousands per job.
		jobsLog.Debug("Skipping HTML archive upload: body exceeds size cap",
			"task_id", task.ID,
			"size_bytes", len(result.Body),
			"cap_bytes", maxHTMLUploadBodyBytes,
		)
		return nil, false
	}

	payload, err := gzipHTML(result.Body)
	if err != nil {
		jobsLog.Error("Failed to gzip HTML", "error", err, "task_id", task.ID)
		return nil, false
	}

	uploadCT := mediaType
	if uploadCT == "" {
		uploadCT = "text/html"
	}
	checksum := sha256.Sum256(result.Body)

	return &TaskHTMLUpload{
		Path:                archive.TaskHTMLObjectPath(task.JobID, task.ID),
		ContentType:         normalisedHTMLContentType(result.ContentType),
		UploadContentType:   uploadCT,
		ContentEncoding:     htmlContentEncoding,
		SizeBytes:           int64(len(result.Body)),
		CompressedSizeBytes: int64(len(payload)),
		SHA256:              hex.EncodeToString(checksum[:]),
		CapturedAt:          capturedAt,
		Payload:             payload,
	}, true
}

func canonicalHTMLContentType(ct string) string {
	trimmed := strings.TrimSpace(ct)
	if trimmed == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil {
		return strings.ToLower(trimmed)
	}
	return strings.ToLower(mediaType)
}

func normalisedHTMLContentType(ct string) string {
	trimmed := strings.TrimSpace(ct)
	if trimmed == "" {
		return "text/html"
	}
	return strings.ToLower(trimmed)
}

func gzipHTML(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(body); err != nil {
		return nil, fmt.Errorf("write gzip html: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close gzip html: %w", err)
	}
	return buf.Bytes(), nil
}

func cloneDiscoveredLinks(links map[string][]string) map[string][]string {
	if len(links) == 0 {
		return nil
	}
	cloned := make(map[string][]string, len(links))
	for cat, vals := range links {
		cloned[cat] = append([]string(nil), vals...)
	}
	return cloned
}

func statusCodeOrZero(result *crawler.CrawlResult) int {
	if result != nil {
		return result.StatusCode
	}
	return 0
}

var (
	ErrDomainDelay = errors.New("domain rate limit delay")
)
