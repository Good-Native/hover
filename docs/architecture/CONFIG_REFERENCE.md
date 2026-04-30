# Configuration Reference

Last reviewed: 2026-04-21

Every configurable dial in the application — env vars, hardcoded constants, and
their relationships. Values reflect production unless noted. For the flat
inventory of env vars and their classification (secret vs non-secret), see
[`docs/operations/ENV_VARS.md`](../operations/ENV_VARS.md).

---

## Key relationships

Services split (`cmd/app` = API, `cmd/worker` = crawl execution) and share a
Redis broker for scheduling. Each service has its own DB pool budget against the
shared Supabase pgBouncer.

```text
API server (cmd/app)
  DB_MAX_OPEN_CONNS (60)
    └── DB_POOL_RESERVED_CONNECTIONS (4)  →  available = 56
          └── bulk lane hard cap (88, capped by availability)
                └── bulk PressureController soft limit (self-tuning)

Worker service (cmd/worker)
  DB_MAX_OPEN_CONNS (60) — batch writes, counter sync, outbox sweeper
    └── WORKER_COUNT (30) × WORKER_CONCURRENCY (20) = 600 task slots

Redis broker (shared)
  REDIS_POOL_SIZE (200)
    └── ZSET schedule:{job} + Stream stream:{job} + consumer group group:{job}
          └── Dispatcher interval (100ms) + autoclaim (30s)
```

The two services deploy independently and are sized to fit under Supabase's
180-connection pool with headroom for internal Supabase traffic. Direct DB calls
(page writes, domain lookups, etc.) bypass the queue semaphore and draw from the
shared pool.

---

## DB connection pool

**Source:** `internal/db/db.go`, `fly.toml`

| Env var / constant       | Production value    | Code default | What it controls                                 |
| ------------------------ | ------------------- | ------------ | ------------------------------------------------ |
| `DB_MAX_OPEN_CONNS`      | **60** (both tomls) | 70           | Per-service cap on open connections to pgBouncer |
| `DB_MAX_IDLE_CONNS`      | **15** (worker)     | 20           | Idle connections kept warm (API uses default)    |
| `DATABASE_QUEUE_URL`     | unset               | —            | Optional second DSN for tasks/outbox in split-DB |
| `defaultConnMaxLifetime` | hardcoded           | 5 min        | Max connection lifetime                          |
| `defaultConnMaxIdleTime` | hardcoded           | 2 min        | Idle connection eviction                         |
| `statementTimeoutMs`     | hardcoded           | 60s          | Per-statement timeout (added to DSN)             |
| `idleInTxnTimeoutMs`     | hardcoded           | 30s          | Idle-in-transaction timeout (added to DSN)       |

When `DATABASE_QUEUE_URL` is set, the worker routes `tasks`, `task_outbox`, and
scheduler writes to the queue database while keeping reads/writes of `jobs`,
`pages`, etc. on the primary. In single-DB deployments the queue URL is unset
and both halves share `DATABASE_URL`.

Supabase pooler is auto-detected via the `pooler.supabase.com` hostname in
`DATABASE_URL`. When detected, `simple_protocol` and `pgbouncer=true` are
appended to the DSN automatically.

Supabase connection pool size is configured on the Supabase dashboard (currently
180). Leave headroom above `DB_MAX_OPEN_CONNS` for Supabase-internal connections
(realtime, auth, API, migrations).

---

## Queue semaphore

**Source:** `internal/db/queue.go`, `internal/db/pressure.go`

The queue now uses two DB execution lanes:

- **Control lane** — reserved for task claim, task release, and other low-volume
  broker control paths. This lane is not pressure-shed by the bulk EMA.
- **Bulk lane** — used for heavier write paths such as sitemap insertion,
  discovered-link persistence, traffic-score updates, and batch task-status
  flushes.

The bulk lane has two limits:

- **Hard limit** — `min(DB_QUEUE_MAX_CONCURRENCY, available_after_reserve)`, set
  at startup.
- **Soft limit** — the pressure-adjusted effective bulk limit maintained by
  `PressureController`. Starts at `GNH_PRESSURE_INITIAL_LIMIT` (production 88)
  and moves between `GNH_PRESSURE_MIN_LIMIT` (production 30) and the hard limit
  based on observed bulk execution time.

| Env var / constant             | Production value    | Default | What it controls                                                        |
| ------------------------------ | ------------------- | ------- | ----------------------------------------------------------------------- |
| `DB_QUEUE_MAX_CONCURRENCY`     | **88** (`fly.toml`) | 12      | Bulk-lane hard cap before pool-reserve and control-lane carving         |
| `DB_POOL_RESERVED_CONNECTIONS` | **4** (unset)       | 4       | Connections held back from the semaphore budget                         |
| `DB_POOL_WARN_THRESHOLD`       | **0.90** (unset)    | 0.90    | Log warn at 90% pool usage                                              |
| `DB_POOL_REJECT_THRESHOLD`     | **0.95** (unset)    | 0.95    | Fire Sentry "DB pool saturated" at 95%                                  |
| `defaultExecuteTimeout`        | hardcoded           | 30s     | Context timeout for `Execute`/`ExecuteWithContext` when caller has none |
| `controlExecuteTimeout`        | hardcoded           | 10s     | Shorter timeout for control-lane operations                             |
| `DB_TX_MAX_RETRIES`            | **5** (`fly.toml`)  | 3       | Transaction retry attempts on retryable errors                          |
| `DB_TX_BACKOFF_BASE_MS`        | **200ms** (unset)   | 200ms   | Initial TX retry backoff                                                |
| `DB_TX_BACKOFF_MAX_MS`         | **1500ms** (unset)  | 1500ms  | Max TX retry backoff                                                    |

### Adaptive pressure controller

**Source:** `internal/db/pressure.go`

Automatically reduces the semaphore soft limit when Supabase is under load and
restores it when pressure eases. Signal is bulk-lane `exec_total` per
transaction — the cumulative time spent actually executing DB queries (not
semaphore wait time). Slow queries indicate Supabase is overloaded; fast queries
indicate headroom.

| Env var / constant           | Default               | What it controls                                        |
| ---------------------------- | --------------------- | ------------------------------------------------------- |
| `GNH_PRESSURE_HIGH_MARK_MS`  | 500ms                 | EMA above this triggers a reduction                     |
| `GNH_PRESSURE_LOW_MARK_MS`   | 100ms                 | EMA below this triggers restoration                     |
| `GNH_PRESSURE_INITIAL_LIMIT` | hardcoded to lane cap | Starting soft limit before any shedding                 |
| `GNH_PRESSURE_MIN_LIMIT`     | 30                    | Minimum soft limit floor                                |
| `GNH_PRESSURE_STEP_DOWN`     | 5                     | Slots removed per shedding adjustment                   |
| `pressureEMAAlpha`           | 0.15                  | Smoothing factor — lower = slower to react, more stable |
| `pressureStepUp`             | 3                     | Slots added per restore adjustment                      |
| `pressureWarmupSamples`      | 5                     | Observations required before the controller acts        |

Deadband: EMA between 100ms and 500ms → limit holds steady. If
`GNH_PRESSURE_LOW_MARK_MS >= GNH_PRESSURE_HIGH_MARK_MS` the controller logs a
warning and falls back to defaults.

Production tuning currently sets `GNH_PRESSURE_HIGH_MARK_MS=80`,
`GNH_PRESSURE_LOW_MARK_MS=40`, `GNH_PRESSURE_INITIAL_LIMIT=88`,
`GNH_PRESSURE_MIN_LIMIT=30`, and `GNH_PRESSURE_STEP_DOWN=5`.

Typical lifecycle under load: limit 88 → 83 → 78 … → 30 (floor), then recovers 3
slots every 30s as bulk execution time drops back below the low mark.

---

## Stream worker pool (worker service)

**Source:** `cmd/worker/main.go`, `internal/jobs/stream_worker.go`

The worker service runs a fixed-size pool of goroutines that each consume from
Redis Streams via `XREADGROUP`. Unlike the old DB-polling worker pool, the
stream worker pool does not scale dynamically — scaling is handled by running
more worker machines in Fly.io.

| Env var / constant   | Production value            | Default | What it controls                                     |
| -------------------- | --------------------------- | ------- | ---------------------------------------------------- |
| `WORKER_COUNT`       | **30** (`fly.worker.toml`)  | 30      | Number of stream consumer goroutines                 |
| `WORKER_CONCURRENCY` | **20** (`fly.worker.toml`)  | 1       | Tasks per worker goroutine (range 1–20)              |
| `GNH_MAX_WORKERS`    | **130** (`fly.worker.toml`) | —       | Legacy ceiling retained for scaling hints in metrics |

Total base capacity = `WORKER_COUNT × WORKER_CONCURRENCY` = 30 × 20 = **600
concurrent task slots per worker machine**.

Review apps deploy their own worker machine (`.fly/review_apps.worker.toml`)
with `APP_ENV=staging`, so preview branches exercise the real Redis-backed path.
Preview OTEL exports can be filtered via `deployment.environment=staging`.

---

## Shared crawler

**Source:** `internal/crawler/config.go`, `internal/crawler/crawler.go`,
`fly.toml`

All workers share a single crawler instance. Its internal Colly limit is a
separate bottleneck from worker count and worker concurrency.

| Env var / constant            | Production value     | Default | What it controls                                           |
| ----------------------------- | -------------------- | ------- | ---------------------------------------------------------- |
| `GNH_CRAWLER_MAX_CONCURRENCY` | **100** (`fly.toml`) | 10      | Global crawler request parallelism across all worker tasks |
| `DefaultTimeout`              | hardcoded            | 30s     | Per-request crawler HTTP client timeout                    |
| `RateLimit`                   | hardcoded            | 5       | Base Colly delay range: `200ms` to `1s` per request        |

The crawler cap is global because `crawler.New(...)` creates one shared Colly
collector with `DomainGlob="*"` and `Parallelism=MaxConcurrency`.

---

## Redis broker

**Source:** `internal/broker/`, `cmd/worker/main.go`

The broker owns task scheduling and dispatch. The API server writes task
envelopes into per-job ZSETs; the worker service runs the dispatcher that moves
due entries onto per-job streams for consumers to pick up.

### Client + pool

**Source:** `internal/broker/redis.go`

| Env var / constant         | Production value            | Default | What it controls                                              |
| -------------------------- | --------------------------- | ------- | ------------------------------------------------------------- |
| `REDIS_URL`                | Upstash DSN (Fly secret)    | —       | Redis connection string (optional on API; required on worker) |
| `REDIS_TLS_ENABLED`        | set when DSN is `rediss://` | false   | Force TLS regardless of DSN scheme                            |
| `REDIS_POOL_SIZE`          | **200** (`fly.worker.toml`) | 50      | go-redis connection pool size                                 |
| `STREAM_ACTIVE_JOBS_LIMIT` | **200** (unset)             | 200     | Max concurrent active job streams per dispatcher              |

When `REDIS_URL` is unset on the API server, the server starts in
degraded-dispatch mode: new tasks still land in Postgres + outbox, but no
scheduling happens until the worker (which requires Redis) drains the outbox.

### Dispatcher

**Source:** `internal/broker/dispatcher.go`

Runs on the worker service. Polls active job ZSETs for due entries and moves
them onto streams, gated by concurrency + pacer.

| Env var / constant           | Production value              | Default | What it controls                                |
| ---------------------------- | ----------------------------- | ------- | ----------------------------------------------- |
| `REDIS_DISPATCH_INTERVAL_MS` | **100ms** (`fly.worker.toml`) | 200ms   | Interval between dispatcher sweeps              |
| `REDIS_DISPATCH_BATCH_SIZE`  | **50** (`fly.worker.toml`)    | 25      | Max entries moved ZSET→Stream per job per sweep |

### Consumer + autoclaim

**Source:** `internal/broker/consumer.go`

| Env var / constant               | Production value               | Default | What it controls                                                              |
| -------------------------------- | ------------------------------ | ------- | ----------------------------------------------------------------------------- |
| `REDIS_CONSUMER_BLOCK_MS`        | **2000ms** (`fly.worker.toml`) | 2000ms  | XREADGROUP BLOCK duration; `-1` internally means non-blocking                 |
| `REDIS_CONSUMER_READ_COUNT`      | **10** (`fly.worker.toml`)     | 10      | Max messages per XREADGROUP call                                              |
| `REDIS_AUTOCLAIM_INTERVAL_S`     | **30s** (`fly.worker.toml`)    | 30s     | XAUTOCLAIM sweep interval for reclaiming stale pending messages               |
| `REDIS_AUTOCLAIM_COUNT`          | **100** (`fly.worker.toml`)    | 100     | Per-call XAUTOCLAIM COUNT — bounds a single round-trip                        |
| `REDIS_AUTOCLAIM_MAX_PER_SWEEP`  | **1000** (`fly.worker.toml`)   | 1000    | Safety cap on messages reclaimed per tick so one hot job cannot starve others |
| `REDIS_AUTOCLAIM_MIN_IDLE_S`     | **180s** (`fly.worker.toml`)   | 180s    | Min pending-idle time before a message is eligible for reclaim                |
| `REDIS_AUTOCLAIM_MAX_DELIVERIES` | **3** (`fly.worker.toml`)      | 3       | Dead-letter after this many deliveries                                        |

`ReclaimStale` loops the XAUTOCLAIM cursor to `"0-0"` within a single tick so
bursts of stale messages drain in one sweep rather than across many ticks,
subject to the per-tick safety cap.

### Running counters

**Source:** `internal/broker/counters.go`

Replaces the old in-memory batch increment loop. The per-job running-task count
is incremented on receive, decremented on outcome, and synced to
`jobs.running_tasks` on a timer.

| Env var / constant                   | Production value             | Default | What it controls                                                                              |
| ------------------------------------ | ---------------------------- | ------- | --------------------------------------------------------------------------------------------- |
| `REDIS_COUNTER_SYNC_INTERVAL_S`      | **5s** (`fly.worker.toml`)   | 5s      | Redis→Postgres sync interval for `jobs.running_tasks`                                         |
| `REDIS_COUNTER_RECONCILE_INTERVAL_S` | **120s** (`fly.worker.toml`) | 120s    | How often the worker rebuilds the Redis running-counters HASH from the authoritative XPENDING |

Drift between Redis truth and the snapshot Postgres value is reported via
`bee.broker.counter_sync_skew`. Drift between the Redis HASH counter and the
authoritative XPENDING at reconcile time is reported via
`bee.broker.counter_pel_skew`. Jobs that have non-zero PEL but are absent from
the worker's active-job set (frozen-dispatch smoking gun) are reported via
`bee.broker.pel_without_consumer`.

### Outbox sweeper

**Source:** `internal/broker/outbox.go`

Drains `task_outbox` rows (written in the same tx as `tasks`) into the
scheduler. Guarantees durable scheduling even if the fire-and-forget
`OnTasksEnqueued` callback loses the write.

| Env var / constant         | Production value | Default | What it controls                     |
| -------------------------- | ---------------- | ------- | ------------------------------------ |
| `OUTBOX_SWEEP_INTERVAL_MS` | unset            | 500ms   | Interval between outbox sweep passes |
| `OUTBOX_SWEEP_BATCH_SIZE`  | unset            | 100     | Max outbox rows drained per pass     |

### Probe

**Source:** `internal/broker/probe.go`

Periodic goroutine that scrapes Tier 1 broker gauges that have no natural
emission site: stream length, ZSET depth, XPENDING, outbox backlog + age, Redis
PING, pool stats.

| Constant      | Default | What it controls                                                  |
| ------------- | ------- | ----------------------------------------------------------------- |
| `Interval`    | 5s      | Probe tick frequency                                              |
| `TickTimeout` | 3s      | Per-tick deadline so a slow Redis or DB call can't stall the loop |

---

## Batch write channel

**Source:** `internal/db/batch.go`

Buffers task status updates to avoid a DB write on every individual task
completion.

| Env var / constant          | Production value      | Default | What it controls                                               |
| --------------------------- | --------------------- | ------- | -------------------------------------------------------------- |
| `GNH_BATCH_CHANNEL_SIZE`    | **5000** (`fly.toml`) | 2000    | Channel buffer depth (range 500–20,000)                        |
| `GNH_BATCH_MAX_INTERVAL_MS` | **2000ms** (unset)    | 2000ms  | Max time before forcing a flush (range 100–10,000ms)           |
| `MaxBatchSize`              | hardcoded             | 100     | Max tasks per batch write                                      |
| `MaxConsecutiveFailures`    | hardcoded             | 3       | Consecutive failures before falling back to individual updates |

When the main batch channel is full, updates are now coalesced into an in-memory
overflow buffer keyed by `task_id` instead of blocking worker goroutines. This
trades strict immediacy for continued throughput under DB pressure.

---

## Link discovery expansion

**Source:** `internal/jobs/link_discovery.go`, `internal/db/queue.go`

Discovered-link persistence runs asynchronously after parent-task success.
Expansion is bounded by a priority floor so low-value deep links do not flood
the system under load.

| Env var / constant                | Production value               | Default | What it controls                                                                                                                             |
| --------------------------------- | ------------------------------ | ------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `GNH_LINK_DISCOVERY_MIN_PRIORITY` | **0.5** prod / **0.7** preview | 0.5     | Minimum computed child-task priority required before creating new tasks. Preview runs tighter to keep crawls bounded on the smaller DB pool. |
| `minPriorityForTrafficScore`      | hardcoded                      | 0.729   | Minimum structural priority for traffic-score updates on link-found tasks                                                                    |

Homepage/header/footer/body links still enqueue when their computed child
priority is at or above the threshold. Deeper body-link expansion stops once
`parent_priority × 0.9` falls below the configured floor.

---

## HTML persistence

**Source:** `internal/jobs/html_persister.go`, `cmd/worker/main.go`

Direct-to-R2 async pool for uploading raw HTML after task completion. The
persister streams payloads straight to R2 (no Supabase Storage hop) and stamps
storage metadata onto the task row in a batched UPDATE. The R2 bucket and
provider come from the shared archive config (`ARCHIVE_BUCKET`,
`ARCHIVE_PROVIDER`).

| Env var                       | Default | What it controls                                                                            |
| ----------------------------- | ------- | ------------------------------------------------------------------------------------------- |
| `HTML_PERSIST_WORKERS`        | 8       | Goroutines uploading HTML concurrently                                                      |
| `HTML_PERSIST_QUEUE`          | 256     | Channel buffer for pending HTML uploads. Full queue drops the payload (best-effort capture) |
| `HTML_PERSIST_BATCH_SIZE`     | 16      | Successful uploads accumulated per worker before a metadata UPDATE flush                    |
| `HTML_PERSIST_FLUSH_INTERVAL` | `250ms` | Forced flush cadence so a quiet tail doesn't sit on staged metadata rows                    |
| `HTML_PERSIST_UPLOAD_TIMEOUT` | `30s`   | Per-PUT timeout against R2 — caps how long a stalled connection can occupy a worker         |

---

## Domain pacer

**Source:** `internal/broker/pacer.go`, `internal/broker/pacer_lua.go`

Adaptive per-domain token bucket backed by Redis. `TryAcquire` uses `SET NX PX`
plus `PTTL` to claim the domain's time-gate before the dispatcher moves a task
from ZSET to stream; on contention the caller backs off by the reported TTL.
`Release` runs a Redis Lua script (`pacer_lua.go`) to atomically update the
adaptive delay state — rate-limit signals widen the delay, sustained successes
narrow it. All state lives in Redis so it's shared across worker machines —
unlike the previous in-process `DomainLimiter`.

| Env var / constant             | Default | What it controls                                        |
| ------------------------------ | ------- | ------------------------------------------------------- |
| `PacerConfig.BaseDelayMs`      | 50ms    | Minimum delay between dispatches to a domain            |
| `PacerConfig.MaxDelayMs`       | 60000ms | Ceiling for adaptive delay                              |
| `PacerConfig.DelayStepMs`      | 500ms   | Delay increment per rate-limit hit                      |
| `PacerConfig.SuccessThreshold` | 5       | Consecutive successes before attempting delay reduction |
| `GNH_ROBOTS_DELAY_MULTIPLIER`  | 0.5     | Scale factor applied to `Crawl-Delay` from robots.txt   |

Pacer pushbacks are reported via `bee.broker.pacer_pushback_total` and the
current delay per domain via `bee.broker.pacer_delay_ms`.

---

## Cold-storage archival

**Source:** `internal/archive/archive.go`, `fly.toml`

| Env var / constant       | Production value     | Default | What it controls                                                                  |
| ------------------------ | -------------------- | ------- | --------------------------------------------------------------------------------- |
| `ARCHIVE_PROVIDER`       | **r2** (`fly.toml`)  | —       | Storage backend (`r2` or `s3`)                                                    |
| `ARCHIVE_BUCKET`         | (`fly.toml`)         | —       | Bucket name for archived HTML                                                     |
| `ARCHIVE_RETENTION_JOBS` | **3** (`fly.toml`)   | —       | Last N terminal jobs (completed/failed/cancelled/blocked) kept hot per domain/org |
| `ARCHIVE_INTERVAL`       | **1m** (`fly.toml`)  | —       | Sweep frequency                                                                   |
| `ARCHIVE_BATCH_SIZE`     | **100** (`fly.toml`) | —       | Archive candidates processed per sweep                                            |
| `ARCHIVE_CONCURRENCY`    | **5** (`fly.toml`)   | —       | Parallel R2 uploads per sweep                                                     |

---

## Scheduler and health loops

**Source:** `cmd/app/main.go`

Background loops that run independently of the worker pool.

| Constant                  | Value | What it controls                                               |
| ------------------------- | ----- | -------------------------------------------------------------- |
| `schedulerTickInterval`   | 30s   | How often the job scheduler checks for schedulers ready to run |
| `schedulerBatchSize`      | 50    | Max schedulers fetched per tick                                |
| `completionCheckInterval` | 30s   | How often completed jobs are detected and finalised            |
| `healthCheckInterval`     | 5 min | How often stale job health is checked                          |
