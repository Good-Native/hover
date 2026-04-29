package jobs

import (
	"context"
	"database/sql"

	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
)

// CrawlerInterface defines the methods we need from the crawler
type CrawlerInterface interface {
	WarmURL(ctx context.Context, url string, findLinks bool) (*crawler.CrawlResult, error)
	DiscoverSitemapsAndRobots(ctx context.Context, domain string) (*crawler.SitemapDiscoveryResult, error)
	ParseSitemap(ctx context.Context, sitemapURL string) ([]string, error)
	FilterURLs(urls []string, includePaths, excludePaths []string) []string
	GetUserAgent() string
	// Probe issues a pre-flight WAF fingerprint request against the
	// homepage of a domain. Issue #365 row 1.
	Probe(ctx context.Context, domain string) (crawler.WAFDetection, error)
}

// DbQueueInterface defines the database queue operations needed by the job system.
type DbQueueInterface interface {
	UpdateTaskStatus(ctx context.Context, task *db.Task) error
	Execute(ctx context.Context, fn func(*sql.Tx) error) error
	ExecuteControl(ctx context.Context, fn func(*sql.Tx) error) error
	ExecuteWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error
	ExecuteControlWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error
	ExecuteMaintenance(ctx context.Context, fn func(*sql.Tx) error) error
	SetConcurrencyOverride(fn db.ConcurrencyOverrideFunc)
	UpdateDomainTechnologies(ctx context.Context, domainID int, technologies, headers []byte, htmlPath string) error
	UpdateTaskHTMLMetadata(ctx context.Context, taskID string, metadata db.TaskHTMLMetadata) error
	BatchUpsertTaskHTMLMetadata(ctx context.Context, rows []db.TaskHTMLMetadataRow) error
	PromoteWaitingToPending(ctx context.Context, jobID string, limit int) (int, error)
}
