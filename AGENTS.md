# AGENTS.md

This file is for coding agents working on Searchy. Keep the project minimal,
private by design, and production-minded.

- All Telegram traffic goes through `github.com/FreshLabDev/tg` — startup
  (getMe, deleteWebhook, the readiness wait, the capability preflight), the
  poll loop, and every send. There is no second Telegram library and no
  private HTTP client; a method the module lacks goes through `tg.Call` or
  lands in the module.

## Project Shape

- Searchy is one Go service (module `searchy`).
- Its default backend is native Go metasearch: direct Bing and DuckDuckGo
  image/video adapters, plus Wikimedia images. No AI model, Python search
  service, or separate search index is required.
- `SEARCH_BACKEND=searxng` selects the existing private SearXNG JSON backend for
  rollback. With native selected, `SEARCH_FALLBACK=searxng` allows bounded
  fallback on weak or empty results; `none` disables it.
- Telegram uses long polling; the bot makes only outbound connections (no public
  routes).
- Two Postgres connections are used and **both are optional**:
  - `DATABASE_URL` — searchy's own private search analytics.
  - `CORE_DATABASE_URL` — the shared cross-bot **core** hub (identity, presence,
    language).
- Search has only bounded, expiring in-memory result caches and pagination
  sessions; nothing about a query is written to disk. No new database is needed.

## Hard Product Boundaries

- Two media categories only: **images** and **videos** (`i:` / `v:` prefixes).
- `f:` aliases normal fast search, with the same sources and ranking and no AI.
  Leading mode/category prefixes can be combined in either order (`f: v:` or
  `v: f:`); do not rewrite prefix-like text inside the query. Plain queries use
  the same mode. Do not add an `ai:` mode as part of this backend migration.
- Inline search is the primary surface; DM and group search use a numbered grid.
- The `/start` menu is inline and callback-driven (language, stats, help, about).
- In groups, searching is explicit via `/search`; plain messages are ignored.
- Do not add audio/music search, webhook mode, or search/delivery surfaces
  beyond these unless explicitly requested.
- `internal/vido` is a production least-privilege bridge, not a downloader:
  Searchy creates owner-bound intents and executes validated `DeliveryPlan v1`
  operations, while Vido owns URLs, settings, extraction, shared-cache writes,
  error classification, and Vido-DM delivery. `WEBHOOK_SECRET` remains an
  unwired placeholder.

## Privacy Invariants

- **Never store the query text** — not in the analytics DB, not in logs, not in
  in-memory grid sessions. Only counts, timings, category, result type, and the
  source/engine may be recorded.
- Analytics rows key on the raw Telegram user id with no query, title, or URL.
- Do not log the query text anywhere, including on errors or degraded-engine
  warnings.

## Identity Lives In Core

- **Do not recreate a local `users` table.** Identity, presence, and the user's
  saved language live in the shared **core** Postgres, keyed on the global
  Telegram id. The startup schema drops any legacy `users` table on purpose.
- Searchy identifies itself to core as `searchy` and connects with a
  least-privilege role (`searchy_core`).
- Write to core **only** through its SECURITY DEFINER functions (`core.touch`,
  `core.set_language`, `core.clear_language`); read language through
  `core.effective_language`. Do not read or write core tables directly.
- Every `db.Store` and `core.Core` method must stay nil-safe: an unset or
  unreachable database makes the call a harmless no-op so search keeps working.

## Search Backend Invariants

- Native retrieval is incremental. Freeze each returned inline page; late
  results may only enter later pages. Never try to push updates into an already
  answered Telegram inline picker.
- Native `next_offset` values are opaque, fit Telegram's 64-byte limit, and bind
  a page to the user, query digest, language, category, and filters. Replaying a
  cursor returns the same page; expiration and invalid cursors are explicit.
- Retain no raw query in native sessions. Use a process-local HMAC for query
  identity; keep caches/sessions bounded in size and time. Cancellation of one
  caller must not cancel shared work needed by another caller.
- Defaults: `NATIVE_FIRST_PAGE_WAIT=800ms`, `NATIVE_TIMEOUT=3.5s`,
  `REQUEST_TIMEOUT=8s` including fallback, `SEARCH_MAX_CONCURRENT=8`, and
  `CACHE_TTL=5m`. Enforce bounded per-user and per-source work as well. Do not
  cache failed or degraded responses as complete healthy results for five minutes.
- Preserve operator `LANGUAGE`, per-user language, and `SAFE_SEARCH` in native
  adapters. Source markup can change; cover parsers with public saved fixtures
  and keep query-free source timing/error diagnostics.
- Validate every inline media/button URL as strict HTTPS before answering: one
  bad URL rejects the whole `answerInlineQuery`. Browser-displayable media is
  not sufficient evidence of Telegram compatibility. Normalize/filter unsupported
  formats at the source boundary and use a provider's compatible preview when
  needed; inline photo URLs and video covers must be Telegram-compatible JPEGs.
- Any server-side media fetching must reject private addresses and unsafe
  redirects and enforce response size limits. Keep the Vido cache/path rules.

### SearXNG fallback and switchback

- Query with a pinned `engines=` set; **never** send `categories` (it fans out to
  every enabled engine and overloads a private instance).
- `ENGINES_*`, `DISCOVERY_*`, and `IMAGE_PROXY` configure only SearXNG. Do not
  silently apply legacy engine pools or discovery quotas to native search.
- Surface a `403` on `format=json` as a clear "enable JSON format" error.
- Keep the SearXNG instance internal-only; `IMAGE_PROXY` stays `false` unless the
  instance itself is publicly reachable over HTTPS.

## Code Style

- Prefer small packages and simple interfaces.
- Use the standard library unless a dependency materially reduces complexity.
- Keep SQL explicit and close to the store method that uses it.
- Avoid broad refactors while changing behavior.
- Add comments only when they explain non-obvious privacy, concurrency, or
  protocol behavior.
- User-facing strings go through `internal/i18n`; escape user-controlled text
  before Telegram HTML formatting.

## Schema

- The analytics schema is `internal/db/schema.sql`, applied on startup and
  written to be idempotent (`CREATE ... IF NOT EXISTS`, guarded `ALTER`).
- Do not add a query-text column to `searches` or `selections`.
- Keep DDL bounded by `lock_timeout` / `statement_timeout` so startup can't wedge
  on a held lock.

## Versioning

- Work on `dev`. Pre-releases (`-alpha.N`, `-beta.N`, `-rc.N`) are tagged on
  `dev`; stable versions are tagged on `main`, on the merge commit from `dev`.
  The test bot runs `dev`, the production bot runs `main`.
  The `## Unreleased` changelog section tracks what has merged to `dev` but is
  not yet tagged. See `docs/versioning.md`.
- Follow `docs/versioning.md` for release tags.
- Keep the first release line as `v0.1.0-alpha.1`, `v0.1.0-beta.1`,
  `v0.1.0-rc.1`, then `v0.1.0`.
- Use patch versions for fixes, minor versions for MVP-compatible product or
  operational improvements, and reserve `v1.0.0` for a stable production contract.
- Treat required env vars, SearXNG requirements, the analytics schema, the core
  contract, and deployment assumptions as breaking-sensitive before `v1.0.0`.

## Changelog And Releases

- Keep `CHANGELOG.md` updated for notable user-visible, operational, security,
  schema, and behavior changes.
- Add unreleased notes under `## Unreleased`; move them into a version section
  only when preparing the tag.
- Follow `docs/releases.md` for changelog sections, release note shape, and
  GitHub Release commands.
- Mark `alpha`, `beta`, and `rc` GitHub Releases as pre-releases.
- Do not publish a GitHub Release until verification results and release notes
  match the tagged code.

## Testing

The local machine may not have `go` in `PATH`. Use Docker for verification:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go test ./...
```

Run vet when changing service logic:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go vet ./...
```

For Docker/compose changes, also run:

```sh
docker compose -f deploy/docker-compose.yml config
```

## Release Checklist

- `/start` opens the menu in a private chat and in a group.
- Inline search returns image and video cards and pages with `next_offset`.
- DM plain-text search and group `/search` both return a numbered grid; paging
  and per-item pick work.
- Category prefixes (`i:` / `v:`) narrow correctly, with `f:` combinations in
  either order matching normal search.
- Native pages replay unchanged, late results do not reorder returned cards,
  and expired cursors, source failures, cancellation, and concurrency limits
  are tested. Partial/degraded results are not cached as healthy complete pages.
- `SEARCH_BACKEND=searxng` switches back successfully; fallback stays inside
  the overall request deadline, and `SEARCH_FALLBACK=none` works without SearXNG.
- Verify actual Telegram media delivery, not just an HTML thumbnail preview.
- A SearXNG `403` (JSON off) surfaces as a clear error, not a silent empty.
- Language picker persists a choice and it survives a restart (via core).
- `/stats` renders personal and global tabs when `DATABASE_URL` is set, and the
  bot runs cleanly with both databases unset.
- No query text appears in analytics or logs.
- `/healthz` reports the build version, commit, and date.
- The Searchy × Vido matrix passes for DM, group selector, another group member,
  topic, inline deep link, audio, terminal errors, and cached `file_id` reuse.
- A fresh production bridge smoke produces a delivered `target_bot=searchy`
  job; search-only activity is not sufficient evidence for an RC or stable tag.

## License

Searchy is licensed under Apache-2.0. Preserve the root `LICENSE` and `NOTICE`
files and keep public documentation consistent with that license.

## Deploying

Do not invent a deploy. [`docs/releases.md`](docs/releases.md) has a **Deploying**
section describing this stack exactly: which host directory it lives in, which
env file names the image, which networks it needs, and how to roll back. Read it
before touching anything on the host.

Two rules that hold everywhere and are easy to get wrong:

- **Nothing is built on the host.** A production stack pulls the image the
  release workflow published. A `build:` section in a production manifest is a
  bug.
- **Pin the digest, not the tag.** A tag moves; a digest names one build that was
  tested, and a rollback becomes one line with nothing to rebuild.
