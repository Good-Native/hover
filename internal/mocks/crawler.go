package mocks

import (
	"context"

	"github.com/good-native/hover/internal/crawler"
	"github.com/stretchr/testify/mock"
)

// MockCrawler is a mock implementation of the Crawler
type MockCrawler struct {
	mock.Mock
}

// WarmURL mocks the WarmURL method
func (m *MockCrawler) WarmURL(ctx context.Context, url string, findLinks bool) (*crawler.CrawlResult, error) {
	args := m.Called(ctx, url, findLinks)

	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(*crawler.CrawlResult), args.Error(1)
}

// DiscoverSitemapsAndRobots mocks the DiscoverSitemapsAndRobots method
func (m *MockCrawler) DiscoverSitemapsAndRobots(ctx context.Context, domain string) (*crawler.SitemapDiscoveryResult, error) {
	args := m.Called(ctx, domain)

	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(*crawler.SitemapDiscoveryResult), args.Error(1)
}

// ParseSitemap mocks the ParseSitemap method
func (m *MockCrawler) ParseSitemap(ctx context.Context, sitemapURL string) ([]string, error) {
	args := m.Called(ctx, sitemapURL)

	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).([]string), args.Error(1)
}

// FilterURLs mocks the FilterURLs method
func (m *MockCrawler) FilterURLs(urls []string, includePaths, excludePaths []string) []string {
	args := m.Called(urls, includePaths, excludePaths)

	if args.Get(0) == nil {
		return nil
	}

	return args.Get(0).([]string)
}

// GetUserAgent mocks the GetUserAgent method
func (m *MockCrawler) GetUserAgent() string {
	args := m.Called()
	return args.String(0)
}
