-- Migration A: lock down server-internal tables and the quota view.
--
-- Background: the Supabase database linter flagged three public tables with
-- RLS disabled (`task_outbox`, `task_outbox_dead`, `lighthouse_runs`) and one
-- public view (`organisation_quota_status`) running with the creator's
-- permissions. All four are accessed exclusively by the Go server using the
-- service role; no frontend or RPC caller references them.
--
-- This migration:
--   1. Revokes anon/authenticated table grants so PostgREST cannot reach the
--      three internal tables even before RLS is consulted.
--   2. Enables RLS on each table with no policies, so any non-service-role
--      access is denied by default.
--   3. Switches `organisation_quota_status` to `security_invoker=true` so
--      the view enforces the caller's RLS instead of the creator's.

-- 1. Revoke client-role privileges on internal tables.
REVOKE ALL ON TABLE public.task_outbox FROM anon, authenticated;
REVOKE ALL ON TABLE public.task_outbox_dead FROM anon, authenticated;
REVOKE ALL ON TABLE public.lighthouse_runs FROM anon, authenticated;

-- 2. Enable RLS with no policies — service role bypasses, all other roles
-- are denied.
ALTER TABLE public.task_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.task_outbox_dead ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.lighthouse_runs ENABLE ROW LEVEL SECURITY;

-- 3. Make the quota view honour the caller's RLS rather than the creator's.
ALTER VIEW public.organisation_quota_status SET (security_invoker = true);
