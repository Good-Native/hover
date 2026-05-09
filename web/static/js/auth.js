/**
 * Hover Unified Authentication System
 * Core authentication logic extracted from dashboard.html
 *
 * Handles:
 * - Supabase authentication integration
 * - Email/password authentication
 * - Social login (Google, GitHub)
 * - Password strength validation with zxcvbn
 * - Cloudflare Turnstile CAPTCHA
 * - Backend user registration
 * - Auth state management
 * - Modal management
 * - Pending domain flow
 * - Sentry error tracking for auth failures
 */

// Supabase configuration
const runtimeConfig =
  (typeof window !== "undefined" && window.GNH_CONFIG) ||
  (typeof process !== "undefined"
    ? {
        supabaseUrl: process.env.SUPABASE_AUTH_URL,
        supabaseAnonKey: process.env.SUPABASE_PUBLISHABLE_KEY,
      }
    : null);

const SUPABASE_URL = runtimeConfig?.supabaseUrl || "";
const SUPABASE_ANON_KEY = runtimeConfig?.supabaseAnonKey || "";

function hasSupabaseRuntimeConfig() {
  return Boolean(SUPABASE_URL && SUPABASE_ANON_KEY);
}

if (!hasSupabaseRuntimeConfig()) {
  console.error(
    "Supabase configuration unavailable — ensure GNH_CONFIG or environment vars are set"
  );
}

// Global state
let supabaseClient = null;
let captchaToken = null;
const MAX_TURNSTILE_RETRIES = 2;
let pendingSignupSubmission = null;
let turnstileRetryCount = 0;
let awaitingCaptchaRefresh = false;
let captchaIssuedAt = null;
let turnstileWidgetId = null;

// Auth state refresh debouncing - prevents rapid-fire dashboard refreshes
const AUTH_REFRESH_DEBOUNCE_MS = 500;
let authRefreshTimeoutId = null;
let authStateSyncInitialised = false;
const AUTH_SYNC_RETRY_ATTEMPTS = 40;
const AUTH_SYNC_RETRY_DELAY_MS = 100;
let authSyncRetryTimer = null;
let authSyncRetryCount = 0;
let authCallbackRedirectIssued = false;
const PENDING_INVITE_TOKEN_STORAGE_KEY = "gnh_pending_invite_token";
const PENDING_CLI_CALLBACK_STORAGE_KEY = "gnh_pending_cli_callback";
const POST_AUTH_RETURN_TARGET_STORAGE_KEY = "gnh_post_auth_return_target";
const OAUTH_CALLBACK_QUERY_KEYS = [
  "error",
  "error_code",
  "error_description",
  "sb",
  "code",
  "state",
  "return_to",
];
const PUBLIC_ROUTE_PATHS = new Set([
  "/",
  "/welcome",
  "/welcome/",
  "/welcome/invite",
  "/welcome/invite/",
  "/extension-auth",
  "/extension-auth/",
  "/extension-auth.html",
  "/auth-modal.html",
  "/auth/callback",
  "/auth/callback/",
  "/debug-auth.html",
  "/test-login.html",
  "/test-components.html",
  "/test-data-components.html",
]);

function getCleanOAuthCallbackPath() {
  const url = new URL(window.location.href);
  url.hash = "";
  OAUTH_CALLBACK_QUERY_KEYS.forEach((key) => {
    url.searchParams.delete(key);
  });
  return `${url.pathname}${url.search}`;
}

function isProtectedRoutePath(pathname) {
  return !PUBLIC_ROUTE_PATHS.has(pathname);
}

function applyProtectedRouteAuthGate(isAuthenticated) {
  const body = document.body;
  if (!body) return;

  const shouldGate =
    !isAuthenticated && isProtectedRoutePath(window.location.pathname);
  body.classList.toggle("gnh-auth-route-gated", shouldGate);

  if (shouldGate) {
    setPostAuthReturnTargetFromCurrentPath();
    // Ensure the auth modal HTML is loaded, then show it.
    (async () => {
      try {
        await loadAuthModal();
        await waitForAuthScript();
        if (typeof window.setupAuthHandlers === "function") {
          window.setupAuthHandlers();
        }
        if (typeof window.showAuthModal === "function") {
          window.showAuthModal();
        }
      } catch (error) {
        console.error("Auth gate: failed to load login modal:", error);
      }
    })();
  }
}

function getPendingInviteToken() {
  try {
    return window.sessionStorage.getItem(PENDING_INVITE_TOKEN_STORAGE_KEY);
  } catch (_error) {
    return null;
  }
}

function setPendingInviteToken(token) {
  if (!token) return;
  try {
    window.sessionStorage.setItem(PENDING_INVITE_TOKEN_STORAGE_KEY, token);
  } catch (_error) {
    // sessionStorage may be unavailable.
  }
}

function clearPendingInviteToken() {
  try {
    window.sessionStorage.removeItem(PENDING_INVITE_TOKEN_STORAGE_KEY);
  } catch (_error) {
    // sessionStorage may be unavailable.
  }
}

function getPendingCliCallback() {
  try {
    return window.sessionStorage.getItem(PENDING_CLI_CALLBACK_STORAGE_KEY);
  } catch (_error) {
    return null;
  }
}

function setPendingCliCallback(url) {
  if (!url) return;
  try {
    window.sessionStorage.setItem(PENDING_CLI_CALLBACK_STORAGE_KEY, url);
  } catch (_error) {
    // sessionStorage may be unavailable.
  }
}

function clearPendingCliCallback() {
  try {
    window.sessionStorage.removeItem(PENDING_CLI_CALLBACK_STORAGE_KEY);
  } catch (_error) {
    // sessionStorage may be unavailable.
  }
}

function toSafeReturnPath(raw) {
  if (!raw) return "";
  try {
    const parsed = new URL(raw, window.location.origin);
    if (parsed.origin !== window.location.origin) return "";
    const path = `${parsed.pathname}${parsed.search}${parsed.hash}`;
    if (!path || path === "/" || path.startsWith("/auth-modal.html")) return "";
    return path;
  } catch (_error) {
    return "";
  }
}

function getPostAuthReturnTarget() {
  try {
    const params = new URLSearchParams(window.location.search);
    const fromQuery = toSafeReturnPath(params.get("return_to"));
    if (fromQuery) {
      return fromQuery;
    }
  } catch (_error) {
    // Ignore malformed query params.
  }

  try {
    const stored = window.sessionStorage.getItem(
      POST_AUTH_RETURN_TARGET_STORAGE_KEY
    );
    const safeStored = toSafeReturnPath(stored);
    if (safeStored) return safeStored;
  } catch (_error) {
    // Ignore storage errors and try localStorage.
  }

  try {
    const stored = window.localStorage.getItem(
      POST_AUTH_RETURN_TARGET_STORAGE_KEY
    );
    return toSafeReturnPath(stored);
  } catch (_error) {
    return "";
  }
}

function setPostAuthReturnTarget(path) {
  const safePath = toSafeReturnPath(path);
  if (!safePath) return;
  try {
    window.sessionStorage.setItem(
      POST_AUTH_RETURN_TARGET_STORAGE_KEY,
      safePath
    );
  } catch (_error) {
    // sessionStorage may be unavailable.
  }
  try {
    window.localStorage.setItem(POST_AUTH_RETURN_TARGET_STORAGE_KEY, safePath);
  } catch (_error) {
    // localStorage may be unavailable.
  }
}

function clearPostAuthReturnTarget() {
  try {
    window.sessionStorage.removeItem(POST_AUTH_RETURN_TARGET_STORAGE_KEY);
  } catch (_error) {
    // sessionStorage may be unavailable.
  }
  try {
    window.localStorage.removeItem(POST_AUTH_RETURN_TARGET_STORAGE_KEY);
  } catch (_error) {
    // localStorage may be unavailable.
  }
}

function setPostAuthReturnTargetFromCurrentPath() {
  if (window.location.pathname === "/") {
    return;
  }
  const currentPath = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  setPostAuthReturnTarget(currentPath);
}

function getOAuthCallbackURL(params = {}) {
  const callbackUrl = new URL("/auth/callback", window.location.origin);
  Object.entries(params).forEach(([key, value]) => {
    if (value !== undefined && value !== null && value !== "") {
      callbackUrl.searchParams.set(key, String(value));
    }
  });
  return callbackUrl.toString();
}

/**
 * Initialise Supabase client
 * @returns {boolean} Success status
 */
function initialiseSupabase() {
  // If already initialised (client has auth property), return success
  if (window.supabase && window.supabase.auth) {
    supabaseClient = window.supabase;
    return true;
  }

  if (!hasSupabaseRuntimeConfig()) {
    console.error(
      "Cannot initialise Supabase: missing supabaseUrl or supabaseAnonKey"
    );
    return false;
  }

  // Otherwise, create the client from the SDK
  if (window.supabase && window.supabase.createClient) {
    supabaseClient = window.supabase.createClient(
      SUPABASE_URL,
      SUPABASE_ANON_KEY
    );
    window.supabase = supabaseClient; // Ensure it's globally available
    return true;
  }
  return false;
}

/**
 * Load shared authentication modal HTML
 */
async function loadAuthModal() {
  const modalTarget = document.getElementById("authModalContainer");
  if (!modalTarget) {
    const error = new Error("Auth modal container missing");
    console.error("Failed to load auth modal:", error);
    if (window.Sentry) {
      window.Sentry.captureException(error, {
        tags: { component: "auth", action: "load_modal" },
      });
    }
    return false;
  }

  try {
    const response = await fetch("/auth-modal.html", { cache: "no-store" });

    if (!response.ok) {
      throw new Error(
        `Failed to fetch auth modal: ${response.status} ${response.statusText}`
      );
    }

    const modalHTML = await response.text();
    modalTarget.innerHTML = modalHTML;

    // Set default to login form for dashboard
    setTimeout(() => {
      if (window.showLoginForm) {
        showLoginForm();
      }
    }, 10);
  } catch (error) {
    console.error("Failed to load auth modal:", error);
    if (window.Sentry) {
      window.Sentry.captureException(error, {
        tags: { component: "auth", action: "load_modal" },
      });
    }
    modalTarget.innerHTML = `
      <div class="gnh-modal show" role="dialog" aria-live="assertive" aria-label="Sign in is unavailable">
        <div class="gnh-modal-content">
          <p>Sign-in is currently unavailable. Please refresh the page and try again.</p>
          <button type="button" class="gnh-button gnh-button-primary" onclick="window.location.reload()">Retry</button>
        </div>
      </div>
    `;
    return false;
  }

  return true;
}

function waitForAuthScript(pollIntervalMs = 50, timeoutMs = 12000) {
  return new Promise((resolve, reject) => {
    let attempts = 0;
    const maxAttempts = Math.ceil(timeoutMs / pollIntervalMs);
    const timer = setInterval(() => {
      if (
        typeof window.setupAuthHandlers === "function" &&
        typeof window.showAuthModal === "function"
      ) {
        clearInterval(timer);
        resolve();
        return;
      }
      attempts += 1;
      if (attempts >= maxAttempts) {
        clearInterval(timer);
        reject(new Error("Authentication modal script failed to load"));
      }
    }, pollIntervalMs);
  });
}

/**
 * Handle authentication callback tokens from OAuth redirects
 * @returns {Promise<boolean>} Whether tokens were processed
 */
async function handleAuthCallback() {
  try {
    const isExtensionAuthFlow = Boolean(window.GNH_APP?.extensionAuth);

    // Check for error parameters in URL (from OAuth failures)
    const urlParams = new URLSearchParams(window.location.search);
    const hasOAuthCallbackParams =
      urlParams.has("code") ||
      urlParams.has("state") ||
      urlParams.has("error") ||
      urlParams.has("error_code");
    const error = urlParams.get("error");
    const errorDescription = urlParams.get("error_description");

    if (error) {
      console.error("OAuth error:", error, errorDescription);
      const inviteToken =
        urlParams.get("invite_token") || getPendingInviteToken();
      if (
        !isExtensionAuthFlow &&
        inviteToken &&
        window.location.pathname !== "/welcome/invite"
      ) {
        const inviteUrl = new URL(
          `${window.location.origin}/welcome/invite?invite_token=${encodeURIComponent(inviteToken)}`
        );
        inviteUrl.searchParams.set("auth_error", "oauth_failed");
        authCallbackRedirectIssued = true;
        window.location.replace(inviteUrl.toString());
        return false;
      }

      // Clear error from URL
      history.replaceState(null, null, getCleanOAuthCallbackPath());
      // Show error to user
      if (window.showAuthError) {
        showAuthError("Authentication failed. Please try again.");
      }
      return false;
    }

    // Check if we have auth tokens in the URL hash
    const hashParams = new URLSearchParams(window.location.hash.substring(1));
    const accessToken = hashParams.get("access_token");
    const refreshToken = hashParams.get("refresh_token");
    const hasOAuthHashParams = Boolean(accessToken || refreshToken);
    const isOAuthCallbackReturn = hasOAuthCallbackParams || hasOAuthHashParams;

    if (accessToken) {
      // Set the session in Supabase using the tokens
      const {
        data: { session },
        error,
      } = await supabaseClient.auth.setSession({
        access_token: accessToken,
        refresh_token: refreshToken,
      });

      if (session) {
        const pendingInviteToken = getPendingInviteToken();
        if (
          !isExtensionAuthFlow &&
          pendingInviteToken &&
          window.location.pathname !== "/welcome/invite"
        ) {
          const inviteUrl = new URL(
            `${window.location.origin}/welcome/invite?invite_token=${encodeURIComponent(pendingInviteToken)}`
          );
          authCallbackRedirectIssued = true;
          window.location.replace(inviteUrl.toString());
          return false;
        }
        if (!isExtensionAuthFlow && isOAuthCallbackReturn) {
          const returnTarget = getPostAuthReturnTarget();
          if (returnTarget) {
            clearPostAuthReturnTarget();
            if (
              returnTarget !==
              `${window.location.pathname}${window.location.search}${window.location.hash}`
            ) {
              authCallbackRedirectIssued = true;
              window.location.replace(returnTarget);
              return false;
            }
          }
        }
        // Clear the URL hash to clean up the URL
        history.replaceState(null, null, getCleanOAuthCallbackPath());

        // Update user info will be called after dataBinder init
        return true;
      } else if (error) {
        console.error("Auth session setup error:", error);
      }
    } else {
      // Check if already authenticated
      const {
        data: { session },
      } = await supabaseClient.auth.getSession();
      if (session) {
        const pendingInviteToken = getPendingInviteToken();
        if (
          !isExtensionAuthFlow &&
          hasOAuthCallbackParams &&
          pendingInviteToken &&
          window.location.pathname !== "/welcome/invite"
        ) {
          const inviteUrl = new URL(
            `${window.location.origin}/welcome/invite?invite_token=${encodeURIComponent(pendingInviteToken)}`
          );
          authCallbackRedirectIssued = true;
          window.location.replace(inviteUrl.toString());
          return false;
        }
        if (!isExtensionAuthFlow && isOAuthCallbackReturn) {
          const returnTarget = getPostAuthReturnTarget();
          if (returnTarget) {
            clearPostAuthReturnTarget();
            if (
              returnTarget !==
              `${window.location.pathname}${window.location.search}${window.location.hash}`
            ) {
              authCallbackRedirectIssued = true;
              window.location.replace(returnTarget);
              return false;
            }
          }
        }
        return true;
      }
    }

    return false;
  } catch (error) {
    console.error("Auth callback processing error:", error);
    if (window.Sentry) {
      window.Sentry.captureException(error, {
        tags: { component: "auth", action: "process_callback" },
      });
    }
    return false;
  }
}

/**
 * Register user with backend database
 * @param {Object} user - Supabase user object
 * @returns {Promise<boolean>} Success status
 */
async function registerUserWithBackend(user) {
  if (!user || !user.id || !user.email) {
    console.error("Invalid user data for registration");
    return false;
  }

  try {
    const session = await supabaseClient.auth.getSession();
    if (!session.data.session) {
      console.error("No session available for registration");
      return false;
    }

    const metadata = user.user_metadata || {};
    const firstName =
      (metadata.given_name || metadata.first_name || "").trim() || null;
    const lastName =
      (metadata.family_name || metadata.last_name || "").trim() || null;
    const fullName =
      (
        metadata.full_name ||
        metadata.name ||
        composeDisplayName(firstName, lastName) ||
        ""
      ).trim() || null;

    const response = await fetch("/v1/auth/register", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${session.data.session.access_token}`,
      },
      body: JSON.stringify({
        user_id: user.id,
        email: user.email,
        first_name: firstName,
        last_name: lastName,
        full_name: fullName,
      }),
    });

    if (!response.ok) {
      // If user already exists (409 Conflict), that's fine
      if (response.status === 409) {
        return true;
      }
      const errorData = await response.json();
      console.error("Backend registration failed:", errorData);
      return false;
    }

    await response.json();
    return true;
  } catch (error) {
    console.error("Failed to register user with backend:", error);
    if (window.Sentry) {
      window.Sentry.captureException(error, {
        tags: { component: "auth", action: "backend_registration" },
        level: "error",
      });
    }
    return false;
  }
}

/**
 * Update UI elements based on authentication state
 * @param {boolean} isAuthenticated - Whether user is authenticated
 */
function updateAuthState(isAuthenticated) {
  // Show/hide elements based on authentication (support both old and new attributes)
  const allAuthElements = document.querySelectorAll(
    "[data-gnh-auth], [gnh-auth]"
  );

  const guestElements = [];
  const requiredElements = [];

  allAuthElements.forEach((el) => {
    const authValue =
      el.getAttribute("gnh-auth") || el.getAttribute("data-gnh-auth");
    if (authValue === "guest") {
      guestElements.push(el);
    } else if (authValue === "required") {
      requiredElements.push(el);
    }
  });

  // Authentication state display management
  // Strategy: Remove inline styles when showing elements (let CSS cascade work)
  // Only preserve inline styles that were explicitly set before we modified them
  guestElements.forEach((el) => {
    if (isAuthenticated) {
      // Store original inline display before hiding
      if (el.style.display && el.style.display !== "none") {
        el.dataset.originalDisplay = el.style.display;
      }
      el.style.display = "none";
    } else {
      // Restore: either use stored value or remove inline style
      if (el.dataset.originalDisplay) {
        el.style.display = el.dataset.originalDisplay;
      } else {
        el.style.removeProperty("display"); // Let CSS control it
      }
    }
  });

  requiredElements.forEach((el) => {
    if (!isAuthenticated) {
      // Store original inline display value (if any), but never store "none"
      // and don't overwrite an already-stored value
      if (
        el.style.display &&
        el.style.display !== "none" &&
        !el.dataset.originalDisplay
      ) {
        el.dataset.originalDisplay = el.style.display;
      }
    }

    if (isAuthenticated) {
      // Restore: either use stored value or remove inline style
      if (el.dataset.originalDisplay) {
        el.style.display = el.dataset.originalDisplay;
      } else {
        el.style.removeProperty("display"); // Let CSS control it
      }
    } else {
      el.style.display = "none";
    }
  });

  // If user just authenticated and dataBinder exists, load dashboard data (debounced)
  if (isAuthenticated && window.dataBinder) {
    // Clear any pending refresh to debounce rapid auth state changes
    if (authRefreshTimeoutId) {
      clearTimeout(authRefreshTimeoutId);
    }
    authRefreshTimeoutId = setTimeout(() => {
      authRefreshTimeoutId = null;
      window.dataBinder.refresh();
    }, AUTH_REFRESH_DEBOUNCE_MS);
  }

  // Re-setup logout handler after elements become visible
  if (isAuthenticated) {
    setTimeout(() => {
      const logoutBtn = document.getElementById("logoutBtn");
      if (
        logoutBtn &&
        !logoutBtn.hasAttribute("data-logout-handler-attached")
      ) {
        logoutBtn.addEventListener("click", async () => {
          try {
            const { error } = await supabaseClient.auth.signOut();
            if (error) {
              console.error("Logout error:", error);
              alert("Logout failed. Please try again.");
            } else {
              window.location.reload();
            }
          } catch (error) {
            console.error("Logout error:", error);
            alert("Logout failed. Please try again.");
          }
        });
        logoutBtn.setAttribute("data-logout-handler-attached", "true");
      }
    }, 150);
  }

  applyProtectedRouteAuthGate(isAuthenticated);
}

/**
 * Update user info in global-nav header elements.
 * Uses nav-scoped selectors so that ID lookups stay within the nav context.
 */
async function updateUserInfo() {
  const userEmailElement = document.querySelector(".global-nav #userEmail");
  const userAvatarElement = document.querySelector(".global-nav #userAvatar");

  if (!userEmailElement || !userAvatarElement) return;

  try {
    // Get current user from Supabase directly
    const {
      data: { session },
    } = await supabaseClient.auth.getSession();

    if (session && session.user && session.user.email) {
      const email = session.user.email;
      const metadata = session.user.user_metadata || {};
      const firstName =
        (metadata.given_name || metadata.first_name || "").trim() || "";
      const lastName =
        (metadata.family_name || metadata.last_name || "").trim() || "";
      const fullName = (
        metadata.full_name ||
        metadata.name ||
        composeDisplayName(firstName, lastName) ||
        ""
      ).trim();
      const displayLabel = fullName || email;

      userEmailElement.textContent = displayLabel;

      // Update avatar with Gravatar fallback to initials
      const initials = getInitials(displayLabel);
      await setUserAvatar(userAvatarElement, email, initials);
    } else {
      // No session, reset to defaults
      userEmailElement.textContent = "Loading...";
      userAvatarElement.textContent = "?";
    }
  } catch (error) {
    console.error("Failed to update user info:", error);
    userEmailElement.textContent = "Error";
    userAvatarElement.textContent = "?";
  }
}

/**
 * Generate initials from a display name or email address.
 * @param {string} value - Name or email address
 * @returns {string} User initials
 */
function getInitials(value) {
  const raw = (value || "").trim();
  if (!raw) return "?";

  // Name format: "Jane Doe" -> "JD"
  if (raw.includes(" ")) {
    const parts = raw.split(/\s+/).filter(Boolean).slice(0, 2);
    if (parts.length) {
      return parts.map((part) => part.charAt(0).toUpperCase()).join("");
    }
  }

  // Email format fallback
  const emailPrefix = raw.includes("@") ? raw.split("@")[0] : raw;
  const parts = emailPrefix.split(/[._-]+/).filter(Boolean);
  if (parts.length >= 2) {
    return parts
      .slice(0, 2)
      .map((part) => part.charAt(0).toUpperCase())
      .join("");
  }

  return emailPrefix.slice(0, 2).toUpperCase();
}

function composeDisplayName(firstName, lastName) {
  const first = (firstName || "").trim();
  const last = (lastName || "").trim();
  const full = `${first} ${last}`.trim();
  return full || "";
}

async function setUserAvatar(target, email, initials, options = {}) {
  if (!target) return;

  const existingImg = target.querySelector("img");
  if (existingImg) {
    existingImg.remove();
  }

  target.textContent = initials ?? "?";

  const gravatarUrl = await getGravatarUrl(email, options.size ?? 80);
  if (!gravatarUrl) return;

  const avatarImg = document.createElement("img");
  avatarImg.src = gravatarUrl;
  avatarImg.alt = options.alt ?? "User avatar";
  avatarImg.loading = "lazy";
  avatarImg.decoding = "async";
  avatarImg.addEventListener(
    "load",
    () => {
      target.textContent = "";
      target.appendChild(avatarImg);
    },
    { once: true }
  );
  avatarImg.addEventListener(
    "error",
    () => {
      if (avatarImg.parentNode) {
        avatarImg.parentNode.removeChild(avatarImg);
      }
      target.textContent = initials ?? "?";
    },
    { once: true }
  );
}

async function getGravatarUrl(email, size) {
  const normalised = (email || "").trim().toLowerCase();
  if (!normalised || !window.crypto?.subtle) return "";

  const encoder = new TextEncoder();
  const data = encoder.encode(normalised);
  try {
    const digest = await window.crypto.subtle.digest("SHA-256", data);
    const hash = [...new Uint8Array(digest)]
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    const params = new URLSearchParams({
      s: String(size || 80),
      d: "404",
    });
    return `https://www.gravatar.com/avatar/${hash}?${params.toString()}`;
  } catch (error) {
    console.warn("Failed to generate Gravatar hash:", error);
    return "";
  }
}

/**
 * Show authentication modal
 */
function showAuthModal() {
  const authModal = document.getElementById("authModal");
  if (authModal) {
    setPostAuthReturnTargetFromCurrentPath();
    authModal.classList.add("show");
    showAuthForm("login");
  }
}

/**
 * Close authentication modal
 */
function closeAuthModal() {
  const authModal = document.getElementById("authModal");
  if (authModal) {
    authModal.classList.remove("show");
    clearAuthError();
    hideAuthLoading();
  }
}

/**
 * Show specific authentication form
 * @param {string} formType - Type of form to show: 'login', 'signup', 'reset'
 */
function showAuthForm(formType) {
  // Hide all forms
  const loginForm = document.getElementById("loginForm");
  const signupForm = document.getElementById("signupForm");
  const resetForm = document.getElementById("resetForm");

  if (loginForm) loginForm.style.display = "none";
  if (signupForm) signupForm.style.display = "none";
  if (resetForm) resetForm.style.display = "none";

  // Show selected form
  const titles = {
    login: "Sign In",
    signup: "Create Account",
    reset: "Reset Password",
  };

  const authModalTitle = document.getElementById("authModalTitle");
  if (authModalTitle) {
    if (Object.hasOwn(titles, formType)) {
      authModalTitle.textContent = titles[formType];
    } else {
      console.warn("Unknown form type:", formType);
      authModalTitle.textContent = "Authentication";
    }
  }

  const targetForm = document.getElementById(`${formType}Form`);
  if (targetForm) {
    targetForm.style.display = "block";
  }

  // Reset CAPTCHA state for signup form
  if (formType === "signup") {
    captchaToken = null;
    setSignupButtonEnabled(true);
    ensureTurnstileWidgetRendered();
    resetTurnstileWidget("show_form");
  }

  clearAuthError();
  hideAuthLoading();
}

function setSignupButtonEnabled(enabled) {
  const signupBtn = document.getElementById("signupSubmitBtn");
  if (signupBtn) {
    signupBtn.disabled = !enabled;
  }
}

function isTurnstileEnabled() {
  const config = window.GNH_CONFIG || {};
  return Boolean(window.GNH_APP?.enableTurnstile ?? config.enableTurnstile);
}

function getTurnstileWidget() {
  return document.querySelector(".cf-turnstile");
}

function ensureTurnstileWidgetRendered(maxAttempts = 10, delayMs = 200) {
  if (!isTurnstileEnabled()) {
    return;
  }

  const widget = getTurnstileWidget();
  if (!widget) {
    return;
  }
  if (turnstileWidgetId !== null) {
    return;
  }

  let attempts = 0;
  const tryRender = () => {
    attempts += 1;
    if (!window.turnstile) {
      if (attempts < maxAttempts) {
        setTimeout(tryRender, delayMs);
      } else {
        console.warn("Turnstile script did not load within retry window", {
          maxAttempts,
          delayMs,
          sitekey: widget.dataset.sitekey || null,
        });
      }
      return;
    }

    try {
      turnstileWidgetId = window.turnstile.render(widget, {
        sitekey: widget.dataset.sitekey,
        callback: window.onTurnstileSuccess,
      });
    } catch (error) {
      if (attempts < maxAttempts) {
        setTimeout(tryRender, delayMs);
      } else {
        console.warn("Failed to render Turnstile widget:", error);
      }
    }
  };

  tryRender();
}

function resetTurnstileWidget(reason = "manual") {
  captchaToken = null;
  captchaIssuedAt = null;
  setSignupButtonEnabled(true);

  if (!isTurnstileEnabled()) {
    return;
  }

  const widget = getTurnstileWidget();
  if (!widget) {
    return;
  }

  ensureTurnstileWidgetRendered();

  if (!window.turnstile || turnstileWidgetId === null) {
    return;
  }

  try {
    window.turnstile.reset(turnstileWidgetId);
    console.debug("Turnstile reset", { reason });
  } catch (error) {
    console.debug("Skipped Turnstile reset", {
      reason,
      error: error?.message,
    });
  }
}

function shouldRetryTurnstile(error) {
  if (!error) {
    return false;
  }
  const rawMessage =
    `${error.message || ""} ${error.error_description || ""}`.toLowerCase();
  if (rawMessage.includes("106010")) {
    return true;
  }
  if (rawMessage.includes("turnstile") || rawMessage.includes("captcha")) {
    return true;
  }
  return false;
}

function getEmailSignupRedirectTarget() {
  const redirectUrl = new URL(window.location.href);
  redirectUrl.hash = "";
  return redirectUrl.toString();
}

function recordTurnstileEvent(event, metadata = {}) {
  const details = {
    event,
    tokenAgeMs: getCaptchaTokenAgeMs(),
    retries: turnstileRetryCount,
    awaitingCaptchaRefresh,
    ...metadata,
  };
  console.debug("Turnstile event", details);
  if (window.Sentry) {
    window.Sentry.captureMessage(`turnstile.${event}`, {
      level: "info",
      tags: { component: "auth", feature: "turnstile" },
      extra: details,
    });
  }
}

function getCaptchaTokenAgeMs() {
  if (!captchaIssuedAt) {
    return null;
  }
  return Date.now() - captchaIssuedAt;
}

/**
 * Handle email/password login
 * @param {Event} event - Form submit event
 */
async function handleEmailLogin(event) {
  event.preventDefault();
  const formData = new FormData(event.target);
  const email = formData.get("email");
  const password = formData.get("password");

  showAuthLoading();
  clearAuthError();

  try {
    const { data, error } = await supabaseClient.auth.signInWithPassword({
      email,
      password,
    });

    if (error) throw error;

    const handler =
      typeof window.handleAuthSuccess === "function"
        ? window.handleAuthSuccess
        : defaultHandleAuthSuccess;
    await handler(data.user);
  } catch (error) {
    console.error("Email login error:", error);
    if (window.Sentry) {
      window.Sentry.captureException(error, {
        tags: { component: "auth", action: "email_login" },
        level: "warning",
      });
    }
    showAuthError(
      error.message || "Login failed. Please check your credentials."
    );
  } finally {
    hideAuthLoading();
  }
}

/**
 * Handle email signup
 * @param {Event} event - Form submit event
 */
async function handleEmailSignup(event) {
  event.preventDefault();
  const formData = new FormData(event.target);
  pendingSignupSubmission = {
    email: formData.get("email"),
    firstName: (formData.get("firstName") || "").trim(),
    lastName: (formData.get("lastName") || "").trim(),
    password: formData.get("password"),
    passwordConfirm: formData.get("passwordConfirm"),
  };
  turnstileRetryCount = 0;
  awaitingCaptchaRefresh = false;

  await executeEmailSignup();
}

async function executeEmailSignup() {
  if (!pendingSignupSubmission) {
    return;
  }

  const { email, firstName, lastName, password, passwordConfirm } =
    pendingSignupSubmission;

  if (password !== passwordConfirm) {
    showAuthError("Passwords do not match.");
    return;
  }

  if (password.length < 6) {
    showAuthError("Password must be at least 6 characters long.");
    return;
  }

  if (isTurnstileEnabled() && !captchaToken) {
    showAuthError("Please complete the CAPTCHA verification.");
    return;
  }

  showAuthLoading();
  clearAuthError();
  recordTurnstileEvent("signup_attempt", {
    tokenPresent: Boolean(captchaToken),
  });

  try {
    const signupOptions = {};
    if (isTurnstileEnabled() && captchaToken) {
      signupOptions.captchaToken = captchaToken;
    }
    signupOptions.emailRedirectTo = getEmailSignupRedirectTarget();
    const fullName = composeDisplayName(firstName, lastName);
    signupOptions.data = {
      first_name: firstName || "",
      last_name: lastName || "",
      given_name: firstName || "",
      family_name: lastName || "",
      full_name: fullName || "",
      name: fullName || "",
    };

    const { data, error } = await supabaseClient.auth.signUp({
      email,
      password,
      options: signupOptions,
    });

    if (error) throw error;

    recordTurnstileEvent("signup_success", { userId: data.user?.id || null });

    pendingSignupSubmission = null;
    turnstileRetryCount = 0;
    awaitingCaptchaRefresh = false;
    resetTurnstileWidget("post_signup_success");

    if (data.user && !data.user.email_confirmed_at) {
      showAuthError(
        "Please check your email and click the confirmation link before signing in."
      );
      showAuthForm("login");
    } else if (data.user) {
      const handler =
        typeof window.handleAuthSuccess === "function"
          ? window.handleAuthSuccess
          : defaultHandleAuthSuccess;
      await handler(data.user);
    }
  } catch (error) {
    const retryable = shouldRetryTurnstile(error);
    const canRetryTurnstileChallenge = Boolean(
      isTurnstileEnabled() && window.turnstile && getTurnstileWidget()
    );
    recordTurnstileEvent("signup_error", {
      retryable,
      canRetryTurnstileChallenge,
      message: error.message,
      status: error.status,
    });

    if (retryable && !canRetryTurnstileChallenge) {
      awaitingCaptchaRefresh = false;
      pendingSignupSubmission = null;
      console.error("Turnstile challenge required but unavailable:", error);
      showAuthError(
        "Email signup is unavailable right now. Please use Google or GitHub sign-in."
      );
      hideAuthLoading();
      return;
    }

    if (retryable && turnstileRetryCount < MAX_TURNSTILE_RETRIES) {
      turnstileRetryCount += 1;
      awaitingCaptchaRefresh = true;
      console.warn("Turnstile token rejected, scheduling retry", {
        attempt: turnstileRetryCount,
      });
      showAuthError(
        "Security check expired. Please complete the verification again."
      );
      hideAuthLoading();
      resetTurnstileWidget("captcha_retry");
      return;
    }

    awaitingCaptchaRefresh = false;
    pendingSignupSubmission = null;
    console.error("Email signup error:", error);
    if (!retryable && window.Sentry) {
      window.Sentry.captureException(error, {
        tags: { component: "auth", action: "email_signup" },
        level: "warning",
      });
    }
    showAuthError(error.message || "Signup failed. Please try again.");
  } finally {
    if (!awaitingCaptchaRefresh) {
      hideAuthLoading();
    }
  }
}

/**
 * CLI callback hook: when the page is opened with ?cli_callback=<loopback-url>,
 * POST the Supabase session to the CLI's local server after successful auth.
 * Returns true if a callback was sent, false otherwise.
 */
async function trySendCliCallback(callbackUrlOverride) {
  const params = new URLSearchParams(window.location.search);
  const callbackUrl = callbackUrlOverride || params.get("cli_callback");
  if (!callbackUrl) {
    return false;
  }

  // Only allow loopback targets to prevent session exfiltration.
  try {
    const parsed = new URL(callbackUrl);
    const isLoopback =
      parsed.hostname === "127.0.0.1" ||
      parsed.hostname === "localhost" ||
      parsed.hostname === "::1";
    if (!isLoopback || parsed.protocol !== "http:") {
      console.warn("CLI callback: rejected non-loopback URL", callbackUrl);
      return false;
    }
  } catch (_error) {
    return false;
  }

  try {
    if (!supabaseClient) {
      return false;
    }
    const {
      data: { session },
      error,
    } = await supabaseClient.auth.getSession();
    if (error || !session) {
      return false;
    }

    const response = await fetch(callbackUrl, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session }),
    });

    if (response.ok) {
      document.title = "Hover CLI — Authenticated";
      // Replace the page body with a safe "done" message.
      while (document.body.firstChild) {
        document.body.removeChild(document.body.firstChild);
      }
      const wrapper = document.createElement("div");
      wrapper.style.cssText =
        "display:flex;justify-content:center;align-items:center;min-height:100vh;font-family:system-ui;color:#e0e0e0;background:#0a0a0a";
      const inner = document.createElement("div");
      inner.style.textAlign = "center";
      const heading = document.createElement("h2");
      heading.textContent = "Authentication complete";
      const detail = document.createElement("p");
      detail.textContent =
        "You can close this tab and return to your terminal.";
      inner.appendChild(heading);
      inner.appendChild(detail);
      wrapper.appendChild(inner);
      document.body.appendChild(wrapper);
      return true;
    }
    console.error("CLI callback failed:", response.status);
  } catch (error) {
    console.error("CLI callback error:", error);
  }
  return false;
}

async function defaultHandleAuthSuccess(user) {
  await registerUserWithBackend(user);
  closeAuthModal();
  updateUserInfo();
  updateAuthState(true);

  // CLI callback: if the page was opened with ?cli_callback=<url>, send the
  // Supabase session to the CLI's loopback server before any redirect.
  const cliCallbackSent = await trySendCliCallback();

  if (window.dataBinder) {
    await window.dataBinder.refresh();
  }
  await handlePendingDomain();

  // If we sent the session to the CLI, don't redirect — show a done message.
  if (cliCallbackSent) {
    return;
  }

  const returnTarget = getPostAuthReturnTarget();
  if (returnTarget) {
    clearPostAuthReturnTarget();
    if (
      returnTarget !==
      `${window.location.pathname}${window.location.search}${window.location.hash}`
    ) {
      window.location.assign(returnTarget);
    }
    return;
  }

  if (isProtectedRoutePath(window.location.pathname)) {
    return;
  }

  if (window.location.pathname === "/") {
    window.location.assign("/dashboard");
  }
}

/**
 * Handle password reset
 * @param {Event} event - Form submit event
 */
async function handlePasswordReset(event) {
  event.preventDefault();
  const formData = new FormData(event.target);
  const email = formData.get("email");

  showAuthLoading();
  clearAuthError();

  try {
    const { error } = await supabaseClient.auth.resetPasswordForEmail(email, {
      redirectTo: `${window.location.origin}/dashboard`,
    });

    if (error) throw error;

    showAuthError("Password reset email sent! Check your inbox.", "success");
    setTimeout(() => {
      showAuthForm("login");
    }, 2000);
  } catch (error) {
    console.error("Password reset error:", error);
    showAuthError(error.message || "Failed to send reset email.");
  } finally {
    hideAuthLoading();
  }
}

/**
 * Handle social login (Google, GitHub)
 * @param {string} provider - OAuth provider name
 */
async function handleSocialLogin(provider, options = {}) {
  showAuthLoading();
  clearAuthError();

  try {
    const getOAuthRedirectTarget = () => {
      const params = new URLSearchParams(window.location.search);
      const inviteToken = params.get("invite_token");
      if (inviteToken) {
        setPendingInviteToken(inviteToken);
      }
      const cliCallback = params.get("cli_callback");
      if (cliCallback) {
        setPendingCliCallback(cliCallback);
      }
      return getOAuthCallbackURL({ invite_token: inviteToken || undefined });
    };

    const redirectOverride =
      options.redirectTo ||
      window.GNH_APP?.oauthRedirectOverride ||
      (window.GNH_APP?.extensionAuth ? window.location.href : "");

    const { data, error } = await supabaseClient.auth.signInWithOAuth({
      provider,
      options: {
        redirectTo: redirectOverride || getOAuthRedirectTarget(),
      },
    });

    if (error) throw error;

    // OAuth will redirect, so no need to handle success here
  } catch (error) {
    console.error("Social login error:", error);
    if (window.Sentry) {
      window.Sentry.captureException(error, {
        tags: { component: "auth", action: "social_login", provider: provider },
        level: "warning",
      });
    }
    showAuthError(error.message || `${provider} login failed.`);
    hideAuthLoading();
  } finally {
    if (window.GNH_APP?.oauthRedirectOverride) {
      delete window.GNH_APP.oauthRedirectOverride;
    }
  }
}

async function initAuthCallbackPage() {
  authCallbackRedirectIssued = false;

  if (!initialiseSupabase()) {
    authCallbackRedirectIssued = true;
    window.location.replace("/");
    return;
  }

  try {
    await handleAuthCallback();
  } catch (error) {
    console.error("Auth callback page failed:", error);
  }

  if (authCallbackRedirectIssued) {
    return;
  }

  // CLI callback: if a pending cli_callback was stored before OAuth redirect,
  // send the session to the CLI's loopback server now.
  const pendingCliCallback = getPendingCliCallback();
  if (pendingCliCallback) {
    const sent = await trySendCliCallback(pendingCliCallback);
    if (sent) {
      clearPendingCliCallback();
      return;
    }
  }

  const returnTarget = getPostAuthReturnTarget();
  if (returnTarget) {
    clearPostAuthReturnTarget();
    authCallbackRedirectIssued = true;
    window.location.replace(returnTarget);
    return;
  }

  const {
    data: { session },
  } = await supabaseClient.auth.getSession();
  if (session) {
    authCallbackRedirectIssued = true;
    window.location.replace("/dashboard");
    return;
  }

  authCallbackRedirectIssued = true;
  window.location.replace("/");
}

/**
 * Handle pending domain after authentication
 */
async function handlePendingDomain() {
  const pendingDomain = sessionStorage.getItem("gnh_pending_domain");
  if (pendingDomain && window.dataBinder?.authManager?.isAuthenticated) {
    // Clear the stored domain
    sessionStorage.removeItem("gnh_pending_domain");

    // Auto-create job
    try {
      const response = await window.dataBinder.fetchData("/v1/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          domain: pendingDomain,
          use_sitemap: true,
          find_links: true,
          max_pages: 0,
          // concurrency omitted - uses server default (20)
        }),
      });

      if (window.showSuccessMessage) {
        window.showSuccessMessage(
          `Started crawling ${pendingDomain}! Your cache warming has begun.`
        );
      }

      // Refresh dashboard to show new job
      if (window.dataBinder) {
        await window.dataBinder.refresh();
      }
    } catch (error) {
      console.error("Failed to create job from pending domain:", error);
      if (window.showDashboardError) {
        window.showDashboardError(
          `Failed to start crawling ${pendingDomain}. Please try creating the job manually.`
        );
      }
    }
  }
}

/**
 * Show authentication loading state
 */
function showAuthLoading() {
  const authLoading = document.getElementById("authLoading");
  const visibleForm = document.querySelector(
    '.gnh-auth-form:not([style*="display: none"])'
  );

  if (authLoading) {
    authLoading.style.display = "block";
  }
  if (visibleForm) {
    visibleForm.style.display = "none";
  }
}

/**
 * Hide authentication loading state
 */
function hideAuthLoading() {
  const authLoading = document.getElementById("authLoading");
  if (authLoading) {
    authLoading.style.display = "none";
  }

  // Show appropriate form based on current title
  const authModalTitle = document.getElementById("authModalTitle");
  if (authModalTitle) {
    const title = authModalTitle.textContent;
    if (title === "Sign In") {
      const loginForm = document.getElementById("loginForm");
      if (loginForm) loginForm.style.display = "block";
    } else if (title === "Create Account") {
      const signupForm = document.getElementById("signupForm");
      if (signupForm) signupForm.style.display = "block";
    } else if (title === "Reset Password") {
      const resetForm = document.getElementById("resetForm");
      if (resetForm) resetForm.style.display = "block";
    }
  }
}

/**
 * Show authentication error message
 * @param {string} message - Error message to display
 * @param {string} type - Message type: 'error' or 'success'
 */
function showAuthError(message, type = "error") {
  const errorDiv = document.getElementById("authError");
  if (errorDiv) {
    errorDiv.textContent = message;
    errorDiv.style.display = "block";

    if (type === "success") {
      errorDiv.style.background = "#dcfce7";
      errorDiv.style.color = "#16a34a";
      errorDiv.style.borderColor = "#bbf7d0";
    } else {
      errorDiv.style.background = "#fee2e2";
      errorDiv.style.color = "#dc2626";
      errorDiv.style.borderColor = "#fecaca";
    }
  }
}

/**
 * Clear authentication error message
 */
function clearAuthError() {
  const errorDiv = document.getElementById("authError");
  if (errorDiv) {
    errorDiv.style.display = "none";
  }
}

/**
 * Setup password strength validation using zxcvbn
 */
function setupPasswordStrength() {
  const passwordInput = document.getElementById("signupPassword");
  const confirmInput = document.getElementById("signupPasswordConfirm");
  const strengthIndicator = document.getElementById("passwordStrength");
  const strengthFill = document.getElementById("strengthFill");
  const strengthText = document.getElementById("strengthText");
  const strengthFeedback = document.getElementById("strengthFeedback");

  // Exit silently if elements don't exist (not on a signup page)
  if (!passwordInput || !confirmInput) {
    return;
  }

  // Exit silently if zxcvbn library not loaded
  if (typeof zxcvbn === "undefined") {
    return;
  }

  // Show strength indicator when password field gets focus
  passwordInput.addEventListener("focus", () => {
    if (strengthIndicator) {
      strengthIndicator.style.display = "block";
    }
  });

  // Real-time password strength checking
  passwordInput.addEventListener("input", (e) => {
    const password = e.target.value;

    if (password.length === 0) {
      if (strengthIndicator) {
        strengthIndicator.style.display = "none";
      }
      return;
    }

    if (strengthIndicator) {
      strengthIndicator.style.display = "block";
    }

    // Use zxcvbn to evaluate password strength
    const result = zxcvbn(password);
    const score = result.score; // 0-4 scale

    // Clear previous classes
    if (strengthFill) {
      strengthFill.className = "gnh-strength-fill";
    }
    if (strengthText) {
      strengthText.className = "gnh-strength-text";
    }

    // Apply strength classes and text
    let strengthLabel = "";
    switch (score) {
      case 0:
      case 1:
        if (strengthFill) strengthFill.classList.add("weak");
        if (strengthText) strengthText.classList.add("weak");
        strengthLabel = "Weak";
        break;
      case 2:
        if (strengthFill) strengthFill.classList.add("fair");
        if (strengthText) strengthText.classList.add("fair");
        strengthLabel = "Fair";
        break;
      case 3:
        if (strengthFill) strengthFill.classList.add("good");
        if (strengthText) strengthText.classList.add("good");
        strengthLabel = "Good";
        break;
      case 4:
        if (strengthFill) strengthFill.classList.add("strong");
        if (strengthText) strengthText.classList.add("strong");
        strengthLabel = "Strong";
        break;
    }

    if (strengthText) {
      strengthText.textContent = `Password strength: ${strengthLabel}`;
    }

    // Show feedback and suggestions
    let feedback = "";
    if (result.feedback.warning) {
      feedback += result.feedback.warning + ". ";
    }
    if (result.feedback.suggestions.length > 0) {
      feedback += result.feedback.suggestions.join(". ");
    }
    if (password.length < 8) {
      feedback = "Password must be at least 8 characters long. " + feedback;
    }

    if (strengthFeedback) {
      strengthFeedback.textContent = feedback;
    }

    // Validate confirm password if it has content
    if (confirmInput && confirmInput.value) {
      validatePasswordMatch();
    }
  });

  // Real-time password confirmation checking
  if (confirmInput) {
    confirmInput.addEventListener("input", validatePasswordMatch);
  }

  function validatePasswordMatch() {
    const password = passwordInput.value;
    const confirm = confirmInput.value;

    // Remove existing validation styling
    confirmInput.classList.remove("gnh-field-valid", "gnh-field-invalid");
    const existingError =
      confirmInput.parentElement.querySelector(".gnh-field-error");
    if (existingError) {
      existingError.remove();
    }

    if (confirm.length > 0) {
      if (password === confirm) {
        confirmInput.classList.add("gnh-field-valid");
      } else {
        confirmInput.classList.add("gnh-field-invalid");
        const errorDiv = document.createElement("div");
        errorDiv.className = "gnh-field-error";
        errorDiv.textContent = "Passwords do not match";
        errorDiv.style.cssText =
          "color: #dc2626; font-size: 12px; margin-top: 4px;";
        confirmInput.parentElement.appendChild(errorDiv);
      }
    }
  }
}

/**
 * Setup authentication event handlers
 */
function setupAuthHandlers() {
  handleAuthCallback()
    .then((hasSession) => {
      if (hasSession) {
        updateAuthState(true);
        updateUserInfo();
      }
    })
    .catch((error) => {
      console.warn("Auth callback setup failed:", error);
    });

  // Use event delegation for main auth buttons that might not exist initially
  document.addEventListener("click", (e) => {
    const target = e.target;

    // Handle login button clicks (various IDs)
    if (target.id === "loginBtn" || target.id === "showLoginBtn") {
      e.preventDefault();
      showAuthModal();
      showAuthForm("login");
    }

    // Handle signup button clicks
    if (target.id === "showSignupBtn") {
      e.preventDefault();
      showAuthModal();
      showAuthForm("signup");
    }

    // Handle logout button clicks
    if (target.id === "logoutBtn") {
      e.preventDefault();
      handleLogout();
    }
  });

  // Set up modal form handlers
  setupAuthModalHandlers();

  // Set up password strength checking if signup form is present
  setupPasswordStrength();

  if (!initialiseAuthStateSync()) {
    scheduleAuthStateSyncRetry();
  }

  // CLI auth: show login modal on public routes when cli_callback is present.
  (async () => {
    try {
      const params = new URLSearchParams(window.location.search);
      if (!params.get("cli_callback")) return;

      // Ensure Supabase client is initialized before calling getSession.
      await waitForAuthScript();
      if (!supabaseClient || !supabaseClient.auth) {
        console.error("CLI auth: Supabase client not initialized");
        return;
      }

      const { data } = await supabaseClient.auth.getSession();
      if (!data?.session) {
        await loadAuthModal();
        showAuthModal();
      }
    } catch (error) {
      console.error("CLI auth modal auto-open failed:", error);
    }
  })();
}

function initialiseAuthStateSync() {
  if (authStateSyncInitialised) {
    return true;
  }
  if (!supabaseClient?.auth && !initialiseSupabase()) {
    return false;
  }
  authStateSyncInitialised = true;

  supabaseClient.auth
    .getSession()
    .then(({ data }) => {
      const session = data?.session;
      const isAuthenticated = Boolean(session);
      updateAuthState(isAuthenticated);
      if (isAuthenticated) {
        updateUserInfo();
        trySendCliCallback();
      }
    })
    .catch((error) => {
      console.warn("Failed to synchronise initial auth state:", error);
      updateAuthState(false);
    });

  supabaseClient.auth.onAuthStateChange((_event, session) => {
    const isAuthenticated = Boolean(session);
    updateAuthState(isAuthenticated);
    if (isAuthenticated) {
      updateUserInfo();
    }
  });
  return true;
}

function scheduleAuthStateSyncRetry() {
  if (authStateSyncInitialised || authSyncRetryTimer) {
    return;
  }

  authSyncRetryCount = 0;
  authSyncRetryTimer = setInterval(() => {
    authSyncRetryCount += 1;
    if (
      initialiseAuthStateSync() ||
      authSyncRetryCount >= AUTH_SYNC_RETRY_ATTEMPTS
    ) {
      clearInterval(authSyncRetryTimer);
      authSyncRetryTimer = null;
    }
  }, AUTH_SYNC_RETRY_DELAY_MS);
}

/**
 * Handle logout action
 */
async function handleLogout() {
  try {
    const { error } = await supabaseClient.auth.signOut();
    if (error) {
      console.error("Logout error:", error);
      alert("Logout failed. Please try again.");
    } else {
      window.location.reload();
    }
  } catch (error) {
    console.error("Logout error:", error);
    alert("Logout failed. Please try again.");
  }
}

/**
 * Setup authentication modal form handlers
 */
function setupAuthModalHandlers() {
  // Use event delegation to handle form submissions even when modal loads later
  document.addEventListener("submit", (e) => {
    if (e.target.id === "emailLoginForm") {
      e.preventDefault();
      handleEmailLogin(e);
    } else if (e.target.id === "emailSignupForm") {
      e.preventDefault();
      handleEmailSignup(e);
    } else if (e.target.id === "passwordResetForm") {
      e.preventDefault();
      handlePasswordReset(e);
    }
  });

  // Use event delegation for social login buttons
  document.addEventListener("click", (e) => {
    if (e.target.closest(".gnh-social-btn[data-provider]")) {
      e.preventDefault();
      const button = e.target.closest(".gnh-social-btn[data-provider]");
      const provider = button.dataset.provider;
      const handler =
        typeof window.handleSocialLogin === "function"
          ? window.handleSocialLogin
          : handleSocialLogin;
      handler(provider);
    }

    // Handle modal close
    if (e.target.closest(".gnh-modal-close") || e.target.id === "authModal") {
      if (e.target.id === "authModal" && e.target === e.currentTarget) {
        // Only close if clicking the backdrop
        closeAuthModal();
      } else if (e.target.closest(".gnh-modal-close")) {
        closeAuthModal();
      }
    }
  });
}

/**
 * Setup login page handlers (for homepage integration)
 */
function setupLoginPageHandlers() {
  // This is now handled by event delegation in setupAuthHandlers()
  // No need for direct element handlers since they're covered by delegation
}

function isValidExtensionTargetOrigin(rawOrigin) {
  if (!rawOrigin) return false;
  try {
    const parsed = new URL(rawOrigin);
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
      return false;
    }

    // Allow extension origins used by Webflow dev/test workflows and custom
    // preview/deployment hosts.
    const host = parsed.hostname.toLowerCase();
    if (
      host === "localhost" ||
      host === "127.0.0.1" ||
      host.endsWith(".webflow-ext.com")
    ) {
      return true;
    }

    // Allow explicit deploy preview URLs and staging hosts used by the adapter.
    return host.endsWith(".fly.dev");
  } catch (_error) {
    return false;
  }
}

function initExtensionAuthPage() {
  const params = new URLSearchParams(window.location.search);
  const targetOrigin = params.get("origin") || "";
  const extensionState =
    params.get("extension_state") || params.get("state") || "";
  const statusEl = document.getElementById("extensionAuthStatus");
  const modalContainer = document.getElementById("authModalContainer");
  const reopenButton = document.getElementById("reopenModalBtn");

  if (!statusEl || !modalContainer) {
    console.warn("Extension auth: required elements missing");
    return;
  }

  function setStatus(message, isError = false) {
    statusEl.textContent = message;
    statusEl.classList.toggle("error", Boolean(isError));
  }

  if (!window.opener || window.opener.closed) {
    setStatus(
      "This login window must be opened from the Webflow extension.",
      true
    );
    return;
  }

  if (!isValidExtensionTargetOrigin(targetOrigin)) {
    setStatus("Invalid extension origin. Please reopen sign-in.", true);
    return;
  }

  // Best-effort validation of the opener origin. Browsers can omit referrer
  // headers in popup flows, so we no longer fail closed on missing referrer.
  try {
    const referrerOrigin = document.referrer
      ? new URL(document.referrer).origin
      : "";
    if (referrerOrigin && referrerOrigin !== targetOrigin) {
      setStatus("Origin mismatch. Please relaunch from the extension.", true);
      return;
    }
  } catch (_error) {
    setStatus("Unable to validate opener origin. Please relaunch.", true);
    return;
  }

  if (!initialiseSupabase()) {
    setStatus("Supabase initialisation failed. Please retry.", true);
    return;
  }

  const postAuthMessage = (message) => {
    try {
      window.opener.postMessage(
        {
          source: "gnh-extension-auth",
          state: extensionState,
          extensionState,
          ...message,
        },
        targetOrigin
      );
      return true;
    } catch (error) {
      console.error("Extension auth: postMessage failed", error);
      return false;
    }
  };

  const closePopupSoon = () => {
    window.setTimeout(() => {
      window.close();
    }, 350);
  };

  async function sendSessionToExtension() {
    const {
      data: { session },
      error,
    } = await supabaseClient.auth.getSession();

    if (error || !session?.access_token || !session.user) {
      throw new Error(error?.message || "No active session found", {
        cause: error,
      });
    }

    const registered = await registerUserWithBackend(session.user);
    if (!registered) {
      console.warn("Account setup could not be completed; continuing sign-in");
    }

    const posted = postAuthMessage({
      type: "success",
      accessToken: session.access_token,
      registered,
      user: {
        id: session.user.id,
        email: session.user.email || "",
        avatarUrl: session.user.user_metadata?.avatar_url || "",
      },
    });

    if (!posted) {
      throw new Error("Could not communicate with extension");
    }

    setStatus("Connected. Returning to extension…");
    closePopupSoon();
  }

  function overrideHandleAuthSuccess() {
    window.handleAuthSuccess = async function () {
      try {
        setStatus("Auth successful. Finalising connection…");
        await sendSessionToExtension();
      } catch (error) {
        console.error("Extension auth success handler failed:", error);
        setStatus(
          error?.message || "Failed to complete connection. Please try again.",
          true
        );
      }
    };
  }

  if (reopenButton) {
    reopenButton.addEventListener("click", () => {
      if (typeof window.showAuthModal === "function") {
        window.showAuthModal();
        setStatus("Sign-in modal reopened.");
      }
    });
  }

  (async () => {
    try {
      // Process OAuth callback params/hash if returning from provider.
      await handleAuthCallback();

      const {
        data: { session },
      } = await supabaseClient.auth.getSession();
      if (session?.access_token) {
        setStatus("Existing session found. Connecting extension…");
        await sendSessionToExtension();
        return;
      }

      await loadAuthModal();
      await waitForAuthScript();
      if (typeof window.setupAuthHandlers === "function") {
        window.setupAuthHandlers();
      }
      overrideHandleAuthSuccess();
      if (typeof window.showLoginForm === "function") {
        window.showLoginForm();
      }
      if (typeof window.showAuthModal === "function") {
        window.showAuthModal();
      }
      setStatus("Sign in or create your account to connect this extension.");
    } catch (error) {
      console.error("Extension auth initialisation failed:", error);
      postAuthMessage({
        type: "error",
        message:
          error?.message || "Unable to start sign-in flow. Please try again.",
      });
      setStatus(
        error?.message || "Unable to start sign-in flow. Please try again.",
        true
      );
    }
  })();
}

// CAPTCHA success callback (global function)
window.onTurnstileSuccess = function (token) {
  captchaToken = token;
  captchaIssuedAt = Date.now();
  setSignupButtonEnabled(true);
  recordTurnstileEvent("token_received");

  if (awaitingCaptchaRefresh && pendingSignupSubmission) {
    awaitingCaptchaRefresh = false;
    executeEmailSignup();
  }
};

// Export functions for use by other modules
if (typeof module !== "undefined" && module.exports) {
  // Node.js environment
  module.exports = {
    initialiseSupabase,
    loadAuthModal,
    waitForAuthScript,
    handleAuthCallback,
    registerUserWithBackend,
    updateAuthState,
    updateUserInfo,
    getInitials,
    showAuthModal,
    closeAuthModal,
    showAuthForm,
    handleEmailLogin,
    handleEmailSignup,
    handlePasswordReset,
    handleSocialLogin,
    handlePendingDomain,
    showAuthLoading,
    hideAuthLoading,
    showAuthError,
    clearAuthError,
    setupPasswordStrength,
    setupAuthHandlers,
    setupAuthModalHandlers,
    setupLoginPageHandlers,
    handleLogout,
    isValidExtensionTargetOrigin,
    defaultHandleAuthSuccess,
    initAuthCallbackPage,
    initExtensionAuthPage,
    setUserAvatar,
    getGravatarUrl,
  };
} else {
  window.GNHAvatar = {
    getInitials,
    setUserAvatar,
    getGravatarUrl,
  };

  // Browser environment - make functions globally available
  window.GNHAuth = {
    initialiseSupabase,
    loadAuthModal,
    waitForAuthScript,
    handleAuthCallback,
    registerUserWithBackend,
    updateAuthState,
    updateUserInfo,
    getInitials,
    showAuthModal,
    closeAuthModal,
    showAuthForm,
    handleEmailLogin,
    handleEmailSignup,
    handlePasswordReset,
    handleSocialLogin,
    handlePendingDomain,
    showAuthLoading,
    hideAuthLoading,
    showAuthError,
    clearAuthError,
    setupPasswordStrength,
    setupAuthHandlers,
    setupAuthModalHandlers,
    setupLoginPageHandlers,
    handleLogout,
    isValidExtensionTargetOrigin,
    initAuthCallbackPage,
    initExtensionAuthPage,
    clearPendingInviteToken,
    setUserAvatar,
    getGravatarUrl,
  };

  // Also make individual functions available globally for backward compatibility
  window.initialiseSupabase = initialiseSupabase;
  window.loadAuthModal = loadAuthModal;
  window.waitForAuthScript = waitForAuthScript;
  window.handleAuthCallback = handleAuthCallback;
  window.registerUserWithBackend = registerUserWithBackend;
  window.updateAuthState = updateAuthState;
  window.updateUserInfo = updateUserInfo;
  window.getInitials = getInitials;
  window.setUserAvatar = setUserAvatar;
  window.getGravatarUrl = getGravatarUrl;
  window.showAuthModal = showAuthModal;
  window.closeAuthModal = closeAuthModal;
  window.showAuthForm = showAuthForm;
  window.handleEmailLogin = handleEmailLogin;
  window.handleEmailSignup = handleEmailSignup;
  window.handlePasswordReset = handlePasswordReset;
  window.handleSocialLogin = handleSocialLogin;
  window.handlePendingDomain = handlePendingDomain;
  window.showAuthLoading = showAuthLoading;
  window.hideAuthLoading = hideAuthLoading;
  window.showAuthError = showAuthError;
  window.clearAuthError = clearAuthError;
  window.setupPasswordStrength = setupPasswordStrength;
  window.setupAuthHandlers = setupAuthHandlers;
  window.setupAuthModalHandlers = setupAuthModalHandlers;
  window.setupLoginPageHandlers = setupLoginPageHandlers;
  window.handleLogout = handleLogout;
  window.isValidExtensionTargetOrigin = isValidExtensionTargetOrigin;
  window.handleAuthSuccess = defaultHandleAuthSuccess;
  window.initExtensionAuthPage = initExtensionAuthPage;
  window.initAuthCallbackPage = initAuthCallbackPage;
  window.clearPendingInviteToken = clearPendingInviteToken;

  // Convenience functions for common auth form actions
  window.showLoginForm = () => showAuthForm("login");
  window.showSignupForm = () => showAuthForm("signup");
  window.showResetForm = () => showAuthForm("reset");
}
