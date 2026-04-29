-- WAF detection on first contact (issue #365 row 1).
--
-- The pre-flight probe and the mid-job circuit breaker write the
-- waf_blocked flag onto the domain row when a recognised bot-protection
-- layer (Cloudflare cf-mitigated, Imperva _Incapsula_Resource, DataDome
-- Server header, Akamai AkamaiGHost / akaalb_ / Server-Timing ak_p) is
-- detected. The next CreateJob for the same domain reads the cached
-- flag synchronously and skips both the live probe and the discovery
-- goroutine.
--
-- waf_blocked_at gives the application layer the information it needs
-- to expire the cache (default 24 h) and re-probe; without it a
-- once-blocked domain would never be retested even if the site owner
-- allowlisted Hover.
--
-- The partial index targets the cache-read path (domains where the
-- flag is set); healthy domains never touch the index.

ALTER TABLE public.domains
  ADD COLUMN IF NOT EXISTS waf_blocked BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS waf_vendor TEXT,
  ADD COLUMN IF NOT EXISTS waf_blocked_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_domains_waf_blocked
  ON public.domains(waf_blocked)
  WHERE waf_blocked = TRUE;

-- The statement-level counter trigger from migration 20260426013451 hard-
-- codes ('cancelled', 'failed') as the terminal-status set it must not
-- overwrite when a late task completion fires. 'blocked' is a third
-- terminal state with the same constraint: BlockJob writes
-- jobs.status = 'blocked' inside the same transaction that flips the
-- last in-flight tasks to 'skipped', and the trigger must preserve it
-- against a same-batch completion race. Re-create the function with
-- 'blocked' added to the preserved set.

CREATE OR REPLACE FUNCTION update_job_counters_status_batch()
RETURNS TRIGGER LANGUAGE plpgsql SET search_path = public AS $$
BEGIN
    WITH per_row AS (
        SELECT
            o.job_id                                   AS job_id,
            CASE WHEN o.status = 'completed' THEN -1 ELSE 0 END AS completed_delta,
            CASE WHEN o.status = 'failed'    THEN -1 ELSE 0 END AS failed_delta,
            CASE WHEN o.status = 'skipped'   THEN -1 ELSE 0 END AS skipped_delta,
            CASE WHEN o.status = 'pending'   THEN -1 ELSE 0 END AS pending_delta,
            CASE WHEN o.status = 'waiting'   THEN -1 ELSE 0 END AS waiting_delta,
            FALSE                                       AS running_started
        FROM old_tasks o
        JOIN new_tasks n ON n.id = o.id
        WHERE o.status IS DISTINCT FROM n.status
           OR o.job_id IS DISTINCT FROM n.job_id

        UNION ALL

        SELECT
            n.job_id                                   AS job_id,
            CASE WHEN n.status = 'completed' THEN  1 ELSE 0 END AS completed_delta,
            CASE WHEN n.status = 'failed'    THEN  1 ELSE 0 END AS failed_delta,
            CASE WHEN n.status = 'skipped'   THEN  1 ELSE 0 END AS skipped_delta,
            CASE WHEN n.status = 'pending'   THEN  1 ELSE 0 END AS pending_delta,
            CASE WHEN n.status = 'waiting'   THEN  1 ELSE 0 END AS waiting_delta,
            (n.status = 'running')                       AS running_started
        FROM new_tasks n
        JOIN old_tasks o ON o.id = n.id
        WHERE o.status IS DISTINCT FROM n.status
           OR o.job_id IS DISTINCT FROM n.job_id
    ),
    deltas AS (
        SELECT
            job_id,
            SUM(completed_delta)::INT  AS completed_delta,
            SUM(failed_delta)::INT     AS failed_delta,
            SUM(skipped_delta)::INT    AS skipped_delta,
            SUM(pending_delta)::INT    AS pending_delta,
            SUM(waiting_delta)::INT    AS waiting_delta,
            BOOL_OR(running_started)   AS running_started
        FROM per_row
        GROUP BY job_id
    ),
    _locked AS (
        SELECT j.id
        FROM jobs j
        JOIN deltas d ON d.job_id = j.id
        ORDER BY j.id
        FOR UPDATE OF j
    )
    UPDATE jobs j
    SET
        completed_tasks = GREATEST(0, j.completed_tasks + d.completed_delta),
        failed_tasks    = GREATEST(0, j.failed_tasks    + d.failed_delta),
        skipped_tasks   = GREATEST(0, j.skipped_tasks   + d.skipped_delta),
        pending_tasks   = GREATEST(0, j.pending_tasks   + d.pending_delta),
        waiting_tasks   = GREATEST(0, j.waiting_tasks   + d.waiting_delta),
        progress = CASE
            WHEN j.total_tasks > 0
             AND (j.total_tasks - GREATEST(0, j.skipped_tasks + d.skipped_delta)) > 0
            THEN (
                (GREATEST(0, j.completed_tasks + d.completed_delta)
                 + GREATEST(0, j.failed_tasks  + d.failed_delta))::REAL
                / (j.total_tasks - GREATEST(0, j.skipped_tasks + d.skipped_delta))::REAL
            ) * 100.0
            ELSE 0.0
        END,
        started_at = CASE
            WHEN j.started_at IS NULL AND d.running_started THEN NOW()
            ELSE j.started_at
        END,
        completed_at = CASE
            WHEN j.completed_at IS NULL
             AND j.total_tasks > 0
             AND (
                 GREATEST(0, j.completed_tasks + d.completed_delta)
                 + GREATEST(0, j.failed_tasks  + d.failed_delta)
                 + GREATEST(0, j.skipped_tasks + d.skipped_delta)
             ) >= j.total_tasks
            THEN NOW()
            ELSE j.completed_at
        END,
        status = CASE
            -- 'blocked' joins ('cancelled', 'failed') as a terminal status
            -- the trigger must not overwrite when a same-batch task
            -- completion would otherwise mark the job 'completed'.
            WHEN j.status IN ('cancelled', 'failed', 'blocked') THEN j.status
            WHEN j.total_tasks > 0
             AND (
                 GREATEST(0, j.completed_tasks + d.completed_delta)
                 + GREATEST(0, j.failed_tasks  + d.failed_delta)
                 + GREATEST(0, j.skipped_tasks + d.skipped_delta)
             ) >= j.total_tasks
            THEN 'completed'
            ELSE j.status
        END
    FROM deltas d
    WHERE j.id = d.job_id
      AND j.id IN (SELECT id FROM _locked);

    RETURN NULL;
END;
$$;
