package crawler

import (
	"net/http"
	"strings"
	"testing"
)

func TestDetectWAF(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		headers      http.Header
		body         []byte
		wantBlocked  bool
		wantVendor   string
		reasonPrefix string
	}{
		{
			name:        "amazon.com 200 — must NOT trip detector after UA rename",
			status:      http.StatusOK,
			headers:     http.Header{"Server": []string{"Server"}, "Content-Type": []string{"text/html"}},
			body:        []byte("<!doctype html><html><head><title>Amazon</title></head><body>...big page...</body></html>"),
			wantBlocked: false,
		},
		{
			name:         "akamai — Server: AkamaiGHost on 403",
			status:       http.StatusForbidden,
			headers:      http.Header{"Server": []string{"AkamaiGHost"}},
			body:         []byte("<HTML><HEAD>Access Denied</HEAD></HTML>"),
			wantBlocked:  true,
			wantVendor:   WAFVendorAkamai,
			reasonPrefix: "Server: AkamaiGHost",
		},
		{
			name:   "akamai — akaalb_ cookie on 403",
			status: http.StatusForbidden,
			headers: http.Header{
				"Set-Cookie": []string{"akaalb_main=~op=ALB:abc; Path=/"},
			},
			body:         []byte("blocked"),
			wantBlocked:  true,
			wantVendor:   WAFVendorAkamai,
			reasonPrefix: "akaalb_ cookie",
		},
		{
			// kmart.com.au-class: deeper page returns 403 with Akamai
			// Bot Manager session cookie. Without this signal the
			// detector would fall through to "generic tiny body".
			name:   "akamai BM — _abck cookie on 403",
			status: http.StatusForbidden,
			headers: http.Header{
				"Set-Cookie": []string{"_abck=06F95C1AA35B5110~-1~YAAQjyw...~-1~-1~-1~-1~-1; Domain=.example.com; Path=/"},
			},
			body:         []byte("blocked"),
			wantBlocked:  true,
			wantVendor:   WAFVendorAkamai,
			reasonPrefix: "_abck",
		},
		{
			name:   "akamai BM — bm_sz cookie on 403",
			status: http.StatusForbidden,
			headers: http.Header{
				"Set-Cookie": []string{"bm_sz=E18A0D7D1B94A12A~YAAQjyw...; Domain=.example.com; Path=/"},
			},
			body:         []byte("blocked"),
			wantBlocked:  true,
			wantVendor:   WAFVendorAkamai,
			reasonPrefix: "bm_sz",
		},
		{
			// Critical negative test: Akamai BM cookies appear on every
			// BM-fronted response, including legitimate 200s in monitor
			// mode. Detector must NOT flag these — would over-trigger
			// massively (every kmart.com.au homepage hit would block).
			name:   "akamai BM — _abck on 200 must NOT trip (monitor mode)",
			status: http.StatusOK,
			headers: http.Header{
				"Set-Cookie": []string{"_abck=06F95C1AA35B5110~-1~YAAQjyw...~-1~-1~-1~-1~-1; Domain=.example.com; Path=/"},
			},
			body:        []byte("<html>real content from monitor-mode site</html>"),
			wantBlocked: false,
		},
		{
			name:   "akamai BM — bm_sz on 200 must NOT trip (monitor mode)",
			status: http.StatusOK,
			headers: http.Header{
				"Set-Cookie": []string{"bm_sz=E18A0D7D~YAAQjyw...; Domain=.example.com; Path=/"},
			},
			body:        []byte("<html>real content</html>"),
			wantBlocked: false,
		},
		{
			name:   "akamai — Server-Timing ak_p on 403",
			status: http.StatusForbidden,
			headers: http.Header{
				"Server-Timing": []string{"ak_p; desc=\"123_456_789\";dur=1"},
			},
			body:         []byte("blocked"),
			wantBlocked:  true,
			wantVendor:   WAFVendorAkamai,
			reasonPrefix: "Server-Timing ak_p",
		},
		{
			name:         "akamai — EdgeSuite 373-byte body counts as generic when no header signal",
			status:       http.StatusForbidden,
			headers:      http.Header{},
			body:         []byte(strings.Repeat("x", 373)),
			wantBlocked:  true,
			wantVendor:   WAFVendorGeneric,
			reasonPrefix: "tiny body",
		},
		{
			name:   "cloudflare — cf-mitigated header on 403",
			status: http.StatusForbidden,
			headers: http.Header{
				"Cf-Mitigated": []string{"challenge"},
				"Server":       []string{"cloudflare"},
			},
			body:         []byte("Just a moment..."),
			wantBlocked:  true,
			wantVendor:   WAFVendorCloudflare,
			reasonPrefix: "cf-mitigated",
		},
		{
			name:   "cloudflare — cf-mitigated alone on 200 must NOT trip (caching path)",
			status: http.StatusOK,
			headers: http.Header{
				"Cf-Mitigated": []string{""},
				"Server":       []string{"cloudflare"},
			},
			body:        []byte("<html>real content</html>"),
			wantBlocked: false,
		},
		{
			name:         "imperva — _Incapsula_Resource marker in body",
			status:       http.StatusOK,
			headers:      http.Header{},
			body:         []byte("<script>var marker=\"_Incapsula_Resource SWWRGTS-3995852985\";</script>"),
			wantBlocked:  true,
			wantVendor:   WAFVendorImperva,
			reasonPrefix: "_Incapsula_Resource",
		},
		{
			name:         "datadome — Server: DataDome",
			status:       http.StatusForbidden,
			headers:      http.Header{"Server": []string{"DataDome"}},
			body:         []byte("blocked"),
			wantBlocked:  true,
			wantVendor:   WAFVendorDataDome,
			reasonPrefix: "Server: DataDome",
		},
		{
			name:         "generic — tiny body on 202",
			status:       http.StatusAccepted,
			headers:      http.Header{},
			body:         []byte(strings.Repeat("x", 200)),
			wantBlocked:  true,
			wantVendor:   WAFVendorGeneric,
			reasonPrefix: "tiny body",
		},
		{
			name:        "generic — large body on 403 does NOT trip generic",
			status:      http.StatusForbidden,
			headers:     http.Header{},
			body:        []byte(strings.Repeat("x", 5000)),
			wantBlocked: false,
		},
		{
			name:        "404 with no fingerprint — not blocked",
			status:      http.StatusNotFound,
			headers:     http.Header{},
			body:        []byte("<html>not found</html>"),
			wantBlocked: false,
		},
		{
			name:        "503 with no fingerprint — not blocked (transient)",
			status:      http.StatusServiceUnavailable,
			headers:     http.Header{},
			body:        []byte("upstream down"),
			wantBlocked: false,
		},
		{
			name:         "akamaighost case-insensitive",
			status:       http.StatusForbidden,
			headers:      http.Header{"Server": []string{"akamaighost"}},
			body:         []byte("blocked"),
			wantBlocked:  true,
			wantVendor:   WAFVendorAkamai,
			reasonPrefix: "Server: AkamaiGHost",
		},
		{
			name:        "nil headers — must not panic",
			status:      http.StatusOK,
			headers:     nil,
			body:        []byte("ok"),
			wantBlocked: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectWAF(tc.status, tc.headers, tc.body)
			if got.Blocked != tc.wantBlocked {
				t.Fatalf("Blocked = %v, want %v (vendor %q reason %q)",
					got.Blocked, tc.wantBlocked, got.Vendor, got.Reason)
			}
			if tc.wantVendor != "" && got.Vendor != tc.wantVendor {
				t.Errorf("Vendor = %q, want %q", got.Vendor, tc.wantVendor)
			}
			if tc.reasonPrefix != "" && !strings.Contains(got.Reason, tc.reasonPrefix) {
				t.Errorf("Reason = %q, want substring %q", got.Reason, tc.reasonPrefix)
			}
		})
	}
}
