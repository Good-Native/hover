-- Migration B: revoke anon/authenticated EXECUTE on server-internal
-- SECURITY DEFINER functions.
--
-- Background: the Supabase database linter flagged 22 SECURITY DEFINER
-- functions in the public schema as callable by `anon` and `authenticated`
-- via PostgREST's /rpc endpoint. Audit results:
--
--   * Token store/get/delete functions for GA, Slack, and Webflow are only
--     called from the Go server using the service role
--     (internal/db/google_connections.go, slack.go, webflow_connections.go).
--   * `increment_daily_usage` is called from internal/db/batch.go.
--   * `cleanup_*_vault_secret`, `auto_link_slack_user`,
--     `auto_link_existing_slack_users`, and `sync_slack_user_id` have no
--     callers in the application code at all.
--
-- The Go server uses the service role, which bypasses EXECUTE grants, so
-- revoking from `anon`/`authenticated` removes the public attack surface
-- without affecting the server.
--
-- Three SECURITY DEFINER helpers are intentionally **left alone** because
-- they are invoked inside RLS policies and need to remain callable by the
-- `authenticated` role for those policies to evaluate correctly:
--   * public.user_is_member_of(uuid)
--   * public.user_organisation_id()
--   * public.user_organisations()

-- Token store/get/delete (GA connections).
REVOKE EXECUTE ON FUNCTION public.store_ga_token(uuid, text) FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.get_ga_token(uuid) FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.delete_ga_token(uuid) FROM anon, authenticated;

-- Token store/get/delete (GA accounts).
REVOKE EXECUTE ON FUNCTION public.store_ga_account_token(uuid, text) FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.get_ga_account_token(uuid) FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.delete_ga_account_token(uuid) FROM anon, authenticated;

-- Token store/get/delete (Slack).
REVOKE EXECUTE ON FUNCTION public.store_slack_token(uuid, text) FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.get_slack_token(uuid) FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.delete_slack_token(uuid) FROM anon, authenticated;

-- Token store/get/delete (Webflow).
REVOKE EXECUTE ON FUNCTION public.store_webflow_token(uuid, text) FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.get_webflow_token(uuid) FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.delete_webflow_token(uuid) FROM anon, authenticated;

-- Vault cleanup helpers (no application callers; service-role only).
REVOKE EXECUTE ON FUNCTION public.cleanup_ga_vault_secret() FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.cleanup_slack_vault_secret() FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.cleanup_webflow_vault_secret() FROM anon, authenticated;

-- Slack user-link helpers (no application callers; invoked by triggers /
-- service-role admin scripts only).
REVOKE EXECUTE ON FUNCTION public.auto_link_slack_user() FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.auto_link_existing_slack_users() FROM anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.sync_slack_user_id() FROM anon, authenticated;

-- Daily usage counter (server-side batch writer only).
REVOKE EXECUTE ON FUNCTION public.increment_daily_usage(uuid, integer) FROM anon, authenticated;
