/**
 * lib/auth-session.js — auth and session helpers
 *
 * Thin wrappers over window.supabase.auth for use in new ES module code.
 * New modules import from here rather than calling window.supabase.auth
 * or window.GNHAuth directly.
 *
 * Prerequisites:
 *   The Supabase SDK must be loaded as a plain <script> tag and
 *   initialised before any function here is called. Load order:
 *
 *     <script src="/config.js"></script>
 *     <script src="https://unpkg.com/@supabase/supabase-js@2/..."></script>
 *     <script>
 *       // initialise window.supabase here or via existing core.js
 *     </script>
 *     <script type="module" src="/app/pages/example.js"></script>
 *
 *   This is the Phase 0 bridging approach. A future phase will switch
 *   to importing Supabase directly as an ESM dependency.
 *
 * Transition note:
 *   window.GNHAuth is intentionally not referenced here. New code should
 *   use these helpers. Old pages continue to use window.GNHAuth until
 *   they are migrated.
 */

/**
 * @typedef {import("@supabase/supabase-js").Session} Session
 * @typedef {import("@supabase/supabase-js").User} User
 * @typedef {import("@supabase/supabase-js").AuthChangeEvent} AuthChangeEvent
 */

/**
 * Asserts that window.supabase.auth is available.
 * Throws a descriptive error rather than a cryptic TypeError.
 * @returns {import("@supabase/supabase-js").SupabaseAuthClient}
 */
function authClient() {
  const auth =
    window.HOVER_EXTENSION_SUPABASE_CLIENT?.auth || window.supabase?.auth;
  if (!auth) {
    throw new Error(
      "auth-session: window.supabase is not initialised. " +
        "Ensure the Supabase SDK script and /config.js load before this module runs."
    );
  }
  return auth;
}

/**
 * Returns the current session, or null if unauthenticated.
 * @returns {Promise<Session|null>}
 */
export async function getSession() {
  const { data, error } = await authClient().getSession();
  if (error) {
    throw new Error(`auth-session.getSession failed: ${error.message}`, {
      cause: error,
    });
  }
  return data?.session ?? null;
}

/**
 * Returns the current user, or null if unauthenticated.
 * @returns {Promise<User|null>}
 */
export async function getUser() {
  const session = await getSession();
  return session?.user ?? null;
}

/**
 * Returns the current bearer token, or null if unauthenticated.
 * Useful when constructing requests outside of api-client.js.
 * @returns {Promise<string|null>}
 */
export async function getAccessToken() {
  const session = await getSession();
  return session?.access_token ?? null;
}

/**
 * Returns true when there is an active authenticated session.
 * @returns {Promise<boolean>}
 */
export async function isAuthenticated() {
  const session = await getSession();
  return session !== null;
}

/**
 * Registers a listener for auth state changes.
 * Returns a plain unsubscribe function — call it to stop listening.
 *
 * @param {(event: AuthChangeEvent, session: Session|null) => void} callback
 * @returns {() => void} unsubscribe
 */
export function onAuthStateChange(callback) {
  const { data } = authClient().onAuthStateChange(callback);
  return () => data.subscription.unsubscribe();
}

/**
 * Signs the current user out.
 * @returns {Promise<void>}
 */
export async function signOut() {
  const { error } = await authClient().signOut();
  if (error) {
    throw new Error(`auth-session.signOut failed: ${error.message}`, {
      cause: error,
    });
  }
}

export default {
  getSession,
  getUser,
  getAccessToken,
  isAuthenticated,
  onAuthStateChange,
  signOut,
};
