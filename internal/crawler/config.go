package crawler

import (
	"os"
	"strconv"
	"time"
)

// Config holds the configuration for a crawler instance
type Config struct {
	DefaultTimeout time.Duration // Default timeout for requests
	MaxConcurrency int           // Maximum number of concurrent requests
	RateLimit      int           // Determines request delay range: base=1s/RateLimit, range=base to 1s
	UserAgent      string        // User agent string for requests
	RetryAttempts  int           // Number of retry attempts for failed requests
	RetryDelay     time.Duration // Delay between retry attempts
	SkipCachedURLs bool          // Whether to skip URLs that are already cached (HIT)
	Port           string        // Server port
	Env            string        // Environment (development/production)
	LogLevel       string        // Logging level
	DatabaseURL    string        // Database connection URL
	AuthToken      string        // Database authentication token
	SentryDSN      string        // Sentry DSN for error tracking
	FindLinks      bool          // Whether to extract links (e.g. PDFs/docs) from pages
	SkipSSRFCheck  bool          // Skip SSRF protection (for tests only, never enable in production)
}

// DefaultConfig returns a Config instance with default values
func DefaultConfig() *Config {
	return &Config{
		DefaultTimeout: 30 * time.Second,
		MaxConcurrency: getEnvInt("GNH_CRAWLER_MAX_CONCURRENCY", 10),
		RateLimit:      5, // Maximum no. of times per second (minimum delay 1/ratelimit)
		UserAgent:      "Hover/1.0 (+https://www.goodnative.co/hover)",
		RetryAttempts:  3,
		RetryDelay:     500 * time.Millisecond,
		SkipCachedURLs: false, // Default to crawling all URLs
		FindLinks:      false,
	}
}

func getEnvInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}

	return parsed
}
