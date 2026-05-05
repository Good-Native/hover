package crawler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/good-native/hover/internal/util"
)

// decompressGzip decompresses gzip-encoded data
func decompressGzip(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress gzip data: %w", err)
	}

	return decompressed, nil
}

// isGzipContent checks if the response is gzip-encoded based on headers or URL
func isGzipContent(contentEncoding, url string) bool {
	// Check Content-Encoding header
	if strings.EqualFold(contentEncoding, "gzip") {
		return true
	}

	// Check URL suffix for .gz or .xml.gz
	lowerURL := strings.ToLower(url)
	return strings.HasSuffix(lowerURL, ".gz")
}

// SitemapDiscoveryResult contains both sitemaps and robots.txt rules
type SitemapDiscoveryResult struct {
	Sitemaps    []string
	RobotsRules *RobotsRules
}

// DiscoverSitemapsAndRobots attempts to find sitemaps and parse robots.txt rules for a domain
func (c *Crawler) DiscoverSitemapsAndRobots(ctx context.Context, domain string) (*SitemapDiscoveryResult, error) {
	// Normalise the domain first to handle different input formats
	normalisedDomain := util.NormaliseDomain(domain)
	crawlerLog.Debug("Starting sitemap and robots.txt discovery",
		"original_domain", domain,
		"normalised_domain", normalisedDomain,
	)

	result := &SitemapDiscoveryResult{
		Sitemaps:    []string{},
		RobotsRules: &RobotsRules{}, // Default empty rules
	}

	// Parse robots.txt first - this gets us both sitemaps and crawl rules
	// Guard against Go's nil-interface trap: a typed nil *aiaTransport
	// satisfies http.RoundTripper with a non-nil interface value.
	var robotRules *RobotsRules
	var err error
	if c.aia != nil {
		robotRules, err = ParseRobotsTxt(ctx, normalisedDomain, c.config.UserAgent, c.aia)
	} else {
		robotRules, err = ParseRobotsTxt(ctx, normalisedDomain, c.config.UserAgent)
	}
	if err != nil {
		// Log at warn so TLS/network issues are visible in production logs
		crawlerLog.Warn("Failed to parse robots.txt, proceeding with no restrictions",
			"error", err,
			"domain", normalisedDomain,
		)
	} else {
		result.RobotsRules = robotRules
		result.Sitemaps = robotRules.Sitemaps
	}

	// Log if sitemaps were found in robots.txt
	if len(result.Sitemaps) > 0 {
		crawlerLog.Debug("Sitemaps found in robots.txt", "sitemaps", result.Sitemaps)
	} else {
		crawlerLog.Debug("No sitemaps found in robots.txt")
	}

	// If no sitemaps found in robots.txt, check common locations
	if len(result.Sitemaps) == 0 {
		commonPaths := []string{
			"https://" + normalisedDomain + "/sitemap.xml",
			"https://" + normalisedDomain + "/sitemap_index.xml",
		}

		// Create a client for checking common locations (reuse AIA transport
		// so servers with incomplete certificate chains still work).
		client := &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		}
		if c.aia != nil {
			client.Transport = c.aia
		}

		// Check common locations concurrently with a timeout
		for _, sitemapURL := range commonPaths {
			crawlerLog.Debug("Checking common sitemap location", "checking_sitemap_url", sitemapURL)
			req, err := http.NewRequestWithContext(ctx, "HEAD", sitemapURL, nil)
			if err != nil {
				crawlerLog.Debug("Error creating request for sitemap", "error", err, "url", sitemapURL)
				continue
			}
			req.Header.Set("User-Agent", c.config.UserAgent)

			resp, err := client.Do(req)
			if err != nil {
				crawlerLog.Warn("Error fetching sitemap at common location", "error", err, "url", sitemapURL)
				continue
			}

			_ = resp.Body.Close()
			crawlerLog.Debug("Sitemap check response", "url", sitemapURL, "status", resp.StatusCode)
			if resp.StatusCode == http.StatusOK {
				result.Sitemaps = append(result.Sitemaps, sitemapURL)
				crawlerLog.Debug("Found sitemap at common location", "url", sitemapURL)
			}
		}
	}

	// Deduplicate sitemaps
	seen := make(map[string]bool)
	var uniqueSitemaps []string
	for _, sitemap := range result.Sitemaps {
		if !seen[sitemap] {
			seen[sitemap] = true
			uniqueSitemaps = append(uniqueSitemaps, sitemap)
		}
	}
	result.Sitemaps = uniqueSitemaps

	// Log final result
	if len(result.Sitemaps) > 0 {
		crawlerLog.Debug("Found sitemaps and robots rules for domain",
			"sitemaps", result.Sitemaps,
			"count", len(result.Sitemaps),
			"crawl_delay", result.RobotsRules.CrawlDelay,
			"disallow_patterns", len(result.RobotsRules.DisallowPatterns),
		)
	} else {
		crawlerLog.Debug("No sitemaps found but got robots rules for domain",
			"domain", domain,
			"crawl_delay", result.RobotsRules.CrawlDelay,
			"disallow_patterns", len(result.RobotsRules.DisallowPatterns),
		)
	}

	return result, nil
}

// DiscoverSitemaps is a backward-compatible wrapper that only returns sitemaps
func (c *Crawler) DiscoverSitemaps(ctx context.Context, domain string) ([]string, error) {
	result, err := c.DiscoverSitemapsAndRobots(ctx, domain)
	if err != nil {
		return nil, err
	}
	return result.Sitemaps, nil
}

// Create proper sitemap structs
type SitemapIndex struct {
	XMLName  xml.Name  `xml:"sitemapindex"`
	Sitemaps []Sitemap `xml:"sitemap"`
}

type Sitemap struct {
	XMLName xml.Name `xml:"sitemap"`
	Loc     string   `xml:"loc"`
}

type URLSet struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []URL    `xml:"url"`
}

type URL struct {
	XMLName xml.Name `xml:"url"`
	Loc     string   `xml:"loc"`
}

// ParseSitemap extracts URLs from a sitemap
func (c *Crawler) ParseSitemap(ctx context.Context, sitemapURL string) ([]string, error) {
	var urls []string

	req, err := http.NewRequestWithContext(ctx, "GET", sitemapURL, nil)
	if err != nil {
		return nil, err
	}

	// Request gzip encoding if server supports it
	req.Header.Set("Accept-Encoding", "gzip")

	client := &http.Client{Timeout: 30 * time.Second}
	if c.aia != nil {
		client.Transport = c.aia
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch sitemap: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Decompress if gzip-encoded (via header or .gz URL suffix)
	contentEncoding := resp.Header.Get("Content-Encoding")
	if isGzipContent(contentEncoding, sitemapURL) {
		crawlerLog.Debug("Decompressing gzip sitemap",
			"url", sitemapURL,
			"content_encoding", contentEncoding,
			"compressed_size", len(body),
		)

		body, err = decompressGzip(body)
		if err != nil {
			return nil, fmt.Errorf("failed to decompress sitemap %s: %w", sitemapURL, err)
		}

		crawlerLog.Debug("Sitemap decompressed successfully", "url", sitemapURL, "decompressed_size", len(body))
	}

	// Log the content for debugging
	crawlerLog.Debug("Sitemap content received",
		"url", sitemapURL,
		"content_length", len(body),
		"content_sample", string(body[:min(100, len(body))]),
	)

	// Try to unmarshal as a sitemap index first.
	// encoding/xml handles CDATA, XML entities (&amp; etc.), namespaces,
	// and whitespace automatically.
	var index SitemapIndex
	if err := xml.Unmarshal(body, &index); err == nil && len(index.Sitemaps) > 0 {
		crawlerLog.Debug("Parsed as sitemap index", "url", sitemapURL, "child_count", len(index.Sitemaps))

		for _, child := range index.Sitemaps {
			childURL := util.NormaliseURL(strings.TrimSpace(child.Loc))
			if childURL == "" {
				crawlerLog.Warn("Invalid child sitemap URL, skipping", "url", child.Loc)
				continue
			}

			childURLs, err := c.ParseSitemap(ctx, childURL)
			if err != nil {
				crawlerLog.Warn("Failed to parse child sitemap", "error", err, "url", childURL)
				continue
			}
			urls = append(urls, childURLs...)
		}
	} else {
		// Parse as a regular URL set
		var urlSet URLSet
		if err := xml.Unmarshal(body, &urlSet); err != nil {
			crawlerLog.Warn("Failed to parse sitemap XML", "error", err, "url", sitemapURL)
			// Return empty rather than error — malformed sitemaps shouldn't halt crawling
			return urls, nil
		}

		var validURLs []string
		for _, u := range urlSet.URLs {
			validURL := util.NormaliseURL(strings.TrimSpace(u.Loc))
			if validURL != "" {
				validURLs = append(validURLs, validURL)
			} else {
				crawlerLog.Debug("Skipping invalid URL from sitemap", "invalid_url", u.Loc)
			}
		}

		crawlerLog.Debug("Extracted valid URLs from regular sitemap",
			"sitemap_url", sitemapURL,
			"url_count", len(validURLs),
		)
		urls = append(urls, validURLs...)
	}

	crawlerLog.Debug("Finished parsing sitemap", "sitemap_url", sitemapURL, "total_url_count", len(urls))

	return urls, nil
}

// FilterURLs filters URLs based on include/exclude patterns
func (c *Crawler) FilterURLs(urls []string, includePaths, excludePaths []string) []string {
	if len(includePaths) == 0 && len(excludePaths) == 0 {
		return urls
	}

	var filtered []string

	for _, url := range urls {
		// If include patterns exist, URL must match at least one
		includeMatch := len(includePaths) == 0
		for _, pattern := range includePaths {
			if strings.Contains(url, pattern) {
				includeMatch = true
				break
			}
		}

		if !includeMatch {
			continue
		}

		// If URL matches any exclude pattern, skip it
		excludeMatch := false
		for _, pattern := range excludePaths {
			if strings.Contains(url, pattern) {
				excludeMatch = true
				break
			}
		}

		if !excludeMatch {
			filtered = append(filtered, url)
		}
	}

	return filtered
}
