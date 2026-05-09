/**
 * lib/scheduler-api.js — shared scheduler helpers
 *
 * Shared between app pages and the Webflow Designer extension.
 */

import * as apiClient from "/app/lib/api-client.js";
import { normaliseDomain } from "/app/lib/site-jobs.js";

function resolveApi(api) {
  return api || apiClient;
}

/**
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<Record<string, unknown>[]>}
 */
export async function listSchedulers(options = {}) {
  const api = resolveApi(options.api);
  const response = await api.get("/v1/schedulers");
  return Array.isArray(response) ? response : [];
}

/**
 * @param {string} schedulerId
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<Record<string, unknown> | null>}
 */
export async function getScheduler(schedulerId, options = {}) {
  const api = resolveApi(options.api);
  const response = await api.get(
    `/v1/schedulers/${encodeURIComponent(schedulerId)}`
  );
  return response && typeof response === "object" ? response : null;
}

/**
 * @param {string} domain
 * @param {{ api?: typeof apiClient, schedulers?: Record<string, unknown>[] }} [options]
 * @returns {Promise<Record<string, unknown> | null>}
 */
export async function findSchedulerByDomain(domain, options = {}) {
  const targetDomain = normaliseDomain(domain);
  if (!targetDomain) {
    return null;
  }

  const schedulers =
    options.schedulers || (await listSchedulers({ api: options.api }));

  return (
    schedulers.find(
      (scheduler) => normaliseDomain(scheduler?.domain || "") === targetDomain
    ) || null
  );
}

/**
 * @param {string} domain
 * @param {number} scheduleIntervalHours
 * @param {{ api?: typeof apiClient, currentScheduler?: Record<string, unknown> | null, extra?: Record<string, unknown> }} [options]
 * @returns {Promise<Record<string, unknown>>}
 */
export async function saveSchedulerForDomain(
  domain,
  scheduleIntervalHours,
  options = {}
) {
  const api = resolveApi(options.api);
  const currentScheduler = options.currentScheduler || null;
  const payload = {
    ...(options.extra || {}),
    schedule_interval_hours: scheduleIntervalHours,
  };

  if (!currentScheduler?.id) {
    return api.post("/v1/schedulers", {
      ...payload,
      domain,
    });
  }

  return api.put(`/v1/schedulers/${currentScheduler.id}`, {
    ...payload,
    is_enabled: true,
  });
}

/**
 * @param {string} schedulerId
 * @param {{ api?: typeof apiClient, expectedIsEnabled?: boolean }} [options]
 * @returns {Promise<Record<string, unknown>>}
 */
export async function disableScheduler(schedulerId, options = {}) {
  const api = resolveApi(options.api);
  const payload = { is_enabled: false };
  if (typeof options.expectedIsEnabled === "boolean") {
    payload.expected_is_enabled = options.expectedIsEnabled;
  }
  return api.put(`/v1/schedulers/${schedulerId}`, payload);
}

/**
 * @param {string} schedulerId
 * @param {Record<string, unknown>} updates
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<Record<string, unknown>>}
 */
export async function updateScheduler(schedulerId, updates, options = {}) {
  const api = resolveApi(options.api);
  return api.put(`/v1/schedulers/${schedulerId}`, updates);
}

/**
 * @param {string} schedulerId
 * @param {{ api?: typeof apiClient }} [options]
 * @returns {Promise<void>}
 */
export async function deleteScheduler(schedulerId, options = {}) {
  const api = resolveApi(options.api);
  await api.del(`/v1/schedulers/${encodeURIComponent(schedulerId)}`);
}
