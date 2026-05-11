package api

import "net/http"

const botInfoPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Hover crawler</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
body { font: 14px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; max-width: 760px; margin: 2rem auto; padding: 0 1rem; color: #111; }
h1 { font-size: 1.4rem; margin-top: 1.6rem; }
h2 { font-size: 1.05rem; margin-top: 1.4rem; }
code { background: #f3f3f3; padding: 0 .25em; border-radius: 3px; }
pre { background: #f3f3f3; padding: .75rem 1rem; border-radius: 4px; overflow-x: auto; }
a { color: #0a58ca; }
</style>
</head>
<body>

<h1>Hover crawler</h1>

<pre>User-Agent: Hover/1.0 (+https://hover.app.goodnative.co/bot)
Operator:   Good Native Pty Ltd (Australia)
Contact:    crawler@goodnative.co  (response within 1 business day)</pre>

<h2>What it does</h2>
<p>Hover is an SEO / site-health auditing service. The crawler fetches HTML
pages for sites whose operators have explicitly added them to Hover and
authorised analysis. It does not crawl sites without operator opt-in.</p>

<h2>What it fetches</h2>
<ul>
  <li>HTML of public pages on the authorised domain.</li>
  <li><code>robots.txt</code> (always, on each new analysis).</li>
  <li><code>sitemap.xml</code> when available.</li>
</ul>
<p>It does NOT:</p>
<ul>
  <li>Crawl checkout, cart, account, or admin paths.</li>
  <li>Submit forms or trigger purchases.</li>
  <li>Bypass paywalls or auth gates.</li>
</ul>

<h2>robots.txt</h2>
<p>Hover respects <code>robots.txt</code> directives. Use either:</p>
<pre>User-agent: Hover
Disallow: /</pre>
<p>or the wildcard <code>User-agent: *</code> rules. <code>Crawl-delay</code> is honoured.</p>

<h2>How to block</h2>
<ul>
  <li><code>robots.txt</code> as above (preferred).</li>
  <li>Block by User-Agent at WAF / Cloudflare.</li>
  <li>Email <a href="mailto:crawler@goodnative.co">crawler@goodnative.co</a> &mdash; we will remove the domain.</li>
</ul>

<h2>Rate / politeness</h2>
<ul>
  <li>Adaptive per-domain rate limiter; backs off on 429/5xx.</li>
  <li>Typical concurrent connections per domain: 1&ndash;5.</li>
  <li>We do not retry indefinitely on persistent 4xx.</li>
</ul>

<h2>Verification</h2>
<p>The crawler currently runs from Fly.io shared egress (region: syd). We
are working toward dedicated egress with PTR records on
<code>goodnative.co</code> and intend to register as a Cloudflare Verified
Bot.</p>

</body>
</html>
`

func (h *Handler) BotInfoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(botInfoPage))
}
