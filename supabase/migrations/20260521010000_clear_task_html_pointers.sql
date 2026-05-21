-- Clear dangling `task-html` pointers on the `tasks` table.
--
-- Background: between 2026-03-21 and 2026-04-25, completed crawl tasks had
-- their `html_storage_bucket` / `html_storage_path` columns populated with
-- pointers into the Supabase Storage `task-html` bucket. Since 2026-04-25,
-- the same columns are populated with Cloudflare R2 pointers
-- (`html_storage_bucket = ARCHIVE_BUCKET`).
--
-- The `task-html` bucket is being dropped in the accompanying migration. Rows
-- whose pointers still reference `task-html` would otherwise point at
-- destroyed objects. This migration NULLs those two columns on the affected
-- rows so the dangling references are not mistaken for valid retrievals.
--
-- Retained on those rows for historical analysis: `html_content_type`,
-- `html_content_encoding`, `html_size_bytes`, `html_compressed_size_bytes`,
-- `html_sha256`, `html_captured_at`. These describe the original response
-- and remain factually correct even without the stored body.

UPDATE tasks
SET html_storage_bucket = NULL,
    html_storage_path   = NULL
WHERE html_storage_bucket = 'task-html';
