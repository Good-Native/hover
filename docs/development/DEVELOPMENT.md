# Development Guide

## Prerequisites

- **Go 1.26.1** - We use Go 1.26.1 for the latest runtime fixes and improvements
- **Docker Desktop** - Required for local Supabase instance
  ([Download here](https://docs.docker.com/desktop/))
- **Supabase CLI** - Database management (`npm install -g supabase` or
  `brew install supabase/tap/supabase`)
- **Air** - Hot reloading for development
  (`go install github.com/air-verse/air@latest`)
- **Git** - Version control
- **golangci-lint** (optional) - Code quality checks
  (`brew install golangci-lint`)
- **1Password CLI** (optional but recommended) — loads external API secrets
  automatically (`brew install 1password-cli`). Without it, you'll need to add
  Stripe/Slack/Webflow/Google/Loops keys to `.env.local` manually.

## Quick Setup

### 1. Clone and Setup

```bash
# Fork and clone the repository
git clone https://github.com/[your-username]/hover.git
cd hover

# Set up tracked Git hooks once per clone
# Future worktrees inherit this shared hooks path automatically
bash scripts/setup-hooks.sh
```

The Git hooks will automatically format your code before each commit:

- ✅ Go files formatted with `gofmt`
- ✅ Markdown, YAML, JSON formatted with Prettier
- ✅ No manual formatting needed!
- ✅ Future worktrees reuse the same tracked hooks path

### 2. Start Development Environment

**That's it!** Just run:

```bash
# Windows:
dev              # Clean output (PC platform)
dev debug        # Verbose output (PC platform)

# Mac/Linux:
./dev.sh         # Clean output (Mac platform)
./dev.sh debug   # Verbose output (Mac platform)
```

This single command will:

- ✅ Check prerequisites (Docker Desktop + Supabase CLI)
- ✅ Start local Supabase instance (if not running)
- ✅ Apply all database migrations automatically
- ✅ Watch for migration changes and auto-reset database
- ✅ Configure Air for your platform automatically
- ✅ Connect to isolated local database on port 54322
- ✅ Start the app with hot reloading on port 8847
- ✅ Display helpful URLs for easy access
- ✅ Use clean logging by default (info level)
- ✅ Zero production database interference

### 3. Environment Configuration (Automatic)

`dev.sh` generates `.env.local` automatically from `supabase status` on first
run. It produces:

```bash
# Local development overrides — not committed to git
# Generated from: supabase status

APP_ENV=development
LOG_LEVEL=info

DATABASE_URL=postgresql://postgres:postgres@127.0.0.1:54322/postgres

SUPABASE_URL=http://127.0.0.1:54321
SUPABASE_AUTH_URL=http://127.0.0.1:54321
SUPABASE_PUBLISHABLE_KEY=sb_publishable_<project-specific>
SUPABASE_SERVICE_ROLE_KEY=sb_secret_<project-specific>
```

If `.env.local` already exists, `dev.sh` leaves it untouched. Do not commit it —
it is gitignored. For the complete list of runtime configuration options, see
[CONFIG_REFERENCE.md](../architecture/CONFIG_REFERENCE.md).

To regenerate (e.g. after a Supabase CLI upgrade changes the keys):

```bash
rm .env.local
./dev.sh
```

### 4. Prerequisites Check

If `air` fails, ensure you have:

```bash
# Check Docker Desktop is running
docker ps

# Check Supabase CLI is installed
supabase --version

# Install if missing:
# Windows: npm install -g supabase
# Mac: brew install supabase/tap/supabase
```

### 5. Database Migrations

**Creating new migrations (fully automatic)**:

```bash
# 1. Generate a new migration file
supabase migration new your_migration_name

# 2. Edit the file in supabase/migrations/
# 3. Save the file
# 🎉 Database automatically resets and applies the migration!
# 🎉 Go app automatically restarts with the new schema!
```

**No manual steps required** - the `dev` script watches for migration changes
and automatically runs `supabase db reset` when you save any `.sql` file in the
migrations folder.

**Deployment process**:

1. Push changes to feature branch
2. After testing, merge to `main` - migrations apply automatically

**Note**: Supabase GitHub integration handles all migration deployment. Never
run `supabase db push` manually.

## Development Server

### Recommended: `dev.sh` (handles everything)

```bash
./dev.sh
```

This script automatically:

- Starts Supabase local if not already running
- Generates `.env.local` from `supabase status` (DATABASE_URL, auth URL, keys)
- Launches `air` with hot reloading
- Watches for migration changes and auto-resets the database

### Bare `air` (advanced — only if you already have `.env.local`)

```bash
go install github.com/air-verse/air@latest
air
```

If `DATABASE_URL` is not set and `APP_ENV` is `development` (the default), the
app falls back to Supabase local defaults (`localhost:54322/postgres`). For full
functionality (auth, publishable key), use `dev.sh` or create `.env.local`
manually — see `.env.example` for reference.

### Without Hot Reloading

```bash
go build ./cmd/app && ./app
# Or: go run ./cmd/app/main.go
```

### Server will start on `http://localhost:8847`

### Claude Code Preview

Claude Code's built-in preview feature starts the server automatically via
`.claude/launch.json`. It runs `air` directly (hot reloading included).

**Prerequisite:** Supabase must already be running before the preview starts.
Run `supabase start` or `./dev.sh` once to bring it up, then use the preview.

**Windows:** `.air.toml` defaults to Mac/Linux. On Windows, use `./dev.sh pc`
which overrides the build command, or run Air manually:

```bash
air -build.cmd "go build -o ./tmp/main.exe ./cmd/app" -build.bin "tmp/main.exe"
```

## Local Authentication

The local Supabase instance is seeded with test users on every
`supabase db reset`.

### Seed users

| Email                     | Password      | Role         | Notes                                         |
| ------------------------- | ------------- | ------------ | --------------------------------------------- |
| `seed-admin@example.com`  | —             | system_admin | Google OAuth only — cannot use email/password |
| `seed-member@example.com` | —             | member       | Google OAuth only                             |
| `dev@example.com`         | `devpassword` | system_admin | Email/password — use this for local login     |

### Logging in

**Real browser** (Chrome/Safari at `localhost:8847`):

Navigate to `http://localhost:8847/dev/auto-login`. The server signs in as
`dev@example.com` server-side, injects a valid Supabase session into
`localStorage`, then redirects to `/dashboard`. The session lasts one hour and
persists across page reloads.

**Claude preview browser** (sandboxed — cannot reach Supabase directly):

The `/dev/auto-login` endpoint is specifically designed for this. From a preview
eval or by navigating directly:

```javascript
window.location.replace("/dev/auto-login");
```

### Why `/dev/auto-login` exists

The Claude app's preview browser can reach `localhost:8847` but not
`127.0.0.1:54321` (the local Supabase instance). The Supabase JS client normally
calls Supabase directly from the browser for auth. The endpoint bypasses this:
the Go server fetches the session server-side (it can reach Supabase fine), then
injects the tokens directly into `localStorage` before the redirect.

The endpoint returns **404** outside `APP_ENV=development` — it cannot be
accidentally exposed in production.

### After `supabase db reset`

The `dev@example.com` user is recreated automatically by `supabase/seed.sql`. No
manual steps needed — just navigate to `/dev/auto-login` again.

## Testing

See the comprehensive [Testing Documentation](./testing/README.md) for:

- Test environment setup
- Writing and running tests
- CI/CD pipeline details
- Troubleshooting guide

Quick commands:

```bash
# Run all tests
./run-tests.sh

# Run with coverage
go test -v -coverprofile=coverage.out ./...
```

### Manual API Testing

Use the provided HTTP test file:

```bash
# Install httpie or use curl
pip install httpie

# Test health endpoint
http GET localhost:8847/health

# Test job creation (requires auth token)
http POST localhost:8847/v1/jobs \
  Authorization:"Bearer your-jwt-token" \
  domain=example.com \
  use_sitemap:=true
```

### Job Queue Testing

Test the job queue system:

```bash
# Run job queue test utility
go run ./cmd/test_jobs/main.go
```

## Code Organization

### Package Structure

cmd/ ├── app/ # Main application entry point └── test_jobs/ # Job queue testing
utility

internal/ ├── api/ # HTTP handlers and middleware ├── auth/ # Authentication
logic ├── crawler/ # Web crawling functionality ├── db/ # Database operations
├── jobs/ # Job queue and worker management └── util/ # Shared utilities

## Monitoring Fly Logs

For production investigations use `scripts/monitor_logs.sh`:

```bash
# Default: 10-second intervals for 4 hours
./scripts/monitor_logs.sh

# Custom run with descriptive name
./scripts/monitor_logs.sh --run-id "heavy-load-test"

# Custom intervals and duration
./scripts/monitor_logs.sh --interval 30 --iterations 120 --run-id "30min-check"
```

**Output structure:**

- Folder: `logs/YYYYMMDD/HHMM_<name>_<interval>s_<duration>h/`
  - Example: `logs/20251105/0833_heavy-load-test_10s_4h/`
- Raw logs: `raw/<timestamp>_iter<N>.log`
- JSON summaries: `<timestamp>_iter<N>.json`
- Aggregated outputs:
  - `time_series.csv` - per-minute log level counts
  - `summary.md` - human-readable report with critical patterns
  - Automatically regenerated after each iteration

**Defaults:**

- Interval: 10 seconds (better sampling than 60s)
- Iterations: 1440 (4 hours)
- Samples: 400 log lines per fetch

The script runs `scripts/aggregate_logs.py` automatically to process JSON
summaries into time-series data and markdown reports.

### Development Patterns

#### Error Handling

- Use wrapped errors: `fmt.Errorf("context: %w", err)`
- Log errors with context:
  `log.Error().Err(err).Str("job_id", id).Msg("Failed to process")`
- Capture critical errors in Sentry: `sentry.CaptureException(err)`

#### Database Operations

- Use PostgreSQL-style parameters: `$1, $2, $3`
- Wrap operations in transactions via `dbQueue.Execute()`
- Handle connection pooling automatically

#### Testing

- Place tests alongside implementation: `file_test.go`
- Use table-driven tests for multiple scenarios
- Mock external dependencies (HTTP, database)

## Debugging

### Log Levels

Set `LOG_LEVEL` in `.env.local`:

- `debug` - Verbose logging for development
- `info` - Standard operational logging
- `warn` - Warning conditions
- `error` - Error conditions only

### Sentry Integration

In development, Sentry captures all traces (100% sampling):

```bash
# Enable Sentry debugging
DEBUG=true
SENTRY_DSN=your_dsn
```

### Database Debugging

Enable SQL query logging:

```sql
-- In PostgreSQL console
ALTER SYSTEM SET log_statement = 'all';
SELECT pg_reload_conf();
```

### Common Debug Commands

```bash
# Check database connection
go run -ldflags="-X main.debugDB=true" ./cmd/app/main.go

# Run with race detection
go run -race ./cmd/app/main.go

# Profile memory usage
go run ./cmd/app/main.go -memprofile=mem.prof

# Check for goroutine leaks
GODEBUG=gctrace=1 go run ./cmd/app/main.go
```

## Contributing

### Code Quality

We enforce code quality with **golangci-lint** in CI, ensuring consistent
standards across the codebase.

#### CI Linting (Enforced)

Our **GitHub Actions CI** runs golangci-lint v2.9.0 with Go 1.26 support:

- **Runs automatically** on every push/PR
- **Blocks merges** if linting fails
- **Core linters enabled**: govet, staticcheck, errcheck, revive, gofmt,
  goimports, ineffassign, gocyclo, misspell
- **Configured for Australian English spelling**
- **Cyclomatic complexity threshold**: 35 (reduces over time as functions are
  refactored)

#### Formatting (Automatic)

**Pre-commit hooks automatically format files** - you don't need to do anything!

To manually format all files:

```bash
# Format everything (Go + docs/config + web files)
bash scripts/format.sh

# Or format individually:
gofmt -w .                                              # Go files only
prettier --write "**/*.{md,yml,yaml,json,html,css,js}"  # Docs/config/web files
```

#### Local Development (Fast Feedback)

Before pushing, run these **local** checks:

```bash
# 1. Basic static analysis (5-10 seconds)
go vet ./...

# 2. Run tests (1-2 minutes)
./run-tests.sh

# 4. Check coverage (optional)
go test -v -coverprofile=coverage.out ./...
```

#### Running golangci-lint Locally

If your local golangci-lint doesn't support Go 1.26.1, use Docker:

```bash
# Run linting via Docker (recommended)
docker run --rm -v "$(pwd)":/workspace -w /workspace \
  golangci/golangci-lint:v2.9.0 golangci-lint run

# Or install a Go 1.26.1-compatible version
brew upgrade golangci-lint  # macOS
# Then run: golangci-lint run
```

#### Recommended Workflow

```bash
# 1. 🏠 Local development - fast iteration
go fmt ./... && go vet ./... && go test ./...

# 2. 🚀 Push to GitHub
git add specific-file.go && git commit -m "Add new feature" && git push

# 3. ⚡ GitHub CI runs comprehensive checks
# - Linting (golangci-lint)
# - Unit tests
# - Integration tests
# - Coverage reporting
```

#### Pre-Submission Checklist

- [ ] Code formatted with `go fmt ./...`
- [ ] No issues from `go vet ./...`
- [ ] All tests pass with `./run-tests.sh`
- [ ] Update relevant documentation
- [ ] Push and verify GitHub Actions pass (including lint job)

### Git Workflow

See [BRANCHING.md](./BRANCHING.md) for comprehensive Git workflow.

Quick reference:

```bash
# Create feature branch from main
git checkout -b feature/your-feature

# Make changes and commit
git add .
git commit -m "feat: add new feature"

# Push and create PR to test-branch
git push origin feature/your-feature
```

### Commit Messages

Short, plain English — 5-6 words maximum. No conventional commit prefixes, no AI
attribution.

```text
Add cache warming endpoint
Fix job queue timeout bug
Update Supabase seed users
```

### Pull Request Process

1. **Update documentation** for any API or architectural changes
2. **Add/update tests** for critical functionality where regressions would be
   costly
3. **Ensure all tests pass**
4. **Update CHANGELOG.md** if the change affects users
5. **Reference relevant issues** in PR description

### Checking PR Status

Use `scripts/pr-status-check.sh` to get a quick summary of CI checks and
CodeRabbit review comments for any PR:

```bash
# Auto-detect PR from current branch
bash scripts/pr-status-check.sh

# Or specify a PR number
bash scripts/pr-status-check.sh 286
```

Output includes:

- CI check statuses (PASS/FAIL/RUNNING/PENDING/SKIP)
- Failed check error logs (when applicable)
- CodeRabbit review comments with resolution status (OPEN/RESOLVED) and severity
- Actionable agent prompt from the latest review

### Replying to Review Comments

Use `scripts/pr-comment-reply.sh` to reply to and resolve review threads:

```bash
# List open threads (indexed)
bash scripts/pr-comment-reply.sh --list

# Reply to a thread
bash scripts/pr-comment-reply.sh --reply 1 "Fixed in abc1234"

# Reply and resolve in one step
bash scripts/pr-comment-reply.sh --reply 1 "Deferring — reason here" --resolve

# Just resolve (no reply)
bash scripts/pr-comment-reply.sh --resolve 1
```

## Deployment

### Local Build

```bash
# Build for current platform
go build ./cmd/app

# Build for Linux (Fly.io deployment)
GOOS=linux GOARCH=amd64 go build ./cmd/app
```

### Docker Development

```bash
# Build container
docker build -t hover .

# Run with database link
docker run --env-file .env -p 8847:8847 hover
```

### Adding New HTML Pages (Avoid 404s)

When adding a new top-level page (for example `/welcome` or `/welcome/invite`),
you must update all required surfaces:

1. **Register route handlers**
   - Add `mux.HandleFunc(...)` entries in `internal/api/handlers.go`.
   - Add or update the corresponding `Serve...` methods.
2. **Create the HTML file**
   - Add the page file at repository root (for example `welcome.html`).
3. **Package the file in Docker**
   - Add a `COPY --from=builder /app/<page>.html .` line in `Dockerfile`.
   - If omitted, local runs may work, but Fly deployments will return 404.
4. **Verify before merge**
   - Run `docker build -t hover .`
   - Open the route in the built container or review app.

Recommended quick checks:

- `rg -n "/welcome|/your-path" internal/api/handlers.go`
- `rg -n "COPY --from=builder /app/.*\\.html" Dockerfile`

### Auth Redirect Contract

Keep social auth redirect behaviour centralised in `web/static/js/auth.js`.
Avoid page-by-page redirect logic unless the page intentionally owns a
specialised flow.

Baseline rules:

1. **Deep links return to themselves**
   - If auth starts on a deep link (path/query), return to that same URL after
     OAuth.
2. **Homepage uses default app landing**
   - If auth starts from `/`, route to the default signed-in landing page.
3. **Invite links complete invite first**
   - Preserve `invite_token` through OAuth, accept invite, then redirect to
     `/welcome`.
4. **Page-specific overrides are explicit**
   - If a page must override redirect behaviour, document why in code comments.

### Active Organisation Contract

Use backend state as the source of truth for active organisation selection:

1. `GET /v1/organisations` returns both:
   - `organisations`
   - `active_organisation_id`
2. Frontend org bootstrap in `web/static/js/core.js` must prefer
   `active_organisation_id` from API.
3. `localStorage` key `bb_active_org_id` is a cache/fallback only; it must not
   override backend-selected organisation after invite acceptance or org switch.

### Environment-Specific Configs

### Integration Requirements

- Review apps and CI must set `LOOPS_API_KEY` if invite email delivery needs to
  be exercised.
- Invite flows described in **Auth Redirect Contract** rely on this integration
  for delivery (`invite_token` handling and acceptance still run in-app).
- If `LOOPS_API_KEY` is missing, invite records are still created, but email
  delivery is skipped and should be treated as non-delivery for test runs.
- Configure `LOOPS_API_KEY` in your review app and CI environment variables when
  validating invite emails end-to-end.

**Development**:

- Hot reloading enabled
- Verbose logging
- 100% Sentry trace sampling
- Debug mode enabled

**Production**:

- Optimised builds
- Error-level logging
- 10% Sentry trace sampling
- Security hardening

## Troubleshooting

### Common Issues

**Database Connection Errors**:

```bash
# Check local Supabase is running
supabase status

# Verify the DB port (local Supabase uses 54322, not 5432)
# DATABASE_URL=postgresql://postgres:postgres@127.0.0.1:54322/postgres
docker ps | grep supabase_db
```

**Port Already in Use**:

```bash
# Find process using port 8847
lsof -i :8847

# Kill process
kill -9 <PID>
```

**Module Dependencies**:

```bash
# Clean module cache
go clean -modcache

# Re-download dependencies
go mod download
```

## Code Quality & Refactoring

### Function Design Principles

Hover follows focused, testable function design:

- **Function Size**: Keep functions under 50 lines where possible
- **Single Responsibility**: Each function should do one thing well
- **Testing**: Add tests where they prevent costly regressions or aid
  refactoring
- **Error Handling**: Use idiomatic Go patterns (simple error returns)

### Refactoring Large Functions

When encountering functions >50 lines, apply **Extract + Test + Commit**:

1. **Analyse Structure**: Map distinct responsibilities
2. **Extract Functions**: Pull out focused, single-responsibility functions
3. **Create Tests**: Write comprehensive tests with table-driven patterns
4. **Commit Steps**: Commit each extraction separately
5. **Verify Integration**: Ensure no regressions

### Testing Patterns

**Table-Driven Tests**:

```go
func TestValidateInput(t *testing.T) {
    tests := []struct {
        name        string
        input       string
        expectError bool
    }{
        {"valid_input", "test", false},
        {"invalid_input", "", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := validateInput(tt.input)
            if tt.expectError {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

**Database Testing with sqlmock**:

```go
func TestDatabaseOperation(t *testing.T) {
    db, mock, err := sqlmock.New()
    require.NoError(t, err)
    defer db.Close()

    mock.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))

    err = createTable(db)
    assert.NoError(t, err)
    assert.NoError(t, mock.ExpectationsWereMet())
}
```

### Recent Refactoring Success

**5 monster functions eliminated:**

- `getJobTasks`: 216 → 56 lines (74% reduction)
- `CreateJob`: 232 → 42 lines (82% reduction)
- `setupJobURLDiscovery`: 108 → 17 lines (84% reduction)
- `setupSchema`: 216 → 27 lines (87% reduction)
- `WarmURL`: 377 → 68 lines (82% reduction)

**Results**: 80% complexity reduction, 350+ tests created during refactoring

**Hot Reloading Not Working**:

```bash
# Verify Air configuration
cat .air.toml

# Reinstall Air
go install github.com/air-verse/air@latest
```

### Performance Issues

**High Memory Usage**:

- Check for goroutine leaks with `go tool pprof`
- Monitor database connection pool usage
- Verify proper cleanup of HTTP clients

**Slow Database Queries**:

- Enable query logging in PostgreSQL
- Use `EXPLAIN ANALYZE` for query performance
- Check connection pool settings

### Flight Recorder

For detailed performance debugging, see
[Flight Recorder Documentation](flight-recorder.md). The flight recorder
provides runtime trace data that can help diagnose:

- Goroutine scheduling issues
- Memory allocation patterns
- CPU usage hotspots
- Lock contention

### Getting Help

1. **Check existing documentation** in this guide and
   [ARCHITECTURE.md](ARCHITECTURE.md)
2. **Search closed issues** on GitHub for similar problems
3. **Enable debug logging** to get more context
4. **Create minimal reproduction** case for bugs
5. **Open GitHub issue** with detailed information

## Next Steps

After setting up development:

1. **Read [ARCHITECTURE.md](ARCHITECTURE.md)** to understand system design
2. **Review [API.md](API.md)** for endpoint documentation
3. **Check [DATABASE.md](DATABASE.md)** for schema details
4. **Explore the codebase** starting with `cmd/app/main.go`
5. **Run the test suite** to verify everything works
