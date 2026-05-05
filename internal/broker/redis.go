package broker

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/good-native/hover/internal/logging"
	"github.com/redis/go-redis/v9"
)

var brokerLog = logging.Component("broker")

type Config struct {
	URL          string
	PoolSize     int
	TLSEnabled   bool
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	MaxRetries   int
}

// ConfigFromEnv infers TLS from the URL scheme (rediss://) unless
// REDIS_TLS_ENABLED overrides.
func ConfigFromEnv() Config {
	poolSize := envInt("REDIS_POOL_SIZE", 200)
	url := os.Getenv("REDIS_URL")
	tlsDefault := strings.HasPrefix(url, "rediss://")
	tlsEnabled := envBool("REDIS_TLS_ENABLED", tlsDefault)

	return Config{
		URL:          url,
		PoolSize:     poolSize,
		TLSEnabled:   tlsEnabled,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		MaxRetries:   3,
	}
}

type Client struct {
	rdb *redis.Client
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("broker: REDIS_URL is required")
	}

	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("broker: parse REDIS_URL: %w", err)
	}

	opts.PoolSize = cfg.PoolSize
	opts.ReadTimeout = cfg.ReadTimeout
	opts.WriteTimeout = cfg.WriteTimeout
	opts.MaxRetries = cfg.MaxRetries

	if cfg.TLSEnabled && opts.TLSConfig == nil {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if !cfg.TLSEnabled {
		opts.TLSConfig = nil
	}

	rdb := redis.NewClient(opts)

	return &Client{rdb: rdb}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

func (c *Client) Close() error {
	return c.rdb.Close()
}

func (c *Client) RDB() *redis.Client { return c.rdb }

// RemoveJobKeys clears all per-job broker state for a terminal job.
// XGroupDestroy errors are tolerated so a partially-cleaned or
// lighthouse-less job doesn't abort the rest of the cleanup.
func (c *Client) RemoveJobKeys(ctx context.Context, jobID string) error {
	if jobID == "" {
		return fmt.Errorf("broker: RemoveJobKeys requires a jobID")
	}

	streamKey := StreamKey(jobID)
	lhStreamKey := LighthouseStreamKey(jobID)

	if err := c.rdb.XGroupDestroy(ctx, streamKey, ConsumerGroup(jobID)).Err(); err != nil && !isMissingGroup(err) {
		brokerLog.Warn("XGroupDestroy crawl group failed", "error", err, "job_id", jobID)
	}
	if err := c.rdb.XGroupDestroy(ctx, lhStreamKey, LighthouseConsumerGroup(jobID)).Err(); err != nil && !isMissingGroup(err) {
		brokerLog.Warn("XGroupDestroy lighthouse group failed", "error", err, "job_id", jobID)
	}

	pipe := c.rdb.Pipeline()
	pipe.Del(ctx, ScheduleKey(jobID), streamKey, lhStreamKey)
	pipe.HDel(ctx, RunningCountersKey, jobID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("broker: remove job keys for %s: %w", jobID, err)
	}

	// Drop dom:flight fields for this job. dom:flight has no dedicated
	// reconciler so leaks accumulate across cancel/restart cycles — job
	// 91026da8 carried a 12h-old entry that confirmed it. Errors are
	// non-fatal; leaked fields match prior behaviour.
	if err := c.cleanupInflightForJob(ctx, jobID); err != nil {
		brokerLog.Warn("dom:flight cleanup failed", "error", err, "job_id", jobID)
	}

	return nil
}

// cleanupInflightForJob is two-phase (collect-then-delete) because
// HDel-emptying a HASH causes Redis to drop it, and SCAN may then
// skip later keys.
func (c *Client) cleanupInflightForJob(ctx context.Context, jobID string) error {
	pattern := keyPrefix + "dom:flight:*"
	keys, err := c.scanAll(ctx, pattern)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.HDel(ctx, key, jobID)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("broker: HDel inflight pipeline: %w", err)
	}
	return nil
}

// scanAll returns every key matching pattern. Two-phase callers use
// this to avoid the SCAN-during-mutation skip hazard.
func (c *Client) scanAll(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	cursor := uint64(0)
	for {
		page, next, err := c.rdb.Scan(ctx, cursor, pattern, 500).Result()
		if err != nil {
			return nil, fmt.Errorf("broker: scan %s: %w", pattern, err)
		}
		keys = append(keys, page...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return keys, nil
}

// SweepOrphanInflight removes dom:flight fields for jobs absent from
// activeJobIDs. Drift source: SIGKILL bypasses the graceful drain so
// dispatcher increments without a matching pacer.Release decrement.
// dom:flight has no dedicated reconciler.
func (c *Client) SweepOrphanInflight(ctx context.Context, activeJobIDs []string) (int, error) {
	active := make(map[string]struct{}, len(activeJobIDs))
	for _, id := range activeJobIDs {
		active[id] = struct{}{}
	}

	keys, err := c.scanAll(ctx, keyPrefix+"dom:flight:*")
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, key := range keys {
		fields, err := c.rdb.HKeys(ctx, key).Result()
		if err != nil {
			brokerLog.Warn("HKeys failed during orphan sweep", "error", err, "key", key)
			continue
		}
		var orphans []string
		for _, jobID := range fields {
			if _, ok := active[jobID]; !ok {
				orphans = append(orphans, jobID)
			}
		}
		if len(orphans) == 0 {
			continue
		}
		if err := c.rdb.HDel(ctx, key, orphans...).Err(); err != nil {
			brokerLog.Warn("HDel orphans failed", "error", err, "key", key, "orphans", len(orphans))
			continue
		}
		removed += len(orphans)
	}
	return removed, nil
}

func isMissingGroup(err error) bool {
	if err == nil || err == redis.Nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "NOGROUP") ||
		strings.Contains(msg, "no such key") ||
		strings.Contains(msg, "requires the key to exist")
}

// ClearAll deletes every hover:* key the broker writes. Does NOT
// FLUSHDB — safe on shared Redis.
func (c *Client) ClearAll(ctx context.Context) (int, error) {
	patterns := []string{
		keyPrefix + "sched:*",
		keyPrefix + "stream:*",
		keyPrefix + "dom:gate:*",
		keyPrefix + "dom:cfg:*",
		keyPrefix + "dom:flight:*",
	}

	total := 0
	for _, pattern := range patterns {
		n, err := c.scanAndDelete(ctx, pattern)
		if err != nil {
			return total, fmt.Errorf("broker: clear %s: %w", pattern, err)
		}
		total += n
	}

	deleted, err := c.rdb.Del(ctx, RunningCountersKey).Result()
	if err != nil {
		return total, fmt.Errorf("broker: clear %s: %w", RunningCountersKey, err)
	}
	total += int(deleted)

	return total, nil
}

func (c *Client) scanAndDelete(ctx context.Context, pattern string) (int, error) {
	const batch = 500
	var (
		cursor  uint64
		keys    []string
		deleted int
	)

	for {
		var (
			page []string
			err  error
		)
		page, cursor, err = c.rdb.Scan(ctx, cursor, pattern, batch).Result()
		if err != nil {
			return deleted, err
		}
		keys = append(keys, page...)

		for len(keys) >= batch {
			n, err := c.rdb.Del(ctx, keys[:batch]...).Result()
			if err != nil {
				return deleted, err
			}
			deleted += int(n)
			keys = keys[batch:]
		}

		if cursor == 0 {
			break
		}
	}

	if len(keys) > 0 {
		n, err := c.rdb.Del(ctx, keys...).Result()
		if err != nil {
			return deleted, err
		}
		deleted += int(n)
	}

	return deleted, nil
}

// --- env helpers ---

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		brokerLog.Warn("invalid integer for env var, using default",
			"key", key, "value", v, "default", def)
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
