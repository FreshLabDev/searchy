# Changelog

All notable Searchy changes are documented here.

Searchy uses SemVer-style versions with pre-release tags before `v1.0.0`. Release
notes should be copied from the relevant changelog section and lightly edited for
GitHub Releases.

## Unreleased

Use this section for changes that are merged but not released yet.

## v0.2.0-alpha.2 - 2026-07-22

### Fixed

- Stop presenting a text-only video card when its cover can no longer be
  fetched. Searchy now shows an explicit retry response and does not create a
  Vido download intent for that unusable card.
- Record a grid selection only after its media was sent successfully.
- Remove SepiaSearch from the default discovery pool after a live result linked
  to an unreachable PeerTube instance and timed out in Vido. Bilibili and
  PeerTube remain as the two pinned discovery video engines.

## v0.2.0-alpha.1 - 2026-07-22

### Changed

- Search the relevance-oriented core and long-tail discovery engine pools in
  parallel, with a 30% discovery share that rises to 50% for weak core results.
- Rank deduplicated media using SearXNG score, reciprocal rank, title coverage,
  engine consensus, media quality, host diversity, and balanced image/video
  scheduling.
- Use each Searchy user's language for SearXNG and add one English core fallback
  for weak non-English results. Operator-set `LANGUAGE` remains authoritative.
- Keep the proven SearXNG `2026.6.24-e3126b89e` production digest after the
  `2026.7.16-9f9c00819` candidate regressed DuckDuckGo Images under load. Pin an
  eight-second request budget and explicit core and discovery engine defaults.

### Fixed

- Keep all contributing engines and positions during deduplication while
  preserving the primary engine used by existing analytics.
- Accept protocol-relative HTTPS media, upgrade known Bilibili HTTP hosts, and
  select the first safe HTTPS candidate instead of the first non-empty URL.
- Preserve valid width, height, and resolution metadata for quality ranking.

### Privacy and operations

- Upgrade `golang.org/x/text` to `v0.39.0` to fix reachable vulnerability
  `GO-2026-5970` in Unicode normalization used through `pgxpool`.
- Keep query text out of logs, analytics, grid sessions, and benchmark output;
  cache and singleflight isolation now also include the resolved language.
- Add no database migration, proxy, Tor route, public SearXNG route, or new
  secret. `SAFE_SEARCH=0`, `IMAGE_PROXY=false`, and direct egress remain.
- Add a read-only 36-query synthetic benchmark covering exact, long-tail, and
  multilingual searches. Against the live candidate it improved average
  keyword relevance@10 by 18.6%, kept the exact-query group non-regressing,
  returned no empty cases, kept usable HTTPS at 100%, and measured a 1.5-second
  primary p95 even with one image core engine degraded.

## v0.1.0 - 2026-07-19

First stable Searchy release: private image and video search for Telegram with
personal Vido downloads.

### Highlights

- Search images and videos inline, in direct messages, and in groups with
  paginated results and numbered media grids.
- Download video or audio through Vido with the clicking user's personal
  settings, including private handoff for shared group and inline cards.
- Keep inline results personal, preserve the selected language through Core,
  and expose useful personal and global statistics without storing queries.

### Reliability and privacy

- Never persist or log search text; analytics contain only counts, timings,
  categories, result types, and engines.
- Use pinned SearXNG engines and keep search available when analytics, Core, or
  the Vido bridge is temporarily unavailable.
- Prevent duplicate deliveries with owner-bound intents, explicit retry for
  uncertain sends, and bot-specific Telegram file reuse.

### Operations

- Move GitHub Actions to their Node 24 runtime majors, removing the Node 20
  deprecation warnings without changing the Searchy runtime image.

## v0.1.0-rc.1 - 2026-07-18

### Fixed

- Reject an empty pinned-engine set instead of falling back to SearXNG
  `categories`, which could fan out to every enabled engine and overload the
  private instance.
- Align the public, operator, and agent documentation with `v0.1.0-rc.1`, the
  production Vido bridge, the single physical `core-postgres` deployment, and
  the complete Searchy × Vido RC smoke gate.

## v0.1.0-beta.3 - 2026-07-14

### Fixed

- Show exactly one `v` before the version in the About panel, regardless of
  whether the build metadata uses a release tag (`v0.1.0-beta.3`) or a bare
  semantic version (`0.1.0-beta.3`).

## v0.1.0-beta.2 - 2026-07-14

### Changed

- Keep the original in-chat Searchy delivery when the user who selected a
  group video presses Download, but redirect any other group member through a
  personal Vido deep link. Vido applies the clicking user's settings and sends
  the result only in that user's private chat, with no extra group message.

### Security

- Derive the personal Vido intent through Core without returning the source URL
  to Searchy. The new token is bound to the clicking user and the exact original
  group card (`chat_id` plus `message_id`), retains the source for at most the
  card's six-hour lifetime, and rejects copied callback data.

### Operations

- Requires Core `v0.1.0-rc.2` (migration 006) and Vido `v2.3.5-beta.3`.

## v0.1.0-beta.1 - 2026-07-13

### Fixed

- Keep one Searchy process alive while the shared local Telegram Bot API warms
  up, retrying transient `getMe` failures for up to two minutes while still
  failing immediately for permanent configuration or token errors.

### Operations

- Extend the container health start period beyond the bounded Bot API warm-up
  window so health-gated rollouts do not fail while startup is still retrying.
- GitHub Release titles now match their version tags exactly without a project
  name prefix.

## v0.1.0-alpha.5 - 2026-07-13

### Fixed

- Keep the Vido bridge reconnectable when core-postgres is unavailable during
  Searchy startup, so Download buttons recover without restarting the bot.
- Deliver Vido's exact terminal reason to Searchy users for unsupported links,
  2 GB limits, DRM, authentication, source rate limits, timeouts, unavailable
  media, video-only mismatches and audio extraction failures.
- Record a durable `sending` boundary before every Telegram operation. A lost
  ACK can no longer downgrade a delivered operation or trigger an automatic
  duplicate after restart; uncertain delivery requires an explicit owner-bound
  retry.
- Renew long delivery leases, reject malformed plans durably, invalidate every
  cached item in a failed album, and keep terminal notifications alive across
  Searchy restarts.

### Security

- Resolve and reject symlinks inside the read-only shared cache, and keep source
  URLs/tokens out of delivery failures and bridge logs.

### Operations

- Requires core migration `005_vido_searchy_bridge_reliability.sql` and Vido
  `v2.3.5-alpha.2`.

## v0.1.0-alpha.4 - 2026-07-13

### Fixed

- Omit `reply_markup` entirely when a DeliveryPlan operation has no buttons;
  this fixes standalone audio delivery on the strict local Bot API.
- Delete the webhook with a parameterless GET, removing the local Bot API's
  empty-multipart startup warning without dropping pending updates.

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
