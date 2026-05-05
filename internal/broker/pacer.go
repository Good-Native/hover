package broker

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/good-native/hover/internal/observability"
	"github.com/redis/go-redis/v9"
)

type PacerConfig struct {
	SuccessThreshold int
	DelayStepMS      int
	// Defaults to DelayStepMS. Higher = faster recovery than growth, so
	// a 429 spike doesn't throttle a domain for 20 minutes.
	DelayStepDownMS int
	MaxDelayMS      int
	// Floor on RetryAfter so a near-zero gate TTL doesn't tight-loop
	// the dispatcher (Dispatcher tick is 100ms).
	MinPushbackMS int
}

func DefaultPacerConfig() PacerConfig {
	stepUp := envInt("GNH_RATE_LIMIT_DELAY_STEP_MS", 500)
	stepDown := envInt("GNH_RATE_LIMIT_DELAY_STEP_DOWN_MS", stepUp)
	return PacerConfig{
		SuccessThreshold: envInt("GNH_RATE_LIMIT_SUCCESS_THRESHOLD", 5),
		DelayStepMS:      stepUp,
		DelayStepDownMS:  stepDown,
		MaxDelayMS:       envInt("GNH_RATE_LIMIT_MAX_DELAY_MS", 60000),
		MinPushbackMS:    envInt("GNH_DOMAIN_DELAY_PAUSE_MS", 100),
	}
}

type PaceResult struct {
	Acquired bool
	// Only meaningful when Acquired is false.
	RetryAfter time.Duration
}

type DomainPacer struct {
	client *Client
	cfg    PacerConfig
}

func NewDomainPacer(client *Client, cfg PacerConfig) *DomainPacer {
	return &DomainPacer{
		client: client,
		cfg:    cfg,
	}
}

// HSETNX preserves existing values, so callers may re-seed safely.
func (p *DomainPacer) Seed(ctx context.Context, domain string, baseDelayMS, adaptiveDelayMS, floorMS int) error {
	key := DomainConfigKey(domain)
	pipe := p.client.rdb.Pipeline()

	pipe.HSetNX(ctx, key, "base_delay_ms", strconv.Itoa(baseDelayMS))
	pipe.HSetNX(ctx, key, "adaptive_delay_ms", strconv.Itoa(adaptiveDelayMS))
	pipe.HSetNX(ctx, key, "floor_ms", strconv.Itoa(floorMS))
	pipe.HSetNX(ctx, key, "success_streak", "0")
	pipe.HSetNX(ctx, key, "error_streak", "0")
	pipe.Expire(ctx, key, 24*time.Hour)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("broker: seed domain %s: %w", domain, err)
	}
	return nil
}

// Single Lua EVALSHA — the prior three-call form (HMGET → SET NX PX →
// PTTL) was the dominant dispatcher round-trip cost under multi-job loads.
func (p *DomainPacer) TryAcquire(ctx context.Context, domain string) (PaceResult, error) {
	cfgKey := DomainConfigKey(domain)
	gateKey := DomainGateKey(domain)

	raw, err := tryAcquireScript.Run(ctx, p.client.rdb, []string{cfgKey, gateKey}).Result()
	if err != nil {
		return PaceResult{}, fmt.Errorf("broker: try acquire %s: %w", domain, err)
	}

	acquired, delayMS, ttlMS, err := parseTryAcquireResult(raw)
	if err != nil {
		return PaceResult{}, fmt.Errorf("broker: try acquire %s: %w", domain, err)
	}

	observability.RecordBrokerPacerDelay(ctx, domain, float64(delayMS))

	if acquired {
		return PaceResult{Acquired: true}, nil
	}

	ttl := time.Duration(ttlMS) * time.Millisecond
	if ttl <= 0 {
		// Gate TTL expired between SET NX and PTTL inside the script
		// (clock drift) — fall back to the full delay so we still pause.
		ttl = time.Duration(delayMS) * time.Millisecond
	}
	return PaceResult{Acquired: false, RetryAfter: p.pushbackFloor(ttl)}, nil
}

func parseTryAcquireResult(raw interface{}) (acquired bool, delayMS, ttlMS int64, err error) {
	arr, ok := raw.([]interface{})
	if !ok || len(arr) != 3 {
		return false, 0, 0, fmt.Errorf("unexpected script result shape: %T %v", raw, raw)
	}
	ok0, _ := arr[0].(int64)
	d, _ := arr[1].(int64)
	t, _ := arr[2].(int64)
	return ok0 == 1, d, t, nil
}

func (p *DomainPacer) pushbackFloor(d time.Duration) time.Duration {
	floor := time.Duration(p.cfg.MinPushbackMS) * time.Millisecond
	if d < floor {
		return floor
	}
	return d
}

func (p *DomainPacer) Release(ctx context.Context, domain, jobID string, success, rateLimited bool) error {
	cfgKey := DomainConfigKey(domain)

	if success {
		stepDown := p.cfg.DelayStepDownMS
		if stepDown <= 0 {
			stepDown = p.cfg.DelayStepMS
		}
		_, err := adaptiveDelayOnSuccessScript.Run(ctx, p.client.rdb,
			[]string{cfgKey},
			p.cfg.SuccessThreshold,
			stepDown,
		).Result()
		if err != nil {
			return fmt.Errorf("broker: release success %s: %w", domain, err)
		}
	} else if rateLimited {
		_, err := adaptiveDelayOnErrorScript.Run(ctx, p.client.rdb,
			[]string{cfgKey},
			p.cfg.DelayStepMS,
			p.cfg.MaxDelayMS,
		).Result()
		if err != nil {
			return fmt.Errorf("broker: release rate-limited %s: %w", domain, err)
		}
		observability.RecordBrokerPacerPushback(ctx, domain, "rate_limited")
	}

	return p.DecrementInflight(ctx, domain, jobID)
}

// Restores pre-merge behaviour: in-memory limiter reset on each worker
// restart, but the Redis-backed state has a 24h TTL so a single 429
// spike can pin a domain at the 60s floor for a full day. Call on
// worker startup to wipe the slate.
func (p *DomainPacer) FlushAdaptiveDelays(ctx context.Context) (int, error) {
	pattern := keyPrefix + "dom:cfg:*"
	iter := p.client.rdb.Scan(ctx, 0, pattern, 500).Iterator()
	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return 0, fmt.Errorf("broker: flush scan %s: %w", pattern, err)
	}
	if len(keys) == 0 {
		return 0, nil
	}

	deleted := 0
	const chunk = 500
	for i := 0; i < len(keys); i += chunk {
		end := i + chunk
		if end > len(keys) {
			end = len(keys)
		}
		n, err := p.client.rdb.Del(ctx, keys[i:end]...).Result()
		if err != nil {
			return deleted, fmt.Errorf("broker: flush delete: %w", err)
		}
		deleted += int(n)
	}
	return deleted, nil
}

func (p *DomainPacer) IncrementInflight(ctx context.Context, domain, jobID string) error {
	key := DomainInflightKey(domain)
	return p.client.rdb.HIncrBy(ctx, key, jobID, 1).Err()
}

func (p *DomainPacer) DecrementInflight(ctx context.Context, domain, jobID string) error {
	key := DomainInflightKey(domain)
	val, err := p.client.rdb.HIncrBy(ctx, key, jobID, -1).Result()
	if err != nil {
		return err
	}
	if val <= 0 {
		if err := p.client.rdb.HDel(ctx, key, jobID).Err(); err != nil {
			brokerLog.Warn("failed to clean zero inflight entry", "error", err, "domain", domain, "job_id", jobID)
		}
	}
	return nil
}

func (p *DomainPacer) GetInflight(ctx context.Context, domain, jobID string) (int64, error) {
	val, err := p.client.rdb.HGet(ctx, DomainInflightKey(domain), jobID).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}
