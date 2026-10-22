# Search Integration

Searchy defaults to native Go metasearch over external indexes. The existing
SearXNG aggregator is retained behind the same `search.Backend` contract for
optional fallback and explicit switchback. There is no AI model, Python search
service, or new database in this implementation.

## Backend selection and startup

- `SEARCH_BACKEND=native` (default): direct native sources, with
  `SEARCH_FALLBACK=searxng` (default) for bounded weak/empty-result fallback.
- `SEARCH_FALLBACK=none`: native alone, with no SearXNG requests.
- `SEARCH_BACKEND=searxng`: the existing SearXNG aggregator owns the search;
  native sources and their fallback setting are not involved.

Local Compose starts the bot and the private fallback together. Keep its
SearXNG image, settings mount and network so switchback is available. For a
native-only local run, set `SEARCH_FALLBACK=none` in `.env` and run from the
repository root:

```sh
docker compose -f deploy/docker-compose.yml up -d --build --no-deps bot
```

Compose resolves the complete manifest, so `.env` and `SEARXNG_SECRET` are still
needed even if the SearXNG service is not started. This does not stop an existing
fallback container. Direct `make run` reads exported environment variables, not
`.env`; with fallback disabled only `BOT_TOKEN` is required for search.

To switch back, set `SEARCH_BACKEND=searxng`, ensure the private SearXNG instance
is running with JSON enabled, and recreate/restart the bot so it reads the new
environment. Outside Compose, set `SEARXNG_URL` to the instance. Deployment and
image-digest rollback still follow [the existing release process](releases.md).
These local startup commands are not production deployment instructions.

## Native sources and controls

- Bing and DuckDuckGo provide direct image and video results.
- Wikimedia Commons adds images when `SAFE_SEARCH=0`. It has no equivalent
  adult-content filter, so it is excluded for moderate/strict searches.
- `SAFE_SEARCH` keeps `0` off, `1` moderate and `2` strict; adapters translate
  this policy into source-specific settings.
- Effective search language follows the saved user language or Telegram hint.
  An explicit operator `LANGUAGE` override applies to native as well as SearXNG;
  adapters translate locales into each source's parameters.
- `ENGINES_*`, `DISCOVERY_PERCENT`, `DISCOVERY_WEAK_PERCENT` and `IMAGE_PROXY`
  are SearXNG-only settings. Native uses its own source set and ranking.

`f:` is an explicit alias for normal fast search with no AI and the same sources
and ranking. Leading `i:`/`v:` category prefixes can appear before or after it.
For example, `f: v: red panda` and `v: f: red panda` equal `v: red panda`.
Plain `red panda` searches both categories. Prefixes also work in `/search`;
text inside the query is not rewritten. There is no `ai:` mode or AI toggle.

Direct adapters depend on source HTML/JSON structure, token acquisition and
upstream access policy. Markup changes, throttling and challenges can reduce or
break a source even when Searchy is healthy. Use saved public response fixtures
to check parsers, and query-free source errors, timing and usable-result counts
to diagnose failures. Do not log request URLs, tokens or upstream bodies that
can contain query text.

## Incremental retrieval and stable pagination

The engine collects sources in parallel and ranks normalized, de-duplicated
results using title coverage, source position, cross-source consensus and site
and category diversity. It waits a short initial window rather than blocking a
usable first page on every source.

Once returned, an inline page is frozen. Late results enter the unreturned pool
and can be ranked there, but cannot reorder or repeat cards already returned.
Scrolling consumes that pool and, when needed, asks sources for continuation.
Source exhaustion is distinct from a transient failure.

The production native pool holds at most 50 results. Inline pages contain up to
`min(MAX_RESULTS, 24)` results; a smaller page limit does not truncate the whole
pool. DM/group search obtains at most `MAX_RESULTS` results in one grid.

Native `next_offset` is an opaque cursor within Telegram's 64-byte limit. It is
bound to the user, a process-local HMAC query identity, language, categories and
filters. Replaying it returns the same page. Invalid, mismatched or expired
cursors must not silently open a different search. Sessions are temporary and
expire on TTL, eviction or restart; a fresh query starts over.

Telegram receives one `answerInlineQuery` per inline request and asks for later
pages using `next_offset`. This is incremental retrieval inside the engine,
not an ability to push additions into an already-answered picker. DM/group
search obtains a bounded complete result pool for the existing numbered grid;
its paging, selection and Vido behavior stay the same.

## Budgets, cache and degradation

| Setting | Default | Purpose |
|:--|:--|:--|
| `NATIVE_FIRST_PAGE_WAIT` | `800ms` | Initial inline collection window; enough results may return earlier |
| `NATIVE_TIMEOUT` | `3.5s` | Native source work budget |
| `REQUEST_TIMEOUT` | `8s` | Overall search budget including any fallback |
| `SEARCH_MAX_CONCURRENT` | `8` | Concurrent native search limit |
| `CACHE_TTL` | `5m` | Default in-memory result/session lifetime |
| `CACHE_SIZE` | `2048` | Native retained-result budget including frozen pages; legacy cache entries |

A first engine page around 0.5 to 1 second with healthy sources is a test target,
not a measured production result or a guarantee of Telegram display time. Handler
work, Vido-link preparation and Telegram's own media fetch add separate costs.

Fallback is optional and bounded by the remaining overall deadline. It is not
queried during healthy native search and cannot change an already-returned page.
A source failure may still leave useful partial results. Keep partial/degraded
status explicit; do not put failed or degraded output into the five-minute cache
as a healthy complete response. A frozen session page can still be replayed
without claiming all sources succeeded.

Result/session caches are bounded in RAM by size and time. Raw query text is
transient input, never retained in native sessions, analytics or logs; a
process-local HMAC identifies a search. Cancelling one waiter must not cancel
shared work needed by others. Late retrieval is short and bounded, and obsolete
work with no consumers is cancelled. Global, per-user and per-source limits
prevent slow upstreams from creating unlimited work.

## Telegram media compatibility

Native normalization lives in `internal/search/native/media.go`, before results
reach the bot. It validates public HTTPS URLs, removes unsupported media formats
and chooses an acceptable provider preview when the original cannot be used.
Known unsupported photo formats such as SVG, GIF, WebP, AVIF and PNG are not sent
as `InlineQueryResultPhoto.photo_url`. A JPEG original is used only when its
advertised dimensions fit the photo constraints; otherwise a compatible preview
is preferred. Video cards use their JPEG cover, retaining the watch-page URL
for the Open button and the existing Vido download flow.

The laboratory HTML server in `searchy-lab` fetches, decodes, resizes and
re-encodes thumbnails as JPEG through its local `/media/` endpoint. A browser can
also display formats that Telegram's inline-photo API does not accept. Neither
a working HTML preview nor a `.jpg` suffix proves Telegram can fetch and send
the actual bytes. Production does not copy the lab media proxy or AI service:
Telegram fetches the selected public photo/cover URL directly. Check real card
selection and delivery in Telegram before release.

One malformed inline media/button URL can reject the whole answer, so strict
HTTPS validation remains mandatory. Any server-side media downloading must
reject private addresses and unsafe redirects and enforce size limits. Keep
SearXNG's proxy disabled while that instance is private.

## SearXNG fallback and explicit switchback

The following engine pools, discovery quotas, ranking weights and locale fallback
apply only to the legacy SearXNG path. They do not configure native sources.

### Instance requirements

The bundled configuration enables JSON, keeps the instance internal, disables
the public limiter, uses direct egress and pins the proven `2026.6.24` production
image by immutable digest. The `2026.7.16` candidate is intentionally not used:
DuckDuckGo Images returned access-denied failures after benchmark load.

```yaml
server:
  limiter: false
  public_instance: false
  image_proxy: false
  method: POST
search:
  safe_search: 0
  formats: [html, json]
```

Do not expose this limiter-free instance or point Searchy at a public instance.
Telegram fetches media itself, so `IMAGE_PROXY` stays off while SearXNG is
internal-only.

### Requests and pools

Searchy sends form-encoded `POST /search` with `q`, `format=json`, `engines`,
`pageno`, `safesearch` and `language`. Query text therefore does not appear in
access URLs. `categories` is never sent because it would fan out to every engine
in the category.

For requested categories, core and discovery pools run in parallel:

- Core images: Bing and DuckDuckGo.
- Core videos: YouTube, DuckDuckGo Videos, Dailymotion and Bing Videos.
- Discovery images: FindThatMeme, Pinterest, Giphy, Frinkiac and Wikimedia.
- Discovery videos: Bilibili and PeerTube.

Core lists use `ENGINES_IMAGES` and `ENGINES_VIDEOS`; discovery lists use
`ENGINES_IMAGES_DISCOVERY` and `ENGINES_VIDEOS_DISCOVERY`. Missing pinned lists
are rejected before contacting SearXNG.

The saved user language is passed per request and is part of the cache key.
Chinese maps to SearXNG's search locale `zh-CN`; the other supported Searchy
codes map directly.
An explicit `LANGUAGE` environment value overrides all users. When a non-English
core response is weak, Searchy adds one English core request. It does not run a
second discovery request.

### Legacy normalization

SearXNG `score`, `positions` and every confirming `engine` are preserved.
Dimensions come from `width` and `height`, with `resolution` as fallback.

Image results prefer a reliable HTTPS thumbnail. Trusted origin CDNs may use the
higher-resolution original. Video cards require an HTTPS cover and page URL.
Candidate URLs are checked in order, protocol-relative URLs become HTTPS, and
plain HTTP is upgraded only for an explicit Bilibili/CDN allowlist. The final
strict HTTPS validation remains mandatory because one malformed URL can make
Telegram reject the complete inline answer.

### Legacy ranking

Duplicates are merged by stable URL hash. The ranking score is:

- 55% normalized SearXNG score or reciprocal-rank fusion, whichever is higher;
- 25% query-token coverage in the title, with a small ordered-phrase boost;
- 15% consensus across up to three engines;
- 5% media dimensions and quality.

Equal scores retain SearXNG order and then use the stable result ID. Mixed search
keeps a 50/50 image/video balance while results exist in both categories. The
discovery target is 30%, rising to 50% for a weak core category. Weakly matching
discovery items are moved below the first page instead of displacing relevant
core results. In the first ten, each host is capped at two. The cap relaxes one
result at a time when the only alternative would replace a strongly matching
video with unrelated cross-host noise. Later results use a 40% host cap that is
also relaxed only when needed.

A core category is weak when it has fewer than ten unique results, fewer than
three relevant titles in its top ten, more than 60% of its top ten from one host,
or fewer than twenty results alongside degraded engines.

### Legacy cache, failures and privacy

The LRU key contains normalized query, categories, page and language.
`singleflight` collapses identical concurrent work. Transport failures are never
cached. If one primary pool fails, the other can still answer; two failures
produce the existing empty result behavior.

Logs contain pool, locale, timing, raw/usable counts and degraded-engine state,
but never query text. Analytics remain unchanged: counts, timings, category,
result type and the primary engine only. No title, URL or query is persisted.

## Synthetic benchmark

`cmd/searchbench` uses 12 exact, 12 rare or meme-oriented, and 12 multilingual
synthetic queries. It reads no production history and emits JSON to stdout.
Use native alone without SearXNG, or compare its first page with the current
SearXNG aggregator:

```sh
go run ./cmd/searchbench -mode native -summary-only

go run ./cmd/searchbench \
  -url http://localhost:8080 \
  -mode compare-native \
  -summary-only
```

`native` and `compare-native` measure the first engine page, capped at 24 results,
with cold retrieval state per distinct case and no native-to-SearXNG fallback.
They exclude debounce, Telegram handler work, Vido-link preparation and client
thumbnail loading. `-mode compare` still compares the historical `v0.1.0`
SearXNG interleave with the current SearXNG aggregator, without native retrieval.

The report separates exact, rare and multilingual groups and includes keyword
relevance@10, discovery count@20, host diversity, usable HTTPS ratio, empty
count, failed/degraded counts, median, p95 and maximum latency. Keyword relevance is a title-matching heuristic,
not an assessment of actual visual relevance. Native case reports also expose
degraded status and whether a later page is available. Run without
`-summary-only` to inspect top-ten titles and media/source URLs; actual Telegram paging, image delivery
and the DM/group/topic/Vido matrix still need test-bot verification before release.
