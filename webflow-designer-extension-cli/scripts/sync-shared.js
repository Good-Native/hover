#!/usr/bin/env node
/**
 * Sync shared modules from the main app into the extension's public/ directory.
 * Run via: npm run sync-shared (or as part of build/dev).
 */

const fs = require("fs");
const path = require("path");

const APP_ROOT = path.resolve(__dirname, "../../web/static/app");
const PUBLIC = path.resolve(__dirname, "../public");

if (!fs.existsSync(APP_ROOT)) {
  console.error(`ERROR: Source directory not found: ${APP_ROOT}`);
  console.error("Ensure web/static/app exists before running sync-shared.");
  process.exit(1);
}

// Components — self-registering Web Components loaded as <script type="module">
const COMPONENTS = [
  "components/hover-status-pill.js",
  "components/hover-data-table.js",
  "components/hover-toast.js",
  "components/hover-job-card.js",
  "components/hover-tabs.js",
];

// Lib modules required by bridge.js or other shared extension runtime paths.
const REQUIRED_LIB_MODULES = [
  "lib/site-jobs.js",
  "lib/site-view.js",
  "lib/shell-nav.js",
  "lib/settings/account.js",
  "lib/job-export.js",
  "lib/webflow-sites.js",
  "lib/organisation-api.js",
  "lib/scheduler-api.js",
];

// Lib modules — shared logic loaded via bridge.js or available for future reuse.
const OPTIONAL_LIB_MODULES = [
  "lib/api-client.js",
  "lib/auth-session.js",
  "lib/formatters.js",
  "lib/integration-http.js",
  "lib/domain-search.js",
  "lib/invite-flow.js",
];

const REQUIRED_STYLE_FILES = ["styles/shell.css"];

function ensureDir(dir) {
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }
}

function syncFile(src, dest) {
  const destDir = path.dirname(dest);
  ensureDir(destDir);
  fs.copyFileSync(src, dest);
  const rel = path.relative(PUBLIC, dest);
  console.log(`  synced: ${rel}`);
}

console.log("Syncing shared modules from app → extension...");

// Sync components to public/ (flat, matches existing layout)
for (const file of COMPONENTS) {
  const src = path.join(APP_ROOT, file);
  const dest = path.join(PUBLIC, path.basename(file));
  if (fs.existsSync(src)) {
    syncFile(src, dest);
  } else {
    console.warn(`  WARN: ${file} not found, skipping`);
  }
}

// Sync required lib modules to public/lib/
for (const file of REQUIRED_LIB_MODULES) {
  const src = path.join(APP_ROOT, file);
  const dest = path.join(PUBLIC, file);
  if (fs.existsSync(src)) {
    syncFile(src, dest);
  } else {
    console.error(`  ERROR: required shared module missing: ${file}`);
    process.exit(1);
  }
}

// Sync optional lib modules to public/lib/
for (const file of OPTIONAL_LIB_MODULES) {
  const src = path.join(APP_ROOT, file);
  const dest = path.join(PUBLIC, file);
  if (fs.existsSync(src)) {
    syncFile(src, dest);
  } else {
    console.warn(`  WARN: optional shared module missing: ${file}, skipping`);
  }
}

for (const file of REQUIRED_STYLE_FILES) {
  const src = path.join(APP_ROOT, file);
  const dest = path.join(PUBLIC, file);
  if (fs.existsSync(src)) {
    syncFile(src, dest);
  } else {
    console.error(`  ERROR: required shared style missing: ${file}`);
    process.exit(1);
  }
}

console.log("Sync complete.");
