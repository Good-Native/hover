package crawler

import "net/http"

// RequestMetadata stores request details for a crawl attempt.
type RequestMetadata struct {
	Method     string `json:"method,omitempty"`
	URL        string `json:"url,omitempty"`
	FinalURL   string `json:"final_url,omitempty"`
	Scheme     string `json:"scheme,omitempty"`
	Host       string `json:"host,omitempty"`
	Path       string `json:"path,omitempty"`
	Query      string `json:"query,omitempty"`
	Timestamp  int64  `json:"timestamp,omitempty"`
	Provenance string `json:"provenance,omitempty"`
}

// ResponseMetadata stores response details for a crawl attempt.
type ResponseMetadata struct {
	StatusCode    int    `json:"status_code,omitempty"`
	ContentType   string `json:"content_type,omitempty"`
	ContentLength int64  `json:"content_length,omitempty"`
	RedirectURL   string `json:"redirect_url,omitempty"`
	Warning       string `json:"warning,omitempty"`
	Error         string `json:"error,omitempty"`
}

// CacheMetadata stores cache-related headers and interpretation.
type CacheMetadata struct {
	HeaderSource     string `json:"header_source,omitempty"`
	RawValue         string `json:"raw_value,omitempty"`
	NormalisedStatus string `json:"normalised_status,omitempty"`
	Age              string `json:"age,omitempty"`
	CacheControl     string `json:"cache_control,omitempty"`
	Vary             string `json:"vary,omitempty"`
	CacheStatus      string `json:"cache_status,omitempty"`
	CFCacheStatus    string `json:"cf_cache_status,omitempty"`
	XCache           string `json:"x_cache,omitempty"`
	XCacheRemote     string `json:"x_cache_remote,omitempty"`
	XVercelCache     string `json:"x_vercel_cache,omitempty"`
	XVarnish         string `json:"x_varnish,omitempty"`
}

// RequestAttemptDiagnostics stores the diagnostics for a full request attempt.
type RequestAttemptDiagnostics struct {
	Request         *RequestMetadata    `json:"request,omitempty"`
	Response        *ResponseMetadata   `json:"response,omitempty"`
	RequestHeaders  http.Header         `json:"request_headers,omitempty"`
	ResponseHeaders http.Header         `json:"response_headers,omitempty"`
	Timing          *PerformanceMetrics `json:"timing,omitempty"`
	Cache           *CacheMetadata      `json:"cache,omitempty"`
}

// CacheCheckAttempt stores the result of a single cache status check.
type CacheCheckAttempt struct {
	Attempt     int    `json:"attempt"`
	CacheStatus string `json:"cache_status"`
	Delay       int    `json:"delay_ms"`
	// Diagnostics duplicates attempt metadata for backward-compatible probe history.
	Diagnostics *ProbeDiagnostics `json:"diagnostics,omitempty"`
}

// ProbeDiagnostics stores diagnostics for a cache probe attempt.
type ProbeDiagnostics struct {
	Attempt  int               `json:"attempt,omitempty"`
	Request  *RequestMetadata  `json:"request,omitempty"`
	Response *ResponseMetadata `json:"response,omitempty"`
	Cache    *CacheMetadata    `json:"cache,omitempty"`
	DelayMS  int               `json:"delay_ms,omitempty"`
}

// PerformanceMetrics holds detailed timing information for a request.
type PerformanceMetrics struct {
	DNSLookupTime       int64 `json:"dns_lookup_time"`
	TCPConnectionTime   int64 `json:"tcp_connection_time"`
	TLSHandshakeTime    int64 `json:"tls_handshake_time"`
	TTFB                int64 `json:"ttfb"`
	ContentTransferTime int64 `json:"content_transfer_time"`
}

// MaxBodySampleSize is the maximum size of body sample stored for tech detection (50KB)
const MaxBodySampleSize = 50 * 1024

// CrawlResult represents the result of a URL crawl operation
type CrawlResult struct {
	URL                 string              `json:"url"`
	ResponseTime        int64               `json:"response_time"`
	StatusCode          int                 `json:"status_code"`
	Error               string              `json:"error,omitempty"`
	Warning             string              `json:"warning,omitempty"`
	CacheStatus         string              `json:"cache_status"`
	ContentType         string              `json:"content_type"`
	ContentLength       int64               `json:"content_length"`
	Headers             http.Header         `json:"headers"`
	RedirectURL         string              `json:"redirect_url"`
	Performance         PerformanceMetrics  `json:"performance"`
	Timestamp           int64               `json:"timestamp"`
	RetryCount          int                 `json:"retry_count"`
	SkippedCrawl        bool                `json:"skipped_crawl,omitempty"`
	Links               map[string][]string `json:"links,omitempty"`
	SecondResponseTime  int64               `json:"second_response_time,omitempty"`
	SecondCacheStatus   string              `json:"second_cache_status,omitempty"`
	SecondContentLength int64               `json:"second_content_length,omitempty"`
	SecondHeaders       http.Header         `json:"second_headers,omitempty"`
	SecondPerformance   *PerformanceMetrics `json:"second_performance,omitempty"`
	CacheCheckAttempts  []CacheCheckAttempt `json:"cache_check_attempts,omitempty"`
	RequestDiagnostics  *RequestDiagnostics `json:"request_diagnostics,omitempty"`
	BodySample          []byte              `json:"-"` // Truncated body for tech detection (not serialised)
	Body                []byte              `json:"-"` // Full body for storage upload (not serialised)
	WAF                 *WAFDetection       `json:"waf,omitempty"`
}

// RequestDiagnostics stores per-stage diagnostics for a crawl.
type RequestDiagnostics struct {
	Primary   *RequestAttemptDiagnostics `json:"primary,omitempty"`
	Probes    []ProbeDiagnostics         `json:"probes,omitempty"`
	Secondary *RequestAttemptDiagnostics `json:"secondary,omitempty"`
	Timings   *RequestStageTimings       `json:"timings,omitempty"`
}

// RequestStageTimings stores aggregate duration for each crawl phase.
type RequestStageTimings struct {
	PrimaryRequestMS   int64 `json:"primary_request_ms,omitempty"`
	CacheValidationMS  int64 `json:"cache_validation_ms,omitempty"`
	SecondaryRequestMS int64 `json:"secondary_request_ms,omitempty"`
	TotalMS            int64 `json:"total_ms,omitempty"`
}

// CrawlOptions defines configuration options for a crawl operation
type CrawlOptions struct {
	MaxPages    int  // Maximum pages to crawl
	Concurrency int  // Number of concurrent crawlers
	RateLimit   int  // Maximum requests per second
	Timeout     int  // Request timeout in seconds
	FollowLinks bool // Whether to follow links on crawled pages
}
