/**
 * pages/dashboard.js — module-native dashboard shell
 *
 * Uses the same site-focused shell model as the Webflow extension:
 * org switcher, quota badge, per-site scheduler, run-now action, and
 * latest/past report surfaces.
 */

import { get, post } from "/app/lib/api-client.js";
import {
  onAuthStateChange,
  getSession,
  signOut,
} from "/app/lib/auth-session.js";
import { showToast } from "/app/components/hover-toast.js";
import {
  initSurfaceShell,
  renderProfileMenuSummary,
} from "/app/lib/shell-nav.js";
import { initSurfacePage } from "/app/lib/surface-context.js";
import {
  loadOrganisationContext,
  switchOrganisation as switchOrganisationApi,
} from "/app/lib/organisation-api.js";
import { downloadJobExport } from "/app/lib/job-export.js";
import {
  renderJobState as renderSharedJobState,
  renderMiniChart as renderSharedMiniChart,
  renderOrganisations as renderSharedOrganisations,
  renderRecentResults as renderSharedRecentResults,
  renderScheduleState as renderSharedScheduleState,
  renderUsage as renderSharedUsage,
  renderUserAvatar,
} from "/app/lib/site-view.js";
import {
  findSchedulerByDomain,
  saveSchedulerForDomain,
  disableScheduler,
} from "/app/lib/scheduler-api.js";
import { ensureSupabaseClient } from "/app/lib/supabase-client.js";
import {
  buildChartJobsSignature,
  buildCompletedJobsSignature,
  fetchJobs,
  normaliseDomain,
  pickLatestJobByDomains,
  subscribeToJobUpdates,
} from "/app/lib/site-jobs.js";
import {
  ensureDomainByName,
  getDomains,
  loadOrganisationDomains,
  setupDomainSearchInput,
} from "/app/lib/domain-search.js";

const ACTIVE_ORG_STORAGE_KEY = "gnh_active_org_id";
const SELECTED_DOMAIN_STORAGE_KEY = "gnh_dashboard_selected_domain";
const ACTIVE_JOB_STATUSES = new Set([
  "pending",
  "queued",
  "initializing",
  "running",
  "in_progress",
  "processing",
]);
const SCHEDULE_PLACEHOLDER = "off";
const SCHEDULE_OPTIONS = new Set(["off", "6", "12", "24", "48"]);
const APP_ROUTES = {
  auth: "/extension-auth.html",
  settings: "/settings/account",
  viewJob: "/jobs",
  home: "/dashboard",
  help: "/dashboard",
  feedback: "/dashboard",
};

let authSubscriptionCleanup = null;
let jobsSubscriptionCleanup = null;
let statusToastTimer = null;
let initialised = false;
let shellChrome = null;

const state = {
  session: null,
  activeOrganisationId: "",
  organisations: [],
  usage: null,
  selectedDomain: normaliseDomain(
    window.localStorage.getItem(SELECTED_DOMAIN_STORAGE_KEY) || ""
  ),
  siteDomainCandidates: [],
  currentScheduler: null,
  currentJob: null,
  userAvatarUrl: "",
  userEmail: "",
  userDisplayName: "",
  lastCompletedJobsSignature: "",
  lastChartJobsSignature: "",
  refreshing: false,
};

const ui = {
  guestState: document.getElementById("guestState"),
  authState: document.getElementById("authState"),
  loginButton: document.getElementById("dashboardLoginButton"),
  signupButton: document.getElementById("dashboardSignupButton"),
  homeButton: document.getElementById("homeButton"),
  profileMenuButton: document.getElementById("profileMenuButton"),
  profileMenuDropdown: document.getElementById("profileMenuDropdown"),
  profileAvatar: document.getElementById("profileAvatar"),
  profileEmail: document.getElementById("profileEmail"),
  profileOrgName: document.getElementById("profileOrgName"),
  profilePlanText: document.getElementById("profilePlanText"),
  profileUsageText: document.getElementById("profileUsageText"),
  notificationsContainer: document.getElementById("notificationsContainer"),
  notificationsButton: document.getElementById("notificationsBtn"),
  notificationsDropdown: document.getElementById("notificationsDropdown"),
  notificationsList: document.getElementById("notificationsList"),
  notificationsBadge: document.getElementById("notificationsBadge"),
  markAllReadButton: document.getElementById("markAllReadBtn"),
  orgSelect: document.getElementById("orgSelect"),
  domainInput: document.getElementById("domainInput"),
  scheduleSelect: document.getElementById("scheduleSelect"),
  runNowButton: document.getElementById("runNowButton"),
  runFirstCheckButton: document.getElementById("runFirstCheckButton"),
  statusBlock: document.getElementById("statusBlock"),
  statusText: document.getElementById("statusText"),
  detailText: document.getElementById("detailText"),
  noJobState: document.getElementById("noJobState"),
  noJobText: document.getElementById("noJobText"),
  jobSection: document.getElementById("jobSection"),
  latestResultsList: document.getElementById("latestResultsList"),
  recentResultsList: document.getElementById("recentResultsList"),
  miniChart: document.getElementById("miniChart"),
  chartScaleLabels: Array.from(
    document.querySelectorAll(".chart-y-scale span")
  ),
  feedbackButton: document.getElementById("feedbackButton"),
  helpButton: document.getElementById("helpButton"),
};

function getActiveOrganisationName() {
  return (
    state.organisations.find(
      (organisation) => organisation.id === state.activeOrganisationId
    )?.name || "Organisation"
  );
}

function getUserMetadataAvatarUrl(user) {
  return (
    user?.user_metadata?.avatar_url ||
    user?.user_metadata?.picture ||
    user?.user_metadata?.avatar ||
    user?.identities?.find?.((identity) => identity?.identity_data?.avatar_url)
      ?.identity_data?.avatar_url ||
    ""
  );
}

function getUserDisplayName(user) {
  return (
    user?.user_metadata?.full_name ||
    user?.user_metadata?.name ||
    user?.email ||
    ""
  );
}

function show(node) {
  node?.classList.remove("hidden");
}

function hide(node) {
  node?.classList.add("hidden");
}

function setText(node, value) {
  if (node) {
    node.textContent = value;
  }
}

function normaliseJobStatus(status) {
  return String(status || "")
    .trim()
    .toLowerCase();
}

function isActiveJobStatus(status) {
  return ACTIVE_JOB_STATUSES.has(normaliseJobStatus(status));
}

function buildAuthUrl(mode = "login") {
  const authUrl = new URL(APP_ROUTES.auth, window.location.origin);
  authUrl.searchParams.set("return_to", window.location.href);
  authUrl.searchParams.set("mode", mode);
  return authUrl.toString();
}

function openAuth(mode = "login") {
  window.location.assign(buildAuthUrl(mode));
}

function setStatus(message, detail = "") {
  if (statusToastTimer !== null) {
    clearTimeout(statusToastTimer);
    statusToastTimer = null;
  }

  ui.statusBlock?.classList.remove("status-block--fading");
  setText(ui.statusText, message);
  setText(ui.detailText, detail);

  if (!message && !detail) {
    return;
  }

  statusToastTimer = window.setTimeout(() => {
    ui.statusBlock?.classList.add("status-block--fading");
    statusToastTimer = window.setTimeout(() => {
      ui.statusBlock?.classList.remove("status-block--fading");
      setText(ui.statusText, "");
      setText(ui.detailText, "");
      statusToastTimer = null;
    }, 500);
  }, 3000);
}

function renderAuthState(isAuthed) {
  if (isAuthed) {
    hide(ui.guestState);
    show(ui.authState);
    return;
  }

  show(ui.guestState);
  hide(ui.authState);
}

async function updateAvatarFromState() {
  await renderUserAvatar({
    element: ui.profileAvatar,
    displayName: state.userDisplayName || state.userEmail,
    email: state.userEmail,
    avatarUrl: state.userAvatarUrl,
  });
}

function renderUsage() {
  renderSharedUsage({
    usage: state.usage,
    profilePlanText: ui.profilePlanText,
    profileUsageText: ui.profileUsageText,
  });
}

function renderOrganisations() {
  renderSharedOrganisations({
    select: ui.orgSelect,
    organisations: state.organisations,
    activeOrganisationId: state.activeOrganisationId,
  });
}

function renderScheduleState() {
  renderSharedScheduleState({
    select: ui.scheduleSelect,
    currentScheduler: state.currentScheduler,
    placeholder: SCHEDULE_PLACEHOLDER,
    allowedValues: [...SCHEDULE_OPTIONS],
  });
}

function renderJobState(job) {
  renderSharedJobState({
    jobSection: ui.jobSection,
    job,
    isActiveJobStatus,
    context: "extension",
    onViewJob: (path) => {
      window.location.href = path;
    },
    onExportJob: (jobId) => {
      void exportJob(jobId);
    },
  });
}

function renderRecentResults(jobs) {
  renderSharedRecentResults({
    latestResultsList: ui.latestResultsList,
    recentResultsList: ui.recentResultsList,
    noJobState: ui.noJobState,
    noJobText: ui.noJobText,
    noJobActionButton: ui.runFirstCheckButton,
    jobs,
    siteDomain: state.selectedDomain,
    siteDomainCandidates: state.siteDomainCandidates,
    isActiveJobStatus,
    context: "extension",
    onViewJob: (path) => {
      window.location.href = path;
    },
    onExportJob: (jobId) => {
      void exportJob(jobId);
    },
    emptySelectionMessage: "Select a site to review its latest report.",
    emptySiteMessage: `No runs yet for ${state.selectedDomain}.`,
    emptyAllSitesMessage: "No recent runs yet.",
    showEmptyAction: Boolean(state.selectedDomain),
    showAllWhenUnselected: true,
  });
}

function renderMiniChart(jobs) {
  renderSharedMiniChart({
    miniChart: ui.miniChart,
    chartScaleLabels: ui.chartScaleLabels,
    jobs,
    siteDomain: state.selectedDomain,
    siteDomainCandidates: state.siteDomainCandidates,
    onViewJob: (path) => {
      window.location.href = path;
    },
  });
}

function setDisabledAll(disabled) {
  [
    ui.orgSelect,
    ui.domainInput,
    ui.scheduleSelect,
    ui.runNowButton,
    ui.runFirstCheckButton,
  ].forEach((control) => {
    if (
      control instanceof HTMLButtonElement ||
      control instanceof HTMLInputElement ||
      control instanceof HTMLSelectElement
    ) {
      control.disabled = disabled;
    }
  });
}

function updateDomainInput() {
  if (ui.domainInput instanceof HTMLInputElement) {
    ui.domainInput.value = state.selectedDomain || "";
  }
}

function persistSelectedDomain() {
  if (state.selectedDomain) {
    window.localStorage.setItem(
      SELECTED_DOMAIN_STORAGE_KEY,
      state.selectedDomain
    );
    return;
  }
  window.localStorage.removeItem(SELECTED_DOMAIN_STORAGE_KEY);
}

function applySelectedDomain(domain) {
  const nextDomain = normaliseDomain(domain);
  if (nextDomain !== state.selectedDomain) {
    state.lastCompletedJobsSignature = "";
    state.lastChartJobsSignature = "";
  }

  state.selectedDomain = nextDomain;
  state.siteDomainCandidates = state.selectedDomain
    ? [state.selectedDomain]
    : [];
  persistSelectedDomain();
  updateDomainInput();
}

async function waitForSupabaseClient(timeoutMs = 5000) {
  const start = Date.now();

  while (Date.now() - start < timeoutMs) {
    try {
      return ensureSupabaseClient();
    } catch (_error) {
      await new Promise((resolve) => {
        window.setTimeout(resolve, 50);
      });
    }
  }

  throw new Error("Supabase client did not initialise in time.");
}

async function ensureSelectedDomain() {
  if (state.selectedDomain) {
    return;
  }

  const stored = normaliseDomain(
    window.localStorage.getItem(SELECTED_DOMAIN_STORAGE_KEY) || ""
  );
  if (stored) {
    applySelectedDomain(stored);
  }
}

async function loadOrganisationState() {
  const context = await loadOrganisationContext();
  state.organisations = Array.isArray(context.organisations)
    ? context.organisations
    : [];
  state.activeOrganisationId = context.activeOrganisationId || "";
  state.usage = context.usage || null;
  if (state.activeOrganisationId) {
    window.localStorage.setItem(
      ACTIVE_ORG_STORAGE_KEY,
      state.activeOrganisationId
    );
  }
}

async function loadCurrentSchedule() {
  if (!state.selectedDomain) {
    state.currentScheduler = null;
    renderScheduleState();
    return;
  }

  state.currentScheduler = await findSchedulerByDomain(state.selectedDomain);
  renderScheduleState();
}

async function refreshSiteResults() {
  const jobs = await fetchJobs({ limit: 50, include: "stats" });

  if (!state.selectedDomain) {
    const firstJobDomain = normaliseDomain(
      jobs[0]?.domains?.name || jobs[0]?.domain || ""
    );
    if (firstJobDomain) {
      applySelectedDomain(firstJobDomain);
    }
  }

  state.currentJob = pickLatestJobByDomains(jobs, {
    siteDomain: state.selectedDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  });

  renderJobState(state.currentJob);

  const completedSignature = buildCompletedJobsSignature(
    jobs,
    {
      siteDomain: state.selectedDomain,
      siteDomainCandidates: state.siteDomainCandidates,
    },
    isActiveJobStatus
  );

  if (completedSignature !== state.lastCompletedJobsSignature) {
    renderRecentResults(jobs);
    state.lastCompletedJobsSignature = completedSignature;
  }

  const chartSignature = buildChartJobsSignature(jobs, {
    siteDomain: state.selectedDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  });

  if (chartSignature !== state.lastChartJobsSignature) {
    renderMiniChart(jobs);
    state.lastChartJobsSignature = chartSignature;
  }
}

function cleanupJobSubscription() {
  if (jobsSubscriptionCleanup) {
    jobsSubscriptionCleanup();
    jobsSubscriptionCleanup = null;
  }
}

function cleanupNotificationsSubscription() {
  shellChrome?.setActiveOrganisation("");
}

function startJobSubscription() {
  cleanupJobSubscription();
  if (!state.activeOrganisationId) {
    return;
  }

  jobsSubscriptionCleanup = subscribeToJobUpdates({
    orgId: state.activeOrganisationId,
    onUpdate: () => {
      void refreshDashboard({ silent: true });
    },
  });
}

async function refreshDashboard(options = {}) {
  if (state.refreshing) {
    return;
  }

  state.refreshing = true;
  if (!options.silent) {
    setDisabledAll(true);
  }

  try {
    await loadOrganisationState();
    await loadOrganisationDomains();
    await ensureSelectedDomain();
    await Promise.all([loadCurrentSchedule(), refreshSiteResults()]);
    renderUsage();
    renderOrganisations();
    renderProfileMenuSummary({
      emailNode: ui.profileEmail,
      organisationNode: ui.profileOrgName,
      planNode: ui.profilePlanText,
      usageNode: ui.profileUsageText,
      email: state.userEmail,
      organisationName: getActiveOrganisationName(),
      usage: state.usage,
    });
    void updateAvatarFromState();
    renderAuthState(true);
    startJobSubscription();
    shellChrome?.setActiveOrganisation(state.activeOrganisationId);
  } catch (error) {
    console.error("dashboard: failed to refresh", error);
    showToast(error.message || "Failed to refresh the dashboard.", {
      variant: "error",
    });
  } finally {
    if (!options.silent) {
      setDisabledAll(false);
    }
    state.refreshing = false;
  }
}

async function resolveSelectedDomain({ allowCreate = false } = {}) {
  const rawValue =
    ui.domainInput instanceof HTMLInputElement ? ui.domainInput.value : "";
  const nextValue = normaliseDomain(rawValue || state.selectedDomain || "");
  if (!nextValue) {
    applySelectedDomain("");
    await loadCurrentSchedule();
    renderJobState(null);
    renderRecentResults([]);
    renderMiniChart([]);
    return "";
  }

  if (allowCreate) {
    const ensured = await ensureDomainByName(nextValue, { allowCreate: true });
    applySelectedDomain(ensured?.name || nextValue);
  } else {
    applySelectedDomain(nextValue);
  }

  return state.selectedDomain;
}

async function handleDomainCommit({ allowCreate = false } = {}) {
  await resolveSelectedDomain({ allowCreate });
  await Promise.all([loadCurrentSchedule(), refreshSiteResults()]);
}

async function switchOrganisation() {
  if (!(ui.orgSelect instanceof HTMLSelectElement) || !ui.orgSelect.value) {
    return;
  }

  setDisabledAll(true);
  try {
    await switchOrganisationApi(ui.orgSelect.value);
    state.activeOrganisationId = ui.orgSelect.value;
    window.localStorage.setItem(ACTIVE_ORG_STORAGE_KEY, ui.orgSelect.value);
    applySelectedDomain("");
    await refreshDashboard();
  } finally {
    setDisabledAll(false);
  }
}

async function runNow() {
  const domain = await resolveSelectedDomain({ allowCreate: true });
  if (!domain) {
    showToast("Enter a site domain first.", { variant: "error" });
    return;
  }

  setDisabledAll(true);
  try {
    await post("/v1/jobs", {
      domain,
      max_pages: 0,
      use_sitemap: true,
      find_links: true,
    });
    setStatus("Run started.", `Checking ${domain}.`);
    showToast(`Run started for ${domain}`, { variant: "success" });
    await refreshDashboard({ silent: true });
  } catch (error) {
    console.error("dashboard: failed to run job", error);
    showToast(error.message || "Failed to start the run.", {
      variant: "error",
    });
  } finally {
    setDisabledAll(false);
  }
}

async function exportJob(jobId) {
  try {
    const result = await downloadJobExport(jobId);
    if (result.empty) {
      showToast("No tasks to export.", { variant: "warning" });
      return;
    }
    showToast("Export downloaded.", { variant: "success" });
  } catch (error) {
    showToast(`Export failed: ${error.message}`, { variant: "error" });
  }
}

async function setJobSchedule() {
  if (!(ui.scheduleSelect instanceof HTMLSelectElement)) {
    return;
  }

  const requested = ui.scheduleSelect.value;
  if (!SCHEDULE_OPTIONS.has(requested)) {
    ui.scheduleSelect.value = SCHEDULE_PLACEHOLDER;
    return;
  }

  const domain = await resolveSelectedDomain({
    allowCreate: requested !== SCHEDULE_PLACEHOLDER,
  });

  if (!domain) {
    showToast("Enter a site domain before changing the schedule.", {
      variant: "error",
    });
    renderScheduleState();
    return;
  }

  setDisabledAll(true);
  try {
    if (requested === SCHEDULE_PLACEHOLDER) {
      if (state.currentScheduler?.id) {
        await disableScheduler(state.currentScheduler.id, {
          expectedIsEnabled: state.currentScheduler.is_enabled,
        });
      }
      state.currentScheduler = null;
      renderScheduleState();
      setStatus("Scheduler disabled.", `No recurring run for ${domain}.`);
      return;
    }

    const scheduler = await saveSchedulerForDomain(domain, Number(requested), {
      currentScheduler: state.currentScheduler,
      extra: {
        max_pages: 0,
        find_links: true,
        concurrency: 20,
      },
    });
    state.currentScheduler = scheduler;
    renderScheduleState();
    setStatus("Scheduler updated.", `Running every ${requested} hours.`);
  } catch (error) {
    console.error("dashboard: failed to save schedule", error);
    renderScheduleState();
    showToast(error.message || "Failed to save the schedule.", {
      variant: "error",
    });
  } finally {
    setDisabledAll(false);
  }
}

function bindDomainSearch() {
  if (!(ui.domainInput instanceof HTMLInputElement)) {
    return;
  }

  setupDomainSearchInput({
    input: ui.domainInput,
    container: ui.domainInput.parentElement,
    clearOnSelect: false,
    onSelectDomain: async (domain) => {
      applySelectedDomain(domain.name);
      await Promise.all([loadCurrentSchedule(), refreshSiteResults()]);
    },
    onCreateDomain: async (domain) => {
      applySelectedDomain(domain.name);
      await Promise.all([loadCurrentSchedule(), refreshSiteResults()]);
    },
    onError: (message) => {
      showToast(message || "Failed to create domain.", { variant: "error" });
    },
  });

  ui.domainInput.addEventListener("change", () => {
    void handleDomainCommit();
  });
  ui.domainInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      void handleDomainCommit();
    }
  });
}

function bindEvents() {
  ui.loginButton?.addEventListener("click", () => openAuth("login"));
  ui.signupButton?.addEventListener("click", () => openAuth("signup"));
  ui.homeButton?.addEventListener("click", () => {
    applySelectedDomain("");
    void refreshDashboard({ silent: true });
  });
  ui.orgSelect?.addEventListener("change", () => {
    void switchOrganisation();
  });
  ui.scheduleSelect?.addEventListener("change", () => {
    void setJobSchedule();
  });
  ui.runNowButton?.addEventListener("click", () => {
    void runNow();
  });
  ui.runFirstCheckButton?.addEventListener("click", () => {
    void runNow();
  });
  ui.feedbackButton?.addEventListener("click", () => {
    window.location.assign(APP_ROUTES.feedback);
  });
  ui.helpButton?.addEventListener("click", () => {
    window.location.assign(APP_ROUTES.help);
  });

  window.addEventListener("storage", (event) => {
    if (
      event.key === ACTIVE_ORG_STORAGE_KEY &&
      event.newValue &&
      event.newValue !== state.activeOrganisationId
    ) {
      state.activeOrganisationId = event.newValue;
      applySelectedDomain("");
      void refreshDashboard({ silent: true });
    }
  });
}

async function syncAuthState(session) {
  state.session = session;
  state.userEmail = session?.user?.email || "";
  state.userDisplayName = getUserDisplayName(session?.user);
  state.userAvatarUrl = getUserMetadataAvatarUrl(session?.user);
  void updateAvatarFromState();

  if (!session) {
    cleanupJobSubscription();
    cleanupNotificationsSubscription();
    renderAuthState(false);
    return;
  }

  renderAuthState(true);
  await refreshDashboard();
}

async function init() {
  if (initialised) {
    return;
  }
  initialised = true;

  initSurfacePage({
    title: "Dashboard",
    defaultReturnPath: "/dashboard",
  });
  bindEvents();
  bindDomainSearch();
  await waitForSupabaseClient();
  shellChrome = initSurfaceShell({
    profileButton: ui.profileMenuButton,
    profileDropdown: ui.profileMenuDropdown,
    notificationsContainer: ui.notificationsContainer,
    notificationsButton: ui.notificationsButton,
    notificationsDropdown: ui.notificationsDropdown,
    notificationsList: ui.notificationsList,
    notificationsBadge: ui.notificationsBadge,
    markAllReadButton: ui.markAllReadButton,
    onNavigate: (path) => {
      window.location.assign(path);
    },
    onSignOut: async () => {
      await signOut();
      showToast("Signed out.", { variant: "success" });
      window.location.assign(APP_ROUTES.home);
    },
    fetchNotifications: (limit) => get(`/v1/notifications?limit=${limit}`),
    markNotificationRead: (id) => post(`/v1/notifications/${id}/read`),
    markAllNotificationsRead: () => post("/v1/notifications/read-all"),
    subscribeToNotifications: async (orgId, onEvent) => {
      const client = ensureSupabaseClient();
      const channel = client
        .channel(`dashboard-notifications:${orgId}`)
        .on(
          "postgres_changes",
          {
            event: "INSERT",
            schema: "public",
            table: "notifications",
            filter: `organisation_id=eq.${orgId}`,
          },
          () => {
            onEvent();
          }
        )
        .subscribe();

      return () => {
        client.removeChannel(channel).catch(() => {});
      };
    },
  });

  const initialSession = await getSession().catch(() => null);
  await syncAuthState(initialSession);

  authSubscriptionCleanup = onAuthStateChange((event, nextSession) => {
    if (event === "SIGNED_OUT") {
      state.selectedDomain = "";
      persistSelectedDomain();
    }
    void syncAuthState(nextSession);
  });
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => {
    void init();
  });
} else {
  void init();
}

window.HoverDashboard = {
  refresh: () => refreshDashboard(),
  destroy: () => {
    authSubscriptionCleanup?.();
    cleanupJobSubscription();
    shellChrome?.destroy?.();
  },
};
