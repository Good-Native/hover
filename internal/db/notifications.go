package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/good-native/hover/internal/logging"
)

var notificationsLog = logging.Component("db")

// NotificationType defines the types of notifications
type NotificationType string

const (
	NotificationJobComplete    NotificationType = "job_complete"
	NotificationJobFailed      NotificationType = "job_failed"
	NotificationSchedulerRun   NotificationType = "scheduler_run"
	NotificationSchedulerError NotificationType = "scheduler_error"
)

// Notification represents a notification record
type Notification struct {
	ID               string
	OrganisationID   string
	UserID           *string // nil for org-wide notifications
	Type             NotificationType
	Subject          string         // Main heading (e.g., "✅ Job completed: example.com")
	Preview          string         // Short summary for previews/toasts
	Message          string         // Full details (optional)
	Link             string         // URL path to view details (e.g., "/jobs/abc-123")
	Data             map[string]any // Additional structured data
	ReadAt           *time.Time
	SlackDeliveredAt *time.Time
	EmailDeliveredAt *time.Time
	CreatedAt        time.Time
}

// NotificationData is the structured payload for different notification types
type NotificationData struct {
	JobID          string `json:"job_id,omitempty"`
	Domain         string `json:"domain,omitempty"`
	CompletedTasks int    `json:"completed_tasks,omitempty"`
	FailedTasks    int    `json:"failed_tasks,omitempty"`
	Duration       string `json:"duration,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
	SchedulerID    string `json:"scheduler_id,omitempty"`
}

// Note: Notifications are created by PostgreSQL trigger (notify_job_status_change)
// when job status transitions to 'completed' or 'failed'. No Go code needed.

// GetNotification retrieves a notification by ID
func (db *DB) GetNotification(ctx context.Context, notificationID string) (*Notification, error) {
	n := &Notification{}
	var userID, preview, message, link sql.NullString
	var dataJSON []byte
	var readAt, slackDeliveredAt, emailDeliveredAt sql.NullTime

	query := `
		SELECT id, organisation_id, user_id, type, subject, preview, message, link, data,
		       read_at, slack_delivered_at, email_delivered_at, created_at
		FROM notifications
		WHERE id = $1
	`

	err := db.client.QueryRowContext(ctx, query, notificationID).Scan(
		&n.ID, &n.OrganisationID, &userID, &n.Type, &n.Subject, &preview, &message, &link, &dataJSON,
		&readAt, &slackDeliveredAt, &emailDeliveredAt, &n.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("notification not found")
		}
		return nil, fmt.Errorf("failed to get notification: %w", err)
	}

	if userID.Valid {
		n.UserID = &userID.String
	}
	if preview.Valid {
		n.Preview = preview.String
	}
	if message.Valid {
		n.Message = message.String
	}
	if link.Valid {
		n.Link = link.String
	}
	if readAt.Valid {
		n.ReadAt = &readAt.Time
	}
	if slackDeliveredAt.Valid {
		n.SlackDeliveredAt = &slackDeliveredAt.Time
	}
	if emailDeliveredAt.Valid {
		n.EmailDeliveredAt = &emailDeliveredAt.Time
	}
	if dataJSON != nil {
		if err := json.Unmarshal(dataJSON, &n.Data); err != nil {
			notificationsLog.Warn("Failed to unmarshal notification data", "error", err, "notification_id", notificationID)
		}
	}

	return n, nil
}

// ListNotifications retrieves notifications for an organisation
func (db *DB) ListNotifications(ctx context.Context, organisationID string, limit, offset int, unreadOnly bool) ([]*Notification, int, error) {
	var whereClause string
	args := []any{organisationID}
	argIndex := 2

	if unreadOnly {
		whereClause = "WHERE organisation_id = $1 AND read_at IS NULL"
	} else {
		whereClause = "WHERE organisation_id = $1"
	}

	// Count total
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM notifications %s`, whereClause) //nolint:gosec // whereClause is whitelisted internal string
	var total int
	if err := db.client.QueryRowContext(ctx, countQuery, organisationID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count notifications: %w", err)
	}

	// Fetch notifications
	// #nosec G201
	query := fmt.Sprintf(`
		SELECT id, organisation_id, user_id, type, subject, preview, message, link, data,
		       read_at, slack_delivered_at, email_delivered_at, created_at
		FROM notifications
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIndex, argIndex+1)
	args = append(args, limit, offset)

	rows, err := db.client.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list notifications: %w", err)
	}
	defer rows.Close()

	var notifications []*Notification
	for rows.Next() {
		n := &Notification{}
		var userID, preview, message, link sql.NullString
		var dataJSON []byte
		var readAt, slackDeliveredAt, emailDeliveredAt sql.NullTime

		err := rows.Scan(
			&n.ID, &n.OrganisationID, &userID, &n.Type, &n.Subject, &preview, &message, &link, &dataJSON,
			&readAt, &slackDeliveredAt, &emailDeliveredAt, &n.CreatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan notification: %w", err)
		}

		if userID.Valid {
			n.UserID = &userID.String
		}
		if preview.Valid {
			n.Preview = preview.String
		}
		if message.Valid {
			n.Message = message.String
		}
		if link.Valid {
			n.Link = link.String
		}
		if readAt.Valid {
			n.ReadAt = &readAt.Time
		}
		if slackDeliveredAt.Valid {
			n.SlackDeliveredAt = &slackDeliveredAt.Time
		}
		if emailDeliveredAt.Valid {
			n.EmailDeliveredAt = &emailDeliveredAt.Time
		}
		if dataJSON != nil {
			n.Data = make(map[string]any)
			if err := json.Unmarshal(dataJSON, &n.Data); err != nil {
				notificationsLog.Warn("Failed to unmarshal notification data", "error", err, "notification_id", n.ID)
			}
		}

		notifications = append(notifications, n)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("error iterating notifications: %w", err)
	}

	return notifications, total, nil
}

// MarkNotificationRead marks a notification as read
func (db *DB) MarkNotificationRead(ctx context.Context, notificationID, organisationID string) error {
	query := `
		UPDATE notifications
		SET read_at = NOW()
		WHERE id = $1 AND organisation_id = $2
	`

	result, err := db.client.ExecContext(ctx, query, notificationID, organisationID)
	if err != nil {
		return fmt.Errorf("failed to mark notification read: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("notification not found")
	}

	return nil
}

// MarkAllNotificationsRead marks all notifications as read for an organisation
func (db *DB) MarkAllNotificationsRead(ctx context.Context, organisationID string) error {
	query := `
		UPDATE notifications
		SET read_at = NOW()
		WHERE organisation_id = $1 AND read_at IS NULL
	`

	_, err := db.client.ExecContext(ctx, query, organisationID)
	if err != nil {
		return fmt.Errorf("failed to mark all notifications read: %w", err)
	}

	return nil
}

// GetPendingSlackNotifications retrieves notifications not yet delivered to Slack
func (db *DB) GetPendingSlackNotifications(ctx context.Context, limit int) ([]*Notification, error) {
	query := `
		SELECT n.id, n.organisation_id, n.user_id, n.type, n.subject, n.preview, n.message, n.link, n.data,
		       n.read_at, n.slack_delivered_at, n.email_delivered_at, n.created_at
		FROM notifications n
		WHERE n.slack_delivered_at IS NULL
		  AND EXISTS (SELECT 1 FROM slack_connections sc WHERE sc.organisation_id = n.organisation_id)
		ORDER BY n.created_at ASC
		LIMIT $1
	`

	rows, err := db.client.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending Slack notifications: %w", err)
	}
	defer rows.Close()

	var notifications []*Notification
	for rows.Next() {
		n := &Notification{}
		var userID, preview, message, link sql.NullString
		var dataJSON []byte
		var readAt, slackDeliveredAt, emailDeliveredAt sql.NullTime

		err := rows.Scan(
			&n.ID, &n.OrganisationID, &userID, &n.Type, &n.Subject, &preview, &message, &link, &dataJSON,
			&readAt, &slackDeliveredAt, &emailDeliveredAt, &n.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan notification: %w", err)
		}

		if userID.Valid {
			n.UserID = &userID.String
		}
		if preview.Valid {
			n.Preview = preview.String
		}
		if message.Valid {
			n.Message = message.String
		}
		if link.Valid {
			n.Link = link.String
		}
		if readAt.Valid {
			n.ReadAt = &readAt.Time
		}
		if slackDeliveredAt.Valid {
			n.SlackDeliveredAt = &slackDeliveredAt.Time
		}
		if emailDeliveredAt.Valid {
			n.EmailDeliveredAt = &emailDeliveredAt.Time
		}
		if dataJSON != nil {
			n.Data = make(map[string]any)
			if err := json.Unmarshal(dataJSON, &n.Data); err != nil {
				notificationsLog.Warn("Failed to unmarshal notification data", "error", err, "notification_id", n.ID)
			}
		}

		notifications = append(notifications, n)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating notifications: %w", err)
	}

	return notifications, nil
}

// MarkNotificationDelivered marks a notification as delivered to a channel
func (db *DB) MarkNotificationDelivered(ctx context.Context, notificationID, channel string) error {
	var column string
	switch channel {
	case "slack":
		column = "slack_delivered_at"
	case "email":
		column = "email_delivered_at"
	default:
		return fmt.Errorf("unknown channel: %s", channel)
	}

	// #nosec G201
	query := fmt.Sprintf(`
		UPDATE notifications
		SET %s = NOW()
		WHERE id = $1
	`, column)

	_, err := db.client.ExecContext(ctx, query, notificationID)
	if err != nil {
		return fmt.Errorf("failed to mark notification delivered: %w", err)
	}

	return nil
}

// GetUnreadCount returns the count of unread notifications for an organisation
func (db *DB) GetUnreadNotificationCount(ctx context.Context, organisationID string) (int, error) {
	query := `SELECT COUNT(*) FROM notifications WHERE organisation_id = $1 AND read_at IS NULL`

	var count int
	err := db.client.QueryRowContext(ctx, query, organisationID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get unread count: %w", err)
	}

	return count, nil
}
