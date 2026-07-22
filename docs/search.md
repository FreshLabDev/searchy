# Search Integration

Searchy uses a private self-hosted SearXNG instance as a bounded media-search
backend. The provider normalizes engine-specific JSON; the aggregator owns
caching, pool orchestration, fallbacks, de-duplication and ranking.

## SearXNG requirements

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

## Requests and pools

Searchy sends form-encoded `POST /search` with `q`, `format=json`, `engines`,
`pageno`, `safesearch` and `language`. Query text therefore does not appear in
access URLs. `categories` is never sent because it would fan out to every engine
in the category.

Each search starts two explicit requests in parallel:

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

## Normalization

SearXNG `score`, `positions` and every confirming `engine` are preserved.
Dimensions come from `width` and `height`, with `resolution` as fallback.

Image results prefer a reliable HTTPS thumbnail. Trusted origin CDNs may use the
higher-resolution original. Video cards require an HTTPS cover and page URL.
Candidate URLs are checked in order, protocol-relative URLs become HTTPS, and
plain HTTP is upgraded only for an explicit Bilibili/CDN allowlist. The final
strict HTTPS validation remains mandatory because one malformed URL can make
Telegram reject the complete inline answer.

## Ranking

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

## Cache, failures and privacy

The LRU key contains normalized query, categories, page and language.
`singleflight` collapses identical concurrent work. Transport failures are never
cached. If one primary pool fails, the other can still answer; two failures
produce the existing empty result behavior.

Logs contain pool, locale, timing, raw/usable counts and degraded-engine state,
but never query text. Analytics remain unchanged: counts, timings, category,
result type and the primary engine only. No title, URL or query is persisted.

## Synthetic benchmark

`cmd/searchbench` compares the `v0.1.0` engine interleave with the candidate on
12 exact, 12 rare or meme-oriented, and 12 multilingual synthetic queries. It
reads no production history and writes no files; JSON is emitted to stdout.

```sh
go run ./cmd/searchbench \
  -url http://localhost:8080 \
  -mode compare \
  -summary-only
```

The report separates exact, rare and multilingual groups and includes keyword
relevance@10, discovery count@20, host diversity, usable HTTPS ratio, empty
count, p95 and maximum latency. Run without `-summary-only` to manually inspect
the top-ten titles before a release.
