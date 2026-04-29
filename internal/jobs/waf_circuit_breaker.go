package jobs

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/crawler"
)

// blockJobDispatchTimeout bounds the detached terminal-state write
// fired when the breaker trips. The stream worker hot path must not
// stall on lock contention; if BlockJob can't land in this budget the
// caller logs and re-arms so a subsequent WAF response retries.
const blockJobDispatchTimeout = 30 * time.Second

// defaultWAFCircuitBreakerThreshold is the consecutive-WAF-response
// count that trips the breaker mid-job. Lowered from 3 to 2 after
// kmart.com.au-class observations: by the time three tasks have
// returned WAF fingerprints, the sitemap discovery loop has typically
// inserted thousands of URLs that all need to be skipped. Two
// consecutive WAF-flagged responses is still high-confidence (random
// transient 403s rarely cluster) and trips ~33% earlier, capping the
// orphan-task accumulation. Override via GNH_WAF_CIRCUIT_BREAKER_THRESHOLD.
const defaultWAFCircuitBreakerThreshold = 2

// WAFCircuitBreaker tracks per-job runs of consecutive WAF-flagged
// responses and trips a callback once the threshold is reached. The
// state lives in process memory — fine for the current single-pod
// worker deployment (fly.worker.toml). If the worker ever scales out
// horizontally the counter would undercount across replicas; in that
// case migrate it to Redis (INCR job:<id>:waf_consecutive with TTL)
// alongside the existing running-counters HASH.
type WAFCircuitBreaker struct {
	threshold int

	mu      sync.Mutex
	counts  map[string]int                  // jobID -> consecutive WAF responses
	tripped map[string]struct{}             // jobIDs already tripped (single-fire)
	vendors map[string]crawler.WAFDetection // last seen detection per job
}

func NewWAFCircuitBreaker() *WAFCircuitBreaker {
	return &WAFCircuitBreaker{
		threshold: wafCircuitBreakerThreshold(),
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}
}

func wafCircuitBreakerThreshold() int {
	if v := strings.TrimSpace(os.Getenv("GNH_WAF_CIRCUIT_BREAKER_THRESHOLD")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		jobsLog.Warn("invalid GNH_WAF_CIRCUIT_BREAKER_THRESHOLD; using default",
			"value", v, "default", defaultWAFCircuitBreakerThreshold)
	}
	return defaultWAFCircuitBreakerThreshold
}

// Observe records the WAF status of a single task outcome. When det is
// nil or det.Blocked is false the per-job counter resets, ensuring the
// breaker only fires on truly consecutive blocks rather than a sparse
// sprinkle. Returns true exactly once per job — the moment the
// threshold is first crossed — so callers can fire BlockJob without
// guarding against duplicates.
func (b *WAFCircuitBreaker) Observe(jobID string, det *crawler.WAFDetection) (tripped bool, vendor crawler.WAFDetection) {
	if b == nil || jobID == "" {
		return false, crawler.WAFDetection{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, alreadyTripped := b.tripped[jobID]; alreadyTripped {
		return false, crawler.WAFDetection{}
	}

	if det == nil || !det.Blocked {
		delete(b.counts, jobID)
		delete(b.vendors, jobID)
		return false, crawler.WAFDetection{}
	}

	b.counts[jobID]++
	b.vendors[jobID] = *det

	if b.counts[jobID] >= b.threshold {
		b.tripped[jobID] = struct{}{}
		v := b.vendors[jobID]
		delete(b.counts, jobID)
		delete(b.vendors, jobID)
		return true, v
	}
	return false, crawler.WAFDetection{}
}

// Forget drops all per-job state. Called from OnJobTerminated so a
// long-running worker doesn't accumulate per-job map entries.
func (b *WAFCircuitBreaker) Forget(jobID string) {
	if b == nil || jobID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.counts, jobID)
	delete(b.vendors, jobID)
	delete(b.tripped, jobID)
}

// Rearm clears the single-fire tripped flag for a job AND seeds the
// consecutive-WAF counter to threshold-1 (with the previous trip's
// vendor preserved) so a single subsequent blocked outcome immediately
// retrips. Called when the dispatched BlockJob couldn't land — at
// that point we've already proven the domain is consistently walling
// us; making the retry re-establish the full streak would waste N-1
// blocked observations. The counter still resets on any non-blocked
// response (Observe), so a site that recovers between attempts still
// gets a clean slate.
func (b *WAFCircuitBreaker) Rearm(jobID string, lastVendor crawler.WAFDetection) {
	if b == nil || jobID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.tripped, jobID)
	seed := b.threshold - 1
	if seed < 0 {
		seed = 0
	}
	b.counts[jobID] = seed
	b.vendors[jobID] = lastVendor
}

// Threshold exposes the configured trip count for telemetry/logging.
func (b *WAFCircuitBreaker) Threshold() int {
	if b == nil {
		return 0
	}
	return b.threshold
}

// MaybeTripFromOutcome is the convenience wrapper used from the stream
// worker hot path. It pulls the WAF detection from the outcome, calls
// Observe, and on a trip dispatches BlockJob in a detached goroutine
// with a bounded timeout. If the dispatch fails the breaker is
// re-armed for that job so a subsequent WAF response can retry.
//
// The dispatch is asynchronous so the stream worker isn't held up by
// terminal-state DB lock contention — the per-task ACK / counter
// decrement / batch enqueue must not stall behind a multi-statement
// terminal transaction.
func (b *WAFCircuitBreaker) MaybeTripFromOutcome(ctx context.Context, jm JobManagerInterface, outcome *TaskOutcome) {
	if b == nil || jm == nil || outcome == nil || outcome.Task == nil {
		return
	}
	var det *crawler.WAFDetection
	if outcome.CrawlResult != nil {
		det = outcome.CrawlResult.WAF
	}
	tripped, vendor := b.Observe(outcome.Task.JobID, det)
	if !tripped {
		return
	}

	jobID := outcome.Task.JobID
	jobsLog.Info("WAF circuit breaker tripped; dispatching BlockJob",
		"job_id", jobID,
		"threshold", b.Threshold(),
		"vendor", vendor.Vendor,
		"reason", vendor.Reason)

	// Detach from the per-task ctx so the caller's cancellation
	// doesn't truncate the terminal-state writes, then bound the
	// dispatch so a wedged DB doesn't pin a goroutine forever.
	parentCtx := context.WithoutCancel(ctx)
	go func() {
		dispatchCtx, cancel := context.WithTimeout(parentCtx, blockJobDispatchTimeout)
		defer cancel()
		if err := jm.BlockJob(dispatchCtx, jobID, vendor.Vendor, "circuit breaker: "+vendor.Reason); err != nil {
			jobsLog.Error("BlockJob from circuit breaker failed; re-arming for retry",
				"error", err, "job_id", jobID)
			// Re-arm so the next WAF response for this job can trip
			// again — without this a transient DB blip would silently
			// permanently disable the breaker for the job. The vendor
			// from this trip is preserved so the retrip's BlockJob
			// call carries accurate attribution.
			b.Rearm(jobID, vendor)
		}
	}()
}
