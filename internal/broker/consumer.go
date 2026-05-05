package broker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/good-native/hover/internal/observability"
	"github.com/redis/go-redis/v9"
)

// StreamMessage is a parsed task envelope read from a Redis Stream.
type StreamMessage struct {
	MessageID  string
	TaskID     string
	JobID      string
	PageID     int
	Host       string
	Path       string
	Priority   float64
	RetryCount int
	SourceType string
	SourceURL  string
}

type ConsumerOpts struct {
	// ConsumerName: "worker-{machineID}-{goroutineID}".
	ConsumerName  string
	BlockTimeout  time.Duration
	Count         int64
	ClaimInterval time.Duration
	MinIdleTime   time.Duration
	MaxDeliveries int64
	// AutoclaimCount is the per-call XAUTOCLAIM COUNT.
	AutoclaimCount int64
	// AutoclaimMaxPerSweep caps total reclaimed per sweep so one
	// pathological job can't starve the rest of the reclaim loop.
	AutoclaimMaxPerSweep int
}

func DefaultConsumerOpts(consumerName string) ConsumerOpts {
	return ConsumerOpts{
		ConsumerName: consumerName,
		BlockTimeout: time.Duration(envInt("REDIS_CONSUMER_BLOCK_MS", 2000)) * time.Millisecond,
		// Count=10: with Count=1 a worker rotating across 30 jobs
		// makes 30 RTTs per outer loop. 10 amortises that.
		Count:                int64(envInt("REDIS_CONSUMER_READ_COUNT", 10)),
		ClaimInterval:        time.Duration(envInt("REDIS_AUTOCLAIM_INTERVAL_S", 30)) * time.Second,
		MinIdleTime:          time.Duration(envInt("REDIS_AUTOCLAIM_MIN_IDLE_S", 180)) * time.Second,
		MaxDeliveries:        int64(envInt("REDIS_AUTOCLAIM_MAX_DELIVERIES", 3)),
		AutoclaimCount:       int64(envInt("REDIS_AUTOCLAIM_COUNT", 100)),
		AutoclaimMaxPerSweep: envInt("REDIS_AUTOCLAIM_MAX_PER_SWEEP", 1000),
	}
}

// Consumer reads via XREADGROUP and reclaims stale messages via XAUTOCLAIM.
type Consumer struct {
	client *Client
	opts   ConsumerOpts
}

func NewConsumer(client *Client, opts ConsumerOpts) *Consumer {
	return &Consumer{client: client, opts: opts}
}

// Read blocks up to opts.BlockTimeout. Returns nil (not error) when no
// messages are ready.
func (c *Consumer) Read(ctx context.Context, jobID string) ([]StreamMessage, error) {
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)

	streams, err := c.client.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: c.opts.ConsumerName,
		Streams:  []string{streamKey, ">"},
		Count:    c.opts.Count,
		Block:    c.opts.BlockTimeout,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		// Stream/group not yet created — dispatcher does that lazily on first XADD.
		if isNoGroupErr(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("broker: XREADGROUP %s: %w", jobID, err)
	}

	var msgs []StreamMessage
	for _, stream := range streams {
		for _, xMsg := range stream.Messages {
			msg, err := parseStreamMessage(xMsg)
			if err != nil {
				brokerLog.Warn("skipping malformed stream message", "error", err, "message_id", xMsg.ID, "consumer", c.opts.ConsumerName)
				// ACK to stop infinite redelivery.
				_ = c.Ack(ctx, jobID, xMsg.ID)
				continue
			}
			recordMessageAge(ctx, jobID, xMsg.ID)
			msgs = append(msgs, msg)
		}
	}
	return msgs, nil
}

// ReadNonBlocking returns immediately when no messages are ready.
func (c *Consumer) ReadNonBlocking(ctx context.Context, jobID string) ([]StreamMessage, error) {
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)

	streams, err := c.client.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: c.opts.ConsumerName,
		Streams:  []string{streamKey, ">"},
		Count:    c.opts.Count,
		// Block: -1 makes go-redis omit the BLOCK clause. Block: 0
		// would mean "block indefinitely" in Redis.
		Block: -1,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		if isNoGroupErr(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("broker: XREADGROUP (non-blocking) %s: %w", jobID, err)
	}

	var msgs []StreamMessage
	for _, stream := range streams {
		for _, xMsg := range stream.Messages {
			msg, err := parseStreamMessage(xMsg)
			if err != nil {
				brokerLog.Warn("skipping malformed stream message", "error", err, "message_id", xMsg.ID, "consumer", c.opts.ConsumerName)
				_ = c.Ack(ctx, jobID, xMsg.ID)
				continue
			}
			recordMessageAge(ctx, jobID, xMsg.ID)
			msgs = append(msgs, msg)
		}
	}
	return msgs, nil
}

// recordMessageAge emits bee.broker.consumer_message_age_ms.
// Stream IDs are "ms-seq"; parse failures skip silently.
func recordMessageAge(ctx context.Context, jobID, streamID string) {
	dash := strings.IndexByte(streamID, '-')
	if dash <= 0 {
		return
	}
	ms, err := strconv.ParseInt(streamID[:dash], 10, 64)
	if err != nil {
		return
	}
	age := time.Since(time.UnixMilli(ms))
	if age < 0 {
		age = 0
	}
	observability.RecordBrokerMessageAge(ctx, jobID, float64(age.Milliseconds()))
}

func (c *Consumer) Ack(ctx context.Context, jobID string, messageIDs ...string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)
	return c.client.rdb.XAck(ctx, streamKey, groupName, messageIDs...).Err()
}

// ReclaimStale walks the XAUTOCLAIM cursor until "0-0" so a burst of
// stuck messages drains in one tick. Messages over MaxDeliveries are
// returned as deadLetter candidates — caller owns final disposition
// and must ACK/NACK or they'll be reclaimed again next sweep.
func (c *Consumer) ReclaimStale(ctx context.Context, jobID string) (reclaimed []StreamMessage, deadLetter []StreamMessage, err error) {
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)

	perCallCount := c.opts.AutoclaimCount
	if perCallCount <= 0 {
		perCallCount = 100
	}
	maxMessagesPerSweep := c.opts.AutoclaimMaxPerSweep
	if maxMessagesPerSweep <= 0 {
		maxMessagesPerSweep = 1000
	}

	cursor := "0-0"
	totalSeen := 0
	// iterationCap guards against a non-terminal cursor returning zero
	// messages indefinitely; 3× ceil(max/percall) absorbs the empty-batch
	// gaps XAUTOCLAIM produces mid-walk (coderabbit PR #338).
	iterationCap := 0
	if perCallCount > 0 {
		iterationCap = int((int64(maxMessagesPerSweep)+perCallCount-1)/perCallCount) * 3
	}
	if iterationCap < 10 {
		iterationCap = 10
	}
	iterations := 0

	for {
		remaining := int64(maxMessagesPerSweep) - int64(totalSeen)
		if remaining <= 0 {
			break
		}
		hopCount := perCallCount
		if remaining < hopCount {
			hopCount = remaining
		}
		msgs, next, claimErr := c.client.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   streamKey,
			Group:    groupName,
			Consumer: c.opts.ConsumerName,
			MinIdle:  c.opts.MinIdleTime,
			Start:    cursor,
			Count:    hopCount,
		}).Result()
		if claimErr != nil {
			if isNoGroupErr(claimErr) {
				return nil, nil, nil
			}
			return nil, nil, fmt.Errorf("broker: XAUTOCLAIM %s: %w", jobID, claimErr)
		}

		// One XPENDING range call instead of one per message.
		delivered, parsed := c.classifyBatch(ctx, jobID, streamKey, groupName, msgs)
		for _, entry := range parsed {
			if entry.unparseable {
				continue
			}
			cnt, ok := delivered[entry.msg.MessageID]
			if !ok {
				// Already ACKed by another worker between XAUTOCLAIM and
				// classification — skip (coderabbit PR #338).
				continue
			}
			if cnt >= c.opts.MaxDeliveries {
				deadLetter = append(deadLetter, entry.msg)
			} else {
				reclaimed = append(reclaimed, entry.msg)
			}
		}

		totalSeen += len(msgs)
		iterations++

		// "0-0"/"" signals end-of-walk. Don't break on empty msgs —
		// XAUTOCLAIM can return zero claimed entries mid-walk when
		// candidates drop below MinIdle (coderabbit PR #338).
		if next == "0-0" || next == "" {
			break
		}
		if totalSeen >= maxMessagesPerSweep {
			brokerLog.Warn("reclaim sweep hit per-tick cap; will resume next tick",
				"job_id", jobID, "cap", maxMessagesPerSweep)
			break
		}
		if iterations >= iterationCap {
			brokerLog.Warn("reclaim sweep hit iteration cap; will resume next tick",
				"job_id", jobID, "iterations", iterations, "seen", totalSeen)
			break
		}
		cursor = next
	}

	observability.RecordBrokerAutoclaim(ctx, jobID, "reclaimed", len(reclaimed))
	observability.RecordBrokerAutoclaim(ctx, jobID, "dead_letter", len(deadLetter))

	return reclaimed, deadLetter, nil
}

type classifiedMessage struct {
	msg         StreamMessage
	unparseable bool
}

// classifyBatch fetches delivery counts in one XPENDING range call.
// Pre-batching, one-XPENDING-per-message at Count=100 dominated tick
// time and starved other jobs (coderabbit PR #338).
func (c *Consumer) classifyBatch(
	ctx context.Context,
	jobID, streamKey, groupName string,
	msgs []redis.XMessage,
) (map[string]int64, []classifiedMessage) {
	parsed := make([]classifiedMessage, 0, len(msgs))
	for _, xMsg := range msgs {
		m, parseErr := parseStreamMessage(xMsg)
		if parseErr != nil {
			brokerLog.Warn("dead-lettering unparseable reclaimed message",
				"error", parseErr, "message_id", xMsg.ID, "consumer", c.opts.ConsumerName)
			_ = c.Ack(ctx, jobID, xMsg.ID)
			parsed = append(parsed, classifiedMessage{unparseable: true})
			continue
		}
		parsed = append(parsed, classifiedMessage{msg: m})
	}

	var firstID, lastID string
	any := false
	for _, p := range parsed {
		if p.unparseable {
			continue
		}
		if !any {
			firstID, lastID = p.msg.MessageID, p.msg.MessageID
			any = true
			continue
		}
		// XAUTOCLAIM preserves stream-ID order.
		lastID = p.msg.MessageID
	}
	delivered := make(map[string]int64, len(parsed))
	if !any {
		return delivered, parsed
	}

	// 2× budget: XPENDING lists ALL pending entries in the range
	// (any consumer, any idle), but XAUTOCLAIM only reassigned the
	// idle-eligible ones — interleaved fresh entries can push our IDs
	// out of a tight page.
	pending, err := c.client.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamKey,
		Group:  groupName,
		Start:  firstID,
		End:    lastID,
		Count:  int64(len(parsed)) * 2,
	}).Result()
	if err != nil {
		brokerLog.Warn("batched delivery-count lookup failed; treating batch as reclaimable",
			"error", err, "job_id", jobID, "batch_size", len(parsed))
		return delivered, parsed
	}
	for _, p := range pending {
		delivered[p.ID] = p.RetryCount
	}

	// Per-ID fallback for stragglers — defaulting to delivery=0 would
	// bypass the max-delivery gate and let a stuck message loop forever
	// (coderabbit PR #338).
	for _, p := range parsed {
		if p.unparseable {
			continue
		}
		if _, ok := delivered[p.msg.MessageID]; ok {
			continue
		}
		single, perErr := c.client.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: streamKey,
			Group:  groupName,
			Start:  p.msg.MessageID,
			End:    p.msg.MessageID,
			Count:  1,
		}).Result()
		if perErr != nil {
			brokerLog.Warn("per-id delivery-count fallback failed; treating message as reclaimable",
				"error", perErr, "job_id", jobID, "message_id", p.msg.MessageID)
			continue
		}
		if len(single) == 0 {
			// Already ACKed elsewhere — caller treats absence as skip.
			continue
		}
		delivered[single[0].ID] = single[0].RetryCount
	}
	return delivered, parsed
}

// PendingCount returns the PEL size — the authoritative source of
// "currently running" for a job. RunningCounters mirrors this and can
// drift under partial failures.
func (c *Consumer) PendingCount(ctx context.Context, jobID string) (int64, error) {
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)

	summary, err := c.client.rdb.XPending(ctx, streamKey, groupName).Result()
	if err != nil {
		if isNoGroupErr(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("broker: XPENDING %s: %w", jobID, err)
	}
	if summary == nil {
		return 0, nil
	}
	return summary.Count, nil
}

func isNoGroupErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NOGROUP") || strings.Contains(s, "no such key")
}

// --- parsing ---

func parseStreamMessage(xMsg redis.XMessage) (StreamMessage, error) {
	get := func(key string) string {
		v, _ := xMsg.Values[key].(string)
		return v
	}

	for _, key := range []string{"task_id", "job_id", "host", "path"} {
		if get(key) == "" {
			return StreamMessage{}, fmt.Errorf("missing required field: %s", key)
		}
	}

	pageID, err := strconv.Atoi(get("page_id"))
	if err != nil {
		return StreamMessage{}, fmt.Errorf("bad page_id: %w", err)
	}
	priority, err := strconv.ParseFloat(get("priority"), 64)
	if err != nil {
		return StreamMessage{}, fmt.Errorf("bad priority: %w", err)
	}
	retryCount, err := strconv.Atoi(get("retry_count"))
	if err != nil {
		return StreamMessage{}, fmt.Errorf("bad retry_count: %w", err)
	}

	return StreamMessage{
		MessageID:  xMsg.ID,
		TaskID:     get("task_id"),
		JobID:      get("job_id"),
		PageID:     pageID,
		Host:       get("host"),
		Path:       get("path"),
		Priority:   priority,
		RetryCount: retryCount,
		SourceType: get("source_type"),
		SourceURL:  get("source_url"),
	}, nil
}
