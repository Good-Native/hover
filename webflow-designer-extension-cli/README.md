# Hover Webflow Designer Extension (Smoke Test)

This is a valid Webflow CLI extension project used to smoke-test rendering in
Webflow Designer before wiring OAuth/API calls.

## Developing

```bash
npm run dev
```

The command does four things:

- Updates the local Webflow CLI when a newer allowed version is available
- Watches `src/index.ts` and compiles to `public/index.js`
- Serves the extension via `webflow extension serve`
- Keeps `public/index.js` generated from source (not committed)

## Deploying

```bash
npm run build
```

This prepares a `bundle.zip` for upload in Webflow App settings.

## Smoke-test checklist

1. Run `npm run dev`.
2. In Webflow Designer Apps panel, open your app via development mode.
3. Confirm you see:
   - "Designer Extension Smoke Test"
   - "UI loaded in Webflow Designer."
4. Click "Test Interaction" and confirm click counter increments.

## Backend read test

The panel includes a basic read-only API check:

1. Enter API base URL (for example `https://hover.app.goodnative.co`).
2. Click `Sign in / Create account` to open GNH auth popup and obtain token.
3. Click `Check API`.

Expected behaviour:

- Calls `GET /health` first to confirm reachability.
- Calls `GET /v1/integrations/webflow` and shows connection count.
- If unauthenticated, shows a clear "auth required" state.
- Auth token is stored in `sessionStorage` only (tab/session scoped).

## Popup auth bridge

- Extension opens `GET /extension-auth.html` on GNH app domain.
- Popup page is bootstrapped by `web/static/app/pages/webflow-login.js`, which
  still reuses the shared auth modal and OAuth helpers from `/js/auth.js`.
- On success, popup posts
  `{ source: "gnh-extension-auth", state, extensionState, accessToken }` back to
  the extension.
- The extension validates popup origin and state before accepting the token.
