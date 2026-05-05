package util

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/good-native/hover/internal/logging"
	"golang.org/x/net/publicsuffix"
)

var utilLog = logging.Component("util")

func NormaliseDomain(domain string) string {
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "www.")
	domain = strings.TrimSuffix(domain, "/")

	return domain
}

func ValidateDomain(domain string) error {
	domain = strings.TrimSpace(domain)
	domain = NormaliseDomain(domain)

	if domain == "" {
		return fmt.Errorf("domain cannot be empty")
	}

	if strings.Contains(domain, ":") {
		return fmt.Errorf("domain must not include a port")
	}

	if net.ParseIP(domain) != nil {
		return fmt.Errorf("domain %q is not allowed", domain)
	}

	// publicsuffix accepts these as valid TLDs; reject explicitly to avoid SSRF / internal targets.
	lowerDomain := strings.ToLower(domain)
	blockedDomains := []string{
		"localhost",
		"localhost.localdomain",
		"local",
		"internal",
		"test",
		"example",
		"invalid",
	}
	for _, blocked := range blockedDomains {
		if lowerDomain == blocked || strings.HasSuffix(lowerDomain, "."+blocked) {
			return fmt.Errorf("domain %q is not allowed", domain)
		}
	}

	_, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		if strings.Contains(err.Error(), "is a suffix") {
			return fmt.Errorf("invalid domain, please enter a full domain")
		}
		return fmt.Errorf("invalid domain, please enter a full domain")
	}

	return nil
}

func NormaliseURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)

	if rawURL == "" {
		return ""
	}

	if strings.HasPrefix(rawURL, "http://") {
		rawURL = strings.Replace(rawURL, "http://", "https://", 1)
	}

	if !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		utilLog.Debug("Invalid URL format", "url", rawURL, "error", err)
		return ""
	}

	// Defensive against `https://http://example.com` style inputs.
	hostPart := parsedURL.Host
	if strings.Contains(hostPart, "://") {
		utilLog.Debug("URL contains embedded scheme in host part, fixing", "url", rawURL)
		parts := strings.SplitN(hostPart, "://", 2)
		if len(parts) == 2 {
			parsedURL.Host = parts[1]
			rawURL = parsedURL.String()
		}
	}

	return rawURL
}

func ExtractPathFromURL(fullURL string) string {
	path := fullURL
	path = strings.TrimPrefix(path, "http://")
	path = strings.TrimPrefix(path, "https://")
	path = strings.TrimPrefix(path, "www.")

	domainEnd := strings.Index(path, "/")
	if domainEnd != -1 {
		path = path[domainEnd:]
	} else {
		path = "/"
	}

	return path
}

func ConstructURL(domain, path string) string {
	normalisedDomain := NormaliseDomain(domain)

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return "https://" + normalisedDomain + path
}

func normaliseHostPort(host, scheme string) string {
	if scheme == "http" && strings.HasSuffix(host, ":80") {
		return strings.TrimSuffix(host, ":80")
	}
	if scheme == "https" && strings.HasSuffix(host, ":443") {
		return strings.TrimSuffix(host, ":443")
	}
	return host
}

// IsSignificantRedirect: ignores HTTP↔HTTPS, www↔non-www, default-port and
// trailing-slash variants — only host/path differences count.
func IsSignificantRedirect(originalURL, redirectURL string) bool {
	if redirectURL == "" {
		return false
	}

	origParsed, origErr := url.Parse(originalURL)
	redirParsed, redirErr := url.Parse(redirectURL)

	if origErr != nil || redirErr != nil {
		return true
	}

	origHost := normaliseHostPort(origParsed.Host, origParsed.Scheme)
	origHost = strings.ToLower(strings.TrimPrefix(origHost, "www."))
	redirHost := normaliseHostPort(redirParsed.Host, redirParsed.Scheme)
	redirHost = strings.ToLower(strings.TrimPrefix(redirHost, "www."))

	if origHost != redirHost {
		return true
	}

	origPath := origParsed.Path
	redirPath := redirParsed.Path

	if origPath == "" {
		origPath = "/"
	}
	if redirPath == "" {
		redirPath = "/"
	}

	if len(origPath) > 1 {
		origPath = strings.TrimSuffix(origPath, "/")
	}
	if len(redirPath) > 1 {
		redirPath = strings.TrimSuffix(redirPath, "/")
	}

	return origPath != redirPath
}
