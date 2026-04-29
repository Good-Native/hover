package crawler

import (
	"strings"
	"testing"
)

func TestParseRobotsTxtContent(t *testing.T) {
	tests := []struct {
		name         string
		robotsTxt    string
		userAgent    string
		wantDelay    int
		wantSitemaps []string
		wantDisallow []string
		wantAllow    []string
	}{
		{
			name: "Hover specific rules",
			robotsTxt: `
User-agent: *
Crawl-delay: 1
Disallow: /admin

User-agent: Hover
Crawl-delay: 5
Disallow: /checkout
Disallow: /cart
Allow: /cart/view

Sitemap: https://example.com/sitemap.xml
`,
			userAgent:    "Hover/1.0 (+https://www.goodnative.co/hover)",
			wantDelay:    5,
			wantSitemaps: []string{"https://example.com/sitemap.xml"},
			wantDisallow: []string{"/checkout", "/cart"},
			wantAllow:    []string{"/cart/view"},
		},
		{
			name: "Wildcard rules only",
			robotsTxt: `
User-agent: *
Crawl-delay: 10
Disallow: /private/
Disallow: /tmp/

Sitemap: https://example.com/sitemap1.xml
Sitemap: https://example.com/sitemap2.xml
`,
			userAgent:    "Hover/1.0",
			wantDelay:    10,
			wantSitemaps: []string{"https://example.com/sitemap1.xml", "https://example.com/sitemap2.xml"},
			wantDisallow: []string{"/private/", "/tmp/"},
			wantAllow:    []string{},
		},
		{
			name: "No matching rules",
			robotsTxt: `
User-agent: Googlebot
Crawl-delay: 2
Disallow: /nogoogle

User-agent: Bingbot
Crawl-delay: 3
Disallow: /nobing
`,
			userAgent:    "Hover/1.0",
			wantDelay:    0,
			wantSitemaps: []string{},
			wantDisallow: []string{},
			wantAllow:    []string{},
		},
		{
			name: "Uses wildcard rules, ignores SEO bot rules",
			robotsTxt: `
User-agent: *
Crawl-delay: 1
Disallow: /admin
Sitemap: https://example.com/sitemap.xml

User-agent: AhrefsBot
Crawl-delay: 10
Disallow: /checkout
Disallow: /cart
Allow: /cart/view
`,
			userAgent:    "Hover/1.0",
			wantDelay:    1,
			wantSitemaps: []string{"https://example.com/sitemap.xml"},
			wantDisallow: []string{"/admin"},
			wantAllow:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := strings.NewReader(tt.robotsTxt)
			rules, err := parseRobotsTxtContent(reader, tt.userAgent)
			if err != nil {
				t.Fatalf("parseRobotsTxtContent() error = %v", err)
			}

			if rules.CrawlDelay != tt.wantDelay {
				t.Errorf("CrawlDelay = %v, want %v", rules.CrawlDelay, tt.wantDelay)
			}

			if len(rules.Sitemaps) != len(tt.wantSitemaps) {
				t.Errorf("Sitemaps count = %v, want %v", len(rules.Sitemaps), len(tt.wantSitemaps))
			}

			if len(rules.DisallowPatterns) != len(tt.wantDisallow) {
				t.Errorf("DisallowPatterns count = %v, want %v", len(rules.DisallowPatterns), len(tt.wantDisallow))
			}

			if len(rules.AllowPatterns) != len(tt.wantAllow) {
				t.Errorf("AllowPatterns count = %v, want %v", len(rules.AllowPatterns), len(tt.wantAllow))
			}
		})
	}
}

func TestIsPathAllowed(t *testing.T) {
	rules := &RobotsRules{
		DisallowPatterns: []string{"/admin", "/private/", "/tmp/*", "/test$"},
		AllowPatterns:    []string{"/admin/public"},
	}

	tests := []struct {
		path    string
		allowed bool
	}{
		{"/", true},
		{"/index.html", true},
		{"/admin", false},
		{"/admin/secret", false},
		{"/admin/public", true}, // Allow overrides Disallow
		{"/private/data", false},
		{"/tmp/file", false},
		{"/test", false}, // $ means exact match, so /test$ blocks /test
		{"/test/", true}, // Not exact match, so allowed
		{"/public", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsPathAllowed(rules, tt.path); got != tt.allowed {
				t.Errorf("IsPathAllowed(%q) = %v, want %v", tt.path, got, tt.allowed)
			}
		})
	}
}
