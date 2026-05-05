package api

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/good-native/hover/internal/auth"
	"github.com/good-native/hover/internal/broker"
	"github.com/good-native/hover/internal/logging"
	"github.com/lib/pq"
)

func organisationIDOrNone(orgID *string) string {
	if orgID == nil {
		return "none"
	}
	return *orgID
}

// Requires admin JWT and ALLOW_DB_RESET=true.
func (h *Handler) AdminResetDatabase(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if os.Getenv("ALLOW_DB_RESET") != "true" {
		Forbidden(w, r, "Database reset not enabled. Set ALLOW_DB_RESET=true to enable")
		return
	}

	claims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "Authentication required for admin endpoint")
		return
	}

	if !hasSystemAdminRole(claims) {
		logger.Warn("Non-system-admin user attempted to access database reset endpoint", "user_id", claims.UserID)
		Forbidden(w, r, "System administrator privileges required")
		return
	}

	user, err := h.DB.GetUser(claims.UserID)
	if err != nil {
		logger.Error("Failed to verify admin user", "error", err, "user_id", claims.UserID)
		Unauthorised(w, r, "User verification failed")
		return
	}

	logger.Warn("Admin database reset requested",
		"user_id", user.ID,
		"organisation_id", organisationIDOrNone(user.OrganisationID),
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
	)

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("event_type", "admin_action")
		scope.SetTag("action", "database_reset")
		scope.SetUser(sentry.User{
			ID:    user.ID,
			Email: user.Email,
		})
		scope.SetContext("admin_action", map[string]any{
			"endpoint":   "/v1/admin/reset-db",
			"user_agent": r.Header.Get("User-Agent"),
			"ip_address": r.RemoteAddr,
		})
		sentry.CaptureMessage("Admin database reset action")
	})

	resetStart := time.Now()
	if err := h.DB.ResetSchema(); err != nil {
		resetDuration := time.Since(resetStart)
		logger.Error("Failed to reset database schema", "error", err, "user_id", user.ID, "duration", resetDuration)

		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetLevel(sentry.LevelError)
			scope.SetTag("event_type", "admin_action_failed")
			scope.SetTag("action", "database_reset")
			scope.SetUser(sentry.User{
				ID:    user.ID,
				Email: user.Email,
			})
			scope.SetContext("error_details", map[string]any{
				"error":    err.Error(),
				"duration": resetDuration.Milliseconds(),
			})
			sentry.CaptureException(err)
		})

		InternalError(w, r, err)
		return
	}

	resetDuration := time.Since(resetStart)
	logger.Warn("Database schema reset completed successfully by admin", "user_id", user.ID, "duration", resetDuration)

	// Postgres truncated above, so the SCAN+DEL pass can't race new dispatch.
	redisCleared, redisKeysDeleted, redisErr := clearBrokerState(r.Context(), h.Broker, logger, user.ID)

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelInfo)
		scope.SetTag("event_type", "admin_action_success")
		scope.SetTag("action", "database_reset")
		scope.SetUser(sentry.User{
			ID:    user.ID,
			Email: user.Email,
		})
		scope.SetContext("success_details", map[string]any{
			"duration_ms":        resetDuration.Milliseconds(),
			"timestamp":          time.Now().UTC().Format(time.RFC3339),
			"redis_cleared":      redisCleared,
			"redis_keys_deleted": redisKeysDeleted,
		})
		sentry.CaptureMessage("Database reset completed successfully")
	})

	payload := map[string]any{
		"redis_cleared":      redisCleared,
		"redis_keys_deleted": redisKeysDeleted,
	}
	msg := "Database schema reset successfully - Supabase will rebuild from migrations"
	if redisErr != nil {
		payload["redis_error"] = redisErr.Error()
		msg = "Database schema reset; Redis clear failed - flush manually"
	}
	WriteSuccess(w, r, payload, msg)
}

// AdminResetData handles the admin data-only reset endpoint
// Clears all data but preserves schema - safe option for clearing test data
func (h *Handler) AdminResetData(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if os.Getenv("ALLOW_DB_RESET") != "true" {
		Forbidden(w, r, "Database operations not enabled. Set ALLOW_DB_RESET=true to enable")
		return
	}

	claims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "Authentication required for admin endpoint")
		return
	}

	if !hasSystemAdminRole(claims) {
		logger.Warn("Non-system-admin user attempted to access data reset endpoint", "user_id", claims.UserID)
		Forbidden(w, r, "System administrator privileges required")
		return
	}

	user, err := h.DB.GetUser(claims.UserID)
	if err != nil {
		logger.Error("Failed to verify admin user", "error", err, "user_id", claims.UserID)
		Unauthorised(w, r, "User verification failed")
		return
	}

	logger.Warn("Admin data-only reset requested",
		"user_id", user.ID,
		"organisation_id", organisationIDOrNone(user.OrganisationID),
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
	)

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("event_type", "admin_action")
		scope.SetTag("action", "data_reset")
		scope.SetUser(sentry.User{
			ID:    user.ID,
			Email: user.Email,
		})
		scope.SetContext("admin_action", map[string]any{
			"endpoint":   "/v1/admin/reset-data",
			"user_agent": r.Header.Get("User-Agent"),
			"ip_address": r.RemoteAddr,
		})
		sentry.CaptureMessage("Admin data reset action")
	})

	resetStart := time.Now()
	if err := h.DB.ResetDataOnly(); err != nil {
		resetDuration := time.Since(resetStart)
		logger.Error("Failed to reset data", "error", err, "user_id", user.ID, "duration", resetDuration)

		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetLevel(sentry.LevelError)
			scope.SetTag("event_type", "admin_action_failed")
			scope.SetTag("action", "data_reset")
			scope.SetUser(sentry.User{
				ID:    user.ID,
				Email: user.Email,
			})
			scope.SetContext("error_details", map[string]any{
				"error":    err.Error(),
				"duration": resetDuration.Milliseconds(),
			})
			sentry.CaptureException(err)
		})

		InternalError(w, r, err)
		return
	}

	resetDuration := time.Since(resetStart)
	logger.Warn("Data reset completed successfully by admin", "user_id", user.ID, "duration", resetDuration)

	// Postgres is empty by here; SCAN+DEL pass cannot race new dispatch.
	redisCleared, redisKeysDeleted, redisErr := clearBrokerState(r.Context(), h.Broker, logger, user.ID)

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelInfo)
		scope.SetTag("event_type", "admin_action_success")
		scope.SetTag("action", "data_reset")
		scope.SetUser(sentry.User{
			ID:    user.ID,
			Email: user.Email,
		})
		scope.SetContext("success_details", map[string]any{
			"duration_ms":        resetDuration.Milliseconds(),
			"timestamp":          time.Now().UTC().Format(time.RFC3339),
			"redis_cleared":      redisCleared,
			"redis_keys_deleted": redisKeysDeleted,
		})
		sentry.CaptureMessage("Data reset completed successfully")
	})

	payload := map[string]any{
		"redis_cleared":      redisCleared,
		"redis_keys_deleted": redisKeysDeleted,
	}
	msg := "Data cleared successfully - schema preserved"
	if redisErr != nil {
		payload["redis_error"] = redisErr.Error()
		msg = "Data cleared; Redis clear failed - flush manually"
	}
	WriteSuccess(w, r, payload, msg)
}

// One-off backfill sweep for terminal-status jobs whose Redis keys
// were never cleaned. Idempotent; gated on ALLOW_DB_RESET.
func (h *Handler) AdminReclaimRedis(w http.ResponseWriter, r *http.Request) {
	logger := loggerWithRequest(r)

	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if os.Getenv("ALLOW_DB_RESET") != "true" {
		Forbidden(w, r, "Reclaim not enabled. Set ALLOW_DB_RESET=true to enable")
		return
	}

	claims, ok := auth.GetUserFromContext(r.Context())
	if !ok {
		Unauthorised(w, r, "Authentication required for admin endpoint")
		return
	}
	if !hasSystemAdminRole(claims) {
		logger.Warn("Non-system-admin user attempted to access reclaim endpoint", "user_id", claims.UserID)
		Forbidden(w, r, "System administrator privileges required")
		return
	}

	if h.Broker == nil {
		BadRequest(w, r, "Redis broker not configured; nothing to reclaim")
		return
	}

	user, err := h.DB.GetUser(claims.UserID)
	if err != nil {
		logger.Error("Failed to verify admin user", "error", err, "user_id", claims.UserID)
		Unauthorised(w, r, "User verification failed")
		return
	}

	logger.Warn("Admin Redis reclaim requested",
		"user_id", user.ID,
		"organisation_id", organisationIDOrNone(user.OrganisationID),
		"remote_addr", r.RemoteAddr,
	)

	sqlDB := h.DB.GetDB()
	filter := terminalJobFilter(sqlDB)

	report, err := h.Broker.ReclaimTerminalJobKeys(r.Context(), filter)
	if err != nil {
		logger.Error("Reclaim sweep failed", "error", err, "user_id", user.ID)
		sentry.CaptureException(err)
		InternalError(w, r, err)
		return
	}

	logger.Warn("Redis reclaim completed",
		"user_id", user.ID,
		"candidates", report.CandidatesScanned,
		"terminal", report.TerminalJobs,
		"cleaned", report.Cleaned,
		"failed", report.Failed,
	)

	payload := map[string]any{
		"candidates_scanned": report.CandidatesScanned,
		"terminal_jobs":      report.TerminalJobs,
		"cleaned":            report.Cleaned,
		"failed":             report.Failed,
	}
	if report.FirstError != nil {
		payload["first_error"] = report.FirstError.Error()
	}
	WriteSuccess(w, r, payload, "Redis reclaim sweep completed")
}

// Treats missing-from-jobs as terminal (orphaned Redis state).
func terminalJobFilter(sqlDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}) broker.TerminalFilter {
	return func(ctx context.Context, jobIDs []string) ([]string, error) {
		if len(jobIDs) == 0 {
			return nil, nil
		}

		rows, err := sqlDB.QueryContext(ctx,
			`SELECT id FROM jobs
			   WHERE id = ANY($1)
			     AND status IN ('completed', 'failed', 'cancelled', 'archived')`,
			pq.Array(jobIDs))
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		alive := make(map[string]struct{}, len(jobIDs))
		known := make(map[string]struct{})
		var terminal []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			terminal = append(terminal, id)
			known[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		// Bound 'still active' so deletion-by-missing-row only fires
		// for genuine orphans.
		stillActive, err := lookupActiveJobs(ctx, sqlDB, jobIDs)
		if err != nil {
			return nil, err
		}
		for _, id := range stillActive {
			alive[id] = struct{}{}
		}
		for _, id := range jobIDs {
			if _, t := known[id]; t {
				continue
			}
			if _, a := alive[id]; a {
				continue
			}
			terminal = append(terminal, id)
		}
		return terminal, nil
	}
}

func lookupActiveJobs(ctx context.Context, sqlDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, jobIDs []string) ([]string, error) {
	rows, err := sqlDB.QueryContext(ctx,
		`SELECT id FROM jobs
		   WHERE id = ANY($1)
		     AND status NOT IN ('completed', 'failed', 'cancelled', 'archived')`,
		pq.Array(jobIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var active []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		active = append(active, id)
	}
	return active, rows.Err()
}

// Errors logged + sent to Sentry but do not abort — operator needs
// the HTTP reply to know whether to flush Redis manually.
func clearBrokerState(ctx context.Context, broker BrokerCleaner, logger *logging.Logger, userID string) (bool, int, error) {
	if broker == nil {
		logger.Debug("Reset: broker not configured, skipping Redis clear")
		return false, 0, nil
	}

	n, err := broker.ClearAll(ctx)
	if err != nil {
		logger.Error("Reset: cleared Postgres but Redis clear failed",
			"error", err, "user_id", userID)
		sentry.CaptureException(err)
		return false, n, err
	}

	logger.Info("Reset: cleared Redis broker state",
		"user_id", userID, "redis_keys_deleted", n)
	return true, n, nil
}

// system_role is distinct from organisation-level admin: Hover
// operators only, gates platform-level destructive ops.
func hasSystemAdminRole(claims *auth.UserClaims) bool {
	if claims == nil || claims.AppMetadata == nil {
		return false
	}
	if systemRole, exists := claims.AppMetadata["system_role"]; exists {
		if roleStr, ok := systemRole.(string); ok && roleStr == "system_admin" {
			return true
		}
	}
	return false
}
