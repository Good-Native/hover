-- Drop the legacy `task-html` Supabase Storage bucket.
--
-- Page HTML has been written direct to Cloudflare R2 since 2026-04-25
-- (see CHANGELOG entry "All page HTML storage now uses ..." and the
-- `## 3. Recommended Approach` section of
-- `docs/plans/page-content-storage-plan.md`). No Go code path references
-- the `task-html` bucket; it has been holding stale objects from the
-- four-week window when it was the hot store.
--
-- Operational precondition: the bucket must be emptied via the Supabase
-- Storage dashboard or API before this migration is applied. The foreign
-- key from `storage.objects` to `storage.buckets` will block the DELETE
-- below if any objects remain, which is the intended safety net.

DROP POLICY IF EXISTS "Service role can manage task html" ON storage.objects;

DELETE FROM storage.buckets WHERE id = 'task-html';
