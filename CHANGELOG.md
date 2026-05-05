# Changelog

All notable changes to the Adapt project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Multiple version updates may occur on the same date, each with its own version
number. Each version represents a distinct set of changes, even if released on
the same day.

## Release Automation

When merging to main, CI automatically creates releases based on the changelog:

- `## [Unreleased]` or `## [Unreleased:patch]` → Patch release (0.6.4 → 0.6.5)
- `## [Unreleased:minor]` → Minor release (0.6.4 → 0.7.0)
- `## [Unreleased:major]` → Major release (0.6.4 → 1.0.0)

On merge, CI will:

1. Calculate the new version number
2. Replace the heading with `## [X.Y.Z] - YYYY-MM-DD`
3. Add a new `## [Unreleased]` section above
4. Create a git tag and GitHub release
5. Commit the updated changelog

## [Unreleased]

_Add unreleased changes here._

## Full changelog history

## [0.34.4] – 2026-05-05

### Changed

- Repository moved from `Harvey-AU` to `good-native` GitHub org. Go module path
  is now `github.com/good-native/hover` and npm package is `@good-native/hover`.
  CI runners switched from Blacksmith to GitHub-hosted `ubuntu-24.04` pending
  Blacksmith setup on the new org.

## [0.34.3] – 2026-05-02

### Added

- `scripts/logs.sh` unified Fly log tool with `monitor`, `search`, and `analyse`
  subcommands. Bare `logs.sh` runs `monitor` with all defaults (3s capture
  across all five Fly apps, 5-minute analyse snapshots, final report at run
  end). `search` greps captured raw logs by keyword/regex across one or more
  runs (case-insensitive by default, also reads `raw.zip` after cleanup).
  `analyse` runs a fixed probe set — severity, panics, HTTP status, latency,
  heartbeat, process health, autoscaler, database/external errors, Sentry, plus
  ad-hoc keywords — and writes `analysis.{md,json}` with `first_seen`,
  `last_seen`, and `peak` timestamps for every finding.
- `scripts/filter_since.py` per-app cursor filter wired into `capture_app` so
  each iteration only persists log lines newer than the previous capture,
  eliminating the 4× overlap inflation that came from `flyctl logs --no-tail`.

### Changed

- `scripts/monitor_logs.sh` reduced to a back-compat shim that forwards all
  flags to `logs.sh monitor`. Existing call sites in `opencode.json`,
  `.claude/commands/monitor.md`, `.claude/settings.local.json`, and
  `docs/development/DEVELOPMENT.md` continue to work unchanged.
- Default monitor app list now includes the two autoscaler sidecars
  (`hover-autoscaler-worker`, `hover-autoscaler-analysis`).
- Default `--run-id` is auto-generated as a `<adjective>-<colour>` slug (e.g.
  `mellow-rose`) so concurrent runs are easy to distinguish.

## [0.34.2] – 2026-05-02

### Added

- Sentry events now carry `app`, `process`, `region`, and `server_name` tags
  identifying the Fly app, binary, region, and machine that emitted them.
  Review-app errors previously had none of these, so bursts couldn't be
  attributed to a specific deploy. New helpers `logging.InitSentry` and
  `observability.StartMetricsServer` centralise the duplicated bootstrap across
  `cmd/app`, `cmd/worker`, and `cmd/analysis`.

### Fixed

- `cmd/analysis` now gracefully shuts down its metrics HTTP server on SIGTERM;
  previously it spawned the listener but never called `Shutdown`.

## [0.34.1] – 2026-05-01

### Added

- Queue-based autoscaling for `hover-worker` and `hover-analysis` via two
  `flyio/fly-autoscaler:0.3.1` instances. Each polls Fly's managed Prometheus
  every 60s and starts/stops a pre-provisioned machine pool (5 worker, 10
  analysis) based on Redis stream backlog. Replaces the static
  `flyctl scale count 1` lines on both apps. Pre-provisioning + start/stop mode
  (rather than create/destroy) preserves graceful shutdowns. Per-PR autoscaler
  variants deploy + tear down with the rest of the review-app lifecycle.
- New `bee_broker_unclamped_scale_target{stream_type=worker|lighthouse}` gauge
  surfacing the autoscaler's desired count before its `MAX` clamp, so a Grafana
  alert can detect chronic saturation ("we'd want N machines but are capped at
  the pool size").
- New `[metrics]` blocks on `fly.worker.toml`, `fly.analysis.toml`, and their
  review-app variants so Fly's managed Prometheus auto-scrapes the OTel exporter
  at `:9464`. Existing Alloy → Grafana Cloud push is unchanged.

### Changed

- `internal/broker/probe.go` now pipelines the lighthouse stream's `XLEN` and
  `XPENDING` alongside the worker stream, so `bee_broker_stream_length` and
  `bee_broker_consumer_pending` carry a `stream_type` attribute.
- The two `flyctl scale count 1` lines on `hover-worker` and `hover-analysis`
  release jobs are replaced with `scripts/fly-reconcile-pool.sh`, which reaps
  stale-image machines (preserving the original PR #369 purpose) and tops up the
  pre-provisioned pool to its target size.

## [0.34.0] – 2026-04-30

### Added

- Stripe billing integration — Checkout Sessions, Billing Portal, and webhook
  event handling at `POST /v1/webhooks/stripe` (`checkout.session.completed`,
  `customer.subscription.updated`, `customer.subscription.deleted`,
  `invoice.payment_failed`).
- New plan tiers: Starter ($19/200 pages), Plus ($49/1000), Pro ($149/10000),
  Ultra ($399/100000), Max ($849/500000); deactivated old business/enterprise
  tiers.
- `POST /v1/billing/checkout` — admin-only. Free → paid creates a Stripe
  Checkout Session; paid → different paid updates the existing subscription in
  place via Stripe's API (Stripe-managed proration, no duplicate subscriptions).
- `POST /v1/billing/portal` — admin-only. Opens a Stripe Customer Portal session
  for self-service subscription management. Honours an optional
  `STRIPE_PORTAL_CONFIG_ID` env var so the portal config is environment-aware
  (live vs sandbox `bpc_…`).
- `POST /v1/billing/cancel` — admin-only. Schedules cancellation at the end of
  the current billing period via Stripe's `cancel_at_period_end` flag; the
  customer keeps paid features through what they've already paid for, then
  auto-downgrades to free when the period ends.
- Settings → Plans: Upgrade / Switch / Manage Billing buttons; success toast
  with period-end date on cancellation; usage-cache invalidation on plan change
  and org switch.
- Stripe secrets managed via 1Password for both review apps (test keys) and
  production (live keys); `fly-setup` action validates the live keys are present
  before deploy.
- `dev.sh` now auto-injects external secrets (Stripe, Slack, Webflow, Google,
  Loops) from 1Password via `op inject` when `op` CLI is available.
- `.env.op` — committed `op://` template for local dev secrets.

### Changed

- Webhook handler tolerates Stripe API-version drift between the destination and
  the SDK (`webhook.ConstructEventWithOptions` with
  `IgnoreAPIVersionMismatch: true`), so a Stripe SDK upgrade doesn't break
  signature verification for events from older webhook destinations.
- Webhook handlers ACK (return 200) for events about unknown customers or
  unmapped Stripe price IDs rather than 5xx — Stripe stops retrying for
  permanent misconfigurations rather than spinning forever.
- Webhook handlers ignore events for subscriptions that don't match the
  organisation's stored `stripe_subscription_id` — protects against zombie
  subscriptions on the same Stripe customer flipping the wrong org's plan.

### Fixed

- Checkout Session creation uses an idempotency key (`checkout:<org>:<price>`),
  so a double-clicked Upgrade button or proxy retry can't create duplicate
  Stripe subscriptions.
- "Switch to Free" now actually cancels the Stripe subscription server-side
  (previously it only updated the local plan column, leaving the customer billed
  indefinitely).
- `BillingCheckout` defensively reconciles with Stripe before creating a new
  Checkout Session: if Stripe has an active sub for the customer that the local
  DB doesn't know about (e.g. webhook outage), it adopts the existing sub and
  takes the in-place update path.

_Add unreleased changes here._

## [0.33.15] – 2026-04-29

### Fixed

- Review-app and prod deploys no longer stack machines on repeated pushes. The
  Apr-27 build/release split changed the release shape to
  `flyctl deploy --image registry.fly.io/<app>:<label>`, which provisions a new
  machine instead of updating the existing one in place; worker and analysis
  have no `[http_service]` to auto-stop the stragglers, so each push left the
  previous machine running. Fix:
  - `.github/workflows/review-apps.yml` — added `--ha=false` plus a follow-up
    `flyctl scale count 1 --process-group …` to all three release jobs (API,
    analysis, worker), so each review-app converges on one machine per group.
  - `.github/workflows/fly-deploy.yml` — added a follow-up `flyctl scale count`
    to the prod release jobs (API → 1, analysis → 2, worker → 2) so prod keeps
    its HA pair on worker/analysis without stale-image leftovers piling up.
    Existing prod stragglers (`hover-worker`, `hover-analysis`) need a one-time
    `flyctl machine destroy` of the stale-image machines before the next deploy
    takes effect.

## [0.33.14] – 2026-04-29

### Fixed

- WAF pre-flight no longer strands jobs in `pending` if `BlockJob`'s DB write
  fails — a fallback transition writes `failed` with an explanatory message so
  the job always reaches a terminal state.
- Customer-facing `jobs.error_message` for the WAF fallback path is now a stable
  `"WAF detected but block transition failed"` string. The raw underlying error
  (which could include DB driver text like
  `pq: SSL is not enabled on the server`) is still logged via the structured ops
  logger with vendor/reason/domain context, but no longer leaks into the
  customer-visible field.
- `JobStatusBlocked` now triggers the same per-job in-process state cleanup
  (`processedPages`, milestones) as the other terminal statuses; long-running
  workers no longer leak map entries for blocked jobs.
- WAF probe scheme detection is now case-insensitive — `HTTPS://example.com` no
  longer double-prefixes to `https://HTTPS://...` and silently skips the
  verdict.
- `BlockJob` now CAS-guards the terminal `UPDATE jobs` against a stale pre-read
  status, so a freshly-completed/failed/cancelled job from a concurrent worker
  is no longer overwritten with `blocked` (and the domain row no longer stamped
  off a verdict that didn't actually land for that run). A lost race rolls the
  whole transaction back and surfaces as nil success.
- The WAF mid-job circuit breaker now dispatches `BlockJob` in a detached
  goroutine with a 30 s timeout, so the stream worker hot path can't stall on
  terminal-state DB lock contention. On `BlockJob` failure the breaker re-arms
  for the job, so a transient DB blip no longer permanently disables it.
- `EnqueueURLs` now short-circuits under its existing `FOR UPDATE OF j` row lock
  when the target job is in a terminal status (blocked, cancelled, failed,
  completed, archived). Without this, sitemap discovery and link extraction kept
  inserting orphan tasks for jobs that had already transitioned terminal
  mid-flight — kmart.com.au-class jobs were accreting 32k+ pending rows after
  the circuit breaker had already fired. The sitemap-discovery loop additionally
  reads job status between batches as a cheap pre-flight, so terminal jobs stop
  parsing remaining batches instead of round-tripping to the DB just to be
  rejected.

### Changed

- WAF detector now recognises Akamai Bot Manager `_abck` and `bm_sz` cookies on
  blocking status codes (403/202) as Akamai signals. Catches BM-fronted sites
  that don't emit `Server: AkamaiGHost` or `akaalb_*` cookies (e.g.
  kmart.com.au) and gives the mid-job circuit breaker `vendor=akamai`
  attribution instead of falling through to `generic`. Cookies on a 200 response
  are explicitly NOT treated as a block — many sites run BM in monitor mode
  without ever blocking.
- WAF mid-job circuit breaker default threshold lowered from 3 → 2 consecutive
  WAF responses. Trips ~33% earlier, capping orphan-task accumulation when a
  large sitemap is mid-discovery. Override via
  `GNH_WAF_CIRCUIT_BREAKER_THRESHOLD`.

## [0.33.13] – 2026-04-28

### Added

- WAF detection on first contact (#365 row 1). Pre-flight probe + mid-job
  circuit breaker recognise Cloudflare, Imperva, DataDome, and Akamai
  fingerprints; jobs against blocked domains terminate with a new `blocked`
  status instead of enqueueing thousands of doomed tasks. `domains.waf_blocked`
  caches the verdict for 24 h. Customer-facing pill: amber "Blocked by site".

## [0.33.12] – 2026-04-28

### Changed

- Crawler UA: `HoverBot` → `Hover`. Refs #365.
- Trimmed narrative comments across recent broker, stream worker, and Lighthouse
  files (13 files, −1,055 lines net). Comment-only; no code logic changed.
  Load-bearing WHYs preserved.

### Fixed

- Job throughput could collapse to ~0.3 tasks/s after a same-domain
  cancel-and-restart, while concurrent jobs on other domains kept running at 5–8
  tasks/s. Root cause was stale per-job broker state from the previous run
  leaking into the new dispatcher loop.
  - `RemoveJobKeys` now also clears the cancelled job's field from every
    per-host `hover:dom:flight:*` HASH. Without this every cancel left a
    leftover counter that drifted unbounded across restarts.
  - On worker boot, scan `hover:dom:flight:*` and drop fields whose job is not
    in the active Postgres set. Hard SIGKILL bypasses the graceful-shutdown
    drain, so the dispatcher's increment runs but the worker's `pacer.Release`
    decrement never does — `dom:flight` has no dedicated reconciler, so drift
    used to accumulate forever.
  - `RunningCounters.Reconcile` is now atomic: a single Lua EVAL replaces the
    previous `DEL + HSET` pipeline. Concurrent `Increment` / `Decrement` could
    land between the two commands and have its update silently lost when the
    rewrite landed, freezing dispatch for any job whose counter floated up to
    its concurrency cap until the next 120s reconcile.

- Job status pill stayed on "Starting up" forever because `UpdateJobStatus` had
  no callers in the production graph. The dispatcher now flips
  `pending → running` on the first successful publish for a job via a new
  `JobManager.MarkJobRunning` (guarded UPDATE, idempotent across worker
  restarts).

- Dispatcher now self-heals against unknown future drift classes in the per-job
  `hover:running` counter. When `CanDispatch` keeps refusing dispatch for a
  single job for longer than `REDIS_DISPATCH_STUCK_THRESHOLD_S` (default 30s)
  while the job's schedule ZSET still has due tasks, the dispatcher fires an
  immediate `RunningCounters.Reconcile` from the authoritative Redis PEL via the
  worker pool's new `TriggerReconcile` hook. Triggers are rate-limited to one
  per 2× threshold per job and collapse onto any in-flight reconcile via
  `TryLock`, so a genuinely-at-capacity job can't drive a reconcile burst. This
  is a safety net layered on top of the existing 120s `reconcileLoop`, not a
  replacement — closes the gap PR #362 left for future drift classes we haven't
  yet identified.

## [0.33.11] – 2026-04-26

### Changed

- Parallelised the Fly deploy pipeline. `fly-deploy.yml` now splits image build
  from app release: the shared `hover` image (API + worker, baked together by
  `Dockerfile`) is built once instead of twice, and the `hover-analysis` image
  builds in parallel with it. Releases fan out for API and analysis, with worker
  release gated on analysis being healthy to preserve the
  consumer-before-producer invariant on the per-job lighthouse stream. Expected
  wall-clock: ~9.5 min → ~4.5 min.
- Mirrored the same build-once + parallel structure in `review-apps.yml`, so PR
  review apps now exercise the production deploy logic. Provision job sets up
  the per-PR Fly apps, Upstash Redis, and Supabase preview branch and stages
  secrets onto the three apps; build/release jobs pin to the staged image via
  `flyctl deploy --image …`. Shared setup (Go, flyctl, 1Password) lives in a new
  `.github/actions/fly-setup` composite.
- Tightened the `Changelog Check` workflow to fail PRs that don't add a new
  entry under `## [Unreleased]`. Removed the `.github/**` exclusion so workflow
  changes also require a changelog entry.

### Fixed

- `.fly/review_apps.analysis.toml` now pins
  `dockerfile = "../Dockerfile.analysis"` under `[build]`, matching
  `fly.analysis.toml`. Without it, flyctl defaulted to the root `Dockerfile` and
  would have shipped the wrong binary in `hover-analysis-pr-N`. CodeRabbit
  caught this on PR #361.

## [0.33.10] – 2026-04-26

### Added

- Lighthouse Performance reports — Phase 1 foundations (PR #353). New
  `lighthouse_runs` table storing headline metrics plus an R2 report key (no
  JSONB blob); `task_type` column added to `tasks` and `task_outbox` so the
  dispatcher can route Lighthouse work onto `stream:{jobID}:lh`. DB helpers for
  insert / mark-running / complete / fail / skip-quota are gated on
  `status='running'` so a duplicate-delivered worker can't clobber a row that
  already reached a terminal state. A stub Runner behind a `Runner` interface
  lets the rest of the pipeline be exercised before Chromium lands. Sampler is a
  pure function with deterministic tie-break by PageID; its initial shape was
  2.5%-per-band / floor 1 / cap 50.
- Lighthouse Phase 2 producer + analysis app (PR #356). New `hover-analysis` Fly
  service (`cmd/analysis/main.go`, `Dockerfile.analysis`, `fly.analysis.toml`)
  consuming a dedicated `stream:{jobID}:lh` Redis stream. JobManager now fires
  `OnProgressMilestone` at every 10% boundary;
  `lighthouse.Scheduler.OnMilestone` loads completed tasks, runs the sampler,
  inserts `pending` `lighthouse_runs` rows, and bulk-enqueues `task_outbox`
  entries in a single transaction. Review-app and CI deploy workflows extended
  to build and deploy the analysis service alongside web + worker.
- Lighthouse Phase 3 real audits (PR #357).
  `internal/lighthouse/runner_local.go` runs Chromium plus the `lighthouse` npm
  CLI in-process; the JSON report is gzipped and uploaded to R2 under
  `jobs/{job_id}/tasks/{task_id}/lighthouse-mobile.json.gz`.
  `internal/lighthouse/report.go` extracts headline metrics (performance score,
  LCP, CLS, INP, TBT, FCP, Speed Index, TTFB, total byte weight) onto the
  `lighthouse_runs` row. Includes a memory-shed pre-check that defers an audit
  when the host is under pressure and a one-shot retry on transient Chromium
  failures. Production analysis app flipped from StubRunner to the local runner;
  `ARCHIVE_PROVIDER` / `ARCHIVE_BUCKET` set on the analysis service so reports
  land in R2 alongside crawl HTML.

### Changed

- Lighthouse sampler retuned from 2.5%-per-band / floor 1 / cap 50 to
  `floor(sqrt(completed) × 0.15)` per band, floored at 1, capped at 15. A
  10,000-page crawl now tops out at 30 audits/job (15 fastest + 15 slowest)
  instead of the previous 1,000-cap arithmetic. Added shed and retry metrics
  around the local runner so an operator can correlate skips with host memory
  pressure.
- `lighthouse 12` ships the mobile profile by default, so the explicit
  `--preset=mobile` flag was dropped from the local runner invocation.

### Fixed

- Lighthouse sampler now enforces the per-band cap **globally per job** instead
  of re-spending the quota at every 10% milestone. The Phase 1 sampler deduped
  by `page_id` only, so each milestone happily picked another `perBand` fastest
  - slowest from the not-yet-sampled pool — production observed 4 jobs producing
    110 `lighthouse_runs` rows when the cap should have held them to ~14.
    `SelectSamples` now takes `map[int]SelectionBand`, counts existing fastest /
    slowest rows, and only requests `max(0, target − existing)` per band per
    call. The 100% reconcile pass shares the same global budget rather than
    spending a separate one. New `db.GetLighthouseRunPageBands` replaces the
    page-id-only `GetLighthouseRunPageIDs`.

## [0.33.9] – 2026-04-26

### Added

- `ARCHIVE_PATH_PREFIX` env var on the worker. When set, `TaskHTMLObjectPath`
  prepends the prefix to every persisted HTML key — so review-app deployments
  can land in their own R2 sub-tree and be cleaned out as a single sweep. Empty
  prefix preserves the production path layout exactly.

### Changed

- Review-app workers now write HTML to a dedicated bucket
  `native-hover-archive-review` with an `<pr-number>/` prefix
  (`native-hover-archive-review/<pr>/jobs/<job>/tasks/<task>/...`). Cleanup
  becomes a single bucket purge (or R2 lifecycle rule) instead of per-PR prefix
  surgery on the production bucket. The bucket itself must be pre-created in R2
  — credentials in 1Password already carry write access.

### Removed

- Legacy hot-to-cold archive sweep (`internal/archive/scheduler.go`,
  `internal/archive/task_html.go`, `archive.Archiver`, `archive.ArchiveSource`,
  `archive.ArchiveCandidate`) and the corresponding DB methods
  `FindArchiveCandidates` / `MarkTaskArchived` / `MarkArchiveSkipped` /
  `MarkFullyArchivedJobs`. The sweep moved data Supabase Storage → R2; with R2
  now serving as the hot store, nothing was wired to call any of it. Nothing in
  the runtime referenced these symbols. `ARCHIVE_RETENTION_JOBS`,
  `ARCHIVE_INTERVAL`, `ARCHIVE_BATCH_SIZE`, `ARCHIVE_CONCURRENCY` env vars
  removed from `fly.toml`, `fly.worker.toml`, `.fly/review_apps.toml`,
  `.fly/review_apps.worker.toml` for the same reason. DB schema is unchanged —
  `tasks.html_archive_*` columns stay reserved for a future deep-cold tier.

### Fixed

- HTML persister now drains accepted uploads on graceful shutdown instead of
  abandoning queued payloads when `Stop()` is called. Stop rejects further
  Enqueue calls, closes the queue, lets workers drain (uploads keep their
  per-call timeout), then signals the probe loop and cancels the shared context.
  Previously a `Stop()` race could silently drop up to `QueueSize` task bodies
  on every restart.
- `cmd/worker` now starts the persister under a context decoupled from the main
  shutdown `cancel()`. The persister and its upload workers stay live while
  `swp.Stop()` drains the stream worker, so final HTML payloads enqueued during
  drain reach an active reader. Previously the shared context meant SIGTERM
  cancelled the persister's workers immediately, leaving any payload enqueued
  during drain stuck in a queue with no readers.
- HTML metadata flush failures no longer discard the in-memory batch. Transient
  DB errors retain the batch for retry on the next tick/size flush so
  just-uploaded R2 objects keep their metadata pointer; sustained failure caps
  the retained batch at 8× `BatchSize` and drops oldest, with a `flush_err`
  outcome on the existing persister upload counter.
- `cmd/worker` now fails loudly when only one of `ARCHIVE_PROVIDER` /
  `ARCHIVE_BUCKET` is set. A deploy typo previously fell through to the "feature
  disabled" branch and silently recreated the missing-capture failure mode this
  stage is meant to fix.
- Stream worker now logs a warning when an HTML payload is dropped because the
  persister queue is saturated, in addition to the persister's `skipped` outcome
  metric — so an operator can correlate a drop with the specific task without
  combing persister logs.
- Observability instrument-registration failures now write a `WARN:` line to
  stderr instead of being silently swallowed by `_ =`. Surfaces a missing metric
  group at boot rather than as a flat dashboard panel later. Applied to all six
  init calls (worker, crawler, jobs, db pool, broker, html persister) for
  consistency.

## [0.33.8] – 2026-04-25

### Added

- `internal/watchdog`: heartbeat-based wedge detector. The stream worker pool
  ticks the heartbeat per task outcome; a background loop forces `os.Exit(1)` if
  the heartbeat goes flat for 90s while jobs are alive, triggering Fly's restart
  policy. Last-resort recovery for Go-side wedges (blocked stdout, exhausted
  resource pool, mutex deadlock) that no in-process recovery can clear.
- `internal/logging.AsyncWriter`: bounded non-blocking `io.Writer` wrapping
  `os.Stdout` in production. slog handlers enqueue records into a
  drop-on-overflow channel rather than synchronously writing to the pipe.
  Prevents goroutines that hold DB transactions or other resources from wedging
  on platform log-shipper backpressure.
- pprof endpoints exposed on the worker's metrics port (`:9464`) alongside
  `/metrics`. Enables `fly proxy 9464 -a hover-worker` to capture goroutine
  dumps without redeploying.

### Changed

- `executeOnceWithContext` now calls `tx.Rollback()` immediately on error from
  either `applyLocalStatementTimeout` or `fn`, before any logging or other
  downstream work, with the deferred rollback suppressed via `committed=true`.
  Production observed Go goroutines wedging between query-error and the deferred
  rollback (e.g. inside a blocking slog write), leaving Postgres sessions in
  `idle in transaction (aborted)` for 20+ minutes. Eager rollback releases the
  connection regardless of what blocks afterwards.
- `DefaultDBSyncFunc` clamps Redis counter values to ≥0 via `GREATEST(0, $1)`
  before writing `running_tasks`. Eliminates the 6/min
  `jobs_running_tasks_non_negative` constraint violations (HOVER-K4) caused by a
  transient Redis underflow race between `HIncrBy(-1)` and the cleanup `HDel`.

### Removed

- The post-batch wide
  `UPDATE jobs SET running_tasks = 0 WHERE running_tasks > 0 AND id != ALL(...)`
  in `DefaultDBSyncFunc`. Walking job rows in index order acquired locks that
  deadlocked against the AFTER trigger `update_job_queue_counters` fired by
  concurrent task UPDATEs. The reconcile loop
  (`REDIS_COUNTER_RECONCILE_INTERVAL_S=120`) authoritatively rebuilds Redis
  state every two minutes, so missing this sweep only delays the
  `running_tasks=0` reflection in PG by at most one reconcile interval —
  acceptable in exchange for eliminating the deadlock class.

## [0.33.7] – 2026-04-25

### Fixed

- `promote_waiting_with_outbox` no longer inherits `tasks.run_at` into newly-
  inserted `task_outbox` rows. Waiting tasks always carry `run_at = created_at`,
  so promoting a task that had been waiting for hours/days baked an ancient
  timestamp into the outbox row. Production ran ~881k waiting tasks with
  `run_at` more than 30 minutes old (oldest > 3 days), causing the
  `bee.broker.outbox_age_seconds` gauge to climb past 5 hours despite actual
  outbox dwell times being sub-second. Outbox rows now use `NOW()` for `run_at`
  on initial insert; retry/back-off paths in `Sweeper.bumpAttempts` continue to
  set future `run_at` values.
- `bee.broker.outbox_age_seconds` gauge now measures actual outbox dwell time
  (`NOW() - MIN(created_at)` over due rows) instead of staleness of the
  inherited `run_at` value.

## [0.33.6] – 2026-04-25

### Fixed

- Worker counter sync no longer wraps per-job UPDATEs in an outer transaction.
  The previous design (15d146f3) held row locks on every job in the batch until
  commit, serialising against the `update_job_queue_counters` AFTER trigger
  fired by concurrent `promote_waiting_with_outbox` calls. Live
  `pg_stat_activity` showed a single counter-sync UPDATE blocking promoters for
  unrelated jobs, dragging tx duration past 2s and saturating the bulk DB pool.
  Each statement now runs in its own implicit transaction so row-lock hold time
  drops to milliseconds.

### Changed

- Lift `JOBS_LINK_DISCOVERY_MAX_INFLIGHT` default from 32 to 128 and pin it on
  `fly.worker.toml`. The 32-cap was the throughput ceiling driving the ~3–3.5k
  tasks/min plateau in production; with the counter-sync fix the bulk pool
  clears fast enough for the higher cap to stay well below the
  goroutine-explosion event that motivated the original limit.

## [0.33.5] – 2026-04-25

### Added

- Grafana deploy annotations posted on every main merge; dashboards synced from
  repo; panels and traces scoped by app

### Fixed

- Post-redis-broker-merge throughput regression resolved — pacer, FIFO ordering,
  and domain-delay defaults restored
- Broker counter drift stabilised with configurable env vars; reclaim
  classification hardened; metric label cardinality reduced
- Worker metrics now scraped by Alloy sidecar (added to Docker build); four
  broken Grafana panels restored after metrics rename; semaphore wait corrected
  to µs
- `hover` CLI on Windows: replaced `/bin/sh` stub with a Node launcher so
  `npm install -g @good-native/hover` produces a working `hover.cmd`/`hover.ps1`
  shim; install script now extracts the Go binary as `hover-bin[.exe]` to avoid
  a filename collision with the launcher

### Changed

- Go bumped to 1.26.2 for security fixes

## [0.33.4] – 2026-04-25

### Fixed

- `promote_waiting_with_outbox` now sorts the picked CTE with `id ASC` as a
  tie-breaker and orders the outbox `INSERT … SELECT` by `id`, making concurrent
  promoters lock task rows in a deterministic sequence. The AFTER trigger
  `trg_update_job_queue_counters` then updates the parent `jobs` row on top of
  an acyclic lock graph, so the 40P01 deadlock that fired ~3,000 times during
  the 2026-04-24 overnight load test (Sentry HOVER-K2) cannot form. Migration
  `20260425000001_promote_waiting_deterministic_lock_order`.
- `broker.DefaultDBSyncFunc` now sorts `jobIDs` before issuing per-job
  `UPDATE jobs SET running_tasks = …` and wraps the zero-stale UPDATE in a
  `WITH targets AS (… ORDER BY id FOR UPDATE) UPDATE jobs … FROM targets` CTE.
  Previously the sync loop iterated a Go map (random order), so two worker VMs
  ticking concurrently locked `jobs` rows in different sequences and deadlocked
  (Sentry HOVER-K4, ~2,000 events/12h, escalating).
- `StreamWorkerPool.handleDiscoveredLinks` now acquires from a bounded semaphore
  (`JOBS_LINK_DISCOVERY_MAX_INFLIGHT`, default 32) before each link is enqueued.
  Previously the fan-out was unbounded; one Sentry event captured 2,153 live
  goroutines in the worker (HOVER-KG, ~2,500 events/12h).
- Outbox sweeper `StatementTimeout` raised from 5 s to 15 s and made
  env-overridable via `OUTBOX_SWEEP_STATEMENT_TIMEOUT_MS`, so the
  `bump attempts` UPDATE no longer hits the context deadline when the shedding
  pool is under pressure (Sentry HOVER-K3).

### Changed

- Per-job `job.id` label removed from the broker stream-depth gauges
  (`bee.broker.stream_length`, `bee.broker.scheduled_zset_depth`,
  `bee.broker.consumer_pending`), the running-tasks / concurrency-limit gauges
  (`bee.jobs.running_tasks`, `bee.jobs.concurrency_limit`), and the counter-sync
  skew histogram (`bee.broker.counter_sync_skew`). The probe loop now sums
  per-job depths across all active jobs and emits one aggregate per tick, so
  dashboard `sum(...)` queries continue to render the correct totals while Mimir
  series cardinality drops from `6N + 1` to `7` per worker (≈85× reduction at
  100 jobs × 30 workers). Per-job drill-down on these metrics is intentionally
  gone — use trace spans (which still carry `task.domain` / `task.id` /
  `job.id`) for that.
- Crawler `http.Transport` configuration centralised in `newBaseHTTPTransport`.
  Both `CreateHTTPClient` and the colly base transport now derive from the
  helper, and the probe-only HEAD client derives from it too with overrides for
  its smaller pool. Among other things this makes `ForceAttemptHTTP2: false`
  apply uniformly across the three clients.
- `ForceAttemptHTTP2: false` on crawler transports silences ~1,200
  `received DATA after END_STREAM` log lines per heavy-load window; ALPN still
  negotiates HTTP/1.1 and per-request throughput is unchanged.
- HTML body-cap skip log demoted from Warn to Debug — a single large domain was
  producing thousands of warns per job.
- `GNH_PRESSURE_INITIAL_LIMIT` removed from `fly.toml` and `fly.worker.toml`.
  `pressure.go` already defaults the initial limit to `DB_QUEUE_MAX_CONCURRENCY`
  (the safe maximum), so setting it explicitly only invited drift and the
  "exceeds queue cap" clamp warning when the two values got out of sync.
- Production API `LOG_LEVEL` lowered from `debug` to `info` in `fly.toml`, now
  matching the worker. Debug verbosity was retained while we were chasing the
  load-test issues and is no longer needed; review apps stay on `debug`.

## [0.33.3] – 2026-04-25

### Changed

- CLI (`hover jobs generate`) test domain list updated — removed 12 domains with
  confirmed high failure rates (dead DNS, TLS mismatches, blanket 429/403 or
  bot-blocking): `lovethelabel.com.au`, `eucalyptus.com.au`, `frank-body.com`,
  `cottonon.com`, `lululemon.com`, `carawayhome.com`, `siteinspire.com`,
  `webdesignerdepot.com`, `officeworks.com.au`, `commandbar.com`, `remix.run`,
  `noice.com.au`. Failure root causes confirmed via Supabase job task data.
- CLI test domain list expanded from ~203 to ~403 domains — added 200 sites
  across 26 new categories (cloud infra, CI/CD, auth, databases, search,
  payments, CMS, feature flags, Go/Rust/Python ecosystems, more Australian
  businesses, AI/ML tools, open standards, and more).

## [0.33.2] – 2026-04-25

### Added

- Grafana deploy annotations posted on every main merge; dashboards synced from
  repo; panels and traces scoped by app
- HTML persister pool (`internal/jobs/html_persister.go`) streaming completed
  task HTML directly to Cloudflare R2 with a metadata-only batch UPDATE; tunable
  via `HTML_PERSIST_WORKERS`, `HTML_PERSIST_QUEUE`, `HTML_PERSIST_BATCH_SIZE`,
  `HTML_PERSIST_FLUSH_INTERVAL`, and `HTML_PERSIST_UPLOAD_TIMEOUT`.
- New `bee.html_persist.*` metrics: `upload_total{outcome}`, `upload_ms`,
  `queue_depth`, and `body_bytes`, so persister throughput, latency,
  backpressure, and payload size are dashboarded.

### Fixed

- Post-redis-broker-merge throughput regression resolved — pacer, FIFO ordering,
  and domain-delay defaults restored
- Broker counter drift stabilised with configurable env vars; reclaim
  classification hardened; metric label cardinality reduced
- Worker metrics now scraped by Alloy sidecar (added to Docker build); four
  broken Grafana panels restored after metrics rename; semaphore wait corrected
  to µs
- Restored task HTML capture, which had silently dropped on the post-Redis
  worker rewrite (no rows persisted since 2026-04-14). New writes go straight to
  R2 with `html_archived_at` stamped at write time, so the archive sweep no
  longer surfaces them as no-op candidates.

### Changed

- Go bumped to 1.26.2 for security fixes
- Task HTML now persists direct-to-R2 from the worker (issue #332), skipping the
  Supabase Storage hop entirely. The R2 paths land in `html_storage_*`; the
  `html_archive_*` columns are reserved for a future deep-cold-storage tier.

### Removed

- `internal/storage` package (Supabase Storage client) and its tests — the
  direct-to-R2 persister supersedes it and there were no remaining callers.

## [0.33.1] – 2026-04-24

### Added

- `bee.broker.outbox_sweep_total` counter with
  `outcome={dispatched, retried, dead_lettered}` labels so partial-failure and
  dead-letter rates are visible without a database session.
- `task_outbox_dead` table capturing rows whose `attempts` exceeded the retry
  cap (default 10), with `dead_lettered_at` and `last_error` for triage.
- Outbox investigation notes at `docs/diagnostics/outbox-aging-investigation.md`
  covering the oldest-age growth pattern, ranked hypotheses, and diagnostic
  queries.

### Changed

- `Scheduler.ScheduleBatch` now returns a typed `*BatchError` on partial
  pipeline failure, exposing `FailedIndices` so the outbox sweeper can `DELETE`
  the succeeded rows and only bump the failed ones. Previously every row in a
  500-row batch had attempts bumped whenever any single ZADD failed.
- Outbox sweeper bounds each tick's DB work with `SET LOCAL statement_timeout`
  (default 5 s) to keep a wedged backend from holding locks indefinitely.
- `JobManager.CancelJob` now deletes `task_outbox` rows for the cancelled job in
  the same transaction, preventing stale rows from contributing to the backlog
  and oldest-age gauges.

### Fixed

- Running-counter DB sync now uses `tx.ExecContext` instead of
  `tx.PrepareContext`, so the sync loop no longer fails with
  `prepared statement "stmt_…" already exists` (SQLSTATE 42P05) when the worker
  shares Supabase's pgbouncer with the API. The explicit prepare was creating
  deterministically named server-side statements that collided across pooled
  backends; the pool-level `default_query_exec_mode=simple_protocol` setting
  already in place for pooler URLs now takes effect.
- Worker Fly processes now launch via `scripts/start.sh` instead of running the
  binary directly, so the Alloy metrics sidecar runs on every process. Without
  this, `bee.worker.*` and `bee.broker.*` metrics from the prod `hover-worker`
  app and every `hover-worker-pr-*` review app were silently dropped before
  reaching Grafana Cloud.
- `scripts/start.sh` now accepts the binary name as `$1` (default `main`), so a
  single script launches either the API or the worker alongside Alloy. Both
  `fly.worker.toml` and `.fly/review_apps.worker.toml` point at
  `./start.sh worker`.

## [0.33.0] – 2026-04-21

### Added

- Redis broker system for task dispatch — API server schedules tasks into Redis
  ZSETs, dedicated worker service (`cmd/worker`) consumes via Redis Streams
- `internal/broker/` package: Scheduler, DomainPacer, RunningCounters, Consumer,
  Dispatcher with per-domain rate limiting and adaptive backoff
- `StreamWorkerPool` and `TaskExecutor` in `internal/jobs/` for worker-side task
  processing with batch result persistence
- Dedicated worker binary (`cmd/worker/main.go`) with its own `fly.worker.toml`
  deployment config
- Task outcome telemetry (outcome label, reason, duration) via
  `observability.RecordWorkerTaskOutcome`
- Redis is optional for the API server — graceful degradation when `REDIS_URL`
  is not set (tasks stay in Postgres, dispatch disabled)
- Broker integration tests (`internal/broker/dispatcher_test.go`) covering happy
  path, concurrency gating, domain pacing, malformed entries, and XAUTOCLAIM
  reclaim via miniredis
- `docs/operations/REDIS_BROKER_RUNBOOK.md` — day-two ops guide (key map, common
  operations, failure modes)
- `docs/plans/REDIS_BROKER_CUTOVER.md` — staged rollout plan, observability gap
  list, rollback paths, known architectural deferrals
- `STREAM_ACTIVE_JOBS_LIMIT` env var — was hard-coded to 200, now configurable
- Tier 1 + Tier 2 broker OTEL metrics: `bee.broker.stream_length`,
  `scheduled_zset_depth`, `consumer_pending`, `outbox_backlog`,
  `outbox_age_seconds`, `redis_ping_duration_ms`, `dispatch_total`,
  `autoclaim_total`, `consumer_message_age_ms`, `pacer_pushback_total`,
  `pacer_delay_ms`, `counter_sync_skew`, and `redis_pool.{in_use,idle,wait}`
- `internal/broker/probe.go` — periodic probe goroutine scraping stream length,
  ZSET depth, XPENDING, outbox backlog/age, Redis PING, and pool stats every 5s
  (bounded per-tick timeout, skips emission on Redis outage to avoid false
  zeroes)
- Counter skew telemetry — `DefaultDBSyncFunc` now snapshots prior
  `jobs.running_tasks` and emits `bee.broker.counter_sync_skew` per job so
  Redis↔Postgres counter drift is observable
- `OTEL_EXPORTER_OTLP_ENDPOINT` in `fly.worker.toml` — without it the worker
  registered broker instruments but never exported them to Grafana Cloud
- `task_outbox` table + sweeper (`internal/broker/outbox.go`, migration
  `20260419080120_add_task_outbox.sql`) — tasks are written to the outbox in the
  same transaction as `tasks`, and the sweeper drains them into Redis. Closes
  the durability gap where a fire-and-forget `OnTasksEnqueued` callback could
  lose writes during a Redis blip or process crash
- `tasks.run_at` column + migration `20260419120000_add_tasks_run_at.sql` —
  `Scheduler.Reschedule` now mirrors pacer push-backs and retry backoff to
  Postgres so the deadline survives a Redis flush
- Per-preview-PR worker deployment — `.fly/review_apps.worker.toml` and updates
  to `.github/workflows/review-apps.yml` spin up a dedicated worker machine
  alongside each review app so Redis-backed flows are actually exercised in
  preview

### Changed

- API server (`cmd/app`) no longer runs a local worker pool; task execution is
  handled exclusively by the worker service
- `NewJobManager` simplified from 4 parameters to 3 (worker pool removed)
- Manual root task INSERT now includes `priority_score = 1.0` so link discovery
  produces non-zero child priorities
- Reduced `DB_MAX_OPEN_CONNS` from 125 to 60 in `fly.toml` (API no longer runs
  workers)
- Broker components (`Scheduler`, `OutboxSweeper`, `Probe`) now route to
  `queueDB` when `DATABASE_QUEUE_URL` is set so they read/write the database
  that actually holds `task_outbox` rows; falls back to `pgDB` in single-DB
  deployments
- `Consumer.ReadNonBlocking` now uses `Block: -1` so go-redis omits the BLOCK
  clause entirely — `Block: 0` was being interpreted as "block indefinitely"
- Link priority, pressure floor, and restart policy tuned in `fly.toml`,
  `fly.worker.toml`, and review-app tomls; `internal/jobs/shared.go`
  min-priority floor raised to stop link discovery pushing near-zero-priority
  URLs

### Removed

- `internal/jobs/worker.go` (~5,000 LOC) — old DB-polling worker pool
- `internal/jobs/domain_limiter.go` — replaced by `internal/broker/pacer.go`
- `cmd/test_jobs/main.go` — legacy test binary
- Dead DB queue functions: `GetNextTask`, `IncrementRunningTasksBy`,
  `DecrementRunningTasksBy`, `promoteWaitingTasksBatch`
- ~15 old worker pool environment variables from `fly.toml`

### Fixed

- `IsRateLimitError` now caches the lowercased error string instead of calling
  `strings.ToLower` five times per invocation
- `buildTask` error path in `StreamWorkerPool` no longer ACKs on transient DB
  failures — messages stay in the PEL for XAUTOCLAIM redelivery
- `.env.example` Redis TLS config inconsistency (plain `redis://` URL paired
  with `REDIS_TLS_ENABLED=true`)
- Retry scheduling + ACK race in `StreamWorkerPool.handleOutcome` — replaced the
  two-step `Schedule` then `Ack` with a single atomic `ScheduleAndAck` (Redis
  MULTI/EXEC) so a failed ACK after a successful schedule can no longer leave
  the original message in the PEL for XAUTOCLAIM to redeliver
- `fetchJobInfo` in `StreamWorkerPool` now populates `RobotsRules` via a new
  `JobManager.GetRobotsRules` method. Previously `ProcessDiscoveredLinks`
  received nil rules and silently allowed URLs that robots.txt disallows
- Review-app CI: `flyctl redis status` output parsing was passing the whole line
  (including " Private URL │ ..." label and whitespace) into `REDIS_URL`,
  crash-looping the review app on every deploy. Extractor now grabs only the
  `redis(s)?://` substring
- Rate-limited task errors now route to a blocking retry in
  `internal/jobs/executor.go` (previously they fell through to the generic retry
  path, ignoring the rate-limit backoff signal)
- Dead-letter pacer push-back — `StreamWorkerPool` now releases the pacer slot
  when a message goes to the dead-letter queue so a poisoned message can't hold
  a domain's concurrency permanently
- Stale counter sync — `DefaultDBSyncFunc` no longer skips jobs whose Redis
  counter equals the previous snapshot, so Postgres is updated even when a job
  transiently has zero running tasks

## [0.32.7] – 2026-04-18

### Added

- `internal/logging` package: component-scoped slog + Sentry fanout logger with
  14 named components (worker, api, crawler, db, queue, batch, archive, notify,
  auth, startup, techdetect, util, jobs, pressure), `BeforeSend` UUID/number
  normalisation for Sentry issue grouping, and static fingerprinting
- Cross-platform pre-commit golangci-lint hook (`scripts/lint-go.js`) — skips
  gracefully when golangci-lint is absent, fails closed on git errors

### Changed

- Migrated all logging from zerolog to stdlib `log/slog` with `sentry-go/slog`
  v0.45.0; all packages now use `logging.Component("name")` and zerolog is
  removed entirely. Remaining `sentry-go` imports are limited to SDK
  initialisation in `cmd/app/main.go` and `cmd/worker/main.go` plus explicit
  `WithScope` / `StartSpan` usage for audit trails and tracing
- Removed `github.com/rs/zerolog` dependency
- Sentry error capture is now automatic for `Error`/`ErrorContext` calls via the
  fanout handler; use `logging.NoCapture(ctx)` inline to suppress capture for
  expected errors (e.g. 404s, client validation)
- Log monitoring pipeline restructured for agent debugging: `process_logs.py`
  emits a flat per-iteration CSV
  (`timestamp, level, component, message, extras`) alongside the JSON summary;
  `aggregate_logs.py` produces `time_series.csv` (per-minute level counts) and
  `events_per_minute.csv` (per-minute `component: message` counts) using a
  lossless `.aggregate_data.json` state file

## [0.32.6] – 2026-04-13

### Added

- Per-phase timing added to request diagnostics (primary, cache-validation,
  secondary, total)
- New crawler phase and worker task outcome metrics capturing phase, outcome,
  reason and duration
- Crawler emits a final summary of phase timings, probe counts and cache status

### Fixed

- Cache-validation cancellation now propagates correctly; redundant validation
  skipped for secondary requests
- Secondary request failures are surfaced and timings recorded; division-by-zero
  guarded in improvement ratio

## [0.32.5] – 2026-04-12

### Fixed

- Add a production safety switch to freeze runtime worker scale-down so worker
  slots are not recycled mid-run while we stabilise the scaling lifecycle panic
- Isolate worker slot lifecycle state on reuse so a scaled-down worker cannot
  share semaphore or shutdown state with a later worker started on the same
  logical slot

## [0.32.4] – 2026-04-12

### Fixed

- Improve scheduler fairness under high job counts by admitting pending jobs in
  a stable least-recently-touched order instead of an arbitrary subset, reducing
  the chance that newer or repeatedly-seen jobs crowd out older idle jobs
- Lower the task monitor sweep interval from 20 seconds to 5 seconds so newly
  runnable jobs join the worker pool faster during large queue spikes
- Skip claim attempts for jobs whose domain rate-limit window is still closed,
  reducing claim-then-requeue churn when delayed domains are mixed with healthy
  ones

### Added

- Add `jobs.updated_at` with an automatic trigger and active-jobs partial index
  so scheduler fairness can use a cheap row-level activity signal instead of
  consulting the much larger `tasks` table on every admission sweep

## [0.32.3] – 2026-04-11

### Fixed

- Protect control-plane queue traffic from bulk sitemap/link-write pressure by
  splitting DB execution into protected control and pressure-shed bulk lanes;
  retune pressure defaults to start at the full 88-slot cap, shed more gently to
  a 30-slot floor, and expose explicit initial/floor/step env vars
- Add a generic waiting-task recovery monitor so jobs with free capacity and
  queued `waiting` tasks can recover after transient promotion failures without
  being mistaken for “no work”; pending jobs now remain quota-capped until they
  actually have promotable slots
- Narrow sitemap concurrency gating to batch insertion only, preventing large
  sitemap discovery/parsing jobs from monopolising the global sitemap lane
- Make task completion resilient under load by coalescing batch-update overflow
  instead of blocking workers, and by moving discovered-link persistence onto an
  async queue
- Add `GNH_LINK_DISCOVERY_MIN_PRIORITY` so low-priority deep links below `0.7`
  no longer create new tasks, reducing expansion churn on large crawls
- Stop claim-time domain wait gating from starving healthy jobs, use actual
  task-state counts when recovering/removing queued jobs, and lower the base
  per-domain pacing floor to 50ms so healthy domains can refill worker capacity
  more aggressively
- Sync review app crawler tuning with production logic dials, enable OTEL on
  preview branches for throughput debugging, and make `GNH_MAX_WORKERS` override
  the staging fallback when explicitly set
- Scale preview pressure shedding to the smaller review-app queue budget, clear
  stale in-memory worker jobs after DB resets so fresh jobs can be claimed
  again, and sync `OTEL_EXPORTER_OTLP_HEADERS` into review-app CI so preview
  telemetry authenticates correctly

## [0.32.2] – 2026-04-11

### Fixed

- OTEL metric export interval is now configurable via
  `GNH_OTEL_EXPORT_INTERVAL_SECONDS` (default 60s); under high load the previous
  30s interval caused export bursts to exceed Grafana Mimir's ingestion rate
  limit, dropping metric samples and causing Grafana charts to go dark
- Orphaned task cleanup now pauses 100ms between batch iterations, preventing
  consecutive large UPDATE bursts from spiking the DB pressure EMA when a job
  fails with many tasks
- Stuck job health monitor threshold raised from 5 to 15 minutes, matching the
  maximum natural retry cycle duration and eliminating premature alerts for jobs
  on slow sites

## [0.32.1] – 2026-04-11

### Fixed

- Global sitemap insertion semaphore (`GNH_SITEMAP_CONCURRENCY`, default 3)
  limits how many jobs may insert sitemap batches concurrently; previously N
  simultaneous job creations launched N independent goroutines each writing
  100-URL UNNEST batches every 200ms, causing a combined write burst that pushed
  DB EMA well above the 60ms high-water mark and shed concurrency to the 10-slot
  floor
- Archive sweeps now permanently skip tasks where both hot storage and cold
  storage return a 404; previously the task was left with
  `html_archived_at = NULL` and re-queued on every sweep, consuming cold storage
  quota and archive-worker capacity with no chance of success

## [0.32.0] – 2026-04-11

### Added

- Adaptive DB pressure controller: EMA-based semaphore throttle that sheds
  concurrency slots when query execution time crosses a configurable high-water
  mark and restores them gradually when load eases; tunable via
  `GNH_PRESSURE_HIGH_MARK_MS` (default 60ms) and `GNH_PRESSURE_LOW_MARK_MS`
  (default 20ms)
- Four new OTEL metrics pushed to Grafana via OTLP: `bee.db.pressure.ema_ms`,
  `bee.db.pressure.limit`, `bee.db.pressure.adjustments_total`,
  `bee.db.semaphore.wait_ms`
- OTLP metrics export alongside existing Prometheus endpoint; metrics endpoint
  derived automatically from the traces endpoint (`/v1/traces` → `/v1/metrics`)

### Changed

- Sitemap task insertion now batches 100 URLs at a time (down from 1,000) with a
  configurable inter-batch delay (`GNH_SITEMAP_BATCH_DELAY_MS`, default 200ms)
  to prevent write bursts from spiking DB pressure on large sitemaps
- Jobs with sitemaps now transition to `running` after the first batch of tasks
  is inserted rather than waiting for the full sitemap crawl to complete;
  homepage (`/`) is hoisted to the front of the URL list to ensure it is in the
  first batch and scored with priority 1.0
- Pressure controller initial concurrency limit lowered from 55 → 30 to start
  conservatively after a restart

## [0.31.14] – 2026-04-09

### Performance

- Drop 6 redundant indexes on the `tasks` table (`idx_tasks_job_id_status`,
  `idx_tasks_job_host`, `idx_tasks_job_id`,
  `idx_tasks_job_status_priority_pending`, `idx_tasks_pending_by_job_priority`,
  `idx_tasks_pending_claim_order`); removing `idx_tasks_job_id_status`
  re-enables PostgreSQL HOT for status-only updates, eliminating full index
  maintenance across all 17 indexes on every task transition; retains
  `idx_tasks_running_started_at` which is used by stuck-task detection queries

### Internal

- Upgrade OpenTelemetry SDK to v1.43.0
- Pin Prettier to package.json version in auto-release workflow via `npm ci`

## [0.31.13] – 2026-04-09

### Performance

- Replace 3× correlated `COUNT(*) WHERE status = '…'` subqueries in
  `update_job_counters` UPDATE path with O(1) incremental deltas computed from
  `OLD`/`NEW.status`; add an early-exit guard that skips the `UPDATE jobs`
  entirely for non-terminal, non-starting transitions (e.g. pending↔running),
  which previously triggered all three full-table scans for no counter change
- Drop `trigger_update_job_progress` (the legacy full-scan trigger that was
  never removed when `update_job_counters` was added); both triggers fired on
  every task status change causing 2× jobs row UPDATEs and 3× COUNT(\*) scans
  per concurrent worker — the primary cause of sustained 100% Supabase CPU
- Extend `update_job_counters` to handle job status transition (`running` →
  `completed`) with the same O(1) incremental logic, preserving `cancelled` and
  `failed` terminal states
- Narrow `trg_update_job_queue_counters` from `AFTER UPDATE` (all columns) to
  `AFTER UPDATE OF status` — eliminates function-call overhead on metadata-only
  task updates (response_time, priority_score, etc.)
- Replace one-at-a-time waiting-task promotion loop in
  `promoteWaitingTasksWithQuota` with a single batch call to
  `promote_waiting_tasks_for_job`, reducing N DB round-trips to 1 per job and
  computing quota/concurrency limits upfront via a LATERAL subquery

## [0.31.12] – 2026-04-09

### Performance

- Add three partial/covering indexes on `tasks` to reduce Supabase CPU load
  under high concurrency: `idx_tasks_job_has_html_storage` and
  `idx_tasks_job_html_archived` eliminate heap scans in `MarkFullyArchivedJobs`
  EXISTS/NOT EXISTS subqueries; `idx_tasks_job_activity_times` covers the
  per-job MAX timestamp scan in `CleanupStuckJobs`
- Move traffic score UPDATE in `EnqueueURLs` to a separate transaction after the
  job-row lock is released, reducing lock hold time for concurrent workers on
  the same job
- Skip traffic score updates for link-discovered pages deeper than ~3 hops from
  the homepage (priority < 0.729) — these pages are too numerous for the
  `page_analytics` join to be worthwhile; sitemap sources always apply
- Merge two row-level INSERT triggers (`update_job_counters`,
  `update_job_queue_counters`) into a single statement-level trigger; a 500-row
  batch now emits one `UPDATE jobs` instead of 1,000, eliminating the dominant
  source of lock contention during `EnqueueURLs`
- Replace correlated `COUNT(*) WHERE status != 'skipped'` subquery in
  `EnqueueURLs` with `total_tasks - skipped_tasks` (now maintained incrementally
  by the statement-level trigger), removing a per-call table scan from inside
  the job-row lock
- Fix `promote_waiting_task_for_job` to promote N tasks per slot release instead
  of always 1; replace the stale `running_tasks` capacity join with a
  caller-supplied count; remove the redundant promotion loop from the batch
  flush transaction (ran under stale data and caused lock contention inside an
  already long-held lock)

## [0.31.11] – 2026-04-09

### Fixed

- Deduplicate `www.` and non-www URLs to the same page record, preventing
  duplicate tasks for the same path (#303)

## [0.31.10] – 2026-04-08

### Fixed

- Workers no longer block for up to 60 s on domain rate-limit delays;
  `TryAcquire` returns immediately when a domain's delay window is active,
  allowing the worker to pick up a task for a different domain instead
- Domain-delayed tasks are now correctly requeued as `waiting`: the DB
  `running_tasks` slot is released via `releaseRunningTaskSlot` (previously
  missing, causing `running_tasks` to stay at the concurrency cap and
  permanently blocking task promotion), the task status is persisted via
  `QueueTaskUpdate`, and the in-memory concurrency counter is decremented

## [0.31.9] – 2026-04-08

### Fixed

- Prevent deadlocks on concurrent batch page upserts by sorting rows by
  `(host, path)` in Go before building `UNNEST` arrays, ensuring all
  transactions acquire locks in a consistent order
- Sort enqueued pages by ID before insert to prevent lock-order deadlocks in
  `EnqueueURLs` transactions
- Demote permanent HTTP 4xx/429 task failures from warn/info to debug to reduce
  log noise at the default info level

### Changed

- Raise review app VM to 1 GB / 2 vCPU and align `DB_MAX_OPEN_CONNS` to 40 to
  match Supabase preview branch pool size
- Increase production `DB_QUEUE_MAX_CONCURRENCY` to 55 to better utilise
  available connections
- CI now automatically sets Supabase preview branch pool size to 40 on each
  review app deployment

## [0.31.8] – 2026-04-07

### Changed

- Cap dynamic worker max scale from 50 to 40 to stay within DB pool budget
- Raise `DB_MAX_OPEN_CONNS` from 45 to 50 (pgBouncer ceiling is 90)
- Lower `DB_QUEUE_MAX_CONCURRENCY` from 40 to 30, leaving 16 connections for
  non-queue direct calls
- Add `GNH_BATCH_CHANNEL_SIZE=5000` to absorb peak batch write bursts (was 2,000
  default)

## [0.31.7] – 2026-04-06

### Added

- Cold storage archival system — completed task HTML automatically moves from
  Supabase hot storage to R2 (or S3/B2) via a background sweep, keeping storage
  within quota without losing data
- `archived` job status — jobs are marked archived once all their task HTML has
  been moved to cold storage; the UI renders this as a success state, not an
  error
- Archive startup connectivity check — logs a clear error on boot if the cold
  storage bucket is unreachable before the first sweep runs
- `cmd/archive_key_migrate` one-shot tool to rekey existing archived objects
  from the old path format to the canonical
  `jobs/<id>/tasks/<id>/page-content.html.gz` layout

### Security

- Set TLS 1.2 minimum on AIA cert-inspection transport to prevent protocol
  downgrade during handshake
- Route all AIA HTTP requests through `ssrfSafeDialContext` — IP check at
  connect time (not just URL-parse time) to prevent DNS rebinding
- Add `CheckRedirect` to AIA and cert-inspection clients to re-validate redirect
  targets against scheme and private-host checks, preventing SSRF via redirect
- Bound `isPrivateHost` DNS lookup with a 2 s context; fail-closed on timeout
- AIA intermediate certs stored in a separate slice — never injected into
  `TLSClientConfig.RootCAs`; chain verified via `VerifyConnection` callback
- Guard AIA intermediate acceptance: reject self-signed certs and certs without
  `IsCA + BasicConstraintsValid`
- Sanitise AIA log calls to record `host` only, not the full raw URL
- Replace credential-like literals in DSN test fixtures with neutral
  placeholders; remove associated `nolint:gosec` annotations

### Fixed

- Archive default interval corrected to 1 minute (was 1 hour in code default,
  conflicting with fly.toml override)
- Sweep shutdown now propagates a sweep-scoped cancel to all in-flight archive
  goroutines — stop signal no longer leaves goroutines running detached
- Job card renders `archived` status with success styling instead of falling
  through to the error branch
- Archive CAS update now checks `RowsAffected` before deleting the old object —
  prevents orphan deletion if another process already migrated the key
- R2 upload: use unsigned payload and explicit `Content-Length` header; disable
  auto-checksum trailer to avoid SDK/R2 compatibility issues
- Fix doubled path segment in cold storage object key
- Fix archive bucket name mismatch between config and fly secrets
- Fix content type fallback so archived HTML is always stored as
  `text/html; charset=utf-8` + `gzip` encoding

## [0.31.6] – 2026-04-06

### Added

- `hover jobs generate --repeats N` flag to run each test domain N times,
  cycling through domains as their previous jobs reach a terminal status
- `hover jobs generate --limit N` flag to cap total jobs submitted regardless of
  domain count × repeats
- Per-domain stall backoff: after 3 consecutive status-refresh failures the job
  is placed in a timed backoff (5× `--status-interval`) before the domain is
  allowed to restart, preventing duplicate jobs while still recovering from
  transient API errors
- Per-domain create-retry cap: after 3 consecutive `createJob` failures the run
  slot is marked skipped so the load test loop can always terminate
- `"paused"` treated as a terminal job status so paused jobs do not block
  subsequent repeats

### Fixed

- `createJob` now returns a real error when the response cannot be parsed or
  contains no job ID, rather than recording a fake `"unknown"` ID
- HTTP error responses from `createJob` and `fetchJobStatus` no longer include
  the raw response body; errors now report the HTTP status code and optional
  `X-Request-Id` header value only
- Skipped run slots (create-retry cap reached) no longer increment
  `jobsCreated`; the summary accurately reflects only successfully submitted
  jobs

## [0.31.5] – 2026-04-06

### Security

- Fix P-256 JWKS coordinate encoding in tests — replaced `big.Int.Bytes()`
  (drops leading zeros) with `ecdh.PublicKey.Bytes()` to produce RFC 7518
  §6.2.1.2-compliant fixed-length coordinates
- Harden AIA fetching in `tls_aia.go` against DNS rebinding by routing all AIA
  HTTP requests through `ssrfSafeDialContext()` (IP check at connect time, not
  just URL-parse time)
- Add `CheckRedirect` to AIA and cert-inspection clients to re-validate redirect
  targets against scheme and `isPrivateHost` checks, preventing SSRF via
  redirect
- Replace unbounded `net.LookupHost` in `isPrivateHost` with a 2 s
  context-bounded `net.DefaultResolver.LookupIPAddr`; fail-closed on timeout
- Deduplicate private-IP predicate in `isPrivateHost` — now delegates to shared
  `isPrivateOrLocalIP` from `crawler.go`
- Set `MinVersion: tls.VersionTLS12` on AIA cert-inspection transport to prevent
  protocol downgrade during handshake
- Add `ssrfSafeDialContext` and `CheckRedirect` guard to cert-inspection client
  (`inspectClient`) so the HEAD request used to read the leaf cert is also
  protected against DNS rebinding and open redirects
- Replace credential-like literals (`user:pass`, `password=pass`) in DSN test
  fixtures with neutral placeholders; remove `nolint:gosec` annotations
- Refactor AIA intermediate handling: fetched certs are now stored in a separate
  `intermediates []*x509.Certificate` slice and never injected into
  `TLSClientConfig.RootCAs`; the retry uses a `VerifyConnection` callback that
  verifies the chain against system roots + fetched intermediates via
  `x509.VerifyOptions`
- Guard AIA intermediate acceptance with `IsCA + BasicConstraintsValid` and
  self-signed checks so root CAs served from AIA URLs are rejected
- Sanitise AIA log calls to log `host` only, not the full raw URL

## [0.31.4] – 2026-04-05

### Fixed

- Fix file descriptor exhaustion under load caused by per-request HTTP transport
  leak in `CheckCacheStatus()` — each probe call created a new `http.Transport`
  that held idle connections open indefinitely, exhausting 10240 fds during
  sustained crawling
- Add shared `probeClient` to `Crawler` struct, reused across all cache probe
  requests with proper idle connection limits
- Add global `MaxIdleConns` cap (150) to main crawler and `CreateHTTPClient`
  transports to prevent unbounded idle socket accumulation across hosts
- Reduce production DB pool from 70 → 45 open connections via env vars to match
  actual queue semaphore usage (40 concurrent ops + 5 reserved)
- Add file descriptor pressure detection in `ensurePoolCapacity` — rejects DB
  operations early with `ErrPoolSaturated` (triggering existing retry/backoff)
  when fd usage exceeds 90%, instead of failing with cryptic DNS errors
- Raise container fd soft limit to hard limit ceiling in Dockerfile as safety
  net

### Added

- File descriptor observability gauges (`bee.process.fd.current`,
  `bee.process.fd.limit`, `bee.process.fd.pressure`) for Grafana monitoring

## [0.31.3] – 2026-04-04

### Fixed

- npm install downloads from correct `cli-v*` release tag

## [0.31.2] – 2026-04-04

### Fixed

- CLI release now publishes under `cli-v*` tag to avoid collision with app
  releases
- CLI release triggers independently of changelog (on CLI file changes)
- Cancellable confirmation prompt via Ctrl+C
- Sanitised error output in org switching (no raw HTTP body)

## [0.31.1] – 2026-04-04

### Added

- CLI version injected at build time (replaces hardcoded `v0.1.0`)
- CLI update check on startup — notifies when a newer version is available
- Identity and org display before job generation (`Logged in as X in Y`)
- Org switching prompt in CLI (`C` to change active organisation)
- CLI version included in PR release preview comment

### Fixed

- npm `install.js` downloads from `cli-v*` tags instead of `v*`
- Auto-release CLI change detection now includes `npm/` directory
- Release preview comment no longer picks up `cli-v*` tags as app version
- Prevent tag collision in CLI release workflow when `v*` tag already exists
- Binary existence validated after npm postinstall extraction

## [0.31.1] – 2026-04-03

### Fixed

- CLI now targets production domain (`hover.app.goodnative.co`) by default
  instead of `hover.fly.dev`
- CLI release workflow is idempotent — safe to retrigger after partial failures
- Published `@good-native/hover` npm package with MIT licence

## [0.31.0] – 2026-04-03

### Added

- Native `hover` CLI binary — run `hover jobs generate --pr <N>` to create
  load-test jobs without scripts or manual API calls
- Browser-based CLI authentication via the app's existing auth flow — no
  separate credentials or login page needed
- Auto-discovery of Supabase auth config from preview app, so `--anon-key` is no
  longer required
- Session persistence and token refresh for CLI — authenticate once, reuse
  across sessions
- npm distribution (`npm install -g @good-native/hover`) with automatic binary
  download for macOS, Linux, and Windows
- Independent CLI versioning — npm releases only trigger when `cmd/hover/`
  changes, with their own version counter
- Windows support for CLI (amd64 and arm64)

### Removed

- Hosted `cli-login.html` auth page and Python auth helper scripts — replaced by
  native browser auth flow

### Fixed

- PKCE verifier slice bounds panic when code verifier was shorter than 128
  characters

## [0.30.3] – 2026-04-03

### Fixed

- Sitemap parser now uses Go's `encoding/xml` decoder instead of string
  matching, fixing sitemaps that use CDATA wrappers (e.g. WordPress All in One
  SEO) or XML entity-encoded URLs (`&amp;` in query strings)
- AIA (Authority Information Access) transport fetches missing intermediate
  certificates at runtime, fixing TLS failures on servers with incomplete
  certificate chains (e.g. acsi.org.au)
- Job lifecycle hardened: `initialising` state prevents premature completion
  before sitemap URLs are enqueued, with stale timeout and cancellation support

## [0.30.2] – 2026-03-28

### Added

- `/dev/auto-login` endpoint — signs in as `dev@example.com` server-side and
  injects a valid Supabase session into `localStorage`, then redirects to
  `/dashboard`; returns 404 outside `APP_ENV=development`
- `dev@example.com` seed user (email/password) added to `supabase/seed.sql` for
  use with the auto-login endpoint
- `dev.sh` now auto-generates `.env.local` from `supabase status` on first run
- `.claude/launch.json` configured to start Air (hot reloading) via Claude Code
  preview — no manual server start needed
- `/preview` Claude command added to standardise preview startup and auth
- `scripts/pr-review-summary.sh` added to collect CodeRabbit comments and CI
  check results into `.claude/pr-review.md` for agent use
- Claude agent files updated to include Serena MCP tools for Go code navigation

### Fixed

- Homepage auth modal: `await loadAuthModal()` before `showAuthModal()` to
  prevent a race condition on first open
- `dev.sh` key extraction corrected to use `PUBLISHABLE_KEY` from
  `supabase status` (not `ANON_KEY`)
- `.air.toml` Mac/Linux build command now active by default; Windows override
  documented

### Changed

- Commit message convention updated across docs to plain English (5–6 words, no
  conventional commit prefixes)
- `DEVELOPMENT.md` local auth section added with seed user table and login
  instructions for both real and sandboxed browsers
- `DEVELOPMENT.md` Claude Code preview section added
- `flight-recorder.md` corrected to use port 8847 and `./cmd/app` run path
- `CLAUDE.md` updated with Serena code navigation preference and preview server
  rule
- `.claude/settings.local.json` permission allowlist tightened — removed broad
  wildcards, scoped `gh`/`git`/`supabase`/`docker` commands

## [0.30.1] – 2026-03-26

### Fixed

- Trigger redeploy to Fly with correct `fly.toml` config

## [0.30.0] – 2026-03-26

Rebranded from Adapt to Hover across the entire codebase, configuration, and
infrastructure.

### Changed

- **Brand rename** — all user-facing text, HTML titles, nav, and page headings
  updated from "Adapt" to "Hover"
- **Crawler user-agent** — `AdaptBot/1.0` → `HoverBot/1.0`
- **OpenTelemetry service identity** — service name, tracer names, and meter
  names now use `hover/*`
- **Fly.io app name** — `fly.toml` and review app config target the `hover` app
- **GitHub URLs** — badges, workflow refs, and repository links point to
  `good-native/hover`
- **Go module** — `go.mod` module path updated
- **Auth redirects** — `APP_URL` and OAuth callback references use
  `hover.app.goodnative.co`
- **Scripts and CI** — cleanup scripts, review-app workflows, and shell helpers
  reference the renamed app
- **Documentation** — all docs, plans, research summaries, and README updated

## [0.29.0] – 2026-03-25

Migrated the Hover frontend from legacy global scripts to ES modules and
established a shared codebase between the dashboard and Webflow Designer
extension. Both surfaces now consume the same logic (`app/lib/`) and components
(`app/components/`), with a bridge pattern enabling the non-module extension
runtime to access shared code.

### Added

- **Shared module architecture** (`web/static/app/`): three-layer structure —
  `lib/` (pure logic, zero DOM assumptions), `components/` (Web Components),
  `pages/` (thin per-page orchestrators)
  - Lib modules: `api-client`, `auth-session`, `config`, `formatters`,
    `integration-http`, `domain-search`, `invite-flow`, `admin`, `global-nav`
  - Web Components: `hover-job-card`, `hover-data-table`, `hover-status-pill`,
    `hover-tabs`, `hover-toast`
  - Design tokens: `tokens.css`, `base.css`, `components.css`
- **Dashboard fully on ES modules**: `dashboard.html`, `settings.html`, and job
  details run without legacy script dependencies. Settings split into focused
  modules (`lib/settings/` — account, team, plans, schedules, integrations,
  organisations)
- **Extension bridge pattern**: `lib/bridge.js` exposes `window.HoverLib`
  (`api`, `fmt`, `http`) and `window.HoverJobCard` for the extension.
  `api-client.js` made configurable with `configure({ baseUrl, tokenProvider })`
  for cross-origin use. `scripts/sync-shared.js` copies shared modules into the
  extension at build time; import map remaps `/app/` paths to local copies
- **Developer tooling**: Husky + lint-staged pre-commit hook enforces Prettier
  formatting; favicon served on all HTML pages

### Changed

- CI workflows run on PRs targeting any branch, not just `main`
- `/app/**` static routes served with `Cache-Control: public, max-age=86400`
- Synced extension components gitignored (generated by `sync-shared.js`)

### Fixed

- Global nav notification badge race condition (guarded with `BB_APP.coreReady`)
- Extension job cards rendering as plain status text (missing bridge export)
- Job details init crash when `bb-bootstrap.js` not loaded
- Seed idempotency: `auth.identities` `ON CONFLICT` column corrected

## [0.28.1] – 2026-03-22

### Security

- **Function search_path pinning**: Set `search_path = public` on 29 database
  functions missing it, closing a theoretical schema-hijacking vector. Includes
  all `SECURITY DEFINER` functions handling tokens and auth.
- **RLS on `domain_hosts`**: Enabled row-level security with deny-all default.
  Table is backend-only (service role), so no policies needed.

### Changed

- **RLS initplan optimisation**: Wrapped bare `auth.uid()` and `auth.role()`
  calls in `(SELECT ...)` across 8 RLS policies on `jobs`, `slack_user_links`,
  `notifications`, `domains`, and `pages` — evaluated once per query instead of
  per row.
- **Consolidated duplicate permissive policies**: Merged overlapping SELECT
  policies on `jobs` (org-scoped + direct ownership into one) and
  `organisation_members` (dropped redundant self-membership policy already
  covered by co-members policy). Split `jobs` FOR ALL into per-operation
  policies to avoid lint overlap.
- **Index on `jobs.status`**: Added btree index per Supabase index advisor
  recommendation, improving quota-blocked jobs query performance.

## [0.28.0] – 2026-03-22

### Added

- **Task page HTML storage**: Crawled page HTML is now captured,
  gzip-compressed, and uploaded to Supabase Storage (`task-html` bucket) with
  full metadata tracked on the task row — content type, encoding, original and
  compressed sizes, SHA-256 digest, and capture timestamp.
- **Supabase preview branch keys in CI**: Review app deploys now extract
  `SUPABASE_SERVICE_ROLE_KEY`, `SUPABASE_JWT_SECRET`, and `SUPABASE_URL`
  directly from the Supabase preview branch, eliminating the need for manual Fly
  secret overrides.

### Changed

- **Storage bucket consolidation**: Removed the unused `page-crawls` bucket and
  its upload code. All page HTML storage now uses the `task-html` bucket with
  proper compression and metadata tracking.
- **Bucket upsert on migration**: The `task-html` bucket INSERT now uses
  `ON CONFLICT DO UPDATE SET` to enforce intended settings (privacy, size limit,
  allowed MIME types) on existing buckets.

### Fixed

- **Storage auth for preview branches**: Preview app deploys previously used the
  main project's service role key, causing `Invalid Compact JWS` and RLS errors
  when the preview branch had its own keys.
- **HTML persistence drain loop**: Removed busy-wait `default` case from the
  shutdown drain select, preventing CPU spinning when pending items are being
  processed by other workers.

## [0.27.3] – 2026-03-21

### Added

- **Per-request crawl diagnostics**: Tasks now persist a structured
  `request_diagnostics` JSONB payload covering primary requests, cache probe
  attempts, and secondary requests for later inspection without storing page
  bodies.

### Changed

- **Diagnostics hygiene**: Stored crawl diagnostics now redact sensitive
  headers, scrub query strings and fragments from recorded URLs, avoid
  duplicating full probe payloads, and preserve retry/waiting-state diagnostics
  safely through batch promotion paths.

### Fixed

- **Supabase preview seed compatibility**: `auth.identities` seed inserts now
  use the current conflict key `(provider_id, provider)`, restoring preview
  branch seeding on fresh initialisation.

## [0.27.2] – 2026-03-15

### Changed

- **Nav HTML moved to partial**: `bb-global-nav.js` now fetches
  `/web/partials/global-nav.html` at runtime instead of embedding the nav markup
  as an inline string — `global-nav.html` is the single source of truth
- **Binding convention gaps resolved**: Removed duplicate quota and notification
  logic from `dashboard.html` (canonical in `bb-global-nav.js`); create-org
  modal now uses `bbb-form="create-organisation"`; `bbb-show` on top-level
  elements in `job-details.html` replaced with explicit `style="display:none"`
  controlled by `job-page.js`; `settings.html` now loads `bb-bootstrap.js`
- **Avatar/email selectors scoped**: `auth.js updateUserInfo()` uses
  `.global-nav`-scoped `querySelector` instead of global `getElementById`

### Fixed

- **Org switcher after create**: Creating a new organisation now re-renders the
  full org list in the switcher so the new org appears immediately
- **Duplicate org names**: Creating an organisation with a name that already
  exists in the user's orgs now returns a 400 error
- **Seed idempotency**: All seed inserts use `ON CONFLICT DO NOTHING`;
  `auth.identities` uses correct composite key `(provider, id)`

### Security

- **Seed PII removed**: Real email addresses and provider IDs replaced with
  synthetic fixtures (`seed-admin@example.com`, `seed-member@example.com`)

## [0.27.1] – 2026-03-08

### Added

- **Postman-ready OpenAPI spec**: Added `docs/api/openapi.yaml` covering the
  current API surface for backend testing and exploration, including auth,
  request bodies, core response shapes, and documented corrections where live
  handlers differ from requested route assumptions.

## [0.27.0] – 2026-02-23

### Added

- **Webflow Designer extension auth flow**: Added dedicated popup auth handoff
  path and message contract for returning Supabase session tokens to the
  extension after sign-in.
- **Review app environment wiring**: Added `WEBFLOW_REDIRECT_URI` handling and
  updated preview app workflow config so webflow OAuth callback URLs are wired
  in CI/deploy flows.
- **UI/extension config updates**: Extended local extension manifest/build
  artifacts and widget UI styling/assets for the Webflow Designer embed.
- **Jobs list stats include**: Added optional `include=stats` support on
  `GET /v1/jobs` so extension/dashboard list views can consume job stats without
  per-job detail fetches.
- **Result chart interactions**: Added clickable past-results chart bars in the
  Webflow Designer extension that open each job's detailed results page.

### Changed

- **Webflow publish behavior**: Switched run-on-publish enabling to occur only
  when a Webflow site connection is completed; removed auto-enable behavior on
  first scan/load.
- **Auth page resilience**: Made extension auth modal load failures non-blocking
  so the app remains usable even when auth assets are delayed.
- **Auth handoff validation**: Hardened popup origin checks and request handling
  to avoid false failures in cross-origin postMessage exchange.
- **Extension results layout**: Reworked completed-run cards into grouped
  "Latest report" and "Past results" sections, default collapsed card details,
  and improved status/date placement for faster scanning.
- **Past-results chart presentation**: Updated chart to show the last 12
  completed runs with stacked OK/Error outlined bars, numeric Y-axis ticks, and
  native hover tooltips.
- **Latest report behaviour**: Changed latest result cards to start collapsed by
  default, matching past result cards.
- **Result table interactions**: Expanded result cards now auto-open the first
  available issue tab (for example, broken links) when details are opened.
- **In-progress card spacing**: Hidden empty issue and action rows while no
  issue pills are available to prevent extra blank space during active runs.

### Fixed

- **Extension popup/auth reliability**: Fixed token handoff edge cases where
  sign-in returns were dropped or malformed due to load timing and origin
  mismatches.
- **Webflow connection payload handling**: Corrected payload structure used for
  Webflow connect requests and added fallback behavior when a workspace is not
  explicitly selected.
- **Config consistency**: Added environment key examples and backend override
  support for custom Supabase auth endpoint use.
- **Extension export correctness**: Fixed extension export to build CSV from the
  API export payload (`columns` + `tasks`) instead of downloading raw JSON as a
  CSV file.
- **Issue details accuracy**: Replaced placeholder issue rows in extension tabs
  with real `/v1/jobs/:id/tasks` data, including correct broken/slow filtering,
  path-only display, and clickable links that open full URLs in a new window.
- **Live results refresh stability**: Fixed excessive re-renders/flicker by only
  re-rendering completed results and chart sections when relevant job signatures
  change.
- **Completed run transition**: Fixed state transitions so newly completed runs
  appear in Latest/Past results immediately when the in-progress card is hidden.

## [0.26.6] – 2026-02-14

### Fixed

- **Batch Page Upsert Robustness**: Page record creation now binds bulk text
  array parameters correctly and deduplicates duplicate paths within a chunk
  before upsert, preventing PostgreSQL batch upsert conflicts while preserving
  `RETURNING` IDs for both new and existing rows.

## [0.26.5] – 2026-02-13

### Fixed

- **Discovered Link Timeout Noise**: Hardened discovered-link persistence to use
  a bounded detached context and skip writes when task deadline budget is too
  low, reducing intermittent `context deadline exceeded` spikes in
  `BLUE-BANDED-BEE-86`.
- **Skip Log Context**: Added `job_id` and `domain` structured fields to new
  discovered-link skip logs for easier job-level traceability.

## [0.26.4] – 2026-02-11

### Changed

- **Go 1.26 Toolchain Upgrade**: Upgraded runtime and build tooling to Go 1.26.0
  across `go.mod`, Docker builder image, and GitHub Actions workflows to keep
  local, CI, and deploy environments aligned.
- **Go Modernisation Pass**: Applied Go 1.26 modernisers (`go fix`) in queue and
  worker hot paths, simplifying clamp/loop patterns while preserving existing
  behaviour.
- **Google API Response Typing**: Replaced remaining `map[string]any` success
  payloads in Google integration handlers/tests with typed response structs for
  stronger type intent and safer assertions.

### Fixed

- **Notification Listener Resilience**: Added panic recovery around the
  background notification listener goroutine to prevent a single panic from
  taking down the process.
- **CI Lint Compatibility**: Upgraded `golangci-lint` in CI to `v2.9.0` so lint
  runs correctly against Go 1.26 targets.

## [0.26.3] – 2026-02-07

### Fixed

- **Invite Existing Users**: Inviting a user who already has a Supabase account
  no longer returns a 500 error. The system detects the existing account and
  sends a magic link email instead, with the invite record still created for
  tracking.
- **Invite List Refresh**: Pending invites list now refreshes immediately after
  sending or revoking an invite (fixed missing `await` calls).

### Added

- **Copy Invite Link**: Pending invites now show a "Copy link" button so admins
  can manually share the invite URL via other channels. Button is hidden when no
  link is available.
- **Email Delivery Status**: Invite API response now includes an
  `email_delivery` field. The UI shows a warning toast when the notification
  email could not be delivered, advising the admin to share the invite link
  manually.
- **Warning Toast Style**: Settings toast notifications support a new warning
  colour scheme (amber) for non-critical alerts.

### Changed

- **Supabase Auth Helpers**: Extracted shared `resolveSupabaseAuthURL` and
  `supabaseServiceKey` helpers to reduce duplication across invite and magic
  link flows.
- **Magic Link Security**: Magic link endpoint now uses the publishable/anon key
  rather than the service role key, following Supabase best practices.
- **Error Body Reads**: Auth error response reads are now capped at 4 KB via
  `io.LimitReader` to prevent unbounded memory allocation.

## [0.26.2] – 2026-02-07

### Added

- **Loops.so Email Client**: Custom Go client for Loops.so transactional email
  API (`internal/loops/client.go`)
  - SendTransactional, SendEvent, CreateContact, UpdateContact methods
  - Structured APIError type with status code and message
  - Idempotency key support for safe retries
  - Zero external dependencies — follows existing storage client pattern
  - Full test suite with httptest server and custom RoundTripper
- **Request Metadata Utility**: Reusable request metadata extraction
  (`internal/util/request.go`)
  - Extracts client IP (X-Forwarded-For, X-Real-IP, RemoteAddr fallback)
  - User agent parsing for browser and OS detection
  - Cloudflare geolocation via managed transform headers (cf-ipcity, cf-region,
    cf-ipcountry, cf-timezone)
  - ISO country code to full name lookup (60+ countries)
  - Smart location formatting with deduplication (e.g. "Singapore" not
    "Singapore, Singapore, Singapore")
  - 27+ table-driven tests covering all extraction and parsing functions
- **Invite Email Enrichment**: Organisation invite emails now include inviter
  name, device info, location, IP address, and timestamp
  - Inviter name extracted from Supabase JWT UserMetadata with email fallback

### Changed

- **Client IP Extraction**: Moved `getClientIP` from `internal/api/jobs.go` to
  shared `util.GetClientIP` in `internal/util/request.go` — jobs handler updated
  to use the shared function

## [0.26.1] – 2026-02-07

### Fixed

- **Dashboard Cold-Load Race Condition**: Fixed "Failed to initialise dashboard"
  error appearing on first page load (cold browser cache) across all authed
  pages
  - Root cause: `DOMContentLoaded` fired before deferred `core.js` had executed,
    so `window.BB_APP.coreReady` didn't exist yet and the await was silently
    skipped
  - New `bb-bootstrap.js` provides `BB_APP.whenReady()` — a polling wrapper that
    waits for `core.js` to finish before proceeding
  - Loaded without `defer` so it's available immediately; all pages now use a
    single `await window.BB_APP.whenReady()` call
  - Homepage now shows a visible error banner on timeout instead of silently
    leaving buttons non-functional
- **Static Asset Rate Limiting**: Hard refresh no longer triggers 429 errors on
  JS, CSS, and image files
  - Rate limiter now excludes `/js/*`, `/styles/*`, `/web/*`, `/images/*`,
    `/config.js`, and `/favicon.ico` from per-IP rate limits
  - Static assets are cheap to serve and browsers legitimately request many in
    parallel during hard refresh

## [0.26.0] – 2026-02-06

### Added

- **Settings Page**: New unified settings page with account management
  - Profile section with read-only name/email display
  - Security section with password reset via Supabase
  - Team members list with admin-gated removal
  - Team invites with send/list/revoke functionality (admin-only)
  - Plan management with current plan, change plan, and usage history
  - Integrations sections for Slack, Google Analytics, and Webflow
- **Organisation Switching**: Global nav org switcher on all pages
  - Dropdown to switch between organisations
  - Persistent active org selection via localStorage
  - `bb:org-switched` event for cross-component coordination
- **Global Quota Display**: Usage quota now visible in nav on all pages
  - Auto-refreshes every 30 seconds when page visible
  - Updates immediately on organisation switch
  - Warning/exhausted states at 80%/100% thresholds
- **Admin Database Reset**: Settings button to reset database (admin-only)
- **Notifications Dropdown**: Bell icon with notification list in global nav

### Changed

- **Polling Architecture**: Consolidated duplicate polling systems
  - Dashboard and job details now use single fallback polling mechanism
  - Polling only starts when Supabase Realtime fails (staging/preview)
  - Fallback polling stops when first real realtime event received
  - Removed unused auto-refresh code from BBDataBinder (~35 lines)
- **Quota Display**: Moved from settings-only to global nav
  - Cached DOM references for efficiency
  - Re-entrancy guard prevents concurrent fetches
  - 15-second timeout prevents hung requests blocking future updates

### Fixed

- **Staging Realtime**: Fixed polling not working on Supabase preview branches
  where realtime connects but events don't fire
- **Quota Init**: Fixed quota fetch failing when Supabase not ready
- **Org Init Race Conditions**: Unified org initialisation to prevent race
  conditions between nav and page scripts
- **Notification Security**: Validate notification link protocols before
  rendering

## [0.25.0] – 2026-02-01

### Added

- **Domain Search Attributes**: Shared domain search input with
  `bbb-domain-create` and `bbb-domain-search` attributes to control create
  behaviour and dropdown visibility across dashboard and GA workflows
- **GA4 Analytics Integration**: Full Google Analytics 4 Data API integration
  for page view analytics
  - Progressive fetching: initial 100 pages, then background loops of 1000-page
    batches (until 10k), then 50000-page batches until all pages fetched
  - OAuth token refresh with RFC 6749 compliant form-urlencoded requests
  - Thread-safe token management with automatic refresh on 401 responses
  - Triggered automatically when job created with `findLinks: true`
- **Org-Scoped Page Analytics Storage**: GA4 data persisted per organisation
  - New `page_analytics` table stores 7-day, 28-day, and 180-day page view
    counts
  - Data tied to organisation that fetched it, preventing cross-org data leakage
  - RLS policies enforce organisation membership for all CRUD operations
- **GA4 Domain Mapping**: Connect GA4 properties to specific domains
  - Domain tags UI on GA4 connections in integrations modal
  - Inline search input for domain selection with Enter key support
  - PATCH endpoint (`/v1/integrations/google/analytics/{id}/domains`) to update
    mappings
  - `domain_ids` array column on `google_analytics_connections` table
- **Domain Creation Endpoint**: Dedicated `/v1/domains` endpoint for creating
  domains without job side effects
- **Job Response Enhancement**: Job creation response now includes `domain_id`
- **Traffic-Based Task Prioritisation**: High-traffic pages now get prioritised
  in the crawl queue
  - Log-scaled view curve assigns scores from 0.10 to 0.99 based on 28-day page
    views
  - Pages with 0-1 views are excluded from analytics scoring
  - Uses `GREATEST(structural_priority, traffic_score)` so traffic and structure
    both contribute
  - Traffic scores calculated after GA4 fetch completes, applied to pending
    tasks for reprioritisation
  - Link discovery also applies traffic scores via `GREATEST`
- **Job Detail Analytics**: Job tasks and exports now include GA4 page views
  (7d, 28d, 180d)

### Changed

- **Go Version**: Updated to Go 1.25.6 for security fixes
- **Sitemap Baseline Priority**: Default sitemap task priority lowered to 0.1 to
  let GA4 traffic scores surface high-traffic pages

### Fixed

- **Domain Creation Race**: Fixed TOCTOU race with atomic
  `INSERT ... ON CONFLICT` pattern in `GetOrCreateDomainID`
- **Console Noise**: Removed verbose debug logging from GA4 integration and page
  load
- **GA Account Reuse**: Stored GA4 accounts now retry auth initialisation and
  fall back to connection tokens when account tokens are unavailable
- **GA4 Path Normalisation**: Analytics upserts now normalise GA4 page paths so
  traffic scores match task paths more reliably
- **Task Reprioritisation Logs**: Reduced noise when no tasks are updated after
  traffic scoring

## [0.24.3] – 2026-01-27

### Fixed

- **Dashboard Authentication Display**: Fixed bug where elements with
  CSS-defined `display` properties (e.g., `display: flex`) were incorrectly
  forced to `display: block` after authentication state changes
  - Auth elements now properly preserve CSS cascade by removing inline styles
    instead of applying default values
  - Fixes layout breakage in header elements (user info, auth buttons)
  - Resolves quota display not updating after organisation switch
  - Prevents capturing "none" as original display value

## [0.24.2] – 2026-01-26

### Added

- **Immediate Job Triggering**: Jobs now automatically triggered when enabling
  schedules or auto-publish webhooks for Webflow sites
  - Schedule enable/update creates immediate job with
    `source_type="schedule_setup"`
  - Auto-publish enable creates immediate job with
    `source_type="auto_publish_setup"`
  - Provides instant user feedback that automation features are working
  - Graceful error handling - feature enable succeeds even if job creation fails

## [0.24.1] – 2026-01-26

### Changed

- **Test Suite Simplification**: Reduced test suite by 73% (23,000→6,369 LOC,
  78→21 files)
  - Removed: Mock-heavy database tests, CRUD validation tests, manager/worker
    unit tests with extensive mocking
  - Kept: Security tests (JWT validation, webhook signatures, SSRF protection),
    compliance tests (robots.txt enforcement), algorithm tests (sitemap parsing,
    error classification, job lifecycle)
  - Philosophy: Test for breakage, not coverage. Focus on security boundaries
    and complex business logic
- **Documentation Updates**: Updated README and Roadmap to reflect
  security/compliance testing approach instead of coverage percentage targets
- **CI Configuration**: Removed coverage floor gates from CI workflow, kept
  Codecov for visibility only (informational, non-blocking)

## [0.23.0] – 2026-01-04

### Added

- **Per-site Webflow Configuration**: Site-level settings for connected Webflow
  workspaces
  - Site list shows connected Webflow sites with search and pagination
  - Per-site schedule dropdown (None/6h/12h/24h/48h) creates schedulers
    automatically
  - Per-site "Run on Publish" toggle manages site-specific webhooks
  - Sites sorted by last updated with configuration persisted in
    `webflow_site_settings` table
  - OAuth callback now redirects to site configuration modal for initial setup

### Changed

- **Webflow webhook registration**: Moved from bulk registration during OAuth to
  per-site toggle control
  - Webhook handler now validates `auto_publish_enabled` before triggering cache
    warming jobs
  - Improved domain resolution for Webflow API v2 (custom domains as objects,
    constructed default from shortName)

### Fixed

- Fixed schedule dropdown not working due to Webflow API v2 returning custom
  domains as objects instead of strings
- Fixed auto-publish toggle not responding to clicks (hidden checkbox event
  handling)

## [0.22.4] – 2026-01-04

### Changed

- Auto-release now runs on main pushes and only tags when the Unreleased section
  contains actual entries

## [0.22.3] – 2026-01-03

### Changed

- **Org-scoped integrations**: Slack and Webflow connections now follow the
  active organisation context
  - Webflow webhooks accept workspace-based URLs and resolve via platform org
    mappings, with legacy token support retained
  - RLS policies for Slack and Webflow connections now enforce organisation
    membership
  - Slack auto-linking functions now use an explicit `search_path` for safer
    SECURITY DEFINER execution

## [0.22.2] – 2026-01-03

## [0.22.1] – 2026-01-03

## [0.22.0] – 2026-01-03

### Added

- **Webflow Integration**: OAuth 2.0 connection for Webflow workspaces
  - Full OAuth flow with HMAC-signed state for CSRF protection
  - API endpoints: `POST /v1/integrations/webflow` (initiate),
    `GET /v1/integrations/webflow` (list),
    `DELETE /v1/integrations/webflow/{id}` (disconnect)
  - Callback handler at `/v1/integrations/webflow/callback`
  - Access tokens stored securely in Supabase Vault with auto-cleanup on
    deletion
  - Token introspection fetches workspace IDs from Webflow API
  - User display name fetched via `authorized_user:read` scope (shows "FirstName
    LastName" or email)
- **Run on Publish**: Automatic cache warming when Webflow sites are published
  - Auto-registers `site_publish` webhooks for all connected sites during OAuth
  - Webhook handler at `/v1/webhooks/webflow/{token}` triggers cache warming
    jobs
  - Filters to primary domain (excludes `.webflow.io` staging domains)
- **Webflow Dashboard UI**: Connection management in integrations modal
  - Connect/disconnect Webflow workspaces
  - Displays authorising user's name and connection date
  - Success/error feedback using generic integration helper

### Changed

- **OAuth Utils Refactoring**: Extracted shared OAuth state signing to
  `oauth_utils.go`
  - HMAC-SHA256 signed state with nonce and 15-minute expiry
  - Shared between Slack and Webflow integrations
  - Defence-in-depth: secret validation in both generate and validate functions
- **Dashboard Feedback Helper**: Consolidated `showSlackSuccess/Error` and
  `showWebflowSuccess/Error` into generic `showIntegrationFeedback` function

### Security

- OAuth state secret validated before signing and verification (prevents empty
  key attacks)
- Webflow tokens encrypted at rest in Supabase Vault
- Webhook tokens per-user for secure callback routing
- `WebhookToken` field excluded from JSON serialisation

## [0.21.0] – 2026-01-02

### Added

- **SSRF Protection**: Crawler now blocks requests to private/local IP addresses
  - Custom `DialContext` validates IPs at connection time (prevents DNS
    rebinding)
  - Blocks loopback (127.x.x.x, ::1), private (10.x, 172.16-31.x, 192.168.x),
    link-local (169.254.x.x, fe80::), and unspecified (0.0.0.0, ::) addresses
  - `SkipSSRFCheck` config option for test environments only
- **Pre-commit Security Hooks**: Automated security scanning before each commit
  - `govulncheck` for Go dependency vulnerabilities
  - `trivy` for secrets and misconfigurations
  - `eslint-plugin-security` for JavaScript security issues
  - Graceful skip when tools not installed (CI still enforces)
- **ESLint Security Plugin**: JavaScript security linting with blocking rules
  - Critical rules as errors: `detect-child-process`,
    `detect-eval-with-expression`, `detect-non-literal-require`
  - Additional rules as warnings for false-positive-prone checks
- **Docker Security Hardening**: Container runs as non-root user (`appuser`)
- **HTTP Server Hardening**: `ReadHeaderTimeout: 5s` prevents Slowloris attacks
- **Cryptographic Jitter**: Retry delays use `crypto/rand` instead of
  `math/rand`
  - Fallback to `math/rand` only if crypto source unavailable
- **gosec Linter**: Static security analysis integrated into golangci-lint

### Changed

- **ESLint Config**: Reorganised rules with comments separating critical vs
  warning
- **Package.json**: Added `name`, `version`, `private: true` fields
- **SSRF Protection Refactored**: Extracted `ssrfSafeDialContext()` helper to
  eliminate code duplication across transport configurations
- **URL Validation Simplified**: Removed redundant DNS lookup from
  `validateCrawlRequest()` (SSRF check now happens at connection time only)

## [0.20.1] – 2026-01-02

### Added

- **Realtime Dashboard Updates**: Dashboard job list updates instantly via
  Supabase Realtime, replacing 1-second polling with WebSocket subscriptions
  - Organisation-scoped channel subscribes to INSERT, UPDATE, DELETE events
  - Fallback polling (60s) when realtime connection fails
  - Proper cleanup on page unload and SPA navigation
  - Retry logic with maximum attempts to prevent infinite loops
- **Realtime Job Progress**: Job detail page receives live progress updates
  - Per-job subscription for status and task completion changes
  - Auto-unsubscribes when job reaches terminal status
- **Realtime Performance Optimisations**: Database changes to support efficient
  realtime filtering
  - `jobs` table added to Supabase Realtime publication
  - `REPLICA IDENTITY FULL` set on `jobs` table for RLS-filtered events
  - New `users_own_jobs_simple` RLS policy for fast user-based filtering
- **Realtime Notification Updates**: Badge updates instantly when jobs complete
  - Supabase Postgres Changes subscription for `notifications` table
  - WebSocket CSP configured for `wss://adapt.auth.goodnative.co`
  - 200ms delay before querying to avoid transaction visibility race condition

### Changed

- **Dashboard Polling Interval**: Reduced from 1 second to 60 seconds (fallback
  only, realtime handles immediate updates)

## [0.20.0] – 2025-12-31

### Added

- **Notifications Dropdown**: Bell icon in header shows recent notifications
  - Displays last 10 notifications with unread count badge
  - Click-to-mark-read with navigation to notification link
  - "Mark all read" action for bulk clearing
  - "Notification Settings" button opens channel configuration modal
  - API endpoints: `GET /v1/notifications`, `POST /v1/notifications/{id}/read`,
    `POST /v1/notifications/read-all`
- **Slack Integration**: Job completion notifications via Slack DMs
  - OAuth flow for installing BBB Slack app to workspaces
  - Bot tokens stored securely in Supabase Vault (replaces custom AES-256-GCM)
  - Supabase Slack OIDC support for user authentication
  - Auto-linking users to Slack workspaces via database triggers
  - Notification service for sending DMs when jobs complete or fail
  - API endpoints for workspace management and user preferences
- **Notification System**: Database-backed notification queue with delivery
  tracking
  - `notifications` table for storing pending notifications
  - `slack_connections` table for workspace connections
  - `slack_user_links` table for user-to-workspace preferences
  - `slack_user_id` column on users table (synced from Supabase Auth)
- **Vault Integration**: Secure token storage using Supabase Vault
  - `store_slack_token()` and `get_slack_token()` SQL functions
  - Tokens encrypted at rest using Supabase's built-in encryption

### Changed

- **Token Storage**: Migrated from custom crypto to Supabase Vault
  - Removed `internal/util/crypto.go` (AES-256-GCM implementation)
  - Removed `SLACK_TOKEN_ENCRYPTION_KEY` environment variable
  - Bot tokens now stored via Vault SQL functions

## [0.19.3] – 2025-12-30

### Added

- **Organisation Switcher UI**: Dashboard header now shows organisation switcher
  dropdown
  - Users can switch between organisations they belong to
  - Dropdown shows all member organisations with visual indicator for active org
  - "Create Organisation" option allows users to create new organisations
  - Modal form for entering new organisation name with validation
  - Newly created organisations automatically set as active
  - Accessibility improvements: aria-labels, keyboard navigation (Escape to
    close)

## [0.19.2] – 2025-12-29

## [0.19.1] – 2025-12-29

### Added

- **Technology Detection**: Automatic detection of technologies used by crawled
  domains using wappalyzergo
  - Detects CMS platforms (WordPress, Webflow, Shopify), CDNs, frameworks,
    analytics tools, and more
  - Stores full HTML body sample in Supabase Storage (`page-crawls` bucket) for
    future re-analysis
  - New domains table columns: `technologies` (JSONB), `tech_headers` (JSONB),
    `tech_html_path` (storage reference), `tech_detected_at` (timestamp)
  - Detection runs asynchronously once per domain per worker session to avoid
    impacting crawl performance
  - Headers captured for fingerprinting: Server, X-Powered-By, X-Generator, etc.

### Fixed

- **CDN Cache Status Normalisation**: Cache status headers now correctly
  normalised across all major CDNs
  - CloudFront: `"Miss from cloudfront"` → `MISS`, `"Hit from cloudfront"` →
    `HIT`
  - Akamai: `TCP_HIT`, `TCP_MISS`, `TCP_MEM_HIT`, `TCP_DENIED` → standard
    `HIT`/`MISS`/`BYPASS`
  - Azure CDN: `TCP_REMOTE_HIT`, `UNCACHEABLE` → standard format
  - Fastly shielding: `"HIT, MISS"` → takes last value (edge POP result)
  - Netlify/RFC 9211: `"Netlify Edge"; hit` → `HIT`
  - Cloudflare: `NONE`, `UNKNOWN` → `BYPASS`
  - Fixes dashboard showing raw CDN strings instead of normalised HIT/MISS
  - Fixes `shouldMakeSecondRequest()` cache warming logic for non-Cloudflare
    CDNs
  - Covers top 10 CDNs representing ~90% of web traffic (verified Dec 2025)

## [0.19.0] – 2025-12-29

### Added

- **Multi-Organisation Support**: Users can now belong to multiple organisations
  and switch between them
  - New `organisation_members` table for many-to-many user-org relationships
  - New `active_organisation_id` column on users for session persistence
  - New `platform_org_mappings` table for Webflow/Shopify integration
  - New endpoints: `GET /v1/organisations` (list user's orgs) and
    `POST /v1/organisations/switch` (switch active org)
  - Helper functions `GetActiveOrganisation()` and
    `GetActiveOrganisationWithUser()` for consistent org context across handlers
  - `GetEffectiveOrganisationID()` provides backward compatibility (returns
    active org if set, otherwise legacy `organisation_id`)

### Security

- **Scheduler Jobs Query**: Fixed missing `organisation_id` filter in
  `getSchedulerJobs` query that could potentially expose jobs across
  organisations

### Changed

- **Organisation Scoping**: All 19 API endpoints now filter data by user's
  active organisation (4 dashboard, 8 jobs, 5 schedulers, 2 new org endpoints)
- **Stricter Validation**: Users without an organisation now receive HTTP 400
  "User must belong to an organisation" instead of empty results

## [0.18.10] – 2025-12-29

### Added

- **Compressed Sitemap Support**: ParseSitemap now handles gzip-compressed
  sitemaps (.xml.gz files and Content-Encoding: gzip responses)
  - Automatic detection via URL suffix or response header
  - Requests gzip encoding from servers for bandwidth savings

### Changed

- **Storage Optimisation**: Reduced unnecessary data storage in tasks table
  - `source_url` now only stored for 'link' source type (pages discovered from
    other pages); empty for 'sitemap' and 'manual' sources
  - `redirect_url` now only stored for significant redirects (different domain
    or path); trivial redirects (HTTP→HTTPS, www→non-www, trailing slash) no
    longer stored

## [0.18.9] – 2025-12-29

## [0.18.8] – 2025-12-27

## [0.18.7] – 2025-12-25

### Changed

- **Restart Job Simplification**: Frontend now handles job restart via standard
  creation endpoint
  - Restart button fetches existing job config and POSTs to `/v1/jobs`
  - Preserves: domain, find_links, concurrency, max_pages (with sensible
    defaults if missing); resets: use_sitemap=true
  - Removed backend `StartJob` and `restartJob` functions (~700 lines deleted)
  - Eliminates duplicate job creation logic and missing field bugs
  - **Breaking**: Restart endpoint removed and PUT actions restricted:
    - `/v1/jobs/:id/restart` endpoint no longer exists
    - `PUT /v1/jobs/:id` now only accepts `action="cancel"` (start/restart
      actions removed)
    - To restart a job: fetch config via `GET /v1/jobs/:id`, then
      `POST /v1/jobs` with the retrieved settings

## [0.18.6] – 2025-12-25

### Added

- **API Endpoints**: Added `/restart` and `/cancel` endpoints for job management

### Enhanced

- **Worker Notification**: Workers are now notified immediately after task
  creation for faster job processing
- **Robots.txt Handling**: Use default `*` rules and ignore SEO bot-specific
  rules for improved crawling
- **Robots.txt Delay**: Apply 50% multiplier to robots.txt crawl-delay values
  for more conservative rate limiting

### Fixed

- **Job Cancellation**: Fixed race condition in job cancellation logic
- **CancelJob Endpoint**: Now returns database errors properly
- **Crawl Delay**: Fixed multiplier logic to correctly override stored delay
  values
- **Deadlock Retry**: PostgreSQL deadlock errors (40P01) now correctly trigger
  transaction retry

## [0.18.5] – 2025-12-24

## [0.18.4] – 2025-12-24

## [0.18.3] – 2025-12-24

## [0.18.2] – 2025-12-24

### Added

- **Job Details Metadata**: Display additional job configuration in overview
  grid
  - Concurrency, max pages, and source type from jobs table
  - Crawl delay and adaptive delay from domains table

### Enhanced

- **Adaptive Rate Limiting**: Faster delay recovery and smarter task claiming
  - Reduced probe threshold from 20 to 5 consecutive successes before attempting
    lower delay
  - Reduced delay step from 1s to 500ms for finer-grained adjustments
  - Recovery from 21s → 10s adaptive delay now takes ~5.5 minutes instead of ~59
    minutes
  - Added `BBB_RATE_LIMIT_DELAY_STEP_MS` environment variable for operational
    tuning
- **Worker Task Claiming**: Workers now skip domains that aren't available yet
  - Added `EstimatedWait()` method to DomainLimiter for checking domain
    availability
  - Workers check domain availability before claiming tasks, allowing them to
    work on other jobs instead of blocking
  - Optimised lock contention by snapshotting job info cache before iteration

### Fixed

- **Job Details Page Task Filters**: Fixed non-functional filter buttons and
  search input on job details page
  - Added missing `cacheFilter` and `pathFilter` state properties
  - Implemented path keyword search with 300ms debounce (min 3 characters)
  - Fixed filter coordination so selecting status/cache filters clears path
    search and vice versa
  - Hit/Miss cache filter buttons now work correctly
- **Console Log Noise**: Removed verbose dashboard refresh logging
  - Eliminated "Refreshing dashboard data..." log message
  - Removed "Dashboard data refreshed" log with stats object
- **Password Strength Warning**: Silenced "Password strength checking not
  available" console warning on pages without signup forms
  - setupPasswordStrength() now exits silently when signup form elements aren't
    present
  - Password strength checking still works correctly on actual signup forms

## [0.18.1] – 2025-12-22

## [0.18.0] – 2025-12-22

### Added

- Recurring job scheduling system with support for 6, 12, 24, and 48-hour
  intervals
- Scheduler management API endpoints (`/v1/schedulers`) for creating, updating,
  deleting, and listing schedules
- Dashboard UI for managing schedules with enable/disable, view jobs, and delete
  functionality
- Schedule dropdown in job creation modal to optionally create recurring
  schedules
- Background scheduler service that checks for ready schedules every 30 seconds
  and creates jobs automatically
- Jobs created from schedulers are linked via `scheduler_id` and marked with
  `source_type='scheduler'`

### Fixed

- Replaced string error comparisons with sentinel errors
  (`ErrSchedulerNotFound`) for proper error handling
- Fixed N+1 query pattern in `startJobScheduler` by batching domain name lookups
- Added `GetDomainNames()` and `GetDomainNameByID()` helper functions to
  eliminate duplicate domain lookup code
- Improved validation: `ScheduleIntervalHours` now uses pointer type for
  explicit optional updates in API requests
- Added comprehensive aria-labels to all interactive elements in dashboard for
  improved accessibility
- Extracted time formatting logic into reusable `formatNextRunTime()` helper
  function

### Changed

- Scheduler API now uses pointer types for optional update fields to make intent
  explicit (nil = no update, value = update requested)
- Domain lookups now use consistent helper functions across scheduler code,
  eliminating inline queries

## [0.17.13] – 2025-12-02

### Changed

- Default job concurrency increased from 5 to 20 for dashboard/API jobs and from
  3 to 20 for webhook jobs; maximum allowed concurrency raised from 20 to 100.
- Dashboard auto-refresh interval reduced from 10 seconds to 1 second.
- Job details page now auto-refreshes every 1 second while job is
  running/pending, stopping automatically when job completes.
- Workers now stagger startup by 50ms each to prevent thundering herd on cold
  start and scale-up events.

### Added

- Concurrency dropdown added to dashboard job creation form with "Default"
  option (uses server default of 20) plus values 1-100.
- Webhook endpoint now accepts optional `?concurrency=N` query parameter to
  override the default concurrency for Webflow-triggered jobs.

### Removed

- Removed manual refresh interval dropdown from dashboard form (auto-refresh is
  now always enabled at 1 second).

## [0.17.12] – 2025-11-16

## [0.17.11] – 2025-11-16

## [0.17.10] – 2025-11-16

### Changed

- Batch manager now defaults to a 2 s flush interval with a 2000-item channel,
  and both settings can be tuned via `BBB_BATCH_MAX_INTERVAL_MS` and
  `BBB_BATCH_CHANNEL_SIZE` (clamped to 100‑10 000 ms and 500‑20 000 items) to
  absorb heavy task churn without blocking workers.
- Running task slot releases flush pending batches before falling back to a
  direct `DecrementRunningTasksBy` call, and the fallback timeout was extended
  (15 s) to avoid spurious "DecrementRunningTasksBy database error" warnings
  during spikes.
- CLI auth flow now has a dedicated `cli-login.html` that reuses the Supabase
  modal without leaking users' sessions, and the helper
  (`scripts/auth/cli_auth.py`) drives browser-based login, handles CORS
  securely, and caches tokens for load test scripts.

## [0.17.9] – 2025-11-15

## [0.17.8] – 2025-11-15

### Fixed

- Queue worker now inspects per-job counters before removing jobs, requeues
  stale running tasks, promotes waiting work into pending slots, and
  auto-completes jobs whose tasks finished—eliminating the stuck task warnings
  seen in Sentry.
- `CreatePageRecords` processes discovered links in ≤250 item batches using a
  single `INSERT ... ON CONFLICT DO UPDATE RETURNING` upsert, preventing
  long-running transactions and the "transaction already committed"/timeout
  alerts.
- Cloudflare Turnstile signup flow resets tokens on every attempt, retries error
  106010 challenges automatically, and emits `turnstile.*` telemetry to Sentry
  capturing token age, retries, and lifecycle transitions for better
  diagnostics.
- Orphaned task cleanup now batches updates (1000 at a time) to avoid hitting
  statement timeouts on jobs with huge backlogs, eliminating the repeated
  "Failed to cleanup orphaned tasks" log spam.

## [0.17.7] – 2025-11-10

## [0.17.6] – 2025-11-09

## [0.17.5] – 2025-11-09

- Queue claiming:
  - Removed the standalone `jobHasCapacityTx` / `jobHasPendingTasksTx`
    pre-checks (and their `FOR SHARE` locks) so workers no longer lock a job row
    twice before updating `running_tasks`.
  - All concurrency/emptiness checks now happen inside the existing claim CTE,
    eliminating the lock-upgrade deadlocks introduced in the 0.17.0
    optimisations.

## [0.17.4] – 2025-11-09

## [0.17.3] – 2025-11-08

## [0.17.2] – 2025-11-08

- Queue stability improvements:
  - Added `jobs.pending_tasks` / `jobs.waiting_tasks` counters plus a backfill +
    trigger to keep them in sync.
  - `EnqueueURLs` and the idle monitor now read those counters under
    `FOR UPDATE`, so pending slots are computed accurately without scanning
    `tasks`.
  - Worker rebalancer logs aggregated queue depth and demotes overflow pending
    rows using the counters (no more full-table queries).

## [0.17.0] – 2025-11-08

Series of minor optimisations to improve throughput and resource usage.

### Changed

- **Database Connection Pool**: Increased production connection pool from 37 to
  70 max connections (idle: 15 → 20)
  - Utilises available Supabase capacity (90 max connections)
  - Provides better concurrency during load bursts
  - Leaves 20 connections headroom for admin/monitoring
  - With Supavisor transaction pooling: ~150 MB memory impact (70 logical →
    ~10-15 actual connections)

### Enhanced

- **Task Claim Performance**: Optimised hot path with covering indexes and
  capacity pre-checks
  - Added partial indexes on tasks(job_id, priority_score, created_at) and
    jobs(id) with INCLUDE columns
  - Pre-check job capacity before expensive claim query (eliminates wasted CTE
    executions)
  - Replaced COUNT(\*) concurrency checks with EXISTS queries (faster
    short-circuit evaluation)
- **Batch Task Enqueuing**: Improved throughput for bulk task creation
  - Batch INSERT operations for discovered links and sitemap URLs
  - Reduced per-task overhead during link discovery phase
- **Worker Scaling Observability**: Scaling decisions now log their reason,
  worker deltas, and job counts
  - Captures `scale_up`, `scale_down`, and `no_change` outcomes with contextual
    metadata
  - Emergency/idle scale-down paths surface cooldowns, idle worker counts, and
    total job concurrency
- **Waiting Task Telemetry**: Added structured logging + metrics for tasks
  entering the `waiting` state
  - Records waiting reason (`concurrency_limit`, `blocking_retry`,
    `retryable_error`) and per-job counts via OpenTelemetry counter
  - Worker retries now emit explicit waiting reason logs to correlate backlog
    with root cause
- **Job Info Fetching**: Worker pool now deduplicates database lookups for job
  metadata via singleflight caching
  - Concurrent AddJob/prepareTask calls share the same DB query for domain info
  - Cache fallbacks reuse the same loader, eliminating redundant queries under
    load
- **Priority Update Debounce**: Prevents redundant sitemap/link priority updates
  during bursts
  - High-priority pages still update immediately; mid-tier waits 5s, low-tier
    30s between updates
  - Skips DB work when no lower-priority tasks exist or when cooldown is active
- **Job Info Cache Metrics**: Added hit/miss/invalidation counters and
  cache-size gauge
  - Worker pool logs cache hits/misses per job and exposes Grafana-friendly
    metrics for dashboarding

### Added

- **Load Test Analysis**: Comprehensive documentation of 82-minute 10x load test
  results
  - Documented priority update storm, waiting task backlog, worker scaling,
    cache behaviour, DB transactions
  - Resource utilisation analysis (Supabase memory/CPU scaling patterns)
  - Phased optimisation recommendations (immediate, short-term, long-term)
- **Database Optimisation Research**: Documented query performance bottlenecks
  and index recommendations
  - CTE claim query analysis (~53% total time), running_tasks counter contention
    (~20%), INSERT conflicts (~19%)
  - Covering index recommendations, batch update strategies, connection pooling
    guidance
- **Pages Lookup Index**: Added composite index on `pages(domain_id, path)` to
  accelerate path lookups during task enqueue and deduplication

### Removed

- **Legacy Load Test Scripts**: Deleted outdated `load-test-simple.sh` and
  README in favour of generate-test-jobs utility

## [0.16.13] – 2025-11-07

### Fixed

- **Orphaned Task Cleanup**: Resolved systemic issue where orphaned tasks from
  failed jobs weren't being cleaned up
  - Moved cleanup from 60-second maintenance transaction to dedicated goroutine
    running every 30 seconds
  - Processes entire jobs at once with no timeout constraint (eliminates O(n²)
    trigger overhead risk)
  - Handles jobs with 50,000+ orphaned tasks without timing out
  - Fixed job selection query: replaced invalid `SELECT DISTINCT` with `EXISTS`
    subquery (PostgreSQL incompatibility with `ORDER BY` + `FOR UPDATE`)
  - Fixed CancelJob to mark both 'pending' and 'waiting' tasks as skipped
    (previously only handled 'pending')
  - Added comprehensive integration tests covering failed job cleanup, cancelled
    job exclusion, and incremental processing
  - Expected impact: Prevents 16,000+ task backlogs from accumulating over weeks

## [0.16.12] – 2025-11-07

### Changed

- **Dependency Refresh**: Bumped key libraries to their latest stable releases
  before launch
  - sentry-go 0.32.0 → 0.36.2, golang-jwt/jwt/v5 5.2.2 → 5.3.0, pgx/v5 5.7.5 →
    5.7.6, prometheus/client_golang 1.23.0 → 1.23.2, golang.org/x/time 0.11.0 →
    0.14.0
  - Prometheus stack (common, procfs, otlptranslator) and grpc/genproto/
    protobuf modules updated to latest patch levels
  - Miscellaneous indirect upgrades (xmlquery/xpath, bitset, objx, go-difflib,
    go-edlib, OTel auto SDK) via `go get -u ./...`

## [0.17.0] – 2025-11-07

### Fixed

- **OpenTelemetry WriteHeader Warnings**: Eliminated "superfluous
  response.WriteHeader" warnings in production logs
  - Upgraded otelhttp from v0.54.0 to v0.63.0 (includes fix from PR #6055)
  - Fixed known bug in otelhttp response writer wrapper
    (open-telemetry/opentelemetry-go-contrib#6053)
  - Warnings were occurring on static file routes (/, /robots.txt, /dashboard)

### Changed

- **OpenTelemetry Upgrade**: Major update to observability stack
  - Core OTel packages: v1.32.0 → v1.38.0 (6 minor versions)
  - OTel SDK: v1.32.0 → v1.38.0
  - OTLP exporters: v1.28.0 → v1.38.0
  - Prometheus exporter: v0.54.0 → v0.60.0
  - All tests passing, no breaking changes

- **Dependency Updates**:
  - prometheus/client_golang: v1.20.5 → v1.23.0
  - grpc: v1.64.1 → v1.75.0
  - protobuf: v1.36.6 → v1.36.8
  - Various transitive dependencies updated

### Added

- **Diagnostic Instrumentation**: Enhanced middleware logging for debugging
  response writer issues
  - Added request context tracking (requestID, path, method) to responseWrapper
  - WARN-level logging for duplicate WriteHeader attempts with stack traces
  - DEBUG-level logging for all WriteHeader calls
  - Zero performance impact when not in debug mode

## [0.16.11] – 2025-11-07

## [0.16.10] – 2025-11-07

### Fixed

- **Duplicate Job Checking**: Fixed job duplicate checking for users without
  organisations
  - Previously checked `user_id IS NULL AND organisation_id = $1` when user had
    no organisation, causing check to fail
  - Now uses `user_id = $1 AND organisation_id IS NULL` to correctly match
    user-created jobs
  - Prevents false duplicate job creation for users without organisation
    assignments

### Added

- **Organisation Auto-Join**: Business email users now automatically join
  existing organisations
  - Added `GetOrganisationByName()` for case-insensitive organisation lookup
  - Added `isBusinessEmail()` to distinguish business vs personal email domains
  - When users sign up with business emails (e.g., `@teamharvey.co`), they
    automatically join existing organisation instead of creating duplicate
  - Personal email users continue to get individual organisations
  - Example: Second user signing up with `@acme.com` joins existing "Acme"
    organisation

- **Smart Organisation Names**: Organisation names now intelligently derived
  from email addresses
  - Business emails derive org name from domain: `simon@teamharvey.co` →
    "Teamharvey"
  - Personal emails with full name use the name: `user@gmail.com` + "John Doe" →
    "John Doe"
  - Personal emails without name use email prefix: `simon.smallchua@gmail.com` →
    "Simon.Smallchua Organisation"
  - Supports common TLDs (.com, .co.uk, .com.au, .io, .ai, .dev, .net, .org)
  - Recognises personal email providers: Gmail, Outlook, Hotmail, Yahoo, iCloud,
    ProtonMail, AOL, Zoho, Fastmail

## [0.16.9] – 2025-11-05

## [0.16.8] – 2025-11-04

## [0.16.7] – 2025-11-04

### Fixed

- **Worker Pool Error Logging**: Fixed spurious error logs for concurrency
  blocking in `GetNextTask` retry wrapper
  - Added check for `ErrConcurrencyBlocked` before error logging at retry
    summary level
  - Eliminates "Error getting next pending task" logs when tasks are blocked by
    concurrency limits
  - Completes the fix from 0.16.6 - now all code paths handle concurrency
    blocking gracefully

## [0.16.6] – 2025-11-03

### Fixed

- **Worker Pool Query Spam**: Fixed critical issue where workers hammered
  database with 200+ queries/second during concurrency blocking
  - Workers now detect when all pending tasks are blocked by job concurrency
    limits (`ErrConcurrencyBlocked`)
  - `claimPendingTask` gracefully handles concurrency-blocked jobs without error
    logging
  - Exponential backoff (200ms → 5s) applied when tasks exist but are
    concurrency-blocked
  - Expected impact: Reduce database queries from ~3,600/sec to <20/sec during
    high concurrency blocking
  - Prevents CPU throttling and machine restarts from excessive query load
  - Maintains same throughput - only reduces wasteful database polling

## [0.16.5] – 2025-11-03

### Added

- **Idle Worker Scaling & Health Probe**: Automatic worker pool management to
  reduce database query spam during concurrency blocking
  - Idle worker tracking scales down workers when all are idle and no work is
    available (configurable via `BBB_WORKER_IDLE_THRESHOLD`, default: disabled)
  - Health probe periodically checks for work to wake idle workers (configurable
    via `BBB_HEALTH_PROBE_INTERVAL_SECONDS`, default: disabled)
  - Calculates needed workers based on effective job concurrency accounting for
    domain throttling
  - Expected impact: ~3,600 queries/sec → <10 queries/sec during idle periods,
    maintaining <30s wake-up latency
  - Both features disabled by default for backwards compatibility

### Fixed

- **Race Conditions in Worker Pool**: Resolved critical concurrency issues in
  idle worker tracking
  - Fixed map panic from concurrent read/write on `idleWorkers` map by capturing
    count inside mutex lock
  - Cleared stale worker IDs during scale-down to prevent perpetual "all idle"
    state causing health probe spam
  - Removed unused `probeJobIndex` field to avoid confusion

## [0.16.4] – 2025-11-03

- Batch manager flushes (including poison-pill fallbacks) now run with 30-second
  contexts so task updates cannot hang indefinitely.
- Added `ExecuteWithContext` with proper retry/backoff and migrated
  `DecrementRunningTasks` plus mocks/tests to honour these deadlines.
- Queue workers now propagate bounded contexts through database writes,
  eliminating silent stalls under load.
- Added a `--log-level` CLI flag that overrides `LOG_LEVEL` for quick production
  debugging.

## [0.16.3] – 2025-11-03

### Added

- **Database Connection Resilience**: Implemented exponential backoff retry
  logic for database connections to prevent crash-looping during Supabase
  maintenance windows
  - Created `internal/db/retry.go` with configurable retry mechanisms
  - Added `WaitForDatabase()` function with up to 5-minute wait period
  - Added `InitFromURLWithSuffixRetry()` for queue database connections
  - Application now gracefully waits for database availability instead of
    crash-looping when Supabase terminates connections (SQLSTATE 57P01)
  - Retry configuration: 10 attempts max, 1s-30s exponential backoff with jitter
  - Prevents Fly.io machine restart exhaustion (observed: 10 restarts over 5
    hours)

## [0.16.2] – 2025-11-02

### Fixed

- **Pending Queue Overflow**: Fixed pending task queue flooding to 2,673 tasks
  (should be 50-100)
  - Root cause analysis identified three critical issues causing queue overflow:
    1. Unlimited concurrency jobs (NULL/0) set availableSlots to number of pages
       (hundreds/thousands)
    2. Task retries bypassed pending queue cap by going directly to 'pending'
       status
    3. Domain limiter concurrency overrides ignored when calculating available
       slots
  - **Fix #1**: Capped unlimited concurrency jobs to maximum 100 pending tasks
  - **Fix #2**: Routed all retry paths (blocking errors 403/429/503 and
    retryable errors) through 'waiting' status instead of directly to 'pending'
    - Added `TaskStatusWaiting` constant to task status enum
    - Modified retry logic in worker.go to set status to 'waiting' instead of
      'pending'
    - Ensures retries respect the pending queue capacity cap
  - **Fix #3**: Implemented domain limiter concurrency override support in
    availableSlots calculation
    - Added `ConcurrencyOverrideFunc` callback mechanism to DbQueue
    - Created `GetEffectiveConcurrency()` method on DomainLimiter to expose
      adaptive throttling limits
    - Modified `EnqueueURLs()` to query domain name and apply
      `min(configuredConcurrency, limiterOverride)`
    - Prevents over-promotion when domains are being adaptively throttled
  - Updated unit tests to match new SQL query pattern with domain JOIN
  - Expected improvement: Pending queue stays at 50-100 tasks instead of
    flooding to 2,600+

## [0.16.1] – 2025-11-02

## [0.16.0] – 2025-11-02

### Fixed

- **Queue Performance Bottleneck**: Eliminated GetNextTask query performance
  degradation causing 5ms-107s variance (21,000x)
  - Root cause: Query scanned ~5,000 pending tasks looking for jobs with
    available concurrency, causing table scan under load
  - Introduced 'waiting' status for tasks blocked by job concurrency limits
  - Only 'pending' tasks are now eligible for claiming, reducing typical scan
    from 5,000 rows to <100 rows (98% reduction)
  - Added capacity-aware enqueueing:
    `available_slots = concurrency - (running_tasks + existing_pending_count)`
  - New tasks only set to 'pending' if job has available capacity slots,
    remainder go to 'waiting'
  - Migration includes backfill UPDATE to immediately move existing blocked
    pending tasks to waiting status
  - DecrementRunningTasks now automatically promotes one waiting task to pending
    when capacity frees
  - Created partial indexes `idx_tasks_pending_ready` and
    `idx_tasks_waiting_by_job` for optimised scans
  - Added database functions `promote_waiting_task_for_job()` and
    `job_has_capacity()` for atomic state transitions
  - Expected improvement: Query times from 5-107s to consistent <10ms, reduced
    pool contention, improved throughput

### Added

- **Adaptive Domain Rate Limiter**: Shared per-domain throttle with persistence
  and concurrency management to prevent 429 cascades
  - Learns delays based on blocking responses and writes them back via
    `adaptive_delay_seconds` / `adaptive_delay_floor_seconds`
  - Applies shared backoff windows, linear (+1 s) growth, probing after
    sustained success, and concurrency reduction for heavily throttled domains
  - Configurable via `BBB_RATE_LIMIT_BASE_DELAY_MS`,
    `BBB_RATE_LIMIT_MAX_DELAY_SECONDS`, `BBB_RATE_LIMIT_SUCCESS_THRESHOLD`, and
    `BBB_RATE_LIMIT_MAX_RETRIES`
- **Queue Status Flow Documentation**: Comprehensive documentation of task
  lifecycle and status split solution
  - Visual state machine diagrams showing pending/waiting/running/completed
    transitions
  - Capacity calculation examples with concrete scenarios
  - Query performance before/after comparisons with scan size analysis
  - Implementation details for capacity-aware enqueueing and atomic promotion
  - Edge case handling documentation (no concurrency limit, concurrent
    completions, migration timing)
- **Job Failure Guardrail**: Worker pool stops jobs after configurable
  consecutive task failures (`BBB_JOB_FAILURE_THRESHOLD`, default 20) to
  preserve resources when domains remain blocked indefinitely

## [0.15.0] – 2025-10-29

### Added

- Optional `DATABASE_QUEUE_URL` so the worker queue can use a dedicated
  session-mode Postgres connection while the rest of the app remains on the
  pooled endpoint
- Workflow flag `[preview app]` now opt-in deploys GitHub PR preview apps;
  previews are skipped by default to reduce noise

### Changed

- Introduced an in-process semaphore and bounded retry/backoff logic around the
  task queue transactions (`DB_QUEUE_MAX_CONCURRENCY`, `DB_TX_MAX_RETRIES`,
  `DB_TX_BACKOFF_BASE_MS`, `DB_TX_BACKOFF_MAX_MS`) to keep Supabase from
  saturating under bursts
- Startup now reuses the shared pool for general queries while routing queue
  traffic through the optional connection, improving resilience during deploys
- Cache-warming second crawls now use a 500–1000 ms jittered delay with only
  three lightweight HEAD probes, cutting typical task time from ~17 s to under
  8 s

### Fixed

- Cleaned up recent migration churn (`running_tasks` counter column, timing
  columns, job status CTE) so all environments apply schema changes without
  manual intervention

## [0.14.2] – 2025-10-29

## [0.14.1] – 2025-10-26

## [0.14.0] – 2025-10-26

### Enhanced

- **Worker Pool Throughput**: Eliminated 5-second batch delay bottleneck in task
  claiming
  - Decoupled `running_tasks` counter decrement from batch updates for immediate
    concurrency slot release
  - Workers now claim tasks immediately after completion instead of waiting for
    batch flush (up to 5 seconds)
  - Added atomic `DecrementRunningTasks()` function to `DbQueueInterface` for
    immediate counter updates
  - Removed counter decrements from all 4 batch update queries (completed,
    failed, skipped, pending)
  - Batch manager continues to handle detailed field updates efficiently (95%
    transaction reduction maintained)
  - Expected improvement: 15-20 tasks/min → ~180 tasks/min (12× throughput
    increase)
  - Single atomic UPDATE per task completion adds negligible database load
  - Both success and error paths now decrement counters immediately for
    consistent behaviour

### Changed

- **Database Connection Tracking**: Enhanced connection management and cleanup
  - Added application name tracking for connection identification in PostgreSQL
  - Implemented automatic cleanup of stale connections from previous deployments
    on startup
  - Enhanced connection string builder with parameter sanitisation and
    validation
  - Added support for detecting and terminating idle connections from previous
    application instances
  - Connection strings now include `idle_in_transaction_session_timeout` and
    `statement_timeout` for stability

## [0.13.0] – 2025-10-25

### Enhanced

- **Worker Pool Efficiency**: Eliminated redundant database queries during link
  discovery
  - Cached `domain_id` in job info to avoid per-task lookups (50+ queries/second
    reduction)
  - Reduced connection pool pressure by 15-20% during high-throughput operations
  - Added defensive guard for missing domain context to prevent silent failures
  - Enhanced test coverage with explicit assertions for domain ID propagation

### Fixed

- **Pool Saturation During Link Discovery**: Workers no longer hit connection
  pool limits when processing discovered links
  - Root cause: Each completed task queried `SELECT domain_id FROM jobs`
    individually
  - Solution: Domain ID now flows through cached job info → task → link
    processing
  - Impact: Link enqueueing now succeeds consistently, enabling full crawl depth

## [0.12.3] – 2025-10-25

## [0.12.2] – 2025-10-25

## [0.12.1] – 2025-10-25

## [0.12.0] – 2025-10-24

### Enhanced

- **Database Performance Optimisation**: Critical scaling improvements for
  concurrent job processing
  - Added 9 foreign key indexes reducing lock wait times from 2-6s to <100ms
    (20x improvement)
  - Optimised RLS policies wrapping auth.uid() in SELECT for 10,000x reduction
    in per-row evaluation overhead
  - Refactored task claiming to use single CTE query combining SELECT + UPDATE
    (50% fewer round trips)
  - Split RLS policies for shared resources: relaxed INSERT for workers, strict
    SELECT via jobs, UPDATE restricted to service role only
  - Expected improvements: 2x task throughput (400-550/min → 800-1,000/min),
    2-10x concurrent job capacity (5 → 10-50+)

## [0.11.1] – 2025-10-24

### Fixed

- **Dashboard Timezone Handling**: Jobs now display correctly in user's local
  timezone instead of UTC
  - Added automatic timezone detection using browser's IANA timezone string
    (e.g., "Australia/Sydney")
  - Backend converts "today"/"yesterday" boundaries to user's timezone with
    graceful UTC fallback
  - Added "Last Hour" and "Last 24 Hours" rolling window filters alongside
    existing calendar-day filters
  - URL-encodes timezone parameter to handle special characters (`Etc/GMT+10` →
    `Etc%2FGMT%2B10`)
  - Applied to both bb-auth-extension.js and bb-components.js integration paths

## [0.11.0] – 2025-10-24

### Enhanced

- **Batch Task Status Updates**: Implemented PostgreSQL batch UPDATE system
  reducing database transactions by 95% (3000/min → 60/min)
- **Error Classification**: Distinguish infrastructure failures (retry
  indefinitely) from data corruption (poison pill isolation)
- **Graceful Shutdown**: Retry logic with backoff ensures zero data loss during
  application shutdown
- **Sentry Integration**: Critical failure monitoring for poison pills, database
  unavailability, and shutdown errors

## [0.10.3] – 2025-10-24

### Fixed

- **Trigger Storm Causing Deadlocks**: Optimised job progress trigger to fire
  only on task status changes, reducing executions by 80%
- **Dashboard Timezone Issue**: Jobs created in local timezone not showing when
  UTC rolls over (fix deferred, documented in plans/)

## [0.10.2] – 2025-10-23

## [0.10.1] – 2025-10-22

## [0.10.0] – 2025-10-22

### Fixed

- **Connection Pool Exhaustion and Deployment Crashes**: Fixed database
  connection exhaustion causing application crashes
  - Changed deployment strategy from rolling to immediate (stops old machine
    before starting new)
  - Prevents attempting 90 connections during deploys (old + new machine)
  - Eliminates "remaining connection slots reserved for SUPERUSER" errors
  - Brief downtime (~30-60s) during deploys is acceptable trade-off
- **Recovery Batch Timeouts**: Increased statement timeout for maintenance
  operations
  - Increased statement timeout from 30s to 60s for recovery batches
  - Increased context timeout from 35s to 65s to accommodate longer queries
  - Fixes persistent timeout errors when recovering 1,000+ stuck tasks

### Changed

- **Environment-Based Resource Scaling**: Worker pools and database connections
  now scale based on APP_ENV environment variable
  - **Production**: 50 workers (max 50), 32 max connections, 13 idle connections
  - **Preview/Staging**: 10 workers (max 10), 10 max connections, 4 idle
    connections
  - **Development**: 5 workers (max 50), 3 max connections, 1 idle connection
  - Dynamic worker scaling enforces environment-specific limits in AddJob and
    performance-based scaling
  - Prevents preview apps from scaling beyond their connection pool capacity
  - Prevents resource exhaustion during PR testing
  - Stays well under Supabase's 48-connection pool limit
  - Development uses minimal connections to allow multiple local instances

## [0.9.1] – 2025-10-20

## [0.9.0] – 2025-10-20

### Fixed

- **Task Recovery System**: Rewrote stuck task recovery to use batch processing
  - Processes stuck tasks in batches of 100 (oldest first) preventing
    transaction timeouts
  - Failed batches use exponential backoff and bail out after 5 consecutive
    failures to prevent database hammering
  - Tasks from cancelled/failed jobs are marked as failed immediately instead of
    retrying
  - Increased maintenance statement timeout from 5s to 30s to allow recovery
    batches to complete when processing large backlogs
  - Fixes issue where thousands of tasks could remain stuck indefinitely due to
    all-or-nothing transaction rollbacks
- **Monitoring and Alerting**: Reduced Sentry event spam whilst improving alert
  quality
  - Reduced stuck task monitoring from every 5 seconds to every 5 minutes
  - Replaced per-task Sentry events with single aggregated alert reporting
    actual totals (not sample size)
  - Separated job completion checks (30s) from health monitoring (5min)
  - Expected reduction: from 3,600+ events/hour to ~12 events/hour

## [0.10.2] – 2025-10-23

## [0.10.1] – 2025-10-22

## [0.10.0] – 2025-10-22

## [0.9.2] – 2025-10-20

## [0.9.1] – 2025-10-20

## [0.9.0] – 2025-10-20

## [0.8.8] – 2025-10-19

## [0.8.7] – 2025-10-19

## [0.8.6] – 2025-10-19

### Fixed

- **Job Timeout Cleanup**: Automatically mark jobs as failed if pending for 5+
  minutes with no tasks, or running for 30+ minutes with no progress

## [0.8.5] – 2025-10-19

### Fixed

- **Large Sitemap Processing**: Batch sitemap URL enqueueing (1000 URLs per
  batch) to prevent database timeouts on sites with 10,000+ URLs

## [0.8.4] – 2025-10-19

### Fixed

- **Cache Warming Optimisation**: Skip second request for BYPASS/DYNAMIC
  (uncacheable) content
- **Timeout Enforcement**: Clarified HTTP client and context timeout protection
- **Exponential Backoff**: Added backoff for 503/rate-limiting errors (1s, 2s,
  4s, 8s, 16s, 32s, 60s max)

## [0.8.3] – 2025-10-19

### Added

- **Crawling Analysis and Planning**: Research to improve crawling success rates
  and fix timeout issues
- **Referer Header**: Added Referer header to crawler requests
- **Grafana Cloud OTLP Integration**: Configured OpenTelemetry trace export to
  Grafana Cloud Tempo
  - Traces show complete request journeys with timing breakdowns for debugging
    slow jobs
  - Automatically captures job processing, URL warming, database queries, and
    HTTP requests
  - Configured with `OTEL_EXPORTER_OTLP_ENDPOINT` and
    `OTEL_EXPORTER_OTLP_HEADERS` environment variables
  - Health check endpoints (`/health`) excluded from tracing to reduce noise

### Changed

- **Crawler Random Delay**: Adjusted from 0-333ms to 200ms-1s range
- **Reduced Log Noise**: Health check requests from Fly.io no longer generate
  INFO-level logs
  - Health checks still function normally but don't clutter production logs
  - Real API requests continue to be logged for observability
- **Cloudflare Analytics Support**: Updated Content Security Policy to allow
  Cloudflare Web Analytics beacon resources when the zone is proxied

### Fixed

- **OTLP Endpoint Configuration**: Fixed trace export to use full URL path for
  Grafana Cloud compatibility
  - Endpoint now correctly includes `/otlp/v1/traces` path
  - Authentication uses HTTP Basic Auth with Base64-encoded Instance ID and
    Access Policy Token

## [0.8.2] – 2025-10-17

### Fixed

- **Job Recovery**: ensured stale task and job cleanup runs even when the DB
  pool is saturated by routing maintenance updates through a dedicated low-cost
  transaction helper

## [0.8.1] – 2025-10-17

## [0.8.0] – 2025-10-17

### Security

- **JWT Signing Keys Migration**: Migrated from legacy JWT secrets to asymmetric
  JWT signing keys
  - Replaced HMAC (HS256) shared secret validation with JWKS-based public key
    validation
  - **Supports both RS256 (RSA) and ES256 (Elliptic Curve P-256) signing
    algorithms**
  - Added `github.com/MicahParks/keyfunc/v3` for production-ready JWKS handling
    with automatic caching and key rotation
  - Removed `SUPABASE_JWT_SECRET` environment variable - no longer needed with
    public key cryptography
  - Implemented audience validation supporting both `authenticated` and
    `service_role` tokens
  - Enhanced error handling with JWKS-specific error detection and Sentry
    integration
  - Added context cancellation handling for graceful request timeouts
  - Updated authentication to use Supabase's `/auth/v1/certs` JWKS endpoint
  - 10-minute JWKS cache refresh aligns with Supabase Edge cache duration
  - Improved security posture by eliminating shared secret vulnerabilities

### Changed

- **Authentication Configuration**: Simplified auth config structure
  - Removed `JWTSecret` field from `auth.Config` struct
  - Renamed environment variables for clarity:
    - `SUPABASE_URL` → `SUPABASE_AUTH_URL`
    - `SUPABASE_ANON_KEY` → `SUPABASE_PUBLISHABLE_KEY`
  - Updated `NewConfigFromEnv()` to only require `SUPABASE_AUTH_URL` and
    `SUPABASE_PUBLISHABLE_KEY`
  - Updated all authentication tests to use RS256 tokens with proper JWKS
    mocking

### Enhanced

- **Test Coverage**: Comprehensive JWT validation tests for both RS256 and ES256
  - Added test JWKS servers with RSA and Elliptic Curve key generation
  - Tests for valid tokens (both RS256 and ES256), invalid signatures, invalid
    audiences, and context cancellation
  - Helper functions `startTestJWKS()`, `signTestToken()`,
    `startTestJWKSWithES256()`, and `signTestTokenES256()`
  - All tests passing with 100% coverage on new JWKS functionality

## [0.7.3] – 2025-10-14

### Security

- **gRPC Dependency Update**: Updated `google.golang.org/grpc` from v1.64.0 to
  v1.64.1
  - Fixes potential PII leak where private tokens in gRPC metadata could appear
    in logs if context is logged
  - Addresses Dependabot security alert CVE (indirect dependency via
    OpenTelemetry)
  - No impact on Adapt as we don't log contexts containing gRPC metadata

## [0.7.2] – 2025-10-14

## [0.7.1] – 2025-10-13

### Enhanced

- **Database Performance Optimisation**: Composite index strategy based on
  EXPLAIN ANALYZE profiling
  - Created `idx_tasks_claim_optimised` composite index for worker pool task
    claiming (50-70% latency reduction)
  - Added `idx_jobs_org_status_created` and `idx_jobs_org_created` composite
    indexes for dashboard queries (90%+ improvement, 11ms → <1ms)
  - Dropped unused indexes (`idx_jobs_stats`, `idx_jobs_avg_time`,
    `idx_jobs_duration`) saving ~1.3 MB and improving write performance
  - Eliminated sequential scans on jobs table (was scanning 5899 buffers for 164
    rows)
  - Migration: `20251013104047_add_composite_indexes_for_query_optimisation.sql`
  - Migration: `20251013103326_drop_unused_job_indexes.sql`

### Fixed

- **Database Connection Timeout Configuration**: Fixed nested timeout check bug
  - `idle_in_transaction_session_timeout` now correctly applied independently of
    `statement_timeout`
  - Previously, idle timeout was only added if statement_timeout was missing
  - Ensures zombie transaction cleanup works in all configurations

### Documentation

- **Database Performance**: Comprehensive documentation of optimisation strategy
  - Added "Connection Pool Sizing Strategy" section to DATABASE.md with sizing
    formulas and rationale
  - Documented composite index design and query patterns in DATABASE.md
  - Established PostgreSQL cache hit rate baseline (99.94% index, 99.76% table)
    via production query analysis
  - Both metrics exceed 99% target indicating optimal shared buffer
    configuration

## [0.7.0] – 2025-10-12

### Added

- **OpenTelemetry Tracing and Prometheus Metrics**: Comprehensive observability
  infrastructure for performance monitoring
  - Created dedicated `internal/observability` package with OpenTelemetry (OTLP)
    and Prometheus integration
  - Worker task tracing with span instrumentation for individual cache warming
    operations
  - Prometheus metrics endpoint (`/metrics`) exposing task duration histograms
    and counters
  - Configurable OTLP exporter for sending traces to Grafana Cloud or other
    OpenTelemetry backends
  - Environment-aware configuration with sampling controls (10% production, 100%
    development)
  - Process and Go runtime metrics automatically collected
  - HTTP request instrumentation via `otelhttp` middleware

- **Grafana Cloud Integration**: Production monitoring with Grafana Alloy for
  metrics collection
  - Deployed Grafana Alloy sidecar on Fly.io to scrape Prometheus metrics from
    application
  - Successfully configured metrics pipeline: App → Alloy → Grafana Cloud
    Prometheus
  - Resolved authentication and endpoint configuration for Cloud Access Policy
    tokens
  - 310+ metrics flowing into Grafana Cloud including database connections,
    worker performance, and HTTP traffic

- **Database Performance Optimisation**: Strategic indexing and query
  improvements
  - Added composite index `idx_tasks_running_started_at` on
    `(status, started_at)` for efficient stale task recovery
  - Enabled `pg_stat_statements` extension for PostgreSQL query performance
    analysis
  - Added `idle_in_transaction_session_timeout` (5 seconds) to prevent
    connection pool exhaustion
  - Cached normalised page paths on insert to reduce duplicate URL processing
  - Implemented duplicate page key check during URL enqueuing to prevent
    redundant tasks

- **Performance Testing Infrastructure**: Load testing tools for benchmarking
  and optimisation
  - Created `scripts/load-test-simple.sh` for automated performance testing
  - Batch job loading capability for testing with realistic workloads
  - Comprehensive documentation in `scripts/README-load-test.md`

- **Performance Research Documentation**: In-depth research on Go and PostgreSQL
  optimisation
  - Comprehensive analysis in `docs/research/2025-10/EVALUATION.md` covering
    profiling, database tuning, and architectural patterns
  - Documented 9 performance optimisation articles covering Go patterns,
    PostgreSQL pooling, and Supabase performance
  - Captured baseline performance metrics from Supabase dashboard for
    optimisation tracking

### Enhanced

- **Worker Pool Instrumentation**: Detailed telemetry for cache warming
  operations
  - Worker tasks emit OpenTelemetry spans with job ID, task ID, domain, path,
    and find_links attributes
  - Task duration and outcome metrics (completed/failed) recorded to Prometheus
  - Graceful shutdown with proper telemetry provider cleanup

- **Database Insert Efficiency**: Reduced redundant processing and improved
  throughput
  - Optimised insert operations to check for existing pages before database
    calls
  - Improved DB throttling to reduce duplicate queue insertions
  - Better handling of high-throughput scenarios with concurrent workers

- **HTTP Handler Instrumentation**: Automatic request tracing for API endpoints
  - `WrapHandler` function applies OpenTelemetry instrumentation when providers
    are active
  - Span names formatted as `METHOD /path` for clear trace visualisation
  - Trace and baggage context propagated across service boundaries

- **Link Extraction Performance**: Optimised visible link checker with reduced
  regex usage
  - Improved link visibility detection performance
  - Reduced CPU overhead from regex operations in crawler

### Fixed

- **Code Quality**: Addressed linting and formatting issues
  - Changed Codecov thresholds to informational mode (project-level only, not
    patch-level)
  - Fixed formatting across all modified files
  - Removed completed tasks from evaluation documentation

### Changed

- **Review App Workflow**: Skip documentation-only changes to reduce CI overhead
  - Review apps no longer deploy for `.md` file changes
  - Faster PR feedback cycle for documentation updates

- **Database Migrations**: New migrations for performance improvements
  - `20251012060206_idx_tasks_running_started_at.sql` - Adds composite index for
    worker recovery queries
  - `20251012070000_enable_pg_stat_statements.sql` - Enables query performance
    monitoring extension

### Documentation

- **Performance Analysis**: Extensive research documentation for future
  optimisation work
  - Supabase performance metrics baseline captured with 122 data points
  - Articles on Go performance patterns, database pooling, and microservices
    architecture
  - Evaluation plan documenting profiling methodology and optimisation targets

## [0.6.9] – 2025-10-12

## [0.6.8] – 2025-10-11

## [0.6.7] – 2025-10-11

### Fixed

- **Cache Warming Improvement Calculation**: Fixed "Improved Pages" incorrectly
  showing 100% when cache was already warm
  - Changed logic from `second_response_time < response_time` to
    `second_response_time > 0 AND second_response_time < response_time`
  - Pages with `second_response_time = 0` (already cached) are no longer counted
    as "improved"
  - Improvement rate now accurately reflects pages actually warmed by this job
  - Stats calculation version bumped to v4.0

## [0.6.6] – 2025-10-11

### Fixed

- **Job Metrics Calculation**: Fixed response time metrics displaying 0ms when
  cache warming is perfectly effective
  - Resolved bug where `COALESCE(second_response_time, response_time)` treated
    `0` as valid value instead of falling back to first request times
  - Now uses `NULLIF(second_response_time, 0)` to convert instant cache hits
    (0ms) to NULL, enabling fallback to meaningful first-request metrics
  - Recalculates existing jobs with buggy v1.0 or v2.0 stats automatically on
    migration
  - Stats calculation version bumped to v3.0

## [0.6.5] – 2025-10-11

### Fixed

- **Share Link API**: Return 200 with exists flag instead of 404 when no link
  exists
  - Changed GET /v1/jobs/:id/share-links to return 200 with `{"exists": false}`
    when no share link exists
  - Returns 200 with `{"exists": true, "token": "...", "share_link": "..."}`
    when link exists
  - Updated frontend to check exists field instead of 404 status
  - Eliminates console errors and provides cleaner API semantics

## [0.6.4] – 2025-10-11

### Added

- **Automated Release System**: CI now automatically creates releases when
  merging to main
  - Changelog-driven versioning using `[Unreleased]`, `[Unreleased:minor]`, or
    `[Unreleased:major]` markers
  - Auto-updates CHANGELOG.md with version number and date on merge
  - Creates git tags and GitHub releases with changelog content
  - All releases marked as pre-release until stable
- **Changelog Validation**: PR checks enforce changelog updates for all code
  changes
  - Blocks merges if `[Unreleased]` section is empty
  - Skips validation for docs/config-only changes

- **CI Formatting Enforcement**: Automated code formatting checks in CI pipeline
  - Added golangci-lint v2.5.0 with Australian English spell checking
  - Prettier formatting for Markdown, YAML, JSON, HTML, CSS, and JavaScript
  - Pre-commit hooks auto-format files before every commit
  - Format check job blocks merges if formatting is incorrect
- **Sentry Error Tracking on Preview Branches**: Preview apps now report to
  Sentry
  - Added `SENTRY_DSN` to review app secrets for staging environment visibility
  - 5% trace sampling for preview environments (vs 10% production)
  - Enables early issue detection before production deployment

### Fixed

- **Stuck Task Recovery**: Resolved recurring "task stuck in running state"
  errors
  - Fixed error handling in `recoverStaleTasks()` to properly rollback failed
    transactions
  - Recovery attempts now trigger transaction rollback when UPDATE fails
  - Expected 99.9% reduction in stuck task alerts (from 37k to <10 per
    occurrence)
- **Fly.io Review App Cleanup**: Fixed preview apps not being deleted after PR
  merge
  - Corrected YAML syntax error preventing cleanup job from running
  - Removed workflow-level paths-ignore that was blocking cleanup triggers
  - Added cleanup script for manual removal of orphaned apps
- **CI Coverage Report**: Fixed coverage job failing when tests are skipped
  - Coverage report now only runs if at least one test job succeeds
  - Prevents "no coverage files found" errors on documentation-only changes

### Enhanced

- **Code Quality Standards**: Documented linting requirements in CLAUDE.md
  - Australian English spelling enforced via misspell linter
  - Cyclomatic complexity limit: 35
  - Comprehensive linter suite: govet, staticcheck, errcheck, revive, gofmt,
    goimports

## [0.6.3] – 2025-10-09

### Added

- **Dashboard Share Links**: One-click share action on job cards generates and
  copies public URLs with inline feedback.
- **Share Link API Tests**: Contract coverage for create/reuse/revoke flows and
  shared endpoints.

### Improved

- **Unified Job Page**: `/shared/jobs/{token}` now reuses the job details
  template in read-only mode with shared API wiring.
- **Job Page Controls**: Share panel supports generate/copy/revoke with
  shared-mode guards and cleaner script loading.

## [0.6.2] – 2025-10-08

### Fixed

- **Worker Resilience**: Added per-task timeouts, panic recovery, and transient
  connection retries so stuck tasks and “bad connection” alerts recover
  automatically.

## [0.6.1] – 2025-10-08

### Added

- **Standalone Job Page**: Split the dashboard modal into `/jobs/{id}` with
  binder-driven stats, exports, metadata tooltips and pagination parity.

### Improved

- **Dashboard Binding Helpers**: Hardened metric visibility, tooltip loading,
  and task table rendering for the new job page.

## [0.6.0] – 2025-10-05

### Fixed

- **Database Security**: Resolved ambiguous column references in RLS policies
  - Fixed "column reference 'id' is ambiguous" errors preventing job cleanup
  - Qualified all column names with table names in RLS policy subqueries
  - Jobs no longer stuck in pending status due to SQL errors
- **Performance Metrics**: Corrected P95 response time display showing as NaN
  - Removed premature string conversion causing Math.round() to fail
  - P95 metric now displays correctly in job modal
- **Dashboard Authentication**: Fixed event delegation issues
  - Resolved authentication errors when accessing dashboard endpoints
  - Improved token handling and validation

### Changed

- **Failed Pages Metric**: Replaced broken links metric with failed pages count
  - Now counts tasks with `status='failed'` instead of HTTP 4xx codes only
  - Captures crawler errors that don't set HTTP status codes
  - Renamed `total_broken_links` to `total_failed_pages` for clarity
  - Removed `total_404s` metric entirely (redundant with failed pages)
  - Updated dashboard UI labels and export button text
  - Statistics calculation version bumped to v3.0
- **Performance Statistics**: Switched to second response time for cache
  effectiveness
  - Job statistics now use `second_response_time` (after cache warming) for all
    metrics
  - Provides more accurate representation of user-facing performance
  - First response time still tracked separately for cache improvement analysis

### Added

- **Metadata Tooltips**: Added help information for all job metrics
  - Info icon (🛈) displays contextual help for each statistic
  - Covers cache metrics, response times, failed pages, slow pages, redirects
  - Improved user understanding of dashboard metrics
- **CSP Headers**: Enhanced Content Security Policy for analytics
  - Added Google Tag Manager (GTM) to script-src and img-src
  - Added Google Analytics domains to connect-src
  - Added gstatic.com for Google services resources
- **CSV Export**: Improved data export functionality
  - Export buttons now correctly labelled (Failed Pages, Slow Pages)
  - CSV exports match updated metric definitions

### Improved

- **Dashboard Data Binding**: Enhanced attribute system for cleaner HTML
  - Updated from `data-*` to `bbb-*` attributes across dashboard
  - Backwards compatibility maintained during transition
  - Improved separation of concerns in frontend code
- **External Links**: Dashboard preview links in PR comments open in new tabs
  - Added `target="_blank"` for better user experience

## [0.10.2] – 2025-10-23

## [0.10.1] – 2025-10-22

## [0.10.0] – 2025-10-22

## [0.9.2] – 2025-10-20

## [0.9.1] – 2025-10-20

## [0.9.0] – 2025-10-20

## [0.8.8] – 2025-10-19

## [0.8.7] – 2025-10-19

## [0.8.6] – 2025-10-19

## [0.8.5] – 2025-10-19

## [0.8.4] – 2025-10-19

## [0.8.3] – 2025-10-19

## [0.8.2] – 2025-10-17

## [0.8.1] – 2025-10-17

## [0.8.0] – 2025-10-17

## [0.7.3] – 2025-10-14

## [0.7.2] – 2025-10-14

## [0.7.1] – 2025-10-13

## [0.7.0] – 2025-10-12

## [0.6.9] – 2025-10-12

## [0.6.8] – 2025-10-11

## [0.6.7] – 2025-10-11

## [0.6.6] – 2025-10-11

## [0.6.5] – 2025-10-11 – 2025-08-18

### Enhanced

- **Comprehensive API Testing Infrastructure**: Complete testing foundation for
  Stage 5
  - Added comprehensive tests for all major API endpoints (createJob, getJob,
    updateJob, cancelJob, getJobTasks)
  - Achieved 33.2% API package coverage (+1500% improvement from baseline)
  - Implemented interface-based testing (JobManagerInterface) and sqlmock
    patterns
  - Added comprehensive dashboard and webhook endpoint testing
  - Created separated test file structure for maintainability (test_mocks.go,
    jobs_create_test.go, etc.)
- **Function Refactoring Excellence**: Major complexity reduction using
  Extract + Test + Commit
  - Completed processTask refactoring: 162 → 28 lines (83% reduction)
  - Completed processNextTask refactoring: 136 → 31 lines (77% reduction)
  - Created 6 focused, single-responsibility functions with 100% coverage on
    testable functions
  - Achieved consistent 75-85% complexity reductions across targeted functions

### Added

- **Testing Architecture**: Interface-based dependency injection enabling
  comprehensive unit testing
  - MockJobManager and MockDBClient for isolated API testing
  - Sqlmock integration for testing direct SQL query functions
  - Auth context utilities testing (GetUserFromContext: 0% → 100%)
  - Table-driven test patterns with comprehensive edge case coverage

### Improved

- **Documentation Cleanup**: Streamlined and accuracy-focused documentation
  - Retired REFACTOR_PLAN.md after successful completion of methodology goals
  - Streamlined TEST_PLAN.md to forward-focused testing guide
  - Removed outdated testing documentation with inaccurate coverage claims
  - Deleted redundant and completed planning documents

## [0.5.34+] – 2025-08-16

### Improved

- **Code Architecture**: Major refactoring eliminating monster functions (>200
  lines)
  - Applied Extract + Test + Commit methodology across 5 core functions
  - 80% reduction in function complexity (1353 → 274 lines total)
  - Created 23 focused, single-responsibility functions with comprehensive tests
- **Testing Coverage**: Expanded from 30% to 38.9% total coverage
  - Added 350+ test cases across API, database, job management, and crawler
    logic
  - Introduced focused unit testing patterns with comprehensive mocking
  - Implemented table-driven tests and edge case validation
- **API Architecture**: Improved async patterns and error handling
  - CreateJob returns immediately with background processing
  - Proper context propagation with timeouts for goroutines
  - Idiomatic Go error patterns throughout
- **Database Operations**: Simplified and modularised core database functions
  - Separated table creation, indexing, and security setup
  - Enhanced testability with focused functions

## [Previous] – 2025-08-16

### Enhanced

- **Test Coverage Expansion**: Major improvements to jobs package testing
  - Improved test coverage: jobs package (1% → 31.6%)
  - Refactored WorkerPool to use interfaces for proper dependency injection
  - Created comprehensive unit tests for worker processing and job lifecycle
  - Moved helper functions from tests to production code where they belong
  - Fixed test design to test actual code rather than re-implement logic

### Added

- **Interface-Based Architecture**: Enabled proper unit testing
  - `DbQueueInterface` - Interface for database queue operations
  - `CrawlerInterface` - Extended with GetUserAgent method
  - Mock implementations for both interfaces in tests
- **Worker Processing Tests**: Core task processing functionality
  - `worker_process_test.go` - Tests for processTask and processNextTask
  - Error classification and retry logic tests
  - Task processing with various scenarios (delays, redirects, errors)

- **Job Lifecycle Tests**: Job management functionality
  - `job_lifecycle_test.go` - Tests for job completion detection
  - Job progress calculation tests
  - Status transition validation tests
  - Job status update mechanism tests

- **Production Helper Methods**: Moved from tests to JobManager
  - `IsJobComplete()` - Determines when a job is finished
  - `CalculateJobProgress()` - Calculates job completion percentage
  - `ValidateStatusTransition()` - Validates job status changes
  - `UpdateJobStatus()` - Updates job status with timestamps

### Fixed

- **Architectural Issues**: Resolved design problems preventing testing
  - WorkerPool now accepts interfaces instead of concrete types
  - CreatePageRecords now accepts TransactionExecutor interface
  - Removed unused methods from DbQueueInterface
  - Added missing GetUserAgent to MockCrawler

## [0.5.34] – 2025-08-08

### Enhanced

- **Test Infrastructure Improvements**: Comprehensive test suite enhancements
  - Fixed critical health endpoint panic when DB is nil - now returns 503 status
  - Made DBClient interface fully mockable for unit testing
  - Added sqlmock tests for database health endpoint
  - Extracted DSN augmentation logic to testable helper function
  - Created comprehensive unit tests for worker and manager components
  - Fixed broken `contains()` function in advanced worker tests
  - Added proper cleanup with `t.Cleanup()` for resource management
  - Removed fragile timing assertions in middleware tests
  - Enabled fail-fast behaviour in test scripts with `set -euo pipefail`

### Added

- **Mock Infrastructure**: Complete mock implementations for testing
  - Expanded MockDB with all DBClient interface methods
  - Created MockDBWithRealDB wrapper for sqlmock integration
  - Added comprehensive DSN helper tests covering URL and key=value formats

### Fixed

- **Test Quality Issues**: Resolved critical test suite problems
  - Fixed placeholder tests in db_operations_test.go
  - Centralised version string management to avoid hardcoded values
  - Improved test coverage: db package (10.5% → 14.3%), jobs package (1.1% →
    5.0%)
  - Modernised interface{} to any throughout test files

## [0.5.33] – 2025-08-06

### Enhanced

- **Webhook ID**: Created unique field on user for use in Webhook verification
  (Webflow) rather than using user ID

### Fixed

- **Account creation**: New accounts weren't being created and org name was
  wrong
  - Updated sign in or create account to create a new profile
  - Fix logic to create org name based on domain

- **Supabase / Github workflow**: Fixed schema issues with main vs. test-branch
  in supabase and github
  - Deleted all data from Supabase, including a gigantic job (abc.net.au) that
    was in an infinite loop and huge dataset and was just for testing.
  - Deleted all migrations
  - Created new clean migration file for both branches
  - Setup preview branching for PRs in Github to apply migrations there for
    tests

## [0.5.33] – 2025-08-02

### Enhanced

- **Admin Endpoint Security**: Implemented proper authentication for admin
  endpoints
  - Added `system_role` authentication requirement for all admin endpoints
    (`/admin/*`)
  - Admin endpoints now require `system_role` claim in JWT token for access
  - Unauthorised access attempts properly rejected with 403 Forbidden responses
  - Comprehensive test coverage added for admin authentication scenarios
  - Security enhancement ensures admin functionality is protected in production

## [0.5.32] – 2025-08-01

### Fixed

- **Job Progress Counting**: Fixed database trigger causing completed_tasks to
  exceed total_tasks
  - Updated `update_job_progress` trigger to recalculate total_tasks from actual
    task count
  - Migration: `20250801113006_fix_update_job_progress_trigger_total_tasks.sql`

## [0.5.31] – 2025-07-28

### Added

- **Comprehensive robots.txt Compliance**:
  - Added robots.txt parsing and crawl-delay honouring
  - Implemented URL filtering against Disallow/Allow patterns
  - Added robots.txt caching at job level to prevent repeated fetches
  - Manual root URLs now fail if robots.txt cannot be checked
  - Dynamically discovered links are filtered against robots rules
  - Added GetUserAgent method to crawler for proper identification
  - Added 1MB size limit for robots.txt parsing (security)

### Changed

- **Performance Optimisation**:
  - Robots.txt is now fetched once per job and cached in worker pool
  - Database query reduced from per-task to per-job for job information
  - Refactored processSitemap into smaller, maintainable functions

### Fixed

- **Interface Cleanup**: Removed duplicate DiscoverSitemaps method from
  interfaces
- **Security**: Reduced robots.txt size limit from 10MB to 1MB to prevent memory
  exhaustion

## [0.5.30] – 2025-07-27

### Added

- **Comprehensive Test Suite**:
  - Integration tests for core job operations (GetJob, CreateJob, CancelJob,
    ProcessSitemapFallback, EnqueueJobURLs)
  - Unit tests with mocks using testify framework
  - Refactored to use interfaces for better testability (CrawlerInterface)
  - Test coverage reporting with Codecov (17.4% coverage achieved)
  - Test Analytics enabled with JUnit XML reports
  - Codecov Flags and Components configuration for better test categorisation
- **Codecov Configuration**: Added codecov.yml for coverage reporting settings
- **Post-Launch API Testing Plan**: Created comprehensive testing strategy for
  implementation after product launch

### Changed

- **CI/CD Pipeline**:
  - Updated to use Supabase pooler URLs for IPv4 compatibility in GitHub Actions
  - Separated test workflow from deployment workflow
  - Added unit and integration test separation with build tags
- **Test Environment**: Standardised on TEST_DATABASE_URL for all test database
  connections
- **Testing Documentation**: Reorganised into modular structure under
  docs/testing/
- **Project Guidance**: Updated CLAUDE.md and gemini.md with platform
  documentation verification approach

### Fixed

- **CI Database Connection**: Resolved IPv6 connectivity issues by using
  Supabase session pooler
- **Test Environment Loading**: Fixed test configuration to properly use
  .env.test file
- **Coverage Calculation**: Fixed coverage reporting to include all packages
  with -coverpkg=./...
- **Test Race Conditions**: Implemented polling approach instead of fixed sleep
  times

## [0.5.29] – 2025-07-26

### Added

- **Sitemap Fallback**: Falls back to crawling from root page when sitemap
  unavailable
- **Database Migrations**: Transitioned to migration-based database management

## [0.5.28] – 2025-07-19

### Fixed

- **Memory Leak**: Removed unbounded in-memory HTTP cache that was causing
  memory exhaustion during crawl jobs. The cache was storing entire HTML pages
  without eviction, leading to out-of-memory crashes.

## [0.5.27] – 2025-07-19

### Enhanced

- **DB Optimisation**: Implemented a bunch of indexes on Supabase tables and
  deleted all historical data on pages/domains/tasks/jobs to clean and speed up.

## [0.5.26] – 2025-07-07

### Enhanced

- **Crawler Efficiency**: Implemented an in-memory cache for the HTTP client
  used by the crawler. This significantly reduces bandwidth and speeds up
  crawling by preventing the repeated download of assets (like JavaScript and
  CSS) within the same crawl job.

## [0.5.25] – 2025-07-06

### Github action updates

- **Codecov**: Implemented integration to report on testing coverage, indicated
  in badge in README
- **Go Report**: Added code quality reporting into README

## [0.5.24] – 2025-07-06

### Security

- **CSRF Protection**: Implemented global Cross-Site Request Forgery (CSRF)
  protection by adding Go 1.25's experimental `http.CrossOriginProtection`
  middleware to all API endpoints. This hardens the application against
  malicious cross-origin requests that could otherwise perform unauthorised
  actions on behalf of an authenticated user.

## [0.5.23] – 2025-07-06

### Added

- **Performance Debugging**: Implemented Go's built-in flight recorder
  (`runtime/trace`) to allow for in-depth performance analysis of the
  application in production environments. The trace data is accessible via the
  `/debug/fgtrace` endpoint.

### Fixed

- **Flight Recorder**: Corrected the flight recorder's shutdown logic to ensure
  `trace.Stop()` is called during graceful server shutdown instead of
  immediately on startup. This allows the recorder to capture the full
  application lifecycle, making it usable for production performance debugging.

## [0.5.22] – 2025-07-03

### Enhanced

- **Database Performance**: Implemented an in-memory cache for page lookups
  (`pages` table) to significantly reduce redundant "upsert" queries. This
  dramatically improves performance during the page creation phase of a job by
  caching results for URLs that are processed multiple times within the same
  job.

## [0.5.21] – 2025-07-03

### Changed

- **Database Driver**: Switched the PostgreSQL driver from `lib/pq` to the more
  modern and performant `pgx`.
  - This resolves underlying issues with connection poolers (like Supabase
    PgBouncer) without requiring connection string workarounds.
  - The `prepare_threshold=0` setting is no longer needed and has been removed.
- **Notification System**: Rewrote the database notification listener
  (`LISTEN/NOTIFY`) to use `pgx`'s native, more robust implementation, improving
  real-time worker notifications.

### Enhanced

- **Database Performance**: Optimised the `tasks` table indexing for faster
  worker performance.
  - Replaced several general-purpose indexes with a highly specific partial
    index (`idx_tasks_pending_claim_order`) for the critical task-claiming
    query.
  - This significantly improves the speed and scalability of task processing by
    eliminating expensive sorting operations.

### Fixed

- **Graceful Shutdown**: Fixed an issue where the new `pgx`-based notification
  listener would not terminate correctly during a graceful shutdown, preventing
  the worker pool from stopping cleanly.

## [0.5.20] – 2025-07-03

### Added

- **Cache Warming Auditing**: Added detailed auditing for the cache warming
  retry mechanism.
  - The `tasks` table now includes a `cache_check_attempts` JSONB column to
    store the results of each `HEAD` request check.
  - Each attempt logs the cache status and the delay before the check.

### Enhanced

- **Cache Warming Strategy**: Improved the cache warming retry logic for more
  robust cache verification.
  - Increased the maximum number of `HEAD` check retries from 5 to 10.
  - Implemented a progressive backoff for the delay between checks, starting at
    2 seconds and increasing by 1 second for each subsequent attempt.

### Fixed

- **Database Connection Stability**: Resolved a critical issue causing
  `driver: bad connection` and `unexpected Parse response` errors when using a
  connection pooler (like Supabase PgBouncer).
  - The PostgreSQL connection string now includes `prepare_threshold=0` to
    disable server-side prepared statements, ensuring compatibility with
    transaction-based poolers.
  - Added an automatic schema migration (`ALTER TABLE`) to ensure the
    `cache_check_attempts` column is added to existing databases.

## [0.5.19] – 2025-07-02

### Enhanced

- **Task Prioritisation**: Refactored job initiation and link discovery for more
  accurate and efficient priority assignment.
  - The separate, post-sitemap homepage scan for header/footer links has been
    removed, eliminating a redundant HTTP request and potential race conditions.
  - The homepage (`/`) is now assigned a priority of `1.000` directly during
    sitemap processing.
  - Link discovery logic is now context-aware:
    - On the homepage, links in the `<header>` are assigned priority `1.000`,
      and links in the `<footer>` get `0.990`.
    - On all other pages, links within `<header>` and `<footer>` are ignored,
      preventing low-value navigation links from being crawled repeatedly.
    - Links in the page body inherit their priority from the parent page as
      before.

## [0.5.18] – 2025-07-02

### Enhanced

- **Crawler Efficiency**: Implemented a comprehensive visibility check to
  prevent the crawler from processing links that are hidden. The check includes
  inline styles (`display: none`, `visibility: hidden`), common utility classes
  (`hide`, `d-none`, `sr-only`, etc.), and attributes like `aria-hidden="true"`,
  `data-hidden`, and `data-visible="false"`. This significantly reduces the
  number of unnecessary tasks created.

## [0.5.17] – 2025-07-02

### Added

- **Task Logging**: Included the `priority_score` in the log message when a task
  is claimed by a worker for improved debugging.

### Fixed

- **Crawler Stability**: Fixed an infinite loop issue where relative links
  containing only a query string (e.g., `?page=2`) were repeatedly appended to
  the current URL instead of replacing the existing query.

## [0.5.16] – 2025-07-02

### Enhanced

- **User Registration**: The default organisation name is now set to the user's
  full name upon registration for a more personalized experience.
- **Organisation Name Cleanup**: Organisation names derived from email addresses
  are now cleaned of common TLDs (e.g., `.com`), ignores generic domains, and
  doesn't capitalise.

### Fixed

- **Database Efficiency**: Removed a redundant database call in the page
  creation process by passing the domain name as a parameter.
- **Task Auditing**: Ensured that the `retry_count` for a task is correctly
  preserved when a task succeeds after one or more retries.

## [0.5.15] – 2025-07-02

### Changed

- **Codebase Cleanup**: Numerous small changes to improve code clarity,
  including fixing comment typos, removing unused code, and standardising
  function names.
- **Worker Pool Logic**: Simplified worker scaling logic and reduced worker
  sleep time to improve responsiveness.

### Fixed

- **Architectural Consistency**: Corrected a flaw where the `WorkerPool` did not
  correctly use the `JobManager` for enqueueing tasks, ensuring
  duplicate-checking logic is now properly applied.

### Documentation

- **Project Management**: Updated `TODO.md` to convert all file references to
  clickable links and consolidated several in-code `TODO` comments into the main
  file for better tracking.
- **AI Collaboration**: Added `gemini.md` to document the best practices and
  working protocols for AI collaboration on this project.
- **Language Standardisation**: Renamed `Serialize` function to `Serialise` to
  maintain British English consistency throughout the codebase.

## [0.5.14] – 2025-06-26

### Added

- **Task Prioritisation Implementation**: Implemented priority-based task
  processing system
  - Added `priority_score` column to tasks table (0.000-1.000 range)
  - Tasks now processed by priority score (DESC) then creation time (ASC)
  - Homepage automatically gets priority 1.000 after sitemap processing
  - Header links (detected by common paths) also get priority 1.000
  - Discovered links inherit 80% of source page priority (propagation)
  - Added index `idx_tasks_priority` for efficient priority-based queries
  - All tasks start at 0.000 and only increase based on criteria

### Enhanced

- **Task Processing Order**: Changed from FIFO to priority-based processing
  - High-value pages (homepage, header links) crawled first
  - Important pages discovered early get higher priority
  - Ensures critical site pages are cached before less important ones

### Fixed

- **Recrawl pages with EXPIRED cache status**

## [0.5.13] – 2025-06-26

### Added

- **Task Prioritisation Planning**: Created comprehensive plan for
  priority-based task processing
  - PostgreSQL view-based approach using percentage scores (0.0-1.0)
  - Minimal schema changes - only adds `priority_score` column to pages table
  - Homepage detection and automatic highest priority assignment
  - Link propagation scoring - pages inherit 80% of source page priority
  - Detailed implementation plan in
    [docs/plans/task-prioritization.md](docs/plans/_archive/task-prioritisation.md)

### Enhanced

- **Job Duplicate Prevention**: Cancel existing jobs when creating new job for
  same domain
  - Prevents multiple concurrent crawls of same domain
  - Automatically cancels in-progress jobs for domain before creating new one
  - Improves resource utilisation and prevents redundant crawling

- **Cache Warming Timing**: Adjusted delay for second cache warming attempt
  - Increased delay to 1.5x initial response time for better cache propagation
  - Added randomisation to cache warming delays for more natural traffic
    patterns
  - Enhanced logging of cache status results for analysis

### Fixed

- **Link Discovery**: Fixed link extraction to properly find paginated links
  - Restored proper link discovery functionality that was inadvertently disabled

## [0.5.12] – 2025-06-06

### Enhanced

- **Cache Warming Timing**: Added 500ms delay between first request (cache MISS
  detection) and second request (cache verification) to allow CDNs time to
  process and cache the first response
- **Webflow Webhook Domain Selection**: Fixed webhook to use first domain
  (primary/canonical) instead of last domain (staging .webflow.io)
- **Webflow Webhook Page Limits**: Removed 100-page limit for webhook-triggered
  jobs - now unlimited for complete site cache warming

### Fixed

- **Build Error**: Removed unused `fmt` import from `internal/api/handlers.go`
  that was causing GitHub Actions build failures

## [0.5.11] – 2025-06-06

### Added

- **Webflow Webhook Integration**: Automatic cache warming triggered by Webflow
  site publishing
  - Webhook endpoint `/v1/webhooks/webflow/USER_ID` for user-specific triggers
  - Automatic job creation and execution when Webflow sites are published
  - Smart domain selection (uses first domain in array - primary/canonical
    domain)
- **Job Source Tracking**: Comprehensive tracking of job creation sources for
  debugging and analytics
  - `source_type` field: `"webflow_webhook"` or `"dashboard"`
  - `source_detail` field: Clean display text (publisher name or action type)
  - `source_info` field: Full debugging JSON (webhook payload or request
    details)

### Enhanced

- **Job Creation Architecture**: Refactored to eliminate code duplication
  - Extracted shared `createJobFromRequest()` function for consistent job
    creation
  - Webhook and dashboard endpoints now use common job creation logic
  - Improved maintainability and consistency across creation sources

### Technical Implementation

- **Database Schema**: Added `source_type`, `source_detail`, and `source_info`
  columns to jobs table
- **Webhook Security**: No authentication required for webhooks (Webflow can
  POST directly)
- **Source Attribution**: Dashboard jobs tagged as `"create_job"` ready for
  future `"retry_job"` actions

## [0.5.10] – 2025-06-06

### Fixed

- **Cache Warming Data Storage**: Fixed second cache warming data not being
  stored in database
- **Timeout Retry Logic**: Added automatic retry for network timeouts and
  connection errors up to 5 attempts

## [0.5.9] – 2025-06-06

### Enhanced

- **Worker Pool Scaling**: Improved auto-scaling for better performance and bot
  protection
  - Simplified worker scaling from complex job-requirements tracking to simple
    +5/-5 arithmetic per job
  - Auto-scaling: 1 job = 5 workers, 2 jobs = 10 workers, up to maximum 50
    workers (10 jobs)
  - Each job gets dedicated workers preventing single-job monopolisation and bot
    detection risks
- **Database Connection Pool**: Increased to support higher concurrency
  - MaxOpenConns: 25 → 75 connections to prevent bottlenecks with increased
    worker count
  - MaxIdleConns: 10 → 25 connections for better connection reuse
- **Crawler Rate Limiting**: Reduced aggressive settings for better politeness
  to target servers
  - MaxConcurrency: 50 → 10 concurrent requests per crawler instance
  - RateLimit: 100 → 10 requests per second for safer cache warming

### Technical Implementation

- **Simplified Scaling Logic**: Removed complex `jobRequirements` map and
  maximum calculation logic
  - `AddJob()`: Simple `currentWorkers + 5` with max limit of 50
  - `RemoveJob()`: Simple `currentWorkers - 5` with minimum limit of 5
  - Eliminated per-job worker requirement tracking for cleaner, more predictable
    scaling

## [0.5.8] – 2025-06-03

### Added

- **Cache Warming System**: Implemented blocking cache warming with second HTTP
  requests on cache MISS/BYPASS
  - Added `second_response_time` and `second_cache_status` columns to track
    cache warming effectiveness
  - Cache warming logic integrated into crawler with automatic MISS detection
    from multiple CDN headers
  - Blocking approach ensures complete metrics collection and immediate cache
    verification
  - Supabase can calculate cache warming success (`cache_warmed`) using:
    `second_cache_status = 'HIT'`

### Fixed

- **Critical Link Extraction Bug**: Fixed context handling bug that was
  preventing all link discovery
  - Link extraction was defaulting to disabled when `find_links` context value
    wasn't properly set
  - Now defaults to enabled link extraction, fixing pagination link discovery
    (e.g., `?b84bb98f_page=2`)
  - **TODO: Verify this fix works by testing teamharvey.co/stories pagination
    links**
- **Link Extraction Logic**: Consolidated to Colly-only crawler to extract only
  user-clickable links
  - Removed overly aggressive filtering that was blocking legitimate navigation
    links
  - Now only filters empty hrefs, fragments (#), javascript:, and mailto: links
- **Dashboard Form**: Fixed max_pages input field to consistently show default
  value of 0 (unlimited)

### Enhanced

- **Code Architecture**: Eliminated logic duplication in cache warming
  implementation
  - Cache warming second request reuses main `WarmURL()` method with
    `findLinks=false`
  - Removed redundant `cache_warmed` field - can be calculated in
    Supabase/dashboard
  - Database schema includes cache warming columns in initial table creation for
    new installations

## [0.5.7] – 2025-06-01

### Fixed

- **Critical Job Creation Bug**: Resolved POST request failures preventing job
  creation functionality
  - Fixed `BBDataBinder.fetchData()` method to properly accept and use options
    parameter for method, headers, and body
  - Method signature updated from `async fetchData(endpoint)` to
    `async fetchData(endpoint, options = {})`
  - POST requests now correctly send JSON body data instead of being converted
    to GET requests
  - Job creation modal now successfully creates jobs and refreshes dashboard
    data
- **Data Binding Library**: Enhanced fetchData method to support all HTTP
  methods
  - Added proper options parameter spread to fetch configuration
  - Maintained backward compatibility for GET requests (existing code
    unaffected)
  - Fixed API integration throughout dashboard for POST, PUT, DELETE operations

### Enhanced

- **Job Creation Modal**: Simplified interface with essential fields only
  - Removed non-functional include_paths and exclude_paths fields that aren't
    implemented in API
  - Hidden concurrency setting as job-level concurrency has no effect (system
    uses global concurrency of 50)
  - Set sensible defaults: use_sitemap=true, find_links=true, concurrency=5
  - Changed domain input from URL type to text type to allow simple domain names
    (e.g., "teamharvey.co")
- **User Experience**: Improved job creation workflow with better validation and
  feedback
  - Domain input now accepts domain names without requiring full URLs with
    protocol
  - Better error messaging when job creation fails
  - Real-time progress updates after successful job creation
  - Toast notifications for success and error states

### Technical Implementation

- **Data Binding Library Rebuild**: Updated and rebuilt all Web Components with
  fetchData fix
  - Rebuilt `bb-data-binder.js` and `bb-data-binder.min.js` with corrected
    method implementation
  - Updated `bb-components.js` and `bb-components.min.js` for production
    deployment
  - All POST/PUT/DELETE API calls throughout the application now function
    correctly
- **API Integration**: Fixed job creation endpoint integration
  - `/v1/jobs` POST endpoint now receives proper JSON data from dashboard
  - Request debugging confirmed proper method, headers, and body transmission
  - Removed debug logging after confirming fix works correctly

### Development Process

- **Testing Workflow**: Comprehensive debugging and testing of job creation flow
  - Traced request path from modal form submission through data binding library
    to API
  - Console logging confirmed fetchData method was ignoring POST parameters
  - Verified fix works by testing job creation with various domain inputs
  - Confirmed dashboard refresh and real-time updates work after job creation

## [0.5.6] – 2025-06-01

### Enhanced

- **User Experience**: Improved dashboard user identification and testing
  workflow
  - Updated dashboard header to display actual user email instead of placeholder
    "user@example.com"
  - Added automatic user avatar generation with smart initials extraction from
    email addresses
  - Real-time user info updates when authentication state changes
    (login/logout/token refresh)
  - Enhanced user session management with proper cleanup and state
    synchronisation

### Fixed

- **Dashboard User Display**: Resolved hardcoded placeholder in user interface
  - Replaced static "user@example.com" text with dynamic user email from
    Supabase session
  - Fixed avatar initials to properly reflect current authenticated user
  - Added fallback states for loading and error conditions
  - Improved authentication state listening for immediate UI updates

### Technical Implementation

- **Session Management**: Enhanced authentication flow integration
  - Direct Supabase session querying for reliable user data access
  - Auth state change listeners update user info automatically across
    login/logout cycles
  - Graceful error handling for session retrieval failures
  - Smart initials generation supporting various email formats
    (firstname.lastname, firstname_lastname, etc.)

### Development Workflow

- **Production Testing**: Completed full 6-step development workflow
  - GitHub Actions deployment successful for user interface improvements
  - Production deployment confirmed working via manual verification

## [0.5.5] – 2025-06-01

### Added

- **Authentication Testing Infrastructure**: Comprehensive testing workflow for
  authentication flows
  - Created and successfully tested account creation with
    `simon+claude@teamharvey.co` using real-time password validation
  - Implemented real-time password strength checking using zxcvbn library with
    visual feedback indicators
  - Added password confirmation validation with visual success/error states
  - Database verification of account creation process (account created but
    requires email confirmation)

### Enhanced

- **Authentication Modal UX**: Production-ready authentication interface with
  industry-standard patterns
  - Real-time password strength evaluation using zxcvbn library (0-4 scale with
    colour-coded feedback)
  - Live password confirmation matching with instant visual validation feedback
  - Enhanced form validation with field-level error states and success
    indicators
  - Improved user experience with immediate feedback on password quality and
    match status
  - Modal-based authentication flow supporting login, signup, and password reset
    workflows

### Fixed

- **Domain References**: Corrected all application URLs to use proper domain
  structure
  - Updated authentication redirect URLs from `goodnative.co` to
    `adapt.app.goodnative.co`
  - Fixed API base URLs in Web Components to point to `adapt.app.goodnative.co`
  - Updated all script URLs and CDN references in examples and documentation
  - Rebuilt Web Components with correct production URLs

### Documentation

- **Domain Usage Clarification**: Comprehensive documentation of domain
  structure and usage
  - **Local development**: `http://localhost:8080` - Adapt application for local
    testing
  - **Production marketing site**: `https://goodnative.co` - Marketing website
    only
  - **Production application**: `https://adapt.app.goodnative.co` - Live
    application, services, demo pages
  - **Authentication service**: `https://adapt.auth.goodnative.co` - Supabase
    authentication (unchanged)
  - Updated all documentation files to clearly specify domain purposes and usage
    contexts

### Technical Implementation

- **Authentication Flow Testing**: Complete browser automation testing of
  authentication workflows
  - Tested modal opening/closing, form switching between login/signup modes
  - Verified real-time password validation and strength indicators
  - Confirmed account creation process with database verification
  - Established testing patterns for future authentication feature development
- **Web Components Updates**: Rebuilt production components with correct domain
  configuration
  - Updated `web/src/utils/api.js` and rebuilt distribution files
  - Fixed OAuth redirect URLs in `dashboard.html`
  - Updated test helpers and example files with correct domain references

## [0.5.4] – 2025-05-31

### Added

- **Complete Data Binding Library**: Comprehensive template + data binding
  system for flexible dashboard development
  - Built `BBDataBinder` JavaScript library with `data-bb-bind` attribute
    processing for dynamic content
  - Implemented template engine with `data-bb-template` for repeated elements
    (job lists, tables, etc.)
  - Added authentication integration with `data-bb-auth` for conditional element
    display
  - Created comprehensive form handling with `data-bb-form` attributes and
    real-time validation
  - Built style and attribute binding with `data-bb-bind-style` and
    `data-bb-bind-attr` for dynamic CSS and attributes
- **Enhanced Form Processing**: Production-ready form handling with validation
  and error management
  - Real-time field validation with `data-bb-validate` attributes and custom
    validation rules
  - Automatic form submission to API endpoints with authentication token
    handling
  - Loading states, success/error messaging, and form reset capabilities
  - Support for job creation, profile updates, and custom forms with
    configurable endpoints
- **Example Templates**: Complete working examples demonstrating all data
  binding features
  - `data-binding-example.html` - Full demonstration of template binding with
    mock data
  - `form-example.html` - Comprehensive form handling examples with validation
  - `dashboard-enhanced.html` - Production-ready dashboard using data binding
    library

### Enhanced

- **Build System**: Updated Rollup configuration to build data binding library
  alongside Web Components
  - Added `bb-data-binder.js` and `bb-data-binder.min.js` builds for production
    deployment
  - Library available at `/js/bb-data-binder.min.js` endpoint for CDN-style
    usage
  - Zero runtime dependencies - works with vanilla JavaScript and Supabase

### Technical Implementation

- **Data Binding Architecture**: Template-driven approach where HTML controls
  layout and JavaScript provides functionality
  - DOM scanning system finds and registers elements with data binding
    attributes
  - Efficient element updates with path-based data mapping and template caching
  - Event delegation for `bb-action` attributes combined with data binding for
    complete template system
- **Authentication Integration**: Seamless Supabase Auth integration with
  conditional rendering
  - Elements with `data-bb-auth="required"` only show when authenticated
  - Elements with `data-bb-auth="guest"` only show when not authenticated
  - Automatic auth state monitoring and element visibility updates
- **Form Processing Pipeline**: Complete form lifecycle management from
  validation to submission
  - Client-side validation with multiple rule types (required, email, URL,
    length, pattern)
  - API endpoint determination based on form action with automatic
    authentication headers
  - Success/error handling with custom events and configurable redirects

## [0.5.3] – 2025-05-31

### Changed

- **Dashboard Architecture**: Replaced Web Components with vanilla JavaScript +
  attribute-based event handling
  - Removed Web Components dependencies (`bb-auth-login`, `bb-job-dashboard`)
    from dashboard
  - Implemented vanilla JavaScript with modern styling for better reliability
    and maintainability
  - Added attribute-based event system: elements with `bb-action` attributes
    automatically handle functionality
  - Replaced `onclick` handlers with `bb-action="refresh-dashboard"`,
    `bb-action="create-job"` pattern
  - Maintained modern UI design whilst switching to proven vanilla JavaScript
    approach

### Enhanced

- **Template + Data Binding Foundation**: Established framework for flexible
  dashboard development
  - Dashboard now demonstrates template approach where HTML layout is
    customisable
  - JavaScript automatically scans for `bb-action` and `bb-data-*` attributes to
    provide functionality
  - Event delegation system allows any HTML element with `bb-action` to trigger
    Adapt features
  - Sets foundation for future template binding system where users control
    layout design

### Fixed

- **Production Dashboard Stability**: Resolved Web Components authentication and
  loading issues
  - Dashboard now uses proven vanilla JavaScript patterns instead of
    experimental Web Components
  - Removed complex component lifecycle management in favour of direct API
    integration
  - Eliminated dependency on Web Components build pipeline for core dashboard
    functionality

### Technical Details

- Consolidated `dashboard-new.html` and `dashboard.html` into single vanilla
  JavaScript implementation
- Added `setupAttributeHandlers()` function with event delegation for
  `bb-action` attributes
- Maintained API integration with `/v1/dashboard/stats` and `/v1/jobs` endpoints
- Preserved modern grid layout and responsive design from Web Components version

## [0.5.2] – 2025-05-31

### Fixed

- **Authentication Component OAuth Redirect**: Resolved OAuth login redirecting
  to dashboard on test pages
  - Fixed auth state change listener to only redirect when `redirect-url`
    attribute is explicitly set
  - Simplified redirect logic - components without `redirect-url` stay on
    current page after login
  - Removed complex `test-mode` attribute approach in favour of intuitive
    behaviour
  - OAuth flows (Google, GitHub, Slack) now complete on test pages without
    unwanted redirects

### Enhanced

- **Component Design Philosophy**: Streamlined authentication component
  behaviour
  - Test pages: `<bb-auth-login>` (no redirect-url) = No redirect, works in both
    logged-in/out states
  - Production pages: `<bb-auth-login redirect-url="/dashboard">` = Redirects
    after successful login
  - Cleaner, more predictable component behaviour without special testing
    attributes

### Technical Details

- Auth state change listener now checks for `redirect-url` attribute before
  triggering redirects
- Removed `test-mode` from observed attributes and related logic
- Web Components rebuilt and deployed with simplified redirect handling
- Both initial load check and OAuth completion follow same redirect-url logic

## [0.5.1] – 2025-05-31

### Added

- **Dashboard Route**: Added `/dashboard` endpoint to resolve OAuth redirect 404
  errors
  - Created dashboard page handler in Go API to serve `dashboard.html`
  - Updated Dockerfile to include dashboard.html in container deployment
  - Fixed authentication component redirect behaviour to prevent 404 errors
    after successful login

### Enhanced

- **Web Components Testing Infrastructure**: Comprehensive test page
  improvements
  - Added `test-mode` attribute to `bb-auth-login` component to prevent
    automatic redirects during testing
  - Created logout functionality for testing different authentication states
  - Enhanced test page with authentication status display and manual controls
  - Fixed redirect issues that prevented proper component testing

### Fixed

- **Authentication Component Redirect Logic**: Resolved automatic redirect
  problems
  - Modified `bb-auth-login` component to respect `test-mode="true"` attribute
  - Updated redirect logic to properly handle empty redirect URLs
  - Fixed issue where authenticated users were immediately redirected away from
    test pages

### Documentation

- **Supabase Integration Strategy**: Updated architecture documentation with
  platform integration recommendations
  - Added comprehensive Supabase feature mapping to development roadmap stages
  - Enhanced Architecture.md with real-time features, database functions, and
    Edge Functions strategy
  - Updated Roadmap.md to incorporate Supabase capabilities across Stage 5
    (Performance & Scaling) and Stage 6 (Multi-tenant & Teams)
- **Development Workflow**: Enhanced CLAUDE.md with comprehensive working style
  guidance
  - Added communication preferences, git workflow, and tech stack leverage
    guidelines
  - Documented build process awareness, testing strategy, and configuration
    management practices
  - Created clear guidance for future AI sessions to work more productively

### Technical Details

- Dashboard route serves existing dashboard.html with corrected Supabase
  credentials
- Test mode in authentication component prevents both initial redirect checks
  and post-login redirects
- Web Components require rebuild (`npm run build`) when source files are
  modified
- Git workflow updated to commit freely but only push when ready for production
  testing

## [0.5.0] – 2025-05-30

### Added

- **Web Components MVP Interface**: Complete frontend infrastructure for Webflow
  integration
  - Built vanilla Web Components architecture using template + data slots
    pattern (industry best practice)
  - Created `bb-data-loader` core component for API data fetching and Webflow
    template population
  - Implemented `bb-auth-login` component with full Supabase authentication and
    social providers
  - Added `BBBaseComponent` base class with loading/error states, data binding,
    and event handling
- **Production Build System**: Rollup-based build pipeline for component
  distribution
  - Zero runtime dependencies (vanilla JavaScript, Supabase via CDN)
  - Minified production bundle (`bb-components.min.js`) ready for CDN deployment
  - Development and production builds with source maps and error handling
- **Static File Serving**: Integrated component serving into existing Go
  application
  - Added `/js/` endpoint to serve Web Components as static files from Go app
  - Components now accessible at
    `https://adapt.app.goodnative.co/js/bb-components.min.js`
  - Docker container properly configured to include built components

### Enhanced

- **Webflow Integration Strategy**: Clarified multi-interface architecture and
  user journeys
  - **BBB Main Website**: Primary dashboard built on Webflow with embedded Web
    Components
  - **Webflow Designer Extension**: Lightweight progress modals within Webflow
    Designer
  - **Slack Integration**: Threaded conversations with links to main BBB site
  - Updated documentation to reflect three distinct user journey patterns
- **Component Architecture**: Template-driven approach for maximum Webflow
  compatibility
  - Data binding with `data-bind` attributes for text content population
  - Style binding with `data-style-bind` for dynamic CSS properties (progress
    bars, etc.)
  - Event handling for user interactions (view details, cancel jobs, form
    submissions)
  - Real-time updates with configurable refresh intervals and WebSocket support

### Technical Implementation

- **API Integration**: Seamless connection to existing `/v1/*` RESTful endpoints
  - Authentication via JWT tokens from Supabase Auth
  - Error handling with structured API responses and user-friendly error
    messages
  - Rate limiting and CORS support for cross-origin requests
- **Development Workflow**: Streamlined build and deployment process
  - Source files in `/web/src/` with modular component structure
  - Build process: `npm run build` → commit built files → Fly deployment
  - No CDN required initially - components served from existing infrastructure

### Documentation

- **Complete Integration Examples**: Production-ready code examples for Webflow
  - `webflow-integration.html` - Copy-paste example for Webflow pages
  - `complete-example.html` - Full-featured demo with all component features
  - Comprehensive README with step-by-step Webflow integration instructions
- **Architecture Documentation**: Updated UI implementation plan with clarified
  user journeys
  - Documented template + data slots pattern and Web Components best practices
  - Clear separation between BBB main site, Designer Extension, and Slack
    integration
  - Technical justification for vanilla Web Components over framework
    alternatives

### Infrastructure

- **Deployment Ready**: Production infrastructure complete for Stage 4 MVP
  - Components automatically built and deployed with existing Fly.io workflow
  - Static file serving integrated into Go application without additional
    services
  - Backward compatible - no changes to existing API or authentication systems

## [0.4.3] – 2025-05-30

### Added

- **Complete Sentry Integration**: Comprehensive error tracking and performance
  monitoring
  - Properly initialised Sentry SDK in main.go with environment-aware
    configuration
  - Added error capture (`sentry.CaptureException()`) for critical business
    logic failures
  - Strategic error monitoring in job management, worker operations, and
    database transactions
  - Performance span tracking already operational: job operations, database
    operations, sitemap processing
  - Configured 10% trace sampling in production, 100% in development for optimal
    observability
- **Comprehensive Documentation Consolidation**: Streamlined from 31 to 10
  documentation files
  - Created unified `ARCHITECTURE.md` combining system design, technical
    concepts, and component details
  - Consolidated `DEVELOPMENT.md` merging setup, testing, debugging, and
    contribution guidelines
  - Cleaned up `API.md` with consistent endpoint references and comprehensive
    documentation
  - Created `DATABASE.md` covering PostgreSQL schema, queries, operations, and
    performance optimisation
  - Consolidated future plans into 3 actionable documents: UI implementation,
    Webflow integration, scaling strategy

### Changed

- **Documentation Structure**: Complete reorganisation for maintainability and
  clarity
  - Eliminated content overlap between architecture files (mental-model,
    implementation-details, jobs)
  - Fixed content inconsistencies: corrected project stage references, removed
    deprecated depth column mentions
  - Updated README.md with accurate Stage 4 status and enhanced documentation
    index with descriptions
  - Improved CLAUDE.md with updated code organisation reflecting current package
    structure
- **Error Monitoring Strategy**: Strategic approach to avoid over-logging while
  capturing critical issues
  - Focus on infrastructure failures, data consistency issues, and critical
    business operations
  - Avoided granular task-level logging while maintaining comprehensive system
    health monitoring
  - Integration with existing performance spans for complete observability

### Removed

- **Redundant Documentation**: Eliminated 21 redundant files and outdated
  content
  - Removed overlapping architecture files: mental-model.md,
    implementation-details.md, jobs.md
  - Consolidated reference files: codebase-structure.md, file-map.md,
    auth-integration.md, database-config.md
  - Cleaned up outdated plans: 8 completed or irrelevant planning documents
  - Removed CONTRIBUTING.md (merged into DEVELOPMENT.md) and duplicate guide
    content

### Fixed

- **Documentation Accuracy**: Corrected stale and inconsistent information
  throughout
  - Fixed project stage references (Stage 3 → Stage 4) in README.md
  - Removed deprecated depth column references from database documentation
  - Updated API endpoint paths to match current `/v1/*` structure
  - Corrected outdated technology references (SQLite → PostgreSQL)

## [0.4.2] – 2025-05-29

### Added

- **RESTful API Architecture**: Complete API infrastructure overhaul with modern
  standards
  - Implemented standardised error handling with request IDs and consistent HTTP
    status codes
  - Created comprehensive middleware stack: CORS, request ID tracking,
    structured logging, rate limiting
  - Built RESTful endpoint structure under `/v1/*` namespace for versioned API
    access
  - Added proper authentication middleware integration with Supabase JWT
    validation
- **API Response Standardisation**: Consistent response formats across all
  endpoints
  - Success responses include `status`, `data`, `message`, and `request_id`
    fields
  - Error responses provide structured error information with HTTP status codes
    and error codes
  - Request ID tracking for distributed tracing and debugging support
- **Enhanced Security**: Improved security posture with secured admin endpoints
  - Moved debug endpoints to `/admin/*` namespace with environment variable
    protection
  - Added CORS middleware for secure cross-origin requests
  - Implemented rate limiting with proper IP detection and standardised error
    responses

### Changed

- **API Endpoint Structure**: Migrated from ad-hoc endpoints to RESTful design
  - Job creation: `POST /v1/jobs` with JSON body instead of query parameters
  - Job status: `GET /v1/jobs/:id` following RESTful conventions
  - Authentication: Consolidated under `/v1/auth/*` namespace
  - Health checks: Standardised `/health` and `/health/db` endpoints
- **Error Handling**: Replaced inconsistent `http.Error()` calls with structured
  error responses
  - All errors now include request IDs for tracing
  - Consistent error codes: `BAD_REQUEST`, `UNAUTHORISED`, `NOT_FOUND`, etc.
  - Proper HTTP status code usage throughout the API
- **Code Organisation**: Refactored API logic into dedicated `internal/api`
  package
  - Separated concerns: handlers, middleware, errors, responses, authentication
  - Dependency injection pattern with clean handler structure
  - Eliminated duplicate endpoint logic and inconsistent patterns

### Removed

- **Legacy Endpoints**: Removed unused endpoints since APIs are not yet
  published
  - Removed `/site` and `/job-status` legacy endpoints and their handlers
  - Cleaned up duplicate code paths and unused imports
  - Simplified codebase by removing backward compatibility code

### Enhanced

- **Testing Infrastructure**: Created comprehensive API testing tools
  - Updated test login page to use new `/v1/*` endpoints
  - Added `api-tests.http` file for VS Code REST Client testing
  - Created detailed API testing guide with authentication examples
- **Documentation**: Updated API reference and implementation documentation
  - Comprehensive API testing guide with practical examples
  - Updated roadmap to reflect completed API infrastructure work
  - Enhanced code documentation with clear separation of concerns

### Technical Details

- New `internal/api` package structure with clean separation of handlers,
  middleware, and utilities
- Middleware stack processes requests in proper order: CORS → Request ID →
  Logging → Rate Limiting
- JWT authentication middleware integrates seamlessly with Supabase token
  validation
- Request ID generation uses timestamp + random bytes for unique request
  tracking
- Error responses provide consistent structure while maintaining security (no
  information leakage)

## [0.4.1] – 2025-05-27

### Fixed

- **Database Schema Issues**: Resolved critical production database errors
  - Added missing `error_message` column to jobs table to prevent database
    insertion failures
  - Fixed duplicate user creation constraint violations with idempotent user
    registration
  - `CreateUserWithOrganisation` now handles existing users gracefully instead
    of failing
- **User Registration Flow**: Enhanced authentication reliability
  - Multiple login attempts with same user ID no longer cause database
    constraint violations
  - Existing users are returned with their organisations rather than attempting
    duplicate creation
  - Improved error handling and logging for user creation scenarios

### Enhanced

- **Development Workflow**: Added git policy documentation to prevent accidental
  commits
- **Project Planning**: Added multi-provider account linking testing to roadmap
  for future investigation

### Technical Details

- Database migration adds `error_message TEXT` column to jobs table with
  `ALTER TABLE IF NOT EXISTS`
- User creation now checks for existing users before attempting INSERT
  operations
- Transaction rollback properly handles failed user creation attempts
- All database fixes are backward compatible with existing installations

## [0.4.0] – 2025-05-27

### Added

- **Complete Supabase Authentication System**: Full multi-tenant authentication
  with social login support
  - JWT validation middleware with structured error handling and token
    validation
  - Support for 8 social login providers: Google, Facebook, Slack, GitHub,
    Microsoft, Figma, LinkedIn + Email/Password
  - Custom domain authentication using `adapt.auth.goodnative.co` for
    professional OAuth flows
  - User and organisation management with automatic organisation creation on
    signup
  - Row Level Security (RLS) policies for secure multi-tenant data access
- **Protected API Endpoints**: All job creation and user data endpoints now
  require authentication
  - `/site` endpoint (job creation) now requires valid JWT token and links jobs
    to users/organisations
  - `/job-status` endpoint protected with organisation-scoped access control
  - `/api/auth/profile` endpoint for authenticated user profile access
  - User registration API with automatic organisation linking
- **Database Schema Extensions**: Enhanced schema to support multi-tenant
  architecture
  - Added `users` and `organisations` tables with foreign key relationships
  - Added `user_id` and `organisation_id` columns to `jobs` table
  - Implemented Row Level Security on all user-related tables
  - Database migration logic for existing installations

### Enhanced

- **Authentication Flow**: Complete OAuth integration with account linking
  support
  - Flexible email-based account linking with UUID-based permanent user identity
  - Session management with token expiry detection and refresh warnings
  - Structured error responses for authentication failures
  - Support for multiple auth providers per user account
- **Multi-tenant Job Management**: Jobs are now scoped to organisations with
  shared access
  - All organisation members can view and manage all jobs within their
    organisation
  - Jobs automatically linked to creator's user ID and organisation ID
  - Database queries respect organisation boundaries through RLS policies

### Security

- **Comprehensive Authentication Security**: Production-ready security features
  - JWT token validation with proper error handling and logging
  - Authentication service configuration validation
  - Standardised error responses that don't leak sensitive information
  - Row Level Security policies prevent cross-organisation data access
- **Protected Endpoints**: All sensitive operations require valid authentication
  - Job creation requires authentication and organisation membership
  - Job status queries limited to organisation members
  - User profile access restricted to authenticated user's own data

### Technical Details

- Custom domain setup eliminates unprofessional Supabase URLs in OAuth flows
- Database migration handles existing installations with
  `ALTER TABLE IF NOT EXISTS`
- JWT middleware supports both required and optional authentication scenarios
- Account linking strategy preserves user choice while preventing duplicate
  accounts
- All authentication endpoints follow RESTful conventions with proper HTTP
  status codes

## [0.3.11] – 2025-05-26

### Added

- **MaxPages Functionality**: Implemented page limit controls for jobs
  - `max` query parameter now limits number of pages processed per job
  - Tasks beyond limit automatically set to 'skipped' status during creation
  - Added `skipped_tasks` column to jobs table and Job struct
  - Progress calculation excludes skipped tasks:
    `(completed + failed) / (total - skipped) * 100`
  - API responses include skipped count for full visibility

### Enhanced

- **Smart Task Status Management**: Tasks receive appropriate status at creation
  time
  - First N tasks (up to max_pages) get 'pending' status
  - Remaining tasks automatically get 'skipped' status
  - Eliminates need for post-creation status updates
- **Database Triggers**: Updated progress calculation triggers to handle skipped
  tasks
  - Automatic counting of completed, failed, and skipped tasks
  - Progress percentage calculation excludes skipped tasks from denominator
  - Job completion logic updated to account for skipped tasks

### Changed

- **Link Discovery Default**: Changed default behaviour to enable link discovery
  by default
  - `find_links` now defaults to `true` (was previously `false`)
  - Use `find_links=false` to disable link discovery and only crawl sitemap URLs
  - More intuitive API behaviour for comprehensive cache warming

### Fixed

- **Job Completion Logic**: Fixed job completion detection for jobs with limits
  - Updated completion checker: `(completed + failed) >= (total - skipped)`
  - Added safety check for division by zero in progress calculations
  - Prevents jobs from being stuck with remaining skipped tasks

### Technical Details

- MaxPages limit of 0 means unlimited processing (default behaviour)
- Task status determined during `EnqueueURLs` based on current task count vs
  max_pages
- Database schema migration adds `skipped_tasks INTEGER DEFAULT 0` column
- Backward compatible with existing jobs (skipped_tasks defaults to 0)

## [0.3.10] – 2025-05-26

### Added

- **Database-Driven Architecture**: Moved critical business logic to PostgreSQL
  triggers for improved reliability
  - Automatic job progress calculation (`progress`, `completed_tasks`,
    `failed_tasks`) via database triggers
  - Auto-generated timestamps (`started_at`, `completed_at`) based on task
    completion status
  - Eliminates race conditions and ensures data consistency across concurrent
    workers
- **Enhanced Dashboard UX**: Comprehensive date range and filtering improvements
  - Smart date range presets: Today, Last 24 Hours, Yesterday, Last 7/28/90
    Days, All Time, Custom
  - Automatic timezone conversion from UTC database timestamps to user's local
    timezone
  - Complete time series charts with all increments (shows empty periods for
    accurate visualisation)
  - Dynamic group-by selection that auto-updates based on date range scope

### Fixed

- **Timezone Consistency**: Resolved incorrect timestamp display in dashboard
  - Standardised all database timestamps to use UTC (`time.Now().UTC()` in Go,
    `NOW()` in PostgreSQL)
  - Fixed dashboard date formatting to properly convert UTC to user's local
    timezone
  - Corrected date picker logic to handle precise timestamp filtering instead of
    date-only ranges
- **Dashboard Data Access**: Fixed Row Level Security (RLS) blocking dashboard
  queries
  - Added anonymous read policies for `domains`, `pages`, `jobs`, and `tasks`
    tables
  - Enables dashboard functionality while maintaining security framework for
    future auth

### Enhanced

- **Simplified Go Code**: Removed complex manual progress calculation logic
  - `UpdateJobProgress()` function now handled entirely by database triggers
  - Eliminated manual timestamp management in job start/completion workflows
  - Reduced code complexity while improving reliability through
    database-enforced consistency
- **Chart Visualisation**: Improved dashboard charts with complete time coverage
  - Charts now display all time increments for selected range (e.g., all 24
    hours for "Today")
  - Fixed grouping logic to automatically select appropriate time granularity
  - Enhanced debugging output for troubleshooting data visualisation issues

### Technical Details

- Database triggers automatically fire on task status changes (`INSERT`,
  `UPDATE`, `DELETE` on tasks table)
- Progress calculation uses PostgreSQL aggregate functions for atomic updates
- Timezone handling leverages JavaScript's native `Intl.DateTimeFormat()` for
  accurate local conversion
- Chart time series generation creates complete axis labels even for periods
  with zero activity

## [0.3.9] – 2025-05-25

### Added

- **Startup Recovery System**: Automatic recovery for jobs interrupted by server
  restarts
  - Jobs with 'running' status and 'running' tasks are automatically detected on
    startup
  - Tasks are reset from 'running' to 'pending' and jobs are added back to
    worker pool
  - Eliminates need for manual intervention when jobs are stuck after restarts
- **Smart Link Filtering**: Enhanced crawler to extract only visible,
  user-clickable links
  - Filters out hidden elements (display:none, visibility:hidden,
    screen-reader-only)
  - Skips non-navigation links (javascript:, mailto:, empty hrefs)
  - Rejects links without visible text content (unless they have aria-labels)
  - Prevents extraction of framework-generated or accessibility-only links
- **Live Dashboard**: Real-time job monitoring dashboard with Supabase
  integration
  - Auto-refresh every 10 seconds with date range filtering
  - Smart time grouping (minute/hour/6-hour/day based on selected range)
  - Bar charts showing task completion over time with local timezone support
  - Comprehensive debugging and fallback displays for data access issues

### Fixed

- **Domain Filtering**: Improved same-domain detection to handle www prefix
  variations
  - `www.test.com` and `test.com` now correctly recognised as same domain
  - Enhanced subdomain detection works with both normalised and original domains
  - Prevents false rejection of internal links due to www prefix mismatches
- **External Link Rejection**: Strict filtering to prevent crawling external
  domains
  - All external domain links are now properly rejected with detailed logging
  - Eliminates failed crawls from external links being treated as relative URLs
  - Maintains focus on target domain while preventing scope creep
- **Database Reset**: Enhanced schema reset to handle views and dependencies
  - Properly drops views (job_list, job_dashboard) before dropping tables
  - Uses CASCADE to handle remaining dependencies automatically
  - Added comprehensive error logging and sequence cleanup

### Enhanced

- **Database Connection Resilience**: Improved connection pool settings and
  retry logic
  - Updated connection pool: MaxOpenConns (25→35), MaxIdleConns (10→15),
    MaxLifetime (5min→30min)
  - Added automatic retry logic with exponential backoff for transient
    connection failures
  - Enhanced error detection for connection-related issues (bad connection,
    unexpected parse, etc.)
- **Worker Recovery**: Enhanced task monitoring and job completion detection
  - Improved cleanup of stuck jobs where all tasks are complete but job status
    is still running
  - Better handling of stale task recovery with proper timeout detection
  - Enhanced logging throughout the recovery and monitoring processes
- **URL Normalisation**: Advanced link processing to eliminate duplicate pages
  - Automatic anchor fragment stripping (`/page#section1` → `/page`)
  - Trailing slash normalisation (`/events-news/` → `/events-news`)
  - Ensures consistent URL handling and prevents duplicate crawling of identical
    pages

### Technical Details

- Dashboard uses date-only pickers with proper timezone handling for accurate
  time grouping
- Link filtering integrates with Colly's HTML element processing for efficient
  visibility detection
- Domain comparison uses normalised hostname matching with comprehensive
  subdomain support
- Database retry logic specifically targets PostgreSQL connection issues with
  appropriate backoff strategies

## [0.3.8] – 2025-05-25

### Fixed

- **Critical Production Fix**: Resolved database schema mismatch causing task
  insertion failures
  - Fixed INSERT statement parameter count mismatch in `/internal/db/queue.go`
  - Corrected VALUES clause to match 9 fields with 9 placeholders (`$1` through
    `$9`)
  - Eliminated
    `pq: null value in column "depth" of relation "tasks" violates not-null constraint`
    error
- Fixed compilation issues in test utilities:
  - Updated `cmd/test_jobs/main.go` to use correct function signatures for
    `NewWorkerPool` and `NewJobManager`
  - Added proper `dbQueue` parameter initialisation following production code
    patterns

### Technical Details

- The production database retained the deprecated `depth` column from v0.3.6,
  but the code was updated in v0.3.7 to remove depth functionality
- Database schema reset was required to align production database with current
  code expectations
- Task queue now successfully processes jobs without depth-related constraint
  violations

## [0.3.7] – 2025-05-18

### Removed

- Removed depth functionality from the codebase:
  - Removed depth column from tasks table schema
  - Removed depth parameter from all EnqueueURLs functions
  - Updated code to not use depth in task processing
  - Modified database queries to exclude the depth field
  - Simplified code by removing unused functionality

## [0.3.6] – 2025-05-18

### Fixed

- Fixed job counter updates for `sitemap_tasks` and `found_tasks` columns:
  - Added missing functionality to update sitemap counter when sitemap URLs are
    processed
  - Implemented incrementing of found task counter for URLs discovered during
    crawling
  - Fixed duplicate processing issue by moving the page processing mark after
    successful task creation
  - Updated job query to properly return counter values
- Improved task creation reliability by ensuring pages are only marked as
  processed after successful DB operations

## [0.3.5] – 2025-05-17

### Changed

- Major code refactoring to improve architecture and maintainability:
  - Eliminated duplicate code across the codebase
  - Removed global state in favor of proper dependency injection
  - Standardised function naming conventions
  - Clarified responsibilities between packages
  - Moved database operations to a unified interface
  - Improved transaction management with DbQueue

### Removed

- Removed redundant files and functions:
  - Eliminated `jobs/db.go` (moved functions to other packages)
  - Removed `jobs/queue_helpers.go` (consolidated functionality)
  - Removed global state management with `SetDBInstance`
  - Eliminated duplicate SQL operations

## [0.3.4] – 2025-05-17

### Added

- Enhanced sitemap crawling with improved URL handling
- Added URL normalisation in sitemap processing
- Implemented robust error handling for URL processing
- Added better detection and correction of malformed URLs

### Fixed

- Fixed sitemap URL discovery and processing issues
- Improved relative URL handling in crawler
- Resolved issues with URL encoding/decoding in sitemap parser
- Fixed task queue URL processing in worker pool

### Changed

- Enhanced worker pool to better handle URL variations
- Updated job manager to properly normalise URLs before processing
- Improved URL validation logic in task processing

## [0.3.3] – 2025-04-22

### Added

- Added `sitemap_tasks` and `found_tasks` columns to the `jobs` table and
  corresponding fields in the Job struct
- Enqueued discovered links (same-domain pages and document URLs) via link
  extraction in the worker pool

### Changed

- `processTask` now filters `result.Links` to include only same-site pages and
  docs (`.pdf`, `.doc`, `.docx`) and enqueues them
- Updated `setupSchema` to include new columns with `ALTER TABLE IF NOT EXISTS`
- Exposed `Crawler.Config()` method to allow workers to read the `FindLinks`
  flag

### Documentation

- Updated `docs/architecture/jobs.md` to document new task counters and
  link-extraction behaviour

## [0.3.2] ��� 2025-04-21

### Changed

- Improved database configuration management with validation for required fields
- Enhanced worker pool notification system with more robust connection handling
- Simplified notification handling in worker pool with better error recovery
- Fixed linting issues in worker pool implementation

## [0.3.1] – 2025-04-21

### Changed

- Fixed documentation file references in INIT.md and README.md to use explicit
  relative paths
- Updated ROADMAP.md references to point to root directory instead of docs/
- Ensured consistent file linking across documentation

## [0.3.0] – 2025-04-20

### Added

- New `domains` and `pages` reference tables for improved data integrity
- Helper methods for domain and page management
- Added depth control for crawling with per-task depth configuration

### Removed

- Removed legacy `crawl_results` table and associated code.
- Removed unused functions and methods to improve code maintainability
- Eliminated deprecated code including outdated `rand.Seed` usage

### Changed

- Restructured documentation under `docs/` directory.
- Added limit to site crawl to control no of pages to crawl.
- Modified `jobs` table to reference domains by ID instead of storing domain
  names directly
- Updated `tasks` table to use page references instead of storing full URLs
- Refactored URL handling throughout the codebase to work with the new reference
  system

### Fixed

- Correctly set job and task completion timestamps (`CompletedAt`) when tasks
  and jobs complete.
- Fixed "append result never used" warnings in database operations
- Resolved unused import warnings and other code quality issues
- Fixed SQL parameter placeholders to use PostgreSQL-style numbered parameters
  (`$1`, `$2`, etc.) instead of MySQL/SQLite-style (`?`)
- Fixed task processing issues after database reset by ensuring consistent
  parameter style in all SQL queries
- Corrected parameter count mismatch in batch insert operations

## [0.2.0] - 2025-04-20

### Changed

- **Major Database Migration**: Fully migrated from SQLite/Turso to PostgreSQL
  - Removed all SQLite dependencies including
    `github.com/tursodatabase/libsql-client-go`
  - Reorganised database code structure, moving from `internal/db/postgres` to
    `internal/db`
  - Updated all application code to use PostgreSQL exclusively
  - Fixed all database-related tests

### Fixed

- Fixed crawler's `WarmURL` method to properly handle HTTP responses, context
  cancellation, and timeouts
- Resolved undefined functions and variables in test files related to the
  PostgreSQL task queue
- Implemented rate limiting functionality in the app server
- Updated all tests to work with the PostgreSQL backend
- Ensured all tests pass successfully after modifications

### Technical Debt

- Removed duplicated code from the SQLite implementation
- Cleaned up directory structure to better reflect current architecture

## [0.1.0] - 2025-04-15

### Added

- Initial project setup
- Basic crawler implementation with Colly
- Job queue system for managing crawl tasks
- Web API for submitting and monitoring crawl jobs
- SQLite database integration with Turso

### Technical Details

- Go modules for dependency management
- Internal package structure with clean separation of concerns
- Test suite for crawler and database operations
- Basic rate limiting and error handling
