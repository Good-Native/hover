package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/good-native/hover/internal/cache"
	"github.com/good-native/hover/internal/logging"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var dbLog = logging.Component("db")

// Sentinel errors for missing resources. Callers can branch on these via
// errors.Is to distinguish "this thing doesn't exist" from a transient DB
// failure. Used by Stripe webhook handlers to ACK events Stripe shouldn't
// retry (unknown customer, unknown price) versus surfacing 5xx for real
// faults.
var (
	ErrOrganisationNotFound = errors.New("organisation not found")
	ErrPlanNotFound         = errors.New("plan not found")
)

const (
	// defaultConnMaxLifetime controls how long a connection can live before
	// being recycled. Shorter values improve compatibility with pgBouncer.
	defaultConnMaxLifetime = 5 * time.Minute
	// defaultConnMaxIdleTime closes idle connections after this duration.
	defaultConnMaxIdleTime = 2 * time.Minute
	// statementTimeoutMs is the per-statement timeout appended to the DSN.
	statementTimeoutMs = "60000"
	// idleInTxnTimeoutMs is the idle-in-transaction timeout appended to the DSN.
	idleInTxnTimeoutMs = "30000"
)

// DB represents a PostgreSQL database connection
type DB struct {
	client *sql.DB
	config *Config
	Cache  *cache.InMemoryCache
}

// GetConfig returns the original DB connection settings
func (d *DB) GetConfig() *Config {
	return d.config
}

// Config holds PostgreSQL connection configuration
type Config struct {
	Host            string        // Database host
	Port            string        // Database port
	User            string        // Database user
	Password        string        // Database password
	Database        string        // Database name
	SSLMode         string        // SSL mode (disable, require, verify-ca, verify-full)
	MaxIdleConns    int           // Maximum number of idle connections
	MaxOpenConns    int           // Maximum number of open connections
	MaxLifetime     time.Duration // Maximum lifetime of a connection
	DatabaseURL     string        // Original DATABASE_URL if used
	ApplicationName string        // Identifier for this application instance
}

func poolLimitsForEnv(appEnv string) (maxOpen, maxIdle int) {
	switch appEnv {
	case "production":
		maxOpen, maxIdle = 70, 20
	case "staging":
		maxOpen, maxIdle = 5, 2
	default:
		maxOpen, maxIdle = 2, 1
	}

	if parsed, ok := parsePositiveIntEnv("DB_MAX_OPEN_CONNS"); ok {
		maxOpen = parsed
	} else if raw := strings.TrimSpace(os.Getenv("DB_MAX_OPEN_CONNS")); raw != "" {
		dbLog.Warn("Invalid DB_MAX_OPEN_CONNS; using default", "value", raw)
	}
	if parsed, ok := parsePositiveIntEnv("DB_MAX_IDLE_CONNS"); ok {
		maxIdle = parsed
	} else if raw := strings.TrimSpace(os.Getenv("DB_MAX_IDLE_CONNS")); raw != "" {
		dbLog.Warn("Invalid DB_MAX_IDLE_CONNS; using default", "value", raw)
	}
	if maxOpen < 1 {
		maxOpen = 1
	}
	if maxIdle < 1 {
		maxIdle = 1
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}

	return maxOpen, maxIdle
}

func parsePositiveIntEnv(key string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

func sanitiseAppName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == ':', r == '.':
			builder.WriteRune(r)
		default:
			// Skip unsupported characters to keep connection strings safe
		}
	}

	result := builder.String()
	if result == "" {
		return ""
	}
	return result
}

func trimAppName(name string) string {
	const maxLen = 60 // postgres application_name limit is 64 bytes
	if len(name) <= maxLen {
		return name
	}
	return name[:maxLen]
}

func determineApplicationName() string {
	if override := sanitiseAppName(os.Getenv("DB_APP_NAME")); override != "" {
		return trimAppName(override)
	}

	base := "hover"
	if env := sanitiseAppName(strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))); env != "" {
		base = fmt.Sprintf("hover-%s", env)
	}

	var parts []string
	if machineID := sanitiseAppName(os.Getenv("FLY_MACHINE_ID")); machineID != "" {
		parts = append(parts, machineID)
	}
	if host, err := os.Hostname(); err == nil {
		if hostName := sanitiseAppName(host); hostName != "" {
			parts = append(parts, hostName)
		}
	}
	parts = append(parts, time.Now().UTC().Format("20060102T150405"))

	if len(parts) == 0 {
		return trimAppName(base)
	}

	return trimAppName(fmt.Sprintf("%s:%s", base, strings.Join(parts, ":")))
}

func addConnSetting(connStr, key, value string) (string, bool) {
	if key == "" || value == "" {
		return connStr, false
	}

	trimmed := strings.TrimSpace(connStr)
	if trimmed == "" {
		return connStr, false
	}

	if strings.Contains(trimmed, key+"=") {
		return trimmed, false
	}

	isURL := strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://")

	if isURL {
		parsed, err := url.Parse(trimmed)
		if err == nil {
			q := parsed.Query()
			if q.Get(key) != "" {
				return trimmed, false
			}
			q.Set(key, value)
			parsed.RawQuery = q.Encode()
			return parsed.String(), true
		}

		separator := "?"
		if strings.Contains(trimmed, "?") {
			separator = "&"
		}
		return trimmed + separator + key + "=" + url.QueryEscape(value), true
	}

	escaped := strings.ReplaceAll(value, "'", "")
	if escaped == "" {
		return trimmed, false
	}
	return trimmed + fmt.Sprintf(" %s=%s", key, escaped), true
}

func cleanupAppConnections(ctx context.Context, client *sql.DB, appName string) {
	if client == nil || appName == "" {
		return
	}

	base := appName
	if idx := strings.Index(base, ":"); idx != -1 {
		base = base[:idx]
	}
	if base == "" {
		return
	}

	pattern := base + ":%"
	if base == appName {
		pattern = base
	}

	cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	query := `
		SELECT COALESCE(SUM(CASE WHEN pg_terminate_backend(pid) THEN 1 ELSE 0 END), 0)
		FROM pg_stat_activity
		WHERE pid != pg_backend_pid()
		  AND usename = current_user
		  AND state = 'idle'
		  AND application_name LIKE $1
		  AND application_name <> $2
	`

	var terminated int64
	if err := client.QueryRowContext(cleanupCtx, query, pattern, appName).Scan(&terminated); err != nil {
		dbLog.Warn("Failed to terminate stale PostgreSQL connections for application", "error", err)
		return
	}

	if terminated > 0 {
		dbLog.Info("Terminated stale PostgreSQL connections from previous deployment",
			"application_name", appName,
			"terminated_connections", terminated)
	} else {
		dbLog.Debug("No stale PostgreSQL connections found for termination",
			"application_name", appName)
	}
}

// ConnectionString returns the PostgreSQL connection string
func (c *Config) ConnectionString() string {
	connStr := strings.TrimSpace(c.DatabaseURL)
	if connStr != "" {
		connStr, _ = addConnSetting(connStr, "idle_in_transaction_session_timeout", idleInTxnTimeoutMs)
		connStr, _ = addConnSetting(connStr, "statement_timeout", statementTimeoutMs)
		if strings.Contains(connStr, "pooler.supabase.com") {
			if newStr, added := addConnSetting(connStr, "default_query_exec_mode", "simple_protocol"); added {
				dbLog.Info("Added minimal prepared statement disabling for pooler connection")
				connStr = newStr
			} else {
				connStr = newStr
			}
		}
		if c.ApplicationName != "" {
			connStr, _ = addConnSetting(connStr, "application_name", c.ApplicationName)
		}
		return connStr
	}

	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "require"
	}

	connStr = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Database, sslMode)

	connStr, _ = addConnSetting(connStr, "idle_in_transaction_session_timeout", idleInTxnTimeoutMs)
	connStr, _ = addConnSetting(connStr, "statement_timeout", statementTimeoutMs)
	if strings.Contains(connStr, "pooler.supabase.com") {
		if newStr, added := addConnSetting(connStr, "default_query_exec_mode", "simple_protocol"); added {
			dbLog.Info("Added minimal prepared statement disabling for pooler connection")
			connStr = newStr
		} else {
			connStr = newStr
		}
	}
	if c.ApplicationName != "" {
		connStr, _ = addConnSetting(connStr, "application_name", c.ApplicationName)
	}

	return connStr
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// If we have a DatabaseURL, that's sufficient
	if c.DatabaseURL != "" {
		return nil
	}

	// Otherwise, check individual fields
	if c.Host == "" || c.Port == "" || c.User == "" || c.Password == "" || c.Database == "" {
		if c.Host == "" && c.Port == "" && c.User == "" && c.Password == "" && c.Database == "" {
			return fmt.Errorf("database configuration required")
		}
		return fmt.Errorf("incomplete database configuration")
	}

	return nil
}

// New creates a new PostgreSQL database connection
func New(config *Config) (*DB, error) {
	// Validate required fields only if not using DATABASE_URL
	if config.DatabaseURL == "" {
		if config.Host == "" {
			return nil, fmt.Errorf("database host is required")
		}
		if config.Port == "" {
			return nil, fmt.Errorf("database port is required")
		}
		if config.User == "" {
			return nil, fmt.Errorf("database user is required")
		}
		if config.Database == "" {
			return nil, fmt.Errorf("database name is required")
		}
	}

	// Set defaults for optional fields
	if config.SSLMode == "" {
		config.SSLMode = "disable"
	}
	defaultMaxOpen, defaultMaxIdle := poolLimitsForEnv(os.Getenv("APP_ENV"))
	if config.MaxIdleConns == 0 {
		config.MaxIdleConns = defaultMaxIdle
	}
	if config.MaxOpenConns == 0 {
		config.MaxOpenConns = defaultMaxOpen
	}
	if config.MaxLifetime == 0 {
		config.MaxLifetime = defaultConnMaxLifetime
	}

	if config.ApplicationName == "" {
		config.ApplicationName = determineApplicationName()
	}

	connStr := config.ConnectionString()

	dbLog.Info("Opening PostgreSQL connection")

	client, err := sql.Open("pgx", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Configure connection pool
	client.SetMaxOpenConns(config.MaxOpenConns)
	client.SetMaxIdleConns(config.MaxIdleConns)
	client.SetConnMaxLifetime(config.MaxLifetime)
	client.SetConnMaxIdleTime(defaultConnMaxIdleTime)

	// Test connection
	if err := client.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping PostgreSQL: %w", err)
	}

	cleanupAppConnections(context.Background(), client, config.ApplicationName)

	// Schema is managed by Supabase migrations - no setup required

	// Create the cache
	dbCache := cache.NewInMemoryCache()

	return &DB{client: client, config: config, Cache: dbCache}, nil
}

// InitFromURLWithSuffix creates a PostgreSQL connection using the provided URL and optional
// application name suffix. It applies the same environment-based pooling limits as InitFromEnv.
func InitFromURLWithSuffix(databaseURL string, appEnv string, appNameSuffix string) (*DB, error) {
	trimmed := strings.TrimSpace(databaseURL)
	if trimmed == "" {
		return nil, fmt.Errorf("database url cannot be empty")
	}

	maxOpen, maxIdle := poolLimitsForEnv(appEnv)
	appName := determineApplicationName()
	if suffix := sanitiseAppName(appNameSuffix); suffix != "" {
		if appName != "" {
			appName = trimAppName(fmt.Sprintf("%s:%s", appName, suffix))
		} else {
			appName = trimAppName(suffix)
		}
	}

	config := &Config{
		DatabaseURL:     trimmed,
		MaxIdleConns:    maxIdle,
		MaxOpenConns:    maxOpen,
		MaxLifetime:     5 * time.Minute,
		ApplicationName: appName,
	}

	db, err := New(config)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// InitFromEnv creates a PostgreSQL connection using environment variables
func InitFromEnv() (*DB, error) {
	// If DATABASE_URL is provided, use it with default config
	// Trim whitespace as it causes pgx to ignore the URL and fall back to Unix socket
	if url := strings.TrimSpace(os.Getenv("DATABASE_URL")); url != "" {
		// Optimise connection limits based on environment
		maxOpen, maxIdle := poolLimitsForEnv(os.Getenv("APP_ENV"))

		appName := determineApplicationName()

		config := &Config{
			DatabaseURL:     url,
			MaxIdleConns:    maxIdle,
			MaxOpenConns:    maxOpen,
			MaxLifetime:     5 * time.Minute, // Shorter lifetime for pooler compatibility
			ApplicationName: appName,
		}

		url, _ = addConnSetting(url, "statement_timeout", statementTimeoutMs)
		url, _ = addConnSetting(url, "idle_in_transaction_session_timeout", idleInTxnTimeoutMs)

		if strings.Contains(url, "pooler.supabase.com") {
			if newStr, added := addConnSetting(url, "default_query_exec_mode", "simple_protocol"); added {
				dbLog.Info("Added minimal prepared statement disabling for pooler connection")
				url = newStr
			} else {
				url = newStr
			}

			if newStr, added := addConnSetting(url, "pgbouncer", "true"); added {
				dbLog.Info("Enabled transaction pooling mode (pgbouncer=true)")
				url = newStr
			} else {
				url = newStr
			}
		}

		if appName != "" {
			url, _ = addConnSetting(url, "application_name", appName)
		}

		// Persist the augmented URL back to config for consistency
		config.DatabaseURL = url

		dbLog.Info("Opening PostgreSQL connection via DATABASE_URL", "connection_url", redactURL(url))

		client, err := sql.Open("pgx", url)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to PostgreSQL via DATABASE_URL: %w", err)
		}

		// Configure connection pool using the same settings
		client.SetMaxOpenConns(config.MaxOpenConns)
		client.SetMaxIdleConns(config.MaxIdleConns)
		client.SetConnMaxLifetime(config.MaxLifetime)
		client.SetConnMaxIdleTime(defaultConnMaxIdleTime)

		// Verify connection
		if err := client.Ping(); err != nil {
			return nil, fmt.Errorf("failed to ping PostgreSQL via DATABASE_URL: %w", err)
		}

		cleanupAppConnections(context.Background(), client, config.ApplicationName)

		// Schema is managed by Supabase migrations - no setup required

		// Create the cache
		dbCache := cache.NewInMemoryCache()

		return &DB{client: client, config: config, Cache: dbCache}, nil
	}

	// Fallback to individual environment variables
	maxOpen, maxIdle := poolLimitsForEnv(os.Getenv("APP_ENV"))

	appName := determineApplicationName()

	config := &Config{
		Host:            os.Getenv("POSTGRES_HOST"),
		Port:            os.Getenv("POSTGRES_PORT"),
		User:            os.Getenv("POSTGRES_USER"),
		Password:        os.Getenv("POSTGRES_PASSWORD"),
		Database:        os.Getenv("POSTGRES_DB"),
		SSLMode:         os.Getenv("POSTGRES_SSL_MODE"),
		MaxIdleConns:    maxIdle,
		MaxOpenConns:    maxOpen,
		MaxLifetime:     5 * time.Minute,
		ApplicationName: appName,
	}

	// Use defaults if not set — local Supabase runs on port 54322
	// with database "postgres"; production uses standard port 5432.
	isDev := os.Getenv("APP_ENV") == "" || os.Getenv("APP_ENV") == "development"
	if config.Host == "" {
		config.Host = "localhost"
	}
	if config.Port == "" {
		if isDev {
			config.Port = "54322"
		} else {
			config.Port = "5432"
		}
	}
	if config.User == "" {
		config.User = "postgres"
	}
	if config.Password == "" && isDev {
		config.Password = "postgres"
	}
	if config.Database == "" {
		if isDev {
			config.Database = "postgres"
		} else {
			config.Database = "hover"
		}
	}

	// Create the database connection
	db, err := New(config)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// createCoreTables creates all core database tables

// Close closes the database connection
func (db *DB) Close() error {
	return db.client.Close()
}

// GetDB returns the underlying database connection
func (db *DB) GetDB() *sql.DB {
	return db.client
}

// GetDomainNameByID retrieves a single domain name by ID
func (db *DB) GetDomainNameByID(ctx context.Context, domainID int) (string, error) {
	var domainName string
	err := db.client.QueryRowContext(ctx, `SELECT name FROM domains WHERE id = $1`, domainID).Scan(&domainName)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("domain not found")
		}
		return "", fmt.Errorf("failed to get domain name: %w", err)
	}
	return domainName, nil
}

// GetDomainNames retrieves domain names for multiple domain IDs in a single query
// Returns a map of domainID -> domainName
func (db *DB) GetDomainNames(ctx context.Context, domainIDs []int) (map[int]string, error) {
	if len(domainIDs) == 0 {
		return make(map[int]string), nil
	}

	query := `SELECT id, name FROM domains WHERE id = ANY($1)`
	rows, err := db.client.QueryContext(ctx, query, domainIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to query domain names: %w", err)
	}
	defer rows.Close()

	domainNames := make(map[int]string)
	for rows.Next() {
		var domainID int
		var domainName string
		if err := rows.Scan(&domainID, &domainName); err != nil {
			dbLog.Warn("Failed to scan domain row", "error", err)
			continue
		}
		domainNames[domainID] = domainName
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating domain rows: %w", err)
	}

	return domainNames, nil
}

// UpdateDomainTechnologies updates the detected technologies for a domain.
// Called after first successful task crawl in a job to store tech detection results.
func (db *DB) UpdateDomainTechnologies(ctx context.Context, domainID int, technologies, headers []byte, htmlPath string) error {
	if domainID <= 0 {
		return fmt.Errorf("invalid domain ID: %d", domainID)
	}

	// Validate JSON for JSONB columns
	if len(technologies) > 0 && !json.Valid(technologies) {
		return fmt.Errorf("technologies parameter is not valid JSON")
	}
	if len(headers) > 0 && !json.Valid(headers) {
		return fmt.Errorf("headers parameter is not valid JSON")
	}

	query := `
		UPDATE domains
		SET technologies = $2,
			tech_headers = $3,
			tech_html_path = $4,
			tech_detected_at = NOW()
		WHERE id = $1`

	result, err := db.client.ExecContext(ctx, query, domainID, technologies, headers, htmlPath)
	if err != nil {
		return fmt.Errorf("failed to update domain technologies: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to verify update: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("domain with ID %d not found", domainID)
	}

	return nil
}

// ResetDataOnly clears all data from tables but preserves the schema.
// This is the safe option for clearing test data without triggering schema changes.
func (db *DB) ResetDataOnly() error {
	startTime := time.Now()
	dbLog.Warn("=== DATA-ONLY RESET STARTED ===")
	dbLog.Warn("Clearing all data from database tables (schema preserved)")

	// Use TRUNCATE instead of DELETE - it's atomic, faster, and handles concurrent access better
	// TRUNCATE automatically handles foreign key constraints with CASCADE.
	//
	// Anything that holds operational state (jobs, tasks, usage counters,
	// integration connections) is cleared. users, organisations,
	// organisation_members and plans are deliberately preserved so the
	// admin operator stays signed in and reference data survives.
	tables := []string{
		"tasks",
		"jobs",
		"job_share_links",
		"schedulers",
		"pages",
		"domains",
		"page_analytics",
		"domain_hosts",
		"notifications",
		"daily_usage",
		"organisation_domains",
		"platform_org_mappings",
		"google_analytics_accounts",
		"google_analytics_connections",
		"slack_connections",
		"slack_user_links",
		"webflow_connections",
		"webflow_site_settings",
	}
	totalRowsDeleted := int64(0)

	dbLog.Info("Step 1/2: Truncating all data from tables")
	for i, table := range tables {
		tableStart := time.Now()
		dbLog.Info("Truncating table data",
			"table", table,
			"table_num", i+1,
			"total_tables", len(tables))

		// Get row count before truncate (warn if it fails but keep going)
		var rowCount int64
		if err := db.client.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&rowCount); err != nil {
			dbLog.Warn("Failed to count rows before truncate", "error", err, "table", table)
		}

		// TRUNCATE is atomic and releases all locks immediately
		_, err := db.client.Exec(fmt.Sprintf(`TRUNCATE TABLE %s CASCADE`, table))
		if err != nil {
			dbLog.Error("FAILED to truncate table - reset aborted",
				"error", err,
				"table", table,
				"elapsed", time.Since(tableStart))
			return fmt.Errorf("failed to truncate table %s: %w", table, err)
		}

		totalRowsDeleted += rowCount
		dbLog.Info("Truncated table data successfully",
			"table", table,
			"rows_deleted", rowCount,
			"duration", time.Since(tableStart))
	}

	dbLog.Info("Step 1/2 completed: All table data cleared",
		"total_rows_deleted", totalRowsDeleted,
		"step_duration", time.Since(startTime))

	// Reset sequences to start from 1 again
	sequenceStart := time.Now()
	dbLog.Info("Step 2/2: Resetting sequences")
	sequences := []struct {
		name  string
		table string
	}{
		{"domains_id_seq", "domains"},
		{"pages_id_seq", "pages"},
	}

	sequencesReset := 0
	for _, seq := range sequences {
		_, err := db.client.Exec(fmt.Sprintf(`ALTER SEQUENCE %s RESTART WITH 1`, seq.name))
		if err != nil {
			dbLog.Warn("Failed to reset sequence (may not exist)", "error", err, "sequence", seq.name)
		} else {
			sequencesReset++
			dbLog.Info("Reset sequence to 1", "sequence", seq.name)
		}
	}

	dbLog.Info("Step 2/2 completed: Sequences reset",
		"sequences_reset", sequencesReset,
		"sequences_total", len(sequences),
		"step_duration", time.Since(sequenceStart))

	totalDuration := time.Since(startTime)
	dbLog.Warn("=== DATA-ONLY RESET COMPLETED SUCCESSFULLY ===",
		"total_duration", totalDuration,
		"total_rows_deleted", totalRowsDeleted)
	dbLog.Warn("Schema preserved - no migrations affected")

	return nil
}

// RecalculateJobStats recalculates all statistics for a job based on actual task records
func (db *DB) RecalculateJobStats(ctx context.Context, jobID string) error {
	_, err := db.client.ExecContext(ctx, `SELECT recalculate_job_stats($1)`, jobID)
	if err != nil {
		return fmt.Errorf("failed to recalculate job stats: %w", err)
	}
	return nil
}

// Serialise converts data to JSON string representation.
// It is named with British English spelling for consistency.
func Serialise(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		dbLog.Error("Failed to serialise data", "error", err)
		return "{}"
	}
	return string(data)
}

// UpsertPageWithAnalytics creates or updates a page and its org-scoped analytics data
// Stores GA4 page view data in the page_analytics table tied to the organisation
func (db *DB) UpsertPageWithAnalytics(
	ctx context.Context,
	organisationID string,
	domainID int,
	path string,
	pageViews map[string]int64,
	connectionID string,
) (int, error) {
	// Create/get page_id in pages table
	pageQuery := `
		INSERT INTO pages (domain_id, host, path, created_at)
		VALUES ($1, (SELECT name FROM domains WHERE id = $1), $2, NOW())
		ON CONFLICT (domain_id, host, path)
		DO UPDATE SET domain_id = EXCLUDED.domain_id
		RETURNING id
	`

	var pageID int
	err := db.client.QueryRowContext(ctx, pageQuery, domainID, path).Scan(&pageID)
	if err != nil {
		return 0, fmt.Errorf("failed to upsert page: %w", err)
	}

	// Upsert analytics data to page_analytics table (org-scoped to prevent data leaking)
	analyticsQuery := `
		INSERT INTO page_analytics (
			organisation_id, domain_id, path,
			page_views_7d, page_views_28d, page_views_180d,
			ga_connection_id, fetched_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (organisation_id, domain_id, path)
		DO UPDATE SET
			page_views_7d = EXCLUDED.page_views_7d,
			page_views_28d = EXCLUDED.page_views_28d,
			page_views_180d = EXCLUDED.page_views_180d,
			ga_connection_id = EXCLUDED.ga_connection_id,
			fetched_at = NOW(),
			updated_at = NOW()
	`

	// Extract page view values with defaults
	pageViews7d := pageViews["7d"]
	pageViews28d := pageViews["28d"]
	pageViews180d := pageViews["180d"]

	// Handle nullable connection ID
	var connID any
	if connectionID != "" {
		connID = connectionID
	}

	_, err = db.client.ExecContext(ctx, analyticsQuery,
		organisationID, domainID, path,
		pageViews7d, pageViews28d, pageViews180d,
		connID,
	)
	if err != nil {
		// Log but don't fail - page was created successfully
		dbLog.Warn("Failed to upsert page analytics data; continuing without analytics",
			"error", err,
			"organisation_id", organisationID,
			"domain_id", domainID,
			"next_action", "continuing_without_analytics")
	}

	return pageID, nil
}

// CalculateTrafficScores calculates and stores traffic scores for all pages in a domain
// using a log-scaled view curve (28-day). This emphasises high-traffic pages while
// keeping the long tail near the floor.
// Score range: 0 (0-1 views) then 0.10 (floor) to 0.99 (ceiling).
func (db *DB) CalculateTrafficScores(ctx context.Context, organisationID string, domainID int) error {
	// Calculate log-scaled scores in a single query.
	query := `
		WITH stats AS (
			SELECT
				id,
				COALESCE(page_views_28d, 0) AS page_views_28d,
				MIN(COALESCE(page_views_28d, 0)) OVER () AS min_views,
				MAX(COALESCE(page_views_28d, 0)) OVER () AS max_views
			FROM page_analytics
			WHERE organisation_id = $1 AND domain_id = $2
		)
		UPDATE page_analytics pa
		SET traffic_score = CASE
			WHEN s.page_views_28d <= 1 THEN 0
			WHEN s.max_views = 0 THEN 0.10
			WHEN s.max_views = s.min_views THEN 0.10
			ELSE 0.10 + 0.89 * (
				LN(s.page_views_28d + 1) - LN(s.min_views + 1)
			) / NULLIF(
				LN(s.max_views + 1) - LN(s.min_views + 1),
				0
			)
		END,
		updated_at = NOW()
		FROM stats s
		WHERE pa.id = s.id
	`

	result, err := db.client.ExecContext(ctx, query, organisationID, domainID)
	if err != nil {
		return fmt.Errorf("failed to calculate traffic scores: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	dbLog.Info("Calculated traffic scores for domain",
		"organisation_id", organisationID,
		"domain_id", domainID,
		"pages_updated", rowsAffected)

	return nil
}

// ApplyTrafficScoresToTasks updates pending tasks using traffic scores for a domain.
// This ensures existing tasks are reprioritised once analytics are available.
func (db *DB) ApplyTrafficScoresToTasks(ctx context.Context, organisationID string, domainID int) error {
	query := `
		UPDATE tasks t
		SET priority_score = GREATEST(t.priority_score, COALESCE(pa.traffic_score, 0))
		FROM pages p
		JOIN jobs j ON t.job_id = j.id
		LEFT JOIN page_analytics pa ON pa.organisation_id = j.organisation_id
			AND pa.domain_id = p.domain_id
			AND pa.path = p.path
		WHERE j.organisation_id = $1
		AND p.domain_id = $2
		AND t.page_id = p.id
		AND t.status IN ('pending', 'waiting')
		AND COALESCE(pa.traffic_score, 0) > t.priority_score
	`

	result, err := db.client.ExecContext(ctx, query, organisationID, domainID)
	if err != nil {
		return fmt.Errorf("failed to apply traffic scores to tasks: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		dbLog.Info("Applied traffic scores to pending tasks",
			"organisation_id", organisationID,
			"domain_id", domainID,
			"tasks_updated", rowsAffected)
	} else {
		dbLog.Debug("No pending tasks to reprioritise",
			"organisation_id", organisationID,
			"domain_id", domainID)
	}

	return nil
}

// GetOrCreateDomainID retrieves or creates a domain ID for a given domain name
// Uses INSERT ... ON CONFLICT to handle concurrent creation atomically
func (db *DB) GetOrCreateDomainID(ctx context.Context, domain string) (int, error) {
	var domainID int

	// Use upsert to handle concurrent creation atomically
	query := `
		INSERT INTO domains (name, created_at)
		VALUES ($1, NOW())
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`
	err := db.client.QueryRowContext(ctx, query, domain).Scan(&domainID)
	if err != nil {
		return 0, fmt.Errorf("failed to get or create domain: %w", err)
	}

	return domainID, nil
}

// UpsertOrganisationDomain ensures a domain is associated with an organisation.
func (db *DB) UpsertOrganisationDomain(ctx context.Context, organisationID string, domainID int) error {
	query := `
		INSERT INTO organisation_domains (organisation_id, domain_id, created_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (organisation_id, domain_id) DO NOTHING
	`
	if _, err := db.client.ExecContext(ctx, query, organisationID, domainID); err != nil {
		return fmt.Errorf("failed to upsert organisation domain: %w", err)
	}
	return nil
}

// OrganisationDomain represents a domain belonging to an organisation
type OrganisationDomain struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GetDomainsForOrganisation returns all domains that belong to the organisation
// This includes domains from jobs created by any user in the organisation
func (db *DB) GetDomainsForOrganisation(ctx context.Context, organisationID string) ([]OrganisationDomain, error) {
	query := `
		SELECT DISTINCT id, name
		FROM (
			SELECT d.id, d.name
			FROM domains d
			JOIN organisation_domains od ON od.domain_id = d.id
			WHERE od.organisation_id = $1
			UNION
			SELECT d.id, d.name
			FROM domains d
			JOIN jobs j ON j.domain_id = d.id
			WHERE j.organisation_id = $1
		) AS org_domains
		ORDER BY name ASC
	`

	rows, err := db.client.QueryContext(ctx, query, organisationID)
	if err != nil {
		return nil, fmt.Errorf("failed to query organisation domains: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			dbLog.Error("Failed to close rows", "error", closeErr, "organisation_id", organisationID)
		}
	}()

	var domains []OrganisationDomain
	for rows.Next() {
		var domain OrganisationDomain
		if err := rows.Scan(&domain.ID, &domain.Name); err != nil {
			return nil, fmt.Errorf("failed to scan domain: %w", err)
		}
		domains = append(domains, domain)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating domains: %w", err)
	}

	return domains, nil
}

// redactURL strips credentials from a connection URL before logging.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	u.User = nil
	return u.String()
}
