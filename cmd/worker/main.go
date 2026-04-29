package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Harvey-AU/hover/internal/archive"
	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/jobs"
	"github.com/Harvey-AU/hover/internal/lighthouse"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/Harvey-AU/hover/internal/watchdog"
	"github.com/getsentry/sentry-go"
	"github.com/lib/pq"
)

var workerLog = logging.Component("worker")

func main() {
	appEnv := os.Getenv("APP_ENV")

	// Init before logging.Setup so the sentry slog handler can attach.
	if dsn := os.Getenv("SENTRY_DSN"); dsn != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:              dsn,
			Environment:      appEnv,
			AttachStacktrace: true,
			BeforeSend:       logging.BeforeSend,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "failed to initialise Sentry: %v\n", err)
		} else {
			defer sentry.Flush(2 * time.Second)
		}
	}

	logging.Setup(logging.ParseLevel(os.Getenv("LOG_LEVEL")), appEnv)
	defer flushAsyncLogs()

	workerLog.Info("hover worker starting")

	if os.Getenv("OBSERVABILITY_ENABLED") == "true" {
		// FLY_APP_NAME distinguishes review apps (hover-worker-pr-342) from prod.
		serviceName := strings.TrimSpace(os.Getenv("FLY_APP_NAME"))
		if serviceName == "" {
			serviceName = "hover-worker"
		}
		metricsAddr := os.Getenv("METRICS_ADDR")
		if metricsAddr == "" {
			metricsAddr = ":9464"
		}
		providers, err := observability.Init(context.Background(), observability.Config{
			Enabled:        true,
			ServiceName:    serviceName,
			Environment:    appEnv,
			OTLPEndpoint:   strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
			OTLPHeaders:    observability.ParseOTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
			MetricsAddress: metricsAddr,
		})
		if err != nil {
			workerLog.Warn("failed to initialise observability", "error", err)
		} else {
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = providers.Shutdown(ctx)
			}()

			// Alloy sidecar scrapes /metrics here to add app/environment labels;
			// pure OTLP push bypasses the dashboard's app filter. pprof shares the
			// port (Fly internal network only, no auth guard needed).
			if providers.MetricsHandler != nil && metricsAddr != "" {
				mux := http.NewServeMux()
				mux.Handle("/metrics", providers.MetricsHandler)
				mux.HandleFunc("/debug/pprof/", pprof.Index)
				mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
				mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
				mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
				mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
				metricsSrv := &http.Server{
					Addr:              metricsAddr,
					Handler:           mux,
					ReadHeaderTimeout: 5 * time.Second,
				}
				metricsListener, err := net.Listen("tcp", metricsAddr)
				if err != nil {
					workerLog.Error("metrics server failed to bind", "error", err, "addr", metricsAddr)
				} else {
					go func() {
						workerLog.Info("metrics server listening", "addr", metricsAddr)
						if err := metricsSrv.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
							workerLog.Error("metrics server failed", "error", err)
						}
					}()
					defer func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						if err := metricsSrv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
							workerLog.Warn("graceful shutdown of metrics server failed", "error", err)
						}
					}()
				}
			}
		}
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer dbCancel()

	pgDB, err := db.WaitForDatabase(dbCtx, 5*time.Minute)
	if err != nil {
		workerLog.Fatal("failed to connect to PostgreSQL", "error", err)
	}
	defer func() {
		workerLog.Info("closing database connection")
		// Let in-flight batch flushes and counter syncs complete first.
		time.Sleep(1 * time.Second)
		_ = pgDB.Close()
	}()

	queueDB := pgDB
	if queueURL := strings.TrimSpace(os.Getenv("DATABASE_QUEUE_URL")); queueURL != "" {
		queueCtx, queueCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer queueCancel()
		qConn, err := db.InitFromURLWithSuffixRetry(queueCtx, queueURL, appEnv, "queue")
		if err != nil {
			workerLog.Fatal("failed to connect to queue PostgreSQL", "error", err)
		}
		defer func() {
			time.Sleep(500 * time.Millisecond)
			_ = qConn.Close()
		}()
		queueDB = qConn
	}

	dbQueue := db.NewDbQueue(queueDB)

	redisCfg := broker.ConfigFromEnv()
	redisClient, err := broker.NewClient(redisCfg)
	if err != nil {
		workerLog.Fatal("failed to create Redis client", "error", err)
	}
	defer redisClient.Close()

	if err := redisClient.Ping(context.Background()); err != nil {
		workerLog.Fatal("failed to ping Redis", "error", err)
	}
	workerLog.Info("connected to Redis")

	// DB-backed scheduler so Reschedule mirrors pacing push-backs to
	// tasks.run_at (survives a Redis flush). queueDB holds tasks + task_outbox
	// when DATABASE_QUEUE_URL is set.
	scheduler := broker.NewSchedulerWithDB(redisClient, queueDB.GetDB())
	pacerCfg := broker.DefaultPacerConfig()
	pacer := broker.NewDomainPacer(redisClient, pacerCfg)
	counters := broker.NewRunningCounters(redisClient)

	// Adaptive-delay state lives in Redis with 24h TTL; without this flush a
	// brief 429 burst can throttle a domain for a full day after upstream
	// recovers. Disable via GNH_PACER_FLUSH_ON_START=false.
	if strings.TrimSpace(os.Getenv("GNH_PACER_FLUSH_ON_START")) != "false" {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 10*time.Second)
		deleted, err := pacer.FlushAdaptiveDelays(flushCtx)
		flushCancel()
		if err != nil {
			workerLog.Warn("pacer adaptive-delay flush failed", "error", err)
		} else {
			workerLog.Info("pacer adaptive-delay flush complete", "keys_deleted", deleted)
		}
	}

	sweepOrphanInflightOnBoot(redisClient, pgDB.GetDB())

	machineName := os.Getenv("FLY_MACHINE_ID")
	if machineName == "" {
		machineName, _ = os.Hostname()
	}

	numWorkers := envInt("WORKER_COUNT", 30)
	if numWorkers < 1 {
		workerLog.Fatal("WORKER_COUNT must be >= 1", "value", numWorkers)
	}
	// Global task ceiling = numWorkers × tasksPerWorker.
	tasksPerWorker := envInt("WORKER_CONCURRENCY", 20)
	if tasksPerWorker < 1 {
		workerLog.Fatal("WORKER_CONCURRENCY must be >= 1", "value", tasksPerWorker)
	}
	consumerOpts := broker.DefaultConsumerOpts(fmt.Sprintf("worker-%s", machineName))
	consumer := broker.NewConsumer(redisClient, consumerOpts)

	crawlerCfg := crawler.DefaultConfig()
	cr := crawler.New(crawlerCfg)

	executorCfg := jobs.DefaultExecutorConfig()
	executor := jobs.NewTaskExecutor(cr, executorCfg)

	batchManager := db.NewBatchManager(dbQueue)
	defer batchManager.Stop()

	htmlPersister := buildHTMLPersister(workerLog, dbQueue)

	jobManager := jobs.NewJobManager(pgDB.GetDB(), dbQueue, cr)
	jobManager.OnTasksEnqueued = makeTasksEnqueuedHandler(scheduler, workerLog)

	lhScheduler := lighthouse.NewScheduler(pgDB, dbQueue)
	jobManager.OnProgressMilestone = func(ctx context.Context, jobID string, oldPct, newPct int) {
		if err := lhScheduler.OnMilestone(ctx, jobID, newPct); err != nil {
			workerLog.Warn("lighthouse scheduler OnMilestone failed",
				"error", err, "job_id", jobID, "milestone", newPct)
		}
	}

	batchManager.SetOnBatchFlushed(jobManager.MaybeFireMilestones)

	swpOpts := jobs.StreamWorkerOpts{
		NumWorkers:      numWorkers,
		TasksPerWorker:  tasksPerWorker,
		ReclaimInterval: 30 * time.Second,
	}

	wafBreaker := jobs.NewWAFCircuitBreaker()

	swp := jobs.NewStreamWorkerPool(jobs.StreamWorkerDeps{
		Consumer:      consumer,
		Scheduler:     scheduler,
		Counters:      counters,
		Pacer:         pacer,
		Executor:      executor,
		BatchManager:  batchManager,
		DBQueue:       dbQueue,
		JobManager:    jobManager,
		HTMLPersister: htmlPersister,
		WAFBreaker:    wafBreaker,
	}, swpOpts)

	// Drain per-job breaker state when a job terminates so a long-running
	// worker doesn't accumulate map entries.
	previousOnJobTerminated := jobManager.OnJobTerminated
	jobManager.OnJobTerminated = func(ctx context.Context, jobID string) {
		wafBreaker.Forget(jobID)
		if previousOnJobTerminated != nil {
			previousOnJobTerminated(ctx, jobID)
		}
	}

	// Last-resort recovery: Fly's restart=always brings the worker back
	// with fresh state when a Go-side wedge prevents in-process recovery.
	heartbeat := &watchdog.Heartbeat{}
	swp.SetHeartbeat(heartbeat)

	dispatcherOpts := broker.DefaultDispatcherOpts()
	dispatcher := broker.NewDispatcher(
		redisClient, scheduler, pacer, counters,
		swp,
		swp,
		dispatcherOpts,
	)

	// Without this, CreateJob jobs stay 'pending' forever and the dashboard
	// "Starting up" pill never goes away. MarkJobRunning is idempotent.
	dispatcher.SetOnFirstDispatch(func(ctx context.Context, jobID string) error {
		return jobManager.MarkJobRunning(ctx, jobID)
	})

	// Self-heal lever for the running-counter drift class PR #362 could
	// not fully eliminate. Rate-limited to one trigger per 2× threshold per job.
	dispatcher.SetReconciler(swp)

	counterSyncSec := envInt("REDIS_COUNTER_SYNC_INTERVAL_S", 5)
	if counterSyncSec < 1 {
		counterSyncSec = 5
	}
	syncInterval := time.Duration(counterSyncSec) * time.Second
	syncFn := broker.DefaultDBSyncFunc(pgDB.GetDB())

	// Drains task_outbox so durable Redis scheduling survives a lost
	// fire-and-forget OnTasksEnqueued callback (Redis blip, crash).
	outboxOpts := broker.DefaultOutboxSweeperOpts()
	if v := envInt("OUTBOX_SWEEP_INTERVAL_MS", 0); v > 0 {
		outboxOpts.Interval = time.Duration(v) * time.Millisecond
	}
	if v := envInt("OUTBOX_SWEEP_BATCH_SIZE", 0); v > 0 {
		outboxOpts.BatchSize = v
	}
	if v := envInt("OUTBOX_SWEEP_STATEMENT_TIMEOUT_MS", 0); v > 0 {
		outboxOpts.StatementTimeout = time.Duration(v) * time.Millisecond
	}
	// task_outbox is written in the same tx as tasks, so it lives on queueDB.
	outboxSweeper := broker.NewOutboxSweeper(queueDB.GetDB(), scheduler, outboxOpts)

	// Tier 1 gauges have no natural emission site in the request path.
	probeOpts := broker.DefaultProbeOpts()
	probe := broker.NewProbe(redisClient, queueDB.GetDB(), swp, probeOpts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Decoupled from ctx: cancelling persister on SIGTERM would drop HTML
	// payloads enqueued during swp.Stop into a queue with no readers.
	persisterCtx, persisterCancel := context.WithCancel(context.Background())
	defer persisterCancel()

	if htmlPersister != nil {
		htmlPersister.Start(persisterCtx)
	}
	swp.Start(ctx)
	go dispatcher.Run(ctx)
	go counters.StartDBSync(ctx, syncInterval, syncFn)
	go outboxSweeper.Run(ctx)
	go probe.Run(ctx)

	go startWatchdog(ctx, heartbeat, swp)

	workerLog.Info("hover worker ready",
		"workers", numWorkers,
		"tasks_per_worker", tasksPerWorker,
		"task_slots", numWorkers*tasksPerWorker,
		"machine", machineName,
	)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	workerLog.Info("shutdown signal received", "signal", sig.String())

	cancel()

	done := make(chan struct{})
	go func() {
		swp.Stop()
		// Stop persister after worker pool drains so final outcomes reach the queue.
		if htmlPersister != nil {
			htmlPersister.Stop()
		}
		persisterCancel()
		close(done)
	}()

	select {
	case <-done:
		workerLog.Info("all workers stopped cleanly")
	case <-time.After(3 * time.Minute):
		workerLog.Warn("shutdown timed out after 3 minutes")
	}
}

func flushAsyncLogs() {
	if a := logging.StdoutAsync(); a != nil {
		a.Close()
	}
}

// StallThreshold (3m) exceeds the 2m per-task timeout in
// stream_worker.processTask with a 60s margin; heartbeat only ticks
// after handleOutcome returns.
func startWatchdog(ctx context.Context, hb *watchdog.Heartbeat, swp *jobs.StreamWorkerPool) {
	watchdog.Run(ctx, hb, watchdog.Options{
		StallThreshold: 3 * time.Minute,
		CheckInterval:  15 * time.Second,
		GracePeriod:    2 * time.Minute,
		HasWork: func(checkCtx context.Context) bool {
			ids, err := swp.ActiveJobIDs(checkCtx)
			if err != nil {
				// False-trip is safer than missing a real wedge.
				return true
			}
			return len(ids) > 0
		},
		Logger: slog.Default().With("component", "watchdog"),
	})
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// Respects each entry's RunAt so waiting/retry rows keep their backoff
// deadline instead of being scheduled as ready-now.
func makeTasksEnqueuedHandler(scheduler *broker.Scheduler, log *logging.Logger) func(context.Context, string, []jobs.TaskScheduleEntry) {
	return func(ctx context.Context, jobID string, entries []jobs.TaskScheduleEntry) {
		schedEntries := make([]broker.ScheduleEntry, 0, len(entries))
		for _, e := range entries {
			if e.Status == "skipped" {
				continue
			}
			runAt := e.RunAt
			if runAt.IsZero() {
				runAt = time.Now()
			}
			schedEntries = append(schedEntries, broker.ScheduleEntry{
				TaskID:     e.TaskID,
				JobID:      jobID,
				PageID:     e.PageID,
				Host:       e.Host,
				Path:       e.Path,
				Priority:   e.Priority,
				RetryCount: e.RetryCount,
				SourceType: e.SourceType,
				SourceURL:  e.SourceURL,
				RunAt:      runAt,
			})
		}
		if len(schedEntries) == 0 {
			return
		}
		if err := scheduler.ScheduleBatch(ctx, schedEntries); err != nil {
			log.Error("failed to schedule tasks into Redis",
				"error", err, "job_id", jobID, "count", len(schedEntries))
		}
	}
}

func buildHTMLPersister(log *logging.Logger, dbQueue *db.DbQueue) *jobs.HTMLPersister {
	// Treat partial config as fatal: silent disable would recreate the
	// missing-capture failure mode this stage exists to fix.
	provider := strings.TrimSpace(os.Getenv("ARCHIVE_PROVIDER"))
	bucket := strings.TrimSpace(os.Getenv("ARCHIVE_BUCKET"))
	switch {
	case provider == "" && bucket == "":
		log.Info("ARCHIVE_PROVIDER/ARCHIVE_BUCKET unset — html persister disabled")
		return nil
	case provider == "" || bucket == "":
		log.Fatal("html persister misconfigured — set both ARCHIVE_PROVIDER and ARCHIVE_BUCKET, or neither",
			"archive_provider_set", provider != "",
			"archive_bucket_set", bucket != "")
	}

	archCfg := archive.ConfigFromEnv()
	if archCfg == nil {
		log.Fatal("html persister: ConfigFromEnv returned nil despite provider+bucket set")
	}

	coldProvider, err := archive.ProviderFromEnv()
	if err != nil {
		log.Fatal("failed to construct cold storage provider for html persister", "error", err)
	}
	if coldProvider == nil {
		log.Warn("ARCHIVE_PROVIDER set but provider construction returned nil; html persistence disabled")
		return nil
	}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := coldProvider.Ping(pingCtx, archCfg.Bucket); err != nil {
		log.Fatal("html persister bucket ping failed", "error", err, "bucket", archCfg.Bucket)
	}

	cfg := jobs.DefaultHTMLPersisterConfig()
	cfg.Bucket = archCfg.Bucket
	cfg.Provider = coldProvider.Provider()
	if v := envInt("HTML_PERSIST_WORKERS", 0); v > 0 {
		cfg.Workers = v
	}
	if v := envInt("HTML_PERSIST_QUEUE", 0); v > 0 {
		cfg.QueueSize = v
	}
	if v := envInt("HTML_PERSIST_BATCH_SIZE", 0); v > 0 {
		cfg.BatchSize = v
	}
	if d := envDuration("HTML_PERSIST_FLUSH_INTERVAL", 0); d > 0 {
		cfg.FlushInterval = d
	}
	if d := envDuration("HTML_PERSIST_UPLOAD_TIMEOUT", 0); d > 0 {
		cfg.UploadTimeout = d
	}

	persister, err := jobs.NewHTMLPersister(cfg, jobs.HTMLPersisterDeps{
		Provider: coldProvider,
		DBQueue:  dbQueue,
	})
	if err != nil {
		log.Fatal("failed to construct html persister", "error", err)
	}
	log.Info("html persister wired",
		"bucket", cfg.Bucket,
		"provider", cfg.Provider,
		"workers", cfg.Workers,
		"queue", cfg.QueueSize,
		"batch_size", cfg.BatchSize,
	)
	return persister
}

// SIGKILL (Fly OOM, panic, force-redeploy) skips the graceful drain so
// dom:flight increments run but decrements don't, and unlike the
// running-counter HASH there is no dedicated reconciler. Best-effort:
// failures are logged and tolerated.
func sweepOrphanInflightOnBoot(redisClient *broker.Client, sqlDB *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var activeJobIDs []string
	// 'initializing' is a live pre-running state; MarkJobRunning flips it
	// to 'running' on first publish. Excluding it would wipe dom:flight
	// entries for jobs about to dispatch.
	err := sqlDB.QueryRowContext(ctx, `
		SELECT COALESCE(array_agg(id::text), ARRAY[]::text[])
		  FROM jobs
		 WHERE status IN ('running', 'pending', 'initializing')
	`).Scan(pq.Array(&activeJobIDs))
	if err != nil {
		workerLog.Warn("dom:flight orphan sweep skipped — active job query failed",
			"error", err)
		return
	}

	removed, err := redisClient.SweepOrphanInflight(ctx, activeJobIDs)
	if err != nil {
		workerLog.Warn("dom:flight orphan sweep failed", "error", err)
		return
	}
	if removed > 0 {
		workerLog.Info("dom:flight orphan sweep complete",
			"fields_removed", removed, "active_jobs", len(activeJobIDs))
	}
}

var (
	_ broker.JobLister          = (*jobs.StreamWorkerPool)(nil)
	_ broker.ConcurrencyChecker = (*jobs.StreamWorkerPool)(nil)
	_ broker.Reconciler         = (*jobs.StreamWorkerPool)(nil)
)
