package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// contextKey is used for storing values in request context
type contextKey string

const requestIDKey contextKey = "request_id"

// RequestIDMiddleware adds a unique request ID to each request
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if request ID already exists in headers (from load balancers, etc.)
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
		}

		// Add request ID to response headers
		w.Header().Set("X-Request-ID", requestID)

		// Add request ID to request context
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		r = r.WithContext(ctx)

		// Log the incoming request (skip health checks to reduce noise)
		if r.URL.Path != "/health" {
			apiLog.Info("Incoming request",
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
				"user_agent", r.UserAgent(),
			)
		}

		next.ServeHTTP(w, r)
	})
}

// GetRequestID retrieves the request ID from the request context
func GetRequestID(r *http.Request) string {
	if requestID, ok := r.Context().Value(requestIDKey).(string); ok {
		return requestID
	}
	return ""
}

// generateRequestID creates a unique request ID
func generateRequestID() string {
	// Use timestamp + random bytes for uniqueness
	timestamp := time.Now().UnixNano()

	// Generate 4 random bytes
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		// Fallback to timestamp only if random generation fails
		return fmt.Sprintf("%x", timestamp)
	}

	return fmt.Sprintf("%x-%x", timestamp, randomBytes)
}

// LoggingMiddleware logs request details and response times
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := GetRequestID(r)

		// Wrap the ResponseWriter to capture status code
		wrapper := &responseWrapper{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
			requestID:      requestID,
			requestPath:    r.URL.Path,
			requestMethod:  r.Method,
		}

		next.ServeHTTP(wrapper, r)

		duration := time.Since(start)

		// Log the completed request (skip health checks to reduce noise)
		if r.URL.Path != "/health" {
			apiLog.Info("Request completed",
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapper.statusCode,
				"duration", duration,
			)
		}
	})
}

// responseWrapper wraps http.ResponseWriter to capture status code
type responseWrapper struct {
	http.ResponseWriter
	statusCode    int
	wroteHeader   bool
	requestID     string
	requestPath   string
	requestMethod string
}

func (rw *responseWrapper) WriteHeader(code int) {
	if rw.wroteHeader {
		// Duplicate WriteHeader call detected - log with stack trace
		apiLog.Warn("DIAGNOSTIC: Blocked duplicate WriteHeader call",
			"request_id", rw.requestID,
			"method", rw.requestMethod,
			"path", rw.requestPath,
			"previous_code", rw.statusCode,
			"attempted_code", code,
			"stack_trace", string(debug.Stack()),
		)
		return
	}

	// Log the first WriteHeader call for diagnostic purposes
	// Only log for non-health-check paths to reduce noise
	if rw.requestPath != "/health" {
		apiLog.Debug("DIAGNOSTIC: WriteHeader called",
			"request_id", rw.requestID,
			"method", rw.requestMethod,
			"path", rw.requestPath,
			"status_code", code,
		)
	}

	rw.statusCode = code
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWrapper) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		// Mimic net/http behaviour: implicit 200 when Write is called before WriteHeader
		rw.WriteHeader(rw.statusCode)
	}
	return rw.ResponseWriter.Write(b)
}

// CORSMiddleware adds CORS headers for browser requests
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")

		// Handle preflight requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// CrossOriginProtectionMiddleware provides protection against CSRF attacks.
// It is a wrapper around Go's experimental http.CrossOriginProtection.
func CrossOriginProtectionMiddleware(next http.Handler) http.Handler {
	protector := http.NewCrossOriginProtection()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
			return
		}

		protector.Handler(next).ServeHTTP(w, r)
	})
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func addSupabaseConnectOrigins(values []string, rawURL string) []string {
	rawURL = strings.TrimSpace(strings.TrimSuffix(rawURL, "/"))
	if rawURL == "" {
		return values
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return values
	}

	httpsOrigin := "https://" + parsed.Host
	values = appendUnique(values, httpsOrigin)

	wsScheme := "wss"
	if parsed.Scheme == "http" {
		wsScheme = "ws"
	}
	values = appendUnique(values, wsScheme+"://"+parsed.Host)

	return values
}

func buildConnectSrcValues() string {
	values := []string{
		"'self'",
		"http://127.0.0.1:8765",
		"http://localhost:8765",
		"https://challenges.cloudflare.com",
		"https://cdn.jsdelivr.net",
		"https://www.google-analytics.com",
		"https://www.googletagmanager.com",
		"https://analytics.google.com",
		"https://*.sentry.io",
		"https://*.ingest.sentry.io",
		"https://browser.sentry-cdn.com",
		"https://cloudflareinsights.com",
	}

	values = addSupabaseConnectOrigins(values, os.Getenv("SUPABASE_AUTH_URL"))
	values = addSupabaseConnectOrigins(values, os.Getenv("SUPABASE_FALLBACK_AUTH_URL"))
	values = addSupabaseConnectOrigins(values, os.Getenv("SUPABASE_LEGACY_AUTH_URL"))

	return strings.Join(values, " ")
}

func isWebflowExtensionSurfacePage(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.URL.Query().Get("surface") != "webflow-extension" {
		return false
	}

	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/dashboard":
		return true
	case path == "/settings":
		return true
	case strings.HasPrefix(path, "/settings/"):
		return true
	case strings.HasPrefix(path, "/jobs/"):
		return true
	default:
		return false
	}
}

// SecurityHeadersMiddleware adds security-related headers
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	connectSrcValues := buildConnectSrcValues()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")

		frameAncestors := "'none'"
		if isWebflowExtensionSurfacePage(r) {
			frameAncestors = "'self' https://webflow.com https://*.webflow.com http://localhost:1337 http://127.0.0.1:1337"
		} else {
			w.Header().Set("X-Frame-Options", "DENY")
		}

		// Content Security Policy
		csp := fmt.Sprintf(`
			default-src 'self';
			script-src 'self' 'unsafe-inline' https://challenges.cloudflare.com https://unpkg.com https://cdn.jsdelivr.net https://www.googletagmanager.com https://browser.sentry-cdn.com https://static.cloudflareinsights.com;
			style-src 'self' 'unsafe-inline';
			connect-src %s;
			frame-src https://challenges.cloudflare.com;
			frame-ancestors %s;
			img-src 'self' data: https://www.google-analytics.com https://www.googletagmanager.com https://ssl.gstatic.com https://www.gravatar.com https://lh3.googleusercontent.com https://*.googleusercontent.com;
			font-src 'self' data:;
			object-src 'none';
			base-uri 'self';
			form-action 'self';
		`, connectSrcValues, frameAncestors)
		w.Header().Set("Content-Security-Policy", strings.ReplaceAll(csp, "\n", " "))

		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}
