# Hover Architecture

## System Overview

Hover is a web cache warming service built in Go. It crawls URLs across one or
more domains, warms caches, and records performance metrics. The system is split
into two services: an **API server** (`cmd/app`) that handles HTTP traffic and
schedules tasks, and a **worker service** (`cmd/worker`) that consumes tasks and
executes crawls. The two communicate via a **Redis broker** (ZSET scheduler +
Streams) with PostgreSQL (via Supabase) as the durable source of truth.

---

## Core Components

### Redis Broker

The broker decouples task scheduling from task execution across process
boundaries. It replaces the previous in-process worker-pool model.

- **Scheduler** (`internal/broker/scheduler.go`): the API server writes task
  envelopes into a per-job ZSET (`schedule:{job_id}`) with a `run_at` timestamp
  score. ZSET rescheduling is dual-written to `tasks.run_at` so pacing delays
  survive a Redis flush.
- **Dispatcher** (`internal/broker/dispatcher.go`, runs on the worker service):
  pops due entries from ZSETs (`ZRANGEBYSCORE` + `ZREM`) and `XADD`s them to the
  per-job Redis Stream (`stream:{job_id}`), subject to job-level concurrency
  caps and per-domain pacing.
- **Consumer** (`internal/broker/consumer.go`): worker goroutines `XREADGROUP`
  from streams using consumer group `group:{job_id}`. Stale messages (holder
  crash, long run) are reclaimed via `XAUTOCLAIM`.
- **Domain Pacer** (`internal/broker/pacer.go`): adaptive per-domain
  token-bucket (Lua-scripted in Redis). Rate-limit signals from crawler
  responses widen the delay; sustained successes narrow it.
- **Running Counters** (`internal/broker/counters.go`): per-job in-memory
  running-task count, periodically synced to `jobs.running_tasks` in Postgres
  with drift telemetry.
- **Outbox sweeper** (`internal/broker/outbox.go`): drains the `task_outbox`
  table into the scheduler. Tasks are written to the outbox in the same
  transaction as the `tasks` row, guaranteeing durable scheduling even if the
  fire-and-forget `OnTasksEnqueued` callback fails.

For all configurable values, see [CONFIG_REFERENCE.md](CONFIG_REFERENCE.md). For
operational detail (key map, failure modes, common operations) see
[REDIS_BROKER_RUNBOOK.md](../operations/REDIS_BROKER_RUNBOOK.md).

### Worker Service

The worker binary runs a fixed pool of goroutines (`WORKER_COUNT`, default 30)
that each consume from Redis Streams and execute crawls.

- **Stream worker pool** (`internal/jobs/stream_worker.go`): each goroutine owns
  its XREADGROUP consumer identity (`worker-{machine}-{idx}`) and processes
  messages serially. Concurrency within a worker is driven by
  `WORKER_CONCURRENCY` (tasks per goroutine).
- **Task executor** (`internal/jobs/executor.go`): runs a single crawl,
  classifies the outcome (ok / retryable / permanent failure / rate-limited),
  and feeds the result to the batch writer.
- **Batch writer** (`internal/db/batch.go`): buffers task-status updates and
  link-discovery inserts to amortise DB writes.
- **Graceful shutdown**: on SIGTERM the pool stops reading new messages and
  drains in-flight tasks up to a bounded timeout.

### Database Layer (PostgreSQL via Supabase)

- **Normalised schema**: `domains` → `pages` → `jobs` → `tasks` hierarchy
  reduces storage redundancy and simplifies constraint enforcement
- **Row-level locking**: `FOR UPDATE SKIP LOCKED` for contention-free task
  claiming under high worker concurrency
- **Incremental counters**: Job progress fields (`completed_tasks`,
  `failed_tasks`, `skipped_tasks`, `progress`, `status`) are maintained by
  row-level triggers using O(1) delta arithmetic — no COUNT(\*) scans
- **Connection pooling**: per-service `DB_MAX_OPEN_CONNS` (60 each) against the
  shared Supabase pgBouncer; bulk writes on the API server gated by a
  pressure-controlled queue semaphore
- **Row Level Security**: Enforced at the database layer for multi-tenant data
  isolation

### API Layer

- **RESTful design**: `/v1/*` endpoints with standardised JSON responses
- **Authentication**: JWT validation via Supabase Auth; user and organisation
  context extracted per request
- **Middleware**: CORS, structured logging, IP-based rate limiting, request ID
  tracking
- **Dashboard API**: Serves pre-aggregated stats, job lists, and task-level
  detail to the frontend

### Crawler

- **HTTP transport**: Connection-pooled `net/http` client (150 global / 50
  per-host)
- **Cache validation**: Inspects `X-Cache`, `CF-Cache-Status`, and similar
  headers; records hit/miss/stale
- **Link discovery**: Optional HTML parsing to extract additional URLs and
  enqueue them as new tasks within the same job
- **Robots.txt compliance**: Fetched and cached per domain; `Crawl-Delay`
  honoured via the domain pacer
- **Sitemap processing**: Parses XML sitemaps (including sitemap indexes) to
  seed jobs with a full URL list before crawling begins

---

## Application Entry Points (`cmd/`)

| Directory                  | Purpose                                                                 |
| -------------------------- | ----------------------------------------------------------------------- |
| `cmd/app/`                 | API server: HTTP handlers, job creation, task scheduling into Redis     |
| `cmd/worker/`              | Worker service: Redis Stream consumers, crawl execution, result persist |
| `cmd/hover/`               | CLI tool for job management and diagnostics                             |
| `cmd/archive_key_migrate/` | One-off migration for archive storage keys                              |

---

## Internal Packages (`internal/`)

| Package          | Responsibility                                                            |
| ---------------- | ------------------------------------------------------------------------- |
| `api/`           | HTTP handlers, middleware, request/response types                         |
| `archive/`       | Cold-storage archival to Cloudflare R2 (active background job)            |
| `auth/`          | JWT validation, session management                                        |
| `benchmarks/`    | Go benchmark helpers for performance regression testing                   |
| `broker/`        | Redis broker: scheduler, dispatcher, consumer, pacer, outbox sweeper      |
| `cache/`         | In-process caching utilities                                              |
| `crawler/`       | HTTP crawl logic, sitemap parsing, cache validation, link extraction      |
| `db/`            | PostgreSQL connection, queue semaphore, batch writer, page/domain records |
| `jobs/`          | Job manager, stream worker pool, task executor, link discovery            |
| `loops/`         | Background loop helpers (ticker management, graceful stop)                |
| `mocks/`         | Test doubles for DB and crawler interfaces                                |
| `notifications/` | In-app notification creation and delivery                                 |
| `observability/` | OpenTelemetry metrics, Grafana/OTLP export, Sentry integration            |
| `storage/`       | Supabase Storage client for HTML upload and retrieval                     |
| `techdetect/`    | Technology fingerprinting from crawled page content                       |
| `testutil/`      | Shared test helpers and fixtures                                          |
| `util/`          | URL normalisation, string helpers                                         |

---

## Data Model

### Core Tables

**`domains`** — one row per unique domain name (e.g. `example.com`). Shared
across jobs and organisations.

**`pages`** — one row per unique `(domain_id, host, path)`. Page-level metadata
and crawl metrics accumulate here across multiple jobs.

**`jobs`** — one row per crawl run. Tracks lifecycle status, progress counters
(total/completed/failed/skipped/running/pending/waiting tasks), concurrency
settings, adaptive delay state, and timing.

**`tasks`** — one row per URL × job. The hot table: all status transitions, HTTP
results, timing, and HTML storage references live here. See the migrations for
the current column set — the schema has grown significantly from the initial
version.

### Task Lifecycle

```text
waiting → pending → running → completed
                           ↘ failed
                           ↘ skipped
```

- **waiting**: Created but not yet eligible for dispatch (quota or concurrency
  limit reached, or pacer back-off active). A matching row in `task_outbox`
  drives eventual scheduling into Redis.
- **pending**: Scheduled into the Redis ZSET; `tasks.run_at` mirrors the ZSET
  score so the deadline survives a Redis flush. The dispatcher moves the
  envelope to the stream once due and concurrency / pacing permit.
- **running**: Claimed by a worker via `XREADGROUP`; HTTP request in flight.
- **completed / failed / skipped**: Terminal states. Trigger the incremental
  counter update on the parent job row and remove the message from the stream's
  pending entries list.

For the case → action mapping ("when X happens to a domain or page, what do we
do to the job and tasks?") see [`CRAWL_HANDLING.md`](CRAWL_HANDLING.md).

### Job Counters (Trigger-Maintained)

`jobs.completed_tasks`, `failed_tasks`, `skipped_tasks`, `running_tasks`,
`pending_tasks`, `waiting_tasks`, `progress`, `status`, `started_at`, and
`completed_at` are all maintained by row-level triggers on the `tasks` table. Go
code never manually increments these — the DB is the source of truth.

`running_tasks` is a special case: it is incremented asynchronously in memory
and flushed to the DB in batches to avoid per-task hot-row contention.

---

## Key Design Decisions

### O(1) Trigger Counters

All job progress fields use incremental delta arithmetic in trigger functions
(`update_job_counters`, `update_job_queue_counters`) rather than correlated
COUNT(\*) subqueries. This keeps trigger overhead constant regardless of job
size. See
`supabase/migrations/20260409111417_incremental_update_job_counters.sql` and
`20260409120000_unify_job_progress_triggers.sql`.

### Outbox Pattern for Durable Scheduling

`JobManager.EnqueueJobURLs` writes both the `tasks` row and a matching
`task_outbox` row in one transaction. A fire-and-forget `OnTasksEnqueued`
callback handles the common case of scheduling immediately into Redis. If that
callback fails (Redis blip, process crash), the outbox sweeper in the worker
service picks the row up on its next tick and schedules it durably. This closes
the orphan-task risk where a committed task row could be invisible to the
dispatcher.

### Domain Pacer

`internal/broker/pacer.go` implements an adaptive per-domain token bucket via a
Redis Lua script. Rate-limit signals from crawler responses (429, 503, specific
headers) widen the per-domain delay; sustained successes narrow it. The
dispatcher consults the pacer before moving a task from ZSET to stream. When a
domain is paced, the dispatcher reschedules the task with a later `run_at`
(mirrored to Postgres) rather than stalling the whole worker.

### Batch Running-Task Counters

`jobs.running_tasks` is driven by `internal/broker/counters.go`, which keeps a
per-job in-memory counter incremented on message receive and decremented on
outcome. A background sync loop (`REDIS_COUNTER_SYNC_INTERVAL_S`, default 5s)
flushes changes to Postgres, emitting drift telemetry
(`bee.broker.counter_sync_skew`) so any divergence between Redis truth and the
Postgres snapshot is observable.

### HTML Cold Storage

Crawled HTML is optionally captured and written to Supabase Storage (hot) by an
async worker pool (`GNH_HTML_PERSIST_WORKERS = 32`). A background archival job
(`internal/archive/`) sweeps completed-job HTML to Cloudflare R2 at a
configurable interval, then marks tasks as archived. Jobs are promoted to
`archived` status once all their tasks are confirmed in cold storage.

---

## Observability

### Metrics (OpenTelemetry → Grafana)

Both services export metrics via OTLP to Grafana Cloud. Key signals:

- Broker throughput: `bee.broker.dispatch_total` (by outcome), stream length,
  scheduled ZSET depth, consumer pending count
- Consumer latency: `bee.broker.consumer_message_age_ms` (time from XADD to
  XREADGROUP receipt)
- Pacer behaviour: `bee.broker.pacer_pushback_total`, `pacer_delay_ms`
- Outbox health: `bee.broker.outbox_backlog`, `outbox_age_seconds`
- Redis health: `bee.broker.redis_ping_duration_ms`, pool in-use/idle/wait
- Tasks completed / failed / skipped per minute (worker-side outcome labels)
- DB pool utilisation and queue semaphore wait time
- Redis ↔ Postgres counter drift (`bee.broker.counter_sync_skew`)

### Error Tracking (Sentry)

Sentry captures unexpected errors with stack traces. Routine failures (404s,
timeouts on individual tasks) are not sent to Sentry — only structural failures
(job creation, worker startup, DB connectivity, stuck-job recovery) that
indicate system-level problems.

Sample rate: 10% traces in production, 100% in development.

### Flight Recorder

When `FLIGHT_RECORDER_ENABLED=true`, the application captures Go runtime trace
data on request. Useful for diagnosing goroutine scheduling, GC pressure, and
lock contention. See [flight-recorder.md](flight-recorder.md).

---

## Real-time Frontend Updates

Both the dashboard and job detail page use **Supabase Realtime Postgres
Changes** subscriptions to receive live updates without polling:

- **Dashboard** (`gnh-auth-extension.js`): Subscribes to INSERT/UPDATE/DELETE on
  `jobs` filtered by `organisation_id`. Uses a 250ms debounce to coalesce rapid
  updates, with 1s active / 10s idle fallback polling.
- **Job page** (`job-page.js`): Subscribes to UPDATE on `jobs` for the specific
  `job_id`. Task-level progress is derived from job counter fields — there is no
  separate tasks subscription. Same 250ms debounce + 1s fallback.

Both subscribers ignore the realtime payload content entirely and use the event
purely as a signal to re-fetch current state from the API. This means
`REPLICA IDENTITY DEFAULT` is safe (only the PK is needed in the `old` record).

---

## Frontend

The dashboard UI uses a vanilla JS template-and-data-binding system with no
build step. JS files are served from `web/static/js/` and use `gnh-` prefixed
attribute conventions and file names. The system handles:

- Attribute-based event delegation (`gnh-action`)
- Data binding and template rendering (`data-gnh-bind`, `data-gnh-template`)
- Authentication state gating
- Real-time data refresh via the Supabase subscription above

---

## Deployment

| Concern      | Solution                                                             |
| ------------ | -------------------------------------------------------------------- |
| Hosting      | Fly.io, `syd` region — `hover` API app + `hover-worker` worker app   |
| Broker       | Fly.io Upstash Redis (per-env instance; preview apps get their own)  |
| Database     | Supabase PostgreSQL with pgBouncer pooler                            |
| Auth         | Supabase Auth on custom domain                                       |
| Real-time    | Supabase Realtime (Postgres Changes)                                 |
| Hot storage  | Supabase Storage (HTML captures, assets)                             |
| Cold storage | Cloudflare R2 (archived HTML, via `internal/archive`)                |
| CDN          | Cloudflare                                                           |
| Monitoring   | Grafana Cloud (OTLP metrics) + Sentry                                |
| CI/CD        | GitHub Actions → Fly.io deploy (API + worker deployed independently) |

### Schema Management

All schema changes go through numbered migration files in
`supabase/migrations/`. Migrations are additive only — no destructive changes to
deployed columns or indexes without explicit review. The migration filename
timestamp is the authoritative ordering key.

### Health Checks

Fly.io polls `GET /health` every 15s. The handler verifies DB connectivity and
returns 200/503. Graceful shutdown drains in-flight tasks before termination.
