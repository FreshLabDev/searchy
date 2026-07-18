# Architecture

Searchy is one Go service with three runtime surfaces:

- Telegram long polling for user interaction (inline, DM, and group search).
- An HTTP client that queries a self-hosted SearXNG instance over its JSON API.
- A small health server for container orchestration.

The service holds no durable state of its own beyond optional analytics. Search
is stateless: each query is answered from SearXNG (through an in-process cache),
and results are never written to disk. Two logical Postgres connections are
used, and both are optional — the bot runs fine with neither. Production points
both at one shared `core-postgres` instance: the analytics connection is
restricted to schema `searchy`, while the core connection exposes only the
shared functions and bridge API granted to `searchy_core`.

## Packages

- `cmd/bot`: process wiring, startup (`getMe` token check, `deleteWebhook`,
  command-menu registration), the `/healthz` server, and graceful shutdown.
- `internal/config`: environment parsing with defaults for every value.
- `internal/httpx`: one tuned `*http.Client` shared by the SearXNG provider (not
  used for Telegram long polling, whose held-open connections need different
  timeouts).
- `internal/search`: the media-search domain model (`MediaResult`), the
  `Provider` interface, and the `Aggregator` (LRU+TTL cache, `singleflight`
  de-duplication, round-robin engine interleave, result cap).
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
3. The `Aggregator` serves a cache hit instantly, or collapses concurrent
   identical searches into one SearXNG call with `singleflight`.
4. The SearXNG provider issues one JSON request with a pinned engine set,
   normalizes results, and reports whether another page likely exists.
5. Results are mapped to `InlineQueryResultPhoto` cards (images, and video covers
   with an "Open" button), capped at 50, and returned via `answerInlineQuery`
   with a `next_offset` page index and `cache_time`.

### DM / group search

1. A text message in a private chat (or `/search <query>` in a group) triggers a
   search over the same aggregator.
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
longer needs a standalone database. Local development can use a bundled Postgres
for analytics and skip core entirely.

## Decisions

- Long polling keeps local development simple and needs no public routes; the
  bot makes only outbound connections.
- Search is stateless and cache-fronted. The `Aggregator` detaches the shared
  backend call from any single caller's context, so an inline debounce cancel
  cannot hand an unrelated waiter a spurious empty result.
- SearXNG is queried with a pinned `engines=` set and **never** `categories`,
  which is additive in SearXNG and would fan out to every enabled engine and
  overload a private instance.
- Inline media and button URLs are validated as strict HTTPS before answering,
  because a single malformed URL makes Telegram reject the entire inline answer.
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
restart does not lose the specific error.

The bridge pool is initialized without requiring core-postgres to be reachable
at that exact startup instant. pgx reconnects lazily; health stays `degraded`
while core is down and returns to `ok` without restarting Searchy.
