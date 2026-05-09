/**
 * lib/site-view.js — shared site-surface rendering helpers
 *
 * Shared between the dashboard and Webflow Designer extension.
 * Owns common topbar/render logic for usage, orgs, avatar, job cards,
 * recent results, and the mini chart. Surface-specific bootstrapping and
 * action handlers remain local to each entrypoint.
 */

import { createJobCard } from "/app/components/hover-job-card.js";
import { formatDateTime, getInitials } from "/app/lib/formatters.js";
import { filterJobsByDomains } from "/app/lib/site-jobs.js";

function clearNode(node) {
  if (!node) return;
  while (node.firstChild) {
    node.removeChild(node.firstChild);
  }
}

function asCount(value) {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return 0;
  }
  return Math.max(0, Math.floor(value));
}

function getNoJobTextTarget(noJobState, noJobText) {
  if (noJobText) {
    return noJobText;
  }
  return noJobState?.querySelector?.(".detail") || null;
}

function show(node) {
  node?.classList?.remove("hidden");
}

function hide(node) {
  node?.classList?.add("hidden");
}

function setText(node, value) {
  if (node) {
    node.textContent = value;
  }
}

function getAppOrigin() {
  const fromExtension =
    window.HOVER_EXTENSION_CONFIG?.appOrigin ||
    window.GNH_APP?.apiBaseUrl ||
    "";
  if (fromExtension) {
    try {
      return new URL(fromExtension).origin;
    } catch {
      // Fall through to current origin.
    }
  }
  return window.location.origin;
}

function resolveAssetUrl(path) {
  if (!path) {
    return "";
  }
  if (/^(?:[a-z]+:)?\/\//i.test(path) || path.startsWith("data:")) {
    return path;
  }
  return new URL(path, `${getAppOrigin()}/`).toString();
}

function getIssueCounts(job) {
  const buckets = job.stats?.slow_page_buckets;
  const statsBrokenLinks = asCount(job.stats?.total_broken_links);
  const fallbackBrokenLinks = asCount(job.failed_tasks);

  if (job.stats && buckets) {
    const verySlow = asCount(buckets.over_10s) + asCount(buckets["5_to_10s"]);
    const slow = asCount(buckets["3_to_5s"]);
    return {
      brokenLinks: Math.max(statsBrokenLinks, fallbackBrokenLinks),
      verySlow,
      slow,
    };
  }

  return {
    brokenLinks: fallbackBrokenLinks,
    verySlow: 0,
    slow: 0,
  };
}

async function getGravatarUrl(email, size = 80) {
  const normalised = (email || "").trim().toLowerCase();
  if (!normalised || !globalThis.crypto?.subtle) return "";

  try {
    const data = new TextEncoder().encode(normalised);
    const digest = await globalThis.crypto.subtle.digest("SHA-256", data);
    const hash = [...new Uint8Array(digest)]
      .map((byte) => byte.toString(16).padStart(2, "0"))
      .join("");
    const params = new URLSearchParams({ s: String(size), d: "404" });
    return `https://www.gravatar.com/avatar/${hash}?${params.toString()}`;
  } catch {
    return "";
  }
}

function makeResultCard(job, options = {}) {
  const card = createJobCard(job, {
    context: options.context || "extension",
    compact: Boolean(options.compact),
  });

  if (typeof options.onViewJob === "function") {
    card.addEventListener("hover-job-card:view", (event) => {
      options.onViewJob(event.detail.path, job);
    });
  }

  if (typeof options.onExportJob === "function") {
    card.addEventListener("hover-job-card:export", (event) => {
      options.onExportJob(event.detail.jobId, job);
    });
  }

  return card;
}

export async function renderUserAvatar(options = {}) {
  const { element, displayName = "", email = "", avatarUrl = "" } = options;
  if (!element) {
    return;
  }

  const initials = getInitials(displayName || email || "?");
  const existingImg = element.querySelector("img");
  if (existingImg) {
    existingImg.remove();
  }

  element.textContent = initials;

  const resolvedAvatarUrl = resolveAssetUrl(
    avatarUrl || (await getGravatarUrl(email, 80))
  );
  if (!resolvedAvatarUrl) {
    return;
  }

  const img = document.createElement("img");
  img.alt = displayName || email || "User avatar";
  img.loading = "eager";
  img.decoding = "async";
  const showImage = () => {
    img.style.display = "block";
    element.replaceChildren(img);
  };
  const showInitials = () => {
    element.replaceChildren();
    element.textContent = initials;
  };
  img.addEventListener("load", showImage, { once: true });
  img.addEventListener("error", showInitials, { once: true });
  img.style.display = "none";
  element.appendChild(img);
  img.src = resolvedAvatarUrl;
  if (img.complete) {
    if (img.naturalWidth > 0) {
      showImage();
    } else {
      showInitials();
    }
  }
}

export function renderUsage(options = {}) {
  const {
    usage,
    planNameText,
    planRemainingValue,
    profilePlanText,
    profileUsageText,
  } = options;
  if (!usage) {
    if (planNameText) {
      planNameText.innerHTML = "<strong>Plan:</strong> \u2014";
    }
    setText(planRemainingValue, "\u2014");
    setText(profilePlanText, "Plan");
    setText(profileUsageText, "Usage unavailable");
    return;
  }

  const plan = usage.plan_display_name || usage.plan_name || "Plan";
  const limit = Number(usage.daily_limit || 0).toLocaleString();
  const remaining = Number(usage.daily_remaining || 0).toLocaleString();

  if (planNameText) {
    planNameText.innerHTML = `<strong>Plan:</strong> <strong>${plan}</strong> (${limit} / day)`;
  }
  setText(planRemainingValue, `${remaining} remaining`);
  setText(profilePlanText, `${plan} (${limit} / day)`);
  setText(profileUsageText, `${remaining} remaining today`);
}

export function renderOrganisations(options = {}) {
  const {
    select,
    organisations = [],
    activeOrganisationId = "",
    emptyLabel = "No organisations",
  } = options;
  if (!(select instanceof HTMLSelectElement)) {
    return;
  }

  clearNode(select);

  if (!organisations.length) {
    const option = document.createElement("option");
    option.value = "";
    option.textContent = emptyLabel;
    select.appendChild(option);
    select.disabled = true;
    return;
  }

  select.disabled = false;
  organisations.forEach((organisation) => {
    const option = document.createElement("option");
    option.value = organisation.id;
    option.textContent = organisation.name;
    option.selected = organisation.id === activeOrganisationId;
    select.appendChild(option);
  });
}

export function renderScheduleState(options = {}) {
  const {
    select,
    currentScheduler,
    placeholder = "off",
    allowedValues = ["off", "6", "12", "24", "48"],
  } = options;
  if (!(select instanceof HTMLSelectElement)) {
    return;
  }

  if (!currentScheduler || !currentScheduler.is_enabled) {
    select.value = placeholder;
    return;
  }

  const allowed = new Set(allowedValues);
  const hours = String(currentScheduler.schedule_interval_hours);
  select.value = allowed.has(hours) ? hours : placeholder;
}

export function renderJobState(options = {}) {
  const {
    jobSection,
    job,
    isActiveJobStatus,
    context = "extension",
    onViewJob,
    onExportJob,
  } = options;
  if (!jobSection) {
    return;
  }

  clearNode(jobSection);

  if (!job || !isActiveJobStatus?.(job.status)) {
    hide(jobSection);
    return;
  }

  const card = makeResultCard(job, {
    context,
    onViewJob,
    onExportJob,
  });
  jobSection.appendChild(card);
  show(jobSection);
}

export function renderRecentResults(options = {}) {
  const {
    latestResultsList,
    recentResultsList,
    noJobState,
    noJobText,
    noJobActionButton,
    jobs = [],
    siteDomain = "",
    siteDomainCandidates = [],
    isActiveJobStatus,
    context = "extension",
    onViewJob,
    onExportJob,
    emptySelectionMessage = "Select a site to review its latest report.",
    emptySiteMessage = "No runs yet for this site.",
    emptyCompletedMessage = "No completed runs yet.",
    emptyAllSitesMessage = "No recent runs yet.",
    showEmptyAction = false,
    showAllWhenUnselected = false,
  } = options;

  if (!latestResultsList || !recentResultsList) {
    return;
  }

  clearNode(latestResultsList);
  clearNode(recentResultsList);

  const hasSiteSelection =
    Boolean(siteDomain) || siteDomainCandidates.length > 0;
  const siteJobs =
    !hasSiteSelection && showAllWhenUnselected
      ? jobs
      : filterJobsByDomains(jobs, {
          siteDomain,
          siteDomainCandidates,
        });
  const noJobTextTarget = getNoJobTextTarget(noJobState, noJobText);

  if (!hasSiteSelection && !showAllWhenUnselected) {
    setText(noJobTextTarget, emptySelectionMessage);
    if (noJobActionButton) {
      noJobActionButton.hidden = true;
    }
    show(noJobState);
    return;
  }

  if (siteJobs.length === 0) {
    setText(
      noJobTextTarget,
      !hasSiteSelection && showAllWhenUnselected
        ? emptyAllSitesMessage
        : emptySiteMessage
    );
    if (noJobActionButton) {
      noJobActionButton.hidden =
        !showEmptyAction || (!hasSiteSelection && showAllWhenUnselected);
    }
    show(noJobState);
    return;
  }

  if (noJobActionButton) {
    noJobActionButton.hidden = false;
  }
  hide(noJobState);

  const completedJobs = siteJobs.filter(
    (job) => !isActiveJobStatus?.(job.status)
  );

  if (completedJobs.length === 0) {
    const empty = document.createElement("p");
    empty.className = "detail";
    empty.textContent = emptyCompletedMessage;
    latestResultsList.appendChild(empty);
    return;
  }

  const groupedJobs = completedJobs.slice(0, 6);
  const latestJob = groupedJobs[0] || null;
  const recentJobs = groupedJobs.slice(1, 6);

  if (latestJob) {
    latestResultsList.appendChild(
      makeResultCard(latestJob, {
        context,
        onViewJob,
        onExportJob,
      })
    );
  }

  recentJobs.forEach((job) => {
    recentResultsList.appendChild(
      makeResultCard(job, {
        context,
        compact: true,
        onViewJob,
        onExportJob,
      })
    );
  });
}

export function renderMiniChart(options = {}) {
  const {
    miniChart,
    chartScaleLabels = [],
    jobs = [],
    siteDomain = "",
    siteDomainCandidates = [],
    onViewJob,
  } = options;
  if (!miniChart) {
    return;
  }

  clearNode(miniChart);

  const completedJobs = filterJobsByDomains(jobs, {
    siteDomain,
    siteDomainCandidates,
  })
    .filter(
      (job) =>
        String(job.status || "")
          .trim()
          .toLowerCase() === "completed"
    )
    .slice(0, 12);

  if (completedJobs.length === 0) {
    chartScaleLabels.forEach((label) => {
      label.textContent = "0";
    });
    return;
  }

  const chartRows = completedJobs
    .filter((job) => Boolean(job.stats))
    .map((job) => {
      const { brokenLinks, verySlow, slow } = getIssueCounts(job);
      const errorCount = brokenLinks;
      const okCount = verySlow + slow;
      const totalPages = Math.max(0, Number(job.total_tasks || 0));
      return {
        job,
        errorCount,
        okCount,
        issueTotal: errorCount + okCount,
        totalPages,
      };
    })
    .filter((row) => row.issueTotal > 0 && row.totalPages > 0)
    .reverse();

  if (chartRows.length === 0) {
    chartScaleLabels.forEach((label) => {
      label.textContent = "0";
    });
    return;
  }

  const maxIssues = Math.max(...chartRows.map((row) => row.issueTotal), 1);
  const ticks = [
    maxIssues,
    Math.round(maxIssues * 0.5),
    Math.round(maxIssues * 0.25),
    0,
  ];

  chartScaleLabels.forEach((label, index) => {
    label.textContent = String(ticks[index] ?? 0);
  });

  for (const row of chartRows) {
    const bar = document.createElement("div");
    bar.className = "chart-bar";
    bar.role = "button";
    bar.tabIndex = 0;
    bar.title = `${formatDateTime(row.job.completed_at || row.job.created_at)}\nStatus: Completed\nOK: ${row.okCount}\nError: ${row.errorCount}\nTotal pages: ${Number(row.job.total_tasks || 0).toLocaleString()}`;

    const detailPath = `/jobs/${encodeURIComponent(row.job.id)}`;
    const openDetail = () => {
      if (typeof onViewJob === "function") {
        onViewJob(detailPath, row.job);
      }
    };

    bar.addEventListener("click", openDetail);
    bar.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        openDetail();
      }
    });

    if (row.okCount > 0) {
      const okSegment = document.createElement("div");
      okSegment.className = "chart-bar--warning";
      okSegment.style.height = `${Math.max(
        2,
        Math.min((row.okCount / maxIssues) * 100, 100)
      )}%`;
      bar.appendChild(okSegment);
    }

    if (row.errorCount > 0) {
      const errorSegment = document.createElement("div");
      errorSegment.className = "chart-bar--danger";
      errorSegment.style.height = `${Math.max(
        2,
        Math.min((row.errorCount / maxIssues) * 100, 100)
      )}%`;
      bar.appendChild(errorSegment);
    }

    if (bar.children.length > 0) {
      miniChart.appendChild(bar);
    }
  }
}

export default {
  renderJobState,
  renderMiniChart,
  renderOrganisations,
  renderRecentResults,
  renderScheduleState,
  renderUsage,
  renderUserAvatar,
};
