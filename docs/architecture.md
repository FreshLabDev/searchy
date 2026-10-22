# Architecture

Searchy is one Go service with three runtime surfaces:

- Telegram long polling for user interaction (inline, DM, and group search).
- Native HTTP adapters for Bing and DuckDuckGo images/videos, plus Wikimedia
  images; a private SearXNG JSON backend remains available for fallback/rollback.
- A small health server for container orchestration.

Search has no durable state: results and pagination sessions live only in
bounded, expiring RAM caches, and raw query text is never retained in sessions,
logs or databases. The bot needs no AI model, Python search service, or new
database. Two existing logical Postgres connections are optional. Production points
both at one shared `core-postgres` instance: the analytics connection is
restricted to schema `searchy`, while the core connection exposes only the
shared functions and bridge API granted to `searchy_core`.

## Packages

- `cmd/bot`: process wiring, startup (`getMe` token check, `deleteWebhook`,
  command-menu registration), the `/healthz` server, and graceful shutdown.
- `internal/config`: environment parsing with defaults for every value.
- `internal/httpx`: tuned outbound search HTTP transport. Telegram long polling
  stays in `FreshLabDev/tg`, whose held-open connections need different timeouts.
- `internal/search`: the media-search domain model (`MediaResult`), shared
  `Backend`/`PageRequest`/`Page` contract, source continuation contract, and
  `LegacyBackend` adapter for the existing `Aggregator`. The latter retains
  SearXNG ranking, cache, `singleflight`, discovery quotas and engine pools.
- `internal/search/native`: direct source adapters, media normalization and
  relevance ranking.
- `internal/search/stream`: incremental retrieval, cross-page de-duplication,
  concurrency limits and bounded cursor/session state behind `search.Backend`.
- `internal/search/searxng`: the SearXNG JSON provider for the images and videos
  categories, including field normalization and the engine-pinning logic.
- `internal/collage`: composes a page of covers into one numbered grid JPEG for
  DM/group search.
- `internal/bot`: the Telegram layer — the update router, inline handler,
  DM/group grid handler, the `/start` menu, per-user debounce, rendering, and
  analytics/language helpers.
- `internal/db`: Searchy's logical Postgres schema for private search analytics
  (searches and selections) — no query text, no users table. Production hosts
  it inside the shared physical `core-postgres` instance.
- `internal/core`: a nil-safe client for the shared cross-bot **core** Postgres
  (identity, presence, language) keyed on the global Telegram id.
- `internal/i18n`: user-facing strings (16 languages) with `{placeholder}`
  interpolation and English fallback.
- `internal/vido`: least-privilege core-postgres bridge client plus strict
  `DeliveryPlan v1` validator.
- `internal/buildinfo`: build-time version metadata stamped at link time.

## Data Flow

### Inline search (the primary surface)

1. A user types `@bot query` in any chat; Telegram delivers an `inline_query`.
2. The handler debounces per user, cancelling a superseded query so only the
   latest keystroke reaches the backend.
3. The shared backend contract receives transient query text, categories,
   effective language, user/scope, result limit and optional cursor. Native is
   the default; `SEARCH_BACKEND=searxng` selects the legacy adapter.
4. Native queries eligible Bing, DuckDuckGo and Wikimedia sources concurrently.
   It normalizes media, removes duplicates and ranks the available results by
   title coverage, source position, consensus and site diversity.
5. A short collection window allows the first usable page to return without
   waiting for every source. Weak or empty results can trigger bounded private
   SearXNG fallback if enabled, within the same overall deadline.
6. Results are mapped to `InlineQueryResultPhoto` cards (images, and video covers
   with Open/Download buttons), capped at `min(MAX_RESULTS, 24)` per native page
   and 50 per native session. The returned page is frozen, with
   an opaque native `next_offset` for later pages. A repeated cursor returns the
   same cards in the same order.
7. Bounded late retrieval can add results to the unreturned pool. Scrolling
   consumes that pool and may request source continuation. It never reorders
   returned cards or pushes edits into an already-answered inline picker.

The default budgets are `NATIVE_FIRST_PAGE_WAIT=800ms`, `NATIVE_TIMEOUT=3.5s`,
and `REQUEST_TIMEOUT=8s` including fallback, with `SEARCH_MAX_CONCURRENT=8`.
The first-page engine target of roughly 0.5 to 1 second with healthy sources is
not measured production latency and does not include Telegram's media fetch or
all handler/Vido-link work. See [Search integration](search.md) for failure,
cache and media constraints.

### DM / group search

1. A text message in a private chat (or `/search <query>` in a group) triggers a
   search through the same backend contract, with a bounded complete result pool
   for the existing grid rather than incremental edits to its item list.
2. The first page of covers is downloaded concurrently and composed into a single
   numbered grid JPEG, posted with paging and per-item buttons.
3. Paging edits the grid image in place; tapping a number sends that item full
   (an image as a photo, a video as a cover card).

### Identity, presence, and analytics (best-effort, off the hot path)

- Every interaction upserts identity + presence + the Telegram language hint into
  **core** via `core.touch`, in a background goroutine.
- An answered search records counts and timing in the analytics store; a picked
  result records its type and engine. The query text is never recorded.

## The Two-Database Model

Searchy separates its **own private analytics** from the **shared identity hub**:

- **Analytics DB (`DATABASE_URL`)** — searchy's own tables (`searches`,
  `selections`). Counts and timings only, keyed on the raw Telegram user id with
  no foreign keys and no query text. The schema is applied on startup and is
  idempotent (it also drops any legacy `users` table). Empty disables `/stats`.
- **Core DB (`CORE_DATABASE_URL`)** — the shared cross-bot **core** Postgres, one
  instance for searchy, vido, quoto, and branchy, keyed on the global Telegram
  id. It owns identity, presence, and the user's saved language. Searchy connects
  with a least-privilege role (`searchy_core`) and only ever calls core's
  SECURITY DEFINER functions; it does not touch core tables directly. Empty
  disables core integration and language falls back to the Telegram hint.

In production, both URLs point at the same shared `core-postgres`: analytics
lives in a `searchy` schema there (via `search_path=searchy`), so searchy no
longer needs a standalone database. Local development can use an existing
Postgres for analytics and skip core entirely. The local Compose manifest does
not create a database, and native search does not add one.

## Decisions

- Long polling keeps local development simple and needs no public routes; the
  bot makes only outbound connections.
- Search state is temporary and bounded by time and size. Native cursor identity
  binds user, query HMAC, language, categories and filters; expired/invalid
  cursors are explicit. No raw query is stored in page sessions. Restarting the
  bot expires them.
- Cancelling one caller must not cancel shared work that another caller needs.
  After an inline answer, collection continues only within its bounded budget;
  superseded work without remaining consumers is cancelled.
- Partial/degraded results remain distinguishable from complete healthy results
  and must not poison the five-minute healthy cache.
- The private SearXNG fallback and explicit switchback use pinned `engines=`
  sets and **never** `categories`,
  which is additive in SearXNG and would fan out to every enabled engine and
  overload a private instance.
- Inline media and button URLs are validated as strict HTTPS before answering,
  because a single malformed URL makes Telegram reject the entire inline answer.
- Native source normalization filters unsupported media formats and uses
  compatible provider previews when required. Telegram fetches actual JPEG
  photo/cover URLs; the lab's browser thumbnail converter is not a production
  proxy or evidence of Telegram delivery. Server-side media fetching must reject
  private addresses and unsafe redirects and enforce response size limits.
- Native adapters depend on source markup/API responses. Fixture-based parser
  tests and query-free source errors/timings are needed to detect changes.
- Both databases are nil-safe: an unset or unreachable store makes every call a
  harmless no-op, so search keeps working and language reads degrade to the
  Telegram hint.
- Writes to core go only through its SECURITY DEFINER functions, so searchy holds
  the minimum privilege needed and cannot corrupt shared identity data.
- Menu callbacks are ownership-scoped (`m:<owner>:<action>`); grid callbacks are
  intentionally shared so any group member can drive a posted result grid.
- The query text is never retained — not in analytics, logs, or the in-memory
  grid sessions (which hold already-normalized media URLs, not what was typed).
- Stats are served from a short-lived snapshot cache and refreshed in the
  background, so toggling the personal/global tabs costs no extra queries.

## Vido bridge

Video cards in DM/group search receive an owner-bound `vd:` callback. Migration
004 creates the bridge and hashed intents, migration 005 makes send/ACK and
terminal notifications durable, and migration 006 adds the non-selector group
handoff. Searchy binds the intent to the sent card and asks Vido's standalone
worker to process the source with the user's personal Vido settings. Searchy
receives no source URL or settings back: it claims a versioned DeliveryPlan,
validates its operation/path whitelist, and sends a new message in the original
chat/topic with its own token.

Migration 006 makes a bound group card shareable without making its token
publicly consumable. The selector still receives Searchy delivery in the group;
another user receives a new personal Vido DM intent through
`answerCallbackQuery.url`. Core validates the exact card chat/message and binds
the derived token to that user. Searchy never receives the copied source URL,
and no handoff message is posted in the group.

Inline cards stay cover-only and personal. Their Download button is a Vido
`/start ia_...` deep link because Telegram does not expose the chosen chat id to
Vido. The Vido flow uses chat actions without a visible starting message.

Vido alone writes the temporary shared cache. Searchy and the independent local
Bot API mount it read-only at `/shared-media-cache`; Searchy passes local file
URIs to the Bot API and ACKs each media/album/sidecar operation. The last lease
release lets Vido delete the physical files, while bot-specific Telegram
`file_id` records remain durable.

Before a Telegram call, Searchy records the operation as `sending`. A confirmed
ACK is monotonic and cannot later be replaced by a timeout or failure. If the
process dies after Telegram may have accepted the media, lease recovery marks
the delivery unknown and offers only the card owner an explicit retry warning
about a possible duplicate. Failed downloads are delivered through a durable
notification outbox using Vido's structured `user_message_key`, so a Searchy
restart does not lose the specific error. Searchy retries both the operation ACK
and the final bridge commit on short Core failures without resending Telegram
media.

The bridge pool is initialized without requiring core-postgres to be reachable
at that exact startup instant. pgx reconnects lazily; health stays `degraded`
while core is down and returns to `ok` without restarting Searchy.
