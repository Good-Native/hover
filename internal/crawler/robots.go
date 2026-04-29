package crawler

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type RobotsRules struct {
	CrawlDelay       int // seconds; 0 means unspecified
	Sitemaps         []string
	DisallowPatterns []string
	AllowPatterns    []string // override DisallowPatterns
}

// Precedence: Hover-specific section if present, else wildcard (*).
// Aggressive SEO crawler sections (AhrefsBot, MJ12bot, ...) are intentionally
// not matched — they often carry punitive 10s delays meant for them.
func ParseRobotsTxt(ctx context.Context, domain string, userAgent string, transport ...http.RoundTripper) (*RobotsRules, error) {
	var robotsURL string
	if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		robotsURL = strings.TrimSuffix(domain, "/") + "/robots.txt"
	} else {
		robotsURL = fmt.Sprintf("https://%s/robots.txt", domain)
	}

	crawlerLog.Debug("Fetching robots.txt", "domain", domain, "robots_url", robotsURL)

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	if len(transport) > 0 && transport[0] != nil {
		client.Transport = transport[0]
	}

	req, err := http.NewRequestWithContext(ctx, "GET", robotsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch robots.txt: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			crawlerLog.Debug("No robots.txt found, no restrictions apply")
			return &RobotsRules{}, nil
		}
		return nil, fmt.Errorf("robots.txt returned status %d", resp.StatusCode)
	}

	// 1 MiB cap prevents memory exhaustion on hostile/giant robots.txt.
	limitedReader := io.LimitReader(resp.Body, 1*1024*1024)
	return parseRobotsTxtContent(limitedReader, userAgent)
}

func parseRobotsTxtContent(r io.Reader, userAgent string) (*RobotsRules, error) {
	rules := &RobotsRules{
		Sitemaps:         []string{},
		DisallowPatterns: []string{},
		AllowPatterns:    []string{},
	}

	content, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read robots.txt: %w", err)
	}

	if len(content) == 1*1024*1024 {
		crawlerLog.Warn("Robots.txt file truncated at 1MB limit", "size_bytes", len(content))
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))

	var inOurSection bool
	var inWildcardSection bool
	var foundSpecificSection bool

	// e.g. "Hover/1.0" -> "hover"
	botName := strings.ToLower(strings.Split(userAgent, "/")[0])

	wildcardRules := &RobotsRules{
		Sitemaps:         []string{},
		DisallowPatterns: []string{},
		AllowPatterns:    []string{},
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		lowerLine := strings.ToLower(line)

		if strings.HasPrefix(lowerLine, "user-agent:") {
			agent := strings.TrimSpace(line[11:])
			agentLower := strings.ToLower(agent)

			inOurSection = false
			inWildcardSection = false

			if agent == "*" {
				inWildcardSection = true
			} else if agentLower == botName || strings.Contains(agentLower, botName) {
				inOurSection = true
				foundSpecificSection = true
				// Specific bot wins — discard any wildcard rules collected so far.
				rules = &RobotsRules{
					Sitemaps:         []string{},
					DisallowPatterns: []string{},
					AllowPatterns:    []string{},
				}
				crawlerLog.Debug("Found rules section for our bot", "user_agent_section", agent)
			}
			continue
		}

		// Sitemap directives apply globally regardless of section.
		if strings.HasPrefix(lowerLine, "sitemap:") {
			sitemapURL := strings.TrimSpace(line[8:])
			if sitemapURL != "" {
				rules.Sitemaps = append(rules.Sitemaps, sitemapURL)
				if inWildcardSection && !foundSpecificSection {
					wildcardRules.Sitemaps = append(wildcardRules.Sitemaps, sitemapURL)
				}
			}
			continue
		}

		if !inOurSection && !inWildcardSection {
			continue
		}

		currentRules := rules
		if inWildcardSection && !foundSpecificSection {
			currentRules = wildcardRules
		}

		if strings.HasPrefix(lowerLine, "crawl-delay:") {
			delayStr := strings.TrimSpace(line[12:])
			if delay, err := strconv.Atoi(delayStr); err == nil && delay > 0 {
				currentRules.CrawlDelay = delay
				crawlerLog.Debug("Found Crawl-delay directive",
					"crawl_delay", delay,
					"from_specific_section", inOurSection,
				)
			}
			continue
		}

		if strings.HasPrefix(lowerLine, "disallow:") {
			path := strings.TrimSpace(line[9:])
			// "Disallow: /" blocks the whole site — many sites set this for unknown bots; ignore.
			if path != "" && path != "/" {
				currentRules.DisallowPatterns = append(currentRules.DisallowPatterns, path)
			}
			continue
		}

		if strings.HasPrefix(lowerLine, "allow:") {
			path := strings.TrimSpace(line[6:])
			if path != "" {
				currentRules.AllowPatterns = append(currentRules.AllowPatterns, path)
			}
			continue
		}
	}

	if !foundSpecificSection {
		rules = wildcardRules
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading robots.txt: %w", err)
	}

	crawlerLog.Debug("Parsed robots.txt rules",
		"crawl_delay", rules.CrawlDelay,
		"sitemaps", len(rules.Sitemaps),
		"disallow_patterns", len(rules.DisallowPatterns),
		"allow_patterns", len(rules.AllowPatterns),
	)

	return rules, nil
}

func IsPathAllowed(rules *RobotsRules, path string) bool {
	if rules == nil || len(rules.DisallowPatterns) == 0 {
		return true
	}

	// Allow takes precedence over Disallow per RFC 9309.
	for _, pattern := range rules.AllowPatterns {
		if matchesRobotsPattern(path, pattern) {
			return true
		}
	}

	for _, pattern := range rules.DisallowPatterns {
		if matchesRobotsPattern(path, pattern) {
			return false
		}
	}

	return true
}

// Supports `*` wildcard and `$` end-of-URL marker per the de-facto robots spec.
func matchesRobotsPattern(path, pattern string) bool {
	if before, ok := strings.CutSuffix(pattern, "$"); ok {
		pattern = before
		return path == pattern
	}

	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		if len(parts) == 2 && parts[1] == "" {
			return strings.HasPrefix(path, parts[0])
		}
		currentPos := 0
		for _, part := range parts {
			if part == "" {
				continue
			}
			idx := strings.Index(path[currentPos:], part)
			if idx == -1 {
				return false
			}
			currentPos += idx + len(part)
		}
		return true
	}

	return strings.HasPrefix(path, pattern)
}
