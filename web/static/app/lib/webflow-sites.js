/**
 * lib/webflow-sites.js — shared Webflow connection and site helpers
 *
 * Shared between app pages and the Webflow Designer extension.
 * Rendering and popup handling stay surface-specific; this module only owns
 * Webflow API reads/writes and current-site matching.
 */

import * as apiClient from "/app/lib/api-client.js";
import {
  getSiteDomainCandidates,
  normaliseDomain,
} from "/app/lib/site-jobs.js";

function resolveApi(api) {
  return api || apiClient;
}

/**
 * Start the Webflow OAuth flow.
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<{ auth_url?: string }>}
 */
export async function startWebflowConnection(options = {}) {
  const api = resolveApi(options.api);
  const response = await api.post("/v1/integrations/webflow");
  return response && typeof response === "object" ? response : {};
}

/**
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<Record<string, unknown>[]>}
 */
export async function listWebflowConnections(options = {}) {
  const api = resolveApi(options.api);
  const response = await api.get("/v1/integrations/webflow");
  return Array.isArray(response) ? response : [];
}

/**
 * @param {string} connectionId
 * @param {{ api?: typeof apiClient, page?: number, limit?: number }} [options]
 * @returns {Promise<{ sites: Record<string, unknown>[], pagination: { has_next?: boolean } | null }>}
 */
export async function listWebflowSites(connectionId, options = {}) {
  const api = resolveApi(options.api);
  const page = Number.isFinite(options.page) ? options.page : 1;
  const limit = Number.isFinite(options.limit) ? options.limit : 50;
  const response = await api.get(
    `/v1/integrations/webflow/${encodeURIComponent(connectionId)}/sites?page=${page}&limit=${limit}`
  );

  return {
    sites: Array.isArray(response?.sites) ? response.sites : [],
    pagination:
      response?.pagination && typeof response.pagination === "object"
        ? response.pagination
        : null,
  };
}

/**
 * @param {string} connectionId
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<void>}
 */
export async function disconnectWebflowConnection(connectionId, options = {}) {
  const api = resolveApi(options.api);
  await api.del(`/v1/integrations/webflow/${encodeURIComponent(connectionId)}`);
}

/**
 * @param {string} siteId
 * @param {{
 *   connectionId: string,
 *   enabled: boolean,
 *   api?: typeof apiClient
 * }} options
 * @returns {Promise<void>}
 */
export async function setWebflowSiteAutoPublish(siteId, options) {
  const api = resolveApi(options?.api);
  await api.put(
    `/v1/integrations/webflow/sites/${encodeURIComponent(siteId)}/auto-publish`,
    {
      connection_id: options.connectionId,
      enabled: options.enabled,
    }
  );
}

/**
 * @param {string} siteId
 * @param {{
 *   connectionId: string,
 *   scheduleIntervalHours: number | null,
 *   api?: typeof apiClient
 * }} options
 * @returns {Promise<void>}
 */
export async function setWebflowSiteSchedule(siteId, options) {
  const api = resolveApi(options?.api);
  await api.put(
    `/v1/integrations/webflow/sites/${encodeURIComponent(siteId)}/schedule`,
    {
      connection_id: options.connectionId,
      schedule_interval_hours: options.scheduleIntervalHours,
    }
  );
}

/**
 * Find the Webflow site config that matches the current site domain(s).
 *
 * @param {{
 *   siteDomain?: string | null,
 *   siteDomainCandidates?: string[],
 *   api?: typeof apiClient,
 *   connections?: Record<string, unknown>[],
 *   limit?: number
 * }} [options]
 * @returns {Promise<Record<string, unknown> | null>}
 */
export async function findMatchingWebflowSite(options = {}) {
  const candidates = getSiteDomainCandidates(options);
  if (!candidates.length) {
    return null;
  }

  const connections =
    options.connections || (await listWebflowConnections({ api: options.api }));
  if (!Array.isArray(connections) || connections.length === 0) {
    return null;
  }

  const limit = Number.isFinite(options.limit) ? options.limit : 50;

  for (const connection of connections) {
    if (!connection?.id) {
      continue;
    }

    let page = 1;

    while (true) {
      const payload = await listWebflowSites(connection.id, {
        api: options.api,
        page,
        limit,
      });

      const matchedSite = payload.sites.find((site) => {
        const domain = normaliseDomain(site?.primary_domain || "");
        return candidates.includes(domain);
      });

      if (matchedSite) {
        return {
          ...matchedSite,
          connection_id: connection.id,
        };
      }

      if (!payload.pagination?.has_next) {
        break;
      }

      page += 1;
    }
  }

  return null;
}
