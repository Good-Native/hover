/**
 * lib/site-jobs.js — shared job fetching, site scoping, and realtime helpers
 *
 * Shared between app pages and the Webflow Designer extension.
 * Rendering stays surface-specific; this module only owns data and state helpers.
 */

import { get } from "/app/lib/api-client.js";

const REALTIME_DEBOUNCE_MS = 250;
const SUBSCRIBE_RETRY_INTERVAL_MS = 1000;
const MAX_SUBSCRIBE_RETRIES = 15;
const DEFAULT_FALLBACK_POLLING_INTERVAL_MS = 1000;

/**
 * Fetch the job list from the API.
 *
 * @param {{ limit?: number, range?: string, include?: string }} [options]
 * @returns {Promise<Record<string, unknown>[]>}
 */
export async function fetchJobs(options = {}) {
  const params = new URLSearchParams();
  if (options.limit) params.set("limit", String(options.limit));
  if (options.range) params.set("range", options.range);
  if (options.include) params.set("include", options.include);
  const qs = params.toString();
  const res = await get(`/v1/jobs${qs ? `?${qs}` : ""}`);
  return res?.jobs ?? [];
}

/**
 * Normalise a domain or URL into a bare hostname.
 * @param {string} input
 * @returns {string}
 */
export function normaliseDomain(input) {
  const trimmed = String(input || "")
    .trim()
    .toLowerCase()
    .replace(/^https?:\/\//, "")
    .replace(/^www\./, "");

  if (!trimmed) {
    return "";
  }

  return trimmed.split("/")[0] || trimmed;
}

/**
 * @param {{ siteDomain?: string|null, siteDomainCandidates?: string[] }} [options]
 * @returns {string[]}
 */
export function getSiteDomainCandidates(options = {}) {
  const normalised = new Set(
    (options.siteDomainCandidates || [])
      .map((candidate) => normaliseDomain(candidate))
      .filter(Boolean)
  );

  if (options.siteDomain) {
    normalised.add(normaliseDomain(options.siteDomain));
  }

  return [...normalised];
}

/**
 * Filter jobs to the current site scope.
 *
 * @param {Record<string, unknown>[]} jobs
 * @param {{ siteDomain?: string|null, siteDomainCandidates?: string[] }} [options]
 * @returns {Record<string, unknown>[]}
 */
export function filterJobsByDomains(jobs = [], options = {}) {
  const candidates = getSiteDomainCandidates(options);
  return jobs.filter((job) => {
    const jobDomain = normaliseDomain(job?.domains?.name || job?.domain || "");
    return !candidates.length || candidates.includes(jobDomain);
  });
}

/**
 * @param {Record<string, unknown>[]} jobs
 * @param {{ siteDomain?: string|null, siteDomainCandidates?: string[] }} [options]
 * @returns {Record<string, unknown>|null}
 */
export function pickLatestJobByDomains(jobs = [], options = {}) {
  return filterJobsByDomains(jobs, options)[0] || null;
}

/**
 * @param {Record<string, unknown>[]} jobs
 * @param {{ siteDomain?: string|null, siteDomainCandidates?: string[] }} [options]
 * @param {(status: string) => boolean} [isActiveJobStatus]
 * @returns {string}
 */
export function buildCompletedJobsSignature(
  jobs = [],
  options = {},
  isActiveJobStatus = defaultIsActiveJobStatus
) {
  const completed = filterJobsByDomains(jobs, options)
    .filter((job) => !isActiveJobStatus(String(job.status || "")))
    .slice(0, 6);

  return completed
    .map(
      (job) =>
        `${job.id || ""}:${job.status || ""}:${job.total_tasks || 0}:${job.completed_tasks || 0}:${job.failed_tasks || 0}:${job.skipped_tasks || 0}:${job.completed_at || ""}`
    )
    .join("|");
}

/**
 * @param {Record<string, unknown>[]} jobs
 * @param {{ siteDomain?: string|null, siteDomainCandidates?: string[] }} [options]
 * @returns {string}
 */
export function buildChartJobsSignature(jobs = [], options = {}) {
  const chartJobs = filterJobsByDomains(jobs, options)
    .filter(
      (job) =>
        String(job.status || "")
          .trim()
          .toLowerCase() === "completed"
    )
    .slice(0, 12);

  return chartJobs
    .map(
      (job) =>
        `${job.id || ""}:${job.status || ""}:${job.failed_tasks || 0}:${job.skipped_tasks || 0}:${job.completed_at || ""}:${job.total_tasks || 0}:${job.stats?.total_broken_links || 0}:${job.stats?.slow_page_buckets?.over_10s || 0}:${job.stats?.slow_page_buckets?.["5_to_10s"] || 0}:${job.stats?.slow_page_buckets?.["3_to_5s"] || 0}`
    )
    .join("|");
}

/**
 * Subscribe to job updates via Supabase Realtime with a polling fallback.
 *
 * @param {{
 *   orgId: string,
 *   onUpdate: () => void,
 *   supabaseClient?: { channel?: (name: string) => any, removeChannel?: (channel: any) => Promise<unknown> },
 *   channelName?: string,
 *   getFallbackInterval?: () => number,
 *   onSubscriptionIssue?: (status?: string, err?: Error) => void
 * }} options
 * @returns {() => void}
 */
export function subscribeToJobUpdates(options) {
  const {
    orgId,
    onUpdate,
    supabaseClient = window.supabase,
    channelName = `hover-jobs:${orgId}`,
    getFallbackInterval = () => DEFAULT_FALLBACK_POLLING_INTERVAL_MS,
    onSubscriptionIssue,
  } = options;

  let channel = null;
  let retryCount = 0;
  let retryTimer = null;
  let fallbackTimer = null;
  let lastUpdate = 0;
  let debounceTimer = null;
  let fallbackIntervalMs = null;
  let unsubscribed = false;

  function resolveFallbackInterval() {
    const next = Number(getFallbackInterval());
    return Number.isFinite(next) && next > 0
      ? next
      : DEFAULT_FALLBACK_POLLING_INTERVAL_MS;
  }

  function startFallback() {
    const nextMs = resolveFallbackInterval();
    if (fallbackTimer && fallbackIntervalMs === nextMs) return;
    if (fallbackTimer) {
      clearInterval(fallbackTimer);
    }
    fallbackIntervalMs = nextMs;
    fallbackTimer = setInterval(() => {
      if (!unsubscribed) {
        onUpdate();
      }
    }, fallbackIntervalMs);
  }

  function clearFallback() {
    if (fallbackTimer) {
      clearInterval(fallbackTimer);
      fallbackTimer = null;
      fallbackIntervalMs = null;
    }
  }

  function cleanup() {
    unsubscribed = true;
    if (retryTimer) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
    if (debounceTimer) {
      clearTimeout(debounceTimer);
      debounceTimer = null;
    }
    clearFallback();
    if (channel && supabaseClient?.removeChannel) {
      supabaseClient.removeChannel(channel).catch(() => {});
      channel = null;
    }
  }

  function throttledUpdate() {
    const now = Date.now();
    if (now - lastUpdate >= REALTIME_DEBOUNCE_MS) {
      lastUpdate = now;
      clearFallback();
      onUpdate();
      return;
    }

    if (!debounceTimer) {
      debounceTimer = setTimeout(() => {
        debounceTimer = null;
        if (unsubscribed) return;
        lastUpdate = Date.now();
        clearFallback();
        onUpdate();
      }, REALTIME_DEBOUNCE_MS);
    }
  }

  function subscribe() {
    if (unsubscribed) return;

    if (!orgId || !supabaseClient?.channel || !supabaseClient?.removeChannel) {
      if (retryCount < MAX_SUBSCRIBE_RETRIES) {
        retryCount++;
        retryTimer = setTimeout(subscribe, SUBSCRIBE_RETRY_INTERVAL_MS);
      } else {
        onSubscriptionIssue?.("MAX_RETRIES");
        startFallback();
      }
      return;
    }

    retryCount = 0;
    retryTimer = null;

    try {
      channel = supabaseClient
        .channel(channelName)
        .on(
          "postgres_changes",
          {
            event: "INSERT",
            schema: "public",
            table: "jobs",
            filter: `organisation_id=eq.${orgId}`,
          },
          throttledUpdate
        )
        .on(
          "postgres_changes",
          {
            event: "UPDATE",
            schema: "public",
            table: "jobs",
            filter: `organisation_id=eq.${orgId}`,
          },
          throttledUpdate
        )
        .on(
          "postgres_changes",
          {
            event: "DELETE",
            schema: "public",
            table: "jobs",
            filter: `organisation_id=eq.${orgId}`,
          },
          throttledUpdate
        )
        .subscribe((status, err) => {
          if (
            (status === "CHANNEL_ERROR" || status === "TIMED_OUT" || err) &&
            !unsubscribed
          ) {
            onSubscriptionIssue?.(status, err);
            startFallback();
          }
        });
    } catch (err) {
      onSubscriptionIssue?.("SUBSCRIBE_FAILED", err);
      startFallback();
    }
  }

  subscribe();
  return cleanup;
}

function defaultIsActiveJobStatus(status) {
  const normalised = String(status || "")
    .trim()
    .toLowerCase();
  return [
    "pending",
    "queued",
    "initializing",
    "running",
    "in_progress",
    "processing",
    "cancelling",
  ].includes(normalised);
}

export default {
  fetchJobs,
  normaliseDomain,
  getSiteDomainCandidates,
  filterJobsByDomains,
  pickLatestJobByDomains,
  buildCompletedJobsSignature,
  buildChartJobsSignature,
  subscribeToJobUpdates,
};
