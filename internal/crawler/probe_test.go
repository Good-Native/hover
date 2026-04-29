package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProbe_AkamaiBlocked(t *testing.T) {
	var seenUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		w.Header().Set("Server", "AkamaiGHost")
		w.Header().Set("Set-Cookie", "akaalb_main=~op=ALB:abc; Path=/")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(strings.Repeat("x", 373)))
	}))
	defer ts.Close()

	det, err := Probe(context.Background(), ts.URL, "Hover/1.0", redirectingTransport(ts.URL))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !det.Blocked {
		t.Fatalf("expected Blocked=true, got %+v", det)
	}
	if det.Vendor != WAFVendorAkamai {
		t.Errorf("vendor = %q, want %q", det.Vendor, WAFVendorAkamai)
	}
	if seenUA != "Hover/1.0" {
		t.Errorf("user-agent = %q, want Hover/1.0", seenUA)
	}
}

func TestProbe_HealthyDomain(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "nginx")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("<p>healthy</p>", 100)))
	}))
	defer ts.Close()

	det, err := Probe(context.Background(), ts.URL, "Hover/1.0", redirectingTransport(ts.URL))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if det.Blocked {
		t.Fatalf("expected Blocked=false, got %+v", det)
	}
}

func TestProbe_BodyTruncation(t *testing.T) {
	bigBody := strings.Repeat("y", 50*1024)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(bigBody))
	}))
	defer ts.Close()

	_, err := Probe(context.Background(), ts.URL, "Hover/1.0", redirectingTransport(ts.URL))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
}

func TestNormaliseProbeTarget(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"example.com", "https://example.com/"},
		{"example.com/", "https://example.com/"},
		{"  example.com  ", "https://example.com/"},
		{"https://example.com", "https://example.com/"},
		{"http://example.com/", "http://example.com/"},
		// Case-insensitive scheme detection — without it
		// "HTTPS://example.com" double-prefixed to
		// "https://HTTPS://example.com/" and silently failed.
		{"HTTPS://example.com", "HTTPS://example.com/"},
		{"Http://example.com/", "Http://example.com/"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normaliseProbeTarget(tc.in)
			if got != tc.want {
				t.Errorf("normaliseProbeTarget(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// redirectingTransport rewrites probe requests (which target https://<host>/)
// onto the local httptest server, so we exercise the production
// host-resolution code path while staying offline.
func redirectingTransport(serverURL string) http.RoundTripper {
	parsed, _ := url.Parse(serverURL)
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = parsed.Scheme
		req.URL.Host = parsed.Host
		return http.DefaultTransport.RoundTrip(req)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
