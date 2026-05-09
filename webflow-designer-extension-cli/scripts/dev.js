#!/usr/bin/env node

const fs = require("fs");
const path = require("path");
const { spawn, spawnSync } = require("child_process");

const ROOT = path.resolve(__dirname, "..");
const PUBLIC_DIR = path.join(ROOT, "public");
const DEV_CONFIG_PATH = path.join(PUBLIC_DIR, "dev-runtime-config.js");
const DEFAULT_DEV_CONFIG =
  "window.HOVER_EXTENSION_CONFIG = window.HOVER_EXTENSION_CONFIG || {};\n";
const NPM_CMD = process.platform === "win32" ? "npm.cmd" : "npm";

function parsePreviewNumber(argv, env) {
  const cliArgs = argv.slice(2);
  const envPr = String(env.npm_config_pr || "").trim();

  if (envPr && envPr !== "true" && envPr !== "false") {
    return envPr;
  }

  const explicitArg = cliArgs.find((arg) => /^--pr=/.test(arg));
  if (explicitArg) {
    return explicitArg.split("=")[1] || "";
  }

  const prFlagIndex = cliArgs.indexOf("--pr");
  if (prFlagIndex >= 0 && cliArgs[prFlagIndex + 1]) {
    return cliArgs[prFlagIndex + 1];
  }

  if (envPr === "true") {
    const positional = cliArgs.find((arg) => /^\d+$/.test(arg));
    if (positional) {
      return positional;
    }
  }

  return "";
}

function buildAppOrigin(argv, env) {
  const explicitOrigin = String(env.HOVER_APP_ORIGIN || "").trim();
  if (explicitOrigin) {
    return explicitOrigin.replace(/\/+$/, "");
  }

  const pr = parsePreviewNumber(argv, env);
  if (pr) {
    return `https://hover-pr-${pr}.fly.dev`;
  }

  return "";
}

function writeDevConfig(appOrigin) {
  const content = appOrigin
    ? `window.HOVER_EXTENSION_CONFIG = { appOrigin: ${JSON.stringify(appOrigin)} };\n`
    : DEFAULT_DEV_CONFIG;
  fs.writeFileSync(DEV_CONFIG_PATH, content, "utf8");
}

function runOrExit(command, args) {
  const result = spawnSync(command, args, {
    cwd: ROOT,
    stdio: "inherit",
  });

  if (result.status !== 0) {
    process.exit(result.status || 1);
  }
}

let restored = false;
function restoreDevConfig() {
  if (restored) {
    return;
  }
  restored = true;
  try {
    if (fs.existsSync(DEV_CONFIG_PATH)) {
      fs.unlinkSync(DEV_CONFIG_PATH);
    }
  } catch (_error) {
    // Best-effort cleanup only.
  }
}

function main() {
  const appOrigin = buildAppOrigin(process.argv, process.env);
  writeDevConfig(appOrigin);

  if (appOrigin) {
    console.log(`[dev] Using override app origin: ${appOrigin}`);
  } else {
    console.log("[dev] Using default app origin");
  }

  runOrExit(NPM_CMD, ["run", "sync-shared"]);

  const serve = spawn(
    NPM_CMD,
    ["exec", "--", "webflow", "extension", "serve", "--skip-update-check"],
    {
      cwd: ROOT,
      stdio: "inherit",
    }
  );

  const compile = spawn(
    NPM_CMD,
    ["run", "compile", "--", "--watch", "--preserveWatchOutput"],
    {
      cwd: ROOT,
      stdio: "inherit",
    }
  );

  let shuttingDown = false;

  function shutdown(exitCode = 0) {
    if (shuttingDown) {
      return;
    }
    shuttingDown = true;

    restoreDevConfig();

    if (!serve.killed) {
      serve.kill("SIGTERM");
    }
    if (!compile.killed) {
      compile.kill("SIGTERM");
    }

    setTimeout(() => {
      process.exit(exitCode);
    }, 50);
  }

  serve.on("exit", (code) => {
    shutdown(code || 0);
  });

  compile.on("exit", (code) => {
    shutdown(code || 0);
  });

  process.on("SIGINT", () => shutdown(0));
  process.on("SIGTERM", () => shutdown(0));
  process.on("exit", restoreDevConfig);
}

main();
