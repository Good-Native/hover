/**
 * components/hover-job-card.js — job result card
 *
 * Canonical implementation ported from the Webflow extension's buildResultCard()
 * in webflow-designer-extension-cli/public/index.js. Single source of truth for
 * job card rendering across the dashboard and extension.
 *
 * Usage (app):
 *   import { createJobCard } from "/app/components/hover-job-card.js";
 *   const card = createJobCard(jobObject, { context: "dashboard" });
 *
 * Usage (extension — no /app/ path available):
 *   import { createJobCard, setApiFetcher } from "./hover-job-card.js";
 *   setApiFetcher((path) => apiRequest(path, { method: "GET" }));
 *   const card = createJobCard(jobObject, { context: "extension", compact: true });
 *
 * Attributes:
 *   context — "extension" (default) | "dashboard"
 *   compact — present → adds result-card--compact (reduced padding, no shadow)
 *   expanded — present → starts with issue details expanded
 *
 * Properties:
 *   job     — plain job object from the API; setting this triggers a render
 *
 * Events:
 *   hover-job-card:view    — "All" / "View all X" clicked; detail: { jobId, path }
 *   hover-job-card:export  — "Export Results" clicked; detail: { jobId }
 *   hover-job-card:restart — "Restart" clicked (completed jobs); detail: { jobId, job }
 *   hover-job-card:cancel  — "Cancel" clicked (active jobs); detail: { jobId }
 */

// API fetcher — defaults to /app/lib/api-client.js get(); overridden by extension
// via setApiFetcher() since the extension cannot resolve /app/ paths.
let _apiFetcher = null;

async function defaultFetcher(path) {
  const { get } = await import("/app/lib/api-client.js");
  return get(path);
}

/**
 * Override the API fetch function used for issue-tab data.
 * Call this once before creating any cards in the extension context.
 * @param {(path: string) => Promise<any>} fn
 */
export function setApiFetcher(fn) {
  _apiFetcher = fn;
}

function apiFetch(path) {
  return (_apiFetcher ?? defaultFetcher)(path);
}

// ── Constants ──────────────────────────────────────────────────────────────────

const ACTIVE_STATUSES = new Set([
  "pending",
  "queued",
  "initializing",
  "running",
  "in_progress",
  "processing",
  "cancelling",
]);

const APP_ROUTES = {
  viewJob: "/jobs",
  dashboard: "/dashboard",
};

// ── Imperative helper ──────────────────────────────────────────────────────────

/**
 * @param {object} job
 * @param {{ context?: "extension"|"dashboard", expanded?: boolean, compact?: boolean }} [options]
 * @returns {HoverJobCard}
 */
export function createJobCard(job, options = {}) {
  const el = /** @type {HoverJobCard} */ (
    document.createElement("hover-job-card")
  );
  if (options.context) el.setAttribute("context", options.context);
  if (options.expanded) el.setAttribute("expanded", "");
  if (options.compact) el.setAttribute("compact", "");
  el.job = job;
  return el;
}

// ── Web Component ──────────────────────────────────────────────────────────────

class HoverJobCard extends HTMLElement {
  static get observedAttributes() {
    return ["context", "compact"];
  }

  constructor() {
    super();
    /** @type {object|null} */
    this._job = null;
    /** @type {Map<string, object[]>} */
    this._issueCache = new Map();
    /** @type {string|null} Active issue tab key */
    this._activeTabKey = null;
  }

  // ── Public API ──────────────────────────────────────────────────────────────

  /** @param {object} value */
  set job(value) {
    this._job = value;
    this._issueCache.clear();
    this._activeTabKey = null;
    if (this.isConnected) this._render();
  }

  get job() {
    return this._job;
  }

  get context() {
    return this.getAttribute("context") || "extension";
  }

  // ── Lifecycle ───────────────────────────────────────────────────────────────

  connectedCallback() {
    if (this._job) this._render();
  }

  attributeChangedCallback() {
    if (this._job && this.isConnected) this._render();
  }

  // ── Render ──────────────────────────────────────────────────────────────────

  _render() {
    const job = this._job;
    if (!job) {
      this.innerHTML = "";
      return;
    }

    const startExpanded = this.hasAttribute("expanded");
    const context = this.context;

    this.className = `hover-job-card hover-job-card--${context}`;
    this.innerHTML = "";
    this.appendChild(this._buildCard(job, startExpanded));
  }

  // ── Card builder (mirrors buildResultCard from extension/index.js) ───────────

  _buildCard(job, startExpanded = false) {
    const { brokenLinks, verySlow, slow } = this._getIssueCounts(job);
    const normStatus = normaliseStatus(job.status);
    const isActive = isActiveStatus(normStatus);

    const failCount = isActive ? job.failed_tasks : brokenLinks;
    const warnCount = isActive ? 0 : verySlow + slow;
    const successCount = isActive
      ? Math.max(0, (job.completed_tasks || 0) - (job.failed_tasks || 0))
      : Math.max(0, (job.total_tasks || 0) - brokenLinks - verySlow - slow);

    const dateStr = formatShortDate(job.completed_at || job.created_at);
    const metrics = this._getCompletedCardMetrics(job);

    let outcomeDotClass = "dot--success";
    let outcomeLabel = "Completed";
    let statusColour = "var(--status-colour--success)";

    if (normStatus === "cancelled") {
      outcomeDotClass = "dot--neutral";
      outcomeLabel = "Cancelled";
      statusColour = "var(--status-colour--neutral)";
    } else if (normStatus === "blocked") {
      // Amber rather than red — a WAF block is a site-owner decision,
      // not a Hover failure. Keeps the support framing right.
      outcomeDotClass = "dot--warning";
      outcomeLabel = "Blocked by site";
      statusColour = "var(--status-colour--warning)";
    } else if (isActive) {
      outcomeDotClass = "dot--warning";
      outcomeLabel = statusLabelForJob(normStatus);
      statusColour =
        normStatus === "running" || normStatus === "initializing"
          ? "var(--status-colour--success)"
          : "var(--status-colour--warning)";
    } else if (normStatus === "archived") {
      outcomeDotClass = "dot--success";
      outcomeLabel = statusLabelForJob(normStatus);
      statusColour = "var(--status-colour--success)";
    } else if (normStatus !== "completed") {
      outcomeDotClass = "dot--danger";
      outcomeLabel = "Error";
      statusColour = "var(--status-colour--danger)";
    }

    if (job.total_tasks > 0) {
      outcomeLabel = `${job.total_tasks.toLocaleString()} ${outcomeLabel}`;
    }

    // ── Card root ────────────────────────────────────────────────────────────
    const compact = this.hasAttribute("compact");
    const card = el(
      "div",
      compact
        ? "result-card result-card--complete result-card--compact"
        : "result-card result-card--complete"
    );
    if (isActive) card.classList.add("result-card--active");

    // ── Main ─────────────────────────────────────────────────────────────────
    const main = el("div", "result-card-main");

    // Header: status + summary
    const header = el("div", "result-card-header");

    // Status column
    const statusEl = el("div", "result-card-status");
    const statusLine = el("div", "result-card-status-line");

    const statusIcon = el("span");
    statusIcon.setAttribute("aria-hidden", "true");
    statusIcon.style.color = statusColour;
    if (normStatus === "completed") {
      statusIcon.className =
        "icon icon--small icon--tick result-card-status-icon";
    } else if (isActive) {
      statusIcon.className = iconClassForJob(normStatus);
    } else {
      statusIcon.className = `dot ${outcomeDotClass} result-card-status-dot`;
    }

    const statusLabel = el("span", "result-card-status-label");
    statusLabel.textContent = outcomeLabel;
    statusLabel.style.color = statusColour;

    statusLine.append(statusIcon, statusLabel);

    const timestamp = el("p", "result-card-timestamp");
    timestamp.textContent = dateStr;

    statusEl.append(statusLine, timestamp);
    header.appendChild(statusEl);

    // Summary column (counts + metrics)
    const summary = el("div", "result-card-summary");
    const summaryRow = el("div", "result-card-summary-row");

    for (const item of [
      { dotClass: "dot--success", label: "good", value: successCount },
      { dotClass: "dot--warning", label: "ok", value: warnCount },
      { dotClass: "dot--danger", label: "error", value: failCount },
    ]) {
      const stat = el("span", "result-card-summary-stat");
      const dot = el("span", `dot ${item.dotClass}`);
      stat.appendChild(dot);
      stat.appendChild(
        document.createTextNode(` ${item.value.toLocaleString()} ${item.label}`)
      );
      summaryRow.appendChild(stat);
    }
    summary.appendChild(summaryRow);

    if (metrics.length > 0) {
      const metaRow = el("div", "result-card-summary-meta");
      for (const m of metrics) {
        const metricItem = el("span", "result-card-summary-meta-item");
        metricItem.textContent = `${m.label}: ${m.value}`;
        metaRow.appendChild(metricItem);
      }
      summary.appendChild(metaRow);
    }

    header.appendChild(summary);
    main.appendChild(header);
    card.appendChild(main);

    // ── Footer (issues + actions) ────────────────────────────────────────────
    const footer = el("div", "result-card-footer");
    const issuesRow = el("div", "result-card-issues");
    const details = el("div", "result-card-details hidden");

    const issuesContainer = el("div", "issues-detail");
    const tablePanel = el("div", "issues-table hidden");

    const tabDefs = [
      {
        dotClass: "dot--danger",
        label: "missing",
        count: brokenLinks,
        key: "broken",
      },
      {
        dotClass: "dot--danger",
        label: "very slow",
        count: verySlow,
        key: "veryslow",
      },
      { dotClass: "dot--warning", label: "slow", count: slow, key: "slow" },
    ];

    let hasAnyIssues = false;
    const tabElements = [];
    let firstTab = null;
    let firstTabKey = null;

    const activateTab = (tab, key) => {
      this._activeTabKey = key;
      for (const t of tabElements) t.setAttribute("aria-pressed", "false");
      tab.setAttribute("aria-pressed", "true");
      tablePanel.classList.remove("hidden");
      this._renderIssuesTable(tablePanel, job, key).catch((err) => {
        console.error("Failed to load issues:", err);
        tablePanel.replaceChildren();
        const msg = el("p", "detail");
        msg.textContent = "Could not load issues.";
        msg.setAttribute("role", "alert");
        tablePanel.appendChild(msg);
      });
    };

    for (const def of tabDefs) {
      if (def.count <= 0) continue;
      hasAnyIssues = true;

      const tab = el("button", "btn btn--text");
      tab.type = "button";
      tab.dataset.tabKey = def.key;
      tab.setAttribute("aria-pressed", "false");
      const tabDot = el("span", `dot ${def.dotClass}`);
      const tabLabel = el("span");
      tabLabel.textContent = `${def.count.toLocaleString()} ${def.label}`;
      const tabArrow = el(
        "span",
        "icon icon--small icon--arrow icon--arrow--right"
      );
      tabArrow.setAttribute("aria-hidden", "true");
      tab.append(tabDot, tabLabel, tabArrow);

      tab.addEventListener("click", () => {
        const wasActive = tab.getAttribute("aria-pressed") === "true";
        for (const t of tabElements) t.setAttribute("aria-pressed", "false");
        if (wasActive) {
          tablePanel.classList.add("hidden");
          details.classList.add("hidden");
          card.classList.remove("result-card-expanded");
          this._activeTabKey = null;
          return;
        }
        details.classList.remove("hidden");
        card.classList.add("result-card-expanded");
        activateTab(tab, def.key);
      });

      issuesRow.appendChild(tab);
      tabElements.push(tab);
      if (!firstTab) {
        firstTab = tab;
        firstTabKey = def.key;
      }
    }

    if (hasAnyIssues) {
      issuesContainer.appendChild(tablePanel);
      details.appendChild(issuesContainer);
    }

    footer.appendChild(issuesRow);

    // Footer actions
    const actions = el("div", "result-card-actions");

    const isExtension = this.context === "extension";

    if (!isActive && !isExtension) {
      // Completed jobs: Restart (not in extension — no restart API there)
      const restartBtn = el("button", "btn btn--ghost btn--sm");
      restartBtn.type = "button";
      restartBtn.textContent = "Restart";
      restartBtn.addEventListener("click", () => {
        this.dispatchEvent(
          new CustomEvent("hover-job-card:restart", {
            bubbles: true,
            detail: { jobId: job.id, job },
          })
        );
      });
      actions.appendChild(restartBtn);
    } else if (isActive && !isExtension) {
      // Active jobs: Cancel (not in extension — no cancel API there)
      const cancelBtn = el("button", "btn btn--ghost btn--sm");
      cancelBtn.type = "button";
      cancelBtn.textContent = "Cancel";
      cancelBtn.addEventListener("click", () => {
        this.dispatchEvent(
          new CustomEvent("hover-job-card:cancel", {
            bubbles: true,
            detail: { jobId: job.id },
          })
        );
      });
      actions.appendChild(cancelBtn);
    }

    // All → button — always visible for every job state and context
    const viewBtn = el("button", "btn btn--secondary btn--sm corners--right");
    viewBtn.type = "button";
    const viewArrow = el(
      "span",
      "icon icon--small icon--arrow icon--arrow--right"
    );
    viewArrow.setAttribute("aria-hidden", "true");
    const viewLabel = el("span");
    viewLabel.textContent = "All";
    viewBtn.append(viewArrow, viewLabel);
    viewBtn.addEventListener("click", () => {
      const path = job.id
        ? `${APP_ROUTES.viewJob}/${encodeURIComponent(job.id)}`
        : APP_ROUTES.dashboard;
      this.dispatchEvent(
        new CustomEvent("hover-job-card:view", {
          bubbles: true,
          detail: { jobId: job.id, path },
        })
      );
    });
    actions.appendChild(viewBtn);

    footer.appendChild(actions);

    card.appendChild(footer);

    // Export button — completed jobs only
    if (!isActive) {
      const csvBtn = el("button", "btn btn--ghost btn--xs");
      csvBtn.type = "button";
      csvBtn.innerHTML = `<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3v11"/><path d="M8 10l4 4 4-4"/><path d="M4 18v2h16v-2"/></svg> Export Results`;
      csvBtn.addEventListener("click", () => {
        this.dispatchEvent(
          new CustomEvent("hover-job-card:export", {
            bubbles: true,
            detail: { jobId: job.id },
          })
        );
      });

      const detailActions = el("div", "result-card-pills");
      detailActions.appendChild(csvBtn);
      details.appendChild(detailActions);
    }

    if (details.children.length > 0) card.appendChild(details);

    // Auto-expand if attribute set
    if (startExpanded) {
      card.classList.add("result-card-expanded");
      details.classList.remove("hidden");
      if (hasAnyIssues && firstTab && firstTabKey) {
        activateTab(firstTab, firstTabKey);
      }
    }

    return card;
  }

  // ── Issues table ─────────────────────────────────────────────────────────────

  async _renderIssuesTable(panel, job, tabKey) {
    // Clear
    panel.innerHTML = "";
    const loading = el("p", "detail");
    loading.textContent = "Loading…";
    panel.appendChild(loading);

    let tasks;
    try {
      if (this._issueCache.has(tabKey)) {
        tasks = this._issueCache.get(tabKey);
      } else {
        tasks = await fetchIssueTasks(job.id, tabKey);
        this._issueCache.set(tabKey, tasks);
      }
    } catch (err) {
      if (this._activeTabKey !== tabKey) return;
      console.error("Failed to load issue tasks:", err);
      panel.replaceChildren();
      const failed = el("p", "detail");
      failed.textContent = "Could not load issue details.";
      panel.appendChild(failed);
      return;
    }

    if (this._activeTabKey !== tabKey) return;
    panel.innerHTML = "";

    const labels = {
      broken: ["Broken URL", "Found at"],
      veryslow: ["URL", "Response time"],
      slow: ["URL", "Response time"],
    };
    const [col1Label, col2Label] = labels[tabKey] || ["URL", "Details"];

    const rows = tasks.slice(0, 20);

    if (rows.length === 0) {
      const noData = el("p", "detail");
      noData.textContent =
        tabKey === "broken"
          ? "No broken links found for this run."
          : tabKey === "veryslow"
            ? "No very slow pages found for this run."
            : "No slow pages found for this run.";
      panel.appendChild(noData);
    } else {
      const body = el("div", "issues-table-body");
      const col1 = el("div", "issues-table-col");
      const col2 = el("div", "issues-table-col");

      const h1 = el("div", "issues-table-heading");
      h1.textContent = col1Label;
      col1.appendChild(h1);
      const h2 = el("div", "issues-table-heading");
      h2.textContent = col2Label;
      col2.appendChild(h2);

      for (const task of rows) {
        const leftText = toPathDisplay(task.path || task.url);
        const rightText =
          tabKey === "broken"
            ? toPathDisplay(task.source_url)
            : (() => {
                const rt = taskResponseTime(task);
                return rt != null ? `${rt.toLocaleString()}ms` : "—";
              })();

        const leftHref = toAbsoluteUrl(task.url || task.path, task.url);
        const rightHref =
          tabKey === "broken" ? toAbsoluteUrl(task.source_url) : null;

        col1.appendChild(buildTableCell(leftText, leftHref));
        col2.appendChild(buildTableCell(rightText, rightHref));
      }

      body.append(col1, col2);
      panel.appendChild(body);
    }

    // "View all" footer
    const tableFooter = el("div", "issues-table-footer");
    const viewAllBtn = el("button", "btn btn--tertiary btn--sm");
    viewAllBtn.type = "button";
    viewAllBtn.textContent =
      tabKey === "broken"
        ? "View all broken links"
        : tabKey === "veryslow"
          ? "View all very slow pages"
          : "View all slow pages";
    viewAllBtn.addEventListener("click", () => {
      const path = job.id
        ? `${APP_ROUTES.viewJob}/${encodeURIComponent(job.id)}`
        : APP_ROUTES.dashboard;
      this.dispatchEvent(
        new CustomEvent("hover-job-card:view", {
          bubbles: true,
          detail: { jobId: job.id, path },
        })
      );
    });
    tableFooter.appendChild(viewAllBtn);
    panel.appendChild(tableFooter);
  }

  // ── Data helpers ─────────────────────────────────────────────────────────────

  _getIssueCounts(job) {
    const buckets = job.stats?.slow_page_buckets;
    const statsBroken = asCount(job.stats?.total_broken_links);
    const fallbackBroken = asCount(job.failed_tasks);

    if (job.stats && buckets) {
      const verySlow = asCount(buckets.over_10s) + asCount(buckets["5_to_10s"]);
      const slow = asCount(buckets["3_to_5s"]);
      return {
        brokenLinks: Math.max(statsBroken, fallbackBroken),
        verySlow,
        slow,
      };
    }
    return { brokenLinks: fallbackBroken, verySlow: 0, slow: 0 };
  }

  _getCompletedCardMetrics(job) {
    const metrics = [];
    if (
      typeof job.avg_time_per_task_seconds === "number" &&
      Number.isFinite(job.avg_time_per_task_seconds) &&
      job.avg_time_per_task_seconds > 0
    ) {
      metrics.push({
        label: "Avg",
        value: `${Math.round(job.avg_time_per_task_seconds * 1000).toLocaleString()}ms`,
      });
    }
    const savedMs = fmtMetricMs(getSavedTimeMs(job));
    if (savedMs) metrics.push({ label: "Saved", value: savedMs });
    return metrics;
  }
}

customElements.define("hover-job-card", HoverJobCard);

// ── Module-private helpers ─────────────────────────────────────────────────────

/** Quick element factory */
function el(tag, className = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  return node;
}

function normaliseStatus(status) {
  return (status || "").trim().toLowerCase();
}

function isActiveStatus(status) {
  return ACTIVE_STATUSES.has(normaliseStatus(status));
}

function statusLabelForJob(status) {
  if (status === "completed") return "Done";
  if (status === "running" || status === "initializing") return "In progress";
  if (status === "pending") return "Starting up";
  if (status === "cancelled") return "Cancelled";
  if (status === "archived") return "Archived";
  if (status === "blocked") return "Blocked by site";
  return "Error";
}

function iconClassForJob(status) {
  const base = "job-status-icon";
  if (status === "completed") return `${base} ${base}--completed`;
  if (status === "running" || status === "initializing")
    return `${base} ${base}--running`;
  if (status === "pending" || status === "queued")
    return `${base} ${base}--pending`;
  if (status === "archived") return `${base} ${base}--completed`;
  if (status === "blocked") return `${base} ${base}--warning`;
  return `${base} ${base}--error`;
}

function asCount(value) {
  if (typeof value !== "number" || !Number.isFinite(value)) return 0;
  return Math.max(0, Math.floor(value));
}

function getSavedTimeMs(job) {
  const statsSavedMs = job.stats?.cache_warming_effect?.total_time_saved_ms;
  if (typeof statsSavedMs === "number" && Number.isFinite(statsSavedMs)) {
    return Math.max(0, Math.round(statsSavedMs));
  }
  const statsSavedSeconds =
    job.stats?.cache_warming_effect?.total_time_saved_seconds;
  if (
    typeof statsSavedSeconds === "number" &&
    Number.isFinite(statsSavedSeconds)
  ) {
    return Math.max(0, Math.round(statsSavedSeconds * 1000));
  }
  if (
    typeof job.duration_seconds === "number" &&
    Number.isFinite(job.duration_seconds)
  ) {
    return Math.max(0, Math.round(job.duration_seconds * 1000));
  }
  return null;
}

function fmtMetricMs(value) {
  if (value === null || !Number.isFinite(value)) return null;
  return `${Math.max(0, Math.round(value)).toLocaleString()}ms`;
}

function formatShortDate(value) {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  const months = [
    "Jan",
    "Feb",
    "Mar",
    "Apr",
    "May",
    "Jun",
    "Jul",
    "Aug",
    "Sep",
    "Oct",
    "Nov",
    "Dec",
  ];
  const day = d.getDate();
  const suffix =
    day % 10 === 1 && day !== 11
      ? "st"
      : day % 10 === 2 && day !== 12
        ? "nd"
        : day % 10 === 3 && day !== 13
          ? "rd"
          : "th";
  const h = d.getHours() % 12 || 12;
  const min = d.getMinutes().toString().padStart(2, "0");
  const ampm = d.getHours() >= 12 ? "pm" : "am";
  return `${day}${suffix} ${months[d.getMonth()]} ${h}:${min}${ampm}`;
}

function taskResponseTime(task) {
  if (
    typeof task.second_response_time === "number" &&
    Number.isFinite(task.second_response_time) &&
    task.second_response_time > 0
  ) {
    return task.second_response_time;
  }
  if (
    typeof task.response_time === "number" &&
    Number.isFinite(task.response_time) &&
    task.response_time > 0
  ) {
    return task.response_time;
  }
  return null;
}

function toPathDisplay(value) {
  if (!value) return "—";
  const trimmed = value.trim();
  if (!trimmed) return "—";
  if (trimmed.startsWith("/")) return trimmed;
  try {
    const parsed = new URL(trimmed);
    return `${parsed.pathname || "/"}${parsed.search}${parsed.hash}` || "/";
  } catch {
    return trimmed;
  }
}

function toAbsoluteUrl(value, fallbackUrl) {
  if (!value) return null;
  const trimmed = value.trim();
  if (!trimmed) return null;
  try {
    return new URL(trimmed).toString();
  } catch {
    /* continue */
  }
  if (!trimmed.startsWith("/")) return null;
  if (fallbackUrl) {
    try {
      const base = new URL(fallbackUrl);
      return new URL(trimmed, `${base.protocol}//${base.host}`).toString();
    } catch {
      /* continue */
    }
  }
  return null;
}

function buildTableCell(text, href) {
  const row = el("div", "issues-table-row");
  const cell = el("span", "issues-table-cell");
  if (href) {
    const a = el("a", "issues-table-link");
    a.href = href;
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.textContent = text;
    cell.appendChild(a);
  } else {
    cell.textContent = text;
  }
  row.appendChild(cell);
  return row;
}

async function fetchIssueTasks(jobId, tabKey) {
  const base = `/v1/jobs/${encodeURIComponent(jobId)}/tasks?limit=200`;
  const query =
    tabKey === "broken"
      ? `${base}&status=failed&sort=-created_at`
      : `${base}&sort=-second_response_time`;

  const response = await apiFetch(query);
  const tasks = Array.isArray(response?.tasks) ? response.tasks : [];

  if (tabKey === "broken") return tasks;

  const withTimes = tasks
    .map((t) => ({ task: t, rt: taskResponseTime(t) }))
    .filter((i) => i.rt !== null);

  if (tabKey === "veryslow") {
    return withTimes
      .filter((i) => i.rt >= 5000)
      .sort((a, b) => b.rt - a.rt)
      .map((i) => i.task);
  }
  return withTimes
    .filter((i) => i.rt >= 3000 && i.rt < 5000)
    .sort((a, b) => b.rt - a.rt)
    .map((i) => i.task);
}

export default { createJobCard };

// Bridge: expose on window for non-module consumers (Webflow extension).
window.HoverJobCard = { createJobCard, setApiFetcher };
