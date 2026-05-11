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
	// EstResponseMS bounds the per-domain inflight cap: when
	// adaptive_delay_ms is non-zero, useful concurrency is
	// ceil(EstResponseMS / adaptive_delay_ms). Holding a fixed 20-wide
	// burst against a CF-fronted domain elevates Fly's egress score; the
	// cap collapses the burst to whatever the learned rate can sustain.
	EstResponseMS int
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
		EstResponseMS:    envInt("GNH_PACER_EST_RESPONSE_MS", 1500),
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

// Release decrements inflight and updates adaptive_delay_ms. Returns the
// post-update adaptive delay in ms (or -1 if Release did not touch it, e.g.
// neither success nor rateLimited). DecrementInflight runs unconditionally:
// if the adaptive update fails we still need to release the inflight slot
// because the worker has already ACKed the message, otherwise the domain
// inflight count drifts upward forever and the per-domain cap starts
// refusing dispatches for work that is no longer running.
func (p *DomainPacer) Release(ctx context.Context, domain, jobID string, success, rateLimited bool) (int, error) {
	cfgKey := DomainConfigKey(domain)
	newDelayMS := -1
	var adaptiveErr error

	if success {
		stepDown := p.cfg.DelayStepDownMS
		if stepDown <= 0 {
			stepDown = p.cfg.DelayStepMS
		}
		raw, err := adaptiveDelayOnSuccessScript.Run(ctx, p.client.rdb,
			[]string{cfgKey},
			p.cfg.SuccessThreshold,
			stepDown,
		).Result()
		if err != nil {
			adaptiveErr = fmt.Errorf("broker: release success %s: %w", domain, err)
		} else if v, ok := raw.(int64); ok {
			newDelayMS = int(v)
		}
	} else if rateLimited {
		raw, err := adaptiveDelayOnErrorScript.Run(ctx, p.client.rdb,
			[]string{cfgKey},
			p.cfg.DelayStepMS,
			p.cfg.MaxDelayMS,
		).Result()
		if err != nil {
			adaptiveErr = fmt.Errorf("broker: release rate-limited %s: %w", domain, err)
		} else if v, ok := raw.(int64); ok {
			newDelayMS = int(v)
		}
		observability.RecordBrokerPacerPushback(ctx, domain, "rate_limited")
	}

	if decErr := p.DecrementInflight(ctx, domain, jobID); decErr != nil {
		if adaptiveErr != nil {
			return newDelayMS, fmt.Errorf("%w; decrement inflight: %v", adaptiveErr, decErr)
		}
		return newDelayMS, decErr
	}
	return newDelayMS, adaptiveErr
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

// EffectiveCap returns the maximum useful per-domain inflight count given
// the learned adaptive delay and the configured response-time estimate.
// Returns 0 when no cap applies (adaptive_delay_ms <= 0): the dispatcher
// then falls back to the per-job concurrency check alone.
func (p *DomainPacer) EffectiveCap(ctx context.Context, domain string) (int, error) {
	raw, err := p.client.rdb.HGet(ctx, DomainConfigKey(domain), "adaptive_delay_ms").Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("broker: effective cap %s: %w", domain, err)
	}
	adaptiveMS, err := strconv.Atoi(raw)
	if err != nil || adaptiveMS <= 0 {
		return 0, nil
	}
	est := p.cfg.EstResponseMS
	if est <= 0 {
		return 0, nil
	}
	ceilCap := (est + adaptiveMS - 1) / adaptiveMS
	if ceilCap < 1 {
		ceilCap = 1
	}
	return ceilCap, nil
}

func (p *DomainPacer) GetInflight(ctx context.Context, domain, jobID string) (int64, error) {
	val, err := p.client.rdb.HGet(ctx, DomainInflightKey(domain), jobID).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

// GetDomainInflight returns the sum of inflight counts across all jobs
// targeting this domain. The per-domain cap must compare against this
// total rather than any single job's slot, otherwise N concurrent jobs
// against the same host each get to dispatch their own copy of the cap
// and the burst-prevention path is defeated.
func (p *DomainPacer) GetDomainInflight(ctx context.Context, domain string) (int64, error) {
	vals, err := p.client.rdb.HVals(ctx, DomainInflightKey(domain)).Result()
	if err != nil {
		return 0, fmt.Errorf("broker: domain inflight %s: %w", domain, err)
	}
	var total int64
	for _, v := range vals {
		n, parseErr := strconv.ParseInt(v, 10, 64)
		if parseErr != nil {
			continue
		}
		if n > 0 {
			total += n
		}
	}
	return total, nil
}
