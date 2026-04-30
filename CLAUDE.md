# CLAUDE.md

Last reviewed: 2026-03-28

This file is the project operating guide for Claude Code (desktop/CLI) in this
repository.

## Hard requirements

- Use Australian English in code comments, commit messages, user-facing text,
  and generated docs.
- Preserve existing behaviour unless explicitly asked to change it.
- Ask at most one clarifying question when ambiguity materially affects
  correctness or safety.
- Ask for explicit confirmation before destructive steps (schema changes,
  credentials/config changes, or data-impacting actions).
- Do not expose, invent, or log secrets, credentials, JWTs, or end-user content.
- Keep edits scoped and incremental.
- If a safety limit is reached in a tool, pause and continue with the best
  available path.

## Technical baseline

- Language stack: Go 1.26 backend, Vue-free frontend, Supabase-backed data.
- Run formatting (`gofmt`, `goimports`) on touched Go files.
- Prefer `go test` and targeted checks before broader validation.
- Keep commit messages short and descriptive (five to six words).

## Code navigation

- Prefer symbol-aware or structural code navigation for Go code when available.
- Use `grep`/`glob` for non-Go files (YAML, shell scripts, HTML, JSON).

## Project-specific rules

**Auth redirect contract:** OAuth redirect targets are centralised in
`web/static/js/auth.js` (`handleSocialLogin`). Deep-link URLs must return to the
exact originating URL. Invite acceptance routes to `/welcome`. Homepage auth may
route to the default app landing page.

**Dockerfile triple-surface rule:** Every new top-level HTML page requires three
changes — HTTP route in `internal/api/handlers.go`, the page file on disk, and a
`COPY` line in `Dockerfile`. Missing the Dockerfile copy causes a runtime 404 on
Fly.

**Database migrations:** Use `supabase migration new <name>` to create migration
files. Never edit or rename migrations after they are deployed. Keep migrations
additive; avoid destructive schema changes.

**Local dev auth:** Use `GET /dev/auto-login` to sign in during local
development — no OAuth flow, no manual credential entry. The Go server fetches a
session server-side and injects it into `localStorage`, then redirects to
`/dashboard`. Only works when `APP_ENV=development`. The preview browser in
Claude cannot reach `127.0.0.1:54321` directly, so always use this endpoint
rather than the normal sign-in modal. After `supabase db reset`, the dev user
(`dev@example.com`) is re-seeded automatically — just hit `/dev/auto-login`
again.

**Claude preview server:** `.claude/launch.json` starts the server via `air`
(hot reloading). Supabase must already be running — call `preview_start` with
name `go-server` and then navigate to `/dev/auto-login` to authenticate.

## Instruction loading (how this repo should be read by Claude Code)

- `CLAUDE.md` (this file) and optional `CLAUDE.local.md` are read in the project
  scope.
- Agent role files are loaded from `.claude/agents/*.md` and use YAML
  frontmatter.
- Project agent files should be named and structured as `name`, `description`,
  and optional `tools`/`model`.

## Claude subagents required in this repo

Use these files as dedicated specialists to reduce context pollution:

- `.claude/agents/planner.md`
- `.claude/agents/code-reviewer.md`
- `.claude/agents/security-auditor.md`

PR review support:

- Use `bash scripts/pr-status-check.sh [PR_NUMBER]` to fetch CodeRabbit comments
  and CI check statuses. Do not use raw `gh api` commands for this.
- Use `bash scripts/pr-comment-reply.sh` to reply to, resolve, or skip review
  threads. Do not use raw `gh api` GraphQL mutations for this.
- The `/pr-review` skill automates the full review-and-resolve workflow
  (`.claude/skills/pr-review/SKILL.md`).

## Work approach

- For small tasks: do minimal read/plan/implement.
- For large changes: confirm scope, prepare a staged plan, then implement in
  bounded increments.
- Report blockers clearly with concrete risk and proposed mitigation.

## Automated review gates

- Treat `scripts/security-check.sh` and Coderabbit checks as mandatory pre-merge
  gates.
- Do not recommend or request bypasses unless explicitly approved by project
  maintainers.
- If a change risks failing pre-commit/security checks, call it out before
  implementation.

## Source-of-truth docs

For detailed, authoritative rules and onboarding:

- `README.md`
- `CHANGELOG.md`
- `SECURITY.md`
- `docs/architecture/ARCHITECTURE.md`
- `docs/architecture/CRAWL_HANDLING.md` — case → action policy (what we do to a
  job and its tasks for each domain/page condition). Add a row when you add a
  case.
- `docs/architecture/CONFIG_REFERENCE.md`
- `docs/architecture/DATABASE.md`
- `docs/architecture/API.md`
- `docs/development/DEVELOPMENT.md`
- `docs/development/BRANCHING.md`
- `docs/TEST_PLAN.md` (or equivalent)
