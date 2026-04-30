# Crawl handling — case table

A flat list of "what does Hover do when X happens?" Each row is a case (a domain
or page condition) and the action(s) we take on the **job** and on the **task**
that surfaced the case.

Optimised for skim-reading and incremental growth: when a new case becomes
interesting, add a row. Don't expand the conceptual prose unless multiple rows
need the same explanation.

For state machine internals (full transition rules, lock ordering, trigger
semantics) see [`internal/jobs/manager.go`](../../internal/jobs/manager.go)
`ValidateStatusTransition` — that's the single source of truth.

---

## Domain-level cases

Decided at job creation, pre-flight probe, or mid-crawl via the circuit breaker.

| Case                                  | How detected                                                                                                                          | Action on job                                                                                                                                                                                           | Action on tasks                                                                                                                                | Source                                                   |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------- |
| **Healthy domain**                    | Pre-flight `/` returns 200, no WAF fingerprint                                                                                        | Status proceeds: `pending` → `initialising` → `running`                                                                                                                                                 | Discovery enqueues normally                                                                                                                    | `internal/jobs/manager.go` `setupJobURLDiscovery`        |
| **WAF wall (immediate)**              | Pre-flight `/` returns recognised fingerprint (Cloudflare/Imperva/DataDome/Akamai)                                                    | Status → `blocked`, `error_message` carries vendor + reason                                                                                                                                             | Zero enqueued                                                                                                                                  | `internal/crawler/waf.go`, `BlockJob`                    |
| **WAF wall (cached)**                 | `domains.waf_blocked = true` AND `waf_blocked_at` within 24 h                                                                         | Status → `blocked` set in same tx as job INSERT (synchronous, no probe)                                                                                                                                 | Zero enqueued                                                                                                                                  | `setupJobDatabase` cache-hit branch                      |
| **WAF wall (mid-crawl)**              | 2 consecutive task responses with WAF fingerprints (configurable: `GNH_WAF_CIRCUIT_BREAKER_THRESHOLD`)                                | Status → `blocked` via async `BlockJob`; `domains.waf_blocked = true`, `waf_vendor` set                                                                                                                 | Pending/waiting → `skipped`; in-flight finish naturally; sitemap loop aborts at next batch boundary; further `EnqueueURLs` calls become no-ops | `internal/jobs/waf_circuit_breaker.go`                   |
| **Robots.txt disallows `/`**          | `validateRootURLAccess` finds `Disallow: /` matches our UA section                                                                    | Status → `failed`, `error_message = "Root path (/) is disallowed by robots.txt"`                                                                                                                        | Zero enqueued                                                                                                                                  | `validateRootURLAccess`                                  |
| **Robots.txt unreachable**            | `DiscoverSitemapsAndRobots` returns network error                                                                                     | Status → `failed`, `error_message = "Failed to fetch robots.txt: <err>"`                                                                                                                                | Zero enqueued                                                                                                                                  | `validateRootURLAccess`                                  |
| **Robots.txt with crawl-delay**       | `Crawl-delay: <n>` in our UA section                                                                                                  | Stamped on `domains.crawl_delay_seconds`                                                                                                                                                                | All tasks paced through `DomainPacer` honouring delay                                                                                          | `internal/crawler/robots.go`, `internal/broker/pacer.go` |
| **Existing active job**               | Same domain + org has a job in `pending`/`initialising`/`running`/`paused`                                                            | New job creation cancels the existing one first, then proceeds                                                                                                                                          | Existing job's pending/waiting tasks → `skipped`; outbox cleared                                                                               | `handleExistingJobs` → `CancelJob`                       |
| **Domain unreachable (DNS/TCP/TLS)**  | Probe network error                                                                                                                   | Probe error logged as warning, **discovery proceeds** as if probe passed (transient errors must not block legitimate jobs); subsequent task failures will surface via the circuit breaker if persistent | Discovery proceeds; per-task failures classified as transient and retried                                                                      | `runWAFPreflight`                                        |
| **User cancellation**                 | `POST /v1/jobs/:id/cancel`                                                                                                            | Status → `cancelled`; `error_message` cleared/preserved                                                                                                                                                 | Pending/waiting → `skipped`; outbox cleared; in-flight finish naturally                                                                        | `CancelJob`                                              |
| **Quota exhausted (org daily limit)** | `get_daily_quota_remaining` returns 0 inside `EnqueueURLs`                                                                            | Job not transitioned (continues to drain existing tasks); new URLs not enqueued                                                                                                                         | Already-pending tasks proceed; new URLs from sitemap/links are dropped (or shifted to `waiting` if concurrency-limited)                        | `internal/db/queue.go` `EnqueueURLs`                     |
| **Job already terminal (any reason)** | `EnqueueURLs` reads `jobs.status` under `FOR UPDATE OF j` and finds it in {`completed`, `failed`, `cancelled`, `archived`, `blocked`} | No status change                                                                                                                                                                                        | Insert is a no-op; sitemap loop also short-circuits at next batch via `isJobInTerminalStatus`                                                  | `internal/db/queue.go` terminal-status guard             |

---

## Page-level cases

Decided per-task by the worker after the HTTP response (or error) returns. Row
order is roughly response-frequency.

| Case                                       | How detected                                                                       | Task action                                                                                 | Job-level effect                                                                                | Notes                                                                       |
| ------------------------------------------ | ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| **2xx OK with content**                    | Status 200–299 + non-empty body                                                    | Status → `completed`, response stored, links extracted if `find_links` enabled              | Counter increments                                                                              | Happy path                                                                  |
| **2xx with empty / SPA shell**             | 200, body present but `<p>` and `<a>` counts ~0                                    | Status → `completed` (currently treated as success)                                         | Counter increments                                                                              | **Limitation**: data-quality miss, not a wasted resource. Issue #365 row 5. |
| **3xx redirect (same registrable domain)** | 301/302/303/307/308 to same eTLD+1                                                 | Followed up to redirect cap; final response classified                                      | Counter increments on final step                                                                | `validateCrawlRequest`                                                      |
| **3xx redirect (cross-domain)**            | 3xx pointing off-site                                                              | Status → `completed` with `redirect_url` recorded; new URL **not** enqueued unless explicit | Counter increments                                                                              | Subdomain-cross-link policy is per-job                                      |
| **404 Not Found**                          | Status 404                                                                         | Status → `failed`, `error_message = "non-success status code: 404"`                         | Counter increments (failed)                                                                     | No retry — terminal class                                                   |
| **410 Gone**                               | Status 410                                                                         | Status → `failed`                                                                           | Counter increments (failed)                                                                     | Treated like 404 currently                                                  |
| **403 with WAF fingerprint**               | 403 + recognised vendor signal (Cloudflare/Imperva/DataDome/Akamai)                | Status → `failed`, marked rate-limited, `result.WAF` populated                              | Counter increments; **circuit breaker observes** — N consecutive trips → `BlockJob`             | See domain-level "WAF wall (mid-crawl)"                                     |
| **403 without WAF fingerprint**            | 403, no recognised signal, body ≥ 500 B                                            | Status → `failed`, marked rate-limited, retried with backoff up to `MaxBlockingRetries`     | Counter increments                                                                              | Could be auth gate, geo-fence, or unrecognised WAF                          |
| **403 with tiny body**                     | 403, body 1–499 B                                                                  | `result.WAF` set to generic vendor; same retry/breaker handling as above                    | Counter increments                                                                              | Catches unfingerprinted WAF blocks                                          |
| **429 Too Many Requests**                  | Status 429                                                                         | Status → `failed`, marked rate-limited; honour `Retry-After` if present; retry with backoff | Counter increments (failed); **`DomainPacer` adapts**, slowing future dispatches for the domain | `internal/broker/pacer.go`                                                  |
| **5xx server error**                       | Status 500–599 (excl. 503 with rate-limit signal)                                  | Status → `failed`, retried with backoff                                                     | Counter increments (failed)                                                                     | Transient class — keeps retrying within budget                              |
| **Timeout (TTFB)**                         | No first byte within timeout                                                       | Status → `failed`, transient class, retried                                                 | Counter increments (failed)                                                                     |                                                                             |
| **Timeout (body read)**                    | First byte received but read stalls                                                | Status → `failed`, transient class, retried                                                 | Counter increments (failed)                                                                     |                                                                             |
| **TLS / certificate error**                | Handshake failure                                                                  | Status → `failed`, terminal class, NOT retried                                              | Counter increments (failed)                                                                     | Repeat retries won't fix a cert problem                                     |
| **DNS / TCP error**                        | Connect-time failure                                                               | Status → `failed`, transient class, retried                                                 | Counter increments (failed)                                                                     |                                                                             |
| **Robots.txt disallows path**              | `IsPathAllowed` returns false at discovery                                         | URL **not enqueued** as a task                                                              | No counter change                                                                               | Filtered at link-discovery / sitemap-parse, never becomes a task row        |
| **Cross-subdomain link discovered**        | Link extracted from page points to a sibling subdomain                             | Enqueued only if `allow_cross_subdomain_links` is set on the job                            | If allowed, counter increments via `found_tasks`                                                | Per-job toggle                                                              |
| **Discovered link already crawled**        | `processedPages` map already records the page ID                                   | Skipped at discovery — no DB round-trip                                                     | No counter change                                                                               | In-process dedupe; survives across batches within a job                     |
| **Discovered link exceeds `max_pages`**    | `currentTaskCount + pendingCount + waitingCount >= max_pages`                      | URL **not enqueued** (overflow) — does NOT materialise as a `skipped` task row              | `total_tasks` doesn't grow; dashboard counts stay accurate                                      | `classifyEnqueuedTask` overflow path                                        |
| **Stale running task (worker crash)**      | Task in `running` state past `TaskStaleTimeout` (3 min) without a worker heartbeat | Reclaimed by `XAUTOCLAIM`, redelivered, retry counter incremented                           | If retry budget exhausted → dead-lettered as `failed`                                           | `reclaimStaleMessages`                                                      |

---

## Job statuses (reference)

Defined in [`internal/jobs/types.go`](../../internal/jobs/types.go).

| Status         | Meaning                                                            | Terminal?              |
| -------------- | ------------------------------------------------------------------ | ---------------------- |
| `pending`      | Created, no tasks yet                                              | No                     |
| `initialising` | Discovery in progress (sitemap parsing)                            | No                     |
| `running`      | Tasks dispatching                                                  | No                     |
| `paused`       | Manually paused; tasks not dispatching                             | No                     |
| `completed`    | All tasks reached terminal state, total > 0                        | Yes                    |
| `failed`       | System / discovery error before/during execution                   | Yes                    |
| `cancelled`    | User cancellation (or auto-cancel from a duplicate-domain new job) | Yes                    |
| `blocked`      | WAF detected (pre-flight or mid-crawl); not retried automatically  | Yes                    |
| `archived`     | Soft-deleted from active views                                     | Yes (already terminal) |

Allowed transitions are enforced by `ValidateStatusTransition`. Terminal jobs
can be restarted by transitioning back to `running` (e.g. customer manually
retries a `blocked` job after requesting an allowlist from the site owner).

---

## Task statuses (reference)

Defined in [`internal/jobs/types.go`](../../internal/jobs/types.go).

| Status      | Meaning                                                              |
| ----------- | -------------------------------------------------------------------- |
| `waiting`   | Concurrency-limited; will be promoted to `pending` when a slot opens |
| `pending`   | Ready for dispatch                                                   |
| `running`   | Picked up by a worker, in flight                                     |
| `completed` | Finished successfully (any 2xx)                                      |
| `failed`    | Finished with error (4xx/5xx/timeout/etc.) — may have retried first  |
| `skipped`   | Cancelled or blocked before execution                                |

---

## Domains table (reference)

Schema in [`docs/architecture/DATABASE.md`](DATABASE.md) and the migrations
under [`supabase/migrations/`](../../supabase/migrations/).

| Column                                      | Set by                                         | Used by                                         |
| ------------------------------------------- | ---------------------------------------------- | ----------------------------------------------- |
| `crawl_delay_seconds`                       | `validateRootURLAccess` reading `Crawl-delay:` | `DomainPacer` for per-domain throttling         |
| `adaptive_delay_seconds` / `_floor_seconds` | Pacer's adaptive backoff on 429s               | Pacer dispatch decisions                        |
| `waf_blocked`                               | `BlockJob` (pre-flight or circuit breaker)     | `setupJobDatabase` cached-flag fast path        |
| `waf_vendor`                                | `BlockJob`                                     | Customer-facing reason on subsequent jobs       |
| `waf_blocked_at`                            | `BlockJob`                                     | Cache TTL (24 h) — older verdicts are re-probed |

---

## Adding a new case

1. Add a row to the appropriate table above.
2. If it changes job/task status semantics, also update
   `ValidateStatusTransition` and the relevant counter trigger in
   `supabase/migrations/`.
3. If it's a per-task outcome class, the natural home for the classification is
   `internal/jobs/executor.go` `buildErrorOutcome` or `buildSuccessOutcome`.
4. Add a test in the package that owns the policy (`internal/jobs/*_test.go` for
   status transitions, `internal/crawler/*_test.go` for response
   classification).
