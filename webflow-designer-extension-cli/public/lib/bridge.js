/**
 * lib/bridge.js — bridge between shared ES modules and the extension's index.js
 *
 * Loads shared modules and exposes key functions on window.HoverLib
 * so the non-module index.js can consume them.
 *
 * Import map in index.html remaps /app/lib/ → ./lib/ and /app/components/ → ./
 * so the shared modules' internal imports resolve correctly.
 */

import * as apiClient from "/app/lib/api-client.js";
import * as formatters from "/app/lib/formatters.js";
import * as integrationHttp from "/app/lib/integration-http.js";
import * as jobExport from "/app/lib/job-export.js";
import * as organisationApi from "/app/lib/organisation-api.js";
import * as schedulerApi from "/app/lib/scheduler-api.js";
import * as shellNav from "/app/lib/shell-nav.js";
import * as settingsAccount from "/app/lib/settings/account.js";
import * as siteJobs from "/app/lib/site-jobs.js";
import * as siteView from "/app/lib/site-view.js";
import * as webflowSites from "/app/lib/webflow-sites.js";

// Expose shared modules for index.js consumption
window.HoverLib = {
  api: apiClient,
  exports: jobExport,
  fmt: formatters,
  http: integrationHttp,
  organisations: organisationApi,
  schedulers: schedulerApi,
  shell: shellNav,
  settings: {
    account: settingsAccount,
  },
  jobs: siteJobs,
  view: siteView,
  webflow: webflowSites,
};

// Signal that shared libs are ready
window.dispatchEvent(new Event("hover-lib-ready"));
