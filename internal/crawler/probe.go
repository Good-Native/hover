package crawler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProbeBodyLimit caps the body read during a pre-flight probe. 4 KB is
// enough for the EdgeSuite block page (~373 bytes), the Imperva script
// preamble, and Cloudflare's interstitial.
const ProbeBodyLimit = 4 * 1024

// ProbeTimeout bounds how long a probe waits before giving up. Akamai
// SYN-drop variants will hold the connection — at this point we'd
// rather fall through to the normal flow and let the mid-job circuit
// breaker catch a real wall.
const ProbeTimeout = 8 * time.Second

// Probe issues a GET against the homepage of the given domain and runs
// the WAF detector against the response. The probe sends the supplied
// User-Agent so the verdict matches what real crawl tasks will see.
//
// On network or timeout error the probe returns WAFDetection{} with
// the underlying error; callers should treat a network error as
// "no verdict" rather than as a block.
func Probe(ctx context.Context, domain string, userAgent string, transport http.RoundTripper) (WAFDetection, error) {
	target := normaliseProbeTarget(domain)

	probeCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target, nil)
	if err != nil {
		return WAFDetection{}, fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-AU,en;q=0.9")

	client := &http.Client{
		Timeout: ProbeTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	if transport != nil {
		client.Transport = transport
	}

	resp, err := client.Do(req)
	if err != nil {
		return WAFDetection{}, fmt.Errorf("probe %s: %w", target, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, ProbeBodyLimit))
	if err != nil {
		return WAFDetection{}, fmt.Errorf("read probe body: %w", err)
	}

	return DetectWAF(resp.StatusCode, resp.Header, body), nil
}

// normaliseProbeTarget produces a probeable URL for the given domain.
// If the input already includes a scheme (case-insensitive "http://" or "https://"),
// it preserves the original input (minus any trailing slashes) and ensures exactly
// one trailing slash. Otherwise it prefixes "https://" and appends a trailing slash.
func normaliseProbeTarget(domain string) string {
	d := strings.TrimSpace(domain)
	// Scheme detection is case-insensitive — "HTTPS://example.com"
	// otherwise double-prefixes to "https://HTTPS://example.com/" and
	// the request build fails, silently skipping the WAF verdict.
	dl := strings.ToLower(d)
	if strings.HasPrefix(dl, "http://") || strings.HasPrefix(dl, "https://") {
		return strings.TrimSuffix(d, "/") + "/"
	}
	return "https://" + strings.TrimSuffix(d, "/") + "/"
}
