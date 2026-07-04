# Search Integration

Searchy searches through a self-hosted **SearXNG** instance over its JSON API.
The provider lives in `internal/search/searxng` and implements the
`search.Provider` interface; the `search.Aggregator` wraps it with caching and
de-duplication.

## SearXNG Requirements

SearXNG's JSON API is **off by default**. The bot needs it on:

```yaml
search:
  formats: [html, json]   # without `json`, format=json returns HTTP 403
server:
  limiter: false          # fine for a private, internal-only instance
```

The bundled `deploy/searxng/settings.yml` already sets this. After editing
`settings.yml`, restart the container. A `403` from `format=json` almost always
means the JSON format is not actually enabled (or the service was not restarted)
— the bot surfaces that as a clear error rather than a generic failure.

> Do not point Searchy at a public SearXNG instance: they typically disable JSON
> and run bot detection. Self-host, and keep the instance internal-only.

## Querying

Each search issues one request:

```text
GET {SEARXNG_URL}/search?format=json&engines=<pinned>&pageno=<n>&safesearch=<0|1|2>&language=<lang>
```

Key choices baked into the client:

- **Pinned engines, not categories.** The bot sends a small `engines=` set and
  deliberately omits `categories`. `categories` is *additive* in SearXNG: it runs
  every enabled engine for the category regardless of `engines`, firing ~15
  engines at once and overloading a private instance's network layer (engines
  then fail with proxy errors). Restricting to a few named engines keeps queries
  fast and reliable. If no engines are configured for the requested categories,
  the client falls back to `categories`.
- **Engine sets are configurable** via `ENGINES_IMAGES` and `ENGINES_VIDEOS`
  (comma-separated). Defaults pin a handful of reliable image and video engines.
- **Pagination.** `pageno` is 1-based (the bot's 0-based page + 1). SearXNG does
  not report total pages reliably, so the provider reports "another page may
  exist" whenever the current page returned any raw results; the caller caps the
  page count.
- **Safe search** maps `SAFE_SEARCH` (0/1/2) to SearXNG's `safesearch`.
- **Language.** `LANGUAGE` sets SearXNG's `language`; `all` (the default) applies
  no language bias.
- **A browser-like User-Agent** reduces the chance of SearXNG's bot detection
  rejecting the request even on a private instance.

## Result Normalization

SearXNG emits an engine-dependent union of fields; every field is optional. The
provider normalizes each result into a flat `search.MediaResult`, keyed on a
short hash of the source URL (used as the stable Telegram inline result id).

### Images (`template: images.html`)

- Telegram fetches the photo URL server-side to send it, so it must be a
  reachable JPEG (≤ 5 MB). Origin `img_src` often fails this (hotlink 403, or
  huge — e.g. Wikimedia originals), so the provider uses the CDN `thumbnail_src`
  as the photo for most engines, keeping the higher-res origin only for engines
  with their own reliable CDN (`unsplash`, `flickr`).
- A result with no valid photo URL is dropped.

### Videos (`template: videos.html`)

- Rendered as a cover card, so the provider only needs a usable cover
  (`thumbnail` / `thumbnail_src`) and a page link (`url` / `iframe_src`). This
  deliberately accepts videos from **any** platform, not just those exposing an
  embeddable iframe.
- Duration is parsed from SearXNG's `length` field, which may be a number of
  seconds or a `M:SS` / `H:MM:SS` string.
- A result with no valid cover or page URL is dropped.

### Strict URL validation

Every inline media and button URL is validated as strict HTTPS (real host, no
control/unsafe characters, sane length) before use. Telegram rejects the **entire**
`answerInlineQuery` if even one result carries a malformed URL, so invalid results
are filtered out rather than risked.

## Aggregation

The `Aggregator` (`internal/search/aggregate.go`) sits between the bot and the
provider:

- **Cache.** An in-process LRU with TTL (`CACHE_SIZE` / `CACHE_TTL`) keyed on the
  normalized query, categories, and page. A hit returns instantly. Failures are
  not cached, so the next attempt retries.
- **De-duplication.** `singleflight` collapses identical concurrent searches into
  one backend call and hands the result to every waiter. The shared call is
  detached from any single caller's context, so an inline debounce cancel can't
  give an unrelated waiter a spurious empty result.
- **Post-processing.** Results are de-duplicated by id, then interleaved
  round-robin across engines so a single high-volume engine (e.g. bing/ddg, which
  flood junk for some queries) can't bury higher-quality curated results, and
  finally capped at `MAX_RESULTS`.

## Privacy and Logging

The query text is never logged. Degraded/unresponsive engines are surfaced in
logs (how a flaky or blocked engine is spotted) with counts only, never the
query. A cancelled request (the inline debouncer dropping a superseded query) is
treated as expected, not an error.
