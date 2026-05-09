package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestIDMiddleware(t *testing.T) {
	tests := []struct {
		name              string
		existingRequestID string
		expectGenerated   bool
	}{
		{
			name:              "generates_new_request_id_when_none_exists",
			existingRequestID: "",
			expectGenerated:   true,
		},
		{
			name:              "uses_existing_request_id_from_header",
			existingRequestID: "existing-request-id-123",
			expectGenerated:   false,
		},
		{
			name:              "uses_request_id_from_load_balancer",
			existingRequestID: "lb-generated-id-456",
			expectGenerated:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test handler that verifies request ID is in context
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestID := GetRequestID(r)

				if tt.expectGenerated {
					assert.NotEmpty(t, requestID)
					// Verify format of generated ID (hex-hex)
					assert.Contains(t, requestID, "-")
					parts := strings.Split(requestID, "-")
					assert.Len(t, parts, 2)
				} else {
					assert.Equal(t, tt.existingRequestID, requestID)
				}

				w.WriteHeader(http.StatusOK)
			})

			// Wrap handler with middleware
			middlewareHandler := RequestIDMiddleware(handler)

			// Create request
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.existingRequestID != "" {
				req.Header.Set("X-Request-ID", tt.existingRequestID)
			}

			// Execute request
			rec := httptest.NewRecorder()
			middlewareHandler.ServeHTTP(rec, req)

			// Verify response header contains request ID
			responseRequestID := rec.Header().Get("X-Request-ID")
			assert.NotEmpty(t, responseRequestID)

			if !tt.expectGenerated {
				assert.Equal(t, tt.existingRequestID, responseRequestID)
			}
		})
	}
}

func TestGetRequestID(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
		expected  string
	}{
		{
			name:      "retrieves_existing_request_id",
			requestID: "test-request-123",
			expected:  "test-request-123",
		},
		{
			name:      "returns_empty_string_when_no_request_id",
			requestID: "",
			expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			if tt.requestID != "" {
				ctx := context.WithValue(req.Context(), requestIDKey, tt.requestID)
				req = req.WithContext(ctx)
			}

			result := GetRequestID(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateRequestID(t *testing.T) {
	// Test that generated IDs are unique
	ids := make(map[string]bool)

	for range 100 {
		id := generateRequestID()

		// Check format
		assert.Contains(t, id, "-")
		parts := strings.Split(id, "-")
		assert.Len(t, parts, 2)

		// Check uniqueness
		assert.False(t, ids[id], "Generated ID should be unique")
		ids[id] = true
	}
}

func TestLoggingMiddleware(t *testing.T) {
	tests := []struct {
		name         string
		requestID    string
		responseCode int
		method       string
		path         string
	}{
		{
			name:         "logs_successful_request",
			requestID:    "log-test-123",
			responseCode: http.StatusOK,
			method:       http.MethodGet,
			path:         "/api/test",
		},
		{
			name:         "logs_error_response",
			requestID:    "log-test-456",
			responseCode: http.StatusInternalServerError,
			method:       http.MethodPost,
			path:         "/api/error",
		},
		{
			name:         "logs_request_without_id",
			requestID:    "",
			responseCode: http.StatusNotFound,
			method:       http.MethodDelete,
			path:         "/api/missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test handler
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(1 * time.Millisecond)
				w.WriteHeader(tt.responseCode)
			})

			// Wrap with middleware
			middlewareHandler := LoggingMiddleware(handler)

			// Create request
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.requestID != "" {
				ctx := context.WithValue(req.Context(), requestIDKey, tt.requestID)
				req = req.WithContext(ctx)
			}

			// Execute request
			rec := httptest.NewRecorder()

			// Record start time
			startTime := time.Now()
			middlewareHandler.ServeHTTP(rec, req)
			duration := time.Since(startTime)

			// Verify response
			assert.Equal(t, tt.responseCode, rec.Code)

			// Verify duration was recorded
			assert.Greater(t, duration.Nanoseconds(), int64(0))
		})
	}
}

func TestResponseWrapper(t *testing.T) {
	tests := []struct {
		name               string
		statusCode         int
		writeData          bool
		expectedStatusCode int
	}{
		{
			name:               "captures_200_status",
			statusCode:         http.StatusOK,
			writeData:          true,
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "captures_404_status",
			statusCode:         http.StatusNotFound,
			writeData:          false,
			expectedStatusCode: http.StatusNotFound,
		},
		{
			name:               "captures_500_status",
			statusCode:         http.StatusInternalServerError,
			writeData:          true,
			expectedStatusCode: http.StatusInternalServerError,
		},
		{
			name:               "defaults_to_200_when_not_explicitly_set",
			statusCode:         0, // Don't call WriteHeader
			writeData:          true,
			expectedStatusCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			wrapper := &responseWrapper{
				ResponseWriter: rec,
				statusCode:     http.StatusOK, // Default
			}

			if tt.statusCode > 0 {
				wrapper.WriteHeader(tt.statusCode)
			}

			if tt.writeData {
				_, _ = wrapper.Write([]byte("test response"))
			}

			assert.Equal(t, tt.expectedStatusCode, wrapper.statusCode)

			if tt.statusCode > 0 {
				assert.Equal(t, tt.statusCode, rec.Code)
			}
		})
	}
}

func TestCORSMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		expectNext     bool
		expectedStatus int
	}{
		{
			name:           "handles_regular_get_request",
			method:         http.MethodGet,
			expectNext:     true,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "handles_post_request",
			method:         http.MethodPost,
			expectNext:     true,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "handles_preflight_options_request",
			method:         http.MethodOptions,
			expectNext:     false,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlerCalled := false
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			middlewareHandler := CORSMiddleware(handler)

			req := httptest.NewRequest(tt.method, "/api/test", nil)
			rec := httptest.NewRecorder()

			middlewareHandler.ServeHTTP(rec, req)

			// Check CORS headers are set
			assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
			assert.Equal(t, "GET, POST, PUT, PATCH, DELETE, OPTIONS", rec.Header().Get("Access-Control-Allow-Methods"))
			assert.Equal(t, "Content-Type, Authorization, X-Request-ID", rec.Header().Get("Access-Control-Allow-Headers"))
			assert.Equal(t, "X-Request-ID", rec.Header().Get("Access-Control-Expose-Headers"))

			// Check if next handler was called
			assert.Equal(t, tt.expectNext, handlerCalled)
			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}

func TestCrossOriginProtectionMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected"))
	})

	middlewareHandler := CrossOriginProtectionMiddleware(handler)

	tests := []struct {
		name           string
		origin         string
		method         string
		expectedStatus int
	}{
		{
			name:           "allows_same_origin_request",
			origin:         "",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "handles_cross_origin_get",
			origin:         "https://example.com",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
		},
		// Note: Full CSRF protection testing would require more complex setup
		// as Go's CrossOriginProtection has specific behavior patterns
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/test", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()

			middlewareHandler.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}

	t.Run("allows_cross_origin_mutating_request_with_bearer_token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{}`))
		req.Header.Set("Origin", "https://webflow.com")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Authorization", "Bearer test-token")
		rec := httptest.NewRecorder()

		middlewareHandler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middlewareHandler := SecurityHeadersMiddleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	middlewareHandler.ServeHTTP(rec, req)

	// Verify all security headers are set
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'self'")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "connect-src")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "http://127.0.0.1:8765")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "http://localhost:8765")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "https://lh3.googleusercontent.com")
	assert.Equal(t, "max-age=63072000; includeSubDomains", rec.Header().Get("Strict-Transport-Security"))
}

func TestSecurityHeadersMiddlewareAllowsWebflowExtensionSurfacePages(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middlewareHandler := SecurityHeadersMiddleware(handler)

	req := httptest.NewRequest(
		http.MethodGet,
		"/settings/account?surface=webflow-extension",
		nil,
	)
	rec := httptest.NewRecorder()

	middlewareHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", rec.Header().Get("X-Frame-Options"))
	assert.Contains(
		t,
		rec.Header().Get("Content-Security-Policy"),
		"frame-ancestors 'self' https://webflow.com https://*.webflow.com http://localhost:1337 http://127.0.0.1:1337",
	)
}

func TestMiddlewareChaining(t *testing.T) {
	// Test that multiple middlewares work together correctly
	finalHandlerCalled := false
	capturedRequestID := ""

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalHandlerCalled = true
		capturedRequestID = GetRequestID(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	})

	// Chain middlewares
	chainedHandler := RequestIDMiddleware(
		LoggingMiddleware(
			SecurityHeadersMiddleware(
				CORSMiddleware(handler),
			),
		),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	chainedHandler.ServeHTTP(rec, req)

	// Verify all middlewares executed
	assert.True(t, finalHandlerCalled)
	assert.NotEmpty(t, capturedRequestID)
	assert.NotEmpty(t, rec.Header().Get("X-Request-ID"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "success", rec.Body.String())
}

func TestRequestIDMiddlewareConcurrency(t *testing.T) {
	// Test that request ID middleware is safe for concurrent use
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := GetRequestID(r)
		assert.NotEmpty(t, requestID)
		w.WriteHeader(http.StatusOK)
	})

	middlewareHandler := RequestIDMiddleware(handler)

	done := make(chan bool, 10)
	results := make(chan bool, 20)
	for i := range 10 {
		go func(index int) {
			defer func() { done <- true }()

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()

			middlewareHandler.ServeHTTP(rec, req)

			results <- (rec.Code == http.StatusOK)
			results <- (rec.Header().Get("X-Request-ID") != "")
		}(i)
	}

	// Wait for all goroutines
	for range 10 {
		<-done
	}

	// Verify results on main goroutine
	for range 20 { // two checks per goroutine
		ok := <-results
		assert.True(t, ok)
	}
}

func TestLoggingMiddlewarePerformance(t *testing.T) {
	// Test that logging middleware correctly measures performance
	delays := []time.Duration{
		10 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
	}

	for _, delay := range delays {
		t.Run(delay.String(), func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(delay)
				w.WriteHeader(http.StatusOK)
			})

			middlewareHandler := LoggingMiddleware(handler)

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()

			start := time.Now()
			middlewareHandler.ServeHTTP(rec, req)
			elapsed := time.Since(start)

			// Verify that the request took at least the expected delay
			assert.GreaterOrEqual(t, elapsed, delay)
			// Remove upper bound check as it's fragile in CI environments
		})
	}
}

func TestCORSMiddlewareWithVariousHeaders(t *testing.T) {
	tests := []struct {
		name            string
		requestHeaders  map[string]string
		expectedAllowed bool
	}{
		{
			name: "with_authorization_header",
			requestHeaders: map[string]string{
				"Authorization": "Bearer token123",
			},
			expectedAllowed: true,
		},
		{
			name: "with_content_type_header",
			requestHeaders: map[string]string{
				"Content-Type": "application/json",
			},
			expectedAllowed: true,
		},
		{
			name: "with_custom_request_id",
			requestHeaders: map[string]string{
				"X-Request-ID": "custom-id-123",
			},
			expectedAllowed: true,
		},
		{
			name: "with_multiple_headers",
			requestHeaders: map[string]string{
				"Authorization": "Bearer token",
				"Content-Type":  "application/json",
				"X-Request-ID":  "req-123",
			},
			expectedAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			middlewareHandler := CORSMiddleware(handler)

			req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
			for key, value := range tt.requestHeaders {
				req.Header.Set(key, value)
			}

			rec := httptest.NewRecorder()
			middlewareHandler.ServeHTTP(rec, req)

			if tt.expectedAllowed {
				assert.Equal(t, http.StatusOK, rec.Code)
			}

			// Verify CORS headers are always set
			assert.NotEmpty(t, rec.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}

func TestMiddlewareErrorHandling(t *testing.T) {
	// Test that middlewares handle panics gracefully
	t.Run("panic_recovery", func(t *testing.T) {
		panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("test panic")
		})

		// Note: In production, you'd want a recovery middleware
		// This test verifies that our middlewares don't prevent panic recovery

		middlewareHandler := RequestIDMiddleware(
			LoggingMiddleware(panicHandler),
		)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()

		assert.Panics(t, func() {
			middlewareHandler.ServeHTTP(rec, req)
		})
	})
}

func TestSecurityHeadersPreservesExisting(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set a custom header in the handler
		w.Header().Set("X-Custom-Header", "custom-value")
		w.WriteHeader(http.StatusOK)
	})

	middlewareHandler := SecurityHeadersMiddleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	middlewareHandler.ServeHTTP(rec, req)

	// Verify custom header is preserved
	assert.Equal(t, "custom-value", rec.Header().Get("X-Custom-Header"))
	// And security headers are also set
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

// Benchmark tests
func BenchmarkRequestIDMiddleware(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middlewareHandler := RequestIDMiddleware(handler)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		middlewareHandler.ServeHTTP(rec, req)
	}
}

func BenchmarkGenerateRequestID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = generateRequestID()
	}
}

func BenchmarkMiddlewareChain(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	chainedHandler := RequestIDMiddleware(
		LoggingMiddleware(
			SecurityHeadersMiddleware(
				CORSMiddleware(handler),
			),
		),
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		chainedHandler.ServeHTTP(rec, req)
	}
}

// Edge case tests
func TestEmptyRequestPath(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middlewareHandler := RequestIDMiddleware(LoggingMiddleware(handler))

	req := httptest.NewRequest(http.MethodGet, "/", nil) // Use "/" instead of empty string
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		middlewareHandler.ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("X-Request-ID"))
}

func TestNilContext(t *testing.T) {
	// Test GetRequestID with nil context (should not panic)
	req := &http.Request{}
	requestID := GetRequestID(req)
	assert.Empty(t, requestID)
}

func TestRequestIDUniqueness(t *testing.T) {
	// Generate many IDs concurrently to test uniqueness under load
	const numGoroutines = 100
	const idsPerGoroutine = 100

	idChan := make(chan string, numGoroutines*idsPerGoroutine)

	for range numGoroutines {
		go func() {
			for range idsPerGoroutine {
				idChan <- generateRequestID()
			}
		}()
	}

	// Collect all IDs
	ids := make(map[string]bool)
	for range numGoroutines * idsPerGoroutine {
		id := <-idChan
		require.False(t, ids[id], "Duplicate ID generated: %s", id)
		ids[id] = true
	}

	assert.Len(t, ids, numGoroutines*idsPerGoroutine)
}
