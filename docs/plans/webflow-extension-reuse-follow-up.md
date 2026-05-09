# Webflow Extension Reuse Follow-up

Date: 2026-04-10 Status: In progress Scope: Webflow Designer extension
consolidation and shared frontend reuse

## Current state

The broader ES modules migration is complete and archived in the changelog. The
main app now has an established shared frontend layer in `web/static/app/`, with
`/dashboard`, settings, and job details already migrated to the module
architecture.

Both branch checkpoints associated with that migration,
`feat/es-modules-extension-sync` and `feat/es-modules-phase-0`, are already
contained in `main`. They should be treated as historical delivery branches, not
pending work.

The Webflow Designer extension has already adopted part of that shared layer:

- shared API helpers via `app/lib/`
- shared Web Components via `app/components/`
- shared module sync into the extension build
- a bridge/import-map pattern so extension code can consume shared modules in a
  cross-origin runtime
- shared shell styling via `web/static/app/styles/shell.css`

That means the transition is no longer only a primitives-level migration. Shared
frontend logic now covers jobs, Webflow site configuration, organisation
context, scheduling, shell navigation, job export, and top-level site view
rendering. `/dashboard` has also moved onto the extension-style shell/layout
instead of the older dashboard-specific shell.

## What is already complete

- `/dashboard`, settings, and job details are on the ES module architecture
- shared module structure exists in `web/static/app/`:
  - `lib/` for reusable logic
  - `components/` for shared UI primitives
  - `pages/` for page orchestration
- the extension reuses shared primitives and API helpers through the bridge/sync
  approach
- shared extension/dashboard logic now includes:
  - `web/static/app/lib/site-jobs.js`
  - `web/static/app/lib/webflow-sites.js`
  - `web/static/app/lib/organisation-api.js`
  - `web/static/app/lib/scheduler-api.js`
  - `web/static/app/lib/site-view.js`
  - `web/static/app/lib/job-export.js`
  - `web/static/app/lib/shell-nav.js`
- design tokens exist in the app layer and mirror the extension theme
- shared shell styling now lives in `web/static/app/styles/shell.css`
- the extension auth popup is now module-native through
  `web/static/app/pages/webflow-login.js`, not the old `/js/auth.js` popup path
- the extension has its first native in-panel settings section (`Account`)
  driven by shared settings logic rather than a framed app page
- the completed migration is already documented in `CHANGELOG.md`

## Remaining gaps

- The extension still keeps most page orchestration in
  `webflow-designer-extension-cli/src/index.ts` rather than in shared `/app`
  modules.
- Native extension settings coverage is incomplete. `Account` is native in the
  extension shell, but the rest of the settings sections still need the same
  treatment.
- Job details still need a native in-extension implementation rather than
  relying on app-page stop-gaps.
- Extension CSS convergence is only partly complete. Shared shell styling now
  exists, but the extension still carries a large local stylesheet in
  `webflow-designer-extension-cli/public/styles.css`.
- Identity/avatar selection logic is aligned between dashboard and extension,
  but still duplicated rather than centralised into one helper.
- Preview Webflow OAuth and run-on-publish flows still depend on callback URL
  registration outside this frontend workstream.
- Some documentation and PR metadata still understate how far this branch has
  moved beyond the original jobs-only extraction.

## Recommended next phase

This follow-up should now be treated as a native extension-surface completion
pass. The branch has already moved beyond a jobs-only or JS-only reuse phase.

- Extract surface-agnostic extension logic from
  `webflow-designer-extension-cli/src/index.ts` into shared `/app` modules.
- Continue the native extension settings rollout:
  - `Team` next
  - then the remaining org-scoped settings sections
- Implement native in-extension job details using shared modules and shared
  layout primitives instead of framed app-page fallbacks.
- Keep the bridge/import-map approach for cross-origin extension use.
- Continue moving extension shell/component CSS into shared app styles so the
  dashboard and extension stop carrying parallel styling for the same UI.
- Centralise shared identity/avatar selection logic so dashboard and extension
  stop duplicating provider-avatar fallback rules.
- Update architecture and planning docs so they reflect the current state
  accurately.

## Non-goals

- Recreating the deleted March 2026 ES modules plan
- Re-running the full `/dashboard` modernisation effort
- Replacing working backend Webflow APIs as part of this documentation update
- Solving preview-domain Webflow OAuth registration inside frontend code

## Acceptance criteria

- The new plan clearly states that the ES modules migration is complete and
  archived in the changelog.
- The new plan clearly states that both ES modules branch checkpoints are
  already contained in `main`.
- The new plan distinguishes between what is already genuinely shared today and
  what still remains extension-local.
- The new plan reflects that `/dashboard` has already moved onto the
  extension-style shell/layout and that the popup auth flow is already
  module-native.
- The new plan makes the remaining work explicit: native settings coverage,
  native job details, `index.ts` thinning, and further CSS convergence.
- The new plan supersedes the old branch-era planning context without recreating
  archived migration history.
