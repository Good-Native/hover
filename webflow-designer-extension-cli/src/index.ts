declare const supabase: {
  createClient: (
    url: string,
    key: string,
    options?: Record<string, unknown>
  ) => SupabaseClient;
};

type SupabaseClient = {
  auth: {
    setSession: (params: {
      access_token: string;
      refresh_token: string;
    }) => Promise<unknown>;
    getUser?: () => Promise<{
      data?: {
        user?: {
          email?: string | null;
          user_metadata?: Record<string, unknown> | null;
        } | null;
      };
    }>;
  };
  channel: (name: string) => RealtimeChannel;
  removeChannel: (channel: RealtimeChannel) => Promise<unknown>;
};

type RealtimeChannel = {
  on: (
    event: string,
    filter: Record<string, string>,
    callback: (payload: unknown) => void
  ) => RealtimeChannel;
  subscribe: (
    callback?: (status: string, err?: Error) => void
  ) => RealtimeChannel;
};

const API_BASE_STORAGE_KEY = "gnh_extension_api_base";
const API_TOKEN_STORAGE_KEY = "gnh_extension_api_token_session";
const ACTIVE_ORG_STORAGE_KEY = "gnh_active_org_id";
const AUTH_POPUP_WIDTH = 520;
const AUTH_POPUP_HEIGHT = 760;
const DEFAULT_GNH_APP_ORIGIN = "https://hover.app.goodnative.co";
const AUTH_POPUP_NAME = "bbbExtensionAuth";
const SCHEDULE_PLACEHOLDER = "off";
const SCHEDULE_OPTIONS = ["off", "6", "12", "24", "48"] as const;
const JOB_POLLING_INTERVAL_MS = 6000;
const FALLBACK_POLLING_INTERVAL_MS = 1000;
const ACCOUNT_SETTINGS_EXTENSION_SIZE = { width: 620, height: 660 } as const;

const APP_ROUTES = {
  dashboard: "/dashboard",
  viewJob: "/jobs",
  account: "/settings/account",
  billing: "/settings/billing",
  notifications: "/settings/notifications",
  analytics: "/settings/analytics",
  automatedJobs: "/settings/automated-jobs",
  changePlan: "/settings/plans",
  manageTeam: "/settings/team",
} as const;
const UNAUTHENTICATED_EXTENSION_SIZE = { width: 240, height: 407 } as const;
const AUTHENTICATED_EXTENSION_SIZE = { width: 450, height: 500 } as const;
type ExtensionPanelSize =
  | "default"
  | "compact"
  | "comfortable"
  | "large"
  | { width: number; height: number };
const ACTIVE_JOB_STATUSES = new Set<string>([
  "pending",
  "queued",
  "initializing",
  "running",
  "in_progress",
  "processing",
]);

declare const webflow: {
  getSiteInfo: () => Promise<{
    siteId: string;
    siteName: string;
    shortName: string;
    isPasswordProtected: boolean;
    isPrivateStaging: boolean;
    workspaceId: string;
    workspaceSlug: string;
    domains: Array<{
      url: string;
      lastPublished: string | null;
      default: boolean;
      stage: "staging" | "production";
    }>;
  }>;
  setExtensionSize: (size: ExtensionPanelSize) => Promise<null>;
};

type ExtensionWindow = Window & {
  HOVER_EXTENSION_CONFIG?: {
    appOrigin?: string;
  };
  HOVER_EXTENSION_SUPABASE_CLIENT?: SupabaseClient | null;
};

// Shared modules exposed by lib/bridge.js via window.HoverLib
declare const HoverLib: {
  api: {
    configure: (config: {
      baseUrl?: string;
      tokenProvider?: () => Promise<string | null>;
    }) => void;
    get: (path: string, init?: RequestInit) => Promise<unknown>;
    post: (
      path: string,
      body?: unknown,
      init?: RequestInit
    ) => Promise<unknown>;
    patch: (
      path: string,
      body?: unknown,
      init?: RequestInit
    ) => Promise<unknown>;
    put: (path: string, body?: unknown, init?: RequestInit) => Promise<unknown>;
    del: (path: string, init?: RequestInit) => Promise<unknown>;
    request: (path: string, init?: RequestInit) => Promise<unknown>;
  };
  exports: {
    downloadJobExport: (
      jobId: string,
      options?: {
        api?: typeof HoverLib.api;
      }
    ) => Promise<{
      empty: boolean;
      filename: string;
      taskCount: number;
    }>;
  };
  fmt: {
    formatDate: (value: string | null | undefined) => string;
    formatDateTime: (value: string | null | undefined) => string;
    formatRelativeTime: (value: string | null | undefined) => string;
    formatDuration: (ms: number | null | undefined) => string;
    formatCount: (value: number | null | undefined) => string;
    formatPercent: (
      value: number | null | undefined,
      decimals?: number
    ) => string;
    formatStatus: (status: string) => string;
    statusCategory: (status: string) => string;
    formatUrl: (url: string | null | undefined) => string;
    escapeCSVValue: (value: unknown) => string;
    sanitiseForFilename: (value: string) => string;
    triggerFileDownload: (
      content: string,
      mimeType: string,
      filename: string
    ) => void;
    getInitials: (value: string) => string;
  };
  http: {
    fetchWithTimeout: (
      url: string,
      options?: RequestInit,
      context?: string
    ) => Promise<Response>;
  };
  organisations: {
    loadOrganisationContext: (options?: {
      api?: typeof HoverLib.api;
      includeUsage?: boolean;
    }) => Promise<{
      organisations: unknown[];
      activeOrganisationId: string;
      usage: unknown | null;
    }>;
    switchOrganisation: (
      organisationId: string,
      options?: { api?: typeof HoverLib.api }
    ) => Promise<unknown | null>;
  };
  schedulers: {
    findSchedulerByDomain: (
      domain: string,
      options?: {
        api?: typeof HoverLib.api;
        schedulers?: unknown[];
      }
    ) => Promise<unknown | null>;
    saveSchedulerForDomain: (
      domain: string,
      scheduleIntervalHours: number,
      options?: {
        api?: typeof HoverLib.api;
        currentScheduler?: unknown | null;
        extra?: Record<string, unknown>;
      }
    ) => Promise<unknown>;
    disableScheduler: (
      schedulerId: string,
      options?: {
        api?: typeof HoverLib.api;
        expectedIsEnabled?: boolean;
      }
    ) => Promise<unknown>;
  };
  jobs: {
    fetchJobs: (options?: {
      limit?: number;
      range?: string;
      include?: string;
    }) => Promise<unknown[]>;
    normaliseDomain: (input: string) => string;
    filterJobsByDomains: (
      jobs: unknown[],
      options?: { siteDomain?: string | null; siteDomainCandidates?: string[] }
    ) => unknown[];
    pickLatestJobByDomains: (
      jobs: unknown[],
      options?: { siteDomain?: string | null; siteDomainCandidates?: string[] }
    ) => unknown | null;
    buildCompletedJobsSignature: (
      jobs: unknown[],
      options?: { siteDomain?: string | null; siteDomainCandidates?: string[] },
      isActiveJobStatus?: (status: string) => boolean
    ) => string;
    buildChartJobsSignature: (
      jobs: unknown[],
      options?: { siteDomain?: string | null; siteDomainCandidates?: string[] }
    ) => string;
    subscribeToJobUpdates: (options: {
      orgId: string;
      onUpdate: () => void;
      supabaseClient?: SupabaseClient | null;
      channelName?: string;
      getFallbackInterval?: () => number;
      onSubscriptionIssue?: (status?: string, err?: Error) => void;
    }) => () => void;
  };
  shell: {
    initSurfaceShell: (options?: {
      profileButton?: HTMLElement | null;
      profileDropdown?: HTMLElement | null;
      notificationsContainer?: HTMLElement | null;
      notificationsButton?: HTMLElement | null;
      notificationsDropdown?: HTMLElement | null;
      notificationsList?: HTMLElement | null;
      notificationsBadge?: HTMLElement | null;
      markAllReadButton?: HTMLElement | null;
      onNavigate?: (path: string) => void;
      onSignOut?: () => Promise<void> | void;
      fetchNotifications?: (limit: number) => Promise<unknown>;
      markNotificationRead?: (id: string) => Promise<void>;
      markAllNotificationsRead?: () => Promise<void>;
      subscribeToNotifications?: (
        orgId: string,
        onEvent: () => void
      ) => Promise<() => void> | (() => void);
    }) => {
      refreshNotifications: (limit?: number) => Promise<unknown>;
      renderNotificationsList: (limit?: number) => Promise<void>;
      setActiveOrganisation: (nextOrganisationId: string) => void;
      destroy: () => void;
    };
    renderProfileMenuSummary: (options?: {
      emailNode?: Element | null;
      organisationNode?: Element | null;
      planNode?: Element | null;
      usageNode?: Element | null;
      email?: string;
      organisationName?: string;
      usage?: unknown | null;
    }) => void;
  };
  settings: {
    account: {
      loadAccountDetails: (container: HTMLElement | null) => Promise<void>;
      setupAccountActions: (
        container: HTMLElement | null,
        options?: { onNameSaved?: () => void }
      ) => void;
    };
  };
  view: {
    renderUserAvatar: (options?: {
      element?: HTMLElement | null;
      displayName?: string;
      email?: string;
      avatarUrl?: string;
    }) => Promise<void>;
    renderUsage: (options?: {
      usage?: unknown | null;
      planNameText?: Element | null;
      planRemainingValue?: Element | null;
      profilePlanText?: Element | null;
      profileUsageText?: Element | null;
    }) => void;
    renderOrganisations: (options?: {
      select?: HTMLSelectElement | null;
      organisations?: unknown[];
      activeOrganisationId?: string;
      emptyLabel?: string;
    }) => void;
    renderScheduleState: (options?: {
      select?: HTMLSelectElement | null;
      currentScheduler?: unknown | null;
      placeholder?: string;
      allowedValues?: string[];
    }) => void;
    renderJobState: (options?: {
      jobSection?: HTMLElement | null;
      job?: unknown | null;
      isActiveJobStatus?: (status: string) => boolean;
      context?: string;
      onViewJob?: (path: string, job?: unknown) => void;
      onExportJob?: (jobId: string, job?: unknown) => void;
    }) => void;
    renderRecentResults: (options?: {
      latestResultsList?: HTMLElement | null;
      recentResultsList?: HTMLElement | null;
      noJobState?: HTMLElement | null;
      noJobText?: Element | null;
      noJobActionButton?: HTMLElement | null;
      jobs?: unknown[];
      siteDomain?: string | null;
      siteDomainCandidates?: string[];
      isActiveJobStatus?: (status: string) => boolean;
      context?: string;
      onViewJob?: (path: string, job?: unknown) => void;
      onExportJob?: (jobId: string, job?: unknown) => void;
      emptySelectionMessage?: string;
      emptySiteMessage?: string;
      emptyCompletedMessage?: string;
      showEmptyAction?: boolean;
    }) => void;
    renderMiniChart: (options?: {
      miniChart?: HTMLElement | null;
      chartScaleLabels?: Element[];
      jobs?: unknown[];
      siteDomain?: string | null;
      siteDomainCandidates?: string[];
      onViewJob?: (path: string, job?: unknown) => void;
    }) => void;
  };
  webflow: {
    startWebflowConnection: () => Promise<{ auth_url?: string }>;
    listWebflowConnections: () => Promise<unknown[]>;
    findMatchingWebflowSite: (options: {
      api?: typeof HoverLib.api;
      connections?: unknown[];
      siteDomain?: string | null;
      siteDomainCandidates?: string[];
      limit?: number;
    }) => Promise<unknown | null>;
    setWebflowSiteAutoPublish: (
      siteId: string,
      options: {
        api?: typeof HoverLib.api;
        connectionId: string;
        enabled: boolean;
      }
    ) => Promise<void>;
  };
};

type ScheduleOption = (typeof SCHEDULE_OPTIONS)[number] | "";
type ExtensionView = "dashboard" | "settings-account";

type ApiError = {
  status: number;
  message: string;
  body?: string;
};

type Organisation = {
  id: string;
  name: string;
};

type UsageStats = {
  daily_limit: number;
  daily_used: number;
  daily_remaining: number;
  usage_percentage: number;
  plan_name: string;
  plan_display_name: string;
};

type JobItem = {
  id: string;
  status: string;
  total_tasks: number;
  completed_tasks: number;
  failed_tasks: number;
  skipped_tasks: number;
  progress: number;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  duration_seconds?: number | null;
  avg_time_per_task_seconds?: number | null;
  domain?: string;
  stats?: {
    total_broken_links?: number;
    slow_page_buckets?: {
      over_10s?: number;
      "5_to_10s"?: number;
      "3_to_5s"?: number;
    };
    cache_warming_effect?: {
      total_time_saved_ms?: number;
      total_time_saved_seconds?: number;
    };
  };
  domains?: {
    name: string;
  };
};

type Scheduler = {
  id: string;
  domain: string;
  schedule_interval_hours: number;
  is_enabled: boolean;
};

type CreateJobRequest = {
  domain: string;
  source_type: string;
  source_detail: string;
};

type WebflowConnection = {
  id: string;
  webflow_workspace_id?: string;
  workspace_name?: string;
};

type WebflowSiteSetting = {
  webflow_site_id: string;
  site_name: string;
  primary_domain: string;
  connection_id?: string;
  auto_publish_enabled: boolean;
  schedule_interval_hours?: number;
  scheduler_id?: string;
};

type AuthMessage = {
  source?: string;
  type?: string;
  state?: string;
  extensionState?: string;
  accessToken?: string;
  user?: {
    id?: string;
    email?: string;
    avatarUrl?: string;
  };
};

type ErrorPayload = {
  code?: string;
  message?: string;
};

function extractErrorMessage(rawBody?: string): string {
  if (!rawBody) {
    return "";
  }

  try {
    const parsed = JSON.parse(rawBody) as ErrorPayload;
    if (parsed?.message) {
      return parsed.message;
    }
  } catch (_error) {
    // ignore parse failures
  }

  return rawBody;
}

async function updateAvatarFromState(): Promise<void> {
  const avatarEl = ui.profileAvatar as HTMLElement | null;
  if (!avatarEl) return;

  await HoverLib.view.renderUserAvatar({
    element: avatarEl,
    displayName: state.userDisplayName || state.userEmail || "",
    email: state.userEmail || "",
    avatarUrl: state.userAvatarUrl || "",
  });
}

function getActiveOrganisationName(): string {
  return (
    state.organisations.find(
      (organisation) => organisation.id === state.activeOrganisationId
    )?.name || "Organisation"
  );
}

const ui = {
  // Status messages
  statusBlock: document.querySelector(".status-block"),
  statusText: document.getElementById("statusText"),
  detailText: document.getElementById("detailText"),

  // Auth states
  unauthState: document.getElementById("unauthState"),
  authState: document.getElementById("authState"),

  // Unauth buttons
  checkSiteButton: document.getElementById("checkSiteButton"),
  signInButton: document.getElementById("signInButton"),

  // Top bar
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
  orgSelect: document.getElementById("orgSelect") as HTMLSelectElement | null,

  // Action bar
  actionBar: document.querySelector(".action-bar"),
  runNowButton: document.getElementById("runNowButton"),
  scheduleSelect: document.getElementById(
    "scheduleSelect"
  ) as HTMLSelectElement | null,
  webflowPublishToggle: document.getElementById(
    "runPublishToggle"
  ) as HTMLInputElement | null,

  // Job card
  jobSection: document.getElementById("jobSection"),
  noJobState: document.getElementById("noJobState"),
  checkSiteAuthButton: document.getElementById("checkSiteAuthButton"),

  // Recent results
  latestResultsList: document.getElementById("latestResultsList"),
  recentResultsList: document.getElementById("recentResultsList"),

  // Mini chart
  miniChart: document.getElementById("miniChart"),
  chartScaleLabels: Array.from(
    document.querySelectorAll("#chartSection .chart-y-scale span")
  ),

  // Footer
  feedbackButton: document.getElementById("feedbackButton"),
  helpButton: document.getElementById("helpButton"),
  panelFooter: document.querySelector(".panel-footer"),
  contentScroll: document.querySelector(".content-scroll"),
  settingsAccountView: document.getElementById("settingsAccountView"),
  settingsBackButton: document.getElementById("settingsBackButton"),
  extensionAccountSection: document.getElementById("extensionAccountSection"),
  settingsProfileGroupTitle: document.getElementById(
    "settingsProfileGroupTitle"
  ),
  settingsOrgGroupTitle: document.getElementById("settingsOrgGroupTitle"),
};

type ExtensionState = {
  apiBaseUrl: string;
  token: string | null;
  siteDomain: string | null;
  siteName: string | null;
  siteDomainCandidates: string[];
  pendingAuthAction?: () => Promise<void> | void;
  organisations: Organisation[];
  activeOrganisationId: string;
  currentJob: JobItem | null;
  usage: UsageStats | null;
  currentScheduler: Scheduler | null;
  webflowConnected: boolean;
  webflowAutoPublishEnabled: boolean;
  userEmail: string | null;
  userDisplayName: string | null;
  userAvatarUrl: string | null;
};

const state: ExtensionState = {
  apiBaseUrl: getStoredBaseUrl(),
  token: getStoredToken(),
  siteDomain: null,
  siteName: null,
  siteDomainCandidates: [],
  organisations: [],
  activeOrganisationId: "",
  currentJob: null,
  usage: null,
  currentScheduler: null,
  webflowConnected: false,
  webflowAutoPublishEnabled: false,
  userEmail: null,
  userDisplayName: null,
  userAvatarUrl: null,
};

let statusToastTimer: ReturnType<typeof setTimeout> | null = null;
let jobStatusPoller: number | null = null;
let jobPollInFlight = false;
let lastCompletedJobsSignature = "";
let lastChartJobsSignature = "";
let crossSurfaceOrgRefreshInFlight = false;
let shellChrome: ReturnType<typeof HoverLib.shell.initSurfaceShell> | null =
  null;
let extensionView: ExtensionView = "dashboard";
let accountSettingsBound = false;
let accountSettingsLayoutBound = false;

// Supabase realtime state
let supabaseClient: SupabaseClient | null = null;
let isRealtimeRefreshing = false;
let jobsSubscriptionCleanup: (() => void) | null = null;

function getStoredBaseUrl(): string {
  const extensionWindow = window as ExtensionWindow;
  const runtimeBaseUrl = String(
    extensionWindow.HOVER_EXTENSION_CONFIG?.appOrigin || ""
  ).trim();
  if (runtimeBaseUrl) {
    return runtimeBaseUrl.replace(/\/+$/, "");
  }

  const storedBaseUrl = localStorage.getItem(API_BASE_STORAGE_KEY);
  if (!storedBaseUrl) {
    return DEFAULT_GNH_APP_ORIGIN;
  }

  if (/^https:\/\/hover-pr-\d+\.fly\.dev\/?$/i.test(storedBaseUrl)) {
    return DEFAULT_GNH_APP_ORIGIN;
  }

  return storedBaseUrl;
}

function getStoredToken(): string | null {
  return sessionStorage.getItem(API_TOKEN_STORAGE_KEY);
}

function setStoredToken(token: string | null): void {
  if (token) {
    sessionStorage.setItem(API_TOKEN_STORAGE_KEY, token);
  } else {
    sessionStorage.removeItem(API_TOKEN_STORAGE_KEY);
  }
  state.token = token;
}

type SupabaseConfig = {
  supabaseUrl: string;
  supabaseAnonKey: string;
};

async function fetchSupabaseConfig(): Promise<SupabaseConfig | null> {
  try {
    const response = await fetch(`${state.apiBaseUrl}/config.js`);
    if (!response.ok) {
      console.warn("Failed to fetch Supabase config:", response.status);
      return null;
    }

    const scriptText = await response.text();
    // config.js sets window.GNH_CONFIG = { supabaseUrl, supabaseAnonKey, ... }
    // Parse the JSON object from the assignment.
    const match = scriptText.match(/window\.GNH_CONFIG\s*=\s*(\{[\s\S]*?\});/);
    if (!match?.[1]) {
      console.warn("Could not parse GNH_CONFIG from config.js");
      return null;
    }

    const config = JSON.parse(match[1]) as Record<string, string>;
    if (!config.supabaseUrl || !config.supabaseAnonKey) {
      console.warn("Supabase config missing url or anon key");
      return null;
    }

    return {
      supabaseUrl: config.supabaseUrl,
      supabaseAnonKey: config.supabaseAnonKey,
    };
  } catch (error) {
    console.warn("Error fetching Supabase config:", error);
    return null;
  }
}

async function initSupabaseClient(): Promise<SupabaseClient | null> {
  if (supabaseClient) {
    return supabaseClient;
  }

  if (!state.token) {
    return null;
  }

  const config = await fetchSupabaseConfig();
  if (!config) {
    return null;
  }

  if (typeof supabase === "undefined" || !supabase?.createClient) {
    console.warn("Supabase SDK not loaded — realtime unavailable");
    return null;
  }

  supabaseClient = supabase.createClient(
    config.supabaseUrl,
    config.supabaseAnonKey,
    {
      auth: { persistSession: false, autoRefreshToken: false },
    }
  );

  // Set the session using the JWT we already have from extension auth.
  // No refresh token available — the extension auth flow only returns the access token.
  await supabaseClient.auth.setSession({
    access_token: state.token,
    refresh_token: "",
  });
  (window as ExtensionWindow).HOVER_EXTENSION_SUPABASE_CLIENT = supabaseClient;

  return supabaseClient;
}

function pickUserDisplayName(
  metadata: Record<string, unknown> | null | undefined
): string {
  const firstName = String(
    metadata?.given_name || metadata?.first_name || ""
  ).trim();
  const lastName = String(
    metadata?.family_name || metadata?.last_name || ""
  ).trim();
  const fullName = String(metadata?.full_name || metadata?.name || "").trim();
  return fullName || [firstName, lastName].filter(Boolean).join(" ").trim();
}

function pickUserAvatarUrl(
  user:
    | {
        user_metadata?: Record<string, unknown> | null;
        identities?: Array<{
          identity_data?: Record<string, unknown> | null;
        } | null> | null;
      }
    | null
    | undefined
): string {
  const metadata =
    (user?.user_metadata as Record<string, unknown> | null | undefined) || null;
  const directAvatar = String(
    metadata?.avatar_url || metadata?.picture || metadata?.avatar || ""
  ).trim();
  if (directAvatar) {
    return directAvatar;
  }

  const identityAvatar = user?.identities
    ?.map((identity) =>
      String(identity?.identity_data?.avatar_url || "").trim()
    )
    .find(Boolean);

  return identityAvatar || "";
}

async function loadCurrentUserIdentity(): Promise<void> {
  const client = await initSupabaseClient();
  if (client?.auth?.getUser) {
    try {
      const result = await client.auth.getUser();
      const user = result?.data?.user;
      const metadata =
        (user?.user_metadata as Record<string, unknown> | null | undefined) ||
        null;

      if (user?.email) {
        state.userEmail = user.email;
      }

      const displayName = pickUserDisplayName(metadata);
      if (displayName) {
        state.userDisplayName = displayName;
      }

      const avatarUrl = pickUserAvatarUrl(user);
      if (avatarUrl) {
        state.userAvatarUrl = avatarUrl;
      }
    } catch (error) {
      console.warn("Unable to load current user identity", error);
    }
  }

  if (state.userEmail && state.userDisplayName) {
    return;
  }

  try {
    const profile = (await HoverLib.api.get("/v1/auth/profile")) as {
      user?: {
        email?: string | null;
        first_name?: string | null;
        last_name?: string | null;
        full_name?: string | null;
      };
    };
    const user = profile?.user;
    if (user?.email) {
      state.userEmail = user.email;
    }
    const displayName =
      String(user?.full_name || "").trim() ||
      [user?.first_name || "", user?.last_name || ""]
        .map((value) => String(value).trim())
        .filter(Boolean)
        .join(" ")
        .trim();
    if (displayName) {
      state.userDisplayName = displayName;
    }
  } catch (error) {
    console.warn("Unable to load profile identity fallback", error);
  }
}

function asNode(element: Element | null): HTMLElement | null {
  return element instanceof HTMLElement ? element : null;
}

function asInput(element: Element | null): HTMLInputElement | null {
  return element instanceof HTMLInputElement ? element : null;
}

function asSelect(element: Element | null): HTMLSelectElement | null {
  return element instanceof HTMLSelectElement ? element : null;
}

function hide(el: HTMLElement | null): void {
  if (el) {
    el.classList.add("hidden");
  }
}

function show(el: HTMLElement | null): void {
  if (el) {
    el.classList.remove("hidden");
  }
}

function setText(node: Element | null, value: string): void {
  if (node) {
    node.textContent = value;
  }
}

function normalizeDomain(input: string): string {
  return HoverLib.jobs.normaliseDomain(input);
}

function normalizeJobStatus(status: string): string {
  return status.trim().toLowerCase();
}

function isActiveJobStatus(status: string): boolean {
  return ACTIVE_JOB_STATUSES.has(normalizeJobStatus(status));
}

function pickLatestJobForCurrentSite(
  jobs: JobItem[] | undefined
): JobItem | null {
  return (HoverLib.jobs.pickLatestJobByDomains(jobs || [], {
    siteDomain: state.siteDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  }) || null) as JobItem | null;
}

function buildCompletedJobsSignature(jobs: JobItem[] | undefined): string {
  return HoverLib.jobs.buildCompletedJobsSignature(
    jobs || [],
    {
      siteDomain: state.siteDomain,
      siteDomainCandidates: state.siteDomainCandidates,
    },
    isActiveJobStatus
  );
}

function buildChartJobsSignature(jobs: JobItem[] | undefined): string {
  return HoverLib.jobs.buildChartJobsSignature(jobs || [], {
    siteDomain: state.siteDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  });
}

function stopJobStatusPolling(): void {
  if (jobStatusPoller !== null) {
    window.clearInterval(jobStatusPoller);
    jobStatusPoller = null;
  }
}

function startJobStatusPolling(): void {
  // When realtime is active, the shared subscription also owns fallback polling.
  // Only start the legacy 6 s poller if we have no shared subscription.
  if (jobsSubscriptionCleanup) {
    return;
  }

  stopJobStatusPolling();

  if (!state.token || !state.currentJob || !state.siteDomain) {
    return;
  }

  if (!isActiveJobStatus(state.currentJob.status)) {
    return;
  }

  jobStatusPoller = window.setInterval(() => {
    void refreshCurrentJob();
  }, JOB_POLLING_INTERVAL_MS);
}

// ---------------------------------------------------------------------------
// Realtime: throttled refresh, fallback polling, subscription, cleanup
// ---------------------------------------------------------------------------

async function realtimeRefresh(): Promise<void> {
  if (isRealtimeRefreshing) return;
  isRealtimeRefreshing = true;

  try {
    // Refresh both job state and organisation context so cross-surface org
    // switches update quota, selected org, and the realtime subscription.
    await Promise.all([refreshCurrentJob(), refreshOrganisationContext()]);
  } finally {
    isRealtimeRefreshing = false;
  }
}

async function refreshOrganisationContext(): Promise<void> {
  if (!state.token) return;

  try {
    const previousOrganisationId = state.activeOrganisationId;
    await loadUsageAndOrgs();
    renderUsage(state.usage);

    if (state.activeOrganisationId !== previousOrganisationId) {
      renderOrganisations();
      if (supabaseClient) {
        subscribeToJobUpdates();
      }
    }
  } catch (error) {
    // Non-critical — keep existing org/quota state displayed.
    console.warn("Failed to refresh organisation context:", error);
  }
}

function cleanupRealtimeSubscription(): void {
  if (jobsSubscriptionCleanup) {
    jobsSubscriptionCleanup();
    jobsSubscriptionCleanup = null;
  }
}

function subscribeToJobUpdates(): void {
  const orgId = state.activeOrganisationId;
  if (!orgId || !supabaseClient) {
    return;
  }

  // The shared subscription owns realtime fallback polling, so the older
  // active-job interval must stop before we hand control across.
  stopJobStatusPolling();
  cleanupRealtimeSubscription();
  jobsSubscriptionCleanup = HoverLib.jobs.subscribeToJobUpdates({
    orgId,
    onUpdate: () => {
      void realtimeRefresh();
    },
    supabaseClient,
    channelName: `jobs-changes:${orgId}`,
    getFallbackInterval: () => FALLBACK_POLLING_INTERVAL_MS,
    onSubscriptionIssue: (status, err) => {
      if (
        status === "CHANNEL_ERROR" ||
        status === "TIMED_OUT" ||
        status === "SUBSCRIBE_FAILED" ||
        status === "MAX_RETRIES" ||
        err
      ) {
        console.warn(
          "[Realtime] Connection issue, fallback polling will continue",
          status,
          err
        );
      }
    },
  });
}

// ---------------------------------------------------------------------------

async function refreshCurrentJob(): Promise<void> {
  if (jobPollInFlight || !state.token || !state.siteDomain) {
    stopJobStatusPolling();
    return;
  }

  try {
    jobPollInFlight = true;
    const jobs = (await HoverLib.jobs.fetchJobs({
      limit: 50,
      include: "stats",
    })) as JobItem[];
    const latest = pickLatestJobForCurrentSite(jobs);
    state.currentJob = latest;
    renderJobState(state.currentJob);

    const completedSignature = buildCompletedJobsSignature(jobs);
    if (completedSignature !== lastCompletedJobsSignature) {
      renderRecentResults(jobs);
      lastCompletedJobsSignature = completedSignature;
    }

    const chartSignature = buildChartJobsSignature(jobs);
    if (chartSignature !== lastChartJobsSignature) {
      renderMiniChart(jobs);
      lastChartJobsSignature = chartSignature;
    }

    if (!isActiveJobStatus(state.currentJob?.status || "")) {
      stopJobStatusPolling();
    }
  } catch (error) {
    if (
      typeof error === "object" &&
      error !== null &&
      "status" in error &&
      ((error as ApiError).status === 401 || (error as ApiError).status === 403)
    ) {
      stopJobStatusPolling();
      handleAuthError(error);
      return;
    }
    console.error("Failed to refresh current job", error);
  } finally {
    jobPollInFlight = false;
  }
}

// Wire hover-job-card's issue-tab fetcher to the shared API client.
// window.HoverJobCard is set by hover-job-card.js (module script, loaded before index.ts).
function initHoverJobCard(): void {
  const hoverJobCard = (window as any).HoverJobCard;
  if (hoverJobCard?.setApiFetcher) {
    hoverJobCard.setApiFetcher((path: string) => HoverLib.api.get(path));
  }
}

function getPopupPosition() {
  const left =
    window.screenX + Math.max(0, (window.outerWidth - AUTH_POPUP_WIDTH) / 2);
  const top =
    window.screenY + Math.max(0, (window.outerHeight - AUTH_POPUP_HEIGHT) / 2);
  return { left: Math.floor(left), top: Math.floor(top) };
}

function createAuthStateValue(): string {
  if (window.crypto?.getRandomValues) {
    const bytes = new Uint8Array(16);
    window.crypto.getRandomValues(bytes);
    return `${Date.now()}-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
  }

  return `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

async function connectAccount(): Promise<string | null> {
  const authBase = new URL(state.apiBaseUrl);
  const stateToken = createAuthStateValue();
  const authUrl = `${state.apiBaseUrl}/extension-auth.html?origin=${encodeURIComponent(window.location.origin)}&extension_state=${encodeURIComponent(stateToken)}&state=${encodeURIComponent(stateToken)}`;
  const popupPosition = getPopupPosition();
  const popupFeatures = `width=${AUTH_POPUP_WIDTH},height=${AUTH_POPUP_HEIGHT},left=${popupPosition.left},top=${popupPosition.top},resizable=yes,scrollbars=yes`;

  const popup = window.open(
    authUrl,
    AUTH_POPUP_NAME,
    popupFeatures
  ) as Window | null;

  if (!popup) {
    setStatus(
      "Popup blocked. Allow popups for Webflow Designer and try again.",
      "error"
    );
    return null;
  }

  try {
    const message = await new Promise<AuthMessage>((resolve, reject) => {
      let settled = false;
      let closedTimer: number | undefined;

      const onMessage = (event: MessageEvent) => {
        if (event.source !== popup) {
          return;
        }
        if (event.origin !== authBase.origin || event.source === null) {
          return;
        }

        const payload = event.data as AuthMessage;
        const payloadState = payload?.state || payload?.extensionState;

        if (
          payload?.source !== "gnh-extension-auth" ||
          payloadState !== stateToken
        ) {
          console.warn(
            "extension auth: ignoring popup message (state mismatch)",
            {
              expected: stateToken,
              received: payload?.state,
              type: payload?.type,
            }
          );
          return;
        }

        settled = true;
        cleanup();
        resolve(payload);
      };

      const cleanup = () => {
        window.removeEventListener("message", onMessage);
        if (closedTimer) {
          window.clearInterval(closedTimer);
        }
      };

      const onClose = () => {
        if (settled) {
          return;
        }

        settled = true;
        cleanup();
        reject(new Error("Auth window closed before sign-in completed"));
      };

      window.addEventListener("message", onMessage);
      closedTimer = window.setInterval(() => {
        if (popup.closed) {
          onClose();
        }
      }, 500);
    });

    if (message.type === "success" && message.accessToken) {
      setStoredToken(message.accessToken);
      if (message.user?.email) state.userEmail = message.user.email;
      if (message.user?.avatarUrl) state.userAvatarUrl = message.user.avatarUrl;
      setStatus("", "");
      return message.accessToken;
    }

    setStatus(message.type || "Auth failed", "error");
    return null;
  } finally {
    if (popup && !popup.closed) {
      popup.close();
    }
  }
}

async function ensureSignedIn(): Promise<boolean> {
  if (state.token) {
    return true;
  }

  const token = await connectAccount();
  return Boolean(token);
}

function setStatus(message: string, detail = "") {
  // Cancel any in-flight toast and reset opacity immediately.
  if (statusToastTimer !== null) {
    clearTimeout(statusToastTimer);
    statusToastTimer = null;
  }
  ui.statusBlock?.classList.remove("status-block--fading");

  setText(ui.statusText, message);
  setText(ui.detailText, detail);

  // Auto-dismiss non-empty toasts: fade at 3 s, clear at 3.5 s.
  if (message || detail) {
    statusToastTimer = setTimeout(() => {
      ui.statusBlock?.classList.add("status-block--fading");
      statusToastTimer = setTimeout(() => {
        setText(ui.statusText, "");
        setText(ui.detailText, "");
        ui.statusBlock?.classList.remove("status-block--fading");
        statusToastTimer = null;
      }, 500);
    }, 3000);
  }
}

async function setExtensionSizeForAuthState(isAuthed: boolean): Promise<void> {
  try {
    await webflow.setExtensionSize(
      isAuthed ? AUTHENTICATED_EXTENSION_SIZE : UNAUTHENTICATED_EXTENSION_SIZE
    );
  } catch (error) {
    console.warn("Unable to set extension size", error);
  }
}

async function setExtensionSizeForView(view: ExtensionView): Promise<void> {
  try {
    await webflow.setExtensionSize(
      view === "settings-account"
        ? ACCOUNT_SETTINGS_EXTENSION_SIZE
        : AUTHENTICATED_EXTENSION_SIZE
    );
  } catch (error) {
    console.warn("Unable to set extension size", error);
  }
}

function renderAuthState(isAuthed: boolean): void {
  if (isAuthed) {
    hide(asNode(ui.unauthState));
    show(asNode(ui.authState));
    void setExtensionSizeForView(extensionView);
    return;
  }

  show(asNode(ui.unauthState));
  hide(asNode(ui.authState));
  void setExtensionSizeForAuthState(false);
}

function renderView(): void {
  const showSettingsAccount = extensionView === "settings-account";

  if (showSettingsAccount) {
    hide(asNode(ui.actionBar));
    hide(asNode(ui.statusBlock));
    hide(asNode(ui.contentScroll));
    hide(asNode(ui.panelFooter));
    show(asNode(ui.settingsAccountView));
  } else {
    show(asNode(ui.actionBar));
    show(asNode(ui.statusBlock));
    show(asNode(ui.contentScroll));
    show(asNode(ui.panelFooter));
    hide(asNode(ui.settingsAccountView));
  }

  if (state.token) {
    void setExtensionSizeForView(extensionView);
  }
}

function renderSettingsSidebar(path: string): void {
  document
    .querySelectorAll<HTMLElement>("[data-settings-path]")
    .forEach((element) => {
      const isActive = element.dataset.settingsPath === path;
      element.classList.toggle("active", isActive);
      if (isActive) {
        element.setAttribute("aria-current", "page");
      } else {
        element.removeAttribute("aria-current");
      }
    });
}

function renderSettingsOrganisationGroupTitle(): void {
  const heading = asNode(ui.settingsOrgGroupTitle);
  if (!heading) {
    return;
  }
  const activeOrg = state.organisations.find(
    (organisation) => organisation.id === state.activeOrganisationId
  );
  heading.textContent = activeOrg?.name
    ? `Manage ${activeOrg.name}`
    : "Manage organisation";
}

function renderSettingsProfileGroupTitle(): void {
  const heading = asNode(ui.settingsProfileGroupTitle);
  if (!heading) {
    return;
  }

  const firstName = asInput(
    document.getElementById("settingsUserFirstNameInput")
  )?.value.trim();
  const lastName = asInput(
    document.getElementById("settingsUserLastNameInput")
  )?.value.trim();
  const displayNameFromFields = [firstName, lastName].filter(Boolean).join(" ");
  heading.textContent =
    displayNameFromFields ||
    state.userDisplayName ||
    state.userEmail ||
    "Profile";
}

function bindAccountSettingsLayout(): void {
  if (accountSettingsLayoutBound) {
    return;
  }

  document
    .querySelectorAll<HTMLElement>("[data-settings-path]")
    .forEach((element) => {
      element.addEventListener("click", () => {
        const path = element.dataset.settingsPath;
        if (!path) {
          return;
        }
        openSettingsPage(path);
      });
    });

  accountSettingsLayoutBound = true;
}

async function openAccountSettingsView(): Promise<void> {
  if (!state.token || !ui.extensionAccountSection) {
    return;
  }

  extensionView = "settings-account";
  renderSettingsSidebar(APP_ROUTES.account);
  renderSettingsOrganisationGroupTitle();
  bindAccountSettingsLayout();
  renderView();

  if (!accountSettingsBound) {
    HoverLib.settings.account.setupAccountActions(ui.extensionAccountSection, {
      onNameSaved: () => {
        const firstName = asInput(
          document.getElementById("settingsUserFirstNameInput")
        )?.value.trim();
        const lastName = asInput(
          document.getElementById("settingsUserLastNameInput")
        )?.value.trim();
        const nextDisplayName = [firstName, lastName]
          .filter(Boolean)
          .join(" ")
          .trim();
        if (nextDisplayName) {
          state.userDisplayName = nextDisplayName;
          void updateAvatarFromState();
        }
        renderSettingsProfileGroupTitle();
      },
    });
    accountSettingsBound = true;
  }

  await HoverLib.settings.account.loadAccountDetails(
    ui.extensionAccountSection
  );
  renderSettingsProfileGroupTitle();
}

function openDashboardView(): void {
  extensionView = "dashboard";
  renderView();
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

/** Show the in-progress card only for active jobs; hide for completed/none. */
function renderJobState(job: JobItem | null): void {
  const section = asNode(ui.jobSection);
  if (!job || !isActiveJobStatus(job.status)) {
    stopJobStatusPolling();
  }
  HoverLib.view.renderJobState({
    jobSection: section,
    job,
    isActiveJobStatus,
    context: "extension",
    onViewJob: (path) => {
      openSettingsPage(path);
    },
    onExportJob: (jobId) => {
      void exportJob(jobId);
    },
  });
}

function renderRecentResults(jobs: JobItem[]): void {
  HoverLib.view.renderRecentResults({
    latestResultsList: asNode(ui.latestResultsList),
    recentResultsList: asNode(ui.recentResultsList),
    noJobState: asNode(ui.noJobState),
    jobs,
    siteDomain: state.siteDomain,
    siteDomainCandidates: state.siteDomainCandidates,
    isActiveJobStatus,
    context: "extension",
    onViewJob: (path) => {
      openSettingsPage(path);
    },
    onExportJob: (jobId) => {
      void exportJob(jobId);
    },
    emptySiteMessage: "No runs yet for this site.",
  });
}
// Job export
// ---------------------------------------------------------------------------

async function exportJob(jobId: string): Promise<void> {
  try {
    const result = await HoverLib.exports.downloadJobExport(jobId, {
      api: HoverLib.api,
    });
    if (result.empty) {
      setStatus("Export unavailable", "No tasks to export.");
    }
  } catch (error) {
    setStatus(
      "Export failed",
      error instanceof Error ? error.message : "Unknown error"
    );
  }
}

// ---------------------------------------------------------------------------
// Mini chart
// ---------------------------------------------------------------------------

function renderMiniChart(jobs: JobItem[]): void {
  HoverLib.view.renderMiniChart({
    miniChart: asNode(ui.miniChart),
    chartScaleLabels: ui.chartScaleLabels,
    jobs,
    siteDomain: state.siteDomain,
    siteDomainCandidates: state.siteDomainCandidates,
    onViewJob: (path) => {
      openSettingsPage(path);
    },
  });
}

function renderUsage(usage: UsageStats | null): void {
  HoverLib.view.renderUsage({
    usage,
    profilePlanText: ui.profilePlanText,
    profileUsageText: ui.profileUsageText,
  });
  HoverLib.shell.renderProfileMenuSummary({
    emailNode: ui.profileEmail,
    organisationNode: ui.profileOrgName,
    planNode: ui.profilePlanText,
    usageNode: ui.profileUsageText,
    email: state.userEmail || "",
    organisationName: getActiveOrganisationName(),
    usage,
  });
}

function renderOrganisations() {
  HoverLib.view.renderOrganisations({
    select: asSelect(ui.orgSelect),
    organisations: state.organisations,
    activeOrganisationId: state.activeOrganisationId,
  });
  renderSettingsOrganisationGroupTitle();
}

function renderWebflowStatus(isConnected: boolean) {
  if (!ui.webflowPublishToggle) {
    return;
  }

  ui.webflowPublishToggle.checked =
    isConnected && state.webflowAutoPublishEnabled;
}

function renderScheduleState(): void {
  HoverLib.view.renderScheduleState({
    select: asSelect(ui.scheduleSelect),
    currentScheduler: state.currentScheduler,
    placeholder: SCHEDULE_PLACEHOLDER,
    allowedValues: [...SCHEDULE_OPTIONS],
  });
}

function buildAppUrl(path: string): string {
  try {
    const trimmedBase = state.apiBaseUrl.replace(/\/+$/, "");
    const normalizedPath = path.startsWith("/") ? path : `/${path}`;
    return new URL(normalizedPath, `${trimmedBase}/`).toString();
  } catch (error) {
    console.error("Failed to build app URL", error);
    return `${state.apiBaseUrl.replace(/\/+$/, "")}/${path}`;
  }
}

function setLoading(element: Element | null, disabled: boolean): void {
  if (
    element instanceof HTMLButtonElement ||
    element instanceof HTMLSelectElement
  ) {
    element.disabled = disabled;
  }
}

function setDisabledAll(disabled: boolean): void {
  const controls: (Element | null)[] = [
    ui.checkSiteButton,
    ui.checkSiteAuthButton,
    ui.signInButton,
    ui.runNowButton,
    ui.scheduleSelect,
    ui.orgSelect,
    ui.webflowPublishToggle,
  ];

  for (const control of controls) {
    setLoading(control, disabled);
  }

  const toggle = asInput(ui.webflowPublishToggle);
  if (toggle) {
    toggle.disabled = disabled;
  }
}

async function loadCurrentSiteInfo() {
  try {
    const siteInfo = await webflow.getSiteInfo();
    const stageFiltered = siteInfo.domains.filter(
      (domain) => domain.stage === "staging" || domain.stage === "production"
    );
    state.siteDomainCandidates = stageFiltered.map(
      (candidate) => candidate.url
    );

    const preferredDomain =
      stageFiltered.find((domain) => domain.default)?.url ||
      stageFiltered.find((domain) => domain.stage === "production")?.url ||
      stageFiltered.find((domain) => domain.stage === "staging")?.url;

    state.siteDomain = preferredDomain
      ? normalizeDomain(preferredDomain)
      : stageFiltered[0]
        ? normalizeDomain(stageFiltered[0].url)
        : normalizeDomain(siteInfo.shortName);
    state.siteName = siteInfo.siteName;
    return state.siteDomain;
  } catch (error) {
    console.error("Failed to get site info", error);
    return null;
  }
}

async function loadLatestJob(): Promise<void> {
  if (!state.siteDomain || !state.token) {
    state.currentJob = null;
    renderJobState(null);
    renderRecentResults([]);
    renderMiniChart([]);
    lastCompletedJobsSignature = "";
    lastChartJobsSignature = "";
    stopJobStatusPolling();
    return;
  }

  try {
    const jobs = (await HoverLib.jobs.fetchJobs({
      limit: 50,
      include: "stats",
    })) as JobItem[];

    const latest = pickLatestJobForCurrentSite(jobs);

    state.currentJob = latest;
    renderJobState(state.currentJob);
    renderRecentResults(jobs);
    renderMiniChart(jobs);
    lastCompletedJobsSignature = buildCompletedJobsSignature(jobs);
    lastChartJobsSignature = buildChartJobsSignature(jobs);
    startJobStatusPolling();
  } catch (error) {
    state.currentJob = null;
    renderJobState(null);
    renderRecentResults([]);
    renderMiniChart([]);
    lastCompletedJobsSignature = "";
    lastChartJobsSignature = "";
    stopJobStatusPolling();
    console.error(error);
  }
}

async function loadUsageAndOrgs(): Promise<void> {
  if (!state.token) {
    state.organisations = [];
    state.usage = null;
    state.currentScheduler = null;
    return;
  }

  const context = (await HoverLib.organisations.loadOrganisationContext({
    api: HoverLib.api,
  })) as {
    organisations: Organisation[];
    activeOrganisationId: string;
    usage: UsageStats | null;
  };

  state.organisations = context.organisations || [];
  state.activeOrganisationId =
    context.activeOrganisationId || state.activeOrganisationId;
  state.usage = context.usage || null;
  shellChrome?.setActiveOrganisation(state.activeOrganisationId);
}

async function loadCurrentSchedule(): Promise<void> {
  if (!state.siteDomain || !state.token) {
    state.currentScheduler = null;
    renderScheduleState();
    return;
  }

  const matching = (await HoverLib.schedulers.findSchedulerByDomain(
    state.siteDomain,
    {
      api: HoverLib.api,
    }
  )) as Scheduler | null;
  state.currentScheduler = matching;
  renderScheduleState();
}

async function findConnectedWebflowSite(): Promise<WebflowSiteSetting | null> {
  if (!state.token || !state.siteDomain) {
    state.webflowConnected = false;
    state.webflowAutoPublishEnabled = false;
    renderWebflowStatus(false);
    return null;
  }

  const connections =
    (await HoverLib.webflow.listWebflowConnections()) as WebflowConnection[];

  if (!connections || connections.length === 0) {
    state.webflowConnected = false;
    state.webflowAutoPublishEnabled = false;
    renderWebflowStatus(false);
    return null;
  }

  state.webflowConnected = true;

  const matched = (await HoverLib.webflow.findMatchingWebflowSite({
    api: HoverLib.api,
    connections,
    siteDomain: state.siteDomain,
    siteDomainCandidates: state.siteDomainCandidates,
  })) as WebflowSiteSetting | null;

  if (matched) {
    state.webflowAutoPublishEnabled = Boolean(matched.auto_publish_enabled);
    state.webflowConnected = true;
    renderWebflowStatus(true);
    return matched;
  }

  state.webflowAutoPublishEnabled = false;
  renderWebflowStatus(true);
  return null;
}

async function setWebflowAutoPublish(enabled: boolean): Promise<void> {
  // Optimistically update UI before the network round-trip.
  state.webflowAutoPublishEnabled = enabled;
  renderWebflowStatus(state.webflowConnected);

  const siteSetting = await findConnectedWebflowSite();
  if (!siteSetting) {
    state.webflowAutoPublishEnabled = false;
    renderWebflowStatus(state.webflowConnected);
    setStatus("Connect Webflow and select this site, then try again.", "");
    return;
  }
  if (!siteSetting.connection_id) {
    throw new Error("Connected Webflow site missing connection id.");
  }

  const payload = {
    connection_id: siteSetting.connection_id,
    enabled,
  };

  try {
    await HoverLib.webflow.setWebflowSiteAutoPublish(
      siteSetting.webflow_site_id,
      {
        api: HoverLib.api,
        connectionId: payload.connection_id,
        enabled: payload.enabled,
      }
    );
  } catch (error) {
    // Revert on failure.
    state.webflowAutoPublishEnabled = !enabled;
    renderWebflowStatus(state.webflowConnected);
    throw error;
  }

  // Re-apply after findConnectedWebflowSite may have overwritten state.
  state.webflowAutoPublishEnabled = enabled;
  renderWebflowStatus(state.webflowConnected);

  setStatus(
    `Auto-publish ${enabled ? "enabled" : "disabled"} for ${state.siteDomain || "this site"}`,
    ""
  );
}

async function setJobSchedule(value: ScheduleOption): Promise<void> {
  if (!state.token || !state.siteDomain) {
    return;
  }

  if (!value) {
    return;
  }

  const domain = state.siteDomain;
  if (value === "off") {
    if (state.currentScheduler) {
      await HoverLib.schedulers.disableScheduler(state.currentScheduler.id, {
        api: HoverLib.api,
        expectedIsEnabled: state.currentScheduler.is_enabled,
      });
    }
    state.currentScheduler = null;
    setStatus("Scheduler disabled for this site.", "");
    renderScheduleState();
    return;
  }

  const scheduleHours = Number(value);

  if (!state.currentScheduler) {
    const created = (await HoverLib.schedulers.saveSchedulerForDomain(
      domain,
      scheduleHours,
      {
        api: HoverLib.api,
      }
    )) as Scheduler;
    state.currentScheduler = created;
    setStatus("Schedule enabled.", "");
  } else {
    const updated = (await HoverLib.schedulers.saveSchedulerForDomain(
      domain,
      scheduleHours,
      {
        api: HoverLib.api,
        currentScheduler: state.currentScheduler,
      }
    )) as Scheduler;
    state.currentScheduler = updated;
    setStatus("Schedule updated.", "");
  }

  renderScheduleState();
}

async function runScanForCurrentSite(): Promise<void> {
  if (!state.token) {
    const started = await ensureSignedIn();
    if (!started) {
      return;
    }
    await refreshDashboard();
  }

  if (!state.siteDomain) {
    setStatus(
      "Could not read current site domain.",
      "Open a site in the Designer and try again."
    );
    return;
  }

  const request: CreateJobRequest = {
    domain: state.siteDomain,
    source_type: "extension",
    source_detail: "webflow_designer_check",
  };

  const created = (await HoverLib.api.post("/v1/jobs", request)) as JobItem;

  state.currentJob = created;
  renderJobState(created);
  startJobStatusPolling();
  setStatus("Scan started.", "Use Run again to requeue a fresh run.");
  await refreshDashboard();
}

function handleAuthError(error: unknown): void {
  if (typeof error === "object" && error !== null && "status" in error) {
    const apiError = error as ApiError;
    if (apiError.status === 401) {
      setStoredToken(null);
      cleanupRealtimeSubscription();
      shellChrome?.setActiveOrganisation("");
      supabaseClient = null;
      (window as ExtensionWindow).HOVER_EXTENSION_SUPABASE_CLIENT = null;
      renderAuthState(false);
      setStatus("Session expired. Sign in again.", "");
      return;
    }

    if (apiError.status === 403) {
      const message = extractErrorMessage(apiError.body);
      setStatus("Action not permitted", message);
      return;
    }

    setStatus(`API error (${apiError.status})`, apiError.body || "");
    return;
  }

  if (error instanceof Error) {
    setStatus("Request failed", error.message);
    return;
  }

  setStatus("Request failed", "Unknown error");
}

async function refreshDashboard(): Promise<void> {
  setDisabledAll(true);

  try {
    setStatus("", "");
    state.token = getStoredToken();

    renderAuthState(Boolean(state.token));

    await loadCurrentSiteInfo();
    if (!state.token) {
      state.currentJob = null;
      state.usage = null;
      state.organisations = [];
      state.currentScheduler = null;
      stopJobStatusPolling();
      cleanupRealtimeSubscription();
      shellChrome?.setActiveOrganisation("");
      supabaseClient = null;
      (window as ExtensionWindow).HOVER_EXTENSION_SUPABASE_CLIENT = null;
      renderJobState(null);
      renderRecentResults([]);
      renderMiniChart([]);
      lastCompletedJobsSignature = "";
      lastChartJobsSignature = "";
      renderUsage(null);
      renderOrganisations();
      renderScheduleState();
      renderWebflowStatus(false);
      openDashboardView();
      return;
    }

    try {
      await Promise.all([
        loadUsageAndOrgs(),
        loadCurrentUserIdentity(),
        loadLatestJob(),
        loadCurrentSchedule(),
        findConnectedWebflowSite(),
      ]);
      renderUsage(state.usage);
      renderOrganisations();
      void updateAvatarFromState();
      HoverLib.shell.renderProfileMenuSummary({
        emailNode: ui.profileEmail,
        organisationNode: ui.profileOrgName,
        planNode: ui.profilePlanText,
        usageNode: ui.profileUsageText,
        email: state.userEmail || "",
        organisationName: getActiveOrganisationName(),
        usage: state.usage,
      });

      // Initialise Supabase realtime; fall back to legacy polling on failure.
      const client = await initSupabaseClient();
      if (client) {
        void subscribeToJobUpdates();
      } else {
        startJobStatusPolling();
      }
    } catch (error) {
      await handleAuthError(error);
    }
  } finally {
    setDisabledAll(false);
  }
}

async function syncActiveOrganisationFromStorage(
  nextOrganisationId: string | null
): Promise<void> {
  if (!state.token || !nextOrganisationId) {
    return;
  }

  if (
    crossSurfaceOrgRefreshInFlight ||
    nextOrganisationId === state.activeOrganisationId
  ) {
    return;
  }

  crossSurfaceOrgRefreshInFlight = true;
  try {
    state.activeOrganisationId = nextOrganisationId;
    await refreshDashboard();
  } finally {
    crossSurfaceOrgRefreshInFlight = false;
  }
}

async function switchOrganisation(): Promise<void> {
  const select = asSelect(ui.orgSelect);
  if (!select || !select.value) {
    return;
  }

  setDisabledAll(true);
  try {
    await HoverLib.organisations.switchOrganisation(select.value, {
      api: HoverLib.api,
    });
    state.activeOrganisationId = select.value;
    await refreshDashboard();
  } finally {
    setDisabledAll(false);
  }
}

function openSettingsPage(path: string): void {
  if (path === APP_ROUTES.account) {
    void openAccountSettingsView();
    return;
  }

  const targetUrl = buildAppUrl(path);
  const popup = window.open(targetUrl, "_blank", "noopener,noreferrer");
  if (!popup) {
    setStatus("Popup blocked. Allow popups and try again.", "");
  }
}

async function subscribeToNotificationsChannel(
  organisationId: string,
  onEvent: () => void
): Promise<() => void> {
  const client = await initSupabaseClient();
  if (!organisationId || !client) {
    return () => {};
  }

  const channel = client
    .channel(`hover-notifications:${organisationId}`)
    .on(
      "postgres_changes",
      {
        event: "INSERT",
        schema: "public",
        table: "notifications",
        filter: `organisation_id=eq.${organisationId}`,
      },
      () => {
        onEvent();
      }
    )
    .subscribe();

  return () => {
    client.removeChannel(channel).catch(() => {});
  };
}

async function connectWebflow(): Promise<void> {
  if (!state.token) {
    const token = await connectAccount();
    if (!token) {
      return;
    }
  }

  const response = (await HoverLib.webflow.startWebflowConnection()) as {
    auth_url: string;
  };

  const popup = window.open(
    response.auth_url,
    "gnh-webflow-connect",
    `width=520,height=760,left=60,top=60`
  );
  if (!popup) {
    setStatus("Popup blocked. Allow popups and try again.", "");
    return;
  }

  const popupResult = await new Promise<{
    connected?: boolean;
    error?: string;
  }>((resolve) => {
    let timer: number | undefined;
    const origin = new URL(state.apiBaseUrl).origin;
    const handleMessage = (event: MessageEvent) => {
      if (event.source !== popup || event.origin !== origin) {
        return;
      }

      const payload = event.data as {
        source?: string;
        type?: string;
        connected?: boolean;
        error?: string;
      };

      if (
        payload?.source !== "gnh-webflow-connect" ||
        payload.type !== "webflow-connect-complete"
      ) {
        return;
      }

      if (timer) {
        window.clearInterval(timer);
      }
      window.removeEventListener("message", handleMessage);
      resolve({
        connected: payload.connected,
        error: payload.error,
      });
    };

    window.addEventListener("message", handleMessage);

    timer = window.setInterval(() => {
      if (popup.closed) {
        if (timer) {
          window.clearInterval(timer);
        }
        window.removeEventListener("message", handleMessage);
        resolve({});
      }
    }, 500);
  });

  if (!popup.closed) {
    popup.close();
  }

  setStatus("Webflow connection flow complete.", "Refreshing connections.");
  await refreshDashboard();

  if (popupResult?.connected) {
    try {
      await setWebflowAutoPublish(true);
    } catch (error) {
      console.warn("Unable to enable run-on-publish after connect:", error);
    }
    return;
  }

  if (popupResult?.error) {
    setStatus("Webflow connect failed.", popupResult.error);
  }
}

function initEventHandlers(): void {
  // Unauth: check site
  ui.checkSiteButton?.addEventListener("click", async () => {
    try {
      await runScanForCurrentSite();
    } catch (error) {
      await handleAuthError(error);
    }
  });

  // Unauth: sign in
  ui.signInButton?.addEventListener("click", async () => {
    await connectAccount();
    await refreshDashboard();
  });

  // Auth: run now (action bar)
  ui.runNowButton?.addEventListener("click", async () => {
    try {
      await runScanForCurrentSite();
    } catch (error) {
      await handleAuthError(error);
    }
  });

  // Auth: check site (no-job state)
  ui.checkSiteAuthButton?.addEventListener("click", async () => {
    try {
      await runScanForCurrentSite();
    } catch (error) {
      await handleAuthError(error);
    }
  });

  ui.homeButton?.addEventListener("click", () => {
    openDashboardView();
  });
  ui.settingsBackButton?.addEventListener("click", () => {
    openDashboardView();
  });

  // Auth: org switcher
  ui.orgSelect?.addEventListener("change", () => {
    void switchOrganisation();
  });

  // Auth: schedule select
  ui.scheduleSelect?.addEventListener("change", async () => {
    const select = asSelect(ui.scheduleSelect);
    if (!select) {
      return;
    }

    const requested = select.value as ScheduleOption;
    try {
      await setJobSchedule(requested);
    } catch (error) {
      await handleAuthError(error);
    }
  });

  // Auth: auto-publish toggle
  ui.webflowPublishToggle?.addEventListener("change", async (event) => {
    const target = event.target as HTMLInputElement | null;
    if (!target) {
      return;
    }

    const enabled = target.checked;
    try {
      if (enabled && !state.webflowConnected) {
        await connectWebflow();
      }
      await setWebflowAutoPublish(enabled);
    } catch (error) {
      if (target) {
        target.checked = !enabled;
      }
      await handleAuthError(error);
    }
  });

  // Footer: feedback
  // TODO: connect to feedback form or mailto link
  ui.feedbackButton?.addEventListener("click", () => {
    openSettingsPage(APP_ROUTES.dashboard);
  });

  // Footer: help
  // TODO: connect to help/docs page
  ui.helpButton?.addEventListener("click", () => {
    openSettingsPage(APP_ROUTES.dashboard);
  });
}

async function initialise(): Promise<void> {
  window.addEventListener("beforeunload", () => {
    stopJobStatusPolling();
    cleanupRealtimeSubscription();
    shellChrome?.destroy();
  });
  window.addEventListener("storage", (event) => {
    if (event.key !== ACTIVE_ORG_STORAGE_KEY) {
      return;
    }
    void syncActiveOrganisationFromStorage(event.newValue);
  });
  try {
    if (!(window as ExtensionWindow).HOVER_EXTENSION_CONFIG?.appOrigin) {
      localStorage.setItem(API_BASE_STORAGE_KEY, state.apiBaseUrl);
    }
  } catch (_error) {
    // ignore
  }

  // Configure shared API client for cross-origin extension use.
  if (typeof HoverLib !== "undefined" && HoverLib?.api?.configure) {
    HoverLib.api.configure({
      baseUrl: state.apiBaseUrl,
      tokenProvider: () => Promise.resolve(getStoredToken()),
    });
  }

  initHoverJobCard();
  initEventHandlers();
  shellChrome = HoverLib.shell.initSurfaceShell({
    profileButton: ui.profileMenuButton as HTMLElement | null,
    profileDropdown: ui.profileMenuDropdown as HTMLElement | null,
    notificationsContainer: ui.notificationsContainer as HTMLElement | null,
    notificationsButton: ui.notificationsButton as HTMLElement | null,
    notificationsDropdown: ui.notificationsDropdown as HTMLElement | null,
    notificationsList: ui.notificationsList as HTMLElement | null,
    notificationsBadge: ui.notificationsBadge as HTMLElement | null,
    markAllReadButton: ui.markAllReadButton as HTMLElement | null,
    onNavigate: (path) => {
      openSettingsPage(path);
    },
    fetchNotifications: (limit) =>
      HoverLib.api.get(`/v1/notifications?limit=${limit}`),
    markNotificationRead: async (id) => {
      await HoverLib.api.post(`/v1/notifications/${id}/read`);
    },
    markAllNotificationsRead: async () => {
      await HoverLib.api.post("/v1/notifications/read-all");
    },
    subscribeToNotifications: (orgId, onEvent) =>
      subscribeToNotificationsChannel(orgId, onEvent),
  });
  renderView();
  await refreshDashboard();
  renderAuthState(Boolean(state.token));

  setStatus("", "");
}

void initialise();
