package db

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/Harvey-AU/hover/internal/util"
)

var queueLog = logging.Component("queue")

var ErrTaskNotReadyForHTMLMetadata = errors.New("task not ready for html metadata")

// ConcurrencyOverrideFunc is a callback to get effective concurrency for a job
// Returns the effective concurrency limit, or 0 if no override exists
type ConcurrencyOverrideFunc func(jobID string, domain string) int

// DbQueue is a PostgreSQL implementation of a job queue
type DbQueue struct {
	db *DB
	// Mutex to prevent concurrent cleanup operations that cause prepared statement conflicts
	cleanupMutex        sync.Mutex
	poolWarnThreshold   float64
	poolRejectThreshold float64
	lastWarnLog         time.Time
	lastRejectLog       time.Time
	lastRejectWaitCount int64
	poolSemaphore       chan struct{}
	controlSemaphore    chan struct{}
	preserveConnections int
	maxConcurrent       int
	controlConcurrent   int
	maxTxRetries        int
	retryBaseDelay      time.Duration
	retryMaxDelay       time.Duration
	rng                 *rand.Rand
	// concurrencyOverride is a callback to get effective concurrency overrides from domain limiter
	concurrencyOverride ConcurrencyOverrideFunc
	// pressure adaptively adjusts the effective semaphore limit based on
	// observed query execution time, preventing Supabase from being overwhelmed.
	pressure *PressureController
}

// ErrPoolSaturated is returned when the database connection pool cannot provide
// a connection before the caller's context expires.
var ErrPoolSaturated = errors.New("database connection pool saturated")

// ErrConcurrencyBlocked is returned when pending tasks exist but all are blocked
// by job concurrency limits. Workers should back off when receiving this error.
var ErrConcurrencyBlocked = errors.New("tasks exist but blocked by concurrency limits")

const (
	defaultPoolWarnThreshold   = 0.90
	defaultPoolRejectThreshold = 0.95
	minRejectWaitDelta         = 5
	poolLogCooldown            = 5 * time.Second
	defaultQueueConcurrency    = 12
	defaultControlConcurrency  = 4
	defaultExecuteTimeout      = 30 * time.Second
	controlExecuteTimeout      = 10 * time.Second
	defaultTxRetries           = 3
	defaultRetryBaseDelay      = 200 * time.Millisecond
	defaultRetryMaxDelay       = 1500 * time.Millisecond

	// fdPressureThreshold is the fraction of the fd limit at which pressure is reported
	fdPressureThreshold = 0.90

	waitingReasonConcurrencyLimit = "concurrency_limit"
	waitingReasonQuotaExhausted   = "quota_exhausted"

	// minPriorityForTrafficScore is the minimum structural priority a page must have
	// for traffic-score updates to be applied when discovered via link-following.
	// Based on 0.9^3 ≈ 0.729 (homepage = 1.000, each link level × 0.9), representing
	// ~3 link-hops from the homepage. Pages deeper than this are too numerous and their
	// ordering matters less to crawl efficiency. Sitemap sources are always eligible
	// regardless of priority.
	minPriorityForTrafficScore = 0.729
)

type queueLane string

const (
	queueLaneBulk    queueLane = "bulk"
	queueLaneControl queueLane = "control"
)

// NewDbQueue creates a PostgreSQL job queue
func NewDbQueue(db *DB) *DbQueue {
	warn := parseThresholdEnv("DB_POOL_WARN_THRESHOLD", defaultPoolWarnThreshold)
	reject := parseThresholdEnv("DB_POOL_REJECT_THRESHOLD", defaultPoolRejectThreshold)

	// Ensure thresholds are sane and warn <= reject
	if reject <= 0 || reject > 1 {
		reject = defaultPoolRejectThreshold
	}
	if warn <= 0 || warn >= reject {
		warn = reject - 0.05
		if warn <= 0 {
			warn = defaultPoolWarnThreshold
		}
	}

	reserve := parseReservedConnections()
	queueLimit := parseIntEnv("DB_QUEUE_MAX_CONCURRENCY", defaultQueueConcurrency)
	txRetries := parseIntEnv("DB_TX_MAX_RETRIES", defaultTxRetries)
	baseDelay := parseDurationEnvMS("DB_TX_BACKOFF_BASE_MS", defaultRetryBaseDelay)
	maxDelay := max(parseDurationEnvMS("DB_TX_BACKOFF_MAX_MS", defaultRetryMaxDelay), baseDelay)

	var semaphore chan struct{}
	var controlSemaphore chan struct{}
	maxConcurrent := queueLimit
	controlConcurrent := defaultControlConcurrency
	if db != nil && db.config != nil {
		maxOpen := db.config.MaxOpenConns
		if maxOpen > 0 {
			available := max(maxOpen-reserve, 1)
			bulkCapacity := max(available-defaultControlConcurrency, 1)
			if queueLimit > 0 && queueLimit < bulkCapacity {
				maxConcurrent = queueLimit
			} else {
				maxConcurrent = bulkCapacity
			}
			controlConcurrent = max(available-maxConcurrent, 1)
		}
	}

	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if controlConcurrent < 1 {
		controlConcurrent = 1
	}

	semaphore = make(chan struct{}, maxConcurrent)
	controlSemaphore = make(chan struct{}, controlConcurrent)

	q := &DbQueue{
		db:                  db,
		poolWarnThreshold:   warn,
		poolRejectThreshold: reject,
		poolSemaphore:       semaphore,
		controlSemaphore:    controlSemaphore,
		preserveConnections: reserve,
		maxConcurrent:       maxConcurrent,
		controlConcurrent:   controlConcurrent,
		maxTxRetries:        txRetries,
		retryBaseDelay:      baseDelay,
		retryMaxDelay:       maxDelay,
		rng:                 nil, // Initialised on first use in randInt63n
	}

	q.pressure = newPressureController(maxConcurrent)
	q.pressure.OnAdjust = func(direction string) {
		observability.RecordDBPressureAdjustment(context.Background(), direction)
	}
	return q
}

// SetConcurrencyOverride sets a callback to retrieve effective concurrency from the domain limiter
func (q *DbQueue) SetConcurrencyOverride(fn ConcurrencyOverrideFunc) {
	q.concurrencyOverride = fn
}

func parseThresholdEnv(key string, fallback float64) float64 {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func parseReservedConnections() int {
	const defaultReserve = 4
	if val := strings.TrimSpace(os.Getenv("DB_POOL_RESERVED_CONNECTIONS")); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			if parsed < 0 {
				return 0
			}
			return parsed
		}
	}
	return defaultReserve
}

func parseIntEnv(key string, fallback int) int {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return fallback
}

func parseDurationEnvMS(key string, fallback time.Duration) time.Duration {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			if parsed < 0 {
				return fallback
			}
			return time.Duration(parsed) * time.Millisecond
		}
	}
	return fallback
}

// Execute runs a bulk-lane database operation in a transaction.
func (q *DbQueue) Execute(ctx context.Context, fn func(*sql.Tx) error) error {
	return q.executeWithContextLane(ctx, queueLaneBulk, func(_ context.Context, tx *sql.Tx) error {
		return fn(tx)
	})
}

// ExecuteControl runs a control-lane database operation in a transaction.
func (q *DbQueue) ExecuteControl(ctx context.Context, fn func(*sql.Tx) error) error {
	return q.executeWithContextLane(ctx, queueLaneControl, func(_ context.Context, tx *sql.Tx) error {
		return fn(tx)
	})
}

// ExecuteWithContext runs a bulk-lane transactional operation with full context propagation.
func (q *DbQueue) ExecuteWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	return q.executeWithContextLane(ctx, queueLaneBulk, fn)
}

// ExecuteControlWithContext runs a control-lane transactional operation with full context propagation.
func (q *DbQueue) ExecuteControlWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	return q.executeWithContextLane(ctx, queueLaneControl, fn)
}

func (q *DbQueue) executeWithContextLane(ctx context.Context, lane queueLane, fn func(context.Context, *sql.Tx) error) error {
	totalStart := time.Now()

	// Add timeout to context if none exists
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		timeout := defaultExecuteTimeout
		if lane == queueLaneControl {
			timeout = controlExecuteTimeout
		}
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	maxAttempts := max(q.maxTxRetries, 1)

	var lastErr error
	var poolWaitTotal time.Duration
	var execTotal time.Duration

	for attempt := range maxAttempts {
		poolWaitStart := time.Now()
		release, err := q.ensurePoolCapacity(ctx, lane)
		poolWait := time.Since(poolWaitStart)
		poolWaitTotal += poolWait

		if err != nil {
			// Classify the error type for observability
			errorClass := classifyError(err)

			if !q.shouldRetry(err) || attempt == maxAttempts-1 {
				// Log pool saturation details
				queueLog.Warn("Failed to acquire database connection",
					"error", err,
					"lane", string(lane),
					"error_class", errorClass,
					"pool_wait_total", poolWaitTotal,
					"total_duration", time.Since(totalStart),
					"attempt", attempt+1,
					"max_attempts", maxAttempts,
				)
				return err
			}

			backoff := q.computeBackoff(attempt)
			queueLog.Debug("Pool capacity check failed, retrying after backoff",
				"error", err,
				"lane", string(lane),
				"error_class", errorClass,
				"backoff", backoff,
				"attempt", attempt+1,
			)

			if err := q.waitForRetry(ctx, attempt); err != nil {
				return err
			}
			continue
		}

		execStart := time.Now()
		execErr := q.executeOnceWithContext(ctx, fn)
		execDuration := time.Since(execStart)
		execTotal += execDuration
		release()

		if execErr == nil {
			totalDuration := time.Since(totalStart)
			if lane == queueLaneBulk && q.pressure != nil {
				q.pressure.Record(float64(execTotal.Milliseconds()))
			}
			// Log if transaction was slow (>5s) to help diagnose performance issues
			if totalDuration > 5*time.Second {
				queueLog.Warn("Slow database transaction completed",
					"lane", string(lane),
					"total_duration", totalDuration,
					"pool_wait_total", poolWaitTotal,
					"exec_total", execTotal,
					"attempts", attempt+1,
				)
			}
			return nil
		}

		lastErr = execErr
		if errors.Is(execErr, sql.ErrNoRows) {
			totalDuration := time.Since(totalStart)
			if lane == queueLaneBulk && q.pressure != nil {
				q.pressure.Record(float64(execTotal.Milliseconds()))
			}
			queueLog.Debug("Database transaction finished with no rows",
				"error", execErr,
				"lane", string(lane),
				"total_duration", totalDuration,
				"pool_wait_total", poolWaitTotal,
				"exec_total", execTotal,
				"attempt", attempt+1,
			)
			return execErr
		}
		// Don't log concurrency blocking as error - it's normal backoff behaviour.
		// Still record exec time so the pressure controller sees the signal.
		if errors.Is(execErr, ErrConcurrencyBlocked) {
			if lane == queueLaneBulk && q.pressure != nil {
				q.pressure.Record(float64(execTotal.Milliseconds()))
			}
			return execErr
		}

		errorClass := classifyError(execErr)

		if !q.shouldRetry(execErr) || attempt == maxAttempts-1 {
			totalDuration := time.Since(totalStart)
			if lane == queueLaneBulk && q.pressure != nil {
				q.pressure.Record(float64(execTotal.Milliseconds()))
			}
			queueLog.Error("Database transaction failed",
				"error", execErr,
				"lane", string(lane),
				"error_class", errorClass,
				"total_duration", totalDuration,
				"pool_wait_total", poolWaitTotal,
				"exec_total", execTotal,
				"attempt", attempt+1,
				"max_attempts", maxAttempts,
				"retryable", q.shouldRetry(execErr),
			)
			return execErr
		}

		backoff := q.computeBackoff(attempt)
		queueLog.Debug("Transaction failed, retrying after backoff",
			"error", execErr,
			"lane", string(lane),
			"error_class", errorClass,
			"backoff", backoff,
			"exec_duration", execDuration,
			"attempt", attempt+1,
		)

		if err := q.waitForRetry(ctx, attempt); err != nil {
			return err
		}
	}

	return lastErr
}

func (q *DbQueue) executeOnceWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	beginStart := time.Now()
	tx, err := q.db.client.BeginTx(ctx, nil)
	beginDuration := time.Since(beginStart)

	if err != nil {
		queueLog.Error("Failed to begin transaction",
			"error", err,
			"begin_duration", beginDuration,
		)
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	var committed bool
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := applyLocalStatementTimeout(ctx, tx); err != nil {
		// Roll back eagerly for the same reason as the fn() error
		// path below — don't depend on the deferred Rollback firing
		// promptly when intervening code (caller, logging) could
		// block.
		_ = tx.Rollback()
		committed = true
		return err
	}

	queryStart := time.Now()
	if err := fn(ctx, tx); err != nil {
		queryDuration := time.Since(queryStart)
		// Roll back IMMEDIATELY, before any logging or other downstream
		// work. Production has shown the goroutine can wedge between an
		// error return and the deferred Rollback (e.g. on a blocked
		// stdout pipe inside slog), leaving Postgres sessions in
		// `idle in transaction (aborted)` for tens of minutes. Calling
		// Rollback eagerly releases the connection regardless of what
		// happens afterwards; the deferred Rollback is suppressed via
		// committed=true so it won't fire again.
		_ = tx.Rollback()
		committed = true
		// Log slow queries even when they fail
		if queryDuration > 5*time.Second {
			queueLog.Warn("Slow query failed in transaction",
				"error", err,
				"begin_duration", beginDuration,
				"query_duration", queryDuration,
			)
		}
		return err
	}
	queryDuration := time.Since(queryStart)

	commitStart := time.Now()
	if err := tx.Commit(); err != nil {
		commitDuration := time.Since(commitStart)
		queueLog.Error("Failed to commit transaction",
			"error", err,
			"begin_duration", beginDuration,
			"query_duration", queryDuration,
			"commit_duration", commitDuration,
		)
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	commitDuration := time.Since(commitStart)

	// Log breakdown for slow transactions
	totalDuration := beginDuration + queryDuration + commitDuration
	if totalDuration > 5*time.Second {
		queueLog.Warn("Slow transaction breakdown",
			"total", totalDuration,
			"begin", beginDuration,
			"query", queryDuration,
			"commit", commitDuration,
		)
	}

	return nil
}

func applyLocalStatementTimeout(ctx context.Context, tx *sql.Tx) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}

	remaining := time.Until(deadline)
	if remaining <= 0 {
		remaining = time.Millisecond
	}

	timeoutMs := remaining.Milliseconds()
	if timeoutMs < 1 {
		timeoutMs = 1
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = '%dms'", timeoutMs)); err != nil {
		return fmt.Errorf("failed to set local statement timeout: %w", err)
	}

	return nil
}

// classifyError categorises errors for observability and retry logging
func classifyError(err error) string {
	if err == nil {
		return "none"
	}

	if errors.Is(err, context.Canceled) {
		return "context_cancelled"
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}

	if errors.Is(err, ErrPoolSaturated) {
		return "pool_saturated"
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "network_timeout"
		}
		return "network_error"
	}

	errStr := strings.ToLower(err.Error())

	if strings.Contains(errStr, "sqlstate") {
		return "postgres_error"
	}
	if strings.Contains(errStr, "too many clients") {
		return "too_many_clients"
	}
	if strings.Contains(errStr, "connection reset") {
		return "connection_reset"
	}
	if strings.Contains(errStr, "connection refused") {
		return "connection_refused"
	}
	if strings.Contains(errStr, "timeout") {
		return "timeout"
	}

	return "unknown"
}

func (q *DbQueue) shouldRetry(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	if errors.Is(err, ErrPoolSaturated) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
	}

	errStr := strings.ToLower(err.Error())
	retrySnippets := []string{
		"failed to begin transaction",
		"failed to commit transaction",
		"too many clients",
		"connection reset",
		"connection refused",
		"timeout",
		"connection pool saturated",
		"deadlock detected",
	}
	for _, snippet := range retrySnippets {
		if strings.Contains(errStr, snippet) {
			return true
		}
	}

	return false
}

func (q *DbQueue) waitForRetry(ctx context.Context, attempt int) error {
	backoff := q.computeBackoff(attempt)
	select {
	case <-time.After(backoff):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *DbQueue) computeBackoff(attempt int) time.Duration {
	base := q.retryBaseDelay
	max := q.retryMaxDelay
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	if max < base {
		max = base
	}

	factor := 1 << attempt
	delay := min(time.Duration(factor)*base, max)

	jitterRange := delay / 5
	if jitterRange <= 0 {
		return delay
	}
	return delay - jitterRange + time.Duration(q.randInt63n(int64(jitterRange*2)))
}

func (q *DbQueue) randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	// Use crypto/rand for security compliance, even for non-sensitive jitter
	res, err := crand.Int(crand.Reader, big.NewInt(n))
	if err == nil {
		return res.Int64()
	}
	// Fallback to basic rand if crypto fails
	return rand.Int63n(n) //nolint:gosec // safe fallback for non-sensitive jitter
}

// ExecuteMaintenance runs a low-impact transaction that bypasses pool saturation guards.
// This is intended for housekeeping tasks that must always run, even when the pool is busy.
func (q *DbQueue) ExecuteMaintenance(ctx context.Context, fn func(*sql.Tx) error) error {
	if q == nil || q.db == nil || q.db.client == nil {
		return fmt.Errorf("maintenance transaction requires an initialised database connection")
	}

	// Keep maintenance units short-lived to minimise pool impact.
	// Allow 65s to accommodate recovery batches processing large backlogs.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 65*time.Second)
		defer cancel()
	}

	tx, err := q.db.client.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		queueLog.Error("Failed to begin maintenance transaction", "error", err)
		return fmt.Errorf("failed to begin maintenance transaction: %w", err)
	}
	var committed bool
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Apply a statement timeout so maintenance never blocks the pool indefinitely.
	// Set to 60s to allow recovery batches time to process large backlogs.
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '60s'`); err != nil {
		queueLog.Warn("Failed to set maintenance statement timeout", "error", err)
	}

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		queueLog.Error("Failed to commit maintenance transaction", "error", err)
		return fmt.Errorf("failed to commit maintenance transaction: %w", err)
	}
	committed = true

	return nil
}

func (q *DbQueue) ensurePoolCapacity(ctx context.Context, lane queueLane) (func(), error) {
	noop := func() {}
	if q == nil || q.db == nil || q.db.client == nil {
		return noop, nil
	}

	release := noop
	semaphore := q.poolSemaphore
	if lane == queueLaneControl {
		semaphore = q.controlSemaphore
	}
	if semaphore != nil {
		// Enforce the pressure-adjusted soft limit before blocking on the channel.
		// len(poolSemaphore) == current in-flight count (send=acquire, recv=release).
		// The check is intentionally non-blocking: a small overshoot is acceptable
		// because the channel hard cap still holds.
		if lane == queueLaneBulk && q.pressure != nil {
			if len(semaphore) >= int(q.pressure.EffectiveLimit()) {
				queueLog.Debug("DB pressure soft limit reached — shedding request",
					"lane", string(lane),
					"soft_limit", q.pressure.EffectiveLimit(),
					"in_flight", len(semaphore),
					"exec_ema_ms", q.pressure.EMA(),
				)
				observability.RecordDBPoolRejection(ctx)
				return noop, ErrPoolSaturated
			}
		}

		semWaitStart := time.Now()
		select {
		case semaphore <- struct{}{}:
			observability.RecordSemaphoreWait(ctx, float64(time.Since(semWaitStart))/float64(time.Millisecond))
			release = func() { <-semaphore }
		case <-ctx.Done():
			// Explicitly log pool rejections when context expires before acquiring semaphore
			queueLog.Debug("Pool semaphore rejected request - context done before acquiring slot",
				"lane", string(lane),
				"error", ctx.Err(),
				"semaphore_capacity", cap(semaphore),
				"semaphore_in_use", len(semaphore),
			)
			observability.RecordDBPoolRejection(ctx)
			return noop, ErrPoolSaturated
		}
	}

	// Check fd pressure before acquiring a DB connection
	fdCurrent, fdLimit, fdErr := util.FDUsage()
	if fdErr == nil {
		fdPressure := util.FDPressureFrom(fdCurrent, fdLimit)
		observability.RecordFDStats(ctx, fdCurrent, fdLimit, fdPressure)
		if fdPressure > fdPressureThreshold {
			queueLog.Warn("File descriptor pressure critical — rejecting DB operation",
				"lane", string(lane),
				"fd_current", fdCurrent,
				"fd_limit", fdLimit,
				"fd_pressure", fdPressure,
			)
			release()
			return noop, ErrPoolSaturated
		}
	}

	stats := q.db.client.Stats()
	maxOpen := stats.MaxOpenConnections
	if maxOpen == 0 && q.db.config != nil {
		maxOpen = q.db.config.MaxOpenConns
	}
	if maxOpen <= 0 {
		return release, nil
	}

	usage := float64(stats.InUse) / float64(maxOpen)

	observability.RecordDBPoolStats(ctx, observability.DBPoolSnapshot{
		InUse:        stats.InUse,
		Idle:         stats.Idle,
		WaitCount:    stats.WaitCount,
		WaitDuration: stats.WaitDuration,
		MaxOpen:      maxOpen,
		Reserved:     q.preserveConnections,
		Usage:        usage,
	})
	if q.pressure != nil {
		observability.RecordDBPressureStats(ctx, q.pressure.EMA(), q.pressure.EffectiveLimit())
	}

	waitDelta := stats.WaitCount - q.lastRejectWaitCount
	if usage >= q.poolRejectThreshold && waitDelta >= minRejectWaitDelta && time.Since(q.lastRejectLog) > poolLogCooldown {
		queueLog.Warn("DB pool saturated: requests will queue",
			"lane", string(lane),
			"in_use", stats.InUse,
			"max_open", maxOpen,
			"reserved", q.preserveConnections,
			"idle", stats.Idle,
			"usage", usage,
			"wait_count", stats.WaitCount,
			"wait_delta", waitDelta,
			"event_type", "db_pool",
			"state", "queue",
		)
		q.lastRejectLog = time.Now()
		q.lastRejectWaitCount = stats.WaitCount
	}

	if usage >= q.poolWarnThreshold && time.Since(q.lastWarnLog) > poolLogCooldown {
		queueLog.Warn("DB pool nearing capacity",
			"lane", string(lane),
			"in_use", stats.InUse,
			"max_open", maxOpen,
			"reserved", q.preserveConnections,
			"usage", usage,
			"wait_count", stats.WaitCount,
		)
		q.lastWarnLog = time.Now()
	}

	return release, nil
}

// Task represents a task in the queue
type Task struct {
	ID          string
	JobID       string
	PageID      int
	Host        string
	Path        string
	Status      string
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
	RetryCount  int
	Error       string
	SourceType  string
	SourceURL   string

	// Result data
	StatusCode          int
	ResponseTime        int64
	CacheStatus         string
	ContentType         string
	ContentLength       int64
	Headers             []byte // Stored as JSONB
	RedirectURL         string
	DNSLookupTime       int64
	TCPConnectionTime   int64
	TLSHandshakeTime    int64
	TTFB                int64
	ContentTransferTime int64

	// Second request data
	SecondResponseTime        int64
	SecondCacheStatus         string
	SecondContentLength       int64
	SecondHeaders             []byte // Stored as JSONB
	SecondDNSLookupTime       int64
	SecondTCPConnectionTime   int64
	SecondTLSHandshakeTime    int64
	SecondTTFB                int64
	SecondContentTransferTime int64
	CacheCheckAttempts        []byte // Stored as JSONB
	RequestDiagnostics        []byte // Stored as JSONB
	HTMLStorageBucket         string
	HTMLStoragePath           string
	HTMLContentType           string
	HTMLContentEncoding       string
	HTMLSizeBytes             int64
	HTMLCompressedSizeBytes   int64
	HTMLSHA256                string
	HTMLCapturedAt            time.Time

	// Priority
	PriorityScore float64
}

// TaskHTMLMetadata stores storage metadata for captured task HTML.
type TaskHTMLMetadata struct {
	StorageBucket       string
	StoragePath         string
	ContentType         string
	ContentEncoding     string
	SizeBytes           int64
	CompressedSizeBytes int64
	SHA256              string
	CapturedAt          time.Time
	// ArchivedAt, when non-zero, marks the row as already in cold storage.
	// Direct-to-R2 writes set this to CapturedAt so the archive sweep
	// candidate query skips the row immediately.
	ArchivedAt time.Time
}

// TaskHTMLMetadataRow pairs a task ID with its HTML metadata for batch
// UPDATE calls.
type TaskHTMLMetadataRow struct {
	TaskID   string
	Metadata TaskHTMLMetadata
}

// enqueueJobConfig holds configuration fetched from the database for task enqueueing
type enqueueJobConfig struct {
	maxPages         int
	concurrency      sql.NullInt64
	runningTasks     int
	pendingTaskCount int
	domainID         sql.NullInt64
	domainName       sql.NullString
	orgID            sql.NullString
	quotaRemaining   sql.NullInt64
	currentTaskCount int
	status           string
}

// terminalJobStatuses are job statuses past which no further task
// inserts should land. The set mirrors the application-side
// JobStatusCompleted/Failed/Cancelled/Archived/Blocked values.
// Defined as a slice so the SQL ANY($N) form stays readable.
var terminalJobStatuses = []string{"completed", "failed", "cancelled", "archived", "blocked"}

// IsTerminalJobStatus reports whether the supplied status string is a
// terminal job state past which task enqueueing should be a no-op.
// Exposed for use in callers that hold a status they read separately
// IsTerminalJobStatus reports whether the provided job status is a terminal state.
// It returns `true` if the status is one of "completed", "failed", "cancelled", "archived", or "blocked", `false` otherwise.
func IsTerminalJobStatus(status string) bool {
	for _, t := range terminalJobStatuses {
		if status == t {
			return true
		}
	}
	return false
}

// deduplicatePages removes duplicate pages, keeping highest priority for each page ID
func deduplicatePages(pages []Page) []Page {
	uniquePages := make([]Page, 0, len(pages))
	seen := make(map[int]int, len(pages))
	for _, page := range pages {
		if page.ID == 0 {
			continue
		}
		if idx, ok := seen[page.ID]; ok {
			if page.Priority > uniquePages[idx].Priority {
				uniquePages[idx].Priority = page.Priority
			}
			continue
		}
		seen[page.ID] = len(uniquePages)
		uniquePages = append(uniquePages, page)
	}
	return uniquePages
}

// calculateEffectiveConcurrency applies domain limiter override if applicable
func (q *DbQueue) calculateEffectiveConcurrency(
	jobID string,
	concurrency sql.NullInt64,
	domainName sql.NullString,
) sql.NullInt64 {
	if q.concurrencyOverride == nil || !domainName.Valid {
		return concurrency
	}
	override := q.concurrencyOverride(jobID, domainName.String)
	if override <= 0 {
		return concurrency
	}
	// Use the minimum of configured concurrency and limiter override
	if !concurrency.Valid || concurrency.Int64 == 0 {
		return sql.NullInt64{Int64: int64(override), Valid: true}
	}
	if int64(override) < concurrency.Int64 {
		return sql.NullInt64{Int64: int64(override), Valid: true}
	}
	return concurrency
}

// calculateAvailableSlots determines how many pending tasks can be created
// Returns availableSlots and whether quota was the limiting factor
func calculateAvailableSlots(
	effectiveConcurrency sql.NullInt64,
	runningTasks, pendingTaskCount int,
	quotaRemaining sql.NullInt64,
) (slots int, quotaLimited bool) {
	const maxPendingQueueSize = 100

	if !effectiveConcurrency.Valid || effectiveConcurrency.Int64 == 0 {
		// Even for unlimited concurrency jobs, cap pending queue to prevent flooding
		used := runningTasks + pendingTaskCount
		slots = max(maxPendingQueueSize-used, 0)
	} else {
		// Capacity = concurrency - (running + existing_pending)
		capacity := int(effectiveConcurrency.Int64)
		used := runningTasks + pendingTaskCount
		slots = max(capacity-used, 0)
	}

	// Cap available slots by daily quota remaining (if org has quota system)
	if quotaRemaining.Valid {
		remaining := int(quotaRemaining.Int64)
		if remaining <= 0 {
			// Quota exhausted or exceeded - no new pending tasks
			slots = 0
			quotaLimited = true
		} else if remaining < slots {
			slots = remaining
			quotaLimited = true
		}
	}

	return slots, quotaLimited
}

type enqueueTaskDisposition string

const (
	enqueueTaskPending enqueueTaskDisposition = "pending"
	enqueueTaskWaiting enqueueTaskDisposition = "waiting"
	enqueueTaskDrop    enqueueTaskDisposition = "drop"
)

func classifyEnqueuedTask(maxPages, currentTaskCount, pendingCount, waitingCount, availableSlots int) enqueueTaskDisposition {
	if maxPages != 0 && currentTaskCount+pendingCount+waitingCount >= maxPages {
		return enqueueTaskDrop
	}
	if pendingCount < availableSlots {
		return enqueueTaskPending
	}
	return enqueueTaskWaiting
}

// EnqueueURLs adds multiple URLs as tasks for a job
func (q *DbQueue) EnqueueURLs(ctx context.Context, jobID string, pages []Page, sourceType string, sourceURL string) error {
	if len(pages) == 0 {
		return nil
	}

	// Declared outside Execute so cfg is accessible after the transaction commits
	// and the job lock is released (used by the traffic-score update below).
	var cfg enqueueJobConfig

	err := q.Execute(ctx, func(tx *sql.Tx) error {
		uniquePages := deduplicatePages(pages)
		if len(uniquePages) == 0 {
			return nil
		}

		// Sort by conflict key (job_id is constant here; page_id determines order) so
		// concurrent transactions acquire row locks in the same order, preventing deadlocks.
		sort.Slice(uniquePages, func(i, j int) bool {
			return uniquePages[i].ID < uniquePages[j].ID
		})

		// Get job's max_pages, concurrency, domain, org, status, and current
		// task counts. Reading j.status under the same FOR UPDATE OF j as
		// the INSERT below makes the terminal-state guard race-free against
		// a concurrent BlockJob/CancelJob: either we acquire the lock
		// first and see the pre-terminal status (the terminal write blocks
		// until our tx commits, then it sees zero matching pending rows),
		// or it commits first and we see the terminal status here and
		// short-circuit without inserting orphan tasks.
		// total_tasks - skipped_tasks is maintained incrementally by triggers and
		// avoids the correlated COUNT(*) subquery that previously ran under the job lock.
		err := tx.QueryRowContext(ctx, `
			SELECT j.max_pages, j.concurrency, j.running_tasks, j.pending_tasks, j.domain_id, d.name,
				   j.total_tasks - j.skipped_tasks,
				   j.organisation_id,
				   CASE WHEN j.organisation_id IS NOT NULL
				        THEN get_daily_quota_remaining(j.organisation_id)
				        ELSE NULL
				   END,
				   j.status
			FROM jobs j
			LEFT JOIN domains d ON j.domain_id = d.id
			WHERE j.id = $1
			FOR UPDATE OF j
		`, jobID).Scan(&cfg.maxPages, &cfg.concurrency, &cfg.runningTasks, &cfg.pendingTaskCount,
			&cfg.domainID, &cfg.domainName, &cfg.currentTaskCount, &cfg.orgID, &cfg.quotaRemaining,
			&cfg.status)
		if err != nil {
			return fmt.Errorf("failed to get job configuration and task count: %w", err)
		}

		// Terminal-status guard: skip the entire INSERT path so a sitemap
		// loop or link-discovery callback that's still running after a
		// BlockJob/CancelJob doesn't accrete orphan tasks against the
		// terminal job. Issue #365 row 1: kmart.com.au saw 32k+ rows
		// land in tasks for a job that had already transitioned to
		// `blocked` mid-discovery.
		if IsTerminalJobStatus(cfg.status) {
			return nil
		}

		// Calculate available slots with concurrency override and quota limits
		effectiveConcurrency := q.calculateEffectiveConcurrency(jobID, cfg.concurrency, cfg.domainName)
		concurrencySlots, _ := calculateAvailableSlots(effectiveConcurrency, cfg.runningTasks, cfg.pendingTaskCount, sql.NullInt64{})
		availableSlots, quotaLimited := calculateAvailableSlots(effectiveConcurrency, cfg.runningTasks, cfg.pendingTaskCount, cfg.quotaRemaining)

		// Ensure we don't try to create more tasks than we have pages
		if availableSlots > len(uniquePages) {
			availableSlots = len(uniquePages)
		}

		// Count how many tasks will be pending/waiting vs dropped.
		// Overflow pages above max_pages are not materialised as skipped tasks:
		// they were never startable and would otherwise pollute task counts and dashboards.
		pendingCount := 0
		waitingCount := 0
		droppedCount := 0
		for range uniquePages {
			switch classifyEnqueuedTask(cfg.maxPages, cfg.currentTaskCount, pendingCount, waitingCount, availableSlots) {
			case enqueueTaskPending:
				pendingCount++
			case enqueueTaskWaiting:
				waitingCount++
			case enqueueTaskDrop:
				droppedCount++
			}
		}

		// Use array-based insert to minimise round-trips and leverage Postgres batching.
		// A CTE (WITH ins AS ...) mirrors the rows the INSERT actually wrote (or updated
		// via ON CONFLICT) into task_outbox. The ON CONFLICT guard suppresses updates
		// for tasks already in a terminal state (running/completed/failed), so those
		// rows do not appear in ins.RETURNING and no spurious outbox row is produced.
		insertQuery := `
			WITH ins AS (
				INSERT INTO tasks (
					id, job_id, page_id, host, path, status, created_at, retry_count,
					source_type, source_url, priority_score
				)
				SELECT
					unnest_ids,
					unnest_job_ids,
					unnest_page_ids,
					unnest_hosts,
					unnest_paths,
					unnest_statuses,
					unnest_created_at,
					unnest_retry_counts,
					unnest_source_types,
					unnest_source_urls,
					unnest_priorities
				FROM UNNEST(
					$1::uuid[],
					$2::uuid[],
					$3::int[],
					$4::text[],
					$5::text[],
					$6::text[],
					$7::timestamptz[],
					$8::int[],
					$9::text[],
					$10::text[],
					$11::double precision[]
				) AS t(
					unnest_ids,
					unnest_job_ids,
					unnest_page_ids,
					unnest_hosts,
					unnest_paths,
					unnest_statuses,
					unnest_created_at,
					unnest_retry_counts,
					unnest_source_types,
					unnest_source_urls,
					unnest_priorities
				)
				ON CONFLICT (job_id, page_id) DO UPDATE
				SET status = EXCLUDED.status,
					host = EXCLUDED.host,
					created_at = EXCLUDED.created_at,
					retry_count = EXCLUDED.retry_count,
					source_type = EXCLUDED.source_type,
					source_url = EXCLUDED.source_url,
					priority_score = GREATEST(tasks.priority_score, EXCLUDED.priority_score),
					started_at = NULL,
					completed_at = NULL,
					error = NULL
				WHERE tasks.status IN ('pending', 'waiting', 'skipped')
				RETURNING id, job_id, page_id, host, path, priority_score,
				          retry_count, source_type, source_url, created_at, status
			)
			INSERT INTO task_outbox (
				task_id, job_id, page_id, host, path,
				priority, retry_count, source_type, source_url, run_at
			)
			SELECT id, job_id, page_id, host, path,
			       priority_score, retry_count, source_type, source_url, created_at
			FROM ins
			WHERE status IN ('pending', 'waiting')
			ON CONFLICT (task_id) DO NOTHING
		`

		now := time.Now().UTC()
		processedPending := 0
		processedWaiting := 0

		var (
			taskIDs     []string
			jobIDs      []string
			pageIDs     []int
			hosts       []string
			paths       []string
			statuses    []string
			createdAts  []time.Time
			retryCounts []int
			sourceTypes []string
			sourceURLs  []string
			priorities  []float64
		)

		for _, page := range uniquePages {
			if page.ID == 0 {
				continue
			}

			host := page.Host
			if host == "" && cfg.domainName.Valid {
				host = cfg.domainName.String
			}
			if host == "" {
				queueLog.Warn("Skipping page enqueue due to missing host", "job_id", jobID, "page_id", page.ID)
				continue
			}

			disposition := classifyEnqueuedTask(
				cfg.maxPages,
				cfg.currentTaskCount,
				processedPending,
				processedWaiting,
				availableSlots,
			)
			if disposition == enqueueTaskDrop {
				continue
			}

			status := string(disposition)
			if disposition == enqueueTaskPending {
				processedPending++
			} else {
				processedWaiting++
			}

			taskIDs = append(taskIDs, uuid.New().String())
			jobIDs = append(jobIDs, jobID)
			pageIDs = append(pageIDs, page.ID)
			hosts = append(hosts, host)
			paths = append(paths, page.Path)
			statuses = append(statuses, status)
			createdAts = append(createdAts, now)
			retryCounts = append(retryCounts, 0)
			sourceTypes = append(sourceTypes, sourceType)
			// Only store source_url for 'link' source type (pages discovered from other pages)
			if sourceType == "link" {
				sourceURLs = append(sourceURLs, sourceURL)
			} else {
				sourceURLs = append(sourceURLs, "")
			}
			priorities = append(priorities, page.Priority)
		}

		if len(taskIDs) == 0 {
			return nil
		}

		_, err = tx.ExecContext(ctx, insertQuery,
			pq.Array(taskIDs),
			pq.Array(jobIDs),
			pq.Array(pageIDs),
			pq.Array(hosts),
			pq.Array(paths),
			pq.Array(statuses),
			pq.Array(createdAts),
			pq.Array(retryCounts),
			pq.Array(sourceTypes),
			pq.Array(sourceURLs),
			pq.Array(priorities),
		)

		if err != nil {
			return fmt.Errorf("failed to insert tasks: %w", err)
		}

		// Note: the same statement also populates task_outbox via the CTE above,
		// so a successful commit guarantees every newly-pending/waiting task has
		// a matching outbox row for the sweeper to pick up.

		// Note: Daily usage is incremented when tasks COMPLETE, not when created
		// See batch.go FlushUpdates for quota increment on completion/failure

		// Log when tasks are placed in waiting status
		if processedWaiting > 0 {
			waitingReason := waitingReasonConcurrencyLimit
			if quotaLimited {
				waitingReason = waitingReasonQuotaExhausted
			}
			observability.RecordTaskWaiting(ctx, jobID, waitingReason, processedWaiting)

			if quotaLimited {
				queueLog.Debug("Created tasks in waiting status due to daily quota limit",
					"job_id", jobID,
					"waiting_tasks", processedWaiting,
					"pending_tasks", processedPending,
					"running_tasks", cfg.runningTasks,
					"existing_pending", cfg.pendingTaskCount,
					"available_slots", availableSlots,
					"concurrency_limit", cfg.concurrency.Int64,
					"waiting_reason", waitingReason,
					"quota_remaining", cfg.quotaRemaining.Int64,
					"concurrency_slots", concurrencySlots,
				)
			} else {
				queueLog.Debug("Created tasks in waiting status due to job concurrency limit",
					"job_id", jobID,
					"waiting_tasks", processedWaiting,
					"pending_tasks", processedPending,
					"running_tasks", cfg.runningTasks,
					"existing_pending", cfg.pendingTaskCount,
					"available_slots", availableSlots,
					"concurrency_limit", cfg.concurrency.Int64,
					"waiting_reason", waitingReason,
				)
			}
		}

		if droppedCount > 0 {
			queueLog.Debug("Dropped overflow tasks at max_pages limit",
				"job_id", jobID,
				"dropped_tasks", droppedCount,
				"current_task_count", cfg.currentTaskCount,
				"max_pages", cfg.maxPages,
			)
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Apply traffic scores in a separate transaction — after the job-lock has been released —
	// to reduce the time concurrent EnqueueURLs calls for the same job wait on the lock.
	// Only score pages from this batch (t.page_id = ANY(eligiblePageIDs)): tasks enqueued
	// by previous calls were already scored at their enqueue time, so a job-wide scan is
	// unnecessary. For link-discovered pages, further restrict to those at or above
	// minPriorityForTrafficScore (≈ level 3 in the hierarchy); deeper pages are too
	// numerous and their rank relative to each other matters too little to justify the join.
	if cfg.orgID.Valid && cfg.domainID.Valid {
		var eligiblePageIDs []int
		for _, p := range pages {
			if sourceType != "link" || p.Priority >= minPriorityForTrafficScore {
				eligiblePageIDs = append(eligiblePageIDs, p.ID)
			}
		}
		if len(eligiblePageIDs) > 0 {
			if err2 := q.Execute(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `
					UPDATE tasks t
					SET priority_score = GREATEST(t.priority_score, COALESCE(pa.traffic_score, 0))
					FROM pages p
					JOIN page_analytics pa ON pa.organisation_id = $1
						AND pa.domain_id = $2
						AND pa.path = p.path
					WHERE t.page_id = p.id
					AND t.job_id = $3
					AND t.page_id = ANY($4)
					AND t.status IN ('pending', 'waiting')
					AND COALESCE(pa.traffic_score, 0) > t.priority_score
				`, cfg.orgID.String, cfg.domainID.Int64, jobID, pq.Array(eligiblePageIDs))
				return err
			}); err2 != nil {
				queueLog.Warn("Failed to apply traffic scores to new tasks; continuing without scores",
					"error", err2,
					"job_id", jobID,
					"next_action", "continuing_without_traffic_scores",
				)
			}
		}
	}

	return nil
}

// CleanupStuckJobs finds and fixes jobs that are stuck in pending/running state
// despite having all their tasks completed
func (q *DbQueue) CleanupStuckJobs(ctx context.Context) error {
	// Serialize cleanup operations to prevent prepared statement conflicts
	q.cleanupMutex.Lock()
	defer q.cleanupMutex.Unlock()

	span := sentry.StartSpan(ctx, "db.cleanup_stuck_jobs")
	defer span.Finish()

	// Define status constants for job states
	const (
		JobStatusCompleted = "completed"
		JobStatusPending   = "pending"
		JobStatusRunning   = "running"
	)

	result, err := q.db.client.ExecContext(ctx, `
		UPDATE jobs
		SET status = $1,
			completed_at = COALESCE(completed_at, $2),
			progress = 100.0
		WHERE (status = $3 OR status = $4)
		AND total_tasks > 0
		AND total_tasks = completed_tasks + failed_tasks + skipped_tasks
	`, JobStatusCompleted, time.Now().UTC(), JobStatusPending, JobStatusRunning)

	if err != nil {
		span.SetTag("error", "true")
		span.SetData("error.message", err.Error())
		queueLog.Error("Failed to cleanup stuck jobs", "error", err)
		return fmt.Errorf("failed to cleanup stuck jobs: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		queueLog.Info("Fixed stuck jobs", "jobs_fixed", rowsAffected)
	}

	return nil
}

// UpdateTaskStatus updates a task's status and associated metadata in a single function
// This provides a unified way to handle various task state transitions
func (q *DbQueue) UpdateTaskStatus(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("cannot update nil task")
	}

	now := time.Now().UTC()

	// Set appropriate timestamps based on status if not already set
	if task.Status == "running" && task.StartedAt.IsZero() {
		task.StartedAt = now
	}
	if (task.Status == "completed" || task.Status == "failed") && task.CompletedAt.IsZero() {
		task.CompletedAt = now
	}

	// Update task in a transaction
	// Also adjust running_tasks counter for the job when status changes
	err := q.Execute(ctx, func(tx *sql.Tx) error {
		var err error
		var jobID string

		// Use different update logic based on status
		switch task.Status {
		case "running":
			// Increment running_tasks when manually setting to running (rare, but handle it)
			err = tx.QueryRowContext(ctx, `
				WITH task_update AS (
					UPDATE tasks
					SET status = $1, started_at = $2
					WHERE id = $3
					RETURNING job_id
				)
				UPDATE jobs
				SET running_tasks = running_tasks + 1
				FROM task_update
				WHERE jobs.id = task_update.job_id
				RETURNING task_update.job_id
			`, task.Status, task.StartedAt, task.ID).Scan(&jobID)

		case "completed":
			// Ensure JSONB fields are never nil and are valid JSON
			headers := task.Headers
			if len(headers) == 0 {
				headers = []byte("{}")
			}
			secondHeaders := task.SecondHeaders
			if len(secondHeaders) == 0 {
				secondHeaders = []byte("{}")
			}
			cacheCheckAttempts := task.CacheCheckAttempts
			if len(cacheCheckAttempts) == 0 {
				cacheCheckAttempts = []byte("[]")
			}
			requestDiagnostics := task.RequestDiagnostics
			if len(requestDiagnostics) == 0 {
				requestDiagnostics = []byte("{}")
			}

			var htmlCapturedAt any
			if !task.HTMLCapturedAt.IsZero() {
				htmlCapturedAt = task.HTMLCapturedAt
			}

			// Log the actual values being passed for debugging
			queueLog.Debug("Updating task with JSONB fields",
				"task_id", task.ID,
				"headers_bytes", len(headers),
				"second_headers_bytes", len(secondHeaders),
				"cache_check_attempts_bytes", len(cacheCheckAttempts),
				"request_diagnostics_bytes", len(requestDiagnostics),
			)

			// Update task fields only (running_tasks decremented separately via DecrementRunningTasks)
			err = tx.QueryRowContext(ctx, `
					UPDATE tasks
					SET status = $1, completed_at = $2, status_code = $3,
						response_time = $4, cache_status = $5, content_type = $6,
						content_length = $7, headers = $8::jsonb, redirect_url = $9,
						dns_lookup_time = $10, tcp_connection_time = $11, tls_handshake_time = $12,
						ttfb = $13, content_transfer_time = $14,
						second_response_time = $15, second_cache_status = $16,
						second_content_length = $17, second_headers = $18::jsonb,
						second_dns_lookup_time = $19, second_tcp_connection_time = $20,
						second_tls_handshake_time = $21, second_ttfb = $22,
						second_content_transfer_time = $23,
						retry_count = $24, cache_check_attempts = $25::jsonb,
						request_diagnostics = $26::jsonb,
						html_storage_bucket = COALESCE(NULLIF($27, ''), html_storage_bucket),
						html_storage_path = COALESCE(NULLIF($28, ''), html_storage_path),
						html_content_type = COALESCE(NULLIF($29, ''), html_content_type),
						html_content_encoding = COALESCE(NULLIF($30, ''), html_content_encoding),
						html_size_bytes = CASE WHEN $31 > 0 THEN $31 ELSE html_size_bytes END,
						html_compressed_size_bytes = CASE WHEN $32 > 0 THEN $32 ELSE html_compressed_size_bytes END,
						html_sha256 = COALESCE(NULLIF($33, ''), html_sha256),
						html_captured_at = COALESCE($34, html_captured_at)
					WHERE id = $35
					RETURNING job_id
				`, task.Status, task.CompletedAt, task.StatusCode,
				task.ResponseTime, task.CacheStatus, task.ContentType,
				task.ContentLength, string(headers), task.RedirectURL,
				task.DNSLookupTime, task.TCPConnectionTime, task.TLSHandshakeTime,
				task.TTFB, task.ContentTransferTime,
				task.SecondResponseTime, task.SecondCacheStatus,
				task.SecondContentLength, string(secondHeaders),
				task.SecondDNSLookupTime, task.SecondTCPConnectionTime,
				task.SecondTLSHandshakeTime, task.SecondTTFB,
				task.SecondContentTransferTime,
				task.RetryCount, string(cacheCheckAttempts), string(requestDiagnostics),
				task.HTMLStorageBucket, task.HTMLStoragePath, task.HTMLContentType,
				task.HTMLContentEncoding, task.HTMLSizeBytes, task.HTMLCompressedSizeBytes,
				task.HTMLSHA256, htmlCapturedAt, task.ID).Scan(&jobID)

		case "failed":
			requestDiagnostics := task.RequestDiagnostics
			if len(requestDiagnostics) == 0 {
				requestDiagnostics = []byte("{}")
			}

			// Update task fields only (running_tasks decremented separately via DecrementRunningTasks)
			err = tx.QueryRowContext(ctx, `
				UPDATE tasks
				SET status = $1, completed_at = $2, error = $3, retry_count = $4,
					request_diagnostics = $5::jsonb
				WHERE id = $6
				RETURNING job_id
			`, task.Status, task.CompletedAt, task.Error, task.RetryCount, string(requestDiagnostics), task.ID).Scan(&jobID)

		case "waiting":
			requestDiagnostics := task.RequestDiagnostics
			if len(requestDiagnostics) == 0 {
				requestDiagnostics = []byte("{}")
			}

			err = tx.QueryRowContext(ctx, `
				UPDATE tasks
				SET status = $1, started_at = NULL, error = $2, retry_count = $3,
					request_diagnostics = $4::jsonb
				WHERE id = $5
				RETURNING job_id
			`, task.Status, task.Error, task.RetryCount, string(requestDiagnostics), task.ID).Scan(&jobID)

		case "skipped":
			// Update task fields only (running_tasks decremented separately via DecrementRunningTasks)
			err = tx.QueryRowContext(ctx, `
				UPDATE tasks
				SET status = $1
				WHERE id = $2
				RETURNING job_id
			`, task.Status, task.ID).Scan(&jobID)

		case "pending":
			// Update task fields only (running_tasks decremented separately via DecrementRunningTasks)
			// The task will re-increment when claimed again
			err = tx.QueryRowContext(ctx, `
				UPDATE tasks
				SET status = $1, retry_count = $2, started_at = $3
				WHERE id = $4
				RETURNING job_id
			`, task.Status, task.RetryCount, task.StartedAt, task.ID).Scan(&jobID)

		default:
			// Generic status update
			_, err = tx.ExecContext(ctx, `
				UPDATE tasks
				SET status = $1
				WHERE id = $2
			`, task.Status, task.ID)
		}

		if err != nil {
			return fmt.Errorf("failed to update task status: %w", err)
		}

		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

// UpdateTaskHTMLMetadata persists storage metadata for an already completed task.
func (q *DbQueue) UpdateTaskHTMLMetadata(ctx context.Context, taskID string, metadata TaskHTMLMetadata) error {
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("taskID cannot be empty")
	}
	if strings.TrimSpace(metadata.StorageBucket) == "" {
		return fmt.Errorf("storage bucket cannot be empty")
	}
	if strings.TrimSpace(metadata.StoragePath) == "" {
		return fmt.Errorf("storage path cannot be empty")
	}

	var capturedAt any
	if !metadata.CapturedAt.IsZero() {
		capturedAt = metadata.CapturedAt
	}

	return q.Execute(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE tasks
			SET html_storage_bucket = COALESCE(NULLIF($2, ''), html_storage_bucket),
				html_storage_path = COALESCE(NULLIF($3, ''), html_storage_path),
				html_content_type = COALESCE(NULLIF($4, ''), html_content_type),
				html_content_encoding = COALESCE(NULLIF($5, ''), html_content_encoding),
				html_size_bytes = CASE WHEN $6 > 0 THEN $6 ELSE html_size_bytes END,
				html_compressed_size_bytes = CASE WHEN $7 > 0 THEN $7 ELSE html_compressed_size_bytes END,
				html_sha256 = COALESCE(NULLIF($8, ''), html_sha256),
				html_captured_at = COALESCE($9, html_captured_at)
			WHERE id = $1 AND status IN ('running', 'completed')
		`, taskID, metadata.StorageBucket, metadata.StoragePath, metadata.ContentType,
			metadata.ContentEncoding, metadata.SizeBytes, metadata.CompressedSizeBytes,
			metadata.SHA256, capturedAt)
		if err != nil {
			return fmt.Errorf("failed to update task HTML metadata: %w", err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to read task HTML metadata update result: %w", err)
		}
		if rowsAffected == 0 {
			var status string
			err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&status)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("task %s not found: %w", taskID, sql.ErrNoRows)
			case err != nil:
				return fmt.Errorf("failed to inspect task HTML metadata state: %w", err)
			case status == "pending" || status == "waiting":
				return fmt.Errorf("%w: %s", ErrTaskNotReadyForHTMLMetadata, taskID)
			default:
				return fmt.Errorf("task %s is not eligible for HTML metadata updates in status %q", taskID, status)
			}
		}
		return nil
	})
}

// BatchUpsertTaskHTMLMetadata stamps storage metadata onto multiple completed
// tasks in a single UPDATE. Used by the direct-to-R2 HTML persister so the
// metadata write is amortised across the batch of payloads it just uploaded.
//
// Only the html_* columns are touched — the surrounding completed-task batch
// UPDATE has already persisted status, timing and response data. ArchivedAt
// is intentionally stamped at write time so the archive sweep's candidate
// query (html_archived_at IS NULL) skips these rows.
func (q *DbQueue) BatchUpsertTaskHTMLMetadata(ctx context.Context, rows []TaskHTMLMetadataRow) error {
	if q == nil || len(rows) == 0 {
		return nil
	}

	ids := make([]string, len(rows))
	buckets := make([]string, len(rows))
	paths := make([]string, len(rows))
	contentTypes := make([]string, len(rows))
	encodings := make([]string, len(rows))
	sizes := make([]int64, len(rows))
	compressedSizes := make([]int64, len(rows))
	sha256s := make([]string, len(rows))
	capturedAts := make([]string, len(rows))
	archivedAts := make([]string, len(rows))

	for i, row := range rows {
		ids[i] = row.TaskID
		buckets[i] = row.Metadata.StorageBucket
		paths[i] = row.Metadata.StoragePath
		contentTypes[i] = row.Metadata.ContentType
		encodings[i] = row.Metadata.ContentEncoding
		sizes[i] = row.Metadata.SizeBytes
		compressedSizes[i] = row.Metadata.CompressedSizeBytes
		sha256s[i] = row.Metadata.SHA256
		if !row.Metadata.CapturedAt.IsZero() {
			capturedAts[i] = row.Metadata.CapturedAt.UTC().Format(time.RFC3339Nano)
		}
		if !row.Metadata.ArchivedAt.IsZero() {
			archivedAts[i] = row.Metadata.ArchivedAt.UTC().Format(time.RFC3339Nano)
		}
	}

	return q.ExecuteWithContext(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(txCtx, `
			UPDATE tasks
			SET html_storage_bucket      = COALESCE(NULLIF(updates.html_storage_bucket, ''), tasks.html_storage_bucket),
				html_storage_path        = COALESCE(NULLIF(updates.html_storage_path, ''), tasks.html_storage_path),
				html_content_type        = COALESCE(NULLIF(updates.html_content_type, ''), tasks.html_content_type),
				html_content_encoding    = COALESCE(NULLIF(updates.html_content_encoding, ''), tasks.html_content_encoding),
				html_size_bytes          = CASE WHEN updates.html_size_bytes > 0 THEN updates.html_size_bytes ELSE tasks.html_size_bytes END,
				html_compressed_size_bytes = CASE WHEN updates.html_compressed_size_bytes > 0 THEN updates.html_compressed_size_bytes ELSE tasks.html_compressed_size_bytes END,
				html_sha256              = COALESCE(NULLIF(updates.html_sha256, ''), tasks.html_sha256),
				html_captured_at         = COALESCE(updates.html_captured_at, tasks.html_captured_at),
				html_archived_at         = COALESCE(updates.html_archived_at, tasks.html_archived_at)
			FROM (
				SELECT
					unnest($1::text[]) AS id,
					unnest($2::text[]) AS html_storage_bucket,
					unnest($3::text[]) AS html_storage_path,
					unnest($4::text[]) AS html_content_type,
					unnest($5::text[]) AS html_content_encoding,
					unnest($6::bigint[]) AS html_size_bytes,
					unnest($7::bigint[]) AS html_compressed_size_bytes,
					unnest($8::text[]) AS html_sha256,
					NULLIF(unnest($9::text[]), '')::timestamptz AS html_captured_at,
					NULLIF(unnest($10::text[]), '')::timestamptz AS html_archived_at
			) AS updates
			WHERE tasks.id = updates.id
			  AND tasks.status IN ('running', 'completed')
		`,
			pq.Array(ids),
			pq.Array(buckets),
			pq.Array(paths),
			pq.Array(contentTypes),
			pq.Array(encodings),
			pq.Array(sizes),
			pq.Array(compressedSizes),
			pq.Array(sha256s),
			pq.Array(capturedAts),
			pq.Array(archivedAts),
		)
		if err != nil {
			return fmt.Errorf("batch upsert task html metadata: %w", err)
		}
		return nil
	})
}

// UpdateDomainTechnologies updates the detected technologies for a domain.
// Delegates to the underlying DB implementation.
func (q *DbQueue) UpdateDomainTechnologies(ctx context.Context, domainID int, technologies, headers []byte, htmlPath string) error {
	if q == nil || q.db == nil {
		return fmt.Errorf("queue not initialised")
	}
	return q.db.UpdateDomainTechnologies(ctx, domainID, technologies, headers, htmlPath)
}

// PromoteWaitingToPending moves up to `limit` waiting tasks for the job
// into pending status and inserts matching task_outbox rows so the
// OutboxSweeper picks them up on its next tick. Returns the number of
// rows promoted.
//
// Called reactively after a counter decrement in the stream worker so a
// freshly freed concurrency slot gets reused by a stranded waiting task
// rather than sitting idle until the next link-discovery burst arrives.
// Before this path existed, waiting rows never transitioned back to
// pending (classifyEnqueuedTask only runs on INSERT), so crawls on
// mature sites with heavy dedupe went idle with large waiting backlogs.
//
// Delegates the UPDATE + task_outbox INSERT to the
// promote_waiting_with_outbox SQL function (migration 20260423000001)
// so both legs execute in one server-side CTE. The prior Go-side path
// issued two round-trips and shipped ten columns back per promoted row
// just to re-serialise them into unnest() arrays for the outbox INSERT;
// at 4k completions/min that averaged ~43 ms per call and dragged the
// bulk-lane EMA above the 80 ms pressure-controller high mark.
//
// The SQL function preserves FOR UPDATE SKIP LOCKED on the waiting-row
// pick and ON CONFLICT (task_id) DO NOTHING on the outbox insert (unique
// index from migration 20260422112007), so the race window against
// EnqueueURLs is unchanged.
func (q *DbQueue) PromoteWaitingToPending(ctx context.Context, jobID string, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}

	var promoted int
	err := q.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT promote_waiting_with_outbox($1::text, $2::integer)`,
			jobID, limit,
		).Scan(&promoted)
	})
	if err != nil {
		return 0, fmt.Errorf("promote waiting->pending for job %s: %w", jobID, err)
	}
	return promoted, nil
}
