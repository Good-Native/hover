package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// OrganisationMember represents a user membership within an organisation.
type OrganisationMember struct {
	UserID    string
	Email     string
	FirstName *string
	LastName  *string
	FullName  *string
	Role      string
	CreatedAt time.Time
}

// OrganisationInvite represents a pending invite for an organisation.
type OrganisationInvite struct {
	ID             string
	OrganisationID string
	Email          string
	Role           string
	Token          string
	CreatedBy      string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	AcceptedAt     *time.Time
	RevokedAt      *time.Time
}

// DailyUsageEntry represents historical daily usage.
type DailyUsageEntry struct {
	UsageDate      time.Time
	PagesProcessed int
	JobsCreated    int
}

// GetOrganisationMemberRole returns the role for a user in an organisation.
func (db *DB) GetOrganisationMemberRole(ctx context.Context, userID, organisationID string) (string, error) {
	query := `
		SELECT role
		FROM organisation_members
		WHERE user_id = $1 AND organisation_id = $2
	`

	var role string
	if err := db.client.QueryRowContext(ctx, query, userID, organisationID).Scan(&role); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("organisation member not found")
		}
		return "", fmt.Errorf("failed to fetch organisation role: %w", err)
	}

	return role, nil
}

// ListOrganisationMembers returns all members for an organisation.
func (db *DB) ListOrganisationMembers(ctx context.Context, organisationID string) ([]OrganisationMember, error) {
	query := `
		SELECT u.id, u.email, u.first_name, u.last_name, u.full_name, om.role, om.created_at
		FROM organisation_members om
		JOIN users u ON u.id = om.user_id
		WHERE om.organisation_id = $1
		ORDER BY om.created_at ASC
	`

	rows, err := db.client.QueryContext(ctx, query, organisationID)
	if err != nil {
		return nil, fmt.Errorf("failed to list organisation members: %w", err)
	}
	defer rows.Close()

	var members []OrganisationMember
	for rows.Next() {
		var member OrganisationMember
		if err := rows.Scan(&member.UserID, &member.Email, &member.FirstName, &member.LastName, &member.FullName, &member.Role, &member.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan organisation member: %w", err)
		}
		members = append(members, member)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate organisation members: %w", err)
	}

	return members, nil
}

// IsOrganisationMemberEmail checks whether an email belongs to a member of the organisation.
func (db *DB) IsOrganisationMemberEmail(ctx context.Context, organisationID, email string) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1
			FROM organisation_members om
			JOIN users u ON u.id = om.user_id
			WHERE om.organisation_id = $1
			  AND lower(u.email) = lower($2)
		)
	`

	var exists bool
	if err := db.client.QueryRowContext(ctx, query, organisationID, email).Scan(&exists); err != nil {
		return false, fmt.Errorf("failed to check organisation member email: %w", err)
	}

	return exists, nil
}

// RemoveOrganisationMember deletes a membership from an organisation.
func (db *DB) RemoveOrganisationMember(ctx context.Context, userID, organisationID string) error {
	query := `
		DELETE FROM organisation_members
		WHERE user_id = $1 AND organisation_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, userID, organisationID)
	if err != nil {
		return fmt.Errorf("failed to remove organisation member: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("organisation member not found")
	}

	return nil
}

// UpdateOrganisationMemberRole updates a member's role in an organisation.
func (db *DB) UpdateOrganisationMemberRole(ctx context.Context, userID, organisationID, role string) error {
	role = strings.TrimSpace(strings.ToLower(role))
	if role != "admin" && role != "member" {
		return fmt.Errorf("invalid role: %s", role)
	}

	query := `
		UPDATE organisation_members
		SET role = $3
		WHERE user_id = $1 AND organisation_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, userID, organisationID, role)
	if err != nil {
		return fmt.Errorf("failed to update organisation member role: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("organisation member not found")
	}

	return nil
}

// CountOrganisationAdmins returns the number of admins in an organisation.
func (db *DB) CountOrganisationAdmins(ctx context.Context, organisationID string) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM organisation_members
		WHERE organisation_id = $1 AND role = 'admin'
	`

	var count int
	if err := db.client.QueryRowContext(ctx, query, organisationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count organisation admins: %w", err)
	}

	return count, nil
}

// CreateOrganisationInvite inserts a new invite record.
func (db *DB) CreateOrganisationInvite(ctx context.Context, invite *OrganisationInvite) (*OrganisationInvite, error) {
	query := `
		INSERT INTO organisation_invites
			(organisation_id, email, role, token, created_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), $6)
		RETURNING id, created_at
	`

	row := db.client.QueryRowContext(
		ctx,
		query,
		invite.OrganisationID,
		strings.ToLower(invite.Email),
		invite.Role,
		invite.Token,
		invite.CreatedBy,
		invite.ExpiresAt,
	)

	if err := row.Scan(&invite.ID, &invite.CreatedAt); err != nil {
		return nil, fmt.Errorf("failed to create organisation invite: %w", err)
	}

	return invite, nil
}

// ListOrganisationInvites returns pending invites for an organisation.
func (db *DB) ListOrganisationInvites(ctx context.Context, organisationID string) ([]OrganisationInvite, error) {
	query := `
		SELECT id, organisation_id, email, role, token, created_by, created_at, expires_at, accepted_at, revoked_at
		FROM organisation_invites
		WHERE organisation_id = $1
		  AND accepted_at IS NULL
		  AND revoked_at IS NULL
		ORDER BY created_at DESC
	`

	rows, err := db.client.QueryContext(ctx, query, organisationID)
	if err != nil {
		return nil, fmt.Errorf("failed to list organisation invites: %w", err)
	}
	defer rows.Close()

	var invites []OrganisationInvite
	for rows.Next() {
		var invite OrganisationInvite
		if err := rows.Scan(
			&invite.ID,
			&invite.OrganisationID,
			&invite.Email,
			&invite.Role,
			&invite.Token,
			&invite.CreatedBy,
			&invite.CreatedAt,
			&invite.ExpiresAt,
			&invite.AcceptedAt,
			&invite.RevokedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan organisation invite: %w", err)
		}
		invites = append(invites, invite)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate organisation invites: %w", err)
	}

	return invites, nil
}

// RevokeOrganisationInvite marks an invite as revoked.
func (db *DB) RevokeOrganisationInvite(ctx context.Context, inviteID, organisationID string) error {
	query := `
		UPDATE organisation_invites
		SET revoked_at = NOW()
		WHERE id = $1
		  AND organisation_id = $2
		  AND accepted_at IS NULL
		  AND revoked_at IS NULL
	`

	result, err := db.client.ExecContext(ctx, query, inviteID, organisationID)
	if err != nil {
		return fmt.Errorf("failed to revoke organisation invite: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("invite not found or already processed")
	}

	return nil
}

// GetOrganisationInviteByToken returns an invite by token.
func (db *DB) GetOrganisationInviteByToken(ctx context.Context, token string) (*OrganisationInvite, error) {
	query := `
		SELECT id, organisation_id, email, role, token, created_by, created_at, expires_at, accepted_at, revoked_at
		FROM organisation_invites
		WHERE token = $1
		LIMIT 1
	`

	var invite OrganisationInvite
	if err := db.client.QueryRowContext(ctx, query, token).Scan(
		&invite.ID,
		&invite.OrganisationID,
		&invite.Email,
		&invite.Role,
		&invite.Token,
		&invite.CreatedBy,
		&invite.CreatedAt,
		&invite.ExpiresAt,
		&invite.AcceptedAt,
		&invite.RevokedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invite not found")
		}
		return nil, fmt.Errorf("failed to fetch invite: %w", err)
	}

	return &invite, nil
}

// AcceptOrganisationInvite marks an invite as accepted and adds the user to the organisation.
func (db *DB) AcceptOrganisationInvite(ctx context.Context, token, userID string) (*OrganisationInvite, error) {
	tx, err := db.client.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start invite transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	query := `
		SELECT id, organisation_id, email, role, token, created_by, created_at, expires_at, accepted_at, revoked_at
		FROM organisation_invites
		WHERE token = $1
		FOR UPDATE
	`

	var invite OrganisationInvite
	if err := tx.QueryRowContext(ctx, query, token).Scan(
		&invite.ID,
		&invite.OrganisationID,
		&invite.Email,
		&invite.Role,
		&invite.Token,
		&invite.CreatedBy,
		&invite.CreatedAt,
		&invite.ExpiresAt,
		&invite.AcceptedAt,
		&invite.RevokedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invite not found")
		}
		return nil, fmt.Errorf("failed to fetch invite: %w", err)
	}

	if invite.AcceptedAt != nil {
		return nil, fmt.Errorf("invite already accepted")
	}
	if invite.RevokedAt != nil {
		return nil, fmt.Errorf("invite has been revoked")
	}
	if time.Now().After(invite.ExpiresAt) {
		return nil, fmt.Errorf("invite has expired")
	}

	memberInsert := `
		INSERT INTO organisation_members (user_id, organisation_id, role, created_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id, organisation_id) DO UPDATE
		SET role = EXCLUDED.role
	`
	if _, err := tx.ExecContext(ctx, memberInsert, userID, invite.OrganisationID, invite.Role); err != nil {
		return nil, fmt.Errorf("failed to add organisation member: %w", err)
	}

	updateInvite := `
		UPDATE organisation_invites
		SET accepted_at = NOW()
		WHERE id = $1
	`
	if _, err := tx.ExecContext(ctx, updateInvite, invite.ID); err != nil {
		return nil, fmt.Errorf("failed to accept invite: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit invite acceptance: %w", err)
	}

	return &invite, nil
}

// SetOrganisationPlan updates the organisation's plan.
func (db *DB) SetOrganisationPlan(ctx context.Context, organisationID, planID string) error {
	query := `
		UPDATE organisations
		SET plan_id = $2, updated_at = NOW()
		WHERE id = $1
		  AND EXISTS (SELECT 1 FROM plans WHERE id = $2 AND is_active = true)
	`

	result, err := db.client.ExecContext(ctx, query, organisationID, planID)
	if err != nil {
		return fmt.Errorf("failed to update organisation plan: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("plan not found or not active")
	}

	return nil
}

// GetOrganisationPlanID returns the current plan ID for an organisation.
func (db *DB) GetOrganisationPlanID(ctx context.Context, organisationID string) (string, error) {
	query := `
		SELECT plan_id
		FROM organisations
		WHERE id = $1
	`

	var planID string
	if err := db.client.QueryRowContext(ctx, query, organisationID).Scan(&planID); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("organisation not found")
		}
		return "", fmt.Errorf("failed to fetch organisation plan: %w", err)
	}

	return planID, nil
}

// SetStripeCustomerID stores the Stripe customer ID on an organisation.
func (db *DB) SetStripeCustomerID(ctx context.Context, organisationID, customerID string) error {
	_, err := db.client.ExecContext(ctx,
		`UPDATE organisations SET stripe_customer_id = $2, updated_at = NOW() WHERE id = $1`,
		organisationID, customerID,
	)
	if err != nil {
		return fmt.Errorf("failed to set stripe customer id: %w", err)
	}
	return nil
}

// GetStripeCustomerID returns the Stripe customer ID for an organisation, or "" if unset.
func (db *DB) GetStripeCustomerID(ctx context.Context, organisationID string) (string, error) {
	var id sql.NullString
	err := db.client.QueryRowContext(ctx,
		`SELECT stripe_customer_id FROM organisations WHERE id = $1`,
		organisationID,
	).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("organisation not found")
		}
		return "", fmt.Errorf("failed to fetch stripe customer id: %w", err)
	}
	if !id.Valid {
		return "", nil
	}
	return id.String, nil
}

// GetOrganisationIDByStripeCustomerID returns the organisation ID for a Stripe customer.
// Returns ErrOrganisationNotFound (sentinel) when no row matches — callers can
// use errors.Is to ACK Stripe webhooks for unknown customers without retries.
func (db *DB) GetOrganisationIDByStripeCustomerID(ctx context.Context, customerID string) (string, error) {
	var orgID string
	err := db.client.QueryRowContext(ctx,
		`SELECT id FROM organisations WHERE stripe_customer_id = $1`,
		customerID,
	).Scan(&orgID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("no organisation for stripe customer %s: %w", customerID, ErrOrganisationNotFound)
		}
		return "", fmt.Errorf("failed to lookup organisation by stripe customer: %w", err)
	}
	return orgID, nil
}

// SetStripeSubscriptionID stores the Stripe subscription ID on an organisation.
// Pass "" to clear the column (stored as NULL) — used when a subscription is
// cancelled so a future Checkout can create a fresh subscription.
func (db *DB) SetStripeSubscriptionID(ctx context.Context, organisationID, subscriptionID string) error {
	var v sql.NullString
	if subscriptionID != "" {
		v = sql.NullString{String: subscriptionID, Valid: true}
	}
	_, err := db.client.ExecContext(ctx,
		`UPDATE organisations SET stripe_subscription_id = $2, updated_at = NOW() WHERE id = $1`,
		organisationID, v,
	)
	if err != nil {
		return fmt.Errorf("failed to set stripe subscription id: %w", err)
	}
	return nil
}

// GetStripeSubscriptionID returns the Stripe subscription ID for an organisation,
// or "" if the org has no active subscription stored.
func (db *DB) GetStripeSubscriptionID(ctx context.Context, organisationID string) (string, error) {
	var id sql.NullString
	err := db.client.QueryRowContext(ctx,
		`SELECT stripe_subscription_id FROM organisations WHERE id = $1`,
		organisationID,
	).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("organisation not found")
		}
		return "", fmt.Errorf("failed to fetch stripe subscription id: %w", err)
	}
	if !id.Valid {
		return "", nil
	}
	return id.String, nil
}

// GetPlanByStripePriceID returns the plan with the given Stripe Price ID.
// Returns ErrPlanNotFound (sentinel) when no row matches — webhook handlers
// use this to ACK events for prices that aren't configured locally.
func (db *DB) GetPlanByStripePriceID(ctx context.Context, priceID string) (*Plan, error) {
	var p Plan
	err := db.client.QueryRowContext(ctx,
		`SELECT id, name, display_name, daily_page_limit, monthly_price_cents,
		        is_active, sort_order, created_at, COALESCE(stripe_price_id, '')
		 FROM plans WHERE stripe_price_id = $1`,
		priceID,
	).Scan(
		&p.ID, &p.Name, &p.DisplayName, &p.DailyPageLimit, &p.MonthlyPriceCents,
		&p.IsActive, &p.SortOrder, &p.CreatedAt, &p.StripePriceID,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no plan for stripe price %s: %w", priceID, ErrPlanNotFound)
		}
		return nil, fmt.Errorf("failed to lookup plan by stripe price id: %w", err)
	}
	return &p, nil
}

// GetFreePlanID returns the plan ID for the 'free' plan.
func (db *DB) GetFreePlanID(ctx context.Context) (string, error) {
	var id string
	err := db.client.QueryRowContext(ctx,
		`SELECT id FROM plans WHERE name = 'free' AND is_active = true LIMIT 1`,
	).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("free plan not found")
		}
		return "", fmt.Errorf("failed to fetch free plan id: %w", err)
	}
	return id, nil
}

// ListDailyUsage returns daily usage rows for an organisation within a date range.
func (db *DB) ListDailyUsage(ctx context.Context, organisationID string, startDate, endDate time.Time) ([]DailyUsageEntry, error) {
	query := `
		SELECT usage_date, pages_processed, jobs_created
		FROM daily_usage
		WHERE organisation_id = $1
		  AND usage_date >= $2
		  AND usage_date <= $3
		ORDER BY usage_date DESC
	`

	rows, err := db.client.QueryContext(ctx, query, organisationID, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to list daily usage: %w", err)
	}
	defer rows.Close()

	var entries []DailyUsageEntry
	for rows.Next() {
		var entry DailyUsageEntry
		if err := rows.Scan(&entry.UsageDate, &entry.PagesProcessed, &entry.JobsCreated); err != nil {
			return nil, fmt.Errorf("failed to scan daily usage: %w", err)
		}
		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate daily usage: %w", err)
	}

	return entries, nil
}
