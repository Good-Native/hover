-- Migration C: performance and hygiene cleanups flagged by the Supabase
-- database linter.
--
-- Background:
--   * `auth_rls_initplan` warnings flag RLS policies that call `auth.uid()`
--     in a way that re-evaluates the function for every row. Wrapping the
--     call in `(select auth.uid())` lets Postgres evaluate it once per
--     query and cache the result.
--   * `multiple_permissive_policies` flags overlapping SELECT policies on
--     `daily_usage`. Scoping the service-role policy `TO service_role`
--     stops it from firing during anon/authenticated evaluations.
--   * `function_search_path_mutable` flags two functions whose
--     `search_path` is not pinned, which is a minor hardening risk.
--
-- All policy rewrites preserve the original USING / WITH CHECK semantics
-- exactly — they only swap `auth.uid()` for `(select auth.uid())`.

-- =============================================================================
-- 1. notifications
-- =============================================================================
DROP POLICY IF EXISTS notifications_select_own_org ON public.notifications;
CREATE POLICY notifications_select_own_org ON public.notifications
    FOR SELECT
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

-- =============================================================================
-- 2. daily_usage — rewrite both policies and scope the service-role one to
--    service_role so it no longer fires during anon/authenticated evaluations.
-- =============================================================================
DROP POLICY IF EXISTS "Service role can manage usage" ON public.daily_usage;
CREATE POLICY "Service role can manage usage" ON public.daily_usage
    FOR ALL
    TO service_role
    USING (true)
    WITH CHECK (true);

DROP POLICY IF EXISTS "Users can view their organisation usage" ON public.daily_usage;
CREATE POLICY "Users can view their organisation usage" ON public.daily_usage
    FOR SELECT
    USING (
        organisation_id IN (
            SELECT om.organisation_id
            FROM organisation_members om
            WHERE om.user_id = (SELECT auth.uid())
        )
    );

-- =============================================================================
-- 3. google_analytics_connections
-- =============================================================================
DROP POLICY IF EXISTS ga_connections_select_own_org ON public.google_analytics_connections;
CREATE POLICY ga_connections_select_own_org ON public.google_analytics_connections
    FOR SELECT
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

DROP POLICY IF EXISTS ga_connections_insert_own_org ON public.google_analytics_connections;
CREATE POLICY ga_connections_insert_own_org ON public.google_analytics_connections
    FOR INSERT
    WITH CHECK (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

DROP POLICY IF EXISTS ga_connections_update_own_org ON public.google_analytics_connections;
CREATE POLICY ga_connections_update_own_org ON public.google_analytics_connections
    FOR UPDATE
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

DROP POLICY IF EXISTS ga_connections_delete_own_org ON public.google_analytics_connections;
CREATE POLICY ga_connections_delete_own_org ON public.google_analytics_connections
    FOR DELETE
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

-- =============================================================================
-- 4. google_analytics_accounts
-- =============================================================================
DROP POLICY IF EXISTS ga_accounts_select_own_org ON public.google_analytics_accounts;
CREATE POLICY ga_accounts_select_own_org ON public.google_analytics_accounts
    FOR SELECT
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

DROP POLICY IF EXISTS ga_accounts_insert_own_org ON public.google_analytics_accounts;
CREATE POLICY ga_accounts_insert_own_org ON public.google_analytics_accounts
    FOR INSERT
    WITH CHECK (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

DROP POLICY IF EXISTS ga_accounts_update_own_org ON public.google_analytics_accounts;
CREATE POLICY ga_accounts_update_own_org ON public.google_analytics_accounts
    FOR UPDATE
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

DROP POLICY IF EXISTS ga_accounts_delete_own_org ON public.google_analytics_accounts;
CREATE POLICY ga_accounts_delete_own_org ON public.google_analytics_accounts
    FOR DELETE
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

-- =============================================================================
-- 5. organisation_domains
-- =============================================================================
DROP POLICY IF EXISTS org_domains_select_own_org ON public.organisation_domains;
CREATE POLICY org_domains_select_own_org ON public.organisation_domains
    FOR SELECT
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

DROP POLICY IF EXISTS org_domains_insert_own_org ON public.organisation_domains;
CREATE POLICY org_domains_insert_own_org ON public.organisation_domains
    FOR INSERT
    WITH CHECK (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

DROP POLICY IF EXISTS org_domains_delete_own_org ON public.organisation_domains;
CREATE POLICY org_domains_delete_own_org ON public.organisation_domains
    FOR DELETE
    USING (
        organisation_id IN (
            SELECT users.organisation_id
            FROM users
            WHERE users.id = (SELECT auth.uid())
        )
    );

-- =============================================================================
-- 6. Pin search_path on the two flagged functions.
-- =============================================================================
ALTER FUNCTION public.update_job_queue_counters() SET search_path = pg_catalog, public;
ALTER FUNCTION public.get_daily_quota_remaining(uuid) SET search_path = pg_catalog, public;
