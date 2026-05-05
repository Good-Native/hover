package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/good-native/hover/internal/logging"
)

var batchLog = logging.Component("batch")

var (
	// MaxBatchSize is the maximum number of tasks to batch before forcing a flush
	MaxBatchSize = 100
	// MaxBatchInterval is the maximum time to wait before flushing a batch
	MaxBatchInterval = 2 * time.Second
	// BatchChannelSize is the buffer size for the update channel
	BatchChannelSize = 2000
	// MaxConsecutiveFailures before falling back to individual updates
	MaxConsecutiveFailures = 3
	// MaxShutdownRetries for final flush attempts
	MaxShutdownRetries = 5
	// ShutdownRetryDelay between retry attempts
	ShutdownRetryDelay = 500 * time.Millisecond
)

func init() {
	if val := strings.TrimSpace(os.Getenv("GNH_BATCH_CHANNEL_SIZE")); val != "" {
		parsed, err := strconv.Atoi(val)
		if err != nil {
			batchLog.Warn("Failed to parse GNH_BATCH_CHANNEL_SIZE override", "value", val)
		} else {
			if parsed < 500 {
				batchLog.Warn("GNH_BATCH_CHANNEL_SIZE below minimum, using 500", "requested", parsed)
				parsed = 500
			} else if parsed > 20000 {
				batchLog.Warn("GNH_BATCH_CHANNEL_SIZE above maximum, using 20000", "requested", parsed)
				parsed = 20000
			}
			BatchChannelSize = parsed
			batchLog.Info("GNH_BATCH_CHANNEL_SIZE override applied", "channel_size", parsed)
		}
	}

	// GNH_RUNNING_TASK_BATCH_SIZE mirrors the pre-Redis dial — caps the
	// number of task-state updates flushed to Postgres per batched write.
	if val := strings.TrimSpace(os.Getenv("GNH_RUNNING_TASK_BATCH_SIZE")); val != "" {
		parsed, err := strconv.Atoi(val)
		if err != nil {
			batchLog.Warn("Failed to parse GNH_RUNNING_TASK_BATCH_SIZE override", "value", val)
		} else {
			if parsed < 1 {
				batchLog.Warn("GNH_RUNNING_TASK_BATCH_SIZE below minimum, using 1", "requested", parsed)
				parsed = 1
			} else if parsed > 1000 {
				batchLog.Warn("GNH_RUNNING_TASK_BATCH_SIZE above maximum, using 1000", "requested", parsed)
				parsed = 1000
			}
			MaxBatchSize = parsed
			batchLog.Info("GNH_RUNNING_TASK_BATCH_SIZE override applied", "batch_size", parsed)
		}
	}

	// GNH_RUNNING_TASK_FLUSH_INTERVAL_MS is the authoritative name for the
	// batch-manager flush cadence (the older GNH_BATCH_MAX_INTERVAL_MS
	// remains recognised below for backward compatibility).
	if val := strings.TrimSpace(os.Getenv("GNH_RUNNING_TASK_FLUSH_INTERVAL_MS")); val != "" {
		parsed, err := strconv.Atoi(val)
		if err != nil {
			batchLog.Warn("Failed to parse GNH_RUNNING_TASK_FLUSH_INTERVAL_MS override", "value", val)
		} else {
			if parsed < 100 {
				batchLog.Warn("GNH_RUNNING_TASK_FLUSH_INTERVAL_MS below minimum, using 100ms", "requested", parsed)
				parsed = 100
			} else if parsed > 10000 {
				batchLog.Warn("GNH_RUNNING_TASK_FLUSH_INTERVAL_MS above maximum, using 10s", "requested", parsed)
				parsed = 10000
			}
			MaxBatchInterval = time.Duration(parsed) * time.Millisecond
			batchLog.Info("GNH_RUNNING_TASK_FLUSH_INTERVAL_MS override applied", "interval_ms", parsed)
		}
	}

	// Backward-compat alias: GNH_BATCH_MAX_INTERVAL_MS takes precedence if
	// also set, but emits a notice so operators move to the new name.
	if val := strings.TrimSpace(os.Getenv("GNH_BATCH_MAX_INTERVAL_MS")); val != "" {
		parsed, err := strconv.Atoi(val)
		if err != nil {
			batchLog.Warn("Failed to parse GNH_BATCH_MAX_INTERVAL_MS override", "value", val)
		} else {
			if parsed < 100 {
				batchLog.Warn("GNH_BATCH_MAX_INTERVAL_MS below minimum, using 100ms", "requested", parsed)
				parsed = 100
			} else if parsed > 10000 {
				batchLog.Warn("GNH_BATCH_MAX_INTERVAL_MS above maximum, using 10s", "requested", parsed)
				parsed = 10000
			}
			MaxBatchInterval = time.Duration(parsed) * time.Millisecond
			batchLog.Info("GNH_BATCH_MAX_INTERVAL_MS override applied (prefer GNH_RUNNING_TASK_FLUSH_INTERVAL_MS)", "interval_ms", parsed)
		}
	}
}

// isRetryableError determines if an error is infrastructure-related (should retry)
// vs data-related (poison pill that should be skipped)
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Unwrap to find the underlying PostgreSQL error
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code.Class() {
		case "08": // Connection exceptions
			return true
		case "53": // Insufficient resources (connection limit, out of memory, disk full)
			return true
		case "57": // Operator intervention (shutdown in progress, etc)
			return true
		case "58": // System errors (IO errors, etc)
			return true
		case "23": // Integrity constraint violations - NOT retryable (bad data)
			return false
		case "22": // Data exceptions (invalid input, etc) - NOT retryable (bad data)
			return false
		default:
			// For unknown postgres errors, be conservative and retry
			return true
		}
	}

	// Check for common Go database errors
	switch err {
	case sql.ErrConnDone:
		return true
	case context.DeadlineExceeded:
		return true
	case context.Canceled:
		return true
	}

	// Check error message for connection issues
	errMsg := err.Error()
	connectionErrors := []string{
		"connection refused",
		"connection reset",
		"broken pipe",
		"no such host",
		"timeout",
		"too many clients",
		"pool",
	}

	for _, connErr := range connectionErrors {
		if stringContains(errMsg, connErr) {
			return true
		}
	}

	// Default: assume it's retryable (safer than dropping data)
	return true
}

// stringContains checks if a string contains a substring
func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TaskUpdate represents a pending task status update
type TaskUpdate struct {
	Task      *Task
	UpdatedAt time.Time
}

// QueueExecutor defines the minimal interface needed for batch operations
type QueueExecutor interface {
	Execute(ctx context.Context, fn func(*sql.Tx) error) error
	ExecuteWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error
}

// BatchFlushCallback is invoked after each successful flush with the
// distinct job IDs touched by the batch. Used by the lighthouse
// scheduler to opportunistically check for progress-milestone crossings
// without internal/db importing internal/jobs (which would create a
// cycle: jobs already depends on db). Fire-and-forget — implementations
// must return promptly and own their own goroutines for any work that
// runs longer than a few milliseconds.
type BatchFlushCallback func(ctx context.Context, jobIDs []string)

// BatchManager coordinates batching of database operations
type BatchManager struct {
	queue            QueueExecutor
	updates          chan *TaskUpdate
	stopCh           chan struct{}
	wg               sync.WaitGroup
	consecutiveFails int
	mu               sync.Mutex
	overflowMu       sync.Mutex
	overflow         map[string]*TaskUpdate
	lastOverflowLog  time.Time

	// onBatchFlushed is invoked after each successful flush. Set via
	// SetOnBatchFlushed at bootstrap. nil callback is allowed.
	onBatchFlushed BatchFlushCallback
}

// SetOnBatchFlushed registers a callback fired once after each
// successful flush with the set of distinct job IDs touched. Safe to
// call once at startup; not safe to mutate concurrently with
// processUpdateBatches.
func (bm *BatchManager) SetOnBatchFlushed(cb BatchFlushCallback) {
	bm.onBatchFlushed = cb
}

// NewBatchManager creates a new batch manager
func NewBatchManager(queue QueueExecutor) *BatchManager {
	bm := &BatchManager{
		queue:    queue,
		updates:  make(chan *TaskUpdate, BatchChannelSize),
		stopCh:   make(chan struct{}),
		overflow: make(map[string]*TaskUpdate),
	}

	// Start the batch processor
	bm.wg.Add(1)
	go bm.processUpdateBatches()

	batchLog.Info("Batch manager started",
		"max_batch_size", MaxBatchSize,
		"max_batch_interval", MaxBatchInterval,
		"channel_size", BatchChannelSize,
	)

	return bm
}

// QueueTaskUpdate adds a task update to the batch queue
func (bm *BatchManager) QueueTaskUpdate(task *Task) {
	update := &TaskUpdate{
		Task:      task,
		UpdatedAt: time.Now().UTC(),
	}

	select {
	case bm.updates <- update:
		// Queued successfully
	default:
		bm.overflowMu.Lock()
		bm.overflow[task.ID] = update
		overflowDepth := len(bm.overflow)
		shouldLog := time.Since(bm.lastOverflowLog) > 5*time.Second
		if shouldLog {
			bm.lastOverflowLog = time.Now()
		}
		bm.overflowMu.Unlock()

		if shouldLog {
			batchLog.Warn("Update batch channel full, coalescing task update in overflow buffer",
				"task_id", task.ID,
				"channel_depth", len(bm.updates),
				"channel_size", BatchChannelSize,
				"overflow_depth", overflowDepth,
			)
		}
	}
}

func (bm *BatchManager) popOverflowBatch(limit int) []*TaskUpdate {
	if limit <= 0 {
		return nil
	}

	bm.overflowMu.Lock()
	defer bm.overflowMu.Unlock()

	if len(bm.overflow) == 0 {
		return nil
	}

	updates := make([]*TaskUpdate, 0, min(limit, len(bm.overflow)))
	for taskID, update := range bm.overflow {
		updates = append(updates, update)
		delete(bm.overflow, taskID)
		if len(updates) >= limit {
			break
		}
	}

	return updates
}

// processUpdateBatches accumulates and flushes task updates
func (bm *BatchManager) processUpdateBatches() {
	defer bm.wg.Done()

	ticker := time.NewTicker(MaxBatchInterval)
	defer ticker.Stop()

	batch := make([]*TaskUpdate, 0, MaxBatchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}

		flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := bm.flushTaskUpdates(flushCtx, batch); err != nil {
			// Classify the error
			retryable := isRetryableError(err)

			if retryable {
				// Infrastructure error - just log and retry, don't count towards poison pill threshold
				batchLog.Warn("Batch flush failed due to infrastructure issue - will retry",
					"error", err,
					"batch_size", len(batch),
					"retryable", true,
				)
				// Keep batch in memory, try again on next flush interval
				return
			}

			// Non-retryable error (likely bad data) - count towards poison pill threshold
			bm.mu.Lock()
			bm.consecutiveFails++
			failCount := bm.consecutiveFails
			bm.mu.Unlock()

			batchLog.Error("Batch flush failed due to data error",
				"error", err,
				"batch_size", len(batch),
				"consecutive_data_failures", failCount,
				"retryable", false,
			)

			// If we've had too many data errors, fall back to individual updates to isolate poison pill
			if failCount >= MaxConsecutiveFailures {
				batchLog.Error("Batch poison pill detected",
					"error", err,
					"consecutive_data_failures", failCount,
				)

				batchLog.Warn("Max consecutive data failures reached - attempting individual updates to isolate poison pill",
					"batch_size", len(batch),
				)

				// Create fresh bounded context for individual updates (flushCtx may be deadline-exceeded)
				fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
				successCount, skippedCount := bm.flushIndividualUpdates(fallbackCtx, batch)
				fallbackCancel()

				batchLog.Info("Individual update fallback completed",
					"total", len(batch),
					"success", successCount,
					"skipped", skippedCount,
				)

				// Clear batch and reset failure counter after individual processing
				batch = batch[:0]
				bm.mu.Lock()
				bm.consecutiveFails = 0
				bm.mu.Unlock()
			}
			// Keep batch for retry on next flush
			return
		}

		// Successful flush - reset batch and failure counter
		batch = batch[:0]
		bm.mu.Lock()
		bm.consecutiveFails = 0
		bm.mu.Unlock()
	}

	for {
		select {
		case update := <-bm.updates:
			batch = append(batch, update)

			// Flush if batch is full
			if len(batch) >= MaxBatchSize {
				flush()
				ticker.Reset(MaxBatchInterval)
			}

		case <-ticker.C:
			batch = append(batch, bm.popOverflowBatch(MaxBatchSize-len(batch))...)
			flush()

		case <-bm.stopCh:
			// Drain remaining updates
			draining := true
			for draining {
				select {
				case update := <-bm.updates:
					batch = append(batch, update)
				default:
					draining = false
				}
			}
			for {
				limit := MaxBatchSize - len(batch)
				if limit <= 0 {
					limit = MaxBatchSize
				}
				overflowBatch := bm.popOverflowBatch(limit)
				if len(overflowBatch) == 0 {
					break
				}
				batch = append(batch, overflowBatch...)
			}

			// Retry final flush with backoff to ensure zero data loss on shutdown
			var lastErr error
			for attempt := 0; attempt < MaxShutdownRetries; attempt++ {
				if len(batch) == 0 {
					break
				}

				shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				lastErr = bm.flushTaskUpdates(shutdownCtx, batch)
				cancel()
				if lastErr == nil {
					batchLog.Info("Final batch flush successful on shutdown",
						"batch_size", len(batch),
						"attempt", attempt+1,
					)
					batch = batch[:0]
					break
				}

				batchLog.Warn("Final batch flush failed - retrying",
					"error", lastErr,
					"batch_size", len(batch),
					"attempt", attempt+1,
					"max_attempts", MaxShutdownRetries,
				)

				if attempt < MaxShutdownRetries-1 {
					time.Sleep(ShutdownRetryDelay)
				}
			}

			// If batch flush still failing after retries, check error type
			if len(batch) > 0 && lastErr != nil {
				retryable := isRetryableError(lastErr)

				if retryable {
					// Infrastructure failure - don't drop data, just log critical error
					batchLog.Error("CRITICAL: Database unavailable on shutdown - task updates could not be persisted",
						"error", lastErr,
						"batch_size", len(batch),
						"retryable", true,
					)
				} else {
					// Data error - try individual updates to isolate poison pill
					batchLog.Warn("Final batch flush failed due to data error - attempting individual updates to isolate poison pill",
						"error", lastErr,
						"batch_size", len(batch),
						"retryable", false,
					)

					// Create fresh bounded context for individual update fallback
					fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
					successCount, skippedCount := bm.flushIndividualUpdates(fallbackCtx, batch)
					fallbackCancel()

					if skippedCount > 0 {
						batchLog.Error("CRITICAL: Some task updates with bad data could not be persisted on shutdown",
							"skipped", skippedCount,
						)
					} else {
						batchLog.Info("All remaining task updates persisted via individual fallback",
							"success", successCount,
						)
					}
				}
			}

			return
		}
	}
}

// flushTaskUpdates performs true batch UPDATE using PostgreSQL unnest
func (bm *BatchManager) flushTaskUpdates(ctx context.Context, updates []*TaskUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	start := time.Now()

	// Group updates by status to use appropriate UPDATE logic
	completedTasks := make([]*Task, 0, len(updates))
	failedTasks := make([]*Task, 0, len(updates))
	skippedTasks := make([]*Task, 0, len(updates))
	pendingTasks := make([]*Task, 0, len(updates))
	waitingTasks := make([]*Task, 0, len(updates))

	for _, update := range updates {
		task := update.Task

		// Set timestamps if not already set
		now := time.Now().UTC()
		if (task.Status == "completed" || task.Status == "failed") && task.CompletedAt.IsZero() {
			task.CompletedAt = now
		}

		switch task.Status {
		case "completed":
			completedTasks = append(completedTasks, task)
		case "failed", "blocked":
			failedTasks = append(failedTasks, task)
		case "skipped":
			skippedTasks = append(skippedTasks, task)
		case "pending":
			pendingTasks = append(pendingTasks, task)
		case "waiting":
			waitingTasks = append(waitingTasks, task)
		default:
			batchLog.Warn("Unexpected task status in batch, skipping",
				"task_id", task.ID,
				"status", task.Status,
			)
		}
	}

	err := bm.queue.ExecuteWithContext(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		// Batch update completed tasks
		if len(completedTasks) > 0 {
			if err := bm.batchUpdateCompleted(txCtx, tx, completedTasks); err != nil {
				return fmt.Errorf("failed to batch update completed tasks: %w", err)
			}
		}

		// Batch update failed tasks
		if len(failedTasks) > 0 {
			if err := bm.batchUpdateFailed(txCtx, tx, failedTasks); err != nil {
				return fmt.Errorf("failed to batch update failed tasks: %w", err)
			}
		}

		// Batch update skipped tasks
		if len(skippedTasks) > 0 {
			if err := bm.batchUpdateSkipped(txCtx, tx, skippedTasks); err != nil {
				return fmt.Errorf("failed to batch update skipped tasks: %w", err)
			}
		}

		// Batch update pending tasks (retries)
		if len(pendingTasks) > 0 {
			if err := bm.batchUpdatePending(txCtx, tx, pendingTasks); err != nil {
				return fmt.Errorf("failed to batch update pending tasks: %w", err)
			}
		}

		if len(waitingTasks) > 0 {
			if err := bm.batchUpdateWaiting(txCtx, tx, waitingTasks); err != nil {
				return fmt.Errorf("failed to batch update waiting tasks: %w", err)
			}
		}

		// After updating task statuses, increment daily usage so subsequent quota checks see accurate values
		if len(completedTasks) > 0 || len(failedTasks) > 0 {
			if err := incrementDailyUsageForTasks(txCtx, tx, completedTasks, failedTasks); err != nil {
				return fmt.Errorf("failed to increment daily usage: %w", err)
			}
		}

		// Running task counters are decremented by the stream worker on task
		// outcome. Waiting→pending promotion is driven per-completion by
		// DbQueue.PromoteWaitingToPending, invoked from the stream worker after
		// the counter decrement succeeds.

		return nil
	})

	duration := time.Since(start)

	if err != nil {
		batchLog.Error("Batch update failed",
			"error", err,
			"total_tasks", len(updates),
			"completed", len(completedTasks),
			"failed", len(failedTasks),
			"skipped", len(skippedTasks),
			"pending", len(pendingTasks),
			"duration", duration,
		)
		return err
	}

	batchLog.Debug("Batch update successful",
		"total_tasks", len(updates),
		"completed", len(completedTasks),
		"failed", len(failedTasks),
		"skipped", len(skippedTasks),
		"pending", len(pendingTasks),
		"duration", duration,
	)

	// Notify post-flush observers (e.g. the lighthouse scheduler watching
	// for progress-milestone crossings) with the distinct job IDs whose
	// terminal task counts changed. Only completed/failed/skipped move the
	// numerator of the progress fraction, so retries (pending/waiting) are
	// excluded — they can't cross a milestone on their own.
	//
	// The callback runs on this goroutine; implementations are expected to
	// dispatch any meaningful work to a goroutine of their own so the next
	// flush isn't blocked.
	if bm.onBatchFlushed != nil {
		jobIDs := uniqueJobIDsForMilestone(completedTasks, failedTasks, skippedTasks)
		if len(jobIDs) > 0 {
			// Wrap the external callback in a panic guard. A panic
			// inside onBatchFlushed (e.g. a milestone scheduler bug)
			// would otherwise unwind the batch loop goroutine and
			// silently halt every subsequent flush, since the outer
			// processUpdateBatches loop has no recover() above it.
			func() {
				defer func() {
					if r := recover(); r != nil {
						batchLog.Error("onBatchFlushed callback panicked",
							"panic", r,
							"job_count", len(jobIDs),
						)
					}
				}()
				bm.onBatchFlushed(ctx, jobIDs)
			}()
		}
	}

	return nil
}

// uniqueJobIDsForMilestone returns the set of distinct job IDs touched
// by the completed/failed/skipped buckets. Pending/waiting buckets are
// intentionally excluded — only terminal transitions advance progress.
func uniqueJobIDsForMilestone(completed, failed, skipped []*Task) []string {
	seen := make(map[string]struct{}, len(completed)+len(failed)+len(skipped))
	for _, t := range completed {
		seen[t.JobID] = struct{}{}
	}
	for _, t := range failed {
		seen[t.JobID] = struct{}{}
	}
	for _, t := range skipped {
		seen[t.JobID] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

// incrementDailyUsageForTasks increments the daily usage counter for completed/failed tasks.
// Returns an error if quota increment fails, allowing callers to gate subsequent operations.
func incrementDailyUsageForTasks(txCtx context.Context, tx *sql.Tx, completedTasks, failedTasks []*Task) error {
	// Build job counts map: jobID → task count
	jobCounts := make(map[string]int)
	for _, task := range completedTasks {
		jobCounts[task.JobID]++
	}
	for _, task := range failedTasks {
		jobCounts[task.JobID]++
	}

	if len(jobCounts) == 0 {
		return nil
	}

	// Collect job IDs for batch query
	jobIDs := make([]string, 0, len(jobCounts))
	for jobID := range jobCounts {
		jobIDs = append(jobIDs, jobID)
	}

	// Single batch query to get organisation IDs for all jobs
	rows, err := tx.QueryContext(txCtx,
		`SELECT id, organisation_id FROM jobs WHERE id = ANY($1) AND organisation_id IS NOT NULL`,
		pq.Array(jobIDs))
	if err != nil {
		return fmt.Errorf("fetch job organisations for quota increment: %w", err)
	}
	defer rows.Close()

	// Map job IDs to org IDs and aggregate counts per org
	orgCounts := make(map[string]int)
	for rows.Next() {
		var jobID string
		var orgID string
		if err := rows.Scan(&jobID, &orgID); err != nil {
			return fmt.Errorf("scan job organisation row for quota increment: %w", err)
		}
		orgCounts[orgID] += jobCounts[jobID]
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate job organisations for quota increment: %w", err)
	}

	// Increment usage once per org with aggregated count
	for orgID, count := range orgCounts {
		_, err := tx.ExecContext(txCtx, `SELECT increment_daily_usage($1, $2)`, orgID, count)
		if err != nil {
			batchLog.Error("Quota increment failed", "error", err, "org_id", orgID, "pages", count)
			return fmt.Errorf("increment daily usage for org %s (%d pages): %w", orgID, count, err)
		}
	}

	return nil
}

// flushIndividualUpdates attempts to update tasks one-by-one to isolate poison pills
// Returns (successCount, skippedCount)
func (bm *BatchManager) flushIndividualUpdates(ctx context.Context, updates []*TaskUpdate) (int, int) {
	successCount := 0
	skippedCount := 0

	for _, update := range updates {
		// Try to update this single task
		err := bm.queue.ExecuteWithContext(ctx, func(txCtx context.Context, tx *sql.Tx) error {
			// Use the original UpdateTaskStatus logic for single task
			task := update.Task

			var updateErr error
			switch task.Status {
			case "completed":
				updateErr = bm.batchUpdateCompleted(txCtx, tx, []*Task{task})
			case "failed", "blocked":
				updateErr = bm.batchUpdateFailed(txCtx, tx, []*Task{task})
			case "skipped":
				updateErr = bm.batchUpdateSkipped(txCtx, tx, []*Task{task})
			case "pending":
				updateErr = bm.batchUpdatePending(txCtx, tx, []*Task{task})
			case "waiting":
				updateErr = bm.batchUpdateWaiting(txCtx, tx, []*Task{task})
			default:
				return fmt.Errorf("unknown status: %s", task.Status)
			}

			return updateErr
		})

		if err != nil {
			// This is the poison pill - auto-captured to Sentry via batchLog.Error
			batchLog.Error("POISON PILL: Task update failed even in individual mode - skipping",
				"error", err,
				"task_id", update.Task.ID,
				"status", update.Task.Status,
			)
			skippedCount++
		} else {
			successCount++
		}
	}

	return successCount, skippedCount
}

// batchUpdateCompleted updates multiple completed tasks in a single statement
func (bm *BatchManager) batchUpdateCompleted(ctx context.Context, tx *sql.Tx, tasks []*Task) error {
	if len(tasks) == 0 {
		return nil
	}

	// Build arrays for all fields
	ids := make([]string, len(tasks))
	startedAts := make([]time.Time, len(tasks))
	completedAts := make([]time.Time, len(tasks))
	statusCodes := make([]int, len(tasks))
	responseTimes := make([]int64, len(tasks))
	cacheStatuses := make([]string, len(tasks))
	contentTypes := make([]string, len(tasks))
	contentLengths := make([]int64, len(tasks))
	headers := make([]string, len(tasks))
	redirectURLs := make([]string, len(tasks))
	dnsLookupTimes := make([]int64, len(tasks))
	tcpConnectionTimes := make([]int64, len(tasks))
	tlsHandshakeTimes := make([]int64, len(tasks))
	ttfbs := make([]int64, len(tasks))
	contentTransferTimes := make([]int64, len(tasks))
	secondResponseTimes := make([]int64, len(tasks))
	secondCacheStatuses := make([]string, len(tasks))
	secondContentLengths := make([]int64, len(tasks))
	secondHeaders := make([]string, len(tasks))
	secondDNSLookupTimes := make([]int64, len(tasks))
	secondTCPConnectionTimes := make([]int64, len(tasks))
	secondTLSHandshakeTimes := make([]int64, len(tasks))
	secondTTFBs := make([]int64, len(tasks))
	secondContentTransferTimes := make([]int64, len(tasks))
	retryCounts := make([]int, len(tasks))
	cacheCheckAttempts := make([]string, len(tasks))
	requestDiagnostics := make([]string, len(tasks))
	htmlStorageBuckets := make([]string, len(tasks))
	htmlStoragePaths := make([]string, len(tasks))
	htmlContentTypes := make([]string, len(tasks))
	htmlContentEncodings := make([]string, len(tasks))
	htmlSizeBytes := make([]int64, len(tasks))
	htmlCompressedSizeBytes := make([]int64, len(tasks))
	htmlSHA256s := make([]string, len(tasks))
	htmlCapturedAts := make([]string, len(tasks))

	for i, task := range tasks {
		ids[i] = task.ID
		// StartedAt is populated by the stream worker when the task is dispatched.
		// Fall back to CompletedAt so the column is never NULL for completed rows.
		if !task.StartedAt.IsZero() {
			startedAts[i] = task.StartedAt
		} else {
			startedAts[i] = task.CompletedAt
		}
		completedAts[i] = task.CompletedAt
		statusCodes[i] = task.StatusCode
		responseTimes[i] = task.ResponseTime
		cacheStatuses[i] = task.CacheStatus
		contentTypes[i] = task.ContentType
		contentLengths[i] = task.ContentLength

		// Ensure JSONB fields are never nil and are valid JSON
		if len(task.Headers) == 0 {
			headers[i] = "{}"
		} else {
			headers[i] = string(task.Headers)
		}

		redirectURLs[i] = task.RedirectURL
		dnsLookupTimes[i] = task.DNSLookupTime
		tcpConnectionTimes[i] = task.TCPConnectionTime
		tlsHandshakeTimes[i] = task.TLSHandshakeTime
		ttfbs[i] = task.TTFB
		contentTransferTimes[i] = task.ContentTransferTime
		secondResponseTimes[i] = task.SecondResponseTime
		secondCacheStatuses[i] = task.SecondCacheStatus
		secondContentLengths[i] = task.SecondContentLength

		if len(task.SecondHeaders) == 0 {
			secondHeaders[i] = "{}"
		} else {
			secondHeaders[i] = string(task.SecondHeaders)
		}

		secondDNSLookupTimes[i] = task.SecondDNSLookupTime
		secondTCPConnectionTimes[i] = task.SecondTCPConnectionTime
		secondTLSHandshakeTimes[i] = task.SecondTLSHandshakeTime
		secondTTFBs[i] = task.SecondTTFB
		secondContentTransferTimes[i] = task.SecondContentTransferTime
		retryCounts[i] = task.RetryCount

		if len(task.CacheCheckAttempts) == 0 {
			cacheCheckAttempts[i] = "[]"
		} else {
			cacheCheckAttempts[i] = string(task.CacheCheckAttempts)
		}

		if len(task.RequestDiagnostics) == 0 {
			requestDiagnostics[i] = "{}"
		} else {
			requestDiagnostics[i] = string(task.RequestDiagnostics)
		}

		htmlStorageBuckets[i] = task.HTMLStorageBucket
		htmlStoragePaths[i] = task.HTMLStoragePath
		htmlContentTypes[i] = task.HTMLContentType
		htmlContentEncodings[i] = task.HTMLContentEncoding
		htmlSizeBytes[i] = task.HTMLSizeBytes
		htmlCompressedSizeBytes[i] = task.HTMLCompressedSizeBytes
		htmlSHA256s[i] = task.HTMLSHA256
		if !task.HTMLCapturedAt.IsZero() {
			htmlCapturedAts[i] = task.HTMLCapturedAt.UTC().Format(time.RFC3339Nano)
		}
	}

	// Single UPDATE statement using unnest to batch update all tasks
	// Note: running_tasks counter is managed by Redis broker counters, not here
	query := `
		UPDATE tasks
		SET status = 'completed',
			started_at = COALESCE(tasks.started_at, updates.started_at),
			completed_at = updates.completed_at,
			status_code = updates.status_code,
			response_time = updates.response_time,
			cache_status = updates.cache_status,
			content_type = updates.content_type,
			content_length = updates.content_length,
			headers = updates.headers::jsonb,
			redirect_url = updates.redirect_url,
			dns_lookup_time = updates.dns_lookup_time,
			tcp_connection_time = updates.tcp_connection_time,
			tls_handshake_time = updates.tls_handshake_time,
			ttfb = updates.ttfb,
			content_transfer_time = updates.content_transfer_time,
			second_response_time = updates.second_response_time,
			second_cache_status = updates.second_cache_status,
			second_content_length = updates.second_content_length,
			second_headers = updates.second_headers::jsonb,
			second_dns_lookup_time = updates.second_dns_lookup_time,
			second_tcp_connection_time = updates.second_tcp_connection_time,
			second_tls_handshake_time = updates.second_tls_handshake_time,
			second_ttfb = updates.second_ttfb,
			second_content_transfer_time = updates.second_content_transfer_time,
			retry_count = updates.retry_count,
			cache_check_attempts = updates.cache_check_attempts::jsonb,
			request_diagnostics = updates.request_diagnostics::jsonb,
			html_storage_bucket = COALESCE(NULLIF(updates.html_storage_bucket, ''), tasks.html_storage_bucket),
			html_storage_path = COALESCE(NULLIF(updates.html_storage_path, ''), tasks.html_storage_path),
			html_content_type = COALESCE(NULLIF(updates.html_content_type, ''), tasks.html_content_type),
			html_content_encoding = COALESCE(NULLIF(updates.html_content_encoding, ''), tasks.html_content_encoding),
			html_size_bytes = CASE WHEN updates.html_size_bytes > 0 THEN updates.html_size_bytes ELSE tasks.html_size_bytes END,
			html_compressed_size_bytes = CASE WHEN updates.html_compressed_size_bytes > 0 THEN updates.html_compressed_size_bytes ELSE tasks.html_compressed_size_bytes END,
			html_sha256 = COALESCE(NULLIF(updates.html_sha256, ''), tasks.html_sha256),
			html_captured_at = COALESCE(updates.html_captured_at, tasks.html_captured_at)
		FROM (
			SELECT
				unnest($1::text[]) AS id,
				unnest($2::timestamptz[]) AS completed_at,
				unnest($3::integer[]) AS status_code,
				unnest($4::bigint[]) AS response_time,
				unnest($5::text[]) AS cache_status,
				unnest($6::text[]) AS content_type,
				unnest($7::bigint[]) AS content_length,
				unnest($8::text[]) AS headers,
				unnest($9::text[]) AS redirect_url,
				unnest($10::bigint[]) AS dns_lookup_time,
				unnest($11::bigint[]) AS tcp_connection_time,
				unnest($12::bigint[]) AS tls_handshake_time,
				unnest($13::bigint[]) AS ttfb,
				unnest($14::bigint[]) AS content_transfer_time,
				unnest($15::bigint[]) AS second_response_time,
				unnest($16::text[]) AS second_cache_status,
				unnest($17::bigint[]) AS second_content_length,
				unnest($18::text[]) AS second_headers,
				unnest($19::bigint[]) AS second_dns_lookup_time,
				unnest($20::bigint[]) AS second_tcp_connection_time,
				unnest($21::bigint[]) AS second_tls_handshake_time,
				unnest($22::bigint[]) AS second_ttfb,
				unnest($23::bigint[]) AS second_content_transfer_time,
				unnest($24::integer[]) AS retry_count,
				unnest($25::text[]) AS cache_check_attempts,
				unnest($26::text[]) AS request_diagnostics,
				unnest($27::text[]) AS html_storage_bucket,
				unnest($28::text[]) AS html_storage_path,
				unnest($29::text[]) AS html_content_type,
				unnest($30::text[]) AS html_content_encoding,
				unnest($31::bigint[]) AS html_size_bytes,
				unnest($32::bigint[]) AS html_compressed_size_bytes,
				unnest($33::text[]) AS html_sha256,
				NULLIF(unnest($34::text[]), '')::timestamptz AS html_captured_at,
				unnest($35::timestamptz[]) AS started_at
		) AS updates
		WHERE tasks.id = updates.id
	`

	_, err := tx.ExecContext(ctx, query,
		pq.Array(ids),
		pq.Array(completedAts),
		pq.Array(statusCodes),
		pq.Array(responseTimes),
		pq.Array(cacheStatuses),
		pq.Array(contentTypes),
		pq.Array(contentLengths),
		pq.Array(headers),
		pq.Array(redirectURLs),
		pq.Array(dnsLookupTimes),
		pq.Array(tcpConnectionTimes),
		pq.Array(tlsHandshakeTimes),
		pq.Array(ttfbs),
		pq.Array(contentTransferTimes),
		pq.Array(secondResponseTimes),
		pq.Array(secondCacheStatuses),
		pq.Array(secondContentLengths),
		pq.Array(secondHeaders),
		pq.Array(secondDNSLookupTimes),
		pq.Array(secondTCPConnectionTimes),
		pq.Array(secondTLSHandshakeTimes),
		pq.Array(secondTTFBs),
		pq.Array(secondContentTransferTimes),
		pq.Array(retryCounts),
		pq.Array(cacheCheckAttempts),
		pq.Array(requestDiagnostics),
		pq.Array(htmlStorageBuckets),
		pq.Array(htmlStoragePaths),
		pq.Array(htmlContentTypes),
		pq.Array(htmlContentEncodings),
		pq.Array(htmlSizeBytes),
		pq.Array(htmlCompressedSizeBytes),
		pq.Array(htmlSHA256s),
		pq.Array(htmlCapturedAts),
		pq.Array(startedAts),
	)

	if err != nil {
		return err
	}

	batchLog.Debug("Batch updated completed tasks", "tasks_count", len(tasks))

	return nil
}

// batchUpdateFailed updates multiple failed tasks in a single statement
func (bm *BatchManager) batchUpdateFailed(ctx context.Context, tx *sql.Tx, tasks []*Task) error {
	if len(tasks) == 0 {
		return nil
	}

	ids := make([]string, len(tasks))
	startedAts := make([]time.Time, len(tasks))
	completedAts := make([]time.Time, len(tasks))
	errors := make([]string, len(tasks))
	retryCounts := make([]int, len(tasks))
	statuses := make([]string, len(tasks))
	requestDiagnostics := make([]string, len(tasks))

	for i, task := range tasks {
		ids[i] = task.ID
		if !task.StartedAt.IsZero() {
			startedAts[i] = task.StartedAt
		} else {
			startedAts[i] = task.CompletedAt
		}
		completedAts[i] = task.CompletedAt
		errors[i] = task.Error
		retryCounts[i] = task.RetryCount
		statuses[i] = task.Status // Could be "failed" or "blocked"
		if len(task.RequestDiagnostics) == 0 {
			requestDiagnostics[i] = "{}"
		} else {
			requestDiagnostics[i] = string(task.RequestDiagnostics)
		}
	}

	query := `
		UPDATE tasks
		SET status = updates.status,
			started_at = COALESCE(tasks.started_at, updates.started_at),
			completed_at = updates.completed_at,
			error = updates.error,
			retry_count = updates.retry_count,
			request_diagnostics = updates.request_diagnostics::jsonb
		FROM (
			SELECT
				unnest($1::text[]) AS id,
				unnest($2::text[]) AS status,
				unnest($3::timestamptz[]) AS completed_at,
				unnest($4::text[]) AS error,
				unnest($5::integer[]) AS retry_count,
				unnest($6::text[]) AS request_diagnostics,
				unnest($7::timestamptz[]) AS started_at
		) AS updates
		WHERE tasks.id = updates.id
	`

	_, err := tx.ExecContext(ctx, query,
		pq.Array(ids),
		pq.Array(statuses),
		pq.Array(completedAts),
		pq.Array(errors),
		pq.Array(retryCounts),
		pq.Array(requestDiagnostics),
		pq.Array(startedAts),
	)

	if err != nil {
		return err
	}

	batchLog.Debug("Batch updated failed tasks", "tasks_count", len(tasks))

	return nil
}

// batchUpdateWaiting updates waiting tasks while preserving retry diagnostics.
func (bm *BatchManager) batchUpdateWaiting(ctx context.Context, tx *sql.Tx, tasks []*Task) error {
	if len(tasks) == 0 {
		return nil
	}

	ids := make([]string, len(tasks))
	statuses := make([]string, len(tasks))
	errors := make([]string, len(tasks))
	retryCounts := make([]int, len(tasks))
	requestDiagnostics := make([]string, len(tasks))

	for i, task := range tasks {
		ids[i] = task.ID
		statuses[i] = task.Status
		errors[i] = task.Error
		retryCounts[i] = task.RetryCount
		if len(task.RequestDiagnostics) == 0 {
			requestDiagnostics[i] = "{}"
		} else {
			requestDiagnostics[i] = string(task.RequestDiagnostics)
		}
	}

	query := `
		UPDATE tasks
		SET status = updates.status,
			started_at = NULL,
			error = updates.error,
			retry_count = updates.retry_count,
			request_diagnostics = updates.request_diagnostics::jsonb
		FROM (
			SELECT
				unnest($1::text[]) AS id,
				unnest($2::text[]) AS status,
				unnest($3::text[]) AS error,
				unnest($4::integer[]) AS retry_count,
				unnest($5::text[]) AS request_diagnostics
		) AS updates
		WHERE tasks.id = updates.id
	`

	_, err := tx.ExecContext(ctx, query,
		pq.Array(ids),
		pq.Array(statuses),
		pq.Array(errors),
		pq.Array(retryCounts),
		pq.Array(requestDiagnostics),
	)
	if err != nil {
		return err
	}

	batchLog.Debug("Batch updated waiting tasks", "tasks_count", len(tasks))

	return nil
}

// batchUpdateSkipped updates multiple skipped tasks in a single statement
func (bm *BatchManager) batchUpdateSkipped(ctx context.Context, tx *sql.Tx, tasks []*Task) error {
	if len(tasks) == 0 {
		return nil
	}

	ids := make([]string, len(tasks))
	for i, task := range tasks {
		ids[i] = task.ID
	}

	query := `
		UPDATE tasks
		SET status = 'skipped'
		WHERE id = ANY($1::text[])
	`

	_, err := tx.ExecContext(ctx, query, pq.Array(ids))
	if err != nil {
		return err
	}

	batchLog.Debug("Batch updated skipped tasks", "tasks_count", len(tasks))

	return nil
}

// batchUpdatePending updates tasks that are being retried (set back to pending status)
func (bm *BatchManager) batchUpdatePending(ctx context.Context, tx *sql.Tx, tasks []*Task) error {
	if len(tasks) == 0 {
		return nil
	}

	// Build arrays for batch update
	ids := make([]string, len(tasks))
	retryCounts := make([]int, len(tasks))
	startedAts := make([]time.Time, len(tasks))

	for i, task := range tasks {
		ids[i] = task.ID
		retryCounts[i] = task.RetryCount
		startedAts[i] = task.StartedAt
	}

	query := `
		UPDATE tasks
		SET status = 'pending',
		    retry_count = updates.retry_count,
		    started_at = updates.started_at
		FROM (
			SELECT
				unnest($1::text[]) AS id,
				unnest($2::int[]) AS retry_count,
				unnest($3::timestamptz[]) AS started_at
		) AS updates
		WHERE tasks.id = updates.id
	`

	_, err := tx.ExecContext(ctx, query,
		pq.Array(ids),
		pq.Array(retryCounts),
		pq.Array(startedAts),
	)

	if err != nil {
		return err
	}

	batchLog.Debug("Batch updated pending tasks (retries)", "tasks_count", len(tasks))

	return nil
}

// Stop gracefully shuts down the batch manager, flushing remaining updates
func (bm *BatchManager) Stop() {
	close(bm.stopCh)
	bm.wg.Wait()
	batchLog.Info("Batch manager stopped")
}
