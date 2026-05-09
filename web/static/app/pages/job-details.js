/**
 * pages/job-details.js — job details module entrypoint
 *
 * Phase 4: owns the tasks section of the job details page.
 * Co-exists with job-page.js which continues to own the job header,
 * stats/metrics, action buttons, share link, and export functionality.
 *
 * This module replaces:
 *   - The legacy <table id="tasksTable"> with <hover-data-table>
 *   - The legacy .filter-tabs buttons with <hover-tabs>
 *   - The tasks data fetch, sort, filter, pagination loop from job-page.js
 *   - The status pill span with <hover-status-pill>
 *
 * What this does NOT touch (still handled by job-page.js):
 *   - Job header / status / action buttons (restart, cancel)
 *   - Stats and metrics cards
 *   - Share link generation and copy
 *   - Export (CSV/JSON)
 *   - Shared/token-authenticated view
 *
 * Loading contract (job-details.html):
 *   1. /config.js              — sets window.GNH_CONFIG
 *   2. /js/core.js defer       — Supabase init, window.GNH_APP
 *   3. /js/job-page.js defer   — job header, metrics, actions
 *   4. <script type="module">  — this file
 */

// Signal to job-page.js that this module owns the tasks section.
// Evaluated synchronously before DOMContentLoaded callbacks fire.
window.__hoverTasksOwned = true;

import { get } from "/app/lib/api-client.js";
import { createStatusPill } from "/app/components/hover-status-pill.js";
import { createDataTable } from "/app/components/hover-data-table.js";
import { createTabs } from "/app/components/hover-tabs.js";
import { showToast } from "/app/components/hover-toast.js";
import { formatRelativeTime, formatDuration } from "/app/lib/formatters.js";
import { initSurfacePage } from "/app/lib/surface-context.js";

// ── Constants ──────────────────────────────────────────────────────────────────

const DEFAULT_PAGE_SIZE = 50;
const PAGE_SIZE_OPTIONS = [25, 50, 100, 200];
const PATH_FILTER_DEBOUNCE_MS = 300;

// Fallback polling: 500 ms while job is active, 2 s once terminal.
const POLL_ACTIVE_MS = 500;
const POLL_IDLE_MS = 2000;

// ── Tab definitions ────────────────────────────────────────────────────────────

/**
 * Each tab has a key (used in state), a label, and an optional filter bag
 * that maps directly to API query params (status, cache, performance).
 *
 * @type {Array<{ key: string, label: string, filters?: Record<string,string> }>}
 */
const TABS = [
  { key: "all", label: "All" },
  { key: "broken", label: "Broken Links", filters: { status: "failed" } },
  { key: "success", label: "Success", filters: { cache: "hit" } },
  { key: "slow", label: "Slow", filters: { performance: "slow" } },
  {
    key: "very_slow",
    label: "Very Slow",
    filters: { performance: "very_slow" },
  },
  { key: "in_progress", label: "In Progress", filters: { status: "running" } },
];

// ── State ──────────────────────────────────────────────────────────────────────

/** @type {string|null} */
let jobId = null;

const taskState = {
  limit: DEFAULT_PAGE_SIZE,
  page: 0,
  sortColumn: "created_at",
  sortDirection: "desc",
  activeTab: "all",
  performanceFilter: "",
  statusFilter: "",
  cacheFilter: "",
  pathFilter: "",
  totalTasks: 0,
};

// Realtime / polling state
let pollTimer = null;
let realtimeChannel = null;
let isRefreshing = false;
let lastRefresh = 0;
const THROTTLE_MS = 250;
let throttleTimer = null;

// ── Bootstrap ──────────────────────────────────────────────────────────────────

async function init() {
  jobId = resolveJobId();
  if (!jobId) return;

  initSurfacePage({
    title: "Job details",
    defaultReturnPath: "/dashboard",
  });

  // Wait for a valid session before touching the API
  const token = await waitForSession();
  if (!token) return;

  upgradeStatusPill();
  watchStatusPill();
  buildTasksSection();
  await loadTasks();
  wireInteractions();
  startSubscription();
}

// ── Job ID resolution ──────────────────────────────────────────────────────────

function resolveJobId() {
  const segments = window.location.pathname.split("/").filter(Boolean);
  // /jobs/:id  or  /jobs?id=:id
  if (segments.length >= 2 && segments[0] === "jobs") {
    return segments[segments.length - 1] !== "jobs"
      ? segments[segments.length - 1]
      : null;
  }
  return new URLSearchParams(window.location.search).get("id") || null;
}

// ── Status pill upgrade ────────────────────────────────────────────────────────

function upgradeStatusPill() {
  const span = document.querySelector(
    ".status-pill[gnh-text='job.status_label']"
  );
  if (!span) return;

  const label = span.textContent.trim().toLowerCase();
  if (!label || label.includes("{")) return;

  const STATUS_MAP = {
    completed: "completed",
    done: "completed",
    running: "running",
    "in progress": "running",
    pending: "pending",
    "starting up": "pending",
    queued: "queued",
    failed: "failed",
    error: "failed",
    cancelled: "cancelled",
    cancelling: "cancelling",
    skipped: "skipped",
  };
  const status = STATUS_MAP[label] ?? label;

  const existing = span.parentElement?.querySelector("hover-status-pill");
  if (existing) {
    existing.setAttribute("status", status);
    return;
  }

  const pill = createStatusPill(status);
  span.style.display = "none";
  span.parentElement.insertBefore(pill, span);
}

function watchStatusPill() {
  const span = document.querySelector(
    ".status-pill[gnh-text='job.status_label']"
  );
  if (!span) return;
  new MutationObserver(upgradeStatusPill).observe(span, {
    attributes: true,
    attributeFilter: ["class"],
    characterData: true,
    childList: true,
    subtree: true,
  });
  upgradeStatusPill();
}

// ── Tasks section build ────────────────────────────────────────────────────────

/** @type {HoverDataTable|null} */
let tasksTable = null;

/** @type {HoverTabs|null} */
let tabsEl = null;

/**
 * Replace the legacy .filter-tabs and <table> in the DOM with our
 * hover-tabs and hover-data-table elements. Controls (search, limit,
 * pagination) stay in place — we just wire them.
 */
function buildTasksSection() {
  // ── Filter tabs ────────────────────────────────────────────────────────────
  const legacyFilters = document.getElementById("taskFilters");
  if (legacyFilters) {
    tabsEl = createTabs(
      TABS.map(({ key, label }) => ({ key, label })),
      { active: "all" }
    );
    tabsEl.id = "taskFilters";
    legacyFilters.replaceWith(tabsEl);
  }

  // ── Tasks table ────────────────────────────────────────────────────────────
  const container = document.getElementById("tasksContainer");
  if (!container) return;

  // Hide legacy loading/empty divs — we handle state via hover-data-table
  const legacyLoading = document.getElementById("tasksLoading");
  const legacyEmpty = document.getElementById("tasksEmpty");
  if (legacyLoading) legacyLoading.style.display = "none";
  if (legacyEmpty) legacyEmpty.style.display = "none";

  // Replace the <div style="overflow-x:auto"> wrapper + <table> with our component
  const overflowWrap = container.querySelector("div[style*='overflow']");
  const legacyTable = document.getElementById("tasksTable");
  if (legacyTable) legacyTable.style.display = "none";

  tasksTable = createDataTable({
    columns: buildColumns("all", false),
    rows: [],
    emptyMessage: "No tasks found for this view.",
  });
  tasksTable.id = "tasksDataTable";
  tasksTable.setAttribute("loading", "");

  if (overflowWrap) {
    overflowWrap.replaceWith(tasksTable);
  } else {
    container.appendChild(tasksTable);
  }
}

// ── Column definitions ─────────────────────────────────────────────────────────

function colPath() {
  return {
    key: "path",
    label: "Path",
    sortable: true,
    render: (val, row) => {
      const url = row.url || val || "";
      const display = row.display_path || val || "/";
      if (!url) {
        const code = document.createElement("code");
        code.textContent = display;
        return code;
      }
      const a = document.createElement("a");
      a.href = url;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      const code = document.createElement("code");
      code.textContent = display;
      a.appendChild(code);
      return a;
    },
  };
}

function colStatus() {
  return {
    key: "status",
    label: "Status",
    sortable: true,
    render: (val) => createStatusPill(String(val || "unknown")),
  };
}

function colLoadTime1() {
  return {
    key: "response_time",
    label: "Load time 1st",
    sortable: true,
    render: (val) => fmtMs(val),
  };
}

function colLoadTime2() {
  return {
    key: "second_response_time",
    label: "Load time 2nd",
    sortable: true,
    render: (val) => fmtMs(val),
  };
}

function colCache() {
  return {
    key: "cache_status",
    label: "Cache",
    sortable: true,
    render: (val) => String(val || "—"),
  };
}

function colFoundOn() {
  return {
    key: "source_url",
    label: "Found on",
    render: (val, row) => {
      if (!val || row.source_type === "sitemap") {
        const span = document.createElement("span");
        span.textContent = "Sitemap";
        span.style.color = "var(--text-colour--disabled)";
        return span;
      }
      const a = document.createElement("a");
      a.href = val;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      a.textContent = val.replace(/^https?:\/\/[^/]+/, "") || val;
      a.title = val;
      return a;
    },
  };
}

function colsAnalytics() {
  return [
    {
      key: "page_views_7d",
      label: "Views (7d)",
      sortable: true,
      render: (val) => fmtOptCount(val),
    },
    {
      key: "page_views_28d",
      label: "Views (28d)",
      sortable: true,
      render: (val) => fmtOptCount(val),
    },
    {
      key: "page_views_180d",
      label: "Views (180d)",
      sortable: true,
      render: (val) => fmtOptCount(val),
    },
  ];
}

/**
 * Return the column set for the active tab.
 * Analytics columns are appended when data is present.
 *
 * @param {string} tab  active tab key
 * @param {boolean} showAnalytics
 * @returns {Array}
 */
function buildColumns(tab, showAnalytics) {
  let cols;
  switch (tab) {
    case "broken":
      cols = [colPath(), colFoundOn()];
      break;
    case "success":
    case "slow":
    case "very_slow":
      cols = [
        colPath(),
        colLoadTime1(),
        colLoadTime2(),
        colCache(),
        colFoundOn(),
      ];
      break;
    case "in_progress":
      cols = [colPath(), colStatus(), colFoundOn()];
      break;
    case "all":
    default:
      cols = [
        colPath(),
        colStatus(),
        colLoadTime1(),
        colLoadTime2(),
        colCache(),
        colFoundOn(),
      ];
      break;
  }
  if (showAnalytics) cols.push(...colsAnalytics());
  return cols;
}

// ── Data loading ───────────────────────────────────────────────────────────────

async function loadTasks() {
  if (!tasksTable) return;

  // Hide pagination immediately — avoids flicker where previous tab's count
  // causes pagination to flash before the new result count arrives.
  const paginationEl = document.getElementById("tasksPagination");
  if (paginationEl) paginationEl.style.display = "none";

  const params = new URLSearchParams();
  params.set("limit", String(taskState.limit));
  params.set("offset", String(taskState.page * taskState.limit));
  params.set(
    "sort",
    taskState.sortDirection === "desc"
      ? `-${taskState.sortColumn}`
      : taskState.sortColumn
  );
  if (taskState.statusFilter) params.set("status", taskState.statusFilter);
  if (taskState.cacheFilter) params.set("cache", taskState.cacheFilter);
  if (taskState.performanceFilter)
    params.set("performance", taskState.performanceFilter);
  if (taskState.pathFilter) params.set("path", taskState.pathFilter);

  try {
    const data = await get(`/v1/jobs/${jobId}/tasks?${params.toString()}`);
    const tasks = Array.isArray(data?.tasks) ? data.tasks : [];
    const pagination = data?.pagination || {};

    const showAnalytics = tasks.some(
      (t) =>
        t.page_views_7d !== undefined ||
        t.page_views_28d !== undefined ||
        t.page_views_180d !== undefined
    );

    // Rebuild columns for active tab (and analytics availability)
    tasksTable.columns = buildColumns(taskState.activeTab, showAnalytics);

    const domain = resolveDomain();
    tasksTable.rows = tasks.map((t) => normaliseTask(t, domain));

    tasksTable.removeAttribute("loading");
    tasksTable.removeAttribute("error");

    taskState.totalTasks = Number(pagination.total ?? tasks.length);
    updatePagination(pagination);
  } catch (err) {
    tasksTable.removeAttribute("loading");
    tasksTable.setAttribute("error", "Failed to load tasks.");
    console.error("[job-details] loadTasks failed:", err);
  }
}

/** Normalise a raw task row for rendering */
function normaliseTask(task, defaultDomain) {
  const host = task.domain || defaultDomain || "";
  const path = task.path || "/";
  const url = task.url || (host ? `https://${host}${path}` : path);
  const displayPath = task.host ? `${task.host}${path}` : path;

  return {
    ...task,
    url,
    display_path: displayPath,
    response_time: task.response_time ?? null,
    second_response_time: task.second_response_time ?? null,
    status_code: task.status_code ?? null,
    cache_status: task.cache_status || "—",
    page_views_7d: task.page_views_7d ?? null,
    page_views_28d: task.page_views_28d ?? null,
    page_views_180d: task.page_views_180d ?? null,
  };
}

function resolveDomain() {
  // job-page.js stores domain on window.dataBinder state; fall back to hostname
  return (
    window.dataBinder?.state?.domain ||
    document.querySelector("[gnh-text='job.domain']")?.textContent?.trim() ||
    ""
  );
}

// ── Pagination ─────────────────────────────────────────────────────────────────

function updatePagination(pagination) {
  const paginationEl = document.getElementById("tasksPagination");
  const total = Number(pagination?.total ?? taskState.totalTasks ?? 0);
  const offset = taskState.page * taskState.limit;

  const hasNext = Boolean(
    pagination?.has_next ?? offset + taskState.limit < total
  );
  const hasPrev = Boolean(pagination?.has_prev ?? offset > 0);

  const prevBtn = document.getElementById("prevTasksBtn");
  const nextBtn = document.getElementById("nextTasksBtn");

  if (!paginationEl) return;

  if (!total || total <= taskState.limit) {
    paginationEl.style.display = "none";
    return;
  }

  paginationEl.style.display = "flex";

  const start = total === 0 ? 0 : offset + 1;
  const end = Math.min(offset + taskState.limit, total);
  const summary = `${start}–${end} of ${total}`;

  // Update gnh-text summary if present (legacy binding point)
  const summaryEl = paginationEl.querySelector(
    "[gnh-text='tasks.pagination.summary']"
  );
  if (summaryEl) summaryEl.textContent = summary;

  if (prevBtn) prevBtn.disabled = !hasPrev;
  if (nextBtn) nextBtn.disabled = !hasNext;
}

// ── Interaction wiring ─────────────────────────────────────────────────────────

function wireInteractions() {
  // Per-page limit
  const limitSelect = document.getElementById("tasksLimit");
  if (limitSelect) {
    limitSelect.innerHTML = PAGE_SIZE_OPTIONS.map(
      (v) =>
        `<option value="${v}"${v === taskState.limit ? " selected" : ""}>${v}</option>`
    ).join("");
    limitSelect.addEventListener("change", (e) => {
      taskState.limit = Number(e.target.value) || DEFAULT_PAGE_SIZE;
      taskState.page = 0;
      loadTasks().catch(console.error);
    });
  }

  // Filter tabs
  if (tabsEl) {
    tabsEl.addEventListener("hover-tabs:change", (e) => {
      const tab = TABS.find((t) => t.key === e.detail.key) ?? TABS[0];
      taskState.activeTab = tab.key;
      taskState.statusFilter = tab.filters?.status ?? "";
      taskState.cacheFilter = tab.filters?.cache ?? "";
      taskState.performanceFilter = tab.filters?.performance ?? "";
      taskState.pathFilter = "";
      taskState.page = 0;
      taskState.totalTasks = 0;
      const pathInput = document.getElementById("pathFilter");
      if (pathInput) pathInput.value = "";
      loadTasks().catch(console.error);
    });
  }

  // Path search
  const pathInput = document.getElementById("pathFilter");
  if (pathInput) {
    let debounceTimer = null;
    pathInput.addEventListener("input", (e) => {
      clearTimeout(debounceTimer);
      const value = e.target.value.trim();
      debounceTimer = setTimeout(() => {
        if (value.length === 0 || value.length >= 3) {
          taskState.pathFilter = value;
          taskState.page = 0;
          if (value.length > 0) {
            taskState.activeTab = "all";
            taskState.statusFilter = "";
            taskState.cacheFilter = "";
            taskState.performanceFilter = "";
            if (tabsEl) tabsEl.active = "all";
          }
          loadTasks().catch(console.error);
        }
      }, PATH_FILTER_DEBOUNCE_MS);
    });
  }

  // Pagination prev/next
  const prevBtn = document.getElementById("prevTasksBtn");
  const nextBtn = document.getElementById("nextTasksBtn");
  if (prevBtn) {
    prevBtn.addEventListener("click", () => {
      if (taskState.page > 0) {
        taskState.page--;
        loadTasks().catch(console.error);
      }
    });
  }
  if (nextBtn) {
    nextBtn.addEventListener("click", () => {
      const maxPage = Math.ceil(taskState.totalTasks / taskState.limit) - 1;
      if (taskState.page < maxPage) {
        taskState.page++;
        loadTasks().catch(console.error);
      }
    });
  }

  // Sort — hover-data-table emits hover-data-table:sort
  if (tasksTable) {
    tasksTable.addEventListener("hover-data-table:sort", (e) => {
      const { column, direction } = e.detail;
      taskState.sortColumn = column;
      taskState.sortDirection = direction;
      taskState.page = 0;
      loadTasks().catch(console.error);
    });
  }

  // Manual refresh button
  const refreshBtn = document.getElementById("refreshTasksBtn");
  if (refreshBtn) {
    refreshBtn.addEventListener("click", () => {
      loadTasks().catch(console.error);
    });
  }
}

// ── Realtime + polling ─────────────────────────────────────────────────────────

function throttledRefresh() {
  // Do NOT clear the fallback poll here — it is a background safety net and
  // should only stop when the realtime subscription is confirmed healthy.
  // Clearing it on every realtime event would permanently kill the fallback
  // after the first event fires.
  const now = Date.now();
  if (now - lastRefresh >= THROTTLE_MS && !isRefreshing) {
    executeRefresh();
    return;
  }
  if (!throttleTimer && !isRefreshing) {
    throttleTimer = setTimeout(() => {
      throttleTimer = null;
      if (!isRefreshing) executeRefresh();
    }, THROTTLE_MS);
  }
}

async function executeRefresh() {
  if (isRefreshing) return;
  isRefreshing = true;
  lastRefresh = Date.now();
  try {
    await loadTasks();
  } finally {
    isRefreshing = false;
  }
}

function startPoll(intervalMs) {
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(() => {
    if (!isRefreshing) executeRefresh();
  }, intervalMs);
}

function clearPoll() {
  if (pollTimer) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

function isActiveJob() {
  // Read current status from the pill or gnh-text span
  const statusEl = document.querySelector(
    "[gnh-text='job.status_label'], hover-status-pill"
  );
  const statusText = (
    statusEl?.getAttribute("status") ||
    statusEl?.textContent ||
    ""
  ).toLowerCase();
  return [
    "pending",
    "running",
    "queued",
    "initializing",
    "processing",
    "cancelling",
  ].includes(statusText);
}

function startSubscription() {
  if (!jobId || !window.supabase?.channel) {
    // No realtime available — fall straight to polling
    startPoll(isActiveJob() ? POLL_ACTIVE_MS : POLL_IDLE_MS);
    return;
  }

  try {
    realtimeChannel = window.supabase
      .channel(`job-details-tasks:${jobId}`)
      .on(
        "postgres_changes",
        {
          event: "*",
          schema: "public",
          table: "tasks",
          filter: `job_id=eq.${jobId}`,
        },
        throttledRefresh
      )
      .subscribe((status, err) => {
        if (status === "CHANNEL_ERROR" || status === "TIMED_OUT" || err) {
          startPoll(isActiveJob() ? POLL_ACTIVE_MS : POLL_IDLE_MS);
        }
      });

    // Start fallback immediately; throttledRefresh clears it on first real event
    startPoll(isActiveJob() ? POLL_ACTIVE_MS : POLL_IDLE_MS);
  } catch {
    startPoll(isActiveJob() ? POLL_ACTIVE_MS : POLL_IDLE_MS);
  }

  // Cleanup on unload
  window.addEventListener("beforeunload", () => {
    clearPoll();
    if (throttleTimer) {
      clearTimeout(throttleTimer);
      throttleTimer = null;
    }
    if (realtimeChannel && window.supabase) {
      window.supabase.removeChannel(realtimeChannel).catch(() => {});
    }
  });
}

// ── Helpers ────────────────────────────────────────────────────────────────────

/** Format milliseconds for display */
function fmtMs(val) {
  if (val == null || val === "") return "—";
  const n = Number(val);
  if (Number.isNaN(n)) return "—";
  return `${Math.round(n)} ms`;
}

/** Format optional count */
function fmtOptCount(val) {
  if (val == null) return "—";
  const n = Number(val);
  if (Number.isNaN(n)) return "—";
  return new Intl.NumberFormat("en-AU").format(n);
}

/**
 * Wait for window.supabase to have an active session.
 * @param {number} [timeoutMs=8000]
 * @returns {Promise<string|null>}
 */
function waitForSession(timeoutMs = 8000) {
  return new Promise((resolve) => {
    const start = Date.now();
    const check = async () => {
      try {
        const { data } = await window.supabase?.auth?.getSession();
        const token = data?.session?.access_token;
        if (token) {
          resolve(token);
          return;
        }
      } catch {
        /* not ready yet */
      }
      if (Date.now() - start > timeoutMs) {
        resolve(null);
        return;
      }
      setTimeout(check, 200);
    };
    check();
  });
}

// ── Entry point ────────────────────────────────────────────────────────────────

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () =>
    init().catch(console.error)
  );
} else {
  init().catch(console.error);
}
