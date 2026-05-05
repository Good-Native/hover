package jobs

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/good-native/hover/internal/crawler"
)

const (
	// PR-330 removed the worker pool; this restores the per-job dispatch dial
	// previously fed by GNH_MAX_WORKERS without reintroducing the pool.
	fallbackJobConcurrency = 20

	discoveredLinksDBTimeout  = 30 * time.Second
	discoveredLinksMinRemain  = 8 * time.Second
	discoveredLinksMinTimeout = 5 * time.Second
)

func jobDefaultConcurrency() int {
	if raw := strings.TrimSpace(os.Getenv("GNH_MAX_WORKERS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallbackJobConcurrency
}

type JobInfo struct {
	DomainID                 int
	DomainName               string
	FindLinks                bool
	AllowCrossSubdomainLinks bool
	CrawlDelay               int
	Concurrency              int
	AdaptiveDelay            int
	AdaptiveDelayFloor       int
	RobotsRules              *crawler.RobotsRules
}

// IsRateLimitError matches a broader set than isBlockingError: pacer state
// updates fire even when the executor would not retry.
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "429") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "403") ||
		strings.Contains(lower, "503")
}

// applyCrawlDelay is retained for tests; production crawl-delay is enforced by the broker's DomainPacer.
func applyCrawlDelay(task *Task) {
	if task.CrawlDelay > 0 {
		time.Sleep(time.Duration(task.CrawlDelay) * time.Second)
	}
}

func classifyTaskOutcome(o *TaskOutcome) (string, string) {
	if o.Success {
		return "success", "ok"
	}

	if o.RateLimited {
		if o.Retry != nil && o.Retry.ShouldRetry {
			return "retry_scheduled", "blocking"
		}
		return "failed", "blocking_exhausted"
	}

	if o.Retry != nil && o.Retry.ShouldRetry {
		return "retry_scheduled", "retryable"
	}

	return "failed", "non_retryable"
}

func linkDiscoveryMinPriorityFromEnv() float64 {
	const fallback = 0.5
	if raw := strings.TrimSpace(os.Getenv("GNH_LINK_DISCOVERY_MIN_PRIORITY")); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 && parsed <= 1 {
			return parsed
		}
	}
	return fallback
}
