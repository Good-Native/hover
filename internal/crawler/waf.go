package crawler

import (
	"bytes"
	"net/http"
	"strings"
)

// WAFDetection captures a verdict from the WAF fingerprint detector.
// Vendor identifies the protection layer ("cloudflare", "imperva",
// "datadome", "akamai", "generic", or empty when not blocked). Reason
// is the specific signal that fired, suitable for surfacing in
// jobs.error_message.
type WAFDetection struct {
	Blocked bool   `json:"blocked"`
	Vendor  string `json:"vendor,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Vendor labels.
const (
	WAFVendorCloudflare = "cloudflare"
	WAFVendorImperva    = "imperva"
	WAFVendorDataDome   = "datadome"
	WAFVendorAkamai     = "akamai"
	WAFVendorGeneric    = "generic"
)

// DetectWAF inspects a response and reports whether it carries a
// fingerprint of a known bot-protection layer. The function is pure: no
// I/O, safe for table-driven tests. It is intentionally conservative on
// 200 responses — only blocking status codes (typically 403 or 202)
// combined with corroborating fingerprints trigger a verdict, so a
// healthy site that happens to use Cloudflare for caching does not get
// flagged.
//
// Fingerprints (issue #365 row 1 + comment 4334238167):
//   - Cloudflare: cf-mitigated header set on a non-200 response
//   - Imperva: body contains _Incapsula_Resource
//   - DataDome: Server header equals DataDome
//   - Akamai: Server header AkamaiGHost OR akaalb_/_abck/bm_sz cookie OR
//     Server-Timing ak_p marker, all on a blocking status
//   - Generic: tiny body (<500 bytes) on 403 or 202 with no other signal
func DetectWAF(statusCode int, headers http.Header, bodySample []byte) WAFDetection {
	if headers == nil {
		headers = http.Header{}
	}

	blocking := isBlockingStatus(statusCode)

	if v := strings.TrimSpace(headers.Get("Cf-Mitigated")); v != "" && blocking {
		return WAFDetection{
			Blocked: true,
			Vendor:  WAFVendorCloudflare,
			Reason:  "cf-mitigated header present on " + statusLabel(statusCode),
		}
	}

	if bytes.Contains(bodySample, []byte("_Incapsula_Resource")) {
		return WAFDetection{
			Blocked: true,
			Vendor:  WAFVendorImperva,
			Reason:  "_Incapsula_Resource marker in response body",
		}
	}

	if server := headers.Get("Server"); server != "" {
		serverLower := strings.ToLower(server)
		if strings.Contains(serverLower, "datadome") {
			return WAFDetection{
				Blocked: true,
				Vendor:  WAFVendorDataDome,
				Reason:  "Server: DataDome",
			}
		}
		if blocking && strings.Contains(serverLower, "akamaighost") {
			return WAFDetection{
				Blocked: true,
				Vendor:  WAFVendorAkamai,
				Reason:  "Server: AkamaiGHost on " + statusLabel(statusCode),
			}
		}
	}

	if blocking {
		for _, c := range headers.Values("Set-Cookie") {
			cl := strings.ToLower(c)
			if strings.Contains(cl, "akaalb_") {
				return WAFDetection{
					Blocked: true,
					Vendor:  WAFVendorAkamai,
					Reason:  "akaalb_ cookie on " + statusLabel(statusCode),
				}
			}
			// _abck and bm_sz are Akamai Bot Manager cookies (sensor +
			// session). They appear on every BM-fronted response, so by
			// themselves they're not a block marker — many sites run BM
			// in monitor mode without ever blocking. Combined with a
			// blocking status code (403/202), they're unambiguous BM
			// walls and give better attribution than the generic
			// tiny-body fallback (kmart.com.au-class sites that don't
			// emit Server: AkamaiGHost or akaalb_*).
			if strings.Contains(cl, "_abck=") || strings.HasPrefix(cl, "_abck=") {
				return WAFDetection{
					Blocked: true,
					Vendor:  WAFVendorAkamai,
					Reason:  "_abck (Bot Manager session) cookie on " + statusLabel(statusCode),
				}
			}
			if strings.Contains(cl, "bm_sz=") || strings.HasPrefix(cl, "bm_sz=") {
				return WAFDetection{
					Blocked: true,
					Vendor:  WAFVendorAkamai,
					Reason:  "bm_sz (Bot Manager sensor) cookie on " + statusLabel(statusCode),
				}
			}
		}
		for _, st := range headers.Values("Server-Timing") {
			if strings.Contains(strings.ToLower(st), "ak_p;") {
				return WAFDetection{
					Blocked: true,
					Vendor:  WAFVendorAkamai,
					Reason:  "Server-Timing ak_p marker on " + statusLabel(statusCode),
				}
			}
		}
	}

	if blocking && len(bodySample) > 0 && len(bodySample) < 500 {
		return WAFDetection{
			Blocked: true,
			Vendor:  WAFVendorGeneric,
			Reason:  "tiny body on " + statusLabel(statusCode),
		}
	}

	return WAFDetection{}
}

func isBlockingStatus(code int) bool {
	return code == http.StatusForbidden || code == http.StatusAccepted
}

func statusLabel(code int) string {
	switch code {
	case http.StatusForbidden:
		return "403"
	case http.StatusAccepted:
		return "202"
	default:
		return http.StatusText(code)
	}
}
