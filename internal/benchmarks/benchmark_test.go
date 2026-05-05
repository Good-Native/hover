package benchmarks

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/good-native/hover/internal/cache"
	"github.com/good-native/hover/internal/util"
)

// Benchmark cache operations - hot path for URL deduplication
func BenchmarkCacheSet(b *testing.B) {
	c := cache.NewInMemoryCache()
	key := "test-key"
	value := "test-value"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set(key, value)
	}
}

func BenchmarkCacheGet(b *testing.B) {
	c := cache.NewInMemoryCache()
	key := "test-key"
	c.Set(key, "test-value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get(key)
	}
}

func BenchmarkCacheConcurrentAccess(b *testing.B) {
	c := cache.NewInMemoryCache()

	// Pre-populate cache
	for i := range 100 {
		c.Set(string(rune(i)), i)
	}

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := string(rune(i % 100))
			if i%2 == 0 {
				c.Get(key)
			} else {
				c.Set(key, i)
			}
			i++
		}
	})
}

// Benchmark URL utilities - hot path for URL normalization
func BenchmarkNormalizeURL(b *testing.B) {
	url := "https://www.example.com/path/to/page?query=value#fragment"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		util.NormaliseURL(url)
	}
}

func BenchmarkNormaliseDomain(b *testing.B) {
	domain := "www.example.com"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		util.NormaliseDomain(domain)
	}
}

func BenchmarkExtractPathFromURL(b *testing.B) {
	url := "https://www.example.com/path/to/page?query=value"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		util.ExtractPathFromURL(url)
	}
}

func BenchmarkConstructURL(b *testing.B) {
	domain := "example.com"
	path := "/path/to/page"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		util.ConstructURL(domain, path)
	}
}

// Benchmark response helpers - hot path for API responses
func BenchmarkJSONResponse(b *testing.B) {
	data := map[string]any{
		"status": "success",
		"data": map[string]any{
			"id":      123,
			"name":    "Test",
			"items":   []int{1, 2, 3, 4, 5},
			"enabled": true,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}

func BenchmarkSuccessResponse(b *testing.B) {
	data := struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Active bool   `json:"active"`
	}{
		ID:     123,
		Name:   "Test",
		Active: true,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		respondWithSuccess(w, data)
	}
}

func BenchmarkErrorResponse(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		respondWithError(w, http.StatusBadRequest, "Invalid request")
	}
}

// Benchmark middleware - hot path for request processing
func BenchmarkRequestIDMiddleware(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		// Simulate request ID middleware
		requestID := generateRequestID()
		req.Header.Set("X-Request-ID", requestID)
		handler.ServeHTTP(w, req)
	}
}

func BenchmarkLoggingMiddleware(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		// Simulate logging middleware (without actual logging)
		handler.ServeHTTP(w, req)
		_ = w.Code       // Access status code
		_ = w.Body.Len() // Access response size
	}
}

// Benchmark string operations - hot paths in parsing
func BenchmarkStringContains(b *testing.B) {
	haystack := "https://www.example.com/path/to/page?query=value#fragment"
	needle := "example.com"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = contains(haystack, needle)
	}
}

func BenchmarkStringConcatenation(b *testing.B) {
	base := "https://example.com"
	path := "/api/v1/jobs"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = base + path
	}
}

func BenchmarkStringBuilder(b *testing.B) {
	parts := []string{"https://", "example.com", "/api/v1/", "jobs"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var result strings.Builder
		for _, part := range parts {
			result.WriteString(part)
		}
		_ = result.String()
	}
}

// Benchmark UUID generation - used for job/task IDs
func BenchmarkUUIDGeneration(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generateUUID()
	}
}

// Benchmark map operations - used in various places
func BenchmarkMapSet(b *testing.B) {
	m := make(map[string]any)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m[string(rune(i%1000))] = i
	}
}

func BenchmarkMapGet(b *testing.B) {
	m := make(map[string]any)
	for i := range 1000 {
		m[string(rune(i))] = i
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m[string(rune(i%1000))]
	}
}

func BenchmarkMapConcurrent(b *testing.B) {
	m := make(map[string]any)
	for i := range 100 {
		m[string(rune(i))] = i
	}

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := string(rune(i % 100))
			if i%2 == 0 {
				_ = m[key]
			} else {
				// Note: This would need sync.RWMutex in real code
				// m[key] = i
			}
			i++
		}
	})
}

// Helper functions for benchmarks
func respondWithSuccess(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data":    data,
	})
}

func respondWithError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"error":   message,
	})
}

func generateRequestID() string {
	// Simple mock for benchmark
	return "req-123456789"
}

func generateUUID() string {
	// Simple mock for benchmark
	return "550e8400-e29b-41d4-a716-446655440000"
}

func contains(s, substr string) bool {
	if len(s) == 0 || len(substr) == 0 {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
