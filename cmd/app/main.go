package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"runtime/trace"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/good-native/hover/internal/api"
	"github.com/good-native/hover/internal/broker"
	"github.com/good-native/hover/internal/crawler"
	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/jobs"
	"github.com/good-native/hover/internal/logging"
	"github.com/good-native/hover/internal/loops"
	"github.com/good-native/hover/internal/notifications"
	"github.com/good-native/hover/internal/observability"
	"github.com/joho/godotenv"
	"golang.org/x/time/rate"
)

var startupLog = logging.Component("startup")

const (
	schedulerTickInterval   = 30 * time.Second
	schedulerBatchSize      = 50
	completionCheckInterval = 30 * time.Second
	healthCheckInterval     = 5 * time.Minute
)

func startJobScheduler(ctx context.Context, wg *sync.WaitGroup, jobsManager *jobs.JobManager, pgDB *db.DB) {
	defer wg.Done()

	ticker := time.NewTicker(schedulerTickInterval)
	defer ticker.Stop()

	startupLog.Info("Job scheduler started")

	for {
		select {
		case <-ctx.Done():
			startupLog.Info("Job scheduler stopped")
			return
		case <-ticker.C:
			runSchedulerTick(ctx, jobsManager, pgDB)
		}
	}
}

// runSchedulerTick executes one scheduler pass under a Sentry transaction so
// the existing `manager.create_job` spans get parented and bill as performance
// units. Tick frequency is bounded (every 30s), well within the perf-units
// quota.
func runSchedulerTick(ctx context.Context, jobsManager *jobs.JobManager, pgDB *db.DB) {
	txn := sentry.StartTransaction(ctx, "scheduler.tick")
	defer txn.Finish()
	tickCtx := txn.Context()

	schedulers, err := pgDB.GetSchedulersReadyToRun(tickCtx, schedulerBatchSize)
	if err != nil {
		// Warn not Error: 30s retry loop would flood Sentry on transient Postgres outage.
		startupLog.Warn("Failed to get schedulers ready to run", "error", err)
		return
	}

	domainIDs := make([]int, 0, len(schedulers))
	for _, scheduler := range schedulers {
		domainIDs = append(domainIDs, scheduler.DomainID)
	}

	domainNames, err := pgDB.GetDomainNames(tickCtx, domainIDs)
	if err != nil {
		startupLog.Warn("Failed to get domain names for schedulers", "error", err)
		return
	}

	for _, scheduler := range schedulers {
		domainName, ok := domainNames[scheduler.DomainID]
		if !ok {
			startupLog.Warn("Domain name not found", "domain_id", scheduler.DomainID, "scheduler_id", scheduler.ID)
			continue
		}

		lastJobStart, err := pgDB.GetLastJobStartTimeForScheduler(tickCtx, scheduler.ID)
		if err != nil {
			startupLog.Warn("Failed to get last job start time", "error", err, "scheduler_id", scheduler.ID)
			continue
		}

		if lastJobStart != nil {
			minInterval := time.Duration(scheduler.ScheduleIntervalHours) * time.Hour / 2
			timeSinceLastJob := time.Since(*lastJobStart)

			if timeSinceLastJob < minInterval {
				startupLog.Info("Skipping scheduled job - last job started too recently",
					"scheduler_id", scheduler.ID,
					"domain", domainName,
					"time_since_last_job", timeSinceLastJob,
					"minimum_interval", minInterval,
				)

				nextRun := time.Now().UTC().Add(time.Duration(scheduler.ScheduleIntervalHours) * time.Hour)
				if err := pgDB.UpdateSchedulerNextRun(tickCtx, scheduler.ID, nextRun); err != nil {
					startupLog.Warn("Failed to update scheduler next run", "error", err, "scheduler_id", scheduler.ID)
				}
				continue
			}
		}

		sourceType := "scheduler"
		opts := &jobs.JobOptions{
			Domain:                   domainName,
			OrganisationID:           &scheduler.OrganisationID,
			UseSitemap:               true,
			Concurrency:              scheduler.Concurrency,
			FindLinks:                scheduler.FindLinks,
			AllowCrossSubdomainLinks: true,
			MaxPages:                 scheduler.MaxPages,
			IncludePaths:             scheduler.IncludePaths,
			ExcludePaths:             scheduler.ExcludePaths,
			RequiredWorkers:          scheduler.RequiredWorkers,
			SourceType:               &sourceType,
			SourceDetail:             &scheduler.ID,
			SchedulerID:              &scheduler.ID,
		}

		job, err := jobsManager.CreateJob(tickCtx, opts)
		if err != nil {
			startupLog.Warn("Failed to create scheduled job", "error", err, "scheduler_id", scheduler.ID)
			continue
		}

		nextRun := time.Now().UTC().Add(time.Duration(scheduler.ScheduleIntervalHours) * time.Hour)
		if err := pgDB.UpdateSchedulerNextRun(tickCtx, scheduler.ID, nextRun); err != nil {
			startupLog.Warn("Failed to update scheduler next run", "error", err, "scheduler_id", scheduler.ID)
		} else {
			startupLog.Info("Created scheduled job",
				"scheduler_id", scheduler.ID,
				"job_id", job.ID,
				"domain", domainName,
				"next_run_at", nextRun,
			)
		}
	}
}

// redisClient may be nil when REDIS_URL is unset.
func startHealthMonitoring(ctx context.Context, wg *sync.WaitGroup, pgDB *db.DB, redisClient *broker.Client) {
	defer wg.Done()

	completionTicker := time.NewTicker(completionCheckInterval)
	defer completionTicker.Stop()

	healthTicker := time.NewTicker(healthCheckInterval)
	defer healthTicker.Stop()

	checkJobCompletion := func() {
		rows, err := pgDB.GetDB().Query(`
			UPDATE jobs
			SET status = 'completed', completed_at = NOW()
			WHERE (completed_tasks + failed_tasks) >= (total_tasks - COALESCE(skipped_tasks, 0))
			  AND status = 'running'
			RETURNING id
		`)
		if err != nil {
			startupLog.Error("Failed to update completed jobs", "error", err)
			return
		}

		// Release rows before per-job Redis cleanup; an open Rows iterator
		// holds a row lock on the Postgres connection.
		var completed []string
		for rows.Next() {
			var jobID string
			if err := rows.Scan(&jobID); err == nil {
				startupLog.Info("Job marked as completed", "job_id", jobID)
				completed = append(completed, jobID)
			}
		}
		_ = rows.Close()

		// Without this Redis keys leak forever (active-jobs filter drops
		// them). Partial cleanup is acceptable; reclaim sweeper retries.
		if redisClient != nil {
			for _, jobID := range completed {
				if err := redisClient.RemoveJobKeys(ctx, jobID); err != nil {
					startupLog.Warn("failed to clean up Redis keys for completed job",
						"error", err, "job_id", jobID)
				}
			}
		}
	}

	checkSystemHealth := func() {
		var totalStuckJobs int
		err := pgDB.GetDB().QueryRow(`
			SELECT COUNT(*)
			FROM jobs j
			WHERE j.status = 'running'
			  AND j.progress = 0
			  AND j.started_at < NOW() - INTERVAL '15 minutes'
		`).Scan(&totalStuckJobs)

		if err != nil {
			startupLog.Error("Failed to count stuck jobs", "error", err)
		}

		type stuckJobInfo struct {
			ID        string
			DomainID  int
			StartedAt time.Time
			Progress  float64
		}
		var stuckJobs []stuckJobInfo

		if totalStuckJobs > 0 {
			stuckJobRows, err := pgDB.GetDB().Query(`
				SELECT j.id, j.domain_id, j.started_at, j.progress
				FROM jobs j
				WHERE j.status = 'running'
				  AND j.progress = 0
				  AND j.started_at < NOW() - INTERVAL '15 minutes'
				ORDER BY j.started_at ASC
				LIMIT 10
			`)
			if err != nil {
				startupLog.Error("Failed to query stuck jobs sample", "error", err)
			} else {
				defer stuckJobRows.Close()
				for stuckJobRows.Next() {
					var job stuckJobInfo
					if err := stuckJobRows.Scan(&job.ID, &job.DomainID, &job.StartedAt, &job.Progress); err == nil {
						stuckJobs = append(stuckJobs, job)
					}
				}
			}
		}

		if totalStuckJobs > 0 && len(stuckJobs) > 0 {
			jobIDs := make([]string, len(stuckJobs))
			for i, job := range stuckJobs {
				jobIDs[i] = job.ID
			}

			startupLog.Error("CRITICAL: Jobs stuck without progress for >15 minutes",
				"total_stuck_jobs", totalStuckJobs,
				"sample_count", len(stuckJobs),
				"sample_job_ids", jobIDs,
				"oldest_job", stuckJobs[0].ID,
				"oldest_started_at", stuckJobs[0].StartedAt,
			)
		}

		var totalStuckTasks int
		var totalAffectedJobs int

		err = pgDB.GetDB().QueryRow(`
			SELECT COUNT(*), COUNT(DISTINCT job_id)
			FROM tasks
			WHERE status = 'running'
			  AND started_at < NOW() - INTERVAL '3 minutes'
		`).Scan(&totalStuckTasks, &totalAffectedJobs)

		if err != nil {
			startupLog.Error("Failed to count stuck tasks", "error", err)
		}

		type stuckTaskInfo struct {
			ID         string
			JobID      string
			Path       string
			StartedAt  time.Time
			RetryCount int
		}
		var stuckTasks []stuckTaskInfo

		if totalStuckTasks > 0 {
			stuckTaskRows, err := pgDB.GetDB().Query(`
				SELECT t.id, t.job_id, p.path, t.started_at, t.retry_count
				FROM tasks t
				JOIN pages p ON t.page_id = p.id
				WHERE t.status = 'running'
				  AND t.started_at < NOW() - INTERVAL '3 minutes'
				ORDER BY t.started_at ASC
				LIMIT 20
			`)
			if err != nil {
				startupLog.Error("Failed to query stuck tasks sample", "error", err)
			} else {
				defer stuckTaskRows.Close()
				for stuckTaskRows.Next() {
					var task stuckTaskInfo
					if err := stuckTaskRows.Scan(&task.ID, &task.JobID, &task.Path, &task.StartedAt, &task.RetryCount); err == nil {
						stuckTasks = append(stuckTasks, task)
					}
				}
			}
		}

		if totalStuckTasks > 0 && len(stuckTasks) > 0 {
			jobIDMap := make(map[string]struct{})
			for _, task := range stuckTasks {
				jobIDMap[task.JobID] = struct{}{}
			}
			sampleJobIDs := make([]string, 0, len(jobIDMap))
			for jobID := range jobIDMap {
				sampleJobIDs = append(sampleJobIDs, jobID)
			}

			startupLog.Error("CRITICAL: Tasks stuck in running state for >3 minutes",
				"total_stuck_tasks", totalStuckTasks,
				"total_affected_jobs", totalAffectedJobs,
				"sample_task_count", len(stuckTasks),
				"sample_job_count", len(sampleJobIDs),
				"sample_job_ids", sampleJobIDs,
				"oldest_task", stuckTasks[0].ID,
				"oldest_stuck_at", stuckTasks[0].StartedAt,
			)
		}
	}

	// Wrap each tick in a Sentry transaction so the existing
	// `db.cleanup_stuck_jobs` span (and any future spans inside the
	// completion path) get parented and bill as performance units.
	tickInTransaction := func(name string, fn func()) {
		txn := sentry.StartTransaction(ctx, name)
		defer txn.Finish()
		fn()
	}

	tickInTransaction("health.completion_check", checkJobCompletion)

	startupLog.Info("Health monitoring started")

	for {
		select {
		case <-ctx.Done():
			startupLog.Info("Health monitoring stopped")
			return
		case <-completionTicker.C:
			tickInTransaction("health.completion_check", checkJobCompletion)
		case <-healthTicker.C:
			tickInTransaction("health.system_check", checkSystemHealth)
		}
	}
}

type Config struct {
	Port                  string
	Env                   string
	SentryDSN             string
	LogLevel              string
	FlightRecorderEnabled bool
	ObservabilityEnabled  bool
	MetricsAddr           string
	OTLPEndpoint          string
	OTLPHeaders           string
	OTLPInsecure          bool
	StripeSecretKey       string
	StripeWebhookSecret   string
	StripePublishableKey  string
	StripePortalConfigID  string
}

//nolint:gocyclo // main function setup is naturally complex but straightforward setup logic
func main() {
	logLevelFlag := flag.String("log-level", "", "Log level (debug, info, warn, error) - overrides LOG_LEVEL env var")
	flag.Parse()

	if err := godotenv.Load(".env.local", ".env"); err != nil {
		startupLog.Debug("Notice: .env files not loaded (expected in some environments)", "error", err)
	}

	logLevel := getEnvWithDefault("LOG_LEVEL", "info")
	if *logLevelFlag != "" {
		logLevel = *logLevelFlag
	}

	config := &Config{
		Port:                  getEnvWithDefault("PORT", "8080"),
		Env:                   getEnvWithDefault("APP_ENV", "development"),
		SentryDSN:             os.Getenv("SENTRY_DSN"),
		LogLevel:              logLevel,
		FlightRecorderEnabled: getEnvWithDefault("FLIGHT_RECORDER_ENABLED", "false") == "true",
		ObservabilityEnabled:  getEnvWithDefault("OBSERVABILITY_ENABLED", "true") == "true",
		MetricsAddr:           getEnvWithDefault("METRICS_ADDR", ":9464"),
		OTLPEndpoint:          os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		OTLPHeaders:           os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
		OTLPInsecure:          getEnvWithDefault("OTEL_EXPORTER_OTLP_INSECURE", "false") == "true",
		StripeSecretKey:       os.Getenv("STRIPE_SECRET_KEY"),
		StripeWebhookSecret:   os.Getenv("STRIPE_WEBHOOK_SECRET"),
		StripePublishableKey:  os.Getenv("STRIPE_PUBLISHABLE_KEY"),
		StripePortalConfigID:  os.Getenv("STRIPE_PORTAL_CONFIG_ID"),
	}

	if config.FlightRecorderEnabled {
		f, err := os.Create("trace.out")
		if err != nil {
			startupLog.Fatal("failed to create trace file", "error", err)
		}

		if err := trace.Start(f); err != nil {
			startupLog.Fatal("failed to start flight recorder", "error", err)
		}
		startupLog.Info("Flight recorder enabled, writing to trace.out")

		defer func() {
			trace.Stop()
			if err := f.Close(); err != nil {
				startupLog.Error("failed to close trace file", "error", err)
			}
			startupLog.Info("Flight recorder stopped and trace file closed.")
		}()
	}

	// Init before setupLogging so the fanout handler wires the existing client.
	if config.SentryDSN != "" {
		sentryFlush, err := logging.InitSentry(logging.SentryOptions{
			DSN:              config.SentryDSN,
			Environment:      config.Env,
			Process:          "app",
			TracesSampleRate: appTracesSampleRate(config.Env),
			Debug:            config.Env == "development",
		})
		if err != nil {
			startupLog.Warn("Failed to initialise Sentry", "error", err)
		} else {
			startupLog.Info("Sentry initialised successfully", "environment", config.Env)
			defer sentryFlush()
		}
	} else {
		startupLog.Warn("Sentry DSN not configured, error tracking disabled")
	}

	setupLogging(config)
	defer func() {
		if a := logging.StdoutAsync(); a != nil {
			a.Close()
		}
	}()

	var obsProviders *observability.Providers

	if config.ObservabilityEnabled {
		// FLY_APP_NAME distinguishes review apps (hover-pr-342) from prod.
		serviceName := strings.TrimSpace(os.Getenv("FLY_APP_NAME"))
		if serviceName == "" {
			serviceName = "hover"
		}
		// EnablePprof intentionally false — pprof on cmd/app would expose a
		// debug surface alongside the public HTTP listener.
		metricsSrv, err := observability.StartMetricsServer(context.Background(), observability.MetricsServerOptions{
			ServiceName:    serviceName,
			Environment:    config.Env,
			OTLPEndpoint:   strings.TrimSpace(config.OTLPEndpoint),
			OTLPHeaders:    observability.ParseOTLPHeaders(config.OTLPHeaders),
			OTLPInsecure:   config.OTLPInsecure,
			MetricsAddress: config.MetricsAddr,
		})
		if err != nil {
			startupLog.Warn("Failed to initialise observability providers", "error", err)
		} else {
			obsProviders = metricsSrv.Providers
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				metricsSrv.Shutdown(shutdownCtx)
			}()
		}
	}

	appEnv := os.Getenv("APP_ENV")

	// 5m timeout accommodates Supabase maintenance windows.
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer dbCancel()

	pgDB, err := db.WaitForDatabase(dbCtx, 5*time.Minute)
	if err != nil {
		startupLog.Fatal("Failed to connect to PostgreSQL database", "error", err)
	}
	defer func() {
		startupLog.Info("Closing database connections")
		time.Sleep(1 * time.Second)
		if err := pgDB.Close(); err != nil {
			startupLog.Error("Error closing database", "error", err)
		}
		startupLog.Info("Database closed")
	}()

	startupLog.Info("Connected to PostgreSQL database")

	queueDB := pgDB
	if queueURL := strings.TrimSpace(os.Getenv("DATABASE_QUEUE_URL")); queueURL != "" {
		// Fresh context: primary connection may have consumed shared budget.
		queueCtx, queueCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer queueCancel()

		queueConn, err := db.InitFromURLWithSuffixRetry(queueCtx, queueURL, appEnv, "queue")
		if err != nil {
			startupLog.Fatal("Failed to connect to queue PostgreSQL database", "error", err)
		}

		defer func() {
			startupLog.Info("Closing queue database connections")
			time.Sleep(500 * time.Millisecond)
			if err := queueConn.Close(); err != nil {
				startupLog.Error("Error closing queue database", "error", err)
			}
			startupLog.Info("Queue database closed")
		}()

		queueDB = queueConn
		startupLog.Info("Queue database connection established")
	}

	crawlerConfig := crawler.DefaultConfig()
	cr := crawler.New(crawlerConfig)

	dbQueue := db.NewDbQueue(queueDB)

	redisCfg := broker.ConfigFromEnv()

	jobsManager := jobs.NewJobManager(pgDB.GetDB(), dbQueue, cr)

	// Outer scope so admin reset endpoints can reach it; nil when REDIS_URL unset.
	var redisClient *broker.Client

	if redisCfg.URL != "" {
		client, err := broker.NewClient(redisCfg)
		if err != nil {
			startupLog.Fatal("failed to create Redis client", "error", err)
		}
		redisClient = client
		defer redisClient.Close()

		if err := redisClient.Ping(context.Background()); err != nil {
			startupLog.Fatal("failed to ping Redis", "error", err)
		}
		startupLog.Info("connected to Redis")

		// DB-backed for parity with worker; any Reschedule call must dual-write.
		scheduler := broker.NewSchedulerWithDB(redisClient, pgDB.GetDB())

		// Respects RunAt so waiting/retry rows keep their backoff deadline.
		jobsManager.OnTasksEnqueued = func(ctx context.Context, jobID string, entries []jobs.TaskScheduleEntry) {
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
			if len(schedEntries) > 0 {
				if err := scheduler.ScheduleBatch(ctx, schedEntries); err != nil {
					startupLog.Error("failed to schedule tasks into Redis",
						"error", err, "job_id", jobID, "count", len(schedEntries))
				}
			}
		}

		// CancelJob path; auto-completion uses redisClient directly in startHealthMonitoring.
		jobsManager.OnJobTerminated = func(ctx context.Context, jobID string) {
			if err := redisClient.RemoveJobKeys(ctx, jobID); err != nil {
				startupLog.Warn("failed to clean up Redis keys for terminated job",
					"error", err, "job_id", jobID)
			}
		}
	} else {
		startupLog.Warn("REDIS_URL not set — task dispatch to Redis is disabled; API will still create tasks in Postgres")
	}

	notificationService := notifications.NewService(pgDB)
	slackChannel, err := notifications.NewSlackChannel(pgDB)
	if err != nil {
		startupLog.Warn("Failed to create Slack channel - notifications disabled", "error", err)
	} else {
		notificationService.AddChannel(slackChannel)
		startupLog.Info("Slack notification channel enabled")
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	var backgroundWG sync.WaitGroup

	limiter := newRateLimiter()

	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if googleClientID == "" || googleClientSecret == "" {
		startupLog.Info("GA4 integration unavailable: GOOGLE_CLIENT_ID or GOOGLE_CLIENT_SECRET not configured")
	}

	var loopsClient *loops.Client
	if loopsAPIKey := os.Getenv("LOOPS_API_KEY"); loopsAPIKey != "" {
		loopsClient = loops.New(loopsAPIKey)
		startupLog.Info("Loops email client initialised")
	} else {
		startupLog.Info("Loops email client unavailable: LOOPS_API_KEY not configured")
	}

	var brokerCleaner api.BrokerCleaner
	if redisClient != nil {
		brokerCleaner = redisClient
	}
	apiHandler := api.NewHandler(
		pgDB,
		jobsManager,
		loopsClient,
		brokerCleaner,
		googleClientID,
		googleClientSecret,
		config.StripeSecretKey,
		config.StripeWebhookSecret,
		config.StripePublishableKey,
		config.StripePortalConfigID,
		getEnvWithDefault("SETTINGS_URL", ""),
	)

	mux := http.NewServeMux()

	apiHandler.SetupRoutes(mux)

	// Static assets bypass rate limiting: hard refresh fires many parallel requests.
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		isStatic := strings.HasPrefix(p, "/js/") ||
			strings.HasPrefix(p, "/app/") ||
			strings.HasPrefix(p, "/styles/") ||
			strings.HasPrefix(p, "/assets/") ||
			strings.HasPrefix(p, "/web/") ||
			strings.HasPrefix(p, "/images/") ||
			p == "/config.js" ||
			p == "/favicon.ico"
		if !isStatic {
			ip := getClientIP(r)
			if !limiter.getLimiter(ip).Allow() {
				api.WriteErrorMessage(w, r, "Too many requests", http.StatusTooManyRequests, api.ErrCodeRateLimit)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})

	// Reverse order: outermost first.
	handler = api.LoggingMiddleware(handler)
	handler = api.RequestIDMiddleware(handler)
	handler = api.SecurityHeadersMiddleware(handler)
	handler = api.CrossOriginProtectionMiddleware(handler)
	handler = api.CORSMiddleware(handler)
	// Sentry HTTP wrapper opens a transaction per request so the existing
	// manager.* spans in jobs/manager.go become children and bill as
	// performance units. TracesSampleRate from logging.InitSentry caps volume.
	handler = sentryhttp.New(sentryhttp.Options{Repanic: true}).Handle(handler)
	handler = observability.WrapHandler(handler, obsProviders)

	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second, // G112: Slowloris.
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan struct{})

	serverErrCh := make(chan error, 1)

	go func() {
		startupLog.Info("Starting server", "port", config.Port)

		// Bind before announcing readiness so bind failure surfaces correctly.
		listener, err := net.Listen("tcp", ":"+config.Port)
		if err != nil {
			serverErrCh <- fmt.Errorf("failed to bind %s: %w", config.Port, err)
			return
		}

		baseURL := fmt.Sprintf("http://localhost:%s", config.Port)
		startupLog.Info("Hover server ready", "environment", config.Env)
		startupLog.Info("Open Homepage", "homepage", baseURL)
		startupLog.Info("Open Dashboard", "dashboard", baseURL+"/dashboard")
		startupLog.Info("Health Check", "health", baseURL+"/health")
		if config.Env == "development" {
			startupLog.Info("Open Supabase Studio", "supabase_studio", "http://localhost:54323")
		}

		if err := server.Serve(listener); err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	go func() {
		<-stop
		startupLog.Info("Shutting down server...")

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			startupLog.Error("Server forced to shutdown", "error", err)
		}

		close(done)
	}()

	// Task execution lives in cmd/worker; this server only schedules into Redis.

	backgroundWG.Add(1)
	go startHealthMonitoring(appCtx, &backgroundWG, pgDB, redisClient)

	backgroundWG.Add(1)
	go startJobScheduler(appCtx, &backgroundWG, jobsManager, pgDB)

	backgroundWG.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				startupLog.Error("Recovered panic in notification listener", "panic", r)
			}
		}()

		notifications.StartWithFallback(appCtx, pgDB.GetConfig().ConnectionString(), notificationService)
	})

	var serverErr error
	select {
	case serverErr = <-serverErrCh:
	case <-done:
		serverErr = <-serverErrCh
	}

	appCancel()
	backgroundWG.Wait()
	startupLog.Info("All background goroutines stopped")

	if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
		startupLog.Fatal("Server error", "error", serverErr)
	}

	startupLog.Info("Server stopped")
}

func getEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// Production runs at 10% to keep trace volume within budget; non-prod
// environments sample fully so review apps emit a usable trace stream.
func appTracesSampleRate(env string) float64 {
	if env == "production" {
		return 0.1
	}
	return 1.0
}

func setupLogging(config *Config) {
	logging.Setup(logging.ParseLevel(config.LogLevel), config.Env)
}

type RateLimiter struct {
	limits   map[string]*IPRateLimiter
	mu       sync.Mutex
	rate     rate.Limit
	capacity int
}

type IPRateLimiter struct {
	limiter *rate.Limiter
}

func newRateLimiter() *RateLimiter {
	return &RateLimiter{
		limits:   make(map[string]*IPRateLimiter),
		rate:     rate.Limit(20),
		capacity: 10,
	}
}

func (rl *RateLimiter) getLimiter(ip string) *IPRateLimiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limiter, exists := rl.limits[ip]
	if !exists {
		limiter = &IPRateLimiter{
			limiter: rate.NewLimiter(rl.rate, rl.capacity),
		}
		rl.limits[ip] = limiter
	}

	return limiter
}

func (ipl *IPRateLimiter) Allow() bool {
	return ipl.limiter.Allow()
}

func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip != "" {
		// May contain multiple IPs; first is the originating client.
		ips := strings.Split(ip, ",")
		ip = strings.TrimSpace(ips[0])
		return ip
	}

	ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	return ip
}
