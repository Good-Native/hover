/**
 * lib/settings/account.js — account section logic
 *
 * Handles user profile (name, email), auth method management (connect/remove
 * OAuth providers), and password reset. Surface-agnostic: render functions
 * accept a container element so they work in settings.html or any future
 * surface (e.g. extension panel).
 *
 * Usage:
 *   import { loadAccountDetails, setupAccountActions } from "/app/lib/settings/account.js";
 *
 *   await loadAccountDetails(document.getElementById("account"));
 */

import { get, patch } from "/app/lib/api-client.js";
import { getSession } from "/app/lib/auth-session.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";

const MAX_NAME_LENGTH = 80;

/** Adapter: gnh-settings uses (variant, message); hover-toast uses (message, {variant}). */
function toast(variant, message) {
  _showToast(message, { variant });
}

// ── Auth method definitions ────────────────────────────────────────────────────

const AUTH_METHOD_DEFS = [
  {
    key: "google",
    label: "Google",
    icon_url: "/assets/auth-providers/google.svg",
    supported: true,
  },
  {
    key: "github",
    label: "GitHub",
    icon_url: "/assets/auth-providers/github.svg",
    supported: true,
  },
  {
    key: "email",
    label: "Email/Password",
    icon_url: "",
    supported: true,
  },
  {
    key: "azure",
    label: "Microsoft",
    icon_url: "/assets/auth-providers/microsoft.svg",
    supported: true,
  },
  {
    key: "facebook",
    label: "Facebook",
    icon_url: "/assets/auth-providers/facebook.png",
    supported: true,
  },
  {
    key: "slack_oidc",
    label: "Slack",
    icon_url: "/assets/auth-providers/slack.svg",
    supported: true,
  },
];

// ── Helpers ────────────────────────────────────────────────────────────────────

function normaliseAuthProvider(provider) {
  const value = (provider || "").trim().toLowerCase();
  if (value === "slack") return "slack_oidc";
  if (
    value === "google" ||
    value === "github" ||
    value === "email" ||
    value === "azure" ||
    value === "facebook" ||
    value === "slack_oidc"
  ) {
    return value;
  }
  return "";
}

function getAuthMethodDef(provider) {
  return (
    AUTH_METHOD_DEFS.find((m) => m.key === provider) || {
      key: provider,
      label: provider || "Unknown",
      icon_url: "",
      supported: true,
    }
  );
}

function formatAuthMethod(method) {
  const value = normaliseAuthProvider(method) || method;
  return getAuthMethodDef(value).label;
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

function providerIconHtml(provider) {
  const def = getAuthMethodDef(provider);
  if (def.icon_url) {
    const img = document.createElement("img");
    img.src = resolveAssetUrl(def.icon_url);
    img.alt = "";
    img.loading = "lazy";
    img.decoding = "async";
    img.referrerPolicy = "no-referrer";
    return img;
  }
  const span = document.createElement("span");
  span.className = "settings-auth-fallback-icon";
  span.setAttribute("aria-hidden", "true");
  span.textContent = "\u2022";
  return span;
}

function providerSubtitle(method) {
  if (method.connected) {
    return method.email || "Connected";
  }
  if (method.provider === "email") {
    return "Set a password to enable email sign-in";
  }
  return "Not connected";
}

function splitName(fullName) {
  const value = (fullName || "").trim();
  if (!value) return { firstName: "", lastName: "" };
  const parts = value.split(/\s+/).filter(Boolean);
  if (parts.length === 0) return { firstName: "", lastName: "" };
  if (parts.length === 1) return { firstName: parts[0], lastName: "" };
  return { firstName: parts[0], lastName: parts.slice(1).join(" ") };
}

function getOAuthQueryParams(provider) {
  switch (provider) {
    case "google":
      return { prompt: "select_account consent" };
    case "azure":
      return { prompt: "select_account" };
    case "facebook":
      return { auth_type: "reauthenticate" };
    case "slack_oidc":
      return { prompt: "consent" };
    default:
      return {};
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
      // fall through to current origin
    }
  }
  return window.location.origin;
}

function getSupabaseClient() {
  return window.HOVER_EXTENSION_SUPABASE_CLIENT?.auth
    ? window.HOVER_EXTENSION_SUPABASE_CLIENT
    : window.supabase?.auth
      ? window.supabase
      : null;
}

// ── Auth method actions ────────────────────────────────────────────────────────

/**
 * Connect an OAuth provider. Preserves the redirect contract:
 * stores return path in sessionStorage, redirects via Supabase linkIdentity.
 */
export async function connectAuthMethod(provider) {
  const client = getSupabaseClient();
  if (!client?.auth) return;

  try {
    if (provider === "email") {
      await sendPasswordReset();
      toast(
        "success",
        "Password setup email sent. This enables email sign-in."
      );
      return;
    }

    const currentPath = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    if (currentPath && currentPath !== "/") {
      try {
        window.sessionStorage.setItem(
          "gnh_post_auth_return_target",
          currentPath
        );
      } catch {
        // Ignore storage failures and continue OAuth flow.
      }
    }

    const callbackTarget = new URL(`${getAppOrigin()}/auth/callback`);
    if (currentPath && currentPath !== "/") {
      callbackTarget.searchParams.set("return_to", currentPath);
    }
    const callbackUrl = callbackTarget.toString();
    const queryParams = getOAuthQueryParams(provider);

    if (typeof client.auth.linkIdentity === "function") {
      const { data, error } = await client.auth.linkIdentity({
        provider,
        options: { redirectTo: callbackUrl, queryParams },
      });
      if (error) throw error;
      if (data?.url) {
        window.location.assign(data.url);
        return;
      }
    } else {
      const { data, error } = await client.auth.signInWithOAuth({
        provider,
        options: { redirectTo: callbackUrl, queryParams },
      });
      if (error) throw error;
      if (data?.url) {
        window.location.assign(data.url);
        return;
      }
    }

    toast("success", `${formatAuthMethod(provider)} connected`);
  } catch (err) {
    console.error(`Failed to connect ${provider}:`, err);
    toast(
      "error",
      err?.message || `Failed to connect ${formatAuthMethod(provider)}`
    );
  }
}

async function unlinkIdentityViaApi(identityId) {
  const session = await getSession();
  const accessToken = session?.access_token;
  const authUrl = window.GNH_CONFIG?.supabaseUrl;
  const anonKey = window.GNH_CONFIG?.supabaseAnonKey;
  if (!accessToken || !authUrl || !anonKey) {
    throw new Error("Missing auth session details");
  }

  const response = await fetch(
    `${authUrl}/auth/v1/user/identities/${encodeURIComponent(identityId)}`,
    {
      method: "DELETE",
      headers: {
        Authorization: `Bearer ${accessToken}`,
        apikey: anonKey,
        "Content-Type": "application/json",
      },
    }
  );

  if (!response.ok) {
    const responseJson = await response.json().catch(() => ({}));
    throw new Error(
      responseJson?.msg || responseJson?.error || "Unlink failed"
    );
  }
}

export async function removeAuthMethod(method, connectedCount) {
  const client = getSupabaseClient();
  if (connectedCount <= 1) {
    toast("error", "You must keep at least one sign-in method.");
    return;
  }
  if (method.provider === "email") {
    toast("warning", "Email/password removal isn't supported in settings yet.");
    return;
  }
  if (!method.identity?.identity_id) {
    toast("error", "Unable to remove this method.");
    return;
  }
  if (!confirm(`Remove ${formatAuthMethod(method.provider)} sign-in?`)) {
    return;
  }

  try {
    if (typeof client?.auth?.unlinkIdentity === "function") {
      const { error } = await client.auth.unlinkIdentity(method.identity);
      if (error) throw error;
    } else {
      await unlinkIdentityViaApi(method.identity.identity_id);
    }

    try {
      await client?.auth?.refreshSession?.();
    } catch (err) {
      console.warn("Failed to refresh session after unlink:", err);
    }

    toast("success", `${formatAuthMethod(method.provider)} removed`);
  } catch (err) {
    console.error(`Failed to remove ${method.provider}:`, err);
    toast(
      "error",
      err?.message || `Failed to remove ${formatAuthMethod(method.provider)}`
    );
  }
}

// ── Rendering ──────────────────────────────────────────────────────────────────

/**
 * Render auth method cards into a container element.
 * @param {HTMLElement} container — element to render cards into (cleared first)
 * @param {object[]} methods — auth method models
 * @param {object} [options]
 * @param {function} [options.onRefresh] — called after a connect/remove action
 */
export function renderAuthMethods(container, methods, options = {}) {
  if (!container || !Array.isArray(methods)) return;

  container.replaceChildren();
  const connectedCount = methods.filter((m) => m.connected).length;
  const visibleMethods = methods.filter((m) => m.provider !== "email");

  visibleMethods.forEach((method) => {
    const card = document.createElement("div");
    card.className = "settings-auth-method-card";

    const details = document.createElement("div");
    details.className = "settings-auth-method-details";

    const icon = document.createElement("span");
    icon.className = `settings-auth-provider-icon settings-auth-provider-${method.provider}`;
    icon.appendChild(providerIconHtml(method.provider));

    const text = document.createElement("div");
    text.className = "settings-auth-method-text";

    const name = document.createElement("strong");
    name.textContent = formatAuthMethod(method.provider);

    const subtitle = document.createElement("span");
    subtitle.className = "settings-muted";
    subtitle.textContent = providerSubtitle(method);

    text.appendChild(name);
    text.appendChild(subtitle);
    details.appendChild(icon);
    details.appendChild(text);

    const actionBtn = document.createElement("button");
    actionBtn.className = "gnh-button gnh-button-outline settings-btn-sm";
    actionBtn.type = "button";
    actionBtn.textContent = method.connected ? "Remove" : "Connect";

    const permanentlyDisabled =
      method.connected && (connectedCount <= 1 || method.provider === "email");

    if (permanentlyDisabled) {
      actionBtn.disabled = true;
      actionBtn.title = "At least one sign-in method must remain";
    }

    actionBtn.addEventListener("click", async () => {
      if (permanentlyDisabled) return;
      actionBtn.disabled = true;
      const originalText = actionBtn.textContent;
      actionBtn.textContent = method.connected
        ? "Removing..."
        : "Connecting...";
      if (method.connected) {
        await removeAuthMethod(method, connectedCount);
      } else {
        await connectAuthMethod(method.provider);
      }
      actionBtn.textContent = originalText;
      actionBtn.disabled = permanentlyDisabled;
      if (options.onRefresh) options.onRefresh();
    });

    card.appendChild(details);
    card.appendChild(actionBtn);
    container.appendChild(card);
  });
}

// ── Data loading ───────────────────────────────────────────────────────────────

/**
 * Load and render account details into a container.
 * @param {HTMLElement} container — the account section element
 * @returns {Promise<void>}
 */
export async function loadAccountDetails(container) {
  const client = getSupabaseClient();
  const session = await getSession().catch(() => null);
  const sessionUser = session?.user || null;

  const fallbackEmail = sessionUser?.email || "";
  const fallbackFirstName =
    sessionUser?.user_metadata?.given_name ||
    sessionUser?.user_metadata?.first_name ||
    "";
  const fallbackLastName =
    sessionUser?.user_metadata?.family_name ||
    sessionUser?.user_metadata?.last_name ||
    "";
  const fallbackFullName =
    sessionUser?.user_metadata?.full_name ||
    sessionUser?.user_metadata?.name ||
    "";
  const fallbackMethods = sessionUser?.app_metadata?.providers || [];

  let email = fallbackEmail;
  let firstName = fallbackFirstName;
  let lastName = fallbackLastName;
  let fullName = fallbackFullName;
  let methods = fallbackMethods;
  let identities = [];
  let authUser = sessionUser;

  try {
    // Use supabase.auth.getUser() directly for fresh identity data
    // (auth-session.getUser() returns session.user which may be stale).
    const userResult = await client?.auth?.getUser();
    authUser = userResult?.data?.user || sessionUser;
    identities = Array.isArray(authUser?.identities) ? authUser.identities : [];
  } catch (err) {
    console.warn("Failed to load auth identities:", err);
  }

  try {
    const response = await get("/v1/auth/profile");
    const profileUser = response?.user || {};
    if (profileUser.email) email = profileUser.email;
    if (Object.hasOwn(profileUser, "full_name")) {
      fullName = (profileUser.full_name || "").trim();
    }
    if (Object.hasOwn(profileUser, "first_name")) {
      firstName = (profileUser.first_name || "").trim();
    }
    if (Object.hasOwn(profileUser, "last_name")) {
      lastName = (profileUser.last_name || "").trim();
    }
    if (Array.isArray(response?.auth_methods)) {
      methods = response.auth_methods;
    }
  } catch {
    console.warn("Failed to load profile from API.");
  }

  if (!firstName && !lastName && fullName) {
    const split = splitName(fullName);
    firstName = split.firstName;
    lastName = split.lastName;
  }

  // Update module state
  const connectedProviders = new Set();
  const hasIdentityData = Array.isArray(identities) && identities.length > 0;
  if (hasIdentityData) {
    identities.forEach((identity) => {
      const normalised = normaliseAuthProvider(identity.provider);
      if (normalised) connectedProviders.add(normalised);
    });
  } else {
    (Array.isArray(methods) ? methods : []).forEach((provider) => {
      const normalised = normaliseAuthProvider(provider);
      if (normalised) connectedProviders.add(normalised);
    });
  }

  // Build method models for rendering
  const methodModels = AUTH_METHOD_DEFS.map((def) => {
    const identity = identities.find(
      (c) => normaliseAuthProvider(c.provider) === def.key
    );
    return {
      provider: def.key,
      supported: true,
      connected: connectedProviders.has(def.key),
      email:
        identity?.identity_data?.email ||
        identity?.email ||
        authUser?.email ||
        email,
      identity: identity || null,
    };
  });

  // Render into container (or fall back to document for legacy compat)
  const root = container || document;
  const emailEl = root.querySelector("#settingsUserEmail");
  const firstNameEl = root.querySelector("#settingsUserFirstNameInput");
  const lastNameEl = root.querySelector("#settingsUserLastNameInput");
  const passwordStatusEl = root.querySelector("#settingsPasswordMethodStatus");
  const authMethodsEl = root.querySelector("#settingsAuthMethods");

  if (emailEl) emailEl.textContent = email || "Not set";
  if (firstNameEl) firstNameEl.value = firstName || "";
  if (lastNameEl) lastNameEl.value = lastName || "";

  if (passwordStatusEl) {
    const emailMethod = methodModels.find((m) => m.provider === "email");
    passwordStatusEl.textContent = emailMethod?.connected
      ? "Email/password sign-in enabled. Use reset email to change your password."
      : "Email/password sign-in not connected yet. Send reset email to set it up.";
  }

  if (authMethodsEl) {
    renderAuthMethods(authMethodsEl, methodModels, {
      onRefresh: () => loadAccountDetails(container),
    });
  }
}

// ── Profile actions ────────────────────────────────────────────────────────────

/**
 * Save profile name from form inputs within a container.
 * @param {HTMLElement} container — the account section element
 * @param {object} [options]
 * @param {function} [options.onSaved] — called after successful save
 */
export async function saveProfileName(container, options = {}) {
  const root = container || document;
  const client = getSupabaseClient();
  const firstNameEl = root.querySelector("#settingsUserFirstNameInput");
  const lastNameEl = root.querySelector("#settingsUserLastNameInput");
  const saveBtn = root.querySelector("#settingsSaveName");
  if (!firstNameEl || !lastNameEl || !saveBtn) return;

  const firstName = firstNameEl.value.trim();
  const lastName = lastNameEl.value.trim();
  const fullName = `${firstName} ${lastName}`.trim();

  if (firstName.length > MAX_NAME_LENGTH) {
    toast("error", `First name must be ${MAX_NAME_LENGTH} characters or fewer`);
    return;
  }
  if (lastName.length > MAX_NAME_LENGTH) {
    toast("error", `Last name must be ${MAX_NAME_LENGTH} characters or fewer`);
    return;
  }

  saveBtn.disabled = true;
  const originalText = saveBtn.textContent;
  saveBtn.textContent = "Saving...";

  try {
    let metadataUpdateSucceeded = true;
    try {
      await client?.auth?.updateUser({
        data: {
          first_name: firstName || "",
          last_name: lastName || "",
          given_name: firstName || "",
          family_name: lastName || "",
          full_name: fullName || "",
          name: fullName || "",
        },
      });
    } catch {
      console.warn("Failed to update auth metadata name.");
      metadataUpdateSucceeded = false;
    }

    await patch("/v1/auth/profile", {
      first_name: firstName,
      last_name: lastName,
      full_name: fullName,
    });

    if (metadataUpdateSucceeded) {
      toast("success", "Name updated");
    } else {
      toast(
        "warning",
        "Name saved, but auth metadata sync failed. Please re-login if needed."
      );
    }

    await loadAccountDetails(container);
    if (options.onSaved) options.onSaved();
  } catch {
    console.error("Failed to save profile name.");
    toast("error", "Failed to update name");
  } finally {
    saveBtn.disabled = false;
    saveBtn.textContent = originalText || "Save name";
  }
}

/**
 * Send password reset email for the current user.
 */
export async function sendPasswordReset() {
  const client = getSupabaseClient();
  const session = await getSession().catch(() => null);
  let email = session?.user?.email || "";
  if (!email) {
    try {
      const response = await get("/v1/auth/profile");
      email = response?.user?.email || "";
    } catch {
      // Fall back to the session email only.
    }
  }
  if (!email) {
    toast("error", "Email address not available");
    return;
  }

  try {
    const { error } = await client?.auth?.resetPasswordForEmail(email, {
      redirectTo: `${getAppOrigin()}/settings/account#security`,
    });
    if (error) throw error;
    toast("success", "Password reset email sent");
  } catch (err) {
    console.error("Failed to send password reset:", err);
    toast("error", "Failed to send password reset email");
  }
}

/**
 * Wire up account section event listeners within a container.
 * @param {HTMLElement} container — the account section element
 * @param {object} [options]
 * @param {function} [options.onNameSaved] — called after name save (e.g. refresh members)
 */
export function setupAccountActions(container, options = {}) {
  const root = container || document;

  const saveNameBtn = root.querySelector("#settingsSaveName");
  if (saveNameBtn) {
    saveNameBtn.addEventListener("click", () =>
      saveProfileName(container, { onSaved: options.onNameSaved })
    );
  }

  const resetBtn = root.querySelector("#settingsResetPassword");
  if (resetBtn) {
    resetBtn.addEventListener("click", sendPasswordReset);
  }
}
