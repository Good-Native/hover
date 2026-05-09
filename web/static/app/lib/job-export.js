/**
 * lib/job-export.js — shared job export helpers
 *
 * Shared between the dashboard and Webflow Designer extension.
 */

import * as apiClient from "/app/lib/api-client.js";
import {
  escapeCSVValue,
  sanitiseForFilename,
  triggerFileDownload,
} from "/app/lib/formatters.js";

function resolveApi(api) {
  return api || apiClient;
}

function prepareExportColumns(columns, tasks) {
  if (Array.isArray(columns) && columns.length > 0) {
    return {
      keys: columns.map((column) => column.key),
      headers: columns.map((column) => column.label || column.key),
    };
  }

  const keySet = new Set();
  for (const task of tasks) {
    Object.keys(task || {}).forEach((key) => keySet.add(key));
  }

  const keys = [...keySet];
  return { keys, headers: keys };
}

export async function downloadJobExport(jobId, options = {}) {
  const api = resolveApi(options.api);
  const payload = await api.get(`/v1/jobs/${encodeURIComponent(jobId)}/export`);
  const tasks = Array.isArray(payload?.tasks) ? payload.tasks : [];

  if (tasks.length === 0) {
    return {
      empty: true,
      filename: "",
      taskCount: 0,
    };
  }

  const { keys, headers } = prepareExportColumns(payload.columns, tasks);
  const csvRows = [headers.join(",")];
  for (const task of tasks) {
    const values = keys.map((key) => escapeCSVValue(task[key]));
    csvRows.push(values.join(","));
  }

  const filenameBase = sanitiseForFilename(payload.domain || `job-${jobId}`);
  const filename = `${filenameBase}-hover-export.csv`;
  triggerFileDownload(csvRows.join("\n"), "text/csv", filename);

  return {
    empty: false,
    filename,
    taskCount: tasks.length,
  };
}

export default {
  downloadJobExport,
};
