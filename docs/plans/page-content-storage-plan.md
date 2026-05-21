# Page Content Storage Plan

> **Update — 2026-04-25:** the canonical write path is now **direct to
> Cloudflare R2** (issue #332). Supabase Storage is no longer involved. The
> sections below preserve the original design rationale;
> `## 3. Recommended Approach` and `## 6. Task Metadata` reflect the current
> implementation.

This document outlines how to store per-task HTML page content in object storage
rather than Postgres, so each crawl run can retain the fetched page body for
later inspection.

## 1. Goal

- Store the fetched HTML body for each completed crawl task.
- Keep page content out of Postgres.
- Reuse Supabase Storage as the cheaper content store.
- Preserve enough metadata on the task row to find and retrieve the stored file.

## 2. Scope

In scope:

- HTML/XHTML page bodies fetched by the crawler
- one canonical stored object per task id
- task-level metadata pointing to the stored object
- gzip compression before upload

Out of scope:

- images, CSS, JS, PDFs, or other external assets
- full asset mirroring
- retention rules and archival policy
- frontend/UI work

## 3. Recommended Approach

Store page bodies in a private Cloudflare R2 bucket (S3-compatible) and keep
only metadata on `tasks`. R2 was previously the cold-storage tier behind a sweep
job; with the direct-to-R2 path it is now the hot store too. A future
deep-cold-storage tier (provider TBD) will eventually replace the
`html_archive_*` columns the archive scheduler still references.

Current default:

- bucket: configured via `ARCHIVE_BUCKET` (production: `native-hover-archive`)
- provider: `ARCHIVE_PROVIDER=r2`
- object path: `jobs/{job_id}/tasks/{task_id}/page-content.html.gz`
- always upload gzip-compressed HTML
- one PUT per task; no Supabase Storage hop
- metadata columns on `tasks` populated in a single batched UPDATE per persister
  flush, with `html_archived_at` stamped at write time so the archive sweep
  skips the row

## 4. Why Storage Instead Of Postgres

- HTML bodies are the large payloads in this workflow.
- Supabase Storage is a better fit for large opaque blobs.
- Postgres stays focused on queryable metadata.
- Signed URLs can provide manual inspection later without exposing the bucket.

## 5. Compression Expectations

HTML compresses very well.

Typical gzip reduction for HTML:

- simple/clean HTML: `60-75%` smaller
- script/style-heavy HTML: `70-85%` smaller
- planning average: `~75%` smaller

Rough example:

- `200 KB` raw HTML -> `40-80 KB` gzip
- `500 KB` raw HTML -> `90-180 KB` gzip

This means gzip is likely worth doing before upload if the goal is to observe
real storage scale efficiently.

## 6. Task Metadata To Store

Task-level columns populated by `BatchUpsertTaskHTMLMetadata`:

- `html_storage_bucket TEXT` — the R2 bucket name (matches `ARCHIVE_BUCKET`)
- `html_storage_path TEXT`
- `html_content_type TEXT`
- `html_size_bytes BIGINT`
- `html_compressed_size_bytes BIGINT`
- `html_content_encoding TEXT`
- `html_captured_at TIMESTAMPTZ`
- `html_sha256 TEXT`
- `html_archived_at TIMESTAMPTZ` — stamped equal to `html_captured_at` so the
  archive sweep candidate query (`html_archived_at IS NULL`) treats the row as
  already cold-stored

The `html_archive_*` columns remain on the schema but are unused by the
direct-to-R2 path. They are reserved for the future deep-cold-storage tier.

These columns allow later retrieval, rough size analysis, and content change
tracking without querying Storage directly.

## 7. Upload Timing

Queue HTML persistence from the worker success path after the crawl result is
available, then let a bounded background worker handle upload and metadata
storage.

Suggested flow:

1. crawler fetches page and populates `result.Body`
2. worker verifies response is HTML/XHTML and body is non-empty
3. worker queues an HTML persistence request after task success handling
4. background worker gzip-compresses the body and uploads to Supabase Storage
5. background worker persists object metadata on the completed task row

If upload fails:

- task should still complete normally
- log the upload failure
- leave storage metadata empty

## 8. Object Naming

Recommended path shape:

- `jobs/{job_id}/tasks/page-path/{task_id}.html.gz`

Benefits:

- deterministic path
- easy per-job cleanup later
- no timestamp needed if each task has one canonical stored body

Alternative if multiple versions per task are needed later:

- `jobs/{job_id}/tasks/page-path/{task_id}/{unix_ts}.html.gz`

## 9. Content Eligibility Rules

Store content only when:

- response body is non-empty
- content type is HTML or XHTML page content
- task completed successfully enough to produce a body

Skip storage when:

- response is binary or clearly not page content
- body is empty
- upload client is not configured

## 10. Implementation Touchpoints

### Migration

Add task metadata columns.

File:

- `supabase/migrations/<new_migration>.sql`

### Task model

Extend the DB task struct with new storage metadata fields.

Files:

- `internal/db/queue.go`
- `internal/db/batch.go`

### Worker persistence

Add a helper that compresses and uploads page content, then stores returned
metadata on the task.

File:

- `internal/jobs/worker.go`

### Storage client

Reuse `internal/storage/client.go` and optionally extend it with a
metadata-aware upload helper if needed.

### Crawler result

No major crawler schema change is required because `result.Body` already exists.

Relevant current behaviour:

- full body already exists in `CrawlResult.Body`
- storage uploads already exist for domain-level tech HTML samples

## 11. Suggested Bucket Setup

> **Historical — superseded.** The `task-html` Supabase Storage bucket was
> dropped on 2026-05-21. Page HTML now lives in the private Cloudflare R2 bucket
> configured via `ARCHIVE_BUCKET` (production: `native-hover-archive`). See
> `## 3. Recommended Approach` for the current design.

- bucket name: `task-html` (no longer present)
- visibility: private
- access: signed URLs only when needed

Reason:

- page bodies may contain sensitive content, tokens in markup, or unpublished
  data
- private storage is the safer default

## 12. Data Volume Planning

Using gzip and rough averages:

- average compressed page: `40-150 KB`
- `1000` stored pages: `~40-150 MB`
- `10,000` stored pages: `~0.4-1.5 GB`

This will vary a lot by site, but gzip should make the storage footprint much
smaller than raw HTML.

## 13. Risks

- storage growth can become significant without retention later
- page content may contain sensitive material that diagnostics alone would not
  capture
- upload latency adds a small amount of work to the task success path

## 14. Recommendation

Proceed with:

- private Supabase Storage bucket
- gzip-compressed per-task HTML uploads
- task metadata columns storing path and size details
- no UI work initially
- no retention work in the first pass

This is a moderate implementation with low schema risk because the project
already has working Supabase Storage upload plumbing.
