# Structured Logging

When adding logging to Go code in this project, use the centralised
`internal/logging` package. It provides a unified `slog`-based logger that
writes structured JSON to stdout **and** captures errors to Sentry automatically
— no direct zerolog or sentry imports required in most files.

## Component logger pattern

Every package declares a package-level logger with its component name:

```go
import "github.com/good-native/hover/internal/logging"

var workerLog = logging.Component("worker")
```

Then call it like stdlib `slog`:

```go
workerLog.Info("Job started", "job_id", jobID, "domain", domain)
workerLog.Warn("Retry scheduled", "attempt", attempt, "delay_ms", delay.Milliseconds())
workerLog.Error("DB write failed", "error", err, "org_id", orgID)
```

The component name appears as a structured field in every log line and as a
Sentry tag, enabling filtering by subsystem in both log aggregators and Sentry.

## Component taxonomy

| Component    | Package(s)                              |
| ------------ | --------------------------------------- |
| `worker`     | `cmd/worker/main.go`                    |
| `queue`      | `internal/db/queue.go`                  |
| `batch`      | `internal/db/batch.go`                  |
| `db`         | `internal/db/*.go` (all other DB files) |
| `api`        | `internal/api/*.go`                     |
| `crawler`    | `internal/crawler/*.go`                 |
| `archive`    | `internal/archive/*.go`                 |
| `notify`     | `internal/notifications/*.go`           |
| `auth`       | `internal/auth/*.go`                    |
| `startup`    | `cmd/app/main.go`                       |
| `techdetect` | `internal/techdetect/*.go`              |
| `util`       | `internal/util/*.go`                    |
| `jobs`       | `internal/jobs/*.go`                    |
| `broker`     | `internal/broker/*.go`                  |
| `pressure`   | `internal/db/pressure.go`               |

## Level selection

- **Debug**: Noisy instrumentation, only in development (`LOG_LEVEL=debug`)
- **Info**: Expected state changes (job created, worker started, webhook
  received)
- **Warn**: Unexpected but recovered behaviour (retry scheduled, rate limited,
  fallback used)
- **Error**: Failures we can't auto-correct (500 responses, DB write rejected,
  worker panic)

`Error` and `Fatal` calls are **automatically captured to Sentry** — no explicit
`sentry.CaptureException` needed.

## Sentry best practices

### Use static message strings

Sentry groups issues by their fingerprint. Dynamic values in the message create
a new Sentry issue per unique value — avoid this:

```go
// BAD — creates unique Sentry issue per domain
workerLog.Error(fmt.Sprintf("Failed to crawl %s", domain), "error", err)

// GOOD — dynamic values go in structured fields, message stays static
workerLog.Error("Failed to crawl domain", "error", err, "domain", domain)
```

The logging library automatically sets the Sentry fingerprint to
`[component, message]` so all occurrences of the same error group together
regardless of field values.

### Suppress capture for expected errors

Use `logging.NoCapture(ctx)` to prevent routine errors from reaching Sentry:

```go
ctx = logging.NoCapture(ctx)
workerLog.ErrorContext(ctx, "Page not found", "url", url) // logged, not sent to Sentry
```

Typical candidates: 404s, validation failures, expected rate limits.

### Keep tracing spans — they're separate from logging

`sentry.StartSpan` calls are performance tracing, not error capture. Keep them:

```go
span := sentry.StartSpan(ctx, "crawl.fetch")
defer span.Finish()
```

## Key principles

1. One log per meaningful event — avoid logging inside tight loops
2. Always pass the error as `"error", err` on Error level
3. Include `"request_id"` in API handler logs for correlation
4. Never log secrets, JWTs, credentials, or user content
5. Log once at the boundary that handles the error — don't double-log
6. Prefer `ErrorContext` when you have a `context.Context` (enables `NoCapture`)

## Setup (main.go / entry points)

```go
logging.Setup(logging.ParseLevel(config.LogLevel), config.Env)
```

Pass `logging.BeforeSend` to `sentry.Init` to normalise dynamic values in event
messages:

```go
sentry.Init(sentry.ClientOptions{
    Dsn:        config.SentryDSN,
    BeforeSend: logging.BeforeSend,
    // ...
})
```

## Reference

See `internal/logging/logging.go` for the full implementation and
`internal/logging/logging_test.go` for usage examples.
