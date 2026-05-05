package api

import (
	"net/http"

	"github.com/good-native/hover/internal/logging"
)

var apiLog = logging.Component("api")

func loggerWithRequest(r *http.Request) *logging.Logger {
	if r == nil {
		return apiLog
	}
	return apiLog.With(
		"request_id", GetRequestID(r),
		"method", r.Method,
		"path", r.URL.Path,
	)
}

// Use for routes whose raw URL path embeds a secret (e.g. legacy
// webhook-token URLs) so credentials don't reach logs or Sentry.
func loggerWithRequestPath(r *http.Request, path string) *logging.Logger {
	if r == nil {
		return apiLog.With("path", path)
	}
	return apiLog.With(
		"request_id", GetRequestID(r),
		"method", r.Method,
		"path", path,
	)
}
