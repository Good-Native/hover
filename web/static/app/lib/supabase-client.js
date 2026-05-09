/**
 * lib/supabase-client.js — module-side Supabase bootstrap
 *
 * New ES module code can call ensureSupabaseClient() to convert the loaded
 * UMD SDK namespace into the shared window.supabase auth client expected by
 * the existing app helpers.
 */

import { isConfigured, supabaseAnonKey, supabaseUrl } from "/app/lib/config.js";

/**
 * Returns the active browser auth client when already initialised.
 * @returns {import("@supabase/supabase-js").SupabaseClient|null}
 */
export function getSupabaseClient() {
  return window.supabase?.auth ? window.supabase : null;
}

/**
 * Initialises the browser auth client from the loaded UMD SDK when needed.
 * The returned client is stored on window.supabase for compatibility with the
 * existing module helpers that read window.supabase.auth.
 *
 * @returns {import("@supabase/supabase-js").SupabaseClient}
 */
export function ensureSupabaseClient() {
  const existingClient = getSupabaseClient();
  if (existingClient) {
    return existingClient;
  }

  if (!isConfigured()) {
    throw new Error(
      "Supabase configuration unavailable. Please reload and try again."
    );
  }

  const supabaseNamespace = window.supabase;
  if (typeof supabaseNamespace?.createClient !== "function") {
    throw new Error("Supabase SDK is not available yet. Please refresh.");
  }

  const client = supabaseNamespace.createClient(supabaseUrl, supabaseAnonKey);
  window.supabase = client;
  return client;
}

export default {
  getSupabaseClient,
  ensureSupabaseClient,
};
