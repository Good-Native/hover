package notifications

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"time"

	"github.com/good-native/hover/internal/logging"
	"github.com/lib/pq"
)

var notifyLog = logging.Component("notify")

type Listener struct {
	connStr string
	service *Service
}

// Returns nil when service is nil; callers must check.
func NewListener(connStr string, service *Service) *Listener {
	if service == nil {
		notifyLog.Error("Cannot create notification listener: service is nil")
		return nil
	}
	return &Listener{
		connStr: connStr,
		service: service,
	}
}

func (l *Listener) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			notifyLog.Info("Notification listener stopped")
			return
		default:
			if err := l.listen(ctx); err != nil {
				notifyLog.Warn("Notification listener error, retrying in 5s", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}
		}
	}
}

func (l *Listener) listen(ctx context.Context) error {
	listener := pq.NewListener(l.connStr, 10*time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			notifyLog.Warn("Notification listener event error", "error", err)
		}
	})
	defer listener.Close()

	if err := listener.Listen("new_notification"); err != nil {
		return err
	}

	notifyLog.Info("Notification listener started (real-time mode)")

	// Sweep on startup in case any arrived while we were down.
	l.processPending(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil

		case notification := <-listener.Notify:
			if notification == nil {
				return nil
			}

			notifyLog.Debug("Received notification",
				"channel", notification.Channel,
				"payload", notification.Extra,
			)

			// Process all pending — payload carries one ID but bursts can coalesce.
			l.processPending(ctx)

		case <-time.After(90 * time.Second):
			if err := listener.Ping(); err != nil {
				return err
			}
		}
	}
}

func (l *Listener) processPending(ctx context.Context) {
	if err := l.service.ProcessPendingNotifications(ctx, 50); err != nil {
		if ctx.Err() == nil {
			notifyLog.Warn("Failed to process pending notifications", "error", err)
		}
	}
}

// PgBouncer in transaction mode kills LISTEN — DATABASE_DIRECT_URL is the escape hatch.
func StartWithFallback(ctx context.Context, connStr string, service *Service) {
	directURL := os.Getenv("DATABASE_DIRECT_URL")
	if directURL != "" {
		if testConnection(directURL) {
			listener := NewListener(directURL, service)
			if listener != nil {
				notifyLog.Info("Notification listener started (real-time via DATABASE_DIRECT_URL)")
				go listener.Start(ctx)
				return
			}
		} else {
			notifyLog.Warn("DATABASE_DIRECT_URL connection failed, falling back to polling")
		}
	}

	listener := NewListener(connStr, service)
	if listener == nil {
		// nil service — starting polling would panic on the first tick.
		notifyLog.Warn("Notification listener not created; notifications disabled")
		return
	}

	if canUseListen(connStr) {
		go listener.Start(ctx)
		return
	}

	notifyLog.Info("Using polling mode for notifications (connection pooler detected)")
	go startPolling(ctx, service)
}

func testConnection(connStr string) bool {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		notifyLog.Debug("Failed to open direct connection", "error", err)
		return false
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		notifyLog.Debug("Failed to ping direct connection", "error", err)
		return false
	}
	return true
}

// Heuristic: Supabase pooler hosts contain "pooler"; PgBouncer defaults to :6543.
func canUseListen(connStr string) bool {
	if strings.Contains(connStr, "pooler") {
		return false
	}
	if strings.Contains(connStr, ":6543") {
		return false
	}
	return true
}

func startPolling(ctx context.Context, service *Service) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	notifyLog.Info("Notification processor started (polling mode)")

	for {
		select {
		case <-ctx.Done():
			notifyLog.Info("Notification processor stopped")
			return
		case <-ticker.C:
			if err := service.ProcessPendingNotifications(ctx, 50); err != nil {
				if ctx.Err() == nil {
					notifyLog.Warn("Failed to process pending notifications", "error", err)
				}
			}
		}
	}
}
