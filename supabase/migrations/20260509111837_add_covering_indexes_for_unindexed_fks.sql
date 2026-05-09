-- Migration D: add covering indexes for foreign keys flagged by the
-- Supabase database linter (`unindexed_foreign_keys`).
--
-- Without a covering index, Postgres must perform a sequential scan on the
-- referencing table whenever the parent row is updated/deleted (FK validation)
-- or whenever a join filters on the FK column. All nine indexes are additive
-- and guarded with `IF NOT EXISTS` to remain idempotent across replays.
-- `CONCURRENTLY` is intentionally omitted — Supabase's branch migration runner
-- pipelines statements over the extended protocol, which Postgres forbids
-- `CREATE INDEX CONCURRENTLY` inside. These tables are small and there are no
-- live customers yet, so the brief exclusive lock during a plain `CREATE
-- INDEX` is acceptable. Switch to `CONCURRENTLY` (in a separate, non-pipelined
-- migration) if any of these tables grow before public launch.

CREATE INDEX IF NOT EXISTS idx_ga_accounts_installing_user_id
    ON public.google_analytics_accounts(installing_user_id);

CREATE INDEX IF NOT EXISTS idx_ga_connections_installing_user_id
    ON public.google_analytics_connections(installing_user_id);

CREATE INDEX IF NOT EXISTS idx_lighthouse_runs_source_task_id
    ON public.lighthouse_runs(source_task_id);

CREATE INDEX IF NOT EXISTS idx_organisation_invites_created_by
    ON public.organisation_invites(created_by);

CREATE INDEX IF NOT EXISTS idx_page_analytics_ga_connection_id
    ON public.page_analytics(ga_connection_id);

CREATE INDEX IF NOT EXISTS idx_platform_org_mappings_created_by
    ON public.platform_org_mappings(created_by);

CREATE INDEX IF NOT EXISTS idx_slack_connections_installing_user_id
    ON public.slack_connections(installing_user_id);

CREATE INDEX IF NOT EXISTS idx_task_outbox_dead_lighthouse_run_id
    ON public.task_outbox_dead(lighthouse_run_id);

CREATE INDEX IF NOT EXISTS idx_webflow_connections_installing_user_id
    ON public.webflow_connections(installing_user_id);
