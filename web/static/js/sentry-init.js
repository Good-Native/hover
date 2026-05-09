// Shared Sentry browser SDK initialisation.
// Loaded after the Sentry CDN bundle on every top-level HTML page so the
// captureException calls in auth.js and other modules have a real client
// to talk to. Edit here to change every page at once.

(function () {
  if (!window.Sentry) {
    return;
  }

  // Mirrors the backend APP_ENV mapping so frontend events join up with their
  // server-side counterparts in Sentry: prod hostname → production, any
  // *.fly.dev (review apps) → staging, everything else (localhost, etc.) →
  // development.
  function detectEnvironment() {
    const host = window.location.hostname;
    if (host === "hover.app.goodnative.co") return "production";
    if (host.endsWith(".fly.dev")) return "staging";
    return "development";
  }

  Sentry.init({
    dsn: "https://04f57d1dedc67f307cef525b1b1551a6@o4509113950928896.ingest.us.sentry.io/4509113952370688",
    environment: detectEnvironment(),

    tracesSampleRate: 0.1,
    replaysSessionSampleRate: 0,
    replaysOnErrorSampleRate: 1.0,

    beforeSend(event) {
      return event;
    },

    ignoreErrors: [
      "ResizeObserver loop limit exceeded",
      "Non-Error promise rejection captured",
      /Failed to fetch/,
      /NetworkError/,
      /Load failed/,
    ],
  });
})();
