/**
 * lib/organisation-api.js — shared organisation and usage helpers
 *
 * Shared between app pages and the Webflow Designer extension.
 * UI state and event dispatch stay surface-specific.
 */

import * as apiClient from "/app/lib/api-client.js";

function resolveApi(api) {
  return api || apiClient;
}

/**
 * @param {{ api?: typeof apiClient, includeUsage?: boolean }} [options]
 * @returns {Promise<{
 *   organisations: Record<string, unknown>[],
 *   activeOrganisationId: string,
 *   usage: Record<string, unknown> | null
 * }>}
 */
export async function loadOrganisationContext(options = {}) {
  const api = resolveApi(options.api);
  const includeUsage = options.includeUsage !== false;

  const [organisationsPayload, usagePayload] = await Promise.all([
    api.get("/v1/organisations"),
    includeUsage ? api.get("/v1/usage") : Promise.resolve(null),
  ]);

  return {
    organisations: Array.isArray(organisationsPayload?.organisations)
      ? organisationsPayload.organisations
      : [],
    activeOrganisationId: organisationsPayload?.active_organisation_id || "",
    usage: usagePayload?.usage || null,
  };
}

/**
 * @param {string} name
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<Record<string, unknown> | null>}
 */
export async function createOrganisation(name, options = {}) {
  const api = resolveApi(options.api);
  const response = await api.post("/v1/organisations", { name });
  return response?.organisation || null;
}

/**
 * @param {string} organisationId
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<Record<string, unknown> | null>}
 */
export async function switchOrganisation(organisationId, options = {}) {
  const api = resolveApi(options.api);
  const response = await api.post("/v1/organisations/switch", {
    organisation_id: organisationId,
  });
  return response?.organisation || null;
}
