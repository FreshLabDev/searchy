# Changelog

All notable Searchy changes are documented here.

Searchy uses SemVer-style versions with pre-release tags before `v1.0.0`. Release
notes should be copied from the relevant changelog section and lightly edited for
GitHub Releases.

## Unreleased

Use this section for changes that are merged but not released yet.

## v0.1.0-alpha.3 - 2026-07-13

### Fixed

- Validate the bot token with a parameterless HTTP GET so Searchy can start on
  the pinned local Telegram Bot API server. The Telegram library's malformed
  empty multipart request was accepted by the cloud API but returned an empty
  response from the local server.

### Security

- Startup transport errors never include the bot-token-bearing request URL.

## v0.1.0-alpha.2 - 2026-07-13

### Added

- Owner-bound Vido downloads for every video result in DM and group cards;
  selected media is sent as a separate new Searchy message in the original
  chat/topic using the user's personal Vido settings.
- Personal inline Download deep links to Vido DM, with cover-only inline cards
  and no attempt to infer the selected chat.
- A least-privilege core-postgres bridge client and strict `DeliveryPlan v1`
  executor for video, photo, audio, document, album and sidecar operations.
- Owner-bound audio follow-up jobs, Vido download-settings menu link, localized
  bridge/error strings in all 16 languages, and explicit possible-duplicate
  retry after `delivery_unknown`.
- Optional shared local Bot API support and `/healthz` bridge component state.

### Changed

- Inline answers are personal (`is_personal=true`).
- Telegram 429 delivery waits exactly for `retry_after`; confirmed operation
  ACKs survive restarts and are never resent automatically.

### Security

- Searchy has no direct bridge-table access and receives neither source URLs nor
  Vido settings after job creation.
- Delivery plans reject unknown operations, non-HTTPS URL buttons, oversized
  captions/files, album-limit violations and local paths outside the shared
  cache root.

## v0.1.0-alpha.1 - 2026-07-04

First `v0.1.0` pre-release. A minimal Telegram bot that searches images and
videos through a self-hosted SearXNG instance and answers inline, in DMs, and in
groups — built with privacy and speed as first principles.

### Added

- Initial Searchy MVP implementation:
  - Telegram inline media search (the primary surface): `@bot query` returns up
    to 50 image/video cards with thumbnails, paged as the user scrolls via
    `next_offset`.
  - Category prefixes: `i:` searches images only, `v:` videos only, otherwise
    both.
  - DM and group search: a text message in DM (or `/search <query>` in a group)
    returns a single numbered result grid; tap a number to pull one item full, or
    page through 10 at a time. Grid buttons are shared in groups.
  - Video cards: a cover photo with an "Open on <platform>" link button and a
    "Download" placeholder button for the future `@vido` handoff.
  - `/start` inline menu (callback-driven, edited in place, owner-scoped):
    language picker, personal/global stats, help, and about panels.
  - 16-language i18n with the chosen language saved to the shared cross-bot
    **core** Postgres and resolved back through `core.effective_language`,
    falling back to the Telegram client's language hint when core is unset.
  - Private search analytics in searchy's own Postgres: counts and timings of
    searches and selections (category, result type, SearXNG engine) — never the
    query text — surfaced through the `/stats` panel.
  - Speed features: per-user inline debounce, an in-process LRU+TTL cache,
    `singleflight` de-duplication of identical concurrent searches, a tuned HTTP
    client, per-request timeouts with partial results, and Telegram inline
    `cache_time`.
  - SearXNG JSON provider: queries a pinned engine set (never `categories`),
    normalizes the engine-dependent image/video fields, interleaves results
    round-robin across engines, and surfaces degraded engines in logs.
  - Startup-applied, idempotent analytics schema with no `users` table (identity
    lives in core; any legacy `users` table is dropped).
  - `/healthz` runtime health endpoint reporting the stamped build
    version/commit/date, a distroless image with a binary `-healthcheck` probe,
    graceful shutdown, and JSON structured logging.
- Apache-2.0 license under FreshLab.
- Project documentation for architecture, the SearXNG integration, Telegram
  behavior, versioning, and release process.

### Security

- The query text is never persisted (analytics, logs, or grid sessions);
  analytics rows key on the Telegram user id with no query, title, or URL.
- Writes to the shared core go exclusively through its SECURITY DEFINER functions
  (`core.touch`, `core.set_language`, `core.clear_language`), using a
  least-privilege `searchy_core` role.
- Inline media and button URLs are validated as strict HTTPS before every
  `answerInlineQuery`, since one malformed URL rejects the whole answer.
- User-controlled text is HTML-escaped before Telegram HTML formatting.

### Known Limitations

- This is an alpha release intended for early live usage and feedback.
- Audio/music search, the `@vido` download handoff, and webhook mode are
  intentionally out of scope for this release.
