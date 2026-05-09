/**
 * pages/webflow-jobs.js — dashboard job helpers and table renderer
 *
 * Shared fetching and realtime state lives in /app/lib/site-jobs.js.
 * This page module keeps the dashboard-facing table renderer and a thin
 * wrapper for the dashboard's adaptive polling cadence.
 */

import {
  fetchJobs as fetchSharedJobs,
  subscribeToJobUpdates as subscribeToSharedJobUpdates,
} from "/app/lib/site-jobs.js";
import {
  formatRelativeTime,
  formatDuration,
  formatCount,
} from "/app/lib/formatters.js";
import { createStatusPill } from "/app/components/hover-status-pill.js";
import { createDataTable } from "/app/components/hover-data-table.js";

// ── Constants ──────────────────────────────────────────────────────────────────

const FALLBACK_POLLING_INTERVAL_ACTIVE_MS = 500;
const FALLBACK_POLLING_INTERVAL_IDLE_MS = 1000;

// ── Data fetching ──────────────────────────────────────────────────────────────

/**
 * Fetch the job list from the API.
 *
 * @param {{ limit?: number, range?: string, include?: string }} [options]
 * @returns {Promise<import("/app/lib/api-client.js").unknown[]>}
 */
export async function fetchJobs(options = {}) {
  return fetchSharedJobs(options);
}

// ── Rendering ──────────────────────────────────────────────────────────────────

/**
 * Render a list of jobs into a container using hover-data-table.
 * Replaces any existing table in the container.
 *
 * @param {HTMLElement} container
 * @param {Record<string,unknown>[]} jobs
 * @param {{ onRowClick?: (job: Record<string,unknown>) => void }} [options]
 */
export function renderJobList(container, jobs, options = {}) {
  // Remove previous table if any
  const existing = container.querySelector("hover-data-table");
  if (existing) existing.remove();

  const table = createDataTable({
    columns: [
      {
        key: "domain",
        label: "Domain",
        render: (val, row) => {
          const domain = val || row.domains?.name || "—";
          const span = document.createElement("span");
          span.className = "hover-jobs__domain";
          span.textContent = domain;
          return span;
        },
      },
      {
        key: "status",
        label: "Status",
        render: (val) => {
          return createStatusPill(String(val || ""));
        },
      },
      {
        key: "progress",
        label: "Progress",
        render: (val, row) => {
          const pct = Math.round(Number(val) || 0);
          const total = formatCount(row.total_tasks);
          const done = formatCount(row.completed_tasks);
          const span = document.createElement("span");
          span.textContent = `${done} / ${total} (${pct}%)`;
          return span;
        },
      },
      {
        key: "started_at",
        label: "Started",
        render: (val) => (val ? formatRelativeTime(String(val)) : "—"),
      },
      {
        key: "duration_seconds",
        label: "Duration",
        render: (val) =>
          val != null ? formatDuration(Number(val) * 1000) : "—",
      },
    ],
    rows: jobs.map((job) => ({
      ...job,
      domain: job.domains?.name || job.domain || "",
    })),
    emptyMessage: "No jobs yet for this site.",
    onRowClick: options.onRowClick,
  });

  container.appendChild(table);
}

/**
 * Render an empty state message into a container.
 * @param {HTMLElement} container
 * @param {string} [message]
 */
export function renderEmptyState(container, message = "No runs yet.") {
  container.innerHTML = "";
  const div = document.createElement("div");
  div.className = "hover-jobs__empty";
  div.textContent = message;
  container.appendChild(div);
}

/**
 * Render an error state into a container.
 * @param {HTMLElement} container
 * @param {string} [message]
 */
export function renderErrorState(container, message = "Failed to load jobs.") {
  container.innerHTML = "";
  const div = document.createElement("div");
  div.className = "hover-jobs__error";
  div.textContent = message;
  container.appendChild(div);
}

// ── Realtime subscription ──────────────────────────────────────────────────────

/**
 * Subscribe to job changes for an organisation via Supabase Realtime.
 * Falls back to polling if realtime fails.
 *
 * @param {string} orgId
 * @param {() => void} onUpdate - called when jobs may have changed
 * @returns {() => void} unsubscribe / cleanup function
 */
export function subscribeToJobUpdates(orgId, onUpdate) {
  return subscribeToSharedJobUpdates({
    orgId,
    onUpdate,
    getFallbackInterval: () =>
      window.dataBinder?.hasRealtimeActiveJobs
        ? FALLBACK_POLLING_INTERVAL_ACTIVE_MS
        : FALLBACK_POLLING_INTERVAL_IDLE_MS,
  });
}
