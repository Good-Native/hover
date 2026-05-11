package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBotInfoHandler(t *testing.T) {
	h := &Handler{}

	t.Run("GET returns operator page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/bot", nil)
		rr := httptest.NewRecorder()

		h.BotInfoHandler(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html prefix", ct)
		}
		body := rr.Body.String()
		for _, want := range []string{
			"Hover/1.0",
			"hover.app.goodnative.co/bot",
			"crawler@goodnative.co",
			"robots.txt",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q", want)
			}
		}
	})

	t.Run("POST rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/bot", nil)
		rr := httptest.NewRecorder()

		h.BotInfoHandler(rr, req)

		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rr.Code)
		}
	})
}
