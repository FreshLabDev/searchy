# Changelog

All notable Searchy changes are documented here.

The `## <tag>` section of this file *is* the GitHub Release body: the release
workflow copies it verbatim and refuses a tag that has no section. Write it
for whoever has to decide whether to upgrade.

See [`docs/versioning.md`](docs/versioning.md) for what the numbers mean and
[`docs/releases.md`](docs/releases.md) for how a release is published.

## Unreleased

Use this section for changes that are merged but not released yet.

### Added

- **"Follow Telegram" on the language screen.** Picking a language wrote a
  manual claim to the shared core store, and a manual claim outranks
  everything — but nothing removed it, so one mistaken tap followed a person
  across every bot in the family until an admin deleted the row by hand. The
  new button under the grid deletes searchy's own observation and lets the
  Telegram client's `language_code` win again. Translated into all 16
  languages.

### Changed

- **Every option of a set is marked, in both states.** The language picker
  marked only the current language, so fifteen labels started at the button's
  left edge and one started two characters in. Both states now carry a glyph
  (`◉ ` / `◎ `), the way the statistics tabs already did.
- **Three buttons carry colour, and no others.** Close is destructive
  everywhere it appears — including the result grid, which had its own copy of
  it. The current language and the open statistics tab are marked as state.
  Language on the DM `/start` panel is the one thing that leads, because
  nothing else on that screen is legible until the language is right. The group
  panel leads with inline search by position and takes no colour at all. Older
  Telegram clients ignore button styles and show the panels exactly as before.
- **Every screen's text comes from the same two helpers.** The About card, the
  result grid caption and the "Bot" sub-heading in global statistics each wrote
  their own bold/italic markup; the empty statistics screen put its one line
  outside the blockquote, so the panel changed shape depending on whether you
  had searched yet. Only that empty screen looks different — it gains the
  blockquote every other panel has.

## v0.2.1 - 2026-09-09

Searchy leaves the third-party Telegram client it was the last bot in the family
to use, and moves onto the shared one. Nothing about searching changes; what
changes is that it can now be given the things the shared client gets.


### Changed

- Depends on the stable `github.com/FreshLabDev/tg` v0.1.0.

- **`/start` is a different screen in a group than in a DM.** A DM is the
  user's own space, so it keeps language and personal statistics and has no
  Close — there is nothing else in the chat, and the button offered to delete
  the only thing on screen. A group is shared, so the panel there leads with
  inline search, keeps Help and About, points at the DM for the personal
  settings, and always offers Close: the panel is one member's menu sitting in
  everyone else's feed. Help, About, Language and Statistics follow the same
  rule — Close only where there is something to close.
- **The About tab is the family's format.** Name and version on one line, one
  line of purpose, then a blockquote of `key · value` facts: the search backend,
  the build date, the repository as a link with its license, and the admin. The
  repository is a link inside the text rather than a button of its own, because
  two controls for one action is one too many. Everything is translated into all
  16 languages, values included.
- The build date is visible. `aboutBody` had been passing `buildinfo.Date` into
  a string that referenced it in none of the 16 languages, so nothing told a CI
  build from a laptop one.
- **Emoji are gone from every button and heading**, in all 16 languages: About,
  Help, Language, Statistics, Search, Download, Video download settings, Open
  original, Open on {platform}, Retry sending, the `/start` greeting, the panel
  headings and the results caption. They stay only where they mark state — the
  selected language and the active statistics tab keep `◉ / ◎`. An icon on every
  control is decoration that stops meaning anything, which is why the marks that
  do mean something got lost in it.
- Navigation reads `Back` and `Close`, without the `⬅` and `✖`.
- `homePanel` is assembled the way every other panel is, through `header()` and
  `blockquote()`, instead of concatenating three keys by hand.
- **Searchy runs entirely on `github.com/FreshLabDev/tg`.**
  `github.com/go-telegram/bot` is gone: the poll loop, every send, the inline
  answers and the Vido delivery bridge all go through the family's own client.
  Searchy was the last bot on a third-party library, which cost it everything
  the shared client had learned — Bot API 10.3 button styles, ephemeral
  messages, one place for token redaction, one retry policy, one set of error
  classifiers — and the gap was only going to widen.
- The dispatcher is now searchy's own long-polling loop. Updates are still
  handled concurrently (`WORKERS`, default 32), because the inline path
  deliberately blocks: the debouncer holds a keystroke for its window and
  abandons it when a newer one arrives, which only works while both are in
  flight. The offset advances as soon as an update is dispatched, as it did
  before, and a failed poll backs off from one second to thirty rather than
  ending the process.
- **Preflight now lists everything searchy cannot work without**, not just two
  methods: `answerInlineQuery`, `answerCallbackQuery`, `deleteMessage`,
  `editMessageMedia`, `editMessageReplyMarkup`, `editMessageText`,
  `sendMessage` and `sendPhoto` — plus `sendVideo`, `sendAudio`, `sendDocument`
  and `sendMediaGroup` when the Vido bridge is on, since those are only
  reachable through it. A Bot API server too old for any of them now refuses
  the start instead of letting searchy poll happily and answer nothing, which
  is the failure the check exists for.
- A Vido plan's `local_file_uri` is sent as a local path rather than a bare
  `file://` string, so a searchy pointed at Telegram's own endpoint is told
  that the path could never work there, instead of getting back a
  wrong-file-identifier error that points nowhere near the cause.
- Delivery failures are classified from Telegram's HTTP status rather than a
  library's sentinel errors. The `definite failure` set is unchanged (400, 401,
  403, 404); everything else still becomes `delivery_unknown`, so a send that
  may have landed is never silently retried.
- Captions and text in a delivery plan are always sent as HTML. The plan field
  is named `caption_html` and its text is Vido's own, so the previous
  `parse_mode` passthrough only ever chose between HTML and rendering HTML
  literally.
- One versioning and release document for the whole family. `docs/versioning.md`
  and `docs/releases.md` are now byte-identical across every Asterfield
  repository apart from two clearly marked sections: this repository's own
  version line, and the surface where a change here breaks something. They spell
  out what each of the three numbers means, what the `-alpha.N` suffix counts,
  when alpha becomes beta and when it is legitimate to skip to rc or run a
  pre-release in production.
- **Pre-releases are now tagged on `dev`, not `main`.** Only stable versions are
  tagged on `main`, on the merge commit from `dev`. `release.yml` had no branch
  check at all before, so a tag pushed from any branch would publish; it now
  refuses a tag that is not on the branch its channel is published from.
  Earlier pre-releases were tagged on `main` under the previous rule; they are
  left as they are.


- The production stack pulls the released image instead of building one.
  `deploy/ws04/searchy/compose.yaml` built from a working copy on the host, so
  what served users was not the artifact CI had tested, scanned and published,
  and the version it reported came from `SEARCHY_VERSION` / `SEARCHY_COMMIT` /
  `SEARCHY_BUILD_DATE` kept by hand in `.env`. Those are now baked into the
  image by the release workflow, and `SEARCHY_IMAGE` -- which has no default,
  so an unset one stops the stack -- names the GHCR reference to run.

## v0.2.0-alpha.4 - 2026-09-08

### Changed

- Startup goes through `github.com/FreshLabDev/tg`, the client shared by the
  bot family. Searchy keeps `go-telegram/bot` for polling and handlers, but
  the 170 lines of hand-rolled HTTP around it -- getMe with its own retry
  ladder, deleteWebhook, endpoint building and two error types -- are gone,
  along with the knowledge that had been living only in this repository.
- `go-telegram/bot` moves from v1.21.0 to v1.25.0, which is the release that
  covers Bot API 10.3.
- `TELEGRAM_BOT_API_BASE_URL` is validated at startup. It used to be checked
  while building every request; the shared client takes it as given, so a typo
  now fails with a message naming the variable instead of a connection error on
  the first poll.

### Added

- `docs/releases.md` gained a **Deploying** section, and `AGENTS.md` points at it.
  Releasing was documented; deploying was not, in any repository in the family —
  the process stopped at "deploy it" and never said how. That gap mattered more
  after the stacks moved from building on the host to pulling a published image,
  because the procedure changed on the same day. The section names this stack's
  host directory, its env file, the variable that selects the image, the networks
  it needs, and what a rollback actually is.

- A startup preflight naming `answerInlineQuery` and `sendPhoto`. Searchy is
  an inline bot that answers with pictures, and a Bot API server without those
  would leave it polling and never answering.

### Security

- `golang.org/x/image` moves to v0.45.0. The advisory against v0.43.0 is
  reachable straight from `collage.Render` through the WebP decoder, and every
  image searchy decodes comes from a search result -- that is, from the
  internet.
- The Go floor moves to 1.26.6, build image included.

## v0.2.0-alpha.3 - 2026-08-09

### Fixed

- Retry the final Vido bridge commit after Telegram operations were confirmed,
  preventing transient Core errors from leaving delivered media leased.

### Operations

- Requires Core `v0.2.0-alpha.1` migration 008 and Vido
  `v2.3.7-alpha.5` before bridge intake resumes.

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
- Apache-2.0 license under Asterfield.
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
