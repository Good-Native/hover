/**
 * pages/webflow-login.js — Webflow Designer extension auth screen
 *
 * Entrypoint for the extension-auth page served by the Go backend at
 * /extension-auth. Replaces the legacy core.js + gnh-auth-extension.js
 * global-script model for this surface.
 *
 * Loading contract (extension-auth.html):
 *   1. <script src="/config.js">          — sets window.GNH_CONFIG
 *   2. <script src="supabase.js">         — sets window.supabase (CDN UMD)
 *   3. <script type="module" src="/app/pages/webflow-login.js">
 *
 * No gnh-bootstrap.js. No GNH_APP.whenReady(). No GNHAuth globals required.
 *
 * Auth redirect contract (from AGENTS.md):
 *   - Deep-link URLs must return to the exact originating URL.
 *   - Extension auth uses window.GNH_APP.extensionAuth = true to signal
 *     that the OAuth redirect should return to this page, not /dashboard.
 *   - handleSocialLogin in auth.js reads window.GNH_APP.extensionAuth and
 *     sets redirectTo = window.location.href for extension flows.
 *   - This module sets that flag before any auth action.
 */

import { isConfigured, supabaseUrl, supabaseAnonKey } from "/app/lib/config.js";
import {
  getSession,
  onAuthStateChange,
  signOut,
} from "/app/lib/auth-session.js";
import { showToast } from "/app/components/hover-toast.js";

// ── Constants ──────────────────────────────────────────────────────────────────

/** postMessage target origin for the Webflow Designer extension. */
const EXTENSION_ORIGIN = "https://webflow.com";

/**
 * The state token passed by the extension when opening this popup.
 * Included in the postMessage payload so index.ts can verify it.
 * Passed as both ?state= and ?extension_state= in the URL.
 */
const EXTENSION_STATE =
  new URLSearchParams(window.location.search).get("extension_state") ||
  new URLSearchParams(window.location.search).get("state") ||
  "";

// ── Element references ─────────────────────────────────────────────────────────

/** @returns {HTMLElement|null} */
const el = (id) => document.getElementById(id);

// ── Init ───────────────────────────────────────────────────────────────────────

async function init() {
  // Signal to auth.js (legacy, still loaded via the modal) that this is an
  // extension auth flow so OAuth redirects return here, not to /dashboard.
  window.GNH_APP = Object.assign({}, window.GNH_APP || {}, {
    extensionAuth: true,
  });

  if (!isConfigured()) {
    setStatus("Configuration error — please reload.", "error");
    return;
  }

  // Initialise the Supabase client if auth.js hasn't done it yet.
  // auth.js.initialiseSupabase() is still the canonical initialiser;
  // we call it here as a safety net in case core.js is not present.
  if (window.supabase?.createClient && !window.supabase?.auth) {
    window.supabase = window.supabase.createClient(
      supabaseUrl,
      supabaseAnonKey
    );
  }

  setStatus("Preparing sign-in…");

  // Load the shared auth modal (legacy component — reused as-is for Phase 1).
  // Phase 2+ will replace this with a native module component.
  await loadAuthModal();

  // Wire the legacy auth.js form/social-login event handlers. They use
  // document-level delegation, so this only needs to happen once and works
  // regardless of when the modal HTML was injected. Without this, clicking
  // sign-in does nothing because no submit handler is bound.
  if (typeof window.setupAuthModalHandlers === "function") {
    window.setupAuthModalHandlers();
  }

  // Handle any OAuth callback tokens already in the URL/hash.
  await handleCallbackIfPresent();

  // Check existing session.
  const session = await getSession().catch(() => null);
  if (session) {
    await handleAuthenticated(session);
    return;
  }

  // No session — show the login modal.
  showLogin();

  // Listen for auth state changes (sign-in after modal interaction).
  const unsubscribe = onAuthStateChange(async (event, newSession) => {
    if ((event === "SIGNED_IN" || event === "TOKEN_REFRESHED") && newSession) {
      unsubscribe();
      await handleAuthenticated(newSession);
    }
  });

  // Wire up the reopen button.
  const reopenBtn = el("reopenModalBtn");
  if (reopenBtn) {
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

  // Register with backend (idempotent — 409 is success).
  if (session.user) {
    await registerUser(session.user).catch((err) => {
      console.warn(
        "webflow-login: backend registration failed (non-fatal)",
        err
      );
    });
  }

  // Send the session back to the extension.
  // Contract must match what index.ts connectAccount() expects:
  //   { source: "gnh-extension-auth", state, extensionState, type: "success",
  //     accessToken, user: { id, email, avatarUrl } }
  if (!window.opener) {
    // Opened directly (not by the extension popup flow) — nothing to notify.
    setStatus("Signed in — you can close this window.", "success");
    showToast("Signed in successfully.", { variant: "success", duration: 0 });
    return;
  }
  try {
    window.opener.postMessage(
      {
        source: "gnh-extension-auth",
        state: EXTENSION_STATE,
        extensionState: EXTENSION_STATE,
        type: "success",
        accessToken: session.access_token,
        user: {
          id: session.user?.id ?? "",
          email: session.user?.email ?? "",
          avatarUrl: session.user?.user_metadata?.avatar_url ?? "",
        },
      },
      EXTENSION_ORIGIN
    );

    setStatus("Signed in — you can close this window.", "success");
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
 * Handle OAuth callback tokens that may be present in the current URL.
 * Delegates to auth.js.handleAuthCallback if available, otherwise
 * handles the access_token hash directly.
 */
async function handleCallbackIfPresent() {
  if (typeof window.GNHAuth?.handleAuthCallback === "function") {
    await window.GNHAuth.handleAuthCallback().catch(() => {});
    return;
  }

  const hash = new URLSearchParams(window.location.hash.slice(1));
  const accessToken = hash.get("access_token");
  const refreshToken = hash.get("refresh_token");

  if (accessToken && window.supabase?.auth) {
    try {
      await window.supabase.auth.setSession({
        access_token: accessToken,
        refresh_token: refreshToken || "",
      });
      history.replaceState(null, "", window.location.pathname);
    } catch (err) {
      console.warn("webflow-login: failed to restore session from hash", err);
    }
  }
}

// ── Backend registration ───────────────────────────────────────────────────────

/**
 * @param {import("@supabase/supabase-js").User} user
 */
async function registerUser(user) {
  const session = await getSession();
  if (!session?.access_token) return;

  const metadata = user.user_metadata || {};
  const firstName =
    (metadata.given_name || metadata.first_name || "").trim() || null;
  const lastName =
    (metadata.family_name || metadata.last_name || "").trim() || null;
  const fullName =
    (
      metadata.full_name ||
      metadata.name ||
      [firstName, lastName].filter(Boolean).join(" ") ||
      ""
    ).trim() || null;

  const res = await fetch("/v1/auth/register", {
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

  if (!res.ok && res.status !== 409) {
    throw new Error(`Backend registration failed: ${res.status}`);
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

/** Load and inject the shared auth modal HTML, then show the login form. */
async function loadAuthModal() {
  const container = el("authModalContainer");
  if (!container) return;

  try {
    const res = await fetch("/auth-modal.html", { cache: "no-store" });
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
    // Verify content-type before injecting — guards against unexpected proxy responses.
    const ct = res.headers.get("content-type") || "";
    if (!ct.includes("text/html")) {
      throw new Error(`Unexpected content-type: ${ct}`);
    }
    const html = await res.text();
    // Parse via DOMParser and only insert non-script nodes to prevent XSS
    // in the unlikely event the response is tampered.
    const doc = new DOMParser().parseFromString(html, "text/html");
    doc.querySelectorAll("script").forEach((s) => s.remove());
    container.append(...doc.body.childNodes);
  } catch (err) {
    console.error("webflow-login: failed to load auth modal", err);
    setStatus("Sign-in modal failed to load — please refresh.", "error");
  }
}

/** Show the auth modal in login mode. */
function showLogin() {
  setStatus("Please sign in to continue.");
  if (typeof window.showAuthModal === "function") {
    window.showAuthModal();
  }
  if (typeof window.showLoginForm === "function") {
    window.showLoginForm();
  }
}

// ── Bootstrap ──────────────────────────────────────────────────────────────────

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
