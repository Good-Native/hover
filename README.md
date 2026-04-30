# Hover 🐝

[![Fly Deploy](https://github.com/Harvey-AU/hover/actions/workflows/fly-deploy.yml/badge.svg)](https://github.com/Harvey-AU/hover/actions/workflows/fly-deploy.yml)
[![Tests](https://github.com/Harvey-AU/hover/actions/workflows/test.yml/badge.svg)](https://github.com/Harvey-AU/hover/actions/workflows/test.yml)
[![codecov](https://codecov.io/github/harvey-au/hover/graph/badge.svg?token=EC0JW5IU7X)](https://codecov.io/github/harvey-au/hover)
[![Go Report Card](https://goreportcard.com/badge/github.com/Harvey-AU/hover?style=flat)](https://goreportcard.com/report/github.com/Harvey-AU/hover)
[![Go Reference](https://pkg.go.dev/badge/github.com/Harvey-AU/hover.svg)](https://pkg.go.dev/github.com/Harvey-AU/hover)
[![Go Version](https://img.shields.io/badge/go-1.26.1-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![Maintenance](https://img.shields.io/badge/Maintained%3F-yes-green.svg)](https://github.com/Harvey-AU/hover/graphs/commit-activity)

A comprehensive website health and performance tool that monitors site health,
detects broken links, identifies slow pages, and warms cache for optimal
performance after publishing. Integrates seamlessly with Webflow via OAuth with
automated scheduling and webhook-triggered crawls.

Keep your site fast and healthy with continuous monitoring and intelligent cache
warming.

Built by the Good Native team in Castlemaine, Victoria, Australia.

## Key Features

### Site Health Monitoring

- 🔍 Broken link detection across your entire site
- 🚨 Identify 404s, timeouts, and redirect chains
- 🐌 Detect slow-loading pages and performance bottlenecks
- 📈 Track broken links and performance over time
- ⚡ Lightning fast speed, without being blocked or spamming your site

### Cache Warming

- 🔥 Smart warming with automatic retry on cache MISS
- 🥇 Priority processing - homepage and critical pages first
- ⚡ Improved initial page load times after publishing
- 🤖 Robots.txt compliance with crawl-delay honouring

### Automation & Integration

- 🔄 Scheduled crawls (6/12/24/48 hour intervals) per site
- 🚀 Webflow OAuth integration with auto-crawl on publish webhooks
- 📊 Real-time dashboard with live job progress via WebSockets
- 🔔 Slack notifications via DMs when jobs complete or fail
- 🔐 Multi-organisation support with Supabase Auth and RLS
- 🔌 RESTful API for platform integrations
- 🏷️ Technology detection (CMS, CDN, frameworks)

## Quick Start

```bash
# Clone the repository
git clone https://github.com/Harvey-AU/hover.git hover
cd hover

# Set up tracked Git hooks once per clone
# Future worktrees inherit this shared hooks path automatically
bash scripts/setup-hooks.sh

# Start development environment
# Windows:
dev              # Clean output (info level)
dev debug        # Verbose output (debug level)

# Mac/Linux:
./dev.sh         # Clean output (info level)
./dev.sh debug      # Verbose output (debug level)
```

One command starts everything:

- ✅ Checks prerequisites (Docker + Supabase CLI)
- 🐳 Starts local Supabase database
- 🔄 Auto-applies migrations
- 📝 Generates `.env.local` on first run (from `supabase status`)
- 🔥 Hot reloading on port 8847
- 📊 Displays helpful URLs for homepage, dashboard, and Supabase Studio
- 🚀 Completely isolated from production
- 🔇 Clean logging by default, verbose mode available

**Then log in:** navigate to `http://localhost:8847/dev/auto-login` — signs you
in as the dev seed user instantly, no OAuth flow needed. See
[Local Authentication](docs/development/DEVELOPMENT.md#local-authentication) for
details.

`.env.local` is generated automatically by `dev.sh` — do not commit it.

## Status

**~65% Complete** - Stage 4 of 7 (Core Authentication & MVP Interface)

**Recent milestones:**

- ✅ Webflow OAuth integration with per-site scheduling and webhooks (v0.23.0)
- ✅ Slack notifications and real-time dashboard updates (v0.20.x)
- ✅ Multi-organisation support with context switching (v0.19.0)
- ✅ Security and compliance testing with CI/CD (Go Report Card: A)

**In progress:** Google Analytics integration, payment infrastructure, platform
SDK

See [Roadmap.md](./Roadmap.md) for detailed progress tracking.

## Tech Stack

- **Backend**: Go 1.26 — API server (`cmd/app`) + worker service (`cmd/worker`),
  coordinated via Redis broker (ZSET scheduler + Streams)
- **Database**: Supabase PostgreSQL with pgBouncer pooler
- **Frontend**: Vanilla JavaScript with data-binding (no build process)
- **Infrastructure**: Fly.io (API + worker apps), Cloudflare CDN, Upstash Redis,
  Supabase (auth + realtime)
- **Monitoring**: Sentry (errors), Grafana Cloud (OTLP metrics), Codecov
  (coverage)

## Documentation

- [Getting Started](docs/development/DEVELOPMENT.md)
- [OpenCode Desktop Setup](docs/development/OPENCODE_DESKTOP.md)
- [API Reference](docs/architecture/API.md)
- [Configuration Reference](docs/architecture/CONFIG_REFERENCE.md)
- [Architecture Overview](docs/architecture/ARCHITECTURE.md)
- [Crawl Handling Cases](docs/architecture/CRAWL_HANDLING.md) — what we do for
  each domain/page condition
- [Supabase Realtime](docs/development/SUPABASE-REALTIME.md)
- [Observability & Tracing](docs/operations/OBSERVABILITY.md)
- [All Documentation →](docs/)

## Support

- [Report Issues](https://github.com/Harvey-AU/hover/issues)
- [Security Policy](SECURITY.md)
- Email: <hello@teamharvey.co>

## License

MIT - See [LICENSE](LICENSE)
