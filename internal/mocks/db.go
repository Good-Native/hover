package mocks

import (
	"context"
	"database/sql"
	"time"

	"github.com/good-native/hover/internal/broker"
	"github.com/good-native/hover/internal/db"
	"github.com/stretchr/testify/mock"
)

// MockDB is a mock implementation of the database interface
type MockDB struct {
	mock.Mock
}

// GetDB mocks the GetDB method to return underlying *sql.DB
func (m *MockDB) GetDB() *sql.DB {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*sql.DB)
}

// Close mocks the Close method for database connection cleanup
func (m *MockDB) Close() error {
	args := m.Called()
	return args.Error(0)
}

// GetConfig mocks the GetConfig method to return database configuration
func (m *MockDB) GetConfig() *db.Config {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*db.Config)
}

// RecalculateJobStats mocks the RecalculateJobStats method
func (m *MockDB) RecalculateJobStats(ctx context.Context, jobID string) error {
	args := m.Called(ctx, jobID)
	return args.Error(0)
}

// ResetSchema mocks the ResetSchema method for testing database resets
func (m *MockDB) ResetSchema() error {
	args := m.Called()
	return args.Error(0)
}

// ResetDataOnly mocks the ResetDataOnly method for testing data-only resets
func (m *MockDB) ResetDataOnly() error {
	args := m.Called()
	return args.Error(0)
}

// GetOrCreateUser mocks the GetOrCreateUser method
func (m *MockDB) GetOrCreateUser(userID, email string, orgID *string) (*db.User, error) {
	args := m.Called(userID, email, orgID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.User), args.Error(1)
}

// GetUser mocks retrieving a user by ID
func (m *MockDB) GetUser(userID string) (*db.User, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.User), args.Error(1)
}

// UpdateUserNames mocks updating user name fields
func (m *MockDB) UpdateUserNames(userID string, firstName, lastName, fullName *string) error {
	args := m.Called(userID, firstName, lastName, fullName)
	return args.Error(0)
}

// CreateUser mocks creating a user and organisation
func (m *MockDB) CreateUser(userID, email string, firstName, lastName, fullName *string, orgName string) (*db.User, *db.Organisation, error) {
	args := m.Called(userID, email, firstName, lastName, fullName, orgName)
	var user *db.User
	var org *db.Organisation
	if u := args.Get(0); u != nil {
		user = u.(*db.User)
	}
	if o := args.Get(1); o != nil {
		org = o.(*db.Organisation)
	}
	return user, org, args.Error(2)
}

// GetOrganisation mocks retrieving an organisation by ID
func (m *MockDB) GetOrganisation(organisationID string) (*db.Organisation, error) {
	args := m.Called(organisationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.Organisation), args.Error(1)
}

// ListJobs mocks listing jobs with pagination and filters
func (m *MockDB) ListJobs(organisationID string, limit, offset int, status, dateRange, timezone string) ([]db.JobWithDomain, int, error) {
	args := m.Called(organisationID, limit, offset, status, dateRange, timezone)
	var jobs []db.JobWithDomain
	if v := args.Get(0); v != nil {
		jobs = v.([]db.JobWithDomain)
	}
	total := 0
	if v := args.Get(1); v != nil {
		total = v.(int)
	}
	return jobs, total, args.Error(2)
}

func (m *MockDB) ListJobsWithOffset(organisationID string, limit, offset int, status, dateRange string, tzOffsetMinutes int, includeStats bool) ([]db.JobWithDomain, int, error) {
	args := m.Called(organisationID, limit, offset, status, dateRange, tzOffsetMinutes, includeStats)
	var jobs []db.JobWithDomain
	if v := args.Get(0); v != nil {
		jobs = v.([]db.JobWithDomain)
	}
	total := 0
	if v := args.Get(1); v != nil {
		total = v.(int)
	}
	return jobs, total, args.Error(2)
}

// GetJobStats mocks the GetJobStats method
func (m *MockDB) GetJobStats(organisationID string, startDate, endDate *time.Time) (*db.JobStats, error) {
	args := m.Called(organisationID, startDate, endDate)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.JobStats), args.Error(1)
}

// GetJobActivity mocks the GetJobActivity method
func (m *MockDB) GetJobActivity(organisationID string, startDate, endDate *time.Time) ([]db.ActivityPoint, error) {
	args := m.Called(organisationID, startDate, endDate)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.ActivityPoint), args.Error(1)
}

// GetSlowPages mocks the GetSlowPages method
func (m *MockDB) GetSlowPages(organisationID string, startDate, endDate *time.Time) ([]db.SlowPage, error) {
	args := m.Called(organisationID, startDate, endDate)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.SlowPage), args.Error(1)
}

// GetExternalRedirects mocks the GetExternalRedirects method
func (m *MockDB) GetExternalRedirects(organisationID string, startDate, endDate *time.Time) ([]db.ExternalRedirect, error) {
	args := m.Called(organisationID, startDate, endDate)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.ExternalRedirect), args.Error(1)
}

// GetUserByWebhookToken mocks the GetUserByWebhookToken method
func (m *MockDB) GetUserByWebhookToken(token string) (*db.User, error) {
	args := m.Called(token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.User), args.Error(1)
}

// CreateScheduler mocks the CreateScheduler method
func (m *MockDB) CreateScheduler(ctx context.Context, scheduler *db.Scheduler) error {
	args := m.Called(ctx, scheduler)
	return args.Error(0)
}

// GetScheduler mocks the GetScheduler method
func (m *MockDB) GetScheduler(ctx context.Context, schedulerID string) (*db.Scheduler, error) {
	args := m.Called(ctx, schedulerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.Scheduler), args.Error(1)
}

// ListSchedulers mocks the ListSchedulers method
func (m *MockDB) ListSchedulers(ctx context.Context, organisationID string) ([]*db.Scheduler, error) {
	args := m.Called(ctx, organisationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*db.Scheduler), args.Error(1)
}

// UpdateScheduler mocks the UpdateScheduler method
func (m *MockDB) UpdateScheduler(ctx context.Context, schedulerID string, updates *db.Scheduler, expectedIsEnabled *bool) error {
	args := m.Called(ctx, schedulerID, updates, expectedIsEnabled)
	return args.Error(0)
}

// DeleteScheduler mocks the DeleteScheduler method
func (m *MockDB) DeleteScheduler(ctx context.Context, schedulerID string) error {
	args := m.Called(ctx, schedulerID)
	return args.Error(0)
}

// GetSchedulersReadyToRun mocks the GetSchedulersReadyToRun method
func (m *MockDB) GetSchedulersReadyToRun(ctx context.Context, limit int) ([]*db.Scheduler, error) {
	args := m.Called(ctx, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*db.Scheduler), args.Error(1)
}

// UpdateSchedulerNextRun mocks the UpdateSchedulerNextRun method
func (m *MockDB) UpdateSchedulerNextRun(ctx context.Context, schedulerID string, nextRun time.Time) error {
	args := m.Called(ctx, schedulerID, nextRun)
	return args.Error(0)
}

// GetDomainNameByID mocks the GetDomainNameByID method
func (m *MockDB) GetDomainNameByID(ctx context.Context, domainID int) (string, error) {
	args := m.Called(ctx, domainID)
	return args.String(0), args.Error(1)
}

// GetOrCreateDomainID mocks the GetOrCreateDomainID method
func (m *MockDB) GetOrCreateDomainID(ctx context.Context, domain string) (int, error) {
	args := m.Called(ctx, domain)
	return args.Int(0), args.Error(1)
}

// UpsertOrganisationDomain mocks the UpsertOrganisationDomain method
func (m *MockDB) UpsertOrganisationDomain(ctx context.Context, organisationID string, domainID int) error {
	args := m.Called(ctx, organisationID, domainID)
	return args.Error(0)
}

// GetDomainNames mocks the GetDomainNames method
func (m *MockDB) GetDomainNames(ctx context.Context, domainIDs []int) (map[int]string, error) {
	args := m.Called(ctx, domainIDs)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[int]string), args.Error(1)
}

// GetDomainsForOrganisation mocks the GetDomainsForOrganisation method
func (m *MockDB) GetDomainsForOrganisation(ctx context.Context, organisationID string) ([]db.OrganisationDomain, error) {
	args := m.Called(ctx, organisationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.OrganisationDomain), args.Error(1)
}

// GetLastJobStartTimeForScheduler mocks the GetLastJobStartTimeForScheduler method
func (m *MockDB) GetLastJobStartTimeForScheduler(ctx context.Context, schedulerID string) (*time.Time, error) {
	args := m.Called(ctx, schedulerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*time.Time), args.Error(1)
}

// ListUserOrganisations mocks the ListUserOrganisations method
func (m *MockDB) ListUserOrganisations(userID string) ([]db.UserOrganisation, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.UserOrganisation), args.Error(1)
}

// ValidateOrganisationMembership mocks the ValidateOrganisationMembership method
func (m *MockDB) ValidateOrganisationMembership(userID, organisationID string) (bool, error) {
	args := m.Called(userID, organisationID)
	return args.Bool(0), args.Error(1)
}

// SetActiveOrganisation mocks the SetActiveOrganisation method
func (m *MockDB) SetActiveOrganisation(userID, organisationID string) error {
	args := m.Called(userID, organisationID)
	return args.Error(0)
}

// GetEffectiveOrganisationID mocks the GetEffectiveOrganisationID method
func (m *MockDB) GetEffectiveOrganisationID(user *db.User) string {
	args := m.Called(user)
	return args.String(0)
}

// CreateOrganisation mocks the CreateOrganisation method
func (m *MockDB) CreateOrganisation(name string) (*db.Organisation, error) {
	args := m.Called(name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.Organisation), args.Error(1)
}

// CreateOrganisationForUser mocks the CreateOrganisationForUser method
func (m *MockDB) CreateOrganisationForUser(userID, name string) (*db.Organisation, error) {
	args := m.Called(userID, name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.Organisation), args.Error(1)
}

// AddOrganisationMember mocks the AddOrganisationMember method
func (m *MockDB) AddOrganisationMember(userID, organisationID, role string) error {
	args := m.Called(userID, organisationID, role)
	return args.Error(0)
}

// GetOrganisationMemberRole mocks role lookups
func (m *MockDB) GetOrganisationMemberRole(ctx context.Context, userID, organisationID string) (string, error) {
	args := m.Called(ctx, userID, organisationID)
	return args.String(0), args.Error(1)
}

// ListOrganisationMembers mocks member listing
func (m *MockDB) ListOrganisationMembers(ctx context.Context, organisationID string) ([]db.OrganisationMember, error) {
	args := m.Called(ctx, organisationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.OrganisationMember), args.Error(1)
}

// IsOrganisationMemberEmail mocks email membership checks
func (m *MockDB) IsOrganisationMemberEmail(ctx context.Context, organisationID, email string) (bool, error) {
	args := m.Called(ctx, organisationID, email)
	return args.Bool(0), args.Error(1)
}

// RemoveOrganisationMember mocks member removal
func (m *MockDB) RemoveOrganisationMember(ctx context.Context, userID, organisationID string) error {
	args := m.Called(ctx, userID, organisationID)
	return args.Error(0)
}

// UpdateOrganisationMemberRole mocks member role updates
func (m *MockDB) UpdateOrganisationMemberRole(ctx context.Context, userID, organisationID, role string) error {
	args := m.Called(ctx, userID, organisationID, role)
	return args.Error(0)
}

// CountOrganisationAdmins mocks admin counting
func (m *MockDB) CountOrganisationAdmins(ctx context.Context, organisationID string) (int, error) {
	args := m.Called(ctx, organisationID)
	return args.Int(0), args.Error(1)
}

// CreateOrganisationInvite mocks invite creation
func (m *MockDB) CreateOrganisationInvite(ctx context.Context, invite *db.OrganisationInvite) (*db.OrganisationInvite, error) {
	args := m.Called(ctx, invite)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.OrganisationInvite), args.Error(1)
}

// ListOrganisationInvites mocks invite listing
func (m *MockDB) ListOrganisationInvites(ctx context.Context, organisationID string) ([]db.OrganisationInvite, error) {
	args := m.Called(ctx, organisationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.OrganisationInvite), args.Error(1)
}

// RevokeOrganisationInvite mocks invite revocation
func (m *MockDB) RevokeOrganisationInvite(ctx context.Context, inviteID, organisationID string) error {
	args := m.Called(ctx, inviteID, organisationID)
	return args.Error(0)
}

// GetOrganisationInviteByToken mocks invite lookup
func (m *MockDB) GetOrganisationInviteByToken(ctx context.Context, token string) (*db.OrganisationInvite, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.OrganisationInvite), args.Error(1)
}

// AcceptOrganisationInvite mocks invite acceptance
func (m *MockDB) AcceptOrganisationInvite(ctx context.Context, token, userID string) (*db.OrganisationInvite, error) {
	args := m.Called(ctx, token, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.OrganisationInvite), args.Error(1)
}

// SetOrganisationPlan mocks plan updates
func (m *MockDB) SetOrganisationPlan(ctx context.Context, organisationID, planID string) error {
	args := m.Called(ctx, organisationID, planID)
	return args.Error(0)
}

// GetOrganisationPlanID mocks plan retrieval
func (m *MockDB) GetOrganisationPlanID(ctx context.Context, organisationID string) (string, error) {
	args := m.Called(ctx, organisationID)
	return args.String(0), args.Error(1)
}

// ListDailyUsage mocks daily usage history
func (m *MockDB) ListDailyUsage(ctx context.Context, organisationID string, startDate, endDate time.Time) ([]db.DailyUsageEntry, error) {
	args := m.Called(ctx, organisationID, startDate, endDate)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.DailyUsageEntry), args.Error(1)
}

// Slack integration methods

func (m *MockDB) CreateSlackConnection(ctx context.Context, conn *db.SlackConnection) error {
	args := m.Called(ctx, conn)
	return args.Error(0)
}

func (m *MockDB) GetSlackConnection(ctx context.Context, connectionID string) (*db.SlackConnection, error) {
	args := m.Called(ctx, connectionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.SlackConnection), args.Error(1)
}

func (m *MockDB) ListSlackConnections(ctx context.Context, organisationID string) ([]*db.SlackConnection, error) {
	args := m.Called(ctx, organisationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*db.SlackConnection), args.Error(1)
}

func (m *MockDB) DeleteSlackConnection(ctx context.Context, connectionID, organisationID string) error {
	args := m.Called(ctx, connectionID, organisationID)
	return args.Error(0)
}

func (m *MockDB) CreateSlackUserLink(ctx context.Context, link *db.SlackUserLink) error {
	args := m.Called(ctx, link)
	return args.Error(0)
}

func (m *MockDB) GetSlackUserLink(ctx context.Context, userID, connectionID string) (*db.SlackUserLink, error) {
	args := m.Called(ctx, userID, connectionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.SlackUserLink), args.Error(1)
}

func (m *MockDB) UpdateSlackUserLinkNotifications(ctx context.Context, userID, connectionID string, dmNotifications bool) error {
	args := m.Called(ctx, userID, connectionID, dmNotifications)
	return args.Error(0)
}

func (m *MockDB) DeleteSlackUserLink(ctx context.Context, userID, connectionID string) error {
	args := m.Called(ctx, userID, connectionID)
	return args.Error(0)
}

func (m *MockDB) StoreSlackToken(ctx context.Context, connectionID, token string) error {
	args := m.Called(ctx, connectionID, token)
	return args.Error(0)
}

func (m *MockDB) GetSlackToken(ctx context.Context, connectionID string) (string, error) {
	args := m.Called(ctx, connectionID)
	return args.String(0), args.Error(1)
}

// Notification methods

func (m *MockDB) ListNotifications(ctx context.Context, organisationID string, limit, offset int, unreadOnly bool) ([]*db.Notification, int, error) {
	args := m.Called(ctx, organisationID, limit, offset, unreadOnly)
	if args.Get(0) == nil {
		return nil, args.Int(1), args.Error(2)
	}
	return args.Get(0).([]*db.Notification), args.Int(1), args.Error(2)
}

func (m *MockDB) GetUnreadNotificationCount(ctx context.Context, organisationID string) (int, error) {
	args := m.Called(ctx, organisationID)
	return args.Int(0), args.Error(1)
}

func (m *MockDB) MarkNotificationRead(ctx context.Context, notificationID, organisationID string) error {
	args := m.Called(ctx, notificationID, organisationID)
	return args.Error(0)
}

func (m *MockDB) MarkAllNotificationsRead(ctx context.Context, organisationID string) error {
	args := m.Called(ctx, organisationID)
	return args.Error(0)
}

// Webflow integration mock methods
func (m *MockDB) CreateWebflowConnection(ctx context.Context, conn *db.WebflowConnection) error {
	args := m.Called(ctx, conn)
	return args.Error(0)
}

func (m *MockDB) GetWebflowConnection(ctx context.Context, connectionID string) (*db.WebflowConnection, error) {
	args := m.Called(ctx, connectionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.WebflowConnection), args.Error(1)
}

func (m *MockDB) ListWebflowConnections(ctx context.Context, organisationID string) ([]*db.WebflowConnection, error) {
	args := m.Called(ctx, organisationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*db.WebflowConnection), args.Error(1)
}

func (m *MockDB) DeleteWebflowConnection(ctx context.Context, connectionID, organisationID string) error {
	args := m.Called(ctx, connectionID, organisationID)
	return args.Error(0)
}

func (m *MockDB) StoreWebflowToken(ctx context.Context, connectionID, token string) error {
	args := m.Called(ctx, connectionID, token)
	return args.Error(0)
}

func (m *MockDB) GetWebflowToken(ctx context.Context, connectionID string) (string, error) {
	args := m.Called(ctx, connectionID)
	return args.String(0), args.Error(1)
}

// Platform integration methods

func (m *MockDB) UpsertPlatformOrgMapping(ctx context.Context, mapping *db.PlatformOrgMapping) error {
	args := m.Called(ctx, mapping)
	return args.Error(0)
}

func (m *MockDB) GetPlatformOrgMapping(ctx context.Context, platform, platformID string) (*db.PlatformOrgMapping, error) {
	args := m.Called(ctx, platform, platformID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.PlatformOrgMapping), args.Error(1)
}

// Usage and plans methods

func (m *MockDB) GetOrganisationUsageStats(ctx context.Context, orgID string) (*db.UsageStats, error) {
	args := m.Called(ctx, orgID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.UsageStats), args.Error(1)
}

func (m *MockDB) GetActivePlans(ctx context.Context) ([]db.Plan, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]db.Plan), args.Error(1)
}

// Webflow site settings mock methods

func (m *MockDB) CreateOrUpdateSiteSetting(ctx context.Context, setting *db.WebflowSiteSetting) error {
	args := m.Called(ctx, setting)
	return args.Error(0)
}

func (m *MockDB) GetSiteSetting(ctx context.Context, orgID, siteID string) (*db.WebflowSiteSetting, error) {
	args := m.Called(ctx, orgID, siteID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.WebflowSiteSetting), args.Error(1)
}

func (m *MockDB) GetSiteSettingByID(ctx context.Context, settingID string) (*db.WebflowSiteSetting, error) {
	args := m.Called(ctx, settingID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.WebflowSiteSetting), args.Error(1)
}

func (m *MockDB) ListConfiguredSiteSettings(ctx context.Context, orgID string) ([]*db.WebflowSiteSetting, error) {
	args := m.Called(ctx, orgID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*db.WebflowSiteSetting), args.Error(1)
}

func (m *MockDB) ListAllSiteSettings(ctx context.Context, orgID string) ([]*db.WebflowSiteSetting, error) {
	args := m.Called(ctx, orgID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*db.WebflowSiteSetting), args.Error(1)
}

func (m *MockDB) ListSiteSettingsByConnection(ctx context.Context, connectionID string) ([]*db.WebflowSiteSetting, error) {
	args := m.Called(ctx, connectionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*db.WebflowSiteSetting), args.Error(1)
}

func (m *MockDB) UpdateSiteSchedule(ctx context.Context, orgID, siteID string, intervalHours *int, schedulerID string) error {
	args := m.Called(ctx, orgID, siteID, intervalHours, schedulerID)
	return args.Error(0)
}

func (m *MockDB) UpdateSiteAutoPublish(ctx context.Context, orgID, siteID string, enabled bool, webhookID string) error {
	args := m.Called(ctx, orgID, siteID, enabled, webhookID)
	return args.Error(0)
}

func (m *MockDB) DeleteSiteSetting(ctx context.Context, orgID, siteID string) error {
	args := m.Called(ctx, orgID, siteID)
	return args.Error(0)
}

func (m *MockDB) DeleteSiteSettingsByConnection(ctx context.Context, connectionID string) error {
	args := m.Called(ctx, connectionID)
	return args.Error(0)
}

func (m *MockDB) GetSiteSettingBySiteID(ctx context.Context, orgID, webflowSiteID string) (*db.WebflowSiteSetting, error) {
	args := m.Called(ctx, orgID, webflowSiteID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.WebflowSiteSetting), args.Error(1)
}

// Google Analytics integration mock methods

func (m *MockDB) CreateGoogleConnection(ctx context.Context, conn *db.GoogleAnalyticsConnection) error {
	args := m.Called(ctx, conn)
	return args.Error(0)
}

func (m *MockDB) GetGoogleConnection(ctx context.Context, connectionID string) (*db.GoogleAnalyticsConnection, error) {
	args := m.Called(ctx, connectionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*db.GoogleAnalyticsConnection), args.Error(1)
}

func (m *MockDB) ListGoogleConnections(ctx context.Context, organisationID string) ([]*db.GoogleAnalyticsConnection, error) {
	args := m.Called(ctx, organisationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*db.GoogleAnalyticsConnection), args.Error(1)
}

func (m *MockDB) DeleteGoogleConnection(ctx context.Context, connectionID, organisationID string) error {
	args := m.Called(ctx, connectionID, organisationID)
	return args.Error(0)
}

func (m *MockDB) UpdateGoogleConnectionStatus(ctx context.Context, connectionID, organisationID, status string) error {
	args := m.Called(ctx, connectionID, organisationID, status)
	return args.Error(0)
}

func (m *MockDB) StoreGoogleToken(ctx context.Context, connectionID, token string) error {
	args := m.Called(ctx, connectionID, token)
	return args.Error(0)
}

func (m *MockDB) GetGoogleToken(ctx context.Context, connectionID string) (string, error) {
	args := m.Called(ctx, connectionID)
	return args.String(0), args.Error(1)
}

// MockBrokerCleaner is a mock implementation of api.BrokerCleaner for
// admin handler tests. Returns the configured (count, error) pair.
type MockBrokerCleaner struct {
	mock.Mock
}

// ClearAll mocks the broker ClearAll method.
func (m *MockBrokerCleaner) ClearAll(ctx context.Context) (int, error) {
	args := m.Called(ctx)
	return args.Int(0), args.Error(1)
}

// ReclaimTerminalJobKeys mocks the broker reclaim sweeper. Returns the
// configured (report, error) pair so admin handler tests can assert on
// the surfaced counts. A nil first arg yields a zero-value report;
// passing any other type panics so misconfigured mocks fail loudly
// rather than silently returning empty counts.
func (m *MockBrokerCleaner) ReclaimTerminalJobKeys(ctx context.Context, filter broker.TerminalFilter) (broker.ReclaimReport, error) {
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return broker.ReclaimReport{}, args.Error(1)
	}
	return args.Get(0).(broker.ReclaimReport), args.Error(1)
}
