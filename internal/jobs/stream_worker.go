package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/observability"
	"golang.org/x/sync/singleflight"
)

const (
	defaultActiveJobsLimit              = 200
	defaultPendingAdmissionLimitMin     = 250
	defaultPendingAdmissionWorkerFactor = 3
)

// activeJobsLimit resolves: STREAM_ACTIVE_JOBS_LIMIT override →
// max(GNH_MAX_WORKERS × GNH_PENDING_ADMISSION_WORKER_FACTOR,
// GNH_PENDING_ADMISSION_LIMIT_MIN) → defaultActiveJobsLimit.
// Prod (130 × 3, min 250) yields 390.
func activeJobsLimit() int {
	if v := strings.TrimSpace(os.Getenv("STREAM_ACTIVE_JOBS_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		jobsLog.Warn("invalid STREAM_ACTIVE_JOBS_LIMIT, ignoring",
			"value", v)
	}

	minLimit := envIntWithDefault("GNH_PENDING_ADMISSION_LIMIT_MIN", defaultPendingAdmissionLimitMin)
	factor := envIntWithDefault("GNH_PENDING_ADMISSION_WORKER_FACTOR", defaultPendingAdmissionWorkerFactor)
	maxWorkers := jobDefaultConcurrency()

	derived := maxWorkers * factor
	if derived < minLimit {
		derived = minLimit
	}
	if derived <= 0 {
		return defaultActiveJobsLimit
	}
	return derived
}

type StreamWorkerDeps struct {
	Consumer     *broker.Consumer
	Scheduler    *broker.Scheduler
	Counters     *broker.RunningCounters
	Pacer        *broker.DomainPacer
	Executor     *TaskExecutor
	BatchManager *db.BatchManager
	DBQueue      DbQueueInterface
	JobManager   JobManagerInterface
	// HTMLPersister is nil when ARCHIVE_PROVIDER is unset (local dev
	// without R2); completed tasks then persist without HTML.
	HTMLPersister *HTMLPersister
	// WAFBreaker is nil-safe: when supplied, every task outcome runs
	// through it and a domain that produces N consecutive WAF
	// fingerprints trips a job-level block (issue #365 row 1).
	WAFBreaker *WAFCircuitBreaker
}

type StreamWorkerOpts struct {
	NumWorkers int
	// TasksPerWorker caps in-flight tasks per consumer goroutine.
	// Global ceiling = NumWorkers × TasksPerWorker.
	TasksPerWorker  int
	ReclaimInterval time.Duration
}

func DefaultStreamWorkerOpts() StreamWorkerOpts {
	return StreamWorkerOpts{
		NumWorkers:      30,
		TasksPerWorker:  20,
		ReclaimInterval: 30 * time.Second,
	}
}

const jobInfoTTL = 5 * time.Minute

type cachedJobInfo struct {
	info      *JobInfo
	expiresAt time.Time
}

// StreamWorkerPool consumes from Redis Streams, runs tasks via
// TaskExecutor, and acts on the TaskOutcome.
type StreamWorkerPool struct {
	consumer      *broker.Consumer
	scheduler     *broker.Scheduler
	counters      *broker.RunningCounters
	pacer         *broker.DomainPacer
	executor      *TaskExecutor
	batchManager  *db.BatchManager
	dbQueue       DbQueueInterface
	jobManager    JobManagerInterface
	htmlPersister *HTMLPersister
	wafBreaker    *WAFCircuitBreaker
	opts          StreamWorkerOpts

	jobInfoCache map[string]*cachedJobInfo
	jobInfoMutex sync.RWMutex
	jobInfoGroup singleflight.Group

	activeJobs   []string
	activeJobsMu sync.RWMutex

	// linkDiscoverySem caps ProcessDiscoveredLinks fan-out. Without it
	// NumWorkers × TasksPerWorker goroutines piled onto the bulk DB lane
	// (HOVER-KG, 2k+ goroutines).
	linkDiscoverySem chan struct{}

	heartbeat interface{ Tick() }

	// reconcileMu serialises reconcileCounters across the periodic
	// loop and on-demand TriggerReconcile via TryLock so a flood of
	// triggers collapses onto at most one in-flight reconcile.
	reconcileMu sync.Mutex

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

func (swp *StreamWorkerPool) SetHeartbeat(h interface{ Tick() }) {
	swp.heartbeat = h
}

// defaultLinkDiscoveryMaxInflight: 32 throttled prod to ~3–3.5k
// tasks/min; 128 stays clear of the 2k+ goroutine event that motivated
// the cap while restoring headroom above 4k tasks/min.
const defaultLinkDiscoveryMaxInflight = 128

func linkDiscoveryMaxInflight() int {
	if v := strings.TrimSpace(os.Getenv("JOBS_LINK_DISCOVERY_MAX_INFLIGHT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		jobsLog.Warn("invalid JOBS_LINK_DISCOVERY_MAX_INFLIGHT, using default",
			"value", v, "default", defaultLinkDiscoveryMaxInflight)
	}
	return defaultLinkDiscoveryMaxInflight
}

func NewStreamWorkerPool(deps StreamWorkerDeps, opts StreamWorkerOpts) *StreamWorkerPool {
	return &StreamWorkerPool{
		consumer:         deps.Consumer,
		scheduler:        deps.Scheduler,
		counters:         deps.Counters,
		pacer:            deps.Pacer,
		executor:         deps.Executor,
		batchManager:     deps.BatchManager,
		dbQueue:          deps.DBQueue,
		jobManager:       deps.JobManager,
		htmlPersister:    deps.HTMLPersister,
		wafBreaker:       deps.WAFBreaker,
		opts:             opts,
		jobInfoCache:     make(map[string]*cachedJobInfo),
		linkDiscoverySem: make(chan struct{}, linkDiscoveryMaxInflight()),
	}
}

// Start launches consumer goroutines and the reclaim loop.
// Blocks until Stop is called or ctx is cancelled.
func (swp *StreamWorkerPool) Start(ctx context.Context) {
	ctx, swp.cancel = context.WithCancel(ctx)

	// Refresh active jobs first so reconcile knows which streams to probe.
	swp.refreshActiveJobs(ctx)

	swp.reconcileCounters(ctx)

	for i := range swp.opts.NumWorkers {
		swp.wg.Add(1)
		go func(id int) {
			defer swp.wg.Done()
			swp.workerLoop(ctx, id)
		}(i)
	}

	swp.wg.Add(1)
	go func() {
		defer swp.wg.Done()
		swp.reclaimLoop(ctx)
	}()

	swp.wg.Add(1)
	go func() {
		defer swp.wg.Done()
		swp.activeJobsRefreshLoop(ctx)
	}()

	// Periodic reconcile against the authoritative PEL — without this,
	// mid-run drift (missed decrement after worker crash, stale counter
	// from dead stream) persisted until the next deploy.
	swp.wg.Add(1)
	go func() {
		defer swp.wg.Done()
		swp.reconcileLoop(ctx)
	}()

	jobsLog.Info("stream worker pool started", "workers", swp.opts.NumWorkers)
}

func (swp *StreamWorkerPool) Stop() {
	if swp.cancel != nil {
		swp.cancel()
	}
	swp.wg.Wait()
	jobsLog.Info("stream worker pool stopped")
}

// --- consumer loop ---

func (swp *StreamWorkerPool) workerLoop(ctx context.Context, workerID int) {
	logger := jobsLog.With("worker_id", workerID)
	logger.Debug("worker started")

	perWorker := swp.opts.TasksPerWorker
	if perWorker < 1 {
		perWorker = 1
	}
	sem := make(chan struct{}, perWorker)
	var inflight sync.WaitGroup

	dispatch := func(msg broker.StreamMessage) {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		inflight.Add(1)
		go func(m broker.StreamMessage) {
			defer inflight.Done()
			defer func() { <-sem }()
			swp.processMessage(ctx, m)
		}(msg)
	}

	defer inflight.Wait()

	for {
		if ctx.Err() != nil {
			return
		}

		jobIDs := swp.getActiveJobs()
		if len(jobIDs) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}

		processed := false
		for _, jobID := range jobIDs {
			if ctx.Err() != nil {
				return
			}

			msgs, err := swp.consumer.ReadNonBlocking(ctx, jobID)
			if err != nil {
				logger.Error("stream read error", "error", err, "job_id", jobID)
				continue
			}

			for _, msg := range msgs {
				dispatch(msg)
				processed = true
			}
		}

		if !processed {
			// Shard the BLOCK target by workerID — previously every
			// worker queued on jobIDs[0], starving the other jobs.
			if n := len(jobIDs); n > 0 {
				target := jobIDs[workerID%n]
				msgs, err := swp.consumer.Read(ctx, target)
				if err != nil {
					logger.Warn("blocking read error", "error", err, "job_id", target)
				}
				for _, msg := range msgs {
					dispatch(msg)
				}
			}
		}
	}
}

func (swp *StreamWorkerPool) processMessage(ctx context.Context, msg broker.StreamMessage) {
	logger := jobsLog.With("task_id", msg.TaskID, "job_id", msg.JobID)

	task, err := swp.buildTask(ctx, msg)
	if err != nil {
		logger.Error("failed to build task from stream message", "error", err)
		// Do NOT ACK — buildTask failures are transient; leaving the
		// message in the PEL lets XAUTOCLAIM redeliver it.
		return
	}

	processStart := time.Now()
	taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	outcome := swp.executor.Execute(taskCtx, task)
	cancel()
	processDuration := time.Since(processStart)

	outcomeLabel, reason := classifyTaskOutcome(outcome)
	observability.RecordWorkerTaskOutcome(ctx, observability.WorkerTaskOutcomeMetrics{
		JobID:    msg.JobID,
		Outcome:  outcomeLabel,
		Reason:   reason,
		Duration: processDuration,
	})

	swp.handleOutcome(ctx, msg, outcome)

	// Tick AFTER handleOutcome so a wedge inside it leaves the
	// heartbeat flat and the watchdog trips.
	if swp.heartbeat != nil {
		swp.heartbeat.Tick()
	}
}

func (swp *StreamWorkerPool) handleOutcome(ctx context.Context, msg broker.StreamMessage, outcome *TaskOutcome) {
	domain := msg.Host

	// Retry must use ScheduleAndAck (MULTI/EXEC) — a two-step Schedule
	// then Ack would let XAUTOCLAIM redeliver and cause a duplicate
	// crawl if Schedule succeeded but Ack failed.
	if outcome.Retry != nil && outcome.Retry.ShouldRetry {
		entry := broker.ScheduleEntry{
			TaskID:     outcome.Task.ID,
			JobID:      outcome.Task.JobID,
			PageID:     outcome.Task.PageID,
			Host:       msg.Host,
			Path:       msg.Path,
			Priority:   msg.Priority,
			RetryCount: outcome.Task.RetryCount,
			SourceType: outcome.Task.SourceType,
			SourceURL:  outcome.Task.SourceURL,
			RunAt:      outcome.Retry.NextRunAt,
		}
		if err := swp.scheduler.ScheduleAndAck(ctx, entry, msg.JobID, msg.MessageID); err != nil {
			jobsLog.Error("retry schedule-and-ack failed, leaving in PEL for redelivery", "error", err, "task_id", outcome.Task.ID)
			return
		}
	} else {
		if err := swp.consumer.Ack(ctx, msg.JobID, msg.MessageID); err != nil {
			jobsLog.Error("failed to ACK, skipping bookkeeping", "error", err, "message_id", msg.MessageID)
			return
		}
	}

	decrementedOK := true
	if _, err := swp.counters.Decrement(ctx, msg.JobID); err != nil {
		decrementedOK = false
		jobsLog.Warn("counter decrement failed", "error", err, "job_id", msg.JobID)
	}

	// Promote on success only — double-promotion on a failed decrement
	// would exceed the concurrency cap.
	if decrementedOK && swp.dbQueue != nil {
		if _, err := swp.dbQueue.PromoteWaitingToPending(ctx, msg.JobID, 1); err != nil {
			jobsLog.Warn("waiting->pending promotion failed",
				"error", err, "job_id", msg.JobID)
		}
	}

	if err := swp.pacer.Release(ctx, domain, msg.JobID, outcome.Success, outcome.RateLimited); err != nil {
		jobsLog.Warn("pacer release failed", "error", err, "domain", domain)
	}

	// Hand the payload to the persister BEFORE QueueTaskUpdate — the
	// batch manager mutates the row in place. Enqueue is non-blocking
	// so HTML capture never back-pressures the stream worker.
	if outcome.HTMLUpload != nil && swp.htmlPersister != nil {
		if !swp.htmlPersister.Enqueue(ctx, outcome.Task, outcome.HTMLUpload) {
			jobsLog.Warn("html persister queue saturated — payload dropped",
				"task_id", outcome.Task.ID,
				"job_id", outcome.Task.JobID,
				"queue_depth", swp.htmlPersister.QueueDepth(),
			)
		}
	}
	swp.batchManager.QueueTaskUpdate(outcome.Task)

	// WAF circuit breaker: feed every outcome through the per-job
	// counter and trip a job-level block once N consecutive responses
	// carry recognised fingerprints. Runs after the task row update is
	// queued so the per-task evidence is persisted before we mark the
	// job terminal.
	if swp.wafBreaker != nil && swp.jobManager != nil {
		swp.wafBreaker.MaybeTripFromOutcome(ctx, swp.jobManager, outcome)
	}

	if len(outcome.DiscoveredLinks) > 0 {
		swp.handleDiscoveredLinks(ctx, outcome)
	}
}

func (swp *StreamWorkerPool) handleDiscoveredLinks(ctx context.Context, outcome *TaskOutcome) {
	if swp.jobManager == nil || len(outcome.DiscoveredLinks) == 0 {
		return
	}

	// Block on the semaphore so the caller backpressures naturally —
	// without this every worker drained the bulk pool simultaneously
	// (HOVER-KG).
	if swp.linkDiscoverySem != nil {
		select {
		case swp.linkDiscoverySem <- struct{}{}:
			defer func() { <-swp.linkDiscoverySem }()
		case <-ctx.Done():
			return
		}
	}

	task := &Task{
		ID:                       outcome.Task.ID,
		JobID:                    outcome.Task.JobID,
		Host:                     outcome.Task.Host,
		Path:                     outcome.Task.Path,
		PriorityScore:            outcome.Task.PriorityScore,
		FindLinks:                true,
		AllowCrossSubdomainLinks: false,
	}

	info, err := swp.loadJobInfo(ctx, outcome.Task.JobID)
	if err != nil {
		jobsLog.Error("failed to load job info for link discovery", "error", err, "job_id", outcome.Task.JobID)
		return
	}
	task.DomainID = info.DomainID
	task.DomainName = info.DomainName
	task.AllowCrossSubdomainLinks = info.AllowCrossSubdomainLinks

	sourceURL := ConstructTaskURL(outcome.Task.Path, outcome.Task.Host, info.DomainName)
	if outcome.CrawlResult != nil && outcome.CrawlResult.URL != "" {
		sourceURL = outcome.CrawlResult.URL
	}

	deps := LinkDiscoveryDeps{
		DBQueue:     swp.dbQueue,
		JobManager:  swp.jobManager,
		MinPriority: linkDiscoveryMinPriorityFromEnv(),
	}

	ProcessDiscoveredLinks(ctx, deps, task, outcome.DiscoveredLinks, sourceURL, info.RobotsRules)
}

// 120s balances drift-recovery latency against XPENDING cost across
// hundreds of active jobs. Override via REDIS_COUNTER_RECONCILE_INTERVAL_S.
const defaultReconcileIntervalS = 120

func reconcileInterval() time.Duration {
	return time.Duration(envIntWithDefault("REDIS_COUNTER_RECONCILE_INTERVAL_S", defaultReconcileIntervalS)) * time.Second
}

func (swp *StreamWorkerPool) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swp.runReconcileSerialised(ctx)
		}
	}
}

// TriggerReconcile runs an immediate reconcile, collapsing concurrent
// calls onto an in-flight pass via TryLock. Contract: at least one
// reconcile after the most recent trigger.
func (swp *StreamWorkerPool) TriggerReconcile(ctx context.Context) {
	swp.runReconcileSerialised(ctx)
}

func (swp *StreamWorkerPool) runReconcileSerialised(ctx context.Context) bool {
	if !swp.reconcileMu.TryLock() {
		return false
	}
	defer swp.reconcileMu.Unlock()
	swp.reconcileCounters(ctx)
	return true
}

// --- reclaim loop ---

func (swp *StreamWorkerPool) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(swp.opts.ReclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swp.reclaimStaleMessages(ctx)
		}
	}
}

func (swp *StreamWorkerPool) reclaimStaleMessages(ctx context.Context) {
	jobIDs := swp.getActiveJobs()
	for _, jobID := range jobIDs {
		reclaimed, deadLetter, err := swp.consumer.ReclaimStale(ctx, jobID)
		if err != nil {
			jobsLog.Warn("reclaim failed", "error", err, "job_id", jobID)
			continue
		}

		for _, msg := range reclaimed {
			jobsLog.Info("reclaimed stale task", "task_id", msg.TaskID, "message_id", msg.MessageID)
			swp.processMessage(ctx, msg)
		}

		for _, msg := range deadLetter {
			jobsLog.Warn("dead-lettering task after max deliveries", "task_id", msg.TaskID, "retry_count", msg.RetryCount)
			if err := swp.consumer.Ack(ctx, jobID, msg.MessageID); err != nil {
				jobsLog.Error("dead-letter ACK failed, skipping", "error", err, "task_id", msg.TaskID)
				continue
			}
			if _, err := swp.counters.Decrement(ctx, jobID); err != nil {
				jobsLog.Warn("counter decrement on dead-letter failed", "error", err)
			}
			if err := swp.pacer.Release(ctx, msg.Host, jobID, false, false); err != nil {
				jobsLog.Warn("pacer release on dead-letter failed", "error", err, "domain", msg.Host)
			}

			dbTask := &db.Task{
				ID:          msg.TaskID,
				JobID:       msg.JobID,
				Status:      string(TaskStatusFailed),
				CompletedAt: time.Now().UTC(),
				Error:       "exceeded maximum delivery attempts",
				RetryCount:  msg.RetryCount,
			}
			swp.batchManager.QueueTaskUpdate(dbTask)
			observability.RecordWorkerTaskFailure(ctx, jobID, "dead_letter")
		}
	}
}

// --- active jobs ---

func (swp *StreamWorkerPool) activeJobsRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swp.refreshActiveJobs(ctx)
		}
	}
}

func (swp *StreamWorkerPool) refreshActiveJobs(ctx context.Context) {
	var jobIDs []string
	limit := activeJobsLimit()
	err := swp.dbQueue.ExecuteControl(ctx, func(tx *sql.Tx) error {
		// FIFO by updated_at ASC — created_at DESC starved older jobs
		// under deep backlog. Mirrors pre-merge worker.go:2273-2276.
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM jobs
			 WHERE status IN ('running', 'pending')
			 ORDER BY updated_at ASC NULLS FIRST,
			          started_at ASC NULLS FIRST,
			          created_at ASC
			 LIMIT $1`,
			limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			jobIDs = append(jobIDs, id)
		}
		return rows.Err()
	})
	if err != nil {
		// Warn (not Error): refresh runs every 10s; a control-DB blip
		// would otherwise flood Sentry once per tick.
		jobsLog.Warn("failed to refresh active jobs", "error", err)
		return
	}

	// Never evict a job whose PEL still holds in-flight work — the
	// limit window can shuffle a mid-flight job off the active set,
	// freezing both dispatch and consume for it. Union with any job
	// that has a non-zero running_counters entry.
	counters, counterErr := swp.counters.GetAll(ctx)
	if counterErr != nil {
		jobsLog.Warn("failed to read running counters for active-job merge", "error", counterErr)
	} else if len(counters) > 0 {
		seen := make(map[string]struct{}, len(jobIDs))
		for _, id := range jobIDs {
			seen[id] = struct{}{}
		}
		extra := 0
		for id, n := range counters {
			if n <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			jobIDs = append(jobIDs, id)
			seen[id] = struct{}{}
			extra++
		}
		if extra > 0 {
			jobsLog.Info("retained active jobs with in-flight work",
				"retained", extra, "total_active", len(jobIDs))
		}
	}

	swp.activeJobsMu.Lock()
	swp.activeJobs = jobIDs
	swp.activeJobsMu.Unlock()
}

func (swp *StreamWorkerPool) getActiveJobs() []string {
	swp.activeJobsMu.RLock()
	defer swp.activeJobsMu.RUnlock()
	return swp.activeJobs
}

func (swp *StreamWorkerPool) ActiveJobIDs(ctx context.Context) ([]string, error) {
	return swp.getActiveJobs(), nil
}

// --- job info ---

func (swp *StreamWorkerPool) buildTask(ctx context.Context, msg broker.StreamMessage) (*Task, error) {
	info, err := swp.loadJobInfo(ctx, msg.JobID)
	if err != nil {
		return nil, fmt.Errorf("load job info for %s: %w", msg.JobID, err)
	}

	return &Task{
		ID:                       msg.TaskID,
		JobID:                    msg.JobID,
		PageID:                   msg.PageID,
		Host:                     msg.Host,
		Path:                     msg.Path,
		Status:                   TaskStatusRunning,
		StartedAt:                time.Now().UTC(),
		RetryCount:               msg.RetryCount,
		SourceType:               msg.SourceType,
		SourceURL:                msg.SourceURL,
		PriorityScore:            msg.Priority,
		DomainID:                 info.DomainID,
		DomainName:               info.DomainName,
		FindLinks:                info.FindLinks,
		AllowCrossSubdomainLinks: info.AllowCrossSubdomainLinks,
		CrawlDelay:               info.CrawlDelay,
		JobConcurrency:           info.Concurrency,
		AdaptiveDelay:            info.AdaptiveDelay,
		AdaptiveDelayFloor:       info.AdaptiveDelayFloor,
	}, nil
}

func (swp *StreamWorkerPool) loadJobInfo(ctx context.Context, jobID string) (*JobInfo, error) {
	now := time.Now()

	swp.jobInfoMutex.RLock()
	if cached, ok := swp.jobInfoCache[jobID]; ok && now.Before(cached.expiresAt) {
		swp.jobInfoMutex.RUnlock()
		return cached.info, nil
	}
	swp.jobInfoMutex.RUnlock()

	val, err, _ := swp.jobInfoGroup.Do(jobID, func() (any, error) {
		info, fetchErr := swp.fetchJobInfo(ctx, jobID)
		if fetchErr != nil {
			return nil, fetchErr
		}

		swp.jobInfoMutex.Lock()
		swp.jobInfoCache[jobID] = &cachedJobInfo{
			info:      info,
			expiresAt: time.Now().Add(jobInfoTTL),
		}
		swp.jobInfoMutex.Unlock()

		return info, nil
	})
	if err != nil {
		return nil, err
	}

	return val.(*JobInfo), nil
}

func (swp *StreamWorkerPool) fetchJobInfo(ctx context.Context, jobID string) (*JobInfo, error) {
	var info JobInfo
	var crawlDelay, adaptiveDelay, adaptiveFloor sql.NullInt64

	err := swp.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT d.id, d.name, d.crawl_delay_seconds, d.adaptive_delay_seconds,
			       d.adaptive_delay_floor_seconds, j.find_links,
			       j.allow_cross_subdomain_links, j.concurrency
			FROM domains d
			JOIN jobs j ON j.domain_id = d.id
			WHERE j.id = $1
		`, jobID).Scan(
			&info.DomainID, &info.DomainName, &crawlDelay, &adaptiveDelay,
			&adaptiveFloor, &info.FindLinks, &info.AllowCrossSubdomainLinks,
			&info.Concurrency,
		)
	})
	if err != nil {
		return nil, err
	}

	if crawlDelay.Valid {
		info.CrawlDelay = int(crawlDelay.Int64)
	}
	if adaptiveDelay.Valid {
		info.AdaptiveDelay = int(adaptiveDelay.Int64)
	}
	if adaptiveFloor.Valid {
		info.AdaptiveDelayFloor = int(adaptiveFloor.Int64)
	}

	// Without robots rules, link discovery would let every URL through
	// regardless of disallow.
	if swp.jobManager != nil {
		rules, robotsErr := swp.jobManager.GetRobotsRules(ctx, info.DomainName)
		if robotsErr != nil {
			// Better to enqueue a few extra URLs than stall when
			// robots.txt is temporarily unavailable.
			jobsLog.Warn("failed to load robots rules for job info, continuing without",
				"error", robotsErr, "domain", info.DomainName)
		} else {
			info.RobotsRules = rules
		}
	}

	return &info, nil
}

// --- counter reconciliation ---

// reconcileCounters rebuilds RunningCounters from XPENDING. The PEL
// brackets the worker's crawl+persist lifecycle so it's the source of
// truth — a Postgres-status-based reconcile silently zeroed counters
// because the batch writer never transitions through 'running'.
func (swp *StreamWorkerPool) reconcileCounters(ctx context.Context) {
	activeSet := swp.getActiveJobs()
	activeLookup := make(map[string]struct{}, len(activeSet))
	for _, id := range activeSet {
		activeLookup[id] = struct{}{}
	}
	jobIDs := append([]string(nil), activeSet...)

	// Include jobs in the counters hash that refreshActiveJobs hasn't
	// surfaced yet (e.g. just-transitioned status).
	existing, err := swp.counters.GetAll(ctx)
	if err != nil {
		jobsLog.Warn("failed to read existing running counters; continuing with active-job set only", "error", err)
	} else {
		for id := range existing {
			if _, ok := activeLookup[id]; !ok {
				jobIDs = append(jobIDs, id)
			}
		}
	}

	counts := make(map[string]int64, len(jobIDs))
	orphanPELCount := int64(0)
	for _, jobID := range jobIDs {
		n, err := swp.consumer.PendingCount(ctx, jobID)
		if err != nil {
			jobsLog.Warn("PendingCount failed during reconcile; skipping job", "job_id", jobID, "error", err)
			// Preserve existing counter on transient hiccup — over-count
			// briefly beats flooding the job with extra dispatches.
			if prev, ok := existing[jobID]; ok {
				counts[jobID] = prev
			}
			continue
		}
		if n > 0 {
			counts[jobID] = n
			// Non-zero PEL with no active consumer is a stalled-dispatch
			// signal — surface for alerting.
			if _, isActive := activeLookup[jobID]; !isActive {
				orphanPELCount++
				jobsLog.Warn("job has pending work but is not in active-job set",
					"job_id", jobID, "pending", n)
			}
		}
		prev := existing[jobID]
		diff := prev - n
		if diff < 0 {
			diff = -diff
		}
		observability.RecordBrokerCounterPELSkew(ctx, jobID, float64(diff))
	}

	if err := swp.counters.Reconcile(ctx, counts); err != nil {
		jobsLog.Error("failed to reconcile running counters in Redis", "error", err)
		return
	}

	observability.RecordBrokerPELWithoutConsumer(ctx, orphanPELCount)

	total := int64(0)
	for _, c := range counts {
		total += c
	}
	jobsLog.Info("reconciled running counters from PEL",
		"total_running", total,
		"jobs_probed", len(jobIDs),
		"jobs_with_pel", len(counts),
		"orphan_pel_jobs", orphanPELCount)
}

// CanDispatch implements broker.ConcurrencyChecker.
func (swp *StreamWorkerPool) CanDispatch(ctx context.Context, jobID string) (bool, error) {
	info, err := swp.loadJobInfo(ctx, jobID)
	if err != nil {
		return false, err
	}

	concurrency := info.Concurrency
	if concurrency <= 0 {
		concurrency = fallbackJobConcurrency
	}

	running, err := swp.counters.Get(ctx, jobID)
	if err != nil {
		return false, err
	}

	return running < int64(concurrency), nil
}
