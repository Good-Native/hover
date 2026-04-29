package crawler

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
)

var crawlerLog = logging.Component("crawler")

func normaliseCacheStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return ""
	}

	upper := strings.ToUpper(status)

	// Fastly shielding emits "HIT, MISS" — last value is the edge POP result.
	if strings.Contains(upper, ", ") {
		parts := strings.Split(upper, ", ")
		if len(parts) > 0 {
			upper = strings.TrimSpace(parts[len(parts)-1])
		}
	}

	if strings.Contains(upper, "FROM CLOUDFRONT") {
		if strings.HasPrefix(upper, "HIT") || strings.HasPrefix(upper, "REFRESHHIT") {
			return "HIT"
		}
		if strings.HasPrefix(upper, "MISS") {
			return "MISS"
		}
		return "BYPASS"
	}

	if strings.Contains(upper, " FROM ") && strings.HasPrefix(upper, "TCP_") {
		parts := strings.Split(upper, " FROM")
		if len(parts) > 0 {
			upper = strings.TrimSpace(parts[0])
		}
	}

	if strings.HasPrefix(upper, "TCP_") {
		if strings.Contains(upper, "HIT") {
			return "HIT"
		}
		if strings.Contains(upper, "MISS") {
			return "MISS"
		}
		return "BYPASS"
	}

	switch upper {
	case "UNCACHEABLE", "NONE", "UNKNOWN":
		return "BYPASS"
	}

	// RFC 9211 Cache-Status format (Netlify et al.).
	if strings.Contains(status, ";") {
		lower := strings.ToLower(status)
		if strings.Contains(lower, "; hit") || strings.Contains(lower, ";hit") {
			return "HIT"
		}
		if strings.Contains(lower, "fwd=") {
			return "MISS"
		}
	}

	switch upper {
	case "HIT", "MISS", "DYNAMIC", "BYPASS", "EXPIRED", "STALE", "REVALIDATED", "UPDATING", "PRERENDER", "PASS":
		return upper
	}

	return status
}

func stripDiagnosticURL(raw string) string {
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsedURL.RawQuery = ""
	parsedURL.Fragment = ""
	return parsedURL.String()
}

func buildRequestMetadata(method, targetURL, finalURL string, timestamp time.Time, provenance string) RequestMetadata {
	scrubbedTargetURL := stripDiagnosticURL(targetURL)
	scrubbedFinalURL := stripDiagnosticURL(finalURL)
	meta := RequestMetadata{
		Method:     method,
		URL:        scrubbedTargetURL,
		FinalURL:   scrubbedFinalURL,
		Timestamp:  timestamp.Unix(),
		Provenance: provenance,
	}

	parsedURL, err := url.Parse(scrubbedFinalURL)
	if err != nil {
		parsedURL, err = url.Parse(scrubbedTargetURL)
	}
	if err == nil {
		meta.Scheme = parsedURL.Scheme
		meta.Host = parsedURL.Host
		meta.Path = parsedURL.Path
		meta.Query = ""
	}

	return meta
}

func buildCacheMetadata(headers http.Header) CacheMetadata {
	cache := CacheMetadata{
		Age:           headers.Get("Age"),
		CacheControl:  headers.Get("Cache-Control"),
		Vary:          headers.Get("Vary"),
		CacheStatus:   headers.Get("Cache-Status"),
		CFCacheStatus: headers.Get("CF-Cache-Status"),
		XCache:        headers.Get("X-Cache"),
		XCacheRemote:  headers.Get("X-Cache-Remote"),
		XVercelCache:  headers.Get("x-vercel-cache"),
		XVarnish:      headers.Get("X-Varnish"),
	}

	for _, candidate := range []struct {
		name  string
		value string
	}{
		{name: "CF-Cache-Status", value: cache.CFCacheStatus},
		{name: "X-Cache", value: cache.XCache},
		{name: "X-Cache-Remote", value: cache.XCacheRemote},
		{name: "x-vercel-cache", value: cache.XVercelCache},
		{name: "Cache-Status", value: cache.CacheStatus},
	} {
		if candidate.value != "" {
			cache.HeaderSource = candidate.name
			cache.RawValue = candidate.value
			cache.NormalisedStatus = normaliseCacheStatus(candidate.value)
			return cache
		}
	}

	if cache.XVarnish != "" {
		cache.HeaderSource = "X-Varnish"
		cache.RawValue = cache.XVarnish
		if strings.Contains(cache.XVarnish, " ") {
			cache.NormalisedStatus = "HIT"
		} else {
			cache.NormalisedStatus = "MISS"
		}
	}

	return cache
}

func redactDiagnosticHeaders(headers http.Header) http.Header {
	cloned := headers.Clone()
	for _, name := range []string{
		"Authorization",
		"Proxy-Authorization",
		"Cookie",
		"Set-Cookie",
		"X-Api-Key",
		"X-Auth-Token",
	} {
		cloned.Del(name)
	}
	return cloned
}

func buildProbeErrorDiagnostics(method, targetURL string, err error) ProbeDiagnostics {
	requestMeta := buildRequestMetadata(method, targetURL, targetURL, time.Now().UTC(), "probe")
	responseMeta := ResponseMetadata{Error: err.Error()}
	return ProbeDiagnostics{
		Request:  &requestMeta,
		Response: &responseMeta,
	}
}

func buildRequestAttemptDiagnostics(
	method string,
	targetURL string,
	finalURL string,
	timestamp time.Time,
	provenance string,
	requestHeaders http.Header,
	responseHeaders http.Header,
	response ResponseMetadata,
	timing PerformanceMetrics,
) *RequestAttemptDiagnostics {
	requestHeaders = redactDiagnosticHeaders(requestHeaders)
	responseHeaders = redactDiagnosticHeaders(responseHeaders)
	requestMeta := buildRequestMetadata(method, targetURL, finalURL, timestamp, provenance)
	cacheMeta := buildCacheMetadata(responseHeaders)

	return &RequestAttemptDiagnostics{
		Request:         &requestMeta,
		Response:        &response,
		RequestHeaders:  requestHeaders,
		ResponseHeaders: responseHeaders,
		Timing:          &timing,
		Cache:           &cacheMeta,
	}
}

type Crawler struct {
	config      *Config
	colly       *colly.Collector
	id          string
	metricsMap  *sync.Map
	aia         *aiaTransport
	probeClient *http.Client // Shared to avoid per-call transport leaks.
}

func (c *Crawler) GetUserAgent() string {
	return c.config.UserAgent
}

// Probe runs a pre-flight WAF detection request against the homepage of
// a domain using the crawler's configured User-Agent and shared probe
// client. Reuses probeClient so the connection pool / DNS cache are
// shared with the rest of the crawl path.
func (c *Crawler) Probe(ctx context.Context, domain string) (WAFDetection, error) {
	var transport http.RoundTripper
	if c.probeClient != nil {
		transport = c.probeClient.Transport
	}
	return Probe(ctx, domain, c.config.UserAgent, transport)
}

type tracingRoundTripper struct {
	transport  http.RoundTripper
	metricsMap *sync.Map
}

func (t *tracingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	metrics := &PerformanceMetrics{}

	var dnsStartTime, connectStartTime, tlsStartTime, requestStartTime time.Time
	requestStartTime = time.Now()

	trace := &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			dnsStartTime = time.Now()
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if !dnsStartTime.IsZero() {
				metrics.DNSLookupTime = time.Since(dnsStartTime).Milliseconds()
			}
		},
		ConnectStart: func(network, addr string) {
			connectStartTime = time.Now()
		},
		ConnectDone: func(network, addr string, err error) {
			if err == nil && !connectStartTime.IsZero() {
				metrics.TCPConnectionTime = time.Since(connectStartTime).Milliseconds()
			}
		},
		TLSHandshakeStart: func() {
			tlsStartTime = time.Now()
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if err == nil && !tlsStartTime.IsZero() {
				metrics.TLSHandshakeTime = time.Since(tlsStartTime).Milliseconds()
			}
		},
		GotFirstResponseByte: func() {
			metrics.TTFB = time.Since(requestStartTime).Milliseconds()
		},
	}

	t.metricsMap.Store(req.URL.String(), metrics)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	return t.transport.RoundTrip(req)
}

func New(config *Config, id ...string) *Crawler {
	if config == nil {
		config = DefaultConfig()
	}

	crawlerID := ""
	if len(id) > 0 {
		crawlerID = id[0]
	}

	userAgent := config.UserAgent
	if crawlerID != "" {
		userAgent = fmt.Sprintf("%s Worker-%s", config.UserAgent, crawlerID)
	}

	c := colly.NewCollector(
		colly.UserAgent(userAgent),
		colly.MaxDepth(1),
		colly.Async(true),
		colly.AllowURLRevisit(),
	)

	// Jitter requests over a 1s window: Delay=1s/RateLimit, RandomDelay tops up to 1s.
	baseDelay := time.Second / time.Duration(config.RateLimit)
	if err := c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: config.MaxConcurrency,
		Delay:       baseDelay,
		RandomDelay: time.Second - baseDelay,
	}); err != nil {
		crawlerLog.Error("Failed to set crawler limits", "error", err)
	}

	metricsMap := &sync.Map{}

	baseTransport := newBaseHTTPTransport()

	// SSRF dial validates IPs post-resolution to defeat DNS rebinding.
	if !config.SkipSSRFCheck {
		baseTransport.DialContext = ssrfSafeDialContext()
	}

	// AIA wrap repairs incomplete server cert chains.
	aiaRT := newAIATransport(baseTransport)

	tracingTransport := &tracingRoundTripper{
		transport:  aiaRT,
		metricsMap: metricsMap,
	}

	httpClient := &http.Client{
		Timeout:   config.DefaultTimeout,
		Transport: tracingTransport,
	}
	c.SetClient(httpClient)

	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
		r.Headers.Set("Accept-Language", "en-US,en;q=0.9")
		r.Headers.Set("Accept-Encoding", "gzip, deflate, br")

		if r.URL.Host != "" {
			r.Headers.Set("Referer", fmt.Sprintf("https://%s/", r.URL.Host))
		}

		crawlerLog.Debug("Crawler sending request", "url", r.URL.String())
	})

	// Probe transport derives from the base so H2/compression posture stays consistent;
	// smaller pool because probes are HEAD-only.
	probeTransport := newBaseHTTPTransport()
	probeTransport.MaxIdleConns = 20
	probeTransport.MaxIdleConnsPerHost = 5
	probeTransport.MaxConnsPerHost = 10
	probeTransport.IdleConnTimeout = 30 * time.Second
	if !config.SkipSSRFCheck {
		probeTransport.DialContext = ssrfSafeDialContext()
	}

	return &Crawler{
		config:     config,
		colly:      c,
		id:         crawlerID,
		metricsMap: metricsMap,
		aia:        aiaRT,
		probeClient: &http.Client{
			Timeout:   config.DefaultTimeout,
			Transport: newAIATransport(probeTransport),
		},
	}
}

// SSRF guard: block loopback, link-local, RFC1918, and unspecified ranges.
func isPrivateOrLocalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	if ip.IsUnspecified() {
		return true
	}
	return false
}

// Dials a connection to a resolved IP only after rejecting private/local addresses,
// defeating DNS rebinding. Prefers IPv4 for upstream compatibility.
func ssrfSafeDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("DNS lookup failed: %w", err)
		}

		for _, ip := range ips {
			if isPrivateOrLocalIP(ip) {
				crawlerLog.Warn("SSRF protection: blocked connection to private/local IP", "host", host, "ip", ip.String())
				return nil, fmt.Errorf("blocked connection to private/local IP: %s resolves to %s", host, ip.String())
			}
		}

		var connectAddr string
		for _, ip := range ips {
			if ip.To4() != nil {
				connectAddr = net.JoinHostPort(ip.String(), port)
				break
			}
		}
		if connectAddr == "" && len(ips) > 0 {
			connectAddr = net.JoinHostPort(ips[0].String(), port)
		}

		dialer := &net.Dialer{}
		return dialer.DialContext(ctx, network, connectAddr)
	}
}

func validateCrawlRequest(ctx context.Context, targetURL string, _ bool) (*url.URL, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid URL format: %s", targetURL)
	}

	return parsed, nil
}

func (c *Crawler) setupResponseHandlers(collyClone *colly.Collector, result *CrawlResult, startTime time.Time, targetURL string) {
	collyClone.OnResponse(func(r *colly.Response) {
		startTime := r.Ctx.GetAny("start_time").(time.Time)
		result := r.Ctx.GetAny("result").(*CrawlResult)

		if metricsVal, ok := c.metricsMap.LoadAndDelete(r.Request.URL.String()); ok {
			performanceMetrics := metricsVal.(*PerformanceMetrics)
			if performanceMetrics.TTFB > 0 {
				performanceMetrics.ContentTransferTime = time.Since(startTime).Milliseconds() - performanceMetrics.TTFB
			}
			result.Performance = *performanceMetrics
		}

		result.ResponseTime = time.Since(startTime).Milliseconds()
		result.StatusCode = r.StatusCode
		result.ContentType = r.Headers.Get("Content-Type")
		result.ContentLength = int64(len(r.Body))
		result.Headers = r.Headers.Clone()
		result.RedirectURL = r.Request.URL.String()
		requestHeaders := r.Request.Headers.Clone()

		// BodySample is truncated for wappalyzer detection; Body keeps the full content.
		result.Body = r.Body
		if len(r.Body) > MaxBodySampleSize {
			result.BodySample = r.Body[:MaxBodySampleSize]
		} else {
			result.BodySample = r.Body
		}

		cfCacheStatus := r.Headers.Get("CF-Cache-Status")
		cfRay := r.Headers.Get("CF-Ray")
		cfDatacenter := r.Headers.Get("CF-IPCountry")
		cfConnectingIP := r.Headers.Get("CF-Connecting-IP")
		cfVisitor := r.Headers.Get("CF-Visitor")

		crawlerLog.Debug("Cloudflare headers analysis",
			"url", r.Request.URL.String(),
			"cf_cache_status", cfCacheStatus,
			"cf_ray", cfRay,
			"cf_datacenter", cfDatacenter,
			"cf_connecting_ip", cfConnectingIP,
			"cf_visitor", cfVisitor,
			"response_time_ms", result.ResponseTime,
		)

		cacheMeta := buildCacheMetadata(result.Headers)
		result.CacheStatus = cacheMeta.NormalisedStatus

		// WAF fingerprint runs against the unredacted result.Headers so the
		// Akamai akaalb_ Set-Cookie signal survives — the diagnostic clone
		// strips Set-Cookie before storage.
		if det := DetectWAF(r.StatusCode, result.Headers, result.BodySample); det.Blocked {
			result.WAF = &det
		}

		if r.StatusCode < 200 || r.StatusCode >= 300 {
			result.Error = fmt.Sprintf("non-success status code: %d", r.StatusCode)
		}

		result.RequestDiagnostics.Primary = buildRequestAttemptDiagnostics(
			r.Request.Method,
			targetURL,
			result.RedirectURL,
			startTime,
			"primary",
			requestHeaders,
			result.Headers,
			ResponseMetadata{
				StatusCode:    result.StatusCode,
				ContentType:   result.ContentType,
				ContentLength: result.ContentLength,
				RedirectURL:   stripDiagnosticURL(result.RedirectURL),
				Warning:       result.Warning,
				Error:         result.Error,
			},
			result.Performance,
		)
	})

	collyClone.OnError(func(r *colly.Response, err error) {
		if r == nil || r.Ctx == nil {
			return
		}
		result := r.Ctx.GetAny("result").(*CrawlResult)
		result.Error = err.Error()

		startTime := r.Ctx.GetAny("start_time").(time.Time)
		result.ResponseTime = time.Since(startTime).Milliseconds()
		result.StatusCode = r.StatusCode
		requestHeaders := r.Request.Headers.Clone()
		responseHeaders := http.Header{}
		if r.Headers != nil {
			responseHeaders = r.Headers.Clone()
		}
		result.Headers = responseHeaders
		if r.Request != nil && r.Request.URL != nil {
			result.RedirectURL = r.Request.URL.String()
		}

		// Detector also runs on error paths so 403 walls (Akamai/Cloudflare/
		// DataDome) are flagged for the mid-job circuit breaker even when
		// colly classifies the response as an error.
		if det := DetectWAF(r.StatusCode, responseHeaders, r.Body); det.Blocked {
			result.WAF = &det
		}

		result.RequestDiagnostics.Primary = buildRequestAttemptDiagnostics(
			r.Request.Method,
			targetURL,
			result.RedirectURL,
			startTime,
			"primary",
			requestHeaders,
			responseHeaders,
			ResponseMetadata{
				StatusCode:    result.StatusCode,
				ContentType:   result.ContentType,
				ContentLength: result.ContentLength,
				RedirectURL:   stripDiagnosticURL(result.RedirectURL),
				Warning:       result.Warning,
				Error:         result.Error,
			},
			result.Performance,
		)

		crawlerLog.Debug("URL warming failed",
			"error", err,
			"url", targetURL,
			"duration_ms", time.Duration(result.ResponseTime)*time.Millisecond,
		)
	})
}

var errSoftCacheValidationFailure = errors.New("cache validation failed")
var errCacheValidationSkipped = errors.New("cache validation skipped")

func (c *Crawler) performCacheValidation(ctx context.Context, targetURL string, res *CrawlResult) (bool, error) {
	ensureRequestDiagnostics(res)

	if !shouldMakeSecondRequest(res.CacheStatus) {
		observability.RecordCrawlerPhase(ctx, observability.CrawlerPhaseMetrics{
			Phase:    "cache_validation_skip",
			Outcome:  "not_needed",
			Duration: 0,
		})
		crawlerLog.Debug("No cache warming needed - cache already available or not cacheable",
			"url", targetURL,
			"cache_status", res.CacheStatus,
		)
		return true, errCacheValidationSkipped
	}

	// 500-1000ms jitter avoids hammering origins after a MISS.
	randomInt := 0
	if n, err := crand.Int(crand.Reader, big.NewInt(501)); err == nil {
		randomInt = int(n.Int64())
	} else {
		randomInt = rand.Intn(501) //nolint:gosec // safe fallback for non-sensitive jitter
	}
	jitteredDelay := 500 + randomInt

	crawlerLog.Debug("Cache MISS detected, applying jittered delay before cache validation",
		"url", targetURL,
		"cache_status", res.CacheStatus,
		"initial_response_time", res.ResponseTime,
		"calculated_delay_ms", jitteredDelay,
	)

	select {
	case <-time.After(time.Duration(jitteredDelay) * time.Millisecond):
	case <-ctx.Done():
		crawlerLog.Debug("Cache warming cancelled during initial delay", "url", targetURL)
		return true, ctx.Err()
	}

	maxChecks := 3
	delayBeforeAttempt := jitteredDelay
	nextCheckDelay := 700
	cacheHit := false

	for i := range maxChecks {
		probeStart := time.Now()
		probe, err := c.CheckCacheStatus(ctx, targetURL)
		probeDuration := time.Since(probeStart)
		probe.Attempt = i + 1
		probe.DelayMS = delayBeforeAttempt
		cacheStatus := ""
		if probe.Cache != nil {
			cacheStatus = probe.Cache.NormalisedStatus
		}

		attempt := CacheCheckAttempt{
			Attempt:     i + 1,
			CacheStatus: cacheStatus,
			Delay:       delayBeforeAttempt,
		}
		res.CacheCheckAttempts = append(res.CacheCheckAttempts, attempt)
		if res.RequestDiagnostics != nil {
			res.RequestDiagnostics.Probes = append(res.RequestDiagnostics.Probes, probe)
		}
		observability.RecordCrawlerPhase(ctx, observability.CrawlerPhaseMetrics{
			Phase:    "cache_probe",
			Outcome:  classifyProbeOutcome(err, probe),
			Duration: probeDuration,
		})

		if err != nil {
			crawlerLog.Warn("Failed to check cache status",
				"error", err,
				"url", targetURL,
				"check_attempt", i+1,
			)
		} else {
			crawlerLog.Debug("Cache status check",
				"url", targetURL,
				"cache_status", cacheStatus,
				"check_attempt", i+1,
			)

			if cacheStatus == "HIT" || cacheStatus == "STALE" || cacheStatus == "REVALIDATED" {
				cacheHit = true
				break
			}

			// CDN says uncacheable — abandon further probes.
			if !shouldMakeSecondRequest(cacheStatus) {
				crawlerLog.Debug("Cache status indicates resource will not warm; skipping additional checks",
					"url", targetURL,
					"cache_status", cacheStatus,
					"check_attempt", i+1,
				)
				break
			}
		}

		if i < maxChecks-1 {
			select {
			case <-time.After(time.Duration(nextCheckDelay) * time.Millisecond):
				delayBeforeAttempt = nextCheckDelay
			case <-ctx.Done():
				crawlerLog.Debug("Cache warming cancelled during check loop", "url", targetURL)
				return true, ctx.Err()
			}
			nextCheckDelay += 300
		}
	}

	if cacheHit {
		crawlerLog.Debug("Cache is now available, proceeding with second request", "url", targetURL)
	} else {
		crawlerLog.Debug("Cache did not become available after maximum checks",
			"url", targetURL,
			"max_checks", maxChecks,
		)
	}

	if cacheHit {
		secondStart := time.Now()
		secondResult, err := c.makeSecondRequest(ctx, targetURL)
		secondDuration := time.Since(secondStart)
		if err != nil {
			crawlerLog.Debug("Second request failed, but first request succeeded",
				"error", err,
				"url", targetURL,
				"secondary_duration_ms", secondDuration,
			)
		}
		if secondResult != nil && secondResult.RequestDiagnostics != nil && secondResult.RequestDiagnostics.Timings != nil {
			res.RequestDiagnostics.Timings.SecondaryRequestMS = secondResult.RequestDiagnostics.Timings.SecondaryRequestMS
		} else {
			res.RequestDiagnostics.Timings.SecondaryRequestMS = secondDuration.Milliseconds()
		}
		if secondResult != nil && secondResult.RequestDiagnostics != nil && secondResult.RequestDiagnostics.Primary != nil {
			secondary := *secondResult.RequestDiagnostics.Primary
			if secondary.Request != nil {
				requestCopy := *secondary.Request
				requestCopy.Provenance = "secondary"
				secondary.Request = &requestCopy
			}
			res.RequestDiagnostics.Secondary = &secondary
		}
		if err != nil {
			return true, fmt.Errorf("%w: secondary request failed: %w", errSoftCacheValidationFailure, err)
		}
		if secondResult != nil {
			res.SecondResponseTime = secondResult.ResponseTime
			res.SecondCacheStatus = secondResult.CacheStatus
			res.SecondContentLength = secondResult.ContentLength
			res.SecondHeaders = secondResult.Headers
			res.SecondPerformance = &secondResult.Performance

			improvementRatio := float64(0)
			improvementRatioValid := res.SecondResponseTime > 0
			if improvementRatioValid {
				improvementRatio = float64(res.ResponseTime) / float64(res.SecondResponseTime)
			}

			crawlerLog.Debug("Cache warming analysis - pattern data",
				"url", targetURL,
				"first_cache_status", res.CacheStatus,
				"second_cache_status", res.SecondCacheStatus,
				"first_response_time", res.ResponseTime,
				"second_response_time", res.SecondResponseTime,
				"initial_delay_ms", jitteredDelay,
				"improvement_ratio", improvementRatio,
				"improvement_ratio_valid", improvementRatioValid,
				"cache_hit_before_second", cacheHit,
			)
		}
	} else {
		crawlerLog.Debug("Cache status did not transition to HIT; skipping second request",
			"url", targetURL,
			"cache_status", res.CacheStatus,
		)
	}

	return false, nil
}

func setupLinkExtraction(collyClone *colly.Collector) {
	collyClone.OnHTML("html", func(e *colly.HTMLElement) {
		findLinksVal := e.Request.Ctx.GetAny("find_links")
		if findLinksVal == nil {
			crawlerLog.Debug("find_links not set in context - defaulting to enabled", "url", e.Request.URL.String())
		} else if findLinks, ok := findLinksVal.(bool); ok && !findLinks {
			crawlerLog.Debug("Link extraction disabled for this request",
				"url", e.Request.URL.String(),
				"find_links", findLinks,
			)
			return
		}

		result, ok := e.Request.Ctx.GetAny("result").(*CrawlResult)
		if !ok {
			crawlerLog.Debug("No result context - not collecting links", "url", e.Request.URL.String())
			return
		}

		extractLinks := func(selection *goquery.Selection, category string) {
			selection.Find("a").Each(func(i int, s *goquery.Selection) {
				href := strings.TrimSpace(s.AttrOr("href", ""))
				if href == "" || href == "#" || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
					return
				}

				if isElementHidden(s) {
					return
				}

				var u string
				if strings.HasPrefix(href, "?") {
					base := e.Request.URL
					base.RawQuery = ""
					u = base.String() + href
				} else {
					u = e.Request.AbsoluteURL(href)
				}

				result.Links[category] = append(result.Links[category], u)
			})
		}

		extractLinks(e.DOM.Find("header"), "header")
		extractLinks(e.DOM.Find("footer"), "footer")

		// Strip header/footer so the remainder becomes "body".
		e.DOM.Find("header").Remove()
		e.DOM.Find("footer").Remove()
		extractLinks(e.DOM, "body")

		crawlerLog.Debug("Categorized links from page",
			"url", e.Request.URL.String(),
			"header_links", len(result.Links["header"]),
			"footer_links", len(result.Links["footer"]),
			"body_links", len(result.Links["body"]),
		)
	})
}

func executeCollyRequest(ctx context.Context, collyClone *colly.Collector, targetURL string, res *CrawlResult) error {
	done := make(chan error, 1)

	// Run colly off-thread so ctx cancellation can interrupt the request-level timeout.
	go func() {
		visitErr := collyClone.Visit(targetURL)
		if visitErr != nil {
			done <- visitErr
			return
		}
		collyClone.Wait()
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			res.Error = err.Error()
			crawlerLog.Debug("Colly visit failed", "error", err, "url", targetURL)
			return err
		}
		return nil
	case <-ctx.Done():
		res.Error = ctx.Err().Error()
		crawlerLog.Debug("URL warming cancelled due to context timeout", "error", ctx.Err(), "url", targetURL)
		return ctx.Err()
	}
}

func classifyCrawlerOutcome(err error) string {
	if err == nil {
		return "success"
	}
	if errorsIsContextDone(err) {
		return "cancelled"
	}
	return "error"
}

func errorsIsContextDone(err error) bool {
	if err == nil {
		return false
	}

	errorStr := strings.ToLower(err.Error())
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(errorStr, "context deadline exceeded") ||
		strings.Contains(errorStr, "context canceled")
}

func classifyProbeOutcome(err error, probe ProbeDiagnostics) string {
	if err != nil {
		return classifyCrawlerOutcome(err)
	}
	if probe.Response != nil && probe.Response.StatusCode >= 400 {
		return "error"
	}
	return "success"
}

func ensureRequestDiagnostics(res *CrawlResult) {
	if res.RequestDiagnostics == nil {
		res.RequestDiagnostics = &RequestDiagnostics{}
	}
	if res.RequestDiagnostics.Timings == nil {
		res.RequestDiagnostics.Timings = &RequestStageTimings{}
	}
}

func metricErrForRequestPhase(err error, res *CrawlResult) error {
	if err != nil {
		return err
	}
	if res == nil {
		return nil
	}
	if res.Error != "" {
		return fmt.Errorf("%s", res.Error)
	}
	if res.StatusCode != 0 && (res.StatusCode < 200 || res.StatusCode >= 300) {
		return fmt.Errorf("non-success status code: %d", res.StatusCode)
	}
	return nil
}

func applyPhaseTiming(res *CrawlResult, phase string, duration time.Duration) {
	if res == nil {
		return
	}
	ensureRequestDiagnostics(res)
	switch phase {
	case "primary_request":
		res.RequestDiagnostics.Timings.PrimaryRequestMS = duration.Milliseconds()
	case "secondary_request":
		res.RequestDiagnostics.Timings.SecondaryRequestMS = duration.Milliseconds()
	default:
		crawlerLog.Warn("Skipping timing assignment for unexpected crawler phase", "phase", phase)
	}
}

func (c *Crawler) warmURL(ctx context.Context, targetURL string, findLinks bool, allowCacheValidation bool, requestPhase string) (*CrawlResult, error) {
	_, err := validateCrawlRequest(ctx, targetURL, c.config.SkipSSRFCheck)
	if err != nil {
		res := &CrawlResult{URL: targetURL, Timestamp: time.Now().Unix(), Error: err.Error()}
		return res, err
	}

	start := time.Now()
	res := &CrawlResult{
		URL:                targetURL,
		Timestamp:          start.Unix(),
		Links:              make(map[string][]string),
		RequestDiagnostics: &RequestDiagnostics{},
	}
	ensureRequestDiagnostics(res)

	crawlerLog.Debug("Starting URL warming with Colly",
		"url", targetURL,
		"find_links", findLinks,
		"cache_validation_enabled", allowCacheValidation,
	)

	collyClone := c.colly.Clone()
	setupLinkExtraction(collyClone)

	collyClone.OnRequest(func(r *colly.Request) {
		r.Ctx.Put("result", res)
		r.Ctx.Put("start_time", start)
		r.Ctx.Put("find_links", findLinks)
	})

	c.setupResponseHandlers(collyClone, res, start, targetURL)

	primaryStart := time.Now()
	err = executeCollyRequest(ctx, collyClone, targetURL, res)
	primaryDuration := time.Since(primaryStart)
	phaseErr := metricErrForRequestPhase(err, res)
	observability.RecordCrawlerPhase(ctx, observability.CrawlerPhaseMetrics{
		Phase:    requestPhase,
		Outcome:  classifyCrawlerOutcome(phaseErr),
		Duration: primaryDuration,
	})
	applyPhaseTiming(res, requestPhase, primaryDuration)
	if err != nil {
		res.RequestDiagnostics.Timings.TotalMS = time.Since(start).Milliseconds()
		return res, err
	}

	if res.Error != "" {
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			crawlerLog.Debug("URL warming returned non-success status",
				"status", res.StatusCode,
				"url", targetURL,
				"error", res.Error,
				"duration_ms", time.Duration(res.ResponseTime)*time.Millisecond,
			)
		} else {
			crawlerLog.Debug("URL warming failed",
				"url", targetURL,
				"error", res.Error,
				"duration_ms", time.Duration(res.ResponseTime)*time.Millisecond,
			)
		}
		res.RequestDiagnostics.Timings.TotalMS = time.Since(start).Milliseconds()
		return res, fmt.Errorf("%s", res.Error)
	}

	if allowCacheValidation {
		cacheValidationStart := time.Now()
		cacheValidationProcessed, err := c.performCacheValidation(ctx, targetURL, res)
		if err != nil {
			if errors.Is(err, errCacheValidationSkipped) {
				res.RequestDiagnostics.Timings.CacheValidationMS = time.Since(cacheValidationStart).Milliseconds()
			} else {
				cacheValidationDuration := time.Since(cacheValidationStart)
				observability.RecordCrawlerPhase(ctx, observability.CrawlerPhaseMetrics{
					Phase:    "cache_validation",
					Outcome:  classifyCrawlerOutcome(err),
					Duration: cacheValidationDuration,
				})
				res.RequestDiagnostics.Timings.CacheValidationMS = cacheValidationDuration.Milliseconds()
				if !errors.Is(err, errSoftCacheValidationFailure) {
					res.RequestDiagnostics.Timings.TotalMS = time.Since(start).Milliseconds()
					return res, err
				}
			}
		}
		if !cacheValidationProcessed {
			cacheValidationDuration := time.Since(cacheValidationStart)
			observability.RecordCrawlerPhase(ctx, observability.CrawlerPhaseMetrics{
				Phase:    "cache_validation",
				Outcome:  "success",
				Duration: cacheValidationDuration,
			})
			res.RequestDiagnostics.Timings.CacheValidationMS = cacheValidationDuration.Milliseconds()
		}
	}

	res.RequestDiagnostics.Timings.TotalMS = time.Since(start).Milliseconds()
	crawlerLog.Debug("Crawler phase summary",
		"url", targetURL,
		"cache_validation_enabled", allowCacheValidation,
		"primary_request_ms", res.RequestDiagnostics.Timings.PrimaryRequestMS,
		"cache_validation_ms", res.RequestDiagnostics.Timings.CacheValidationMS,
		"secondary_request_ms", res.RequestDiagnostics.Timings.SecondaryRequestMS,
		"total_ms", res.RequestDiagnostics.Timings.TotalMS,
		"probe_attempts", len(res.RequestDiagnostics.Probes),
		"cache_status", res.CacheStatus,
		"secondary_cache_status", res.SecondCacheStatus,
	)

	return res, nil
}

func (c *Crawler) WarmURL(ctx context.Context, targetURL string, findLinks bool) (*CrawlResult, error) {
	return c.warmURL(ctx, targetURL, findLinks, true, "primary_request")
}

func shouldMakeSecondRequest(cacheStatus string) bool {
	switch strings.ToUpper(cacheStatus) {
	case "MISS", "EXPIRED":
		return true
	default:
		return false
	}
}

func (c *Crawler) makeSecondRequest(ctx context.Context, targetURL string) (*CrawlResult, error) {
	return c.warmURL(ctx, targetURL, false, false, "secondary_request")
}

func (c *Crawler) CheckCacheStatus(ctx context.Context, targetURL string) (ProbeDiagnostics, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", targetURL, nil)
	if err != nil {
		return buildProbeErrorDiagnostics(http.MethodHead, targetURL, err), err
	}

	req.Header.Set("User-Agent", c.config.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")

	resp, err := c.probeClient.Do(req)
	if err != nil {
		return buildProbeErrorDiagnostics(req.Method, targetURL, err), err
	}
	defer resp.Body.Close()

	responseHeaders := resp.Header.Clone()
	cacheMeta := buildCacheMetadata(responseHeaders)
	requestMeta := buildRequestMetadata(req.Method, targetURL, resp.Request.URL.String(), time.Now().UTC(), "probe")
	responseMeta := ResponseMetadata{StatusCode: resp.StatusCode, RedirectURL: stripDiagnosticURL(resp.Request.URL.String())}

	return ProbeDiagnostics{
		Request:  &requestMeta,
		Response: &responseMeta,
		Cache:    &cacheMeta,
		DelayMS:  0,
	}, nil
}

func (c *Crawler) CreateHTTPClient(timeout time.Duration) *http.Client {
	if timeout == 0 {
		timeout = c.config.DefaultTimeout
	}

	transport := newBaseHTTPTransport()

	if !c.config.SkipSSRFCheck {
		transport.DialContext = ssrfSafeDialContext()
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

// ForceAttemptHTTP2 disabled: misbehaving H2 upstreams flood logs with
// "received DATA after END_STREAM" under sustained crawl load. ALPN H1.1
// fallback removes the noise without measurable throughput impact.
func newBaseHTTPTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        150,
		MaxIdleConnsPerHost: 25,
		MaxConnsPerHost:     50,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
	}
}

func (c *Crawler) Config() *Config {
	return c.config
}

// Best-effort visibility check on raw HTML; cannot evaluate external CSS.
func isElementHidden(s *goquery.Selection) bool {
	hidingClasses := []string{
		"hide",
		"hidden",
		"display-none",
		"d-none",
		"invisible",
		"is-hidden",
		"sr-only",
		"visually-hidden",
	}

	for n := s; n.Length() > 0 && !n.Is("body"); n = n.Parent() {
		if _, exists := n.Attr("data-hidden"); exists {
			return true
		}
		if val, exists := n.Attr("data-visible"); exists && val == "false" {
			return true
		}

		if ariaHidden, exists := n.Attr("aria-hidden"); exists && ariaHidden == "true" {
			return true
		}

		if style, exists := n.Attr("style"); exists {
			if strings.Contains(style, "display: none") || strings.Contains(style, "visibility: hidden") {
				return true
			}
		}

		if classAttr, exists := n.Attr("class"); exists {
			padded := " " + classAttr + " "
			for _, class := range hidingClasses {
				if strings.Contains(padded, " "+class+" ") {
					return true
				}
			}
		}
	}

	return false
}
