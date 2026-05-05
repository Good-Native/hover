// Dedicated lighthouse audit service: consumes per-job streams
// (stream:{jobID}:lh) and writes results back into lighthouse_runs.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/good-native/hover/internal/archive"
	"github.com/good-native/hover/internal/broker"
	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/lighthouse"
	"github.com/good-native/hover/internal/logging"
	"github.com/good-native/hover/internal/observability"
	"github.com/redis/go-redis/v9"
)

var analysisLog = logging.Component("analysis")

// Mirror dispatcher.publishAndRemove so producer/consumer drift is greppable.
const (
	streamFieldRunID = "lighthouse_run_id"
	streamFieldJobID = "job_id"
	streamFieldURL   = "source_url"
	streamFieldHost  = "host"
	streamFieldPath  = "path"
)

func main() {
	appEnv := os.Getenv("APP_ENV")

	// Sentry first so logging.Setup can wire its handler.
	sentryFlush, err := logging.InitSentry(logging.SentryOptions{
		DSN:         os.Getenv("SENTRY_DSN"),
		Environment: appEnv,
		Process:     "analysis",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialise Sentry: %v\n", err)
	}
	defer sentryFlush()

	logging.Setup(logging.ParseLevel(os.Getenv("LOG_LEVEL")), appEnv)
	analysisLog.Info("hover analysis starting")

	if os.Getenv("OBSERVABILITY_ENABLED") == "true" {
		serviceName := strings.TrimSpace(os.Getenv("FLY_APP_NAME"))
		if serviceName == "" {
			serviceName = "hover-analysis"
		}
		metricsAddr := os.Getenv("METRICS_ADDR")
		if metricsAddr == "" {
			metricsAddr = ":9464"
		}
		metricsSrv, err := observability.StartMetricsServer(context.Background(), observability.MetricsServerOptions{
			ServiceName:    serviceName,
			Environment:    appEnv,
			OTLPEndpoint:   strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
			OTLPHeaders:    observability.ParseOTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
			MetricsAddress: metricsAddr,
			EnablePprof:    true,
		})
		if err != nil {
			analysisLog.Warn("failed to initialise observability", "error", err)
		} else {
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				metricsSrv.Shutdown(ctx)
			}()
		}
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer dbCancel()
	pgDB, err := db.WaitForDatabase(dbCtx, 5*time.Minute)
	if err != nil {
		analysisLog.Fatal("failed to connect to PostgreSQL", "error", err)
	}
	defer func() {
		_ = pgDB.Close()
	}()

	redisCfg := broker.ConfigFromEnv()
	redisClient, err := broker.NewClient(redisCfg)
	if err != nil {
		analysisLog.Fatal("failed to create Redis client", "error", err)
	}
	defer redisClient.Close()
	if err := redisClient.Ping(context.Background()); err != nil {
		analysisLog.Fatal("failed to ping Redis", "error", err)
	}
	analysisLog.Info("connected to Redis")

	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := loadConsumerConfig()
	analysisLog.Info("consumer configured",
		"max_concurrency", cfg.maxConcurrency,
		"audit_timeout_ms", cfg.auditTimeout.Milliseconds(),
		"poll_interval_ms", cfg.pollInterval.Milliseconds(),
		"runner", cfg.runner,
		"memory_shed_mb", cfg.memoryShedMB,
	)

	provider, bucket := loadArchiveProvider(rootCtx, cfg.runner)

	runner, err := selectRunner(cfg, provider, bucket)
	if err != nil {
		analysisLog.Fatal("failed to construct lighthouse runner", "error", err)
	}

	consumer := newConsumer(pgDB, redisClient, runner, cfg)
	consumer.run(rootCtx)

	analysisLog.Info("analysis service stopped")
}

// Missing config is fatal only when LIGHTHOUSE_RUNNER=local; the stub runner
// boots without R2 credentials so review apps still come up.
func loadArchiveProvider(ctx context.Context, runner string) (archive.ColdStorageProvider, string) {
	cfg := archive.ConfigFromEnv()
	if cfg == nil {
		if runner == "local" {
			analysisLog.Fatal("LIGHTHOUSE_RUNNER=local requires ARCHIVE_PROVIDER + ARCHIVE_BUCKET")
		}
		analysisLog.Info("archive provider unconfigured; stub runner doesn't need it")
		return nil, ""
	}
	provider, err := archive.ProviderFromEnv()
	if err != nil {
		if runner == "local" {
			analysisLog.Fatal("failed to construct archive provider", "error", err)
		}
		analysisLog.Warn("failed to construct archive provider; stub runner unaffected", "error", err)
		return nil, ""
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := provider.Ping(pingCtx, cfg.Bucket); err != nil {
		if runner == "local" {
			analysisLog.Fatal("archive provider ping failed", "error", err, "bucket", cfg.Bucket)
		}
		analysisLog.Warn("archive provider ping failed; stub runner unaffected", "error", err)
		return nil, ""
	}
	analysisLog.Info("archive provider ready", "provider", provider.Provider(), "bucket", cfg.Bucket)
	return provider, cfg.Bucket
}

const runnerEnvKey = "LIGHTHOUSE_RUNNER"

// Unknown LIGHTHOUSE_RUNNER values log a warning and fall back to the stub
// runner so a typo doesn't take the service down. The error return is unused
// today but preserved on the signature so a future stricter mode can surface
// Dockerfile/toml drift as a loud boot failure.
func selectRunner(cfg consumerConfig, provider archive.ColdStorageProvider, bucket string) (lighthouse.Runner, error) {
	switch cfg.runner {
	case "stub":
		analysisLog.Info("using stub lighthouse runner")
		return lighthouse.NewStubRunner(), nil
	case "local":
		analysisLog.Info("using local lighthouse runner",
			"lighthouse_bin", cfg.lighthouseBin,
			"chromium_bin", cfg.chromiumBin,
		)
		return lighthouse.NewLocalRunner(lighthouse.LocalRunnerConfig{
			LighthouseBin: cfg.lighthouseBin,
			ChromiumBin:   cfg.chromiumBin,
			Provider:      provider,
			Bucket:        bucket,
			MemoryShedMB:  cfg.memoryShedMB,
			ProfilePreset: lighthouse.ProfileMobile,
		})
	default:
		analysisLog.Warn("unknown LIGHTHOUSE_RUNNER value; falling back to stub", "requested", cfg.runner)
		return lighthouse.NewStubRunner(), nil
	}
}

type consumerConfig struct {
	maxConcurrency  int
	auditTimeout    time.Duration
	pollInterval    time.Duration
	reclaimInterval time.Duration
	reclaimMinIdle  time.Duration
	consumerName    string

	runner        string // "stub" | "local"
	lighthouseBin string
	chromiumBin   string
	memoryShedMB  int // free-memory floor before deferring an audit
}

func loadConsumerConfig() consumerConfig {
	runner := strings.ToLower(strings.TrimSpace(os.Getenv(runnerEnvKey)))
	if runner == "" {
		runner = "stub"
	}
	cfg := consumerConfig{
		maxConcurrency:  envIntDefault("LIGHTHOUSE_MAX_CONCURRENCY", 1),
		auditTimeout:    time.Duration(envIntDefault("LIGHTHOUSE_AUDIT_TIMEOUT_MS", 90_000)) * time.Millisecond,
		pollInterval:    time.Duration(envIntDefault("LIGHTHOUSE_POLL_INTERVAL_MS", 1_000)) * time.Millisecond,
		reclaimInterval: time.Duration(envIntDefault("LIGHTHOUSE_RECLAIM_INTERVAL_S", 60)) * time.Second,
		// 180s mirrors the crawl stream default and exceeds the 90s audit
		// timeout plus DB write.
		reclaimMinIdle: time.Duration(envIntDefault("LIGHTHOUSE_RECLAIM_MIN_IDLE_S", 180)) * time.Second,
		runner:         runner,
		lighthouseBin:  strings.TrimSpace(os.Getenv("LIGHTHOUSE_BIN")),
		chromiumBin:    strings.TrimSpace(os.Getenv("CHROMIUM_BIN")),
		memoryShedMB:   envIntDefault("LIGHTHOUSE_MEMORY_SHED_THRESHOLD_MB", 600),
	}
	if cfg.maxConcurrency < 1 {
		cfg.maxConcurrency = 1
	}
	machineName := os.Getenv("FLY_MACHINE_ID")
	if machineName == "" {
		machineName, _ = os.Hostname()
	}
	if machineName == "" {
		machineName = "analysis"
	}
	cfg.consumerName = "lh-" + machineName
	return cfg
}

func envIntDefault(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

type consumer struct {
	db     *db.DB
	rdb    *broker.Client
	runner lighthouse.Runner
	cfg    consumerConfig
	sem    chan struct{}
}

func newConsumer(database *db.DB, rdb *broker.Client, runner lighthouse.Runner, cfg consumerConfig) *consumer {
	return &consumer{
		db:     database,
		rdb:    rdb,
		runner: runner,
		cfg:    cfg,
		sem:    make(chan struct{}, cfg.maxConcurrency),
	}
}

func (c *consumer) run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.pollInterval)
	defer ticker.Stop()

	reclaimTicker := time.NewTicker(c.cfg.reclaimInterval)
	defer reclaimTicker.Stop()

	var wg sync.WaitGroup

	// Sweep on start so a fresh pod picks up the previous one's PEL
	// before any new XReadGroup ">" deliveries land.
	c.reclaimAllJobs(ctx, &wg)

	for {
		select {
		case <-ctx.Done():
			analysisLog.Info("analysis consumer stopping; waiting for in-flight audits")
			wg.Wait()
			return
		case <-ticker.C:
			jobs, err := c.activeJobIDs(ctx)
			if err != nil {
				analysisLog.Warn("failed to list jobs with lighthouse work", "error", err)
				continue
			}
			for _, jobID := range jobs {
				if ctx.Err() != nil {
					break
				}
				c.consumeOne(ctx, &wg, jobID)
			}
		case <-reclaimTicker.C:
			c.reclaimAllJobs(ctx, &wg)
		}
	}
}

func (c *consumer) reclaimAllJobs(ctx context.Context, wg *sync.WaitGroup) {
	jobs, err := c.activeJobIDs(ctx)
	if err != nil {
		analysisLog.Warn("reclaim sweep: failed to list jobs", "error", err)
		return
	}
	for _, jobID := range jobs {
		if ctx.Err() != nil {
			return
		}
		c.reclaimStale(ctx, wg, jobID)
	}
}

// No LIMIT: active-job count is bounded by the per-job 100-audit cap, and
// non-blocking polling (Block: -1) round-robins rather than stalling.
func (c *consumer) activeJobIDs(ctx context.Context) ([]string, error) {
	rows, err := c.db.GetDB().QueryContext(ctx, `
		SELECT DISTINCT job_id
		  FROM lighthouse_runs
		 WHERE status IN ('pending', 'running')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var jobID string
		if err := rows.Scan(&jobID); err != nil {
			return nil, err
		}
		out = append(out, jobID)
	}
	return out, rows.Err()
}

// Without this, a process that died between MarkLighthouseRunRunning and
// XAck would orphan its stream message — XReadGroup ">" only delivers
// never-delivered entries.
func (c *consumer) reclaimStale(ctx context.Context, wg *sync.WaitGroup, jobID string) {
	streamKey := broker.LighthouseStreamKey(jobID)
	groupName := broker.LighthouseConsumerGroup(jobID)

	if err := c.ensureGroup(ctx, streamKey, groupName); err != nil {
		analysisLog.Warn("reclaim ensure consumer group failed",
			"error", err, "job_id", jobID)
		return
	}

	cursor := "0-0"
	for ctx.Err() == nil {
		msgs, next, err := c.rdb.RDB().XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   streamKey,
			Group:    groupName,
			Consumer: c.cfg.consumerName,
			MinIdle:  c.cfg.reclaimMinIdle,
			Start:    cursor,
			Count:    int64(c.cfg.maxConcurrency),
		}).Result()
		if err != nil {
			if isNoGroupErr(err) {
				return
			}
			analysisLog.Warn("XAutoClaim failed", "error", err, "job_id", jobID)
			return
		}
		if len(msgs) == 0 && (next == "0-0" || next == "") {
			return
		}
		c.dispatchMessages(ctx, wg, jobID, streamKey, groupName, msgs)
		if next == "0-0" || next == "" {
			return
		}
		cursor = next
	}
}

// Block: -1 returns immediately on an empty stream so a tick over many jobs
// does not stall on any one of them.
func (c *consumer) consumeOne(ctx context.Context, wg *sync.WaitGroup, jobID string) {
	streamKey := broker.LighthouseStreamKey(jobID)
	groupName := broker.LighthouseConsumerGroup(jobID)

	if err := c.ensureGroup(ctx, streamKey, groupName); err != nil {
		analysisLog.Warn("ensure consumer group failed", "error", err, "job_id", jobID)
		return
	}

	streams, err := c.rdb.RDB().XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: c.cfg.consumerName,
		Streams:  []string{streamKey, ">"},
		Count:    int64(c.cfg.maxConcurrency),
		Block:    -1,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return
	}
	if err != nil {
		if isNoGroupErr(err) {
			return
		}
		analysisLog.Warn("XReadGroup failed", "error", err, "job_id", jobID)
		return
	}

	for _, stream := range streams {
		c.dispatchMessages(ctx, wg, jobID, streamKey, groupName, stream.Messages)
	}
}

// Malformed messages are ACKed to drop them.
func (c *consumer) dispatchMessages(
	ctx context.Context,
	wg *sync.WaitGroup,
	jobID, streamKey, groupName string,
	msgs []redis.XMessage,
) {
	for _, msg := range msgs {
		runID, ok := parseRunID(msg.Values)
		if !ok {
			analysisLog.Warn("malformed lighthouse stream message; ACKing to drop",
				"job_id", jobID, "message_id", msg.ID)
			_ = c.rdb.RDB().XAck(ctx, streamKey, groupName, msg.ID).Err()
			continue
		}
		url := stringFromMap(msg.Values, streamFieldURL)
		if url == "" {
			url = composeURLFromMap(msg.Values)
		}
		pageID, _ := strconv.Atoi(stringFromMap(msg.Values, "page_id"))

		req := lighthouse.AuditRequest{
			RunID:   runID,
			JobID:   jobID,
			PageID:  pageID,
			URL:     url,
			Profile: lighthouse.ProfileMobile,
			Timeout: c.cfg.auditTimeout,
		}

		// Acquire before spawning so backpressure doesn't pile up goroutines.
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func(req lighthouse.AuditRequest, msgID string) {
			defer wg.Done()
			defer func() { <-c.sem }()
			c.processOne(ctx, req, msgID, streamKey, groupName)
		}(req, msg.ID)
	}
}

// XAck fires only once the DB write succeeds so a mid-run crash leaves the
// message for redelivery; the status='running' guard in
// internal/db/lighthouse.go makes the redelivery a no-op once terminal.
func (c *consumer) processOne(ctx context.Context, req lighthouse.AuditRequest, msgID, streamKey, groupName string) {
	startedAt := time.Now()

	moved, sourceTaskID, err := c.db.MarkLighthouseRunRunning(ctx, req.RunID)
	if err != nil {
		analysisLog.Warn("MarkLighthouseRunRunning failed", "error", err,
			"run_id", req.RunID, "job_id", req.JobID)
		return
	}
	if !moved {
		// Row is terminal; ACK the stale redelivery.
		analysisLog.Debug("lighthouse run already terminal; acking stale redelivery",
			"run_id", req.RunID, "job_id", req.JobID, "message_id", msgID)
		_ = c.rdb.RDB().XAck(ctx, streamKey, groupName, msgID).Err()
		return
	}
	req.SourceTaskID = sourceTaskID

	analysisLog.Info("lighthouse audit started",
		"run_id", req.RunID, "job_id", req.JobID,
		"page_id", req.PageID,
		// Strip query/fragment so session/signed-link tokens stay out of logs.
		"url", lighthouse.SanitiseAuditURL(req.URL),
		"profile", string(req.Profile),
	)

	result, runErr := c.runner.Run(ctx, req)

	if runErr != nil {
		duration := time.Since(startedAt)
		// Memory-shed: leave the row 'running' and skip ACK so XAUTOCLAIM
		// redelivers once memory recovers; failing it would burn the slot.
		if errors.Is(runErr, lighthouse.ErrMemoryShed) {
			analysisLog.Info("lighthouse audit shed (low memory); leaving for redelivery",
				"run_id", req.RunID, "job_id", req.JobID,
				"duration_ms", duration.Milliseconds(),
			)
			// No duration histogram — these audits never actually ran.
			observability.RecordLighthouseRun(ctx, req.JobID, "shed")
			return
		}
		// Shutdown cancellation: don't mark failed and don't ACK so the
		// message redelivers to a fresh consumer after deploy.
		if errors.Is(runErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			analysisLog.Info("lighthouse audit interrupted by shutdown; leaving for redelivery",
				"run_id", req.RunID, "job_id", req.JobID,
				"duration_ms", duration.Milliseconds(),
			)
			return
		}
		analysisLog.Warn("lighthouse audit failed",
			"error", runErr, "run_id", req.RunID,
			"job_id", req.JobID, "duration_ms", duration.Milliseconds(),
		)
		if dbErr := c.db.FailLighthouseRun(ctx, req.RunID, runErr.Error(), int(duration.Milliseconds())); dbErr != nil {
			analysisLog.Warn("FailLighthouseRun write failed",
				"error", dbErr, "run_id", req.RunID, "job_id", req.JobID)
			// Skip ACK so redelivery retries the row write.
			return
		}
		observability.RecordLighthouseRun(ctx, req.JobID, "failed")
		observability.RecordLighthouseRunDuration(ctx, req.JobID, "failed", float64(duration.Milliseconds()))
		_ = c.rdb.RDB().XAck(ctx, streamKey, groupName, msgID).Err()
		return
	}

	duration := result.Duration
	if duration <= 0 {
		duration = time.Since(startedAt)
	}

	if dbErr := c.db.CompleteLighthouseRun(ctx, req.RunID, db.LighthouseRunMetrics{
		PerformanceScore: result.PerformanceScore,
		LCPMs:            result.LCPMs,
		CLS:              result.CLS,
		INPMs:            result.INPMs,
		TBTMs:            result.TBTMs,
		FCPMs:            result.FCPMs,
		SpeedIndexMs:     result.SpeedIndexMs,
		TTFBMs:           result.TTFBMs,
		TotalByteWeight:  result.TotalByteWeight,
		ReportKey:        result.ReportKey,
		DurationMs:       int(duration.Milliseconds()),
	}); dbErr != nil {
		analysisLog.Warn("CompleteLighthouseRun write failed",
			"error", dbErr, "run_id", req.RunID, "job_id", req.JobID)
		// Skip ACK so redelivery retries the write.
		return
	}

	observability.RecordLighthouseRun(ctx, req.JobID, "succeeded")
	observability.RecordLighthouseRunDuration(ctx, req.JobID, "succeeded", float64(duration.Milliseconds()))

	analysisLog.Info("lighthouse audit completed",
		"run_id", req.RunID, "job_id", req.JobID,
		"page_id", req.PageID,
		"duration_ms", duration.Milliseconds(),
	)

	_ = c.rdb.RDB().XAck(ctx, streamKey, groupName, msgID).Err()
}

func (c *consumer) ensureGroup(ctx context.Context, streamKey, groupName string) error {
	err := c.rdb.RDB().XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}

func parseRunID(values map[string]interface{}) (int64, bool) {
	raw, ok := values[streamFieldRunID].(string)
	if !ok || raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func stringFromMap(values map[string]interface{}, key string) string {
	if v, ok := values[key].(string); ok {
		return v
	}
	return ""
}

func composeURLFromMap(values map[string]interface{}) string {
	host := stringFromMap(values, streamFieldHost)
	path := stringFromMap(values, streamFieldPath)
	if host == "" {
		return ""
	}
	if path == "" {
		path = "/"
	}
	return "https://" + host + path
}

func isNoGroupErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NOGROUP") || strings.Contains(s, "no such key")
}
