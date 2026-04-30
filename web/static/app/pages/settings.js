/**
 * pages/settings.js — settings page orchestrator
 *
 * Imports section modules from lib/settings/ and wires them into
 * settings.html containers. Each section module is surface-agnostic
 * (accepts a container param) so the same logic can render in the
 * Webflow extension or other surfaces.
 *
 * Also owns settings navigation (sidebar, tabs, deep-linking) and
 * the org creation modal — previously in gnh-settings.js.
 */

import {
  loadAccountDetails,
  setupAccountActions,
} from "/app/lib/settings/account.js";
import {
  loadMembers,
  loadInvites,
  setupTeamActions,
  getTeamState,
} from "/app/lib/settings/team.js";
import {
  loadPlansAndUsage,
  loadUsageHistory,
  loadBillingSection,
  invalidateUsageCache,
} from "/app/lib/settings/plans.js";
import {
  loadSchedules,
  setupSchedulesActions,
} from "/app/lib/settings/schedules.js";
import {
  setupSlackIntegration,
  loadSlackConnections,
  handleSlackOAuthCallback,
} from "/app/lib/settings/integrations/slack.js";
import {
  setupWebflowIntegration,
  loadWebflowConnections,
  handleWebflowOAuthCallback,
} from "/app/lib/settings/integrations/webflow.js";
import {
  setupGoogleIntegration,
  loadGoogleConnections,
  handleGoogleOAuthCallback,
} from "/app/lib/settings/integrations/google.js";
import { initCreateOrgModal } from "/app/lib/settings/organisations.js";
import { initAdminResetButton } from "/app/lib/admin.js";
import { handleInviteTokenFlow } from "/app/lib/invite-flow.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

// ── Section containers ──────────────────────────────────────────────────────────

function getContainers() {
  return {
    account: document.getElementById("account"),
    team: document.getElementById("team"),
    plans: document.getElementById("plans"),
    schedules: document.getElementById("automated-jobs"),
  };
}

// ── Settings navigation ─────────────────────────────────────────────────────────

const SECTION_MAP = {
  "/settings": "account",
  "/settings/account": "account",
  "/settings/team": "team",
  "/settings/plans": "plans",
  "/settings/billing": "billing",
  "/settings/notifications": "notifications",
  "/settings/analytics": "analytics",
  "/settings/auto-crawl": "automated-jobs",
  "/settings/automation": "automated-jobs",
  "/settings/automated-jobs": "automated-jobs",
};

function setActiveSettingsLink() {
  const path = window.location.pathname.replace(/\/$/, "");
  const currentPath = path === "/settings" ? "/settings/account" : path;

  document.querySelectorAll(".settings-link").forEach((link) => {
    try {
      const linkPath = new URL(link.href).pathname.replace(/\/$/, "");
      if (linkPath === currentPath) {
        link.classList.add("active");
        link.setAttribute("aria-current", "page");
      } else {
        link.classList.remove("active");
        link.removeAttribute("aria-current");
      }
    } catch {
      link.classList.remove("active");
      link.removeAttribute("aria-current");
    }
  });
}

function resolveTargetSectionId() {
  const hash = window.location.hash.replace("#", "");
  if (hash) {
    const hashTarget = document.getElementById(hash);
    if (hashTarget) {
      const section = hashTarget.closest(".settings-section");
      if (section?.id) return section.id;
    }
  }
  const path = window.location.pathname.replace(/\/$/, "");
  return SECTION_MAP[path] || "account";
}

function setActiveSection() {
  const targetId = resolveTargetSectionId();
  const sections = document.querySelectorAll(".settings-section");
  const target = document.getElementById(targetId);
  const hash = window.location.hash.replace("#", "");
  const hashTarget = hash ? document.getElementById(hash) : null;

  sections.forEach((section) => {
    section.classList.toggle("active", section.id === targetId);
  });

  if (hashTarget) {
    hashTarget.scrollIntoView({ behavior: "smooth", block: "start" });
  } else if (target) {
    target.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  if (targetId === "plans" && !window.location.hash) {
    activatePlanTab("planTabCurrent");
  }
  if (targetId === "automated-jobs" && !window.location.hash) {
    activateAutomationTab("autoCrawlWebflowPanel");
  }
  activateTabFromHash();
}

function activateTabFromHash() {
  const hash = window.location.hash.replace("#", "");
  if (!hash) return;
  const target = document.getElementById(hash);
  const panel = target?.closest(".settings-tab-panel");
  if (panel?.id) {
    if (panel.id.startsWith("planTab")) activatePlanTab(panel.id);
    else if (panel.id.startsWith("autoCrawl")) activateAutomationTab(panel.id);
  }
}

function setupSettingsNavigation() {
  setActiveSettingsLink();
  setActiveSection();

  window.addEventListener("hashchange", () => setActiveSection());
  window.addEventListener("popstate", () => {
    setActiveSettingsLink();
    setActiveSection();
  });
}

// ── Tab groups ──────────────────────────────────────────────────────────────────

function activateTabGroup(sectionId, tabAttribute, panelId) {
  const section = document.getElementById(sectionId);
  if (!section) return;

  section.querySelectorAll(`.settings-tab[${tabAttribute}]`).forEach((tab) => {
    const isActive = tab.getAttribute(tabAttribute) === panelId;
    tab.classList.toggle("active", isActive);
    tab.setAttribute("aria-selected", isActive ? "true" : "false");
    tab.setAttribute("tabindex", isActive ? "0" : "-1");
  });

  section.querySelectorAll(".settings-tab-panel").forEach((panel) => {
    const isActive = panel.id === panelId;
    panel.classList.toggle("active", isActive);
    panel.setAttribute("aria-hidden", isActive ? "false" : "true");
  });
}

function activatePlanTab(panelId) {
  activateTabGroup("plans", "data-tab-target", panelId);
}

function activateAutomationTab(panelId) {
  activateTabGroup("automated-jobs", "data-auto-crawl-tab-target", panelId);
}

function setupPlanTabs() {
  const section = document.getElementById("plans");
  if (!section) return;
  section.querySelectorAll(".settings-tab[data-tab-target]").forEach((tab) => {
    tab.addEventListener("click", () => {
      if (tab.dataset.tabTarget) activatePlanTab(tab.dataset.tabTarget);
    });
  });
}

function setupAutomationTabs() {
  const section = document.getElementById("automated-jobs");
  if (!section) return;
  section
    .querySelectorAll(".settings-tab[data-auto-crawl-tab-target]")
    .forEach((tab) => {
      tab.addEventListener("click", () => {
        if (tab.dataset.autoCrawlTabTarget)
          activateAutomationTab(tab.dataset.autoCrawlTabTarget);
      });
    });
}

// ── Invite token handling ───────────────────────────────────────────────────────

async function handleInviteToken() {
  const result = await handleInviteTokenFlow({
    onAccepted: async () => {
      toast("success", "Invite accepted");
      await refreshSections();
    },
    onError: (err) => {
      console.error("Failed to accept invite:", err);
      toast("error", err?.message || "Failed to accept invite");
    },
  });

  if (result?.status === "auth_required") {
    toast("warning", "Sign in or create an account to accept this invite");
  }
}

// ── Billing return ─────────────────────────────────────────────────────────────

// Reads ?billing=success|cancelled set by the Stripe Checkout redirect, shows
// a toast, busts the usage cache so loadPlansAndUsage picks up fresh state
// (new plan, has_stripe_customer flip), and strips the query param so a reload
// does not re-toast.
function handleBillingRedirect() {
  const params = new URLSearchParams(window.location.search);
  const billing = params.get("billing");
  if (!billing) return;

  if (billing === "success") {
    invalidateUsageCache();
    toast("success", "Subscription activated");
  } else if (billing === "cancelled") {
    toast("warning", "Checkout cancelled");
  }

  params.delete("billing");
  const cleanQuery = params.toString();
  const cleanUrl =
    window.location.pathname +
    (cleanQuery ? "?" + cleanQuery : "") +
    window.location.hash;
  window.history.replaceState({}, "", cleanUrl);
}

// ── Refresh (called on org-switch) ──────────────────────────────────────────────

async function refreshSections() {
  // refreshSections runs on org-switch (via gnh:org-switched). The usage
  // cache in plans.js is keyed by the cache module, not by org, so we must
  // bust it here or the new org's plan tab can render the previous org's
  // plan/has_stripe_customer state for up to USAGE_CACHE_TTL.
  invalidateUsageCache();
  const c = getContainers();
  const teamState = getTeamState();
  try {
    await loadAccountDetails(c.account);
    await loadMembers(c.team);
    await loadInvites(c.team);
    await loadPlansAndUsage(c.plans, {
      currentUserRole: teamState.currentUserRole,
    });
    await loadUsageHistory(c.plans);
    await loadBillingSection();
    await loadSchedules(c.schedules);
    await loadSlackConnections();
    await loadWebflowConnections();
    await loadGoogleConnections();
  } catch (err) {
    console.error("ES module refresh failed:", err);
  }
}

// Expose for org-switch refresh from global-nav and legacy code.
window.__esRefreshSections = refreshSections;

// Listen for org-switch events directly.
document.addEventListener("gnh:org-switched", () =>
  refreshSections().catch(console.error)
);

// ── Bootstrap ──────────────────────────────────────────────────────────────────

let _initialised = false;

async function init() {
  if (_initialised) return;
  _initialised = true;

  // Wait for core readiness (Supabase, org init).
  if (window.GNH_APP?.coreReady) {
    await window.GNH_APP.coreReady;
  }

  // Wait for supabase auth (polling with timeout).
  const start = Date.now();
  while (!window.supabase?.auth && Date.now() - start < 5000) {
    await new Promise((r) => setTimeout(r, 100));
  }

  const session = await window.supabase?.auth?.getSession?.();
  if (!session?.data?.session?.user) return;

  // Navigation (sidebar, tabs, deep-linking).
  setupSettingsNavigation();
  setupPlanTabs();
  setupAutomationTabs();

  // Org creation modal.
  initCreateOrgModal({ onCreated: () => refreshSections() });

  // Invite token handling.
  await handleInviteToken();

  // Stripe Checkout return — show toast, bust the usage cache so the new
  // plan/Manage Billing state renders on the first loadPlansAndUsage below,
  // then strip the query param so reloads don't re-toast.
  handleBillingRedirect();

  const c = getContainers();

  // Wire up event listeners for section modules.
  setupAccountActions(c.account, {
    onNameSaved: () => loadMembers(c.team),
  });
  setupTeamActions(c.team);
  setupSchedulesActions(c.schedules);

  // Load section data.
  try {
    await loadAccountDetails(c.account);
    await loadMembers(c.team);
    await loadInvites(c.team);

    const teamState = getTeamState();
    await loadPlansAndUsage(c.plans, {
      currentUserRole: teamState.currentUserRole,
    });
    await loadUsageHistory(c.plans);
    await loadBillingSection();
    await loadSchedules(c.schedules);
  } catch (err) {
    console.error("Failed to initialise settings sections:", err);
    toast("error", "Some settings sections failed to load.");
  }

  // Integration setup and OAuth callbacks.
  setupSlackIntegration();
  setupWebflowIntegration();
  setupGoogleIntegration();
  handleSlackOAuthCallback();
  handleWebflowOAuthCallback();
  await handleGoogleOAuthCallback();

  try {
    await loadSlackConnections();
    await loadWebflowConnections();
    await loadGoogleConnections();
  } catch (err) {
    console.error("Failed to load integration connections:", err);
  }

  // Admin section (only visible to system_admin users).
  await initAdminResetButton("settingsResetDataBtn", {
    containerSelector: "#adminGroup",
    endpoint: "/v1/admin/reset-data",
    warning:
      "WARNING: This will DELETE ALL jobs, tasks, pages and domains!\n\nIntegrations and account data will be preserved.\n\nAre you absolutely sure?",
  });
  await initAdminResetButton("settingsResetSchemaBtn", {
    endpoint: "/v1/admin/reset-db",
    warning:
      "WARNING: This will DROP AND REBUILD THE ENTIRE SCHEMA!\n\nAll data except users and organisations will be permanently deleted, including integrations.\n\nAre you absolutely sure?",
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
