/**
 * lib/settings/integrations/webflow.js — Webflow integration module
 *
 * Handles Webflow workspace/site connections and per-site configuration.
 * Flow: Connect -> OAuth -> Return to settings -> Configure sites
 */

import {
  disconnectWebflowConnection,
  listWebflowConnections,
  listWebflowSites,
  setWebflowSiteAutoPublish,
  setWebflowSiteSchedule,
  startWebflowConnection,
} from "/app/lib/webflow-sites.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";
import { formatRelativeDate } from "/app/lib/settings/integrations/shared.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

// ── State ───────────────────────────────────────────────────────────────────────

let sitesState = {
  connectionId: null,
  sites: [],
  filteredSites: [],
  currentPage: 1,
  sitesPerPage: 10,
  searchQuery: "",
  loading: false,
};

// ── Setup ───────────────────────────────────────────────────────────────────────

export function setupWebflowIntegration() {
  document.addEventListener("click", (event) => {
    const element = event.target.closest("[gnh-action]");
    if (!element) return;
    const action = element.getAttribute("gnh-action");
    if (!action || !action.startsWith("webflow-")) return;
    event.preventDefault();
    handleWebflowAction(action, element);
  });

  const searchInput = document.getElementById("webflowSiteSearch");
  if (searchInput) {
    let debounceTimer;
    searchInput.addEventListener("input", (event) => {
      clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => handleSiteSearch(event), 200);
    });
  }
}

function handleWebflowAction(action, element) {
  switch (action) {
    case "webflow-connect":
      connectWebflow();
      break;
    case "webflow-disconnect": {
      const connectionId = element.getAttribute("gnh-id");
      if (connectionId) disconnectWebflow(connectionId);
      else console.warn("webflow-disconnect: missing gnh-id attribute");
      break;
    }
    case "webflow-refresh":
      loadWebflowConnections();
      break;
    case "webflow-sites-refresh":
      if (sitesState.connectionId) loadWebflowSites(sitesState.connectionId, 1);
      break;
    case "webflow-sites-prev":
      if (sitesState.currentPage > 1)
        renderWebflowSites(sitesState.currentPage - 1);
      break;
    case "webflow-sites-next": {
      const totalPages = Math.ceil(
        sitesState.filteredSites.length / sitesState.sitesPerPage
      );
      if (sitesState.currentPage < totalPages)
        renderWebflowSites(sitesState.currentPage + 1);
      break;
    }
    default:
      break;
  }
}

// ── Connections ──────────────────────────────────────────────────────────────────

export async function loadWebflowConnections() {
  try {
    const connections = await listWebflowConnections();

    const connectionsList = document.getElementById("webflowConnectionsList");
    const emptyState = document.getElementById("webflowEmptyState");
    const sitesConfig = document.getElementById("webflowSitesConfig");
    if (!connectionsList) return;

    const template = connectionsList.querySelector(
      '[gnh-template="webflow-connection"]'
    );
    if (!template) {
      console.error("Webflow connection template not found");
      return;
    }

    connectionsList
      .querySelectorAll(".webflow-connection")
      .forEach((el) => el.remove());

    if (!connections || connections.length === 0) {
      if (emptyState) emptyState.style.display = "block";
      if (sitesConfig) sitesConfig.style.display = "none";
      return;
    }
    if (emptyState) emptyState.style.display = "none";

    for (const conn of connections) {
      const clone = template.cloneNode(true);
      clone.style.display = "block";
      clone.removeAttribute("gnh-template");
      clone.classList.add("webflow-connection");

      const nameEl = clone.querySelector(".webflow-name");
      if (nameEl) {
        nameEl.textContent =
          conn.workspace_name ||
          (conn.webflow_workspace_id
            ? `Workspace ${conn.webflow_workspace_id}`
            : "Webflow Connection");
      }

      const dateEl = clone.querySelector(".webflow-connected-date");
      if (dateEl)
        dateEl.textContent = `Connected ${formatRelativeDate(conn.created_at)}`;

      const disconnectBtn = clone.querySelector(
        '[gnh-action="webflow-disconnect"]'
      );
      if (disconnectBtn) disconnectBtn.setAttribute("gnh-id", conn.id);

      connectionsList.appendChild(clone);
    }

    if (sitesConfig && connections.length > 0) {
      sitesConfig.style.display = "block";
      const targetConnectionId = sitesState.connectionId || connections[0].id;
      loadWebflowSites(targetConnectionId);
    }
  } catch (error) {
    console.error("Failed to load Webflow connections:", error);
  }
}

async function connectWebflow() {
  try {
    const response = await startWebflowConnection();
    if (response?.auth_url) {
      window.location.href = response.auth_url;
    } else {
      throw new Error("No OAuth URL returned");
    }
  } catch (error) {
    console.error("Failed to start Webflow OAuth:", error);
    toast("error", "Failed to connect to Webflow. Please try again.");
  }
}

async function disconnectWebflow(connectionId) {
  if (
    !confirm(
      "Are you sure you want to disconnect Webflow? Run on Publish will stop working."
    )
  )
    return;

  try {
    await disconnectWebflowConnection(connectionId);
    toast("success", "Webflow disconnected");
    loadWebflowConnections();
  } catch (error) {
    console.error("Failed to disconnect Webflow:", error);
    toast("error", "Failed to disconnect Webflow");
  }
}

// ── Sites ───────────────────────────────────────────────────────────────────────

export async function loadWebflowSites(connectionId, page = 1) {
  if (sitesState.loading) return;
  sitesState.loading = true;

  const loadingEl = document.getElementById("webflowSitesLoading");
  const emptyEl = document.getElementById("webflowSitesEmpty");
  if (loadingEl) loadingEl.style.display = "block";
  if (emptyEl) emptyEl.style.display = "none";

  try {
    const data = await listWebflowSites(connectionId, { page });
    const sites = Array.isArray(data.sites) ? data.sites : [];

    sitesState.connectionId = connectionId;
    sitesState.sites = sites;
    sitesState.filteredSites = [...sites];
    sitesState.currentPage = page;

    const searchBox = document.getElementById("webflowSitesSearchBox");
    if (searchBox)
      searchBox.style.display = sites.length > 5 ? "block" : "none";

    renderWebflowSites(page);
  } catch (error) {
    console.error("Failed to load Webflow sites:", error);
    toast("error", "Failed to load sites. Please try again.");
  } finally {
    sitesState.loading = false;
    if (loadingEl) loadingEl.style.display = "none";
  }
}

function renderWebflowSites(page = 1) {
  const listEl = document.getElementById("webflowSitesList");
  const emptyEl = document.getElementById("webflowSitesEmpty");
  const loadingEl = document.getElementById("webflowSitesLoading");
  const paginationEl = document.getElementById("webflowSitesPagination");
  const template = listEl?.querySelector('[gnh-template="webflow-site"]');
  if (!listEl || !template) return;
  if (loadingEl) loadingEl.style.display = "none";

  listEl
    .querySelectorAll(".webflow-site-row:not([gnh-template])")
    .forEach((el) => el.remove());

  const sites = sitesState.filteredSites;
  if (sites.length === 0) {
    if (emptyEl) emptyEl.style.display = "block";
    if (paginationEl) paginationEl.style.display = "none";
    return;
  }
  if (emptyEl) emptyEl.style.display = "none";
  sitesState.currentPage = page;

  const startIdx = (page - 1) * sitesState.sitesPerPage;
  const pageSites = sites.slice(startIdx, startIdx + sitesState.sitesPerPage);
  const totalPages = Math.ceil(sites.length / sitesState.sitesPerPage);

  for (const site of pageSites) {
    const clone = template.cloneNode(true);
    clone.style.display = "block";
    clone.removeAttribute("gnh-template");
    clone.dataset.siteId = site.webflow_site_id;
    clone.dataset.connectionId = sitesState.connectionId;

    const nameEl = clone.querySelector(".site-name");
    if (nameEl)
      nameEl.textContent =
        site.display_name || site.site_name || "Unnamed Site";

    const domainEl = clone.querySelector(".site-domain");
    if (domainEl && site.primary_domain)
      domainEl.textContent = site.primary_domain;

    const scheduleSelect = clone.querySelector(".site-schedule");
    if (scheduleSelect) {
      scheduleSelect.value = site.schedule_interval_hours ?? "";
      scheduleSelect.dataset.siteId = site.webflow_site_id;
      scheduleSelect.dataset.connectionId = sitesState.connectionId;
      scheduleSelect.addEventListener("change", handleScheduleChange);
    }

    const autoPublishToggle = clone.querySelector(".site-autopublish");
    if (autoPublishToggle) {
      autoPublishToggle.checked = site.auto_publish_enabled || false;
      autoPublishToggle.dataset.siteId = site.webflow_site_id;
      autoPublishToggle.dataset.connectionId = sitesState.connectionId;
      autoPublishToggle.addEventListener("change", handleAutoPublishToggle);
    }

    listEl.appendChild(clone);
  }

  if (paginationEl) {
    paginationEl.style.display = totalPages > 1 ? "flex" : "none";
    const prevBtn = document.getElementById("webflowSitesPrevPage");
    const nextBtn = document.getElementById("webflowSitesNextPage");
    const pageInfo = document.getElementById("webflowSitesPageInfo");
    if (prevBtn) prevBtn.disabled = page <= 1;
    if (nextBtn) nextBtn.disabled = page >= totalPages;
    if (pageInfo) pageInfo.textContent = `Page ${page} of ${totalPages}`;
  }
}

// ── Site actions ────────────────────────────────────────────────────────────────

async function handleScheduleChange(event) {
  const select = event.target;
  const siteId = select.dataset.siteId;
  const connectionId = select.dataset.connectionId;
  const interval = select.value ? parseInt(select.value, 10) : null;
  select.disabled = true;

  try {
    await setWebflowSiteSchedule(siteId, {
      connectionId,
      scheduleIntervalHours: interval,
    });

    const site = sitesState.sites.find((s) => s.webflow_site_id === siteId);
    if (site) site.schedule_interval_hours = interval;

    if (interval) {
      let autoPublishEnabled = false;
      try {
        await setWebflowSiteAutoPublish(siteId, {
          connectionId,
          enabled: true,
        });
        autoPublishEnabled = true;
      } catch (err) {
        console.error("Failed to auto-enable run-on-publish:", err);
        toast(
          "error",
          "Schedule saved, but run-on-publish could not be enabled automatically."
        );
      }
      if (site) site.auto_publish_enabled = autoPublishEnabled;
      const row = select.closest(".webflow-site-row");
      const rowToggle = row?.querySelector(".site-autopublish");
      if (rowToggle) rowToggle.checked = autoPublishEnabled;
    }

    select.style.borderColor = "#10b981";
    setTimeout(() => {
      select.style.borderColor = "";
    }, 1000);
  } catch (error) {
    console.error("Failed to update schedule:", error);
    toast("error", "Failed to save schedule");
    const site = sitesState.sites.find((s) => s.webflow_site_id === siteId);
    if (site) select.value = site.schedule_interval_hours ?? "";
  } finally {
    select.disabled = false;
  }
}

async function handleAutoPublishToggle(event) {
  const toggle = event.target;
  const siteId = toggle.dataset.siteId;
  const connectionId = toggle.dataset.connectionId;
  const enabled = toggle.checked;
  toggle.disabled = true;

  const row = toggle.closest(".webflow-site-row");
  const statusEl = row?.querySelector(".site-status");
  if (statusEl) {
    statusEl.style.display = "block";
    statusEl.textContent = enabled
      ? "Registering webhook..."
      : "Removing webhook...";
    statusEl.style.color = "#6b7280";
  }

  try {
    await setWebflowSiteAutoPublish(siteId, {
      connectionId,
      enabled,
    });

    const site = sitesState.sites.find((s) => s.webflow_site_id === siteId);
    if (site) site.auto_publish_enabled = enabled;

    if (statusEl) {
      statusEl.textContent = enabled ? "Webhook active" : "";
      statusEl.style.color = "#10b981";
      setTimeout(() => {
        statusEl.style.display = "none";
      }, 2000);
    }
  } catch (error) {
    console.error("Failed to toggle auto-publish:", error);
    toast("error", "Failed to update Run on Publish");
    toggle.checked = !enabled;
    if (statusEl) {
      statusEl.textContent = "Failed to update";
      statusEl.style.color = "#dc2626";
    }
  } finally {
    toggle.disabled = false;
  }
}

function handleSiteSearch(event) {
  const query = event.target.value.toLowerCase().trim();
  sitesState.searchQuery = query;

  if (!query) {
    sitesState.filteredSites = [...sitesState.sites];
  } else {
    sitesState.filteredSites = sitesState.sites.filter(
      (site) =>
        (site.display_name || site.site_name || "")
          .toLowerCase()
          .includes(query) ||
        (site.primary_domain || "").toLowerCase().includes(query)
    );
  }
  renderWebflowSites(1);
}

// ── OAuth callback ──────────────────────────────────────────────────────────────

export function handleWebflowOAuthCallback() {
  const params = new URLSearchParams(window.location.search);
  const webflowConnected = params.get("webflow_connected");
  const webflowSetup = params.get("webflow_setup");
  const connectionId = params.get("webflow_connection_id");
  const webflowError = params.get("webflow_error");

  if (webflowSetup === "true" || webflowConnected) {
    const url = new URL(window.location.href);
    url.searchParams.delete("webflow_connected");
    url.searchParams.delete("webflow_setup");
    url.searchParams.delete("webflow_connection_id");
    window.history.replaceState({}, "", url.toString());

    if (connectionId) sitesState.connectionId = connectionId;
    if (webflowConnected)
      toast("success", "Webflow connected! Configure your sites below.");

    focusWebflowSettings();
    loadWebflowConnections();

    if (window.opener && !window.opener.closed) {
      try {
        window.opener.postMessage(
          {
            source: "gnh-webflow-connect",
            type: "webflow-connect-complete",
            connected: true,
            setup: webflowSetup === "true",
            integration: "webflow",
            connectionId,
          },
          "*"
        );
      } catch (error) {
        console.error("Failed to post Webflow message to opener", error);
      }
      window.setTimeout(() => window.close(), 150);
    }
  } else if (webflowError) {
    toast("error", `Failed to connect Webflow: ${webflowError}`);
    const url = new URL(window.location.href);
    url.searchParams.delete("webflow_error");
    window.history.replaceState({}, "", url.toString());

    if (window.opener && !window.opener.closed) {
      try {
        window.opener.postMessage(
          {
            source: "gnh-webflow-connect",
            type: "webflow-connect-complete",
            connected: false,
            error: webflowError,
            integration: "webflow",
          },
          "*"
        );
      } catch (error) {
        console.error("Failed to post Webflow error to opener", error);
      }
    }
  }
}

function focusWebflowSettings() {
  const targetPath = "/settings/automated-jobs";
  const targetHash = "#webflow";
  const currentPath = window.location.pathname.replace(/\/$/, "");

  if (currentPath !== targetPath) {
    window.location.href = `${targetPath}${targetHash}`;
    return;
  }
  if (window.location.hash !== targetHash) {
    window.location.hash = targetHash;
  }

  setTimeout(() => {
    const section = document.getElementById("webflowSitesConfig");
    if (section) section.scrollIntoView({ behavior: "smooth", block: "start" });
  }, 200);
}
