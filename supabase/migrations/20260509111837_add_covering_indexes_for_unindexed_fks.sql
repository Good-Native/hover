-- Migration D: add covering indexes for foreign keys flagged by the
-- Supabase database linter (`unindexed_foreign_keys`).
--
-- Without a covering index, Postgres must perform a sequential scan on the
-- referencing table whenever the parent row is updated/deleted (FK validation)
-- or whenever a join filters on the FK column. All nine indexes are additive,
-- created CONCURRENTLY so they don't block writes, and guarded with
-- `IF NOT EXISTS` to remain idempotent across replays.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ga_accounts_installing_user_id
    ON public.google_analytics_accounts(installing_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ga_connections_installing_user_id
    ON public.google_analytics_connections(installing_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_lighthouse_runs_source_task_id
    ON public.lighthouse_runs(source_task_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_organisation_invites_created_by
    ON public.organisation_invites(created_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_page_analytics_ga_connection_id
    ON public.page_analytics(ga_connection_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_platform_org_mappings_created_by
    ON public.platform_org_mappings(created_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_slack_connections_installing_user_id
    ON public.slack_connections(installing_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_outbox_dead_lighthouse_run_id
    ON public.task_outbox_dead(lighthouse_run_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webflow_connections_installing_user_id
    ON public.webflow_connections(installing_user_id);
