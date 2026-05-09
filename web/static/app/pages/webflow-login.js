/**
 * pages/webflow-login.js — Webflow Designer extension auth screen
 *
 * Module-native auth flow for /extension-auth. This page no longer depends on
 * the legacy auth bundle; it owns its popup UI, OAuth redirect handling, and
 * postMessage contract directly through the app/ ES module layer.
 */

import { enableTurnstile, isConfigured } from "/app/lib/config.js";
import { ensureSupabaseClient } from "/app/lib/supabase-client.js";
import { showToast } from "/app/components/hover-toast.js";

// ── Constants ──────────────────────────────────────────────────────────────────

/**
 * The state token passed by the extension when opening this popup.
 * Included in the postMessage payload so index.ts can verify it.
 * Passed as both ?state= and ?extension_state= in the URL.
 */
const SEARCH_PARAMS = new URLSearchParams(window.location.search);
const AUTH_MODAL_PATH = "/auth-modal.html";
const OAUTH_CALLBACK_QUERY_KEYS = [
  "error",
  "error_code",
  "error_description",
  "sb",
  "code",
  "state",
  "return_to",
];
const TURNSTILE_SCRIPT_SRC =
  "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
const MAX_TURNSTILE_RETRIES = 2;
const POPUP_AUTH_TARGET_ORIGIN_STORAGE_KEY = "gnh_extension_auth_target_origin";
const POPUP_AUTH_STATE_STORAGE_KEY = "gnh_extension_auth_state";
const AUTH_RETURN_TO_STORAGE_KEY = "gnh_auth_return_to";
const REQUESTED_MODE_STORAGE_KEY = "gnh_auth_requested_mode";

/** @type {import("@supabase/supabase-js").SupabaseClient|null} */
let supabaseClient = null;
let currentForm = "login";
let captchaToken = null;
let captchaIssuedAt = null;
let turnstileWidgetId = null;
let turnstileLoadPromise = null;
let turnstileRetryCount = 0;
let awaitingCaptchaRefresh = false;
let pendingSignupSubmission = null;
let targetOrigin = getPopupContextValue(
  POPUP_AUTH_TARGET_ORIGIN_STORAGE_KEY,
  SEARCH_PARAMS.get("origin") || ""
);
let extensionState = getPopupContextValue(
  POPUP_AUTH_STATE_STORAGE_KEY,
  SEARCH_PARAMS.get("extension_state") || SEARCH_PARAMS.get("state") || ""
);
let returnTo = getPopupContextValue(
  AUTH_RETURN_TO_STORAGE_KEY,
  SEARCH_PARAMS.get("return_to") || ""
);
let requestedMode = getPopupContextValue(
  REQUESTED_MODE_STORAGE_KEY,
  SEARCH_PARAMS.get("mode") || "login"
);

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

// ── Element references ─────────────────────────────────────────────────────────

/** @returns {HTMLElement|null} */
const el = (id) => document.getElementById(id);

// ── Init ───────────────────────────────────────────────────────────────────────

async function init() {
  window.GNH_APP = Object.assign({}, window.GNH_APP || {}, {
    extensionAuth: true,
  });

  if (!isConfigured()) {
    setStatus("Configuration error — please reload.", "error");
    return;
  }

  if (window.opener && !validatePopupContract()) {
    return;
  }

  persistAuthContext();
  setStatus("Preparing sign-in…");

  try {
    supabaseClient = ensureSupabaseClient();
    await loadAuthInterface();
  } catch (error) {
    console.error("webflow-login: failed to initialise auth", error);
    setStatus("Sign-in could not be prepared — please refresh.", "error");
    return;
  }

  await handleCallbackIfPresent();

  const session = await getCurrentSession().catch(() => null);
  if (session) {
    await handleAuthenticated(session);
    return;
  }

  showInitialAuthForm();

  const reopenBtn = el("reopenModalBtn");
  if (reopenBtn) {
    reopenBtn.hidden = false;
    reopenBtn.addEventListener("click", showLogin);
  }
}

// ── Auth flow ──────────────────────────────────────────────────────────────────

/**
 * Handles a successfully authenticated session:
 * registers the user with the backend, then posts the session token
 * back to the Webflow Designer extension via postMessage.
 *
 * @param {import("@supabase/supabase-js").Session} session
 */
async function handleAuthenticated(session) {
  setStatus("Signing you in…");

  if (session.user) {
    await registerUserWithBackend(session.user).catch((err) => {
      console.warn(
        "webflow-login: backend registration failed (non-fatal)",
        err
      );
    });
  }

  if (!window.opener) {
    clearAuthContext();
    const destination = resolveReturnDestination();
    if (destination) {
      window.location.replace(destination);
      return;
    }

    setStatus("Signed in — you can close this window.", "success");
    showToast("Signed in successfully.", { variant: "success", duration: 0 });
    return;
  }
  try {
    window.opener.postMessage(
      {
        source: "gnh-extension-auth",
        state: extensionState,
        extensionState,
        type: "success",
        accessToken: session.access_token,
        user: {
          id: session.user?.id ?? "",
          email: session.user?.email ?? "",
          avatarUrl: getUserMetadataAvatarUrl(session.user),
        },
      },
      targetOrigin
    );

    setStatus("Signed in — you can close this window.", "success");
    clearAuthContext();
    showToast("Signed in successfully.", { variant: "success", duration: 0 });
  } catch (err) {
    console.error("webflow-login: postMessage failed", err);
    setStatus(
      "Signed in, but could not notify the extension. Please close and reopen it.",
      "warning"
    );
    showToast("Sign-in complete, but the extension was not notified.", {
      variant: "warning",
      duration: 0,
    });
  }
}

/**
 * Handles OAuth callback tokens already present in the popup URL.
 * Preserves the extension query parameters while cleaning provider callback
 * parameters out of the location bar after the session is restored.
 */
async function handleCallbackIfPresent() {
  const urlParams = new URLSearchParams(window.location.search);
  const error = urlParams.get("error");
  const errorDescription = urlParams.get("error_description");

  if (error) {
    console.error("webflow-login: OAuth error", error, errorDescription);
    cleanOAuthCallbackUrl();
    showAuthError("Authentication failed. Please try again.");
    setStatus("Authentication failed. Please try again.", "error");
    return;
  }

  const hash = new URLSearchParams(window.location.hash.slice(1));
  const accessToken = hash.get("access_token");
  const refreshToken = hash.get("refresh_token");

  if (accessToken && supabaseClient?.auth) {
    try {
      await supabaseClient.auth.setSession({
        access_token: accessToken,
        refresh_token: refreshToken || "",
      });
      cleanOAuthCallbackUrl();
    } catch (err) {
      console.warn("webflow-login: failed to restore session from hash", err);
      showAuthError("Could not restore your sign-in session. Please retry.");
      setStatus("Could not restore your sign-in session.", "error");
    }
    return;
  }

  if (hasOAuthCallbackQuery(urlParams)) {
    try {
      const session = await getCurrentSession();
      if (session) {
        cleanOAuthCallbackUrl();
      }
    } catch (error) {
      console.warn(
        "webflow-login: failed to restore session from query",
        error
      );
      showAuthError("Could not complete sign-in. Please retry.");
      setStatus("Could not complete sign-in. Please retry.", "error");
    }
  }
}

// ── Backend registration ───────────────────────────────────────────────────────

/**
 * @param {import("@supabase/supabase-js").User} user
 */
async function registerUserWithBackend(user) {
  if (!user?.id || !user.email) {
    throw new Error("Invalid user data for registration");
  }

  const session = await getCurrentSession();
  if (!session?.access_token) {
    throw new Error("No session available for registration");
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
      Authorization: `Bearer ${session.access_token}`,
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
    if (response.status === 409) {
      return;
    }
    throw new Error(
      `Backend registration failed: ${response.status} ${response.statusText}`
    );
  }
}

// ── UI helpers ─────────────────────────────────────────────────────────────────

/**
 * Update the status text element.
 * @param {string} message
 * @param {"info"|"success"|"error"|"warning"} [variant]
 */
function setStatus(message, variant = "info") {
  const statusEl = el("extensionAuthStatus");
  if (!statusEl) return;
  statusEl.textContent = message;
  statusEl.dataset.variant = variant;
}

async function loadAuthInterface() {
  const container = el("authModalContainer");
  if (!container) {
    throw new Error("Auth modal container missing");
  }

  const response = await fetch(AUTH_MODAL_PATH, { cache: "no-store" });
  if (!response.ok) {
    throw new Error(
      `Failed to load auth interface: ${response.status} ${response.statusText}`
    );
  }

  container.innerHTML = await response.text();
  container.querySelectorAll("[onclick]").forEach((node) => {
    node.removeAttribute("onclick");
  });

  bindAuthInterface();
  showForm("login");
}

function bindAuthInterface() {
  const container = el("authModalContainer");
  if (!container) return;

  const closeButton = container.querySelector(".gnh-modal-close");
  if (closeButton) {
    closeButton.addEventListener("click", () => {
      hideAuthModal();
      setStatus("Sign-in paused. Reopen the form to continue.");
    });
  }

  const loginLinks = container.querySelectorAll("#loginForm .gnh-auth-links a");
  if (loginLinks[0]) {
    loginLinks[0].dataset.authAction = "show-signup";
  }
  if (loginLinks[1]) {
    loginLinks[1].dataset.authAction = "show-reset";
  }

  const signupLink = container.querySelector("#signupForm .gnh-auth-links a");
  if (signupLink) {
    signupLink.dataset.authAction = "show-login";
  }

  const resetLink = container.querySelector("#resetForm .gnh-auth-links a");
  if (resetLink) {
    resetLink.dataset.authAction = "show-login";
  }

  container.addEventListener("submit", async (event) => {
    if (!(event.target instanceof HTMLFormElement)) {
      return;
    }

    if (event.target.id === "emailLoginForm") {
      await handleEmailLogin(event);
      return;
    }

    if (event.target.id === "emailSignupForm") {
      await handleEmailSignup(event);
      return;
    }

    if (event.target.id === "passwordResetForm") {
      await handlePasswordReset(event);
    }
  });

  container.addEventListener("click", async (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target) return;

    const socialButton = target.closest(".gnh-social-btn[data-provider]");
    if (socialButton instanceof HTMLElement) {
      event.preventDefault();
      const provider = socialButton.dataset.provider || "";
      await handleSocialLogin(provider);
      return;
    }

    const actionLink = target.closest("[data-auth-action]");
    if (actionLink instanceof HTMLElement) {
      event.preventDefault();
      const action = actionLink.dataset.authAction;
      if (action === "show-signup") {
        showForm("signup");
      } else if (action === "show-reset") {
        showForm("reset");
      } else if (action === "show-login") {
        showForm("login");
      }
      return;
    }

    const modal = target.closest("#authModal");
    if (target.id === "authModal" && modal) {
      hideAuthModal();
      setStatus("Sign-in paused. Reopen the form to continue.");
    }
  });

  if (!enableTurnstile) {
    setSignupButtonEnabled(true);
  }
}

function showAuthModal() {
  const modal = el("authModal");
  if (!modal) return;
  modal.classList.add("show");
}

function hideAuthModal() {
  const modal = el("authModal");
  if (!modal) return;
  modal.classList.remove("show");
}

function showForm(formType) {
  currentForm = formType;

  const titles = {
    login: "Sign In",
    signup: "Create Account",
    reset: "Reset Password",
  };

  ["login", "signup", "reset"].forEach((name) => {
    const panel = el(`${name}Form`);
    if (panel) {
      panel.style.display = name === formType ? "block" : "none";
    }
  });

  const titleEl = el("authModalTitle");
  if (titleEl) {
    titleEl.textContent = titles[formType] || "Authentication";
  }

  clearAuthError();
  hideAuthLoading();
  showAuthModal();

  if (formType === "signup") {
    captchaToken = null;
    if (enableTurnstile) {
      setSignupButtonEnabled(false);
      void ensureTurnstileWidget().catch((error) => {
        console.warn("webflow-login: failed to initialise turnstile", error);
        showAuthError(
          "Security verification could not be loaded. Please try another sign-in method."
        );
      });
    } else {
      setSignupButtonEnabled(true);
    }
  }
}

function showAuthLoading() {
  const loadingEl = el("authLoading");
  if (loadingEl) {
    loadingEl.style.display = "block";
  }
  const activeForm = el(`${currentForm}Form`);
  if (activeForm) {
    activeForm.style.display = "none";
  }
}

function hideAuthLoading() {
  const loadingEl = el("authLoading");
  if (loadingEl) {
    loadingEl.style.display = "none";
  }
  const activeForm = el(`${currentForm}Form`);
  if (activeForm) {
    activeForm.style.display = "block";
  }
}

function showAuthError(message, type = "error") {
  const errorEl = el("authError");
  if (!errorEl) return;

  errorEl.textContent = message;
  errorEl.style.display = "block";

  if (type === "success") {
    errorEl.style.background = "#dcfce7";
    errorEl.style.color = "#16a34a";
    errorEl.style.borderColor = "#bbf7d0";
    return;
  }

  errorEl.style.background = "#fee2e2";
  errorEl.style.color = "#dc2626";
  errorEl.style.borderColor = "#fecaca";
}

function clearAuthError() {
  const errorEl = el("authError");
  if (!errorEl) return;
  errorEl.style.display = "none";
}

function setSignupButtonEnabled(enabled) {
  const signupBtn = /** @type {HTMLButtonElement|null} */ (
    el("signupSubmitBtn")
  );
  if (signupBtn) {
    signupBtn.disabled = !enabled;
  }
}

/** Show the auth modal in login mode. */
function showLogin() {
  setStatus("Please sign in to continue.");
  showForm("login");
}

function validatePopupContract() {
  if (!targetOrigin || !extensionState) {
    setStatus("Missing extension context. Please reopen sign-in.", "error");
    return false;
  }

  if (!window.opener || window.opener.closed) {
    setStatus(
      "This login window must be opened from the Webflow extension.",
      "error"
    );
    return false;
  }

  if (targetOrigin && !isValidExtensionTargetOrigin(targetOrigin)) {
    setStatus("Invalid extension origin. Please reopen sign-in.", "error");
    return false;
  }

  try {
    const referrerOrigin = document.referrer
      ? new URL(document.referrer).origin
      : "";
    if (referrerOrigin && targetOrigin && referrerOrigin !== targetOrigin) {
      setStatus(
        "Origin mismatch. Please relaunch from the extension.",
        "error"
      );
      return false;
    }
  } catch (_error) {
    setStatus("Unable to validate opener origin. Please relaunch.", "error");
    return false;
  }

  return true;
}

function isValidExtensionTargetOrigin(rawOrigin) {
  if (!rawOrigin) return false;

  try {
    const parsed = new URL(rawOrigin);
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
      return false;
    }

    const host = parsed.hostname.toLowerCase();
    if (
      host === "localhost" ||
      host === "127.0.0.1" ||
      host.endsWith(".webflow-ext.com")
    ) {
      return true;
    }

    return host.endsWith(".fly.dev");
  } catch (_error) {
    return false;
  }
}

function getPopupContextValue(storageKey, fallbackValue) {
  if (fallbackValue) {
    return fallbackValue;
  }

  try {
    return window.sessionStorage.getItem(storageKey) || "";
  } catch (_error) {
    return "";
  }
}

function persistAuthContext() {
  try {
    if (targetOrigin) {
      window.sessionStorage.setItem(
        POPUP_AUTH_TARGET_ORIGIN_STORAGE_KEY,
        targetOrigin
      );
    }
    if (extensionState) {
      window.sessionStorage.setItem(
        POPUP_AUTH_STATE_STORAGE_KEY,
        extensionState
      );
    }
    if (returnTo) {
      window.sessionStorage.setItem(AUTH_RETURN_TO_STORAGE_KEY, returnTo);
    }
    if (requestedMode) {
      window.sessionStorage.setItem(REQUESTED_MODE_STORAGE_KEY, requestedMode);
    }
  } catch (_error) {
    // Ignore storage failures.
  }
}

function clearAuthContext() {
  try {
    window.sessionStorage.removeItem(POPUP_AUTH_TARGET_ORIGIN_STORAGE_KEY);
    window.sessionStorage.removeItem(POPUP_AUTH_STATE_STORAGE_KEY);
    window.sessionStorage.removeItem(AUTH_RETURN_TO_STORAGE_KEY);
    window.sessionStorage.removeItem(REQUESTED_MODE_STORAGE_KEY);
  } catch (_error) {
    // Ignore storage failures.
  }
}

function resolveRequestedMode() {
  const mode = String(requestedMode || "login")
    .trim()
    .toLowerCase();
  if (mode === "signup" || mode === "reset") {
    return mode;
  }
  return "login";
}

function showInitialAuthForm() {
  const mode = resolveRequestedMode();
  if (mode === "signup") {
    setStatus("Create your account to continue.");
    showForm("signup");
    return;
  }

  if (mode === "reset") {
    setStatus("Reset your password to continue.");
    showForm("reset");
    return;
  }

  showLogin();
}

function resolveReturnDestination() {
  if (!returnTo) {
    return "";
  }

  try {
    const destination = new URL(returnTo, window.location.origin);
    if (destination.origin !== window.location.origin) {
      console.warn("webflow-login: ignoring cross-origin return_to", returnTo);
      return "";
    }
    return destination.toString();
  } catch (error) {
    console.warn("webflow-login: invalid return_to", error);
    return "";
  }
}

function hasOAuthCallbackQuery(urlParams) {
  return (
    urlParams.has("code") ||
    urlParams.has("state") ||
    urlParams.has("error") ||
    urlParams.has("error_code")
  );
}

function cleanOAuthCallbackUrl() {
  const url = new URL(window.location.href);
  url.hash = "";
  OAUTH_CALLBACK_QUERY_KEYS.forEach((key) => {
    url.searchParams.delete(key);
  });
  window.history.replaceState(null, "", `${url.pathname}${url.search}`);
}

async function getCurrentSession() {
  if (!supabaseClient?.auth) {
    throw new Error("Supabase client is not initialised");
  }

  const {
    data: { session },
    error,
  } = await supabaseClient.auth.getSession();

  if (error) {
    throw error;
  }

  return session;
}

function composeDisplayName(firstName, lastName) {
  return [firstName, lastName]
    .map((value) => (typeof value === "string" ? value.trim() : ""))
    .filter(Boolean)
    .join(" ");
}

async function handleEmailLogin(event) {
  event.preventDefault();
  const formData = new FormData(event.target);

  showAuthLoading();
  clearAuthError();

  try {
    const { data, error } = await supabaseClient.auth.signInWithPassword({
      email: String(formData.get("email") || ""),
      password: String(formData.get("password") || ""),
    });

    if (error) throw error;

    const session = data.session || (await getCurrentSession());
    if (!session) {
      throw new Error("Sign-in succeeded, but no session was returned.");
    }

    await handleAuthenticated(session);
  } catch (error) {
    console.error("webflow-login: email login failed", error);
    showAuthError(
      error?.message || "Login failed. Please check your credentials."
    );
    setStatus("Could not sign you in. Please retry.", "error");
  } finally {
    hideAuthLoading();
  }
}

async function handleEmailSignup(event) {
  event.preventDefault();
  const formData = new FormData(event.target);

  pendingSignupSubmission = {
    email: String(formData.get("email") || ""),
    firstName: String(formData.get("firstName") || "").trim(),
    lastName: String(formData.get("lastName") || "").trim(),
    password: String(formData.get("password") || ""),
    passwordConfirm: String(formData.get("passwordConfirm") || ""),
  };
  turnstileRetryCount = 0;
  awaitingCaptchaRefresh = false;

  await executeEmailSignup();
}

async function executeEmailSignup() {
  if (!pendingSignupSubmission) return;

  const { email, firstName, lastName, password, passwordConfirm } =
    pendingSignupSubmission;

  if (password !== passwordConfirm) {
    showAuthError("Passwords do not match.");
    return;
  }

  if (password.length < 8) {
    showAuthError("Password must be at least 8 characters long.");
    return;
  }

  if (enableTurnstile && !captchaToken) {
    showAuthError("Please complete the security verification.");
    return;
  }

  showAuthLoading();
  clearAuthError();

  try {
    const signupOptions = {
      emailRedirectTo: window.location.href.split("#")[0],
      data: {
        first_name: firstName,
        last_name: lastName,
        given_name: firstName,
        family_name: lastName,
        full_name: composeDisplayName(firstName, lastName),
        name: composeDisplayName(firstName, lastName),
      },
    };

    if (enableTurnstile && captchaToken) {
      signupOptions.captchaToken = captchaToken;
    }

    const { data, error } = await supabaseClient.auth.signUp({
      email,
      password,
      options: signupOptions,
    });

    if (error) throw error;

    pendingSignupSubmission = null;
    awaitingCaptchaRefresh = false;
    turnstileRetryCount = 0;
    resetTurnstileWidget();

    if (data.user && !data.user.email_confirmed_at) {
      showAuthError(
        "Please check your email and click the confirmation link before signing in.",
        "success"
      );
      setStatus("Check your email to confirm your account.", "success");
      showForm("login");
      return;
    }

    const session = data.session || (await getCurrentSession());
    if (!session) {
      throw new Error("Account created, but no session was returned.");
    }

    await handleAuthenticated(session);
  } catch (error) {
    console.error("webflow-login: signup failed", error);

    const canRetryCaptcha = Boolean(
      enableTurnstile && turnstileWidgetId !== null
    );
    const shouldRetryCaptcha =
      canRetryCaptcha &&
      turnstileRetryCount < MAX_TURNSTILE_RETRIES &&
      /captcha|turnstile|106010/i.test(
        `${error?.message || ""} ${error?.error_description || ""}`
      );

    if (shouldRetryCaptcha) {
      turnstileRetryCount += 1;
      awaitingCaptchaRefresh = true;
      showAuthError(
        "Security check expired. Please complete the verification again."
      );
      hideAuthLoading();
      resetTurnstileWidget();
      return;
    }

    pendingSignupSubmission = null;
    awaitingCaptchaRefresh = false;
    showAuthError(error?.message || "Signup failed. Please try again.");
    setStatus("Could not create your account. Please retry.", "error");
  } finally {
    if (!awaitingCaptchaRefresh) {
      hideAuthLoading();
    }
  }
}

async function handlePasswordReset(event) {
  event.preventDefault();
  const formData = new FormData(event.target);

  showAuthLoading();
  clearAuthError();

  try {
    const { error } = await supabaseClient.auth.resetPasswordForEmail(
      String(formData.get("email") || ""),
      {
        redirectTo: `${window.location.origin}/dashboard`,
      }
    );

    if (error) throw error;

    showAuthError("Password reset email sent. Check your inbox.", "success");
    setStatus("Password reset email sent.", "success");
    window.setTimeout(() => {
      showForm("login");
    }, 1500);
  } catch (error) {
    console.error("webflow-login: password reset failed", error);
    showAuthError(error?.message || "Failed to send reset email.");
    setStatus("Password reset failed. Please retry.", "error");
  } finally {
    hideAuthLoading();
  }
}

async function handleSocialLogin(provider) {
  if (!provider) return;

  showAuthLoading();
  clearAuthError();

  try {
    persistAuthContext();

    const { data, error } = await supabaseClient.auth.signInWithOAuth({
      provider,
      options: {
        redirectTo: `${window.location.origin}/extension-auth.html`,
      },
    });

    if (error) throw error;
    if (data?.url) {
      window.location.assign(data.url);
    }
  } catch (error) {
    console.error("webflow-login: social login failed", error);
    showAuthError(error?.message || `${provider} login failed.`);
    setStatus("Could not start social sign-in. Please retry.", "error");
    hideAuthLoading();
  }
}

async function ensureTurnstileWidget() {
  const widget = document.querySelector(".cf-turnstile");
  if (!widget) {
    return;
  }

  if (!enableTurnstile) {
    setSignupButtonEnabled(true);
    return;
  }

  await ensureTurnstileLoaded();

  if (turnstileWidgetId !== null || !window.turnstile) {
    return;
  }

  turnstileWidgetId = window.turnstile.render(widget, {
    sitekey: widget.dataset.sitekey,
    callback: handleTurnstileSuccess,
  });
}

function ensureTurnstileLoaded() {
  if (!enableTurnstile) {
    return Promise.resolve();
  }

  if (window.turnstile) {
    return Promise.resolve();
  }

  if (turnstileLoadPromise) {
    return turnstileLoadPromise;
  }

  turnstileLoadPromise = new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = TURNSTILE_SCRIPT_SRC;
    script.async = true;
    script.defer = true;
    script.crossOrigin = "anonymous";
    script.onload = () => resolve();
    script.onerror = () =>
      reject(new Error("Turnstile script failed to load."));
    document.head.appendChild(script);
  });

  return turnstileLoadPromise;
}

function handleTurnstileSuccess(token) {
  captchaToken = token;
  captchaIssuedAt = Date.now();
  setSignupButtonEnabled(true);

  if (awaitingCaptchaRefresh && pendingSignupSubmission) {
    awaitingCaptchaRefresh = false;
    void executeEmailSignup();
  }
}

function resetTurnstileWidget() {
  captchaToken = null;
  captchaIssuedAt = null;
  setSignupButtonEnabled(!enableTurnstile);

  if (!enableTurnstile || !window.turnstile || turnstileWidgetId === null) {
    return;
  }

  try {
    window.turnstile.reset(turnstileWidgetId);
  } catch (error) {
    console.debug("webflow-login: turnstile reset skipped", {
      error: error?.message,
      ageMs: captchaIssuedAt ? Date.now() - captchaIssuedAt : null,
    });
  }
}

// ── Bootstrap ──────────────────────────────────────────────────────────────────

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
