package broker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/good-native/hover/internal/observability"
	"github.com/redis/go-redis/v9"
)

// DispatcherOpts controls the dispatcher's scan behaviour.
type DispatcherOpts struct {
	ScanInterval time.Duration
	BatchSize    int64

	// ParallelJobs caps per-tick dispatch goroutines. Serial dispatch
	// scaled O(N_jobs × batch) and produced a ~70× backlog under 100
	// jobs. Default 32; override via REDIS_DISPATCH_PARALLEL_JOBS.
	ParallelJobs int

	// StuckThreshold gates the self-heal reconcile. Default 30s, env
	// REDIS_DISPATCH_STUCK_THRESHOLD_S; rate-limited to one trigger
	// per 2× threshold per job.
	StuckThreshold time.Duration
}

func DefaultDispatcherOpts() DispatcherOpts {
	interval := time.Duration(envInt("REDIS_DISPATCH_INTERVAL_MS", 100)) * time.Millisecond
	batch := int64(envInt("REDIS_DISPATCH_BATCH_SIZE", 50))
	parallel := envInt("REDIS_DISPATCH_PARALLEL_JOBS", 32)
	stuck := time.Duration(envInt("REDIS_DISPATCH_STUCK_THRESHOLD_S", 30)) * time.Second
	return DispatcherOpts{
		ScanInterval:   interval,
		BatchSize:      batch,
		ParallelJobs:   parallel,
		StuckThreshold: stuck,
	}
}

type JobLister interface {
	ActiveJobIDs(ctx context.Context) ([]string, error)
}

type ConcurrencyChecker interface {
	CanDispatch(ctx context.Context, jobID string) (bool, error)
}

// Reconciler is the dispatcher's self-heal target when CanDispatch
// keeps refusing dispatch despite due ZSET work — the signature of
// `hover:running` counter drift. Implementations must be safe for
// concurrent invocation and should debounce a flood of triggers to
// at most one in-flight reconcile.
type Reconciler interface {
	TriggerReconcile(ctx context.Context)
}

// Dispatcher moves due items from per-job Redis ZSETs into per-job
// Redis Streams.
type Dispatcher struct {
	scheduler *Scheduler
	pacer     *DomainPacer
	counters  *RunningCounters
	client    *Client
	jobLister JobLister
	concCheck ConcurrencyChecker
	opts      DispatcherOpts

	// groupEnsured memoises XGroupCreateMkStream calls. The call is
	// idempotent but still a full Redis RTT; per-task invocation was
	// the second-largest cost in the serial dispatcher. Never cleared:
	// keyspace is bounded by jobs-ever-dispatched-for in this process.
	groupEnsured sync.Map

	// firstDispatched is populated on hook success only — a transient
	// hook failure leaves the entry absent so the next tick retries.
	// Memoising before the call would strand the job in pending for
	// the dispatcher's lifetime if that one call failed.
	firstDispatched sync.Map

	onFirstDispatch func(ctx context.Context, jobID string) error
	reconciler      Reconciler

	stuckMu    sync.Mutex
	stuckSince map[string]time.Time
	// lastTrigger persists across clearStuck so a flapping job can't
	// drive reconcile faster than the rate-limit window.
	lastTrigger map[string]time.Time
}

func NewDispatcher(
	client *Client,
	scheduler *Scheduler,
	pacer *DomainPacer,
	counters *RunningCounters,
	jobLister JobLister,
	concCheck ConcurrencyChecker,
	opts DispatcherOpts,
) *Dispatcher {
	return &Dispatcher{
		scheduler: scheduler,
		pacer:     pacer,
		counters:  counters,
		client:    client,
		jobLister: jobLister,
		concCheck: concCheck,
		opts:      opts,
	}
}

// SetOnFirstDispatch installs an idempotent hook fired the first time
// dispatchJob publishes a task for a jobID. A non-nil return triggers
// retry on the next dispatch.
func (d *Dispatcher) SetOnFirstDispatch(fn func(ctx context.Context, jobID string) error) {
	d.onFirstDispatch = fn
}

// SetReconciler installs the self-heal target. Nil disables self-heal
// and is tolerated throughout the hot path.
func (d *Dispatcher) SetReconciler(r Reconciler) {
	d.reconciler = r
}

// Run blocks until ctx is cancelled. Start as a goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.opts.ScanInterval)
	defer ticker.Stop()

	brokerLog.Info("dispatcher started",
		"interval", d.opts.ScanInterval,
		"batch", d.opts.BatchSize,
	)

	for {
		select {
		case <-ctx.Done():
			brokerLog.Info("dispatcher stopping")
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	jobIDs, err := d.jobLister.ActiveJobIDs(ctx)
	if err != nil {
		brokerLog.Error("failed to list active jobs", "error", err)
		return
	}

	now := time.Now()

	concurrency := d.opts.ParallelJobs
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(jobIDs) {
		concurrency = len(jobIDs)
	}
	if concurrency == 0 {
		return
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
loop:
	for _, jobID := range jobIDs {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// Labelled break — `break` inside select exits the select only.
			break loop
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			dispatched, err := d.dispatchJob(ctx, id, now)
			if err != nil {
				brokerLog.Error("dispatch error", "error", err, "job_id", id)
			}
			if dispatched > 0 {
				brokerLog.Debug("dispatched tasks", "job_id", id, "dispatched", dispatched)
			}
		}(jobID)
	}
	wg.Wait()
}

// dispatchJob moves due items from the job's ZSET to its Stream.
// Returns the number of tasks successfully dispatched.
func (d *Dispatcher) dispatchJob(ctx context.Context, jobID string, now time.Time) (int, error) {
	entries, err := d.scheduler.DueItems(ctx, jobID, now, d.opts.BatchSize)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		d.clearStuck(jobID)
		return 0, nil
	}

	dispatched := 0
	for i := range entries {
		entry := &entries[i]

		canDispatch, err := d.concCheck.CanDispatch(ctx, jobID)
		if err != nil {
			brokerLog.Warn("concurrency check failed, skipping batch", "error", err, "job_id", jobID)
			observability.RecordBrokerDispatch(ctx, jobID, "err")
			break
		}
		if !canDispatch {
			observability.RecordBrokerDispatch(ctx, jobID, "capacity")
			d.maybeTriggerReconcile(ctx, jobID, now)
			break
		}
		// canDispatch=true falsifies the stuck hypothesis even if the
		// dispatch then loses to pacer pushback or a publish error.
		d.clearStuck(jobID)

		domain := entry.Host
		paceResult, err := d.pacer.TryAcquire(ctx, domain)
		if err != nil {
			brokerLog.Warn("pacer check failed", "error", err, "domain", domain)
			observability.RecordBrokerDispatch(ctx, jobID, "err")
			continue
		}
		if !paceResult.Acquired {
			// Redis-only reschedule: a synchronous Postgres UPDATE per
			// paced op collapsed throughput on 2026-04-22 at 100 ops/s.
			// Pacer push-back is ephemeral; OutboxSweeper rehydrates
			// from tasks.run_at if Redis loses state.
			newRunAt := now.Add(paceResult.RetryAfter)
			if err := d.scheduler.RescheduleZSet(ctx, *entry, newRunAt); err != nil {
				brokerLog.Warn("reschedule failed", "error", err, "task_id", entry.TaskID)
			}
			observability.RecordBrokerDispatch(ctx, jobID, "paced")
			observability.RecordBrokerPacerPushback(ctx, domain, "gate")
			continue
		}

		// On failure the TryAcquire gate expires on its own; do NOT
		// call Release because IncrementInflight has not run yet.
		if err := d.publishAndRemove(ctx, entry); err != nil {
			brokerLog.Warn("stream publish+remove failed", "error", err, "task_id", entry.TaskID)
			observability.RecordBrokerDispatch(ctx, jobID, "err")
			continue
		}

		if _, err := d.counters.Increment(ctx, jobID); err != nil {
			brokerLog.Warn("counter increment failed", "error", err, "job_id", jobID)
		}
		if err := d.pacer.IncrementInflight(ctx, domain, jobID); err != nil {
			brokerLog.Warn("inflight increment failed", "error", err, "domain", domain)
		}

		if d.onFirstDispatch != nil {
			if _, seen := d.firstDispatched.Load(jobID); !seen {
				if err := d.onFirstDispatch(ctx, jobID); err != nil {
					brokerLog.Warn("onFirstDispatch failed",
						"error", err, "job_id", jobID)
				} else {
					d.firstDispatched.Store(jobID, struct{}{})
				}
			}
		}

		observability.RecordBrokerDispatch(ctx, jobID, "ok")
		dispatched++
	}

	return dispatched, nil
}

// maybeTriggerReconcile fires the Reconciler when a job has been
// stuck for StuckThreshold, rate-limited to one trigger per 2×
// threshold. Closes the gap PR #362 left for unknown future
// `hover:running` drift classes.
func (d *Dispatcher) maybeTriggerReconcile(ctx context.Context, jobID string, now time.Time) {
	if d.reconciler == nil {
		return
	}
	threshold := d.opts.StuckThreshold
	if threshold <= 0 {
		return
	}

	d.stuckMu.Lock()
	if d.stuckSince == nil {
		d.stuckSince = make(map[string]time.Time)
	}
	if d.lastTrigger == nil {
		d.lastTrigger = make(map[string]time.Time)
	}

	first, ok := d.stuckSince[jobID]
	if !ok {
		d.stuckSince[jobID] = now
		d.stuckMu.Unlock()
		return
	}
	if now.Sub(first) < threshold {
		d.stuckMu.Unlock()
		return
	}
	if last, seen := d.lastTrigger[jobID]; seen && now.Sub(last) < 2*threshold {
		d.stuckMu.Unlock()
		return
	}
	d.lastTrigger[jobID] = now
	d.stuckMu.Unlock()

	brokerLog.Warn("dispatcher self-heal: triggering counter reconcile",
		"job_id", jobID, "stuck_for", now.Sub(first).String())
	d.reconciler.TriggerReconcile(ctx)
}

func (d *Dispatcher) clearStuck(jobID string) {
	d.stuckMu.Lock()
	defer d.stuckMu.Unlock()
	if d.stuckSince != nil {
		delete(d.stuckSince, jobID)
	}
}

// publishAndRemove atomically XADDs the task and ZREMs the ZSET entry
// in a single pipeline so a partial failure can't double-dispatch.
// Routes to StreamKey for "crawl" or LighthouseStreamKey for
// "lighthouse"; an unknown task_type is rejected.
func (d *Dispatcher) publishAndRemove(ctx context.Context, entry *ScheduleEntry) error {
	taskType := entry.TaskType
	if taskType == "" {
		taskType = "crawl"
	}

	var (
		streamKey string
		groupName string
	)
	switch taskType {
	case "crawl":
		streamKey = StreamKey(entry.JobID)
		groupName = ConsumerGroup(entry.JobID)
	case "lighthouse":
		// The analysis consumer reads run_id from the payload with no
		// DB fallback; reject so the row routes to dead-letter.
		if entry.LighthouseRunID <= 0 {
			return fmt.Errorf("broker: lighthouse task %s missing lighthouse_run_id", entry.TaskID)
		}
		streamKey = LighthouseStreamKey(entry.JobID)
		groupName = LighthouseConsumerGroup(entry.JobID)
	default:
		// Fail fast rather than silently routing to the crawl stream.
		return fmt.Errorf("broker: unknown task_type %q for task %s", taskType, entry.TaskID)
	}

	if err := d.ensureConsumerGroup(ctx, streamKey, groupName); err != nil {
		return fmt.Errorf("broker: ensure consumer group %s: %w", groupName, err)
	}

	values := map[string]interface{}{
		"task_id":     entry.TaskID,
		"job_id":      entry.JobID,
		"page_id":     strconv.Itoa(entry.PageID),
		"host":        entry.Host,
		"path":        entry.Path,
		"priority":    fmt.Sprintf("%.4f", entry.Priority),
		"retry_count": strconv.Itoa(entry.RetryCount),
		"source_type": entry.SourceType,
		"source_url":  entry.SourceURL,
		"task_type":   taskType,
	}
	if taskType == "lighthouse" {
		values["lighthouse_run_id"] = strconv.FormatInt(entry.LighthouseRunID, 10)
	}

	pipe := d.client.rdb.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: values,
	})
	pipe.ZRem(ctx, ScheduleKey(entry.JobID), entry.Member())

	_, err := pipe.Exec(ctx)
	return err
}

// ensureConsumerGroup creates the group idempotently and memoises the
// result. XGroupCreateMkStream is a full RTT even on BUSYGROUP — was
// the second largest per-task cost in the serial dispatcher.
func (d *Dispatcher) ensureConsumerGroup(ctx context.Context, streamKey, groupName string) error {
	if _, ok := d.groupEnsured.Load(groupName); ok {
		return nil
	}
	err := d.client.rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err != nil && !isGroupExistsErr(err) {
		return err
	}
	d.groupEnsured.Store(groupName, struct{}{})
	return nil
}

func isGroupExistsErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "BUSYGROUP")
}
