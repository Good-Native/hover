package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/auth"
	"github.com/Harvey-AU/hover/internal/broker"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/jobs"
	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/Harvey-AU/hover/internal/loops"
	stripe "github.com/stripe/stripe-go/v82"
)

// Set via ldflags at build time.
var Version = "0.4.1"

func buildConfigSnippet() ([]byte, error) {
	appEnv := os.Getenv("APP_ENV")
	authURL := strings.TrimSuffix(os.Getenv("SUPABASE_AUTH_URL"), "/")
	if authURL == "" {
		legacyURL := strings.TrimSuffix(os.Getenv("SUPABASE_URL"), "/")
		if legacyURL != "" {
			apiLog.Warn("SUPABASE_AUTH_URL missing; using legacy SUPABASE_URL fallback")
			authURL = legacyURL
		}
	}
	if authURL == "" {
		return nil, fmt.Errorf("SUPABASE_AUTH_URL not set")
	}
	parsedURL, err := url.ParseRequestURI(authURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SUPABASE_AUTH_URL: %w", err)
	}
	if appEnv == "production" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("SUPABASE_AUTH_URL must use https in production")
	}
	key := os.Getenv("SUPABASE_PUBLISHABLE_KEY")
	if key == "" {
		legacyKey := os.Getenv("SUPABASE_ANON_KEY")
		if legacyKey != "" {
			apiLog.Warn("SUPABASE_PUBLISHABLE_KEY missing; using legacy SUPABASE_ANON_KEY fallback")
			key = legacyKey
		}
	}
	if key == "" {
		return nil, fmt.Errorf("SUPABASE_PUBLISHABLE_KEY not set")
	}
	validKey := false
	for _, prefix := range []string{"sb_", "sbp_", "eyJ", "public-"} {
		if strings.HasPrefix(key, prefix) {
			validKey = true
			break
		}
	}
	if !validKey {
		apiLog.Warn("SUPABASE_PUBLISHABLE_KEY has unexpected format; proceeding anyway")
	}
	config := map[string]any{
		"supabaseUrl":     authURL,
		"supabaseAnonKey": key,
	}
	if appEnv != "" {
		config["environment"] = appEnv
	}
	if raw := os.Getenv("GNH_ENABLE_TURNSTILE"); raw != "" {
		if enabled, err := strconv.ParseBool(raw); err == nil {
			config["enableTurnstile"] = enabled
		} else {
			apiLog.Warn("invalid GNH_ENABLE_TURNSTILE value; falling back to default", "value", raw)
		}
	} else {
		config["enableTurnstile"] = appEnv == "production"
	}
	bytes, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}
	return fmt.Appendf(nil, "window.GNH_CONFIG=%s;", bytes), nil
}

type DBClient interface {
	GetDB() *sql.DB
	GetOrCreateUser(userID, email string, orgID *string) (*db.User, error)
	GetJobStats(organisationID string, startDate, endDate *time.Time) (*db.JobStats, error)
	GetJobActivity(organisationID string, startDate, endDate *time.Time) ([]db.ActivityPoint, error)
	GetSlowPages(organisationID string, startDate, endDate *time.Time) ([]db.SlowPage, error)
	GetExternalRedirects(organisationID string, startDate, endDate *time.Time) ([]db.ExternalRedirect, error)
	GetUserByWebhookToken(token string) (*db.User, error)
	GetUser(userID string) (*db.User, error)
	UpdateUserNames(userID string, firstName, lastName, fullName *string) error
	ResetSchema() error
	ResetDataOnly() error
	CreateUser(userID, email string, firstName, lastName, fullName *string, orgName string) (*db.User, *db.Organisation, error)
	GetOrganisation(organisationID string) (*db.Organisation, error)
	ListJobs(organisationID string, limit, offset int, status, dateRange, timezone string) ([]db.JobWithDomain, int, error)
	ListJobsWithOffset(organisationID string, limit, offset int, status, dateRange string, tzOffsetMinutes int, includeStats bool) ([]db.JobWithDomain, int, error)
	CreateScheduler(ctx context.Context, scheduler *db.Scheduler) error
	GetScheduler(ctx context.Context, schedulerID string) (*db.Scheduler, error)
	ListSchedulers(ctx context.Context, organisationID string) ([]*db.Scheduler, error)
	UpdateScheduler(ctx context.Context, schedulerID string, updates *db.Scheduler, expectedIsEnabled *bool) error
	DeleteScheduler(ctx context.Context, schedulerID string) error
	GetSchedulersReadyToRun(ctx context.Context, limit int) ([]*db.Scheduler, error)
	UpdateSchedulerNextRun(ctx context.Context, schedulerID string, nextRun time.Time) error
	GetLastJobStartTimeForScheduler(ctx context.Context, schedulerID string) (*time.Time, error)
	GetDomainNameByID(ctx context.Context, domainID int) (string, error)
	GetDomainNames(ctx context.Context, domainIDs []int) (map[int]string, error)
	ListUserOrganisations(userID string) ([]db.UserOrganisation, error)
	ValidateOrganisationMembership(userID, organisationID string) (bool, error)
	SetActiveOrganisation(userID, organisationID string) error
	GetEffectiveOrganisationID(user *db.User) string
	GetOrganisationMemberRole(ctx context.Context, userID, organisationID string) (string, error)
	ListOrganisationMembers(ctx context.Context, organisationID string) ([]db.OrganisationMember, error)
	IsOrganisationMemberEmail(ctx context.Context, organisationID, email string) (bool, error)
	RemoveOrganisationMember(ctx context.Context, userID, organisationID string) error
	UpdateOrganisationMemberRole(ctx context.Context, userID, organisationID, role string) error
	CountOrganisationAdmins(ctx context.Context, organisationID string) (int, error)
	CreateOrganisation(name string) (*db.Organisation, error)
	CreateOrganisationForUser(userID, name string) (*db.Organisation, error)
	AddOrganisationMember(userID, organisationID, role string) error
	CreateOrganisationInvite(ctx context.Context, invite *db.OrganisationInvite) (*db.OrganisationInvite, error)
	ListOrganisationInvites(ctx context.Context, organisationID string) ([]db.OrganisationInvite, error)
	RevokeOrganisationInvite(ctx context.Context, inviteID, organisationID string) error
	GetOrganisationInviteByToken(ctx context.Context, token string) (*db.OrganisationInvite, error)
	AcceptOrganisationInvite(ctx context.Context, token, userID string) (*db.OrganisationInvite, error)
	SetOrganisationPlan(ctx context.Context, organisationID, planID string) error
	GetOrganisationPlanID(ctx context.Context, organisationID string) (string, error)
	ListDailyUsage(ctx context.Context, organisationID string, startDate, endDate time.Time) ([]db.DailyUsageEntry, error)
	CreateSlackConnection(ctx context.Context, conn *db.SlackConnection) error
	GetSlackConnection(ctx context.Context, connectionID string) (*db.SlackConnection, error)
	ListSlackConnections(ctx context.Context, organisationID string) ([]*db.SlackConnection, error)
	DeleteSlackConnection(ctx context.Context, connectionID, organisationID string) error
	CreateSlackUserLink(ctx context.Context, link *db.SlackUserLink) error
	GetSlackUserLink(ctx context.Context, userID, connectionID string) (*db.SlackUserLink, error)
	UpdateSlackUserLinkNotifications(ctx context.Context, userID, connectionID string, dmNotifications bool) error
	DeleteSlackUserLink(ctx context.Context, userID, connectionID string) error
	StoreSlackToken(ctx context.Context, connectionID, token string) error
	GetSlackToken(ctx context.Context, connectionID string) (string, error)
	ListNotifications(ctx context.Context, organisationID string, limit, offset int, unreadOnly bool) ([]*db.Notification, int, error)
	GetUnreadNotificationCount(ctx context.Context, organisationID string) (int, error)
	MarkNotificationRead(ctx context.Context, notificationID, organisationID string) error
	MarkAllNotificationsRead(ctx context.Context, organisationID string) error
	CreateWebflowConnection(ctx context.Context, conn *db.WebflowConnection) error
	GetWebflowConnection(ctx context.Context, connectionID string) (*db.WebflowConnection, error)
	ListWebflowConnections(ctx context.Context, organisationID string) ([]*db.WebflowConnection, error)
	DeleteWebflowConnection(ctx context.Context, connectionID, organisationID string) error
	StoreWebflowToken(ctx context.Context, connectionID, token string) error
	GetWebflowToken(ctx context.Context, connectionID string) (string, error)
	CreateGoogleConnection(ctx context.Context, conn *db.GoogleAnalyticsConnection) error
	GetGoogleConnection(ctx context.Context, connectionID string) (*db.GoogleAnalyticsConnection, error)
	ListGoogleConnections(ctx context.Context, organisationID string) ([]*db.GoogleAnalyticsConnection, error)
	DeleteGoogleConnection(ctx context.Context, connectionID, organisationID string) error
	UpdateGoogleConnectionStatus(ctx context.Context, connectionID, organisationID, status string) error
	StoreGoogleToken(ctx context.Context, connectionID, refreshToken string) error
	GetGoogleToken(ctx context.Context, connectionID string) (string, error)
	GetActiveGAConnectionForOrganisation(ctx context.Context, orgID string) (*db.GoogleAnalyticsConnection, error)
	GetActiveGAConnectionForDomain(ctx context.Context, organisationID string, domainID int) (*db.GoogleAnalyticsConnection, error)
	GetDomainsForOrganisation(ctx context.Context, organisationID string) ([]db.OrganisationDomain, error)
	UpdateConnectionLastSync(ctx context.Context, connectionID string) error
	UpdateConnectionDomains(ctx context.Context, connectionID string, domainIDs []int) error
	MarkConnectionInactive(ctx context.Context, connectionID, reason string) error
	UpsertPageWithAnalytics(ctx context.Context, organisationID string, domainID int, path string, pageViews map[string]int64, connectionID string) (int, error)
	CalculateTrafficScores(ctx context.Context, organisationID string, domainID int) error
	ApplyTrafficScoresToTasks(ctx context.Context, organisationID string, domainID int) error
	GetOrCreateDomainID(ctx context.Context, domain string) (int, error)
	UpsertOrganisationDomain(ctx context.Context, organisationID string, domainID int) error
	UpsertGA4Account(ctx context.Context, account *db.GoogleAnalyticsAccount) error
	ListGA4Accounts(ctx context.Context, organisationID string) ([]*db.GoogleAnalyticsAccount, error)
	GetGA4Account(ctx context.Context, accountID string) (*db.GoogleAnalyticsAccount, error)
	GetGA4AccountByGoogleID(ctx context.Context, organisationID, googleAccountID string) (*db.GoogleAnalyticsAccount, error)
	StoreGA4AccountToken(ctx context.Context, accountID, refreshToken string) error
	GetGA4AccountToken(ctx context.Context, accountID string) (string, error)
	GetGA4AccountWithToken(ctx context.Context, organisationID string) (*db.GoogleAnalyticsAccount, error)
	GetGAConnectionWithToken(ctx context.Context, organisationID string) (*db.GoogleAnalyticsConnection, error)
	UpsertPlatformOrgMapping(ctx context.Context, mapping *db.PlatformOrgMapping) error
	GetPlatformOrgMapping(ctx context.Context, platform, platformID string) (*db.PlatformOrgMapping, error)
	GetOrganisationUsageStats(ctx context.Context, orgID string) (*db.UsageStats, error)
	GetActivePlans(ctx context.Context) ([]db.Plan, error)
	// Stripe billing methods
	SetStripeCustomerID(ctx context.Context, organisationID, customerID string) error
	GetStripeCustomerID(ctx context.Context, organisationID string) (string, error)
	GetOrganisationIDByStripeCustomerID(ctx context.Context, customerID string) (string, error)
	SetStripeSubscriptionID(ctx context.Context, organisationID, subscriptionID string) error
	GetStripeSubscriptionID(ctx context.Context, organisationID string) (string, error)
	GetPlanByStripePriceID(ctx context.Context, priceID string) (*db.Plan, error)
	GetFreePlanID(ctx context.Context) (string, error)
	// Webflow site settings methods
	CreateOrUpdateSiteSetting(ctx context.Context, setting *db.WebflowSiteSetting) error
	GetSiteSetting(ctx context.Context, organisationID, webflowSiteID string) (*db.WebflowSiteSetting, error)
	GetSiteSettingByID(ctx context.Context, id string) (*db.WebflowSiteSetting, error)
	ListConfiguredSiteSettings(ctx context.Context, organisationID string) ([]*db.WebflowSiteSetting, error)
	ListAllSiteSettings(ctx context.Context, organisationID string) ([]*db.WebflowSiteSetting, error)
	ListSiteSettingsByConnection(ctx context.Context, connectionID string) ([]*db.WebflowSiteSetting, error)
	UpdateSiteSchedule(ctx context.Context, organisationID, webflowSiteID string, scheduleIntervalHours *int, schedulerID string) error
	UpdateSiteAutoPublish(ctx context.Context, organisationID, webflowSiteID string, enabled bool, webhookID string) error
	DeleteSiteSetting(ctx context.Context, organisationID, webflowSiteID string) error
	DeleteSiteSettingsByConnection(ctx context.Context, connectionID string) error
}

type BrokerCleaner interface {
	ClearAll(ctx context.Context) (int, error)
	ReclaimTerminalJobKeys(ctx context.Context, filter broker.TerminalFilter) (broker.ReclaimReport, error)
}

type Handler struct {
	DB                   DBClient
	JobsManager          jobs.JobManagerInterface
	Loops                *loops.Client
	Broker               BrokerCleaner
	GoogleClientID       string
	GoogleClientSecret   string
	StripeSecretKey      string
	StripeWebhookSecret  string
	StripePublishableKey string
	SettingsURL          string
}

// broker may be nil when Redis is not configured — admin reset endpoints skip the Redis clear in that case.
func NewHandler(pgDB DBClient, jobsManager jobs.JobManagerInterface, loopsClient *loops.Client, broker BrokerCleaner, googleClientID, googleClientSecret, stripeSecretKey, stripeWebhookSecret, stripePublishableKey, settingsURL string) *Handler {
	// Set the Stripe API key once at startup to avoid concurrent global writes.
	stripe.Key = stripeSecretKey
	return &Handler{
		DB:                   pgDB,
		JobsManager:          jobsManager,
		Loops:                loopsClient,
		Broker:               broker,
		GoogleClientID:       googleClientID,
		GoogleClientSecret:   googleClientSecret,
		StripeSecretKey:      stripeSecretKey,
		StripeWebhookSecret:  stripeWebhookSecret,
		StripePublishableKey: stripePublishableKey,
		SettingsURL:          settingsURL,
	}
}

// Writes the error response and returns "" on failure.
func (h *Handler) GetActiveOrganisation(w http.ResponseWriter, r *http.Request) string {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return ""
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		InternalError(w, r, err)
		return ""
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return ""
	}

	return orgID
}

func (h *Handler) GetActiveOrganisationWithUser(w http.ResponseWriter, r *http.Request) (*db.User, string, bool) {
	userClaims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "User information not found")
		return nil, "", false
	}

	user, err := h.DB.GetOrCreateUser(userClaims.UserID, userClaims.Email, nil)
	if err != nil {
		InternalError(w, r, err)
		return nil, "", false
	}

	orgID := h.DB.GetEffectiveOrganisationID(user)
	if orgID == "" {
		BadRequest(w, r, "User must belong to an organisation")
		return nil, "", false
	}

	return user, orgID, true
}

func (h *Handler) SetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", h.HealthCheck)
	mux.HandleFunc("/health/db", h.DatabaseHealthCheck)

	mux.Handle("/v1/jobs", auth.AuthMiddleware(http.HandlerFunc(h.JobsHandler)))
	mux.Handle("/v1/jobs/", auth.AuthMiddleware(http.HandlerFunc(h.JobHandler)))
	mux.Handle("/v1/schedulers", auth.AuthMiddleware(http.HandlerFunc(h.SchedulersHandler)))
	mux.Handle("/v1/schedulers/", auth.AuthMiddleware(http.HandlerFunc(h.SchedulerHandler)))
	mux.HandleFunc("/v1/shared/jobs/", h.SharedJobHandler)
	mux.HandleFunc("/shared/jobs/", h.ServeSharedJobPage)

	mux.Handle("/v1/dashboard/stats", auth.AuthMiddleware(http.HandlerFunc(h.DashboardStats)))
	mux.Handle("/v1/dashboard/activity", auth.AuthMiddleware(http.HandlerFunc(h.DashboardActivity)))
	mux.Handle("/v1/dashboard/slow-pages", auth.AuthMiddleware(http.HandlerFunc(h.DashboardSlowPages)))
	mux.Handle("/v1/dashboard/external-redirects", auth.AuthMiddleware(http.HandlerFunc(h.DashboardExternalRedirects)))

	mux.Handle("/v1/metadata/metrics", auth.AuthMiddleware(http.HandlerFunc(h.MetadataHandler)))

	mux.HandleFunc("/v1/auth/register", h.AuthRegister)
	mux.HandleFunc("/v1/auth/session", h.AuthSession)

	mux.Handle("/v1/auth/profile", auth.AuthMiddleware(http.HandlerFunc(h.AuthProfile)))

	mux.HandleFunc("/v1/organisations/invites/preview", h.OrganisationInvitePreviewHandler)
	mux.Handle("/v1/organisations", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationsHandler)))
	mux.Handle("/v1/organisations/switch", auth.AuthMiddleware(http.HandlerFunc(h.SwitchOrganisationHandler)))
	mux.Handle("/v1/organisations/members", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationMembersHandler)))
	mux.Handle("/v1/organisations/members/", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationMemberHandler)))
	mux.Handle("/v1/organisations/invites/accept", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationInviteAcceptHandler)))
	mux.Handle("/v1/organisations/invites", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationInvitesHandler)))
	mux.Handle("/v1/organisations/invites/", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationInviteHandler)))
	mux.Handle("/v1/organisations/plan", auth.AuthMiddleware(http.HandlerFunc(h.OrganisationPlanHandler)))

	mux.Handle("/v1/domains", auth.AuthMiddleware(http.HandlerFunc(h.DomainsHandler)))

	mux.Handle("/v1/usage", auth.AuthMiddleware(http.HandlerFunc(h.UsageHandler)))
	mux.Handle("/v1/usage/history", auth.AuthMiddleware(http.HandlerFunc(h.UsageHistoryHandler)))

	mux.Handle("/v1/plans", http.HandlerFunc(h.PlansHandler))

	mux.HandleFunc("/v1/webhooks/webflow/", h.WebflowWebhook)
	mux.HandleFunc("/v1/webhooks/stripe", h.StripeWebhook)

	mux.Handle("/v1/billing/checkout", auth.AuthMiddleware(http.HandlerFunc(h.BillingCheckout)))
	mux.Handle("/v1/billing/portal", auth.AuthMiddleware(http.HandlerFunc(h.BillingPortal)))

	mux.Handle("/v1/integrations/slack", auth.AuthMiddleware(http.HandlerFunc(h.SlackConnectionsHandler)))
	mux.Handle("/v1/integrations/slack/", auth.AuthMiddleware(http.HandlerFunc(h.SlackConnectionHandler)))
	mux.HandleFunc("/v1/integrations/slack/callback", h.SlackOAuthCallback)

	mux.Handle("/v1/integrations/webflow", auth.AuthMiddleware(http.HandlerFunc(h.WebflowConnectionsHandler)))
	mux.HandleFunc("/v1/integrations/webflow/callback", h.HandleWebflowOAuthCallback)
	// sites/ must precede the catch-all webflow/ route.
	mux.Handle("/v1/integrations/webflow/sites/", auth.AuthMiddleware(http.HandlerFunc(h.webflowSitesRouter)))
	mux.Handle("/v1/integrations/webflow/", auth.AuthMiddleware(http.HandlerFunc(h.WebflowConnectionHandler)))

	mux.Handle("/v1/integrations/google", auth.AuthMiddleware(http.HandlerFunc(h.GoogleConnectionsHandler)))
	mux.Handle("/v1/integrations/google/", auth.AuthMiddleware(http.HandlerFunc(h.GoogleConnectionHandler)))
	mux.HandleFunc("/v1/integrations/google/callback", h.HandleGoogleOAuthCallback)
	mux.Handle("/v1/integrations/google/save-property", auth.AuthMiddleware(http.HandlerFunc(h.SaveGoogleProperty)))

	mux.Handle("/v1/notifications", auth.AuthMiddleware(http.HandlerFunc(h.NotificationsHandler)))
	mux.Handle("/v1/notifications/read-all", auth.AuthMiddleware(http.HandlerFunc(h.NotificationsReadAllHandler)))
	mux.Handle("/v1/notifications/", auth.AuthMiddleware(http.HandlerFunc(h.NotificationHandler)))

	mux.Handle("/v1/admin/reset-db", auth.AuthMiddleware(http.HandlerFunc(h.AdminResetDatabase)))
	mux.Handle("/v1/admin/reset-data", auth.AuthMiddleware(http.HandlerFunc(h.AdminResetData)))
	mux.Handle("/v1/admin/reclaim-redis", auth.AuthMiddleware(http.HandlerFunc(h.AdminReclaimRedis)))

	pprofProtected := func(handler http.Handler) http.Handler {
		return auth.AuthMiddleware(requireSystemAdmin(handler))
	}
	mux.Handle("/debug/pprof/", pprofProtected(http.HandlerFunc(pprof.Index)))
	mux.Handle("/debug/pprof/cmdline", pprofProtected(http.HandlerFunc(pprof.Cmdline)))
	mux.Handle("/debug/pprof/profile", pprofProtected(http.HandlerFunc(pprof.Profile)))
	mux.Handle("/debug/pprof/symbol", pprofProtected(http.HandlerFunc(pprof.Symbol)))
	mux.Handle("/debug/pprof/trace", pprofProtected(http.HandlerFunc(pprof.Trace)))
	mux.Handle("/debug/pprof/heap", pprofProtected(pprof.Handler("heap")))
	mux.Handle("/debug/pprof/goroutine", pprofProtected(pprof.Handler("goroutine")))
	mux.Handle("/debug/pprof/threadcreate", pprofProtected(pprof.Handler("threadcreate")))
	mux.Handle("/debug/pprof/block", pprofProtected(pprof.Handler("block")))
	mux.Handle("/debug/pprof/mutex", pprofProtected(pprof.Handler("mutex")))

	// Sandboxed preview browsers (e.g. Claude app) cannot reach Supabase directly; this endpoint injects a session server-side.
	if os.Getenv("APP_ENV") == "development" {
		mux.HandleFunc("/dev/auto-login", h.DevAutoLogin)
	}

	mux.HandleFunc("/debug/fgtrace", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open("trace.out")
		if err != nil {
			http.Error(w, "could not open trace file", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		logger := loggerWithRequest(r)

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="trace.out"`)
		if _, err := io.Copy(w, f); err != nil {
			logger.Error("Failed to copy trace file", "error", err)
		}
	}))

	mux.HandleFunc("/", h.ServeHomepage)
	mux.HandleFunc("/config.js", h.ServeConfigJS)
	mux.HandleFunc("/test-login.html", h.ServeTestLogin)
	mux.HandleFunc("/test-components.html", h.ServeTestComponents)
	mux.HandleFunc("/test-data-components.html", h.ServeTestDataComponents)
	mux.HandleFunc("/dashboard", h.ServeDashboard)
	mux.HandleFunc("/dashboard-new", h.ServeNewDashboard)
	mux.HandleFunc("/welcome", h.ServeWelcome)
	mux.HandleFunc("/welcome/", h.ServeWelcome)
	mux.HandleFunc("/settings", h.ServeSettings)
	mux.HandleFunc("/settings/", h.ServeSettings)
	mux.HandleFunc("/welcome/invite", h.ServeInviteWelcome)
	mux.HandleFunc("/welcome/invite/", h.ServeInviteWelcome)
	mux.HandleFunc("/auth-modal.html", h.ServeAuthModal)
	mux.HandleFunc("/auth/callback", h.ServeAuthCallback)
	mux.HandleFunc("/auth/callback/", h.ServeAuthCallback)
	mux.HandleFunc("/extension-auth", h.ServeExtensionAuth)
	mux.HandleFunc("/extension-auth/", h.ServeExtensionAuth)
	mux.HandleFunc("/extension-auth.html", h.ServeExtensionAuth)
	mux.HandleFunc("/debug-auth.html", h.ServeDebugAuth)
	mux.HandleFunc("/jobs/", h.ServeJobDetails)

	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=604800")
		http.ServeFile(w, r, "./web/static/assets/Good-Native_Hover_App_Logo_Webflow.png")
	})

	mux.Handle("/js/", withCacheControl(http.StripPrefix("/js/", http.FileServer(http.Dir("./web/static/js/")))))
	mux.Handle("/styles/", withCacheControl(http.StripPrefix("/styles/", http.FileServer(http.Dir("./web/static/styles/")))))
	mux.Handle("/assets/", withCacheControl(http.StripPrefix("/assets/", http.FileServer(http.Dir("./web/static/assets/")))))
	mux.Handle("/app/", withCacheControl(http.StripPrefix("/app/", http.FileServer(http.Dir("./web/static/app/")))))
	mux.Handle("/web/", withCacheControl(http.StripPrefix("/web/", h.jsFileServer(http.Dir("./web/")))))
}

func requireSystemAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r.Context())
		if !ok {
			Unauthorised(w, r, "Authentication required")
			return
		}

		if !hasSystemAdminRole(claims) {
			Forbidden(w, r, "System administrator privileges required")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	WriteHealthy(w, r, "hover", Version)
}

func (h *Handler) DatabaseHealthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	if h.DB == nil {
		WriteUnhealthy(w, r, "postgresql", fmt.Errorf("database connection not configured"))
		return
	}

	if err := h.DB.GetDB().Ping(); err != nil {
		WriteUnhealthy(w, r, "postgresql", err)
		return
	}

	WriteHealthy(w, r, "postgresql", "")
}

func (h *Handler) ServeTestLogin(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "test-login.html")
}

func (h *Handler) ServeTestComponents(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "test-components.html")
}

func (h *Handler) ServeTestDataComponents(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "test-data-components.html")
}

func (h *Handler) ServeDashboard(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "dashboard.html")
}

func (h *Handler) ServeSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	http.ServeFile(w, r, "settings.html")
}

func (h *Handler) ServeWelcome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}
	if r.URL.Path != "/welcome" && r.URL.Path != "/welcome/" {
		NotFound(w, r, "Page not found")
		return
	}

	http.ServeFile(w, r, "welcome.html")
}

func (h *Handler) ServeInviteWelcome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}
	if r.URL.Path != "/welcome/invite" && r.URL.Path != "/welcome/invite/" {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, "invite-welcome.html")
}

func (h *Handler) ServeNewDashboard(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "dashboard.html")
}

func (h *Handler) ServeAuthModal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.ServeFile(w, r, "auth-modal.html")
}

func (h *Handler) ServeAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.ServeFile(w, r, "auth-callback.html")
}

func (h *Handler) ServeDebugAuth(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "debug-auth.html")
}

func (h *Handler) ServeExtensionAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.ServeFile(w, r, "web/templates/extension-auth.html")
}

func (h *Handler) ServeJobDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	http.ServeFile(w, r, "web/templates/job-details.html")
}

func (h *Handler) ServeSharedJobPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	http.ServeFile(w, r, "web/templates/job-details.html")
}

func (h *Handler) ServeHomepage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "homepage.html")
}

func (h *Handler) ServeConfigJS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	snippet, err := buildConfigSnippet()
	if err != nil {
		// Config comes from startup env vars; suppress Sentry to avoid one capture per request on misconfig.
		apiLog.ErrorContext(logging.NoCapture(r.Context()), "supabase config missing", "error", err)
		message := "Supabase config unavailable"
		if os.Getenv("APP_ENV") != "production" {
			message = fmt.Sprintf("%s: %v", message, err)
		}
		http.Error(w, message, http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(snippet); err != nil {
		apiLog.ErrorContext(logging.NoCapture(r.Context()), "failed to write config.js", "error", err)
	}
}

func (h *Handler) DashboardStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	dateRange := r.URL.Query().Get("range")
	if dateRange == "" {
		dateRange = "last7"
	}
	timezone := r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}

	startDate, endDate := calculateDateRange(dateRange, timezone)

	stats, err := h.DB.GetJobStats(orgID, startDate, endDate)
	if err != nil {
		if HandlePoolSaturation(w, r, err) {
			return
		}
		DatabaseError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"total_jobs":          stats.TotalJobs,
		"running_jobs":        stats.RunningJobs,
		"completed_jobs":      stats.CompletedJobs,
		"failed_jobs":         stats.FailedJobs,
		"total_tasks":         stats.TotalTasks,
		"avg_completion_time": stats.AvgCompletionTime,
		"date_range":          dateRange,
		"period_start":        startDate,
		"period_end":          endDate,
	}, "Dashboard statistics retrieved successfully")
}

func (h *Handler) DashboardActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	dateRange := r.URL.Query().Get("range")
	if dateRange == "" {
		dateRange = "last7"
	}
	timezone := r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}

	startDate, endDate := calculateDateRange(dateRange, timezone)

	activity, err := h.DB.GetJobActivity(orgID, startDate, endDate)
	if err != nil {
		DatabaseError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"activity":     activity,
		"date_range":   dateRange,
		"period_start": startDate,
		"period_end":   endDate,
	}, "Dashboard activity retrieved successfully")
}

func calculateDateRange(dateRange, timezone string) (*time.Time, *time.Time) {
	// All Australian east-coast aliases share Sydney's DST rules.
	timezoneAliases := map[string]string{
		"Australia/Melbourne": "Australia/Sydney",
		"Australia/ACT":       "Australia/Sydney",
		"Australia/Canberra":  "Australia/Sydney",
		"Australia/NSW":       "Australia/Sydney",
		"Australia/Victoria":  "Australia/Sydney",
	}

	if canonical, exists := timezoneAliases[timezone]; exists {
		apiLog.Debug("Mapping timezone alias", "original", timezone, "canonical", canonical)
		timezone = canonical
	}

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		apiLog.Warn("Invalid timezone in calculateDateRange, falling back to UTC", "error", err, "timezone", timezone)
		loc = time.UTC
	}

	now := time.Now().In(loc)
	var startDate, endDate *time.Time

	switch dateRange {
	case "last_hour":
		start := now.Add(-1 * time.Hour)
		startDate = &start
		endDate = &now
	case "today":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 999999999, loc)
		startDate = &start
		endDate = &end
	case "last_24_hours", "last24":
		start := now.Add(-24 * time.Hour)
		startDate = &start
		endDate = &now
	case "yesterday":
		yesterday := now.AddDate(0, 0, -1)
		start := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, loc)
		end := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 23, 59, 59, 999999999, loc)
		startDate = &start
		endDate = &end
	case "7days", "last7":
		start := now.AddDate(0, 0, -7)
		startDate = &start
		endDate = &now
	case "30days", "last30":
		start := now.AddDate(0, 0, -30)
		startDate = &start
		endDate = &now
	case "last90":
		start := now.AddDate(0, 0, -90)
		startDate = &start
		endDate = &now
	case "all":
		// nil signals no date filter to the caller.
		return nil, nil
	default:
		start := now.AddDate(0, 0, -7)
		startDate = &start
		endDate = &now
	}

	return startDate, endDate
}

// Short max-age keeps modules cached between navigations but revalidates within minutes of a deploy.
func withCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=60")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) jsFileServer(root http.FileSystem) http.Handler {
	fileServer := http.FileServer(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		}

		fileServer.ServeHTTP(w, r)
	})
}

func (h *Handler) DashboardSlowPages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	dateRange := r.URL.Query().Get("range")
	if dateRange == "" {
		dateRange = "last7"
	}
	timezone := r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}

	startDate, endDate := calculateDateRange(dateRange, timezone)

	slowPages, err := h.DB.GetSlowPages(orgID, startDate, endDate)
	if err != nil {
		DatabaseError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"slow_pages":   slowPages,
		"date_range":   dateRange,
		"period_start": startDate,
		"period_end":   endDate,
		"count":        len(slowPages),
	}, "Slow pages analysis retrieved successfully")
}

func (h *Handler) DashboardExternalRedirects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}

	orgID := h.GetActiveOrganisation(w, r)
	if orgID == "" {
		return
	}

	dateRange := r.URL.Query().Get("range")
	if dateRange == "" {
		dateRange = "last7"
	}
	timezone := r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}

	startDate, endDate := calculateDateRange(dateRange, timezone)

	redirects, err := h.DB.GetExternalRedirects(orgID, startDate, endDate)
	if err != nil {
		DatabaseError(w, r, err)
		return
	}

	WriteSuccess(w, r, map[string]any{
		"external_redirects": redirects,
		"date_range":         dateRange,
		"period_start":       startDate,
		"period_end":         endDate,
		"count":              len(redirects),
	}, "External redirects analysis retrieved successfully")
}

type WebflowWebhookPayload struct {
	TriggerType string `json:"triggerType"`
	SiteID      string `json:"siteId,omitempty"`
	Payload     struct {
		Domains     []string `json:"domains"`
		PublishedBy struct {
			DisplayName string `json:"displayName"`
		} `json:"publishedBy"`
	} `json:"payload"`
}

func (h *Handler) WebflowWebhook(w http.ResponseWriter, r *http.Request) {
	// Legacy path embeds the webhook token; redact it before logging so the credential never reaches Sentry.
	var logger *logging.Logger
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/webhooks/webflow/workspaces/"):
		logger = loggerWithRequestPath(r, "/v1/webhooks/webflow/workspaces/[redacted]")
	case strings.HasPrefix(r.URL.Path, "/v1/webhooks/webflow/"):
		logger = loggerWithRequestPath(r, "/v1/webhooks/webflow/[redacted]")
	default:
		logger = loggerWithRequest(r)
	}

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	// Legacy: /v1/webhooks/webflow/WEBHOOK_TOKEN; org-scoped: /v1/webhooks/webflow/workspaces/WORKSPACE_ID.
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[3] == "" {
		logger.Warn("Webflow webhook missing identifier in URL")
		BadRequest(w, r, "Webhook identifier required in URL path")
		return
	}

	isWorkspaceWebhook := pathParts[3] == "workspaces"
	webhookToken := ""
	workspaceID := ""
	if isWorkspaceWebhook {
		if len(pathParts) < 5 || pathParts[4] == "" {
			logger.Warn("Webflow webhook missing workspace ID in URL")
			BadRequest(w, r, "Webflow workspace ID required in URL path")
			return
		}
		workspaceID = pathParts[4]
	} else {
		webhookToken = pathParts[3]
	}

	var payload WebflowWebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		logger.Warn("Failed to parse Webflow webhook payload", "error", err)
		BadRequest(w, r, "Invalid webhook payload")
		return
	}

	var user *db.User
	orgID := ""
	if isWorkspaceWebhook {
		mapping, err := h.DB.GetPlatformOrgMapping(r.Context(), "webflow", workspaceID)
		if err != nil {
			if errors.Is(err, db.ErrPlatformOrgMappingNotFound) {
				logger.Warn("Unknown Webflow workspace", "workspace_id", workspaceID)
				NotFound(w, r, "Invalid Webflow workspace")
				return
			}
			// Surface DB failures as 500 so Webflow retries instead of giving up on a 404.
			logger.Error("Failed to resolve Webflow workspace mapping", "error", err, "workspace_id", workspaceID)
			InternalError(w, r, fmt.Errorf("failed to resolve webflow workspace: %w", err))
			return
		}
		if mapping.CreatedBy == nil || *mapping.CreatedBy == "" {
			logger.Error("Webflow mapping missing creator user", "workspace_id", workspaceID)
			InternalError(w, r, fmt.Errorf("webflow mapping missing creator"))
			return
		}
		user, err = h.DB.GetUser(*mapping.CreatedBy)
		if err != nil {
			logger.Error("Failed to load Webflow mapping creator", "error", err, "user_id", *mapping.CreatedBy)
			InternalError(w, r, fmt.Errorf("failed to load mapping creator: %w", err))
			return
		}
		orgID = mapping.OrganisationID
	} else {
		var err error
		user, err = h.DB.GetUserByWebhookToken(webhookToken)
		if err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				logger.Warn("Unknown webhook token")
				// 404 (not 401) so attackers cannot probe for valid tokens.
				NotFound(w, r, "Invalid webhook token")
				return
			}
			logger.Error("Failed to get user by webhook token", "error", err)
			InternalError(w, r, fmt.Errorf("failed to resolve webhook user: %w", err))
			return
		}
		orgID = h.DB.GetEffectiveOrganisationID(user)
	}

	logger.Info("Webflow webhook received",
		"user_id", user.ID,
		"trigger_type", payload.TriggerType,
		"published_by", payload.Payload.PublishedBy.DisplayName,
		"domains", payload.Payload.Domains,
	)

	if payload.TriggerType != "site_publish" {
		logger.Warn("Ignoring non-site-publish webhook", "trigger_type", payload.TriggerType)
		WriteSuccess(w, r, nil, "Webhook received but ignored (not site_publish)")
		return
	}

	if payload.SiteID != "" && orgID != "" {
		siteSetting, err := h.DB.GetSiteSetting(r.Context(), orgID, payload.SiteID)
		if err != nil {
			if errors.Is(err, db.ErrWebflowSiteSettingNotFound) {
				logger.Warn("Site not configured for auto-publish, ignoring webhook",
					"site_id", payload.SiteID,
					"organisation_id", orgID,
				)
				WriteSuccess(w, r, nil, "Webhook received but site not configured for auto-publish")
				return
			}
			logger.Error("Failed to check site settings", "error", err, "site_id", payload.SiteID)
			InternalError(w, r, err)
			return
		}

		if !siteSetting.AutoPublishEnabled {
			logger.Info("Auto-publish disabled for site, ignoring webhook",
				"site_id", payload.SiteID,
				"organisation_id", orgID,
			)
			WriteSuccess(w, r, nil, "Webhook received but auto-publish disabled for this site")
			return
		}

		logger.Debug("Site auto-publish check passed",
			"site_id", payload.SiteID,
			"auto_publish_enabled", siteSetting.AutoPublishEnabled,
		)
	}

	if len(payload.Payload.Domains) == 0 {
		logger.Warn("Webflow webhook missing domains")
		BadRequest(w, r, "Domains are required")
		return
	}

	// Webflow lists the primary/canonical domain first.
	selectedDomain := payload.Payload.Domains[0]

	useSitemap := true
	findLinks := true
	concurrency := 20
	if concurrencyParam := r.URL.Query().Get("concurrency"); concurrencyParam != "" {
		parsed, err := strconv.Atoi(concurrencyParam)
		if err != nil {
			BadRequest(w, r, "concurrency must be a positive integer")
			return
		}
		if parsed <= 0 {
			BadRequest(w, r, "concurrency must be a positive integer")
			return
		}
		concurrency = min(parsed, 100)
	}
	maxPages := 0
	sourceType := "webflow_webhook"
	sourceDetail := payload.Payload.PublishedBy.DisplayName

	sourceInfoBytes, _ := json.Marshal(payload)
	sourceInfo := string(sourceInfoBytes)

	req := CreateJobRequest{
		Domain:       selectedDomain,
		UseSitemap:   &useSitemap,
		FindLinks:    &findLinks,
		Concurrency:  &concurrency,
		MaxPages:     &maxPages,
		SourceType:   &sourceType,
		SourceDetail: &sourceDetail,
		SourceInfo:   &sourceInfo,
	}

	// Copy avoids mutating the cached user while injecting webhook-scoped org context.
	userForJob := *user
	if orgID != "" {
		userForJob.ActiveOrganisationID = &orgID
		userForJob.OrganisationID = &orgID
	}
	job, err := h.createJobFromRequest(r.Context(), &userForJob, req, logger)
	if err != nil {
		logger.Error("Failed to create job from webhook",
			"error", err,
			"user_id", user.ID,
			"domain", selectedDomain,
		)
		InternalError(w, r, err)
		return
	}

	logger.Info("Successfully created and started job from Webflow webhook",
		"job_id", job.ID,
		"user_id", user.ID,
		"org_id", orgID,
		"domain", selectedDomain,
		"selected_from", strings.Join(payload.Payload.Domains, ", "),
	)

	WriteSuccess(w, r, map[string]any{
		"job_id":  job.ID,
		"user_id": user.ID,
		"org_id":  orgID,
		"domain":  selectedDomain,
		"status":  "created",
	}, "Job created successfully from webhook")
}
