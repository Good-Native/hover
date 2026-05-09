# Supabase Advisor Findings

This file tracks Supabase database linter findings that we have decided not to
remediate (or have deferred), with the reasoning. The companion file
[`SECURITY_REMEDIATION_PLAN.md`](SECURITY_REMEDIATION_PLAN.md) covers code-level
security scanner suppressions; this file covers the database advisors surfaced
under **Supabase Studio → Advisors**.

Last reviewed: 2026-05-09.

## How to use this list

1. When a new advisor finding appears, fix it via a migration if possible.
2. If the finding is a deliberate keep, append a row below with the `cache_key`,
   the reason it must remain, and the migration or code that makes it necessary.
3. After landing the corresponding migration, dismiss the matching finding in
   the Supabase Studio Advisors UI so the dashboard stays signal-only.

## Deliberate keeps (will keep being flagged)

| Advisor lint                                         | Object                           | Why we keep it                                                                                                                                                                                                                            |
| ---------------------------------------------------- | -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `anon_security_definer_function_executable`          | `public.user_is_member_of(uuid)` | Invoked inside RLS policies that need to look up `organisation_members` rows owned by other users. Must remain executable by `authenticated` for those policies to evaluate. Service role bypass alone is not enough — clients call this. |
| `anon_security_definer_function_executable`          | `public.user_organisation_id()`  | Same reason — used inside RLS policies to derive the caller's primary organisation.                                                                                                                                                       |
| `anon_security_definer_function_executable`          | `public.user_organisations()`    | Same reason — used inside RLS policies for multi-org callers.                                                                                                                                                                             |
| `authenticated_security_definer_function_executable` | Same three functions             | Mirror of the `anon` warnings; same reasoning.                                                                                                                                                                                            |
| `rls_enabled_no_policy`                              | `public.domain_hosts`            | Already deny-all by default. Only the Go server (service role) writes to it.                                                                                                                                                              |

## Deferred (not done in this round)

| Advisor lint                   | Object(s)                                                                                                                                                                                                                                                                                    | Why deferred                                                                                                                                                                   |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `unused_index`                 | ~30 indexes across `domains`, `users`, `organisations`, `organisation_invites`, `organisation_members`, `job_share_links`, `task_outbox_dead`, `domain_hosts`, `notifications`, `platform_org_mappings`, `google_analytics_*`, `page_analytics`, `slack_user_links`, `webflow_site_settings` | `pg_stat_user_indexes` resets at upgrade and does not yet reflect a long enough baseline for the recently-added tables. Revisit only if write throughput becomes a bottleneck. |
| `auth_db_connections_absolute` | Auth server (10 connections fixed)                                                                                                                                                                                                                                                           | Requires a Supabase dashboard configuration change, not a migration. Switch to percentage-based allocation when the project is next scaled up.                                 |

## Resolved in this PR

| Migration                                                     | Findings cleared                                                                                                                                                                                                                                  |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `20260509101603_lock_down_internal_tables_and_quota_view.sql` | `rls_disabled_in_public` on `task_outbox`, `task_outbox_dead`, `lighthouse_runs`; `security_definer_view` on `organisation_quota_status`.                                                                                                         |
| `20260509102622_revoke_anon_execute_on_internal_rpcs.sql`     | `anon_security_definer_function_executable` and `authenticated_security_definer_function_executable` for the 19 server-internal RPCs (token store/get/delete, vault cleanup, slack-link, `increment_daily_usage`).                                |
| `20260509104940_optimise_rls_and_function_search_path.sql`    | `auth_rls_initplan` on `notifications`, `daily_usage`, `google_analytics_*`, `organisation_domains`; `multiple_permissive_policies` on `daily_usage`; `function_search_path_mutable` on `update_job_queue_counters`, `get_daily_quota_remaining`. |
| `20260509111837_add_covering_indexes_for_unindexed_fks.sql`   | `unindexed_foreign_keys` on `google_analytics_accounts`, `google_analytics_connections`, `lighthouse_runs`, `organisation_invites`, `page_analytics`, `platform_org_mappings`, `slack_connections`, `task_outbox_dead`, `webflow_connections`.    |
