package notifications

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/good-native/hover/internal/db"
	"github.com/slack-go/slack"
)

const slackAPITimeout = 10 * time.Second

// Service handles notification delivery to various channels.
// Note: Notifications are created by PostgreSQL triggers, not Go code.
type Service struct {
	db       NotificationDB
	channels []DeliveryChannel
}

// NotificationDB defines the database operations needed by the service
type NotificationDB interface {
	GetPendingSlackNotifications(ctx context.Context, limit int) ([]*db.Notification, error)
	MarkNotificationDelivered(ctx context.Context, notificationID, channel string) error
	GetSlackConnectionsForOrg(ctx context.Context, organisationID string) ([]*db.SlackConnection, error)
	GetEnabledUserLinksForConnection(ctx context.Context, connectionID string) ([]*db.SlackUserLink, error)
}

// DeliveryChannel defines the interface for notification delivery
type DeliveryChannel interface {
	Name() string
	Deliver(ctx context.Context, n *db.Notification) error
}

// NewService creates a notification service
func NewService(database NotificationDB) *Service {
	return &Service{db: database}
}

// AddChannel adds a delivery channel to the service
func (s *Service) AddChannel(ch DeliveryChannel) {
	s.channels = append(s.channels, ch)
}

// ProcessPendingNotifications delivers pending notifications to all channels
func (s *Service) ProcessPendingNotifications(ctx context.Context, limit int) error {
	for _, ch := range s.channels {
		if err := s.deliverToChannel(ctx, ch, limit); err != nil {
			notifyLog.Warn("Failed to deliver notifications", "error", err, "channel", ch.Name())
		}
	}
	return nil
}

func (s *Service) deliverToChannel(ctx context.Context, ch DeliveryChannel, limit int) error {
	var notifications []*db.Notification
	var err error

	switch ch.Name() {
	case "slack":
		notifications, err = s.db.GetPendingSlackNotifications(ctx, limit)
	default:
		notifyLog.Debug("Unknown delivery channel, skipping", "channel", ch.Name())
		return nil
	}

	if err != nil {
		return err
	}

	for _, n := range notifications {
		if err := ch.Deliver(ctx, n); err != nil {
			notifyLog.Warn("Failed to deliver notification",
				"error", err,
				"notification_id", n.ID,
				"channel", ch.Name(),
			)
			continue
		}

		if err := s.db.MarkNotificationDelivered(ctx, n.ID, ch.Name()); err != nil {
			notifyLog.Warn("Failed to mark notification delivered",
				"error", err,
				"notification_id", n.ID,
			)
		}
	}

	return nil
}

// SlackChannel implements the DeliveryChannel interface for Slack
type SlackChannel struct {
	db SlackDB
}

// SlackDB defines Slack-specific database operations
type SlackDB interface {
	GetSlackConnectionsForOrg(ctx context.Context, organisationID string) ([]*db.SlackConnection, error)
	GetEnabledUserLinksForConnection(ctx context.Context, connectionID string) ([]*db.SlackUserLink, error)
	GetSlackToken(ctx context.Context, connectionID string) (string, error)
}

// NewSlackChannel creates a new Slack delivery channel
func NewSlackChannel(database SlackDB) (*SlackChannel, error) {
	if database == nil {
		return nil, fmt.Errorf("database cannot be nil")
	}
	return &SlackChannel{db: database}, nil
}

// Name returns the channel name
func (c *SlackChannel) Name() string {
	return "slack"
}

// Deliver sends a notification to Slack
func (c *SlackChannel) Deliver(ctx context.Context, n *db.Notification) error {
	connections, err := c.db.GetSlackConnectionsForOrg(ctx, n.OrganisationID)
	if err != nil {
		return fmt.Errorf("failed to fetch Slack connections: %w", err)
	}

	if len(connections) == 0 {
		return nil
	}

	var lastErr error
	for _, conn := range connections {
		if err := c.deliverToConnection(ctx, conn, n); err != nil {
			notifyLog.Warn("Failed to deliver to Slack workspace",
				"error", err,
				"workspace_id", conn.WorkspaceID,
				"notification_id", n.ID,
			)
			lastErr = err
		}
	}
	return lastErr
}

func (c *SlackChannel) deliverToConnection(ctx context.Context, conn *db.SlackConnection, n *db.Notification) error {
	// Get token from Supabase Vault
	token, err := c.db.GetSlackToken(ctx, conn.ID)
	if err != nil {
		return fmt.Errorf("failed to get access token from vault: %w", err)
	}

	client := slack.New(token)

	links, err := c.db.GetEnabledUserLinksForConnection(ctx, conn.ID)
	if err != nil {
		return fmt.Errorf("failed to get user links: %w", err)
	}

	if len(links) == 0 {
		return nil
	}

	blocks := c.buildMessageBlocks(n)
	fallbackText := fmt.Sprintf("%s: %s", n.Subject, n.Preview)

	var lastErr error
	for _, link := range links {
		// Use timeout context to prevent hanging on Slack API calls
		msgCtx, cancel := context.WithTimeout(ctx, slackAPITimeout)
		_, _, err := client.PostMessageContext(
			msgCtx,
			link.SlackUserID,
			slack.MsgOptionBlocks(blocks...),
			slack.MsgOptionText(fallbackText, false),
		)
		cancel()
		if err != nil {
			notifyLog.Warn("Failed to send Slack DM",
				"error", err,
				"slack_user_id", link.SlackUserID,
				"notification_id", n.ID,
			)
			lastErr = err
		} else {
			notifyLog.Info("Slack DM sent",
				"slack_user_id", link.SlackUserID,
				"notification_id", n.ID,
				"workspace_name", conn.WorkspaceName,
			)
		}
	}

	return lastErr
}

// buildMessageBlocks creates Slack Block Kit blocks from notification fields.
// The notification already contains formatted content (emoji, text) from the DB trigger.
func (c *SlackChannel) buildMessageBlocks(n *db.Notification) []slack.Block {
	appURL := os.Getenv("APP_URL")
	if appURL == "" {
		appURL = "https://hover.app.goodnative.co"
	}

	// Subject block (already includes emoji from DB)
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*%s*", n.Subject), false, false),
			nil,
			nil,
		),
	}

	// Preview block (short summary)
	if n.Preview != "" {
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", n.Preview, false, false),
			nil,
			nil,
		))
	}

	// Message block (detailed stats)
	if n.Message != "" {
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", "```\n"+n.Message+"\n```", false, false),
			nil,
			nil,
		))
	}

	// Button block - relative paths get APP_URL prepended, absolute URLs used as-is
	if n.Link != "" {
		linkURL := n.Link
		if strings.HasPrefix(n.Link, "/") {
			linkURL = appURL + n.Link
		}
		blocks = append(blocks, slack.NewActionBlock(
			"",
			slack.NewButtonBlockElement(
				"view_details",
				"view_details",
				slack.NewTextBlockObject("plain_text", "View details", false, false),
			).WithURL(linkURL),
		))
	}

	return blocks
}
