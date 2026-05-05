package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/util"
)

// GA4 API limits and constraints
const (
	// GA4InitialBatchSize is the first batch of pages fetched
	GA4InitialBatchSize = 100

	// GA4MediumBatchSize is used for subsequent fetches after the initial batch
	GA4MediumBatchSize = 1000

	// GA4LargeBatchSize is the maximum batch size for bulk fetching
	// GA4 API supports up to 250,000 rows per request
	GA4LargeBatchSize = 50000

	// GA4MediumBatchThreshold is the offset at which we switch from medium to large batches
	GA4MediumBatchThreshold = 10000

	// Date range lookback periods for analytics queries
	GA4Lookback7Days   = 7
	GA4Lookback28Days  = 28
	GA4Lookback180Days = 180
)

// GA4Client is an HTTP client for the Google Analytics 4 Data API
type GA4Client struct {
	mu           sync.RWMutex
	httpClient   *http.Client
	accessToken  string
	clientID     string
	clientSecret string
}

// GA4APIError represents a non-200 response from the GA4 Data API.
type GA4APIError struct {
	StatusCode int
	Body       string
}

func (err *GA4APIError) Error() string {
	return fmt.Sprintf("GA4 API returned status %d", err.StatusCode)
}

func (err *GA4APIError) IsUnauthorised() bool {
	return err.StatusCode == http.StatusUnauthorized
}

// PageViewData represents analytics data for a single page
type PageViewData struct {
	HostName      string
	PagePath      string
	PageViews7d   int64
	PageViews28d  int64
	PageViews180d int64
}

// ga4RunReportRequest is the request structure for the GA4 runReport API
type ga4RunReportRequest struct {
	DateRanges []dateRange `json:"dateRanges"`
	Dimensions []dimension `json:"dimensions"`
	Metrics    []metric    `json:"metrics"`
	OrderBys   []orderBy   `json:"orderBys"`
	Limit      int         `json:"limit"`
	Offset     int         `json:"offset"`
	// DimensionFilter restricts report rows (used for host filtering)
	DimensionFilter *filterExpression `json:"dimensionFilter,omitempty"`
}

type dateRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

type dimension struct {
	Name string `json:"name"`
}

type metric struct {
	Name string `json:"name"`
}

type orderBy struct {
	Metric metricOrderBy `json:"metric"`
	Desc   bool          `json:"desc"`
}

type filterExpression struct {
	Filter *dimensionFilter `json:"filter,omitempty"`
}

type dimensionFilter struct {
	FieldName    string        `json:"fieldName"`
	InListFilter *inListFilter `json:"inListFilter,omitempty"`
}

type inListFilter struct {
	Values        []string `json:"values"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
}

type metricOrderBy struct {
	MetricName string `json:"metricName"`
}

// ga4RunReportResponse is the response structure from the GA4 runReport API
type ga4RunReportResponse struct {
	Rows []struct {
		DimensionValues []struct {
			Value string `json:"value"`
		} `json:"dimensionValues"`
		MetricValues []struct {
			Value string `json:"value"`
		} `json:"metricValues"`
	} `json:"rows"`
	RowCount int `json:"rowCount"`
}

// tokenRefreshResponse is the OAuth token refresh response
type tokenRefreshResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
	TokenType   string `json:"token_type"`
}

// NewGA4Client creates a new GA4 Data API client
func NewGA4Client(accessToken, clientID, clientSecret string) *GA4Client {
	return &GA4Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		accessToken:  accessToken,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// RefreshAccessToken exchanges a refresh token for a new access token
// Uses application/x-www-form-urlencoded as required by OAuth 2.0 RFC 6749
func (c *GA4Client) RefreshAccessToken(ctx context.Context, refreshToken string) (string, error) {
	// Build form data per OAuth 2.0 spec (RFC 6749)
	formData := url.Values{}
	formData.Set("client_id", c.clientID)
	formData.Set("client_secret", c.clientSecret)
	formData.Set("refresh_token", refreshToken)
	formData.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute token refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token refresh response: %w", err)
	}

	apiLog.Debug("Successfully refreshed Google access token", "expires_in", tokenResp.ExpiresIn)

	return tokenResp.AccessToken, nil
}

// FetchTopPages fetches top N pages ordered by screenPageViews descending
// Returns page data for 7-day, 28-day, and 180-day lookback periods
// Makes 3 separate API calls and merges results by path
func (c *GA4Client) FetchTopPages(ctx context.Context, propertyID string, limit, offset int, allowedHostnames []string) ([]PageViewData, error) {
	start := time.Now()

	allowedHosts := make(map[string]struct{})
	for _, host := range allowedHostnames {
		if host == "" {
			continue
		}
		allowedHosts[strings.ToLower(host)] = struct{}{}
	}

	// Fetch 7-day data (primary - determines page ordering)
	pages7d, err := c.fetchSingleDateRange(ctx, propertyID, "7daysAgo", limit, offset, allowedHosts)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch 7d data: %w", err)
	}

	// Build lookup map for merging additional date ranges
	// Key by hostname:path to avoid collisions when GA4 property tracks multiple hostnames
	pageMap := make(map[string]*PageViewData)
	pages := make([]PageViewData, 0, len(pages7d))

	for _, p := range pages7d {
		page := PageViewData{
			HostName:    p.HostName,
			PagePath:    p.PagePath,
			PageViews7d: p.PageViews,
		}
		pages = append(pages, page)
		mapKey := p.HostName + ":" + p.PagePath
		pageMap[mapKey] = &pages[len(pages)-1]
	}

	// Fetch 28-day data and merge
	pages28d, err := c.fetchSingleDateRange(ctx, propertyID, "28daysAgo", limit, offset, allowedHosts)
	if err != nil {
		apiLog.Warn("Failed to fetch 28d data, continuing with 7d only", "error", err)
	} else {
		for _, p := range pages28d {
			mapKey := p.HostName + ":" + p.PagePath
			if existing, ok := pageMap[mapKey]; ok {
				existing.PageViews28d = p.PageViews
			}
		}
	}

	// Fetch 180-day data and merge
	pages180d, err := c.fetchSingleDateRange(ctx, propertyID, "180daysAgo", limit, offset, allowedHosts)
	if err != nil {
		apiLog.Warn("Failed to fetch 180d data, continuing without it", "error", err)
	} else {
		for _, p := range pages180d {
			mapKey := p.HostName + ":" + p.PagePath
			if existing, ok := pageMap[mapKey]; ok {
				existing.PageViews180d = p.PageViews
			}
		}
	}

	elapsed := time.Since(start)
	apiLog.Info("GA4 data fetch completed (3 date ranges)", "property_id", propertyID, "pages_count", len(pages), "duration", elapsed)

	return pages, nil
}

// singleDateRangeResult holds page view data for a single date range
type singleDateRangeResult struct {
	HostName  string
	PagePath  string
	PageViews int64
}

// fetchSingleDateRange fetches page view data for a single date range
func (c *GA4Client) fetchSingleDateRange(ctx context.Context, propertyID, startDate string, limit, offset int, allowedHosts map[string]struct{}) ([]singleDateRangeResult, error) {
	req := ga4RunReportRequest{
		DateRanges: []dateRange{
			{StartDate: startDate, EndDate: "today"},
		},
		Dimensions: []dimension{
			{Name: "hostName"},
			{Name: "pagePath"},
		},
		Metrics: []metric{
			{Name: "screenPageViews"},
		},
		OrderBys: []orderBy{
			{
				Metric: metricOrderBy{MetricName: "screenPageViews"},
				Desc:   true,
			},
		},
		Limit:  limit,
		Offset: offset,
	}

	if len(allowedHosts) > 0 {
		hostValues := make([]string, 0, len(allowedHosts))
		for host := range allowedHosts {
			hostValues = append(hostValues, host)
		}
		req.DimensionFilter = &filterExpression{
			Filter: &dimensionFilter{
				FieldName: "hostName",
				InListFilter: &inListFilter{
					Values:        hostValues,
					CaseSensitive: false,
				},
			},
		}
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal runReport request: %w", err)
	}

	url := fmt.Sprintf("https://analyticsdata.googleapis.com/v1beta/properties/%s:runReport", propertyID)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody)) //nolint:gosec // G704: url targets googleapis.com; propertyID from trusted config
	if err != nil {
		return nil, fmt.Errorf("failed to create runReport request: %w", err)
	}

	c.mu.RLock()
	token := c.accessToken
	c.mu.RUnlock()

	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	apiLog.Debug("Fetching GA4 report for date range",
		"property_id", propertyID,
		"start_date", startDate,
		"limit", limit,
		"offset", offset,
	)

	resp, err := c.httpClient.Do(httpReq) //nolint:gosec // G704: request targets googleapis.com
	if err != nil {
		return nil, fmt.Errorf("failed to execute runReport request: %w", err)
	}
	defer resp.Body.Close()

	// Handle non-200 responses
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, &GA4APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var reportResp ga4RunReportResponse
	if err := json.NewDecoder(resp.Body).Decode(&reportResp); err != nil {
		return nil, fmt.Errorf("failed to decode runReport response: %w", err)
	}

	// Parse response
	results := make([]singleDateRangeResult, 0, len(reportResp.Rows))
	malformedCount := 0
	parseFailCount := 0
	for _, row := range reportResp.Rows {
		if len(row.DimensionValues) < 2 || len(row.MetricValues) < 1 {
			malformedCount++
			continue
		}

		hostName := row.DimensionValues[0].Value

		pageViews, err := strconv.ParseInt(row.MetricValues[0].Value, 10, 64)
		if err != nil {
			parseFailCount++
			continue
		}

		results = append(results, singleDateRangeResult{
			HostName:  hostName,
			PagePath:  row.DimensionValues[1].Value,
			PageViews: pageViews,
		})
	}

	if malformedCount > 0 || parseFailCount > 0 {
		apiLog.Warn("GA4 report rows skipped due to malformed data",
			"property_id", propertyID,
			"start_date", startDate,
			"total_rows", len(reportResp.Rows),
			"malformed_rows", malformedCount,
			"parse_failures", parseFailCount,
		)
	}

	return results, nil
}

// FetchTopPagesWithRetry fetches top pages with automatic token refresh on 401
func (c *GA4Client) FetchTopPagesWithRetry(ctx context.Context, propertyID, refreshToken string, limit, offset int, allowedHostnames []string) ([]PageViewData, error) {
	pages, err := c.FetchTopPages(ctx, propertyID, limit, offset, allowedHostnames)
	if err != nil {
		// Check if error is 401 Unauthorised (token expired)
		if isUnauthorisedError(err) {
			apiLog.Info("Access token expired, refreshing and retrying", "property_id", propertyID)

			// Refresh access token
			newAccessToken, refreshErr := c.RefreshAccessToken(ctx, refreshToken)
			if refreshErr != nil {
				return nil, fmt.Errorf("failed to refresh access token: %w", refreshErr)
			}

			c.mu.Lock()
			c.accessToken = newAccessToken
			c.mu.Unlock()

			// Retry request with new token
			pages, err = c.FetchTopPages(ctx, propertyID, limit, offset, allowedHostnames)
			if err != nil {
				return nil, fmt.Errorf("request failed after token refresh: %w", err)
			}
		} else {
			return nil, err
		}
	}

	return pages, nil
}

// isUnauthorisedError checks if an error indicates a 401 Unauthorised response
func isUnauthorisedError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *GA4APIError
	if errors.As(err, &apiErr) {
		return apiErr.IsUnauthorised()
	}
	return false
}

// DBInterfaceGA4 defines the database operations needed by the progressive fetcher
type DBInterfaceGA4 interface {
	GetActiveGAConnectionForDomain(ctx context.Context, organisationID string, domainID int) (*db.GoogleAnalyticsConnection, error)
	GetDomainNameByID(ctx context.Context, domainID int) (string, error)
	GetGoogleToken(ctx context.Context, connectionID string) (string, error)
	UpdateConnectionLastSync(ctx context.Context, connectionID string) error
	MarkConnectionInactive(ctx context.Context, connectionID, reason string) error
	UpsertPageWithAnalytics(ctx context.Context, organisationID string, domainID int, path string, pageViews map[string]int64, connectionID string) (int, error)
	CalculateTrafficScores(ctx context.Context, organisationID string, domainID int) error
	ApplyTrafficScoresToTasks(ctx context.Context, organisationID string, domainID int) error
}

// ProgressiveFetcher orchestrates GA4 data fetching in multiple phases
type ProgressiveFetcher struct {
	db           DBInterfaceGA4
	clientID     string
	clientSecret string
}

// NewProgressiveFetcher creates a new progressive fetcher instance
func NewProgressiveFetcher(database DBInterfaceGA4, clientID, clientSecret string) *ProgressiveFetcher {
	return &ProgressiveFetcher{
		db:           database,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// FetchAndUpdatePages fetches GA4 data in 3 phases and updates the pages table
// Phase 1 is blocking (top 100 pages), phases 2-3 run in background goroutines
func (pf *ProgressiveFetcher) FetchAndUpdatePages(ctx context.Context, organisationID string, domainID int) error {
	start := time.Now()

	// 1. Get active GA4 connection for this domain
	conn, err := pf.db.GetActiveGAConnectionForDomain(ctx, organisationID, domainID)
	if err != nil {
		return fmt.Errorf("failed to get GA4 connection for domain: %w", err)
	}

	// No active connection is not an error - just skip GA4 integration
	if conn == nil {
		apiLog.Debug("No active GA4 connection, skipping analytics fetch", "organisation_id", organisationID)
		return nil
	}

	domainName, err := pf.db.GetDomainNameByID(ctx, domainID)
	if err != nil {
		return fmt.Errorf("failed to get domain name for GA4 fetch: %w", err)
	}

	allowedHosts := []string{}
	normalisedDomain := util.NormaliseDomain(domainName)
	if normalisedDomain != "" {
		allowedHosts = append(allowedHosts, normalisedDomain)
		allowedHosts = append(allowedHosts, "www."+normalisedDomain)
	}

	// 2. Get refresh token from vault
	refreshToken, err := pf.db.GetGoogleToken(ctx, conn.ID)
	if err != nil {
		apiLog.Error("Failed to get refresh token from vault", "error", err, "connection_id", conn.ID)
		return fmt.Errorf("failed to get refresh token: %w", err)
	}

	// 3. Create GA4 client and refresh access token
	client := NewGA4Client("", pf.clientID, pf.clientSecret)
	accessToken, err := client.RefreshAccessToken(ctx, refreshToken)
	if err != nil {
		// Mark connection inactive on auth failure
		apiLog.Error("Failed to refresh access token", "error", err, "connection_id", conn.ID)

		if markErr := pf.db.MarkConnectionInactive(ctx, conn.ID, "token refresh failed"); markErr != nil {
			apiLog.Error("Failed to mark connection inactive after token refresh failure", "error", markErr, "connection_id", conn.ID)
		}

		return fmt.Errorf("failed to refresh access token: %w", err)
	}
	client.mu.Lock()
	client.accessToken = accessToken
	client.mu.Unlock()

	// 4. Fetch initial batch of top pages
	apiLog.Info("Fetching initial batch of pages from GA4",
		"organisation_id", organisationID,
		"property_id", conn.GA4PropertyID,
		"batch_size", GA4InitialBatchSize,
	)

	initialData, err := client.FetchTopPagesWithRetry(ctx, conn.GA4PropertyID, refreshToken, GA4InitialBatchSize, 0, allowedHosts)
	if err != nil {
		apiLog.Error("Failed to fetch initial GA4 data", "error", err, "property_id", conn.GA4PropertyID)
		return fmt.Errorf("failed to fetch initial data: %w", err)
	}

	// 5. Upsert initial data
	if err := pf.upsertPageData(ctx, organisationID, domainID, conn.ID, initialData); err != nil {
		apiLog.Error("Failed to upsert initial page data", "error", err, "domain_id", domainID, "pages_count", len(initialData))
		return fmt.Errorf("failed to upsert initial data: %w", err)
	}

	// Log sample of top pages for verification (without end-user content)
	for i := range min(5, len(initialData)) {
		apiLog.Info("GA4 top page", "rank", i+1, "page_views_7d", initialData[i].PageViews7d)
	}

	apiLog.Info("Initial GA4 fetch completed",
		"organisation_id", organisationID,
		"pages_count", len(initialData),
		"duration", time.Since(start),
	)

	// 6. Calculate initial traffic scores based on top pages
	if err := pf.db.CalculateTrafficScores(ctx, organisationID, domainID); err != nil {
		apiLog.Warn("Failed to calculate initial traffic scores; continuing with existing priorities",
			"error", err, "organisation_id", organisationID, "domain_id", domainID)
	}
	if err := pf.db.ApplyTrafficScoresToTasks(ctx, organisationID, domainID); err != nil {
		apiLog.Warn("Failed to apply traffic scores to pending tasks; continuing without reprioritisation",
			"error", err, "organisation_id", organisationID, "domain_id", domainID)
	}

	// 7. Fetch remaining pages in background (loops until all fetched)
	go func() { //nolint:gosec // G118: intentionally outlives request; background GA4 page fetch
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		pf.fetchRemainingPagesBackground(bgCtx, organisationID, conn.GA4PropertyID, domainID, conn.ID, client, refreshToken, allowedHosts)
	}()

	// 8. Update last sync timestamp
	if err := pf.db.UpdateConnectionLastSync(ctx, conn.ID); err != nil {
		// Log but don't fail - this is not critical
		apiLog.Warn("Failed to update last sync timestamp; will retry on next sync", "error", err, "connection_id", conn.ID)
	}

	return nil
}

// upsertPageData upserts page analytics data into the page_analytics table
func (pf *ProgressiveFetcher) upsertPageData(ctx context.Context, organisationID string, domainID int, connectionID string, pages []PageViewData) error {
	var failedCount int
	var firstErr error
	for _, page := range pages {
		path := util.ExtractPathFromURL(page.PagePath)
		pageViews := map[string]int64{
			"7d":   page.PageViews7d,
			"28d":  page.PageViews28d,
			"180d": page.PageViews180d,
		}

		_, err := pf.db.UpsertPageWithAnalytics(ctx, organisationID, domainID, path, pageViews, connectionID)
		if err != nil {
			failedCount++
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	apiLog.Debug("Processed GA4 page analytics batch",
		"organisation_id", organisationID,
		"domain_id", domainID,
		"pages_processed", len(pages),
		"pages_failed", failedCount,
	)

	if failedCount > 0 {
		apiLog.Error("Failed to upsert some GA4 page analytics rows",
			"error", firstErr,
			"organisation_id", organisationID,
			"domain_id", domainID,
			"pages_failed", failedCount,
			"pages_total", len(pages),
		)
		return fmt.Errorf("failed to upsert %d pages: %w", failedCount, firstErr)
	}

	return nil
}

// fetchRemainingPagesBackground fetches all remaining pages after the initial batch
// Uses medium batches (1000) until 10k threshold, then large batches (50000) until done
func (pf *ProgressiveFetcher) fetchRemainingPagesBackground(ctx context.Context, organisationID, propertyID string, domainID int, connectionID string, client *GA4Client, refreshToken string, allowedHosts []string) {
	start := time.Now()
	offset := GA4InitialBatchSize
	totalPages := 0

	apiLog.Info("Starting background fetch of remaining pages", "property_id", propertyID, "offset", offset)

	// Phase 2: Medium batches (1000) until threshold
	for offset < GA4MediumBatchThreshold {
		batchSize := min(GA4MediumBatchSize, GA4MediumBatchThreshold-offset)

		pages, err := client.FetchTopPagesWithRetry(ctx, propertyID, refreshToken, batchSize, offset, allowedHosts)
		if err != nil {
			apiLog.Error("Failed to fetch GA4 data batch", "error", err, "property_id", propertyID, "offset", offset)
			return
		}

		if len(pages) == 0 {
			apiLog.Info("Background GA4 fetch completed (no more pages)",
				"property_id", propertyID,
				"total_pages", totalPages,
				"duration", time.Since(start),
			)
			return
		}

		if err := pf.upsertPageData(ctx, organisationID, domainID, connectionID, pages); err != nil {
			apiLog.Error("Failed to upsert page data batch", "error", err, "domain_id", domainID, "pages_count", len(pages))
			return
		}

		totalPages += len(pages)
		offset += len(pages)

		apiLog.Info("Fetched medium batch",
			"property_id", propertyID,
			"batch_pages", len(pages),
			"total_pages", totalPages,
			"offset", offset,
		)

		// If we got fewer than requested, we've fetched all pages
		if len(pages) < batchSize {
			apiLog.Info("Background GA4 fetch completed (all pages fetched)",
				"property_id", propertyID,
				"total_pages", totalPages,
				"duration", time.Since(start),
			)
			return
		}
	}

	// Phase 3: Large batches (50000) until all pages fetched
	for {
		pages, err := client.FetchTopPagesWithRetry(ctx, propertyID, refreshToken, GA4LargeBatchSize, offset, allowedHosts)
		if err != nil {
			apiLog.Error("Failed to fetch GA4 data batch", "error", err, "property_id", propertyID, "offset", offset)
			return
		}

		if len(pages) == 0 {
			break
		}

		if err := pf.upsertPageData(ctx, organisationID, domainID, connectionID, pages); err != nil {
			apiLog.Error("Failed to upsert page data batch", "error", err, "domain_id", domainID, "pages_count", len(pages))
			return
		}

		totalPages += len(pages)
		offset += len(pages)

		apiLog.Info("Fetched large batch",
			"property_id", propertyID,
			"batch_pages", len(pages),
			"total_pages", totalPages,
			"offset", offset,
		)

		// If we got fewer than requested, we've fetched all pages
		if len(pages) < GA4LargeBatchSize {
			break
		}
	}

	apiLog.Info("Background GA4 fetch completed",
		"property_id", propertyID,
		"total_pages", totalPages,
		"duration", time.Since(start),
	)

	// Calculate traffic scores based on page view percentiles
	if err := pf.db.CalculateTrafficScores(ctx, organisationID, domainID); err != nil {
		apiLog.Warn("Failed to calculate traffic scores after GA4 fetch",
			"error", err,
			"organisation_id", organisationID,
			"domain_id", domainID,
			"next_action", "continuing_without_score_updates",
		)
	}
	if err := pf.db.ApplyTrafficScoresToTasks(ctx, organisationID, domainID); err != nil {
		apiLog.Warn("Failed to apply traffic scores after GA4 fetch; tasks continue without reprioritisation",
			"error", err,
			"organisation_id", organisationID,
			"domain_id", domainID,
		)
	}
}
