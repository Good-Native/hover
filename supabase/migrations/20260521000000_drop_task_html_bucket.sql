-- Drop the service-role RLS policy attached to the legacy `task-html`
-- Supabase Storage bucket.
--
-- Page HTML has been written directly to Cloudflare R2 since 2026-04-25
-- (see the `## 3. Recommended Approach` section of
-- `docs/plans/page-content-storage-plan.md`). No Go code path references
-- the `task-html` bucket.
--
-- Scope of this migration: only the policy is dropped here. Removing the
-- bucket row itself (`storage.buckets`) cannot be done via SQL — Supabase
-- enforces "Direct deletion from storage tables is not allowed. Use the
-- Storage API instead." (SQLSTATE 42501). The bucket must therefore be
-- emptied and then deleted via the Supabase Storage dashboard or API as
-- a manual operational step.

DROP POLICY IF EXISTS "Service role can manage task html" ON storage.objects;
