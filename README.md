<h1 align="center">Searchy</h1>

<p align="center"><strong>Fast image &amp; video search in Telegram.</strong><br/>One Go bot with native metasearch, answering inline, in DMs, and in groups.</p>

<p align="center">
  <a href="https://github.com/FreshLabDev/searchy/releases"><img src="https://img.shields.io/github/v/release/FreshLabDev/searchy?include_prereleases&sort=semver&style=for-the-badge&label=latest&labelColor=0f172a&color=4c8c4a" alt="latest version"></a>
  <a href="docs/versioning.md"><img src="https://img.shields.io/badge/version-v0.2.3-4c8c4a?style=for-the-badge&labelColor=0f172a" alt="released version"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/FreshLabDev/searchy?style=for-the-badge&logo=go&logoColor=white&label=go&labelColor=0f172a&color=00ADD8" alt="go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-334155?style=for-the-badge&labelColor=0f172a" alt="license"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> ·
  <a href="#using-searchy">Using Searchy</a> ·
  <a href="#how-it-works">How It Works</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#privacy">Privacy</a> ·
  <a href="#docs">Docs</a>
</p>

---

## The Problem

Searching the open web for images and videos usually means leaving the chat,
opening a browser, and copying links back. Public search APIs are rate-limited,
paid, or tangled up with tracking, and public SearXNG instances disable the JSON
API and run bot detection.

Searchy keeps the MVP deliberately narrow:

| Need | Searchy approach |
|:--|:--|
| Search without leaving Telegram | Inline mode (`@bot query`) plus DM and group search |
| Images and videos only | Two categories, with `i:` / `v:` prefixes to narrow |
| Bounded search time | Per-user debounce, in-process cache, and incremental native retrieval |
| A backend you control | Native Go adapters for Bing, DuckDuckGo and Wikimedia; optional private SearXNG fallback |
| Privacy by design | Stores counts and timings only — **never** the query text |

> **Result:** one small Go service for in-chat image and video search, using
> external indexes without requiring an AI model or a separate search service.

---

## Status

| Channel | Version | Meaning |
|:--|:--|:--|
| Released | `v0.2.3` | Existing SearXNG-based release; see the changelog |
| Development | `Unreleased` | Native backend by default, with SearXNG fallback and switchback |

This checkout documents the native engine work on `dev`. It is not a new
published release or evidence of production latency. Native rollout still needs
test-bot verification of inline paging, DM/group search and Vido delivery. See
[`docs/versioning.md`](docs/versioning.md) for the version line and
[`CHANGELOG.md`](CHANGELOG.md) for release history.

---

## Features

| Surface | What it does |
|:--|:--|
| **Inline search** | Type `@bot query` in any chat; up to 50 results with thumbnails, paged as you scroll |
| **DM search** | Any text message runs a search and returns a single numbered result grid |
| **Group search** | `/search <query>` returns the same grid; anyone can page or open an item |
| **Category prefixes** | `i:` images only, `v:` videos only, otherwise both |
| **Fast mode** | `f:` is an explicit alias of normal non-AI search; combine with category prefixes in either order |
| **Video cards** | Cover + title with Open and Download buttons: the selector receives Searchy delivery, while another group member is handed off to a private Vido job |
| **Menu** | `/start` opens an inline panel: language, stats, help, about |
| **16 languages** | Full i18n; the chosen language is remembered across the sibling bots via **core** |
| **Private analytics** | Optional `/stats` panel (personal + global) built from counts only |

### Search prefixes

| You type | Searches |
|:--|:--|
| `cats` | images **and** videos |
| `i:cats` | images only |
| `v:cats` | videos only |
| `f:cats` | images **and** videos, same as `cats` |
| `f: v:cats` or `v: f:cats` | videos only, same as `v:cats` |

Prefixes are read only at the start and also work in `/search`. `f:` uses the
same sources and ranking as ordinary search. No AI mode is included.

---

## Quick Start

You need Docker and a Telegram bot token from
[BotFather](https://t.me/BotFather) with **inline mode enabled**
(`/setinline`) — without it, inline queries never reach the bot.

```sh
# 1. Copy local configuration
cp .env.example .env

# 2. Set BOT_TOKEN and a strong SEARXNG_SECRET (openssl rand -hex 32)
$EDITOR .env

# 3. Start the native bot and private SearXNG fallback
make up
make logs
```

`make up` runs `docker compose -f deploy/docker-compose.yml up -d --build`.
Native search runs inside the bot. The bundled SearXNG is reachable only on the
internal Docker network and has no host ports; it is retained temporarily for
fallback and rollback. Healthy native search does not query it.

For native-only local use, set `SEARCH_FALLBACK=none` in `.env`, then start only
the bot:

```sh
docker compose -f deploy/docker-compose.yml up -d --build --no-deps bot
```

Keep `SEARXNG_SECRET` set in `.env`: Compose resolves the whole manifest even
with `--no-deps`. This command does not stop an already-running SearXNG container.
To run Go directly without either database or SearXNG, export `BOT_TOKEN` and
`SEARCH_FALLBACK=none`, then run `make run`. `make run` does not load `.env`.

To switch back, set `SEARCH_BACKEND=searxng` and restart/recreate the bot. Keep
SearXNG running with JSON enabled; outside Compose, set `SEARXNG_URL` to your
private instance. See [Search integration](docs/search.md).

---

## Using Searchy

- **Inline (any chat).** Type `@yourbot query`. Searchy answers with image and
  video-cover cards; keep scrolling to load more pages. Late source results can
  appear on later pages, without changing cards already returned.
- **DM.** Send any text to search; the bot replies with one numbered grid image.
  Tap a number to pull that item full, or page through 10 at a time.
- **Group.** Add the bot and use `/search <query>` — plain messages are ignored
  so the bot stays quiet. The result grid's buttons are shared: anyone can page
  or open an item.
- **Menu.** `/start` opens an inline panel (language · stats · help · about). Each
  button edits the panel in place; only the user who opened it can drive it.

---

## How It Works

Search uses bounded temporary RAM state and writes no queries to disk. For a
selected video, Searchy creates an
owner-bound intent in the shared core database; Vido applies the user's personal
download settings and builds a safe DeliveryPlan. Searchy sends the ready media
as a separate message with its own bot token through the shared local Telegram
Bot API. If another group member presses the same card's Download button,
Searchy opens a newly owner-bound Vido DM deep link for that person without
posting anything else in the group. Inline download buttons use the same Vido
DM handoff because Telegram does not expose the chosen chat id to another bot.

Searchy is one Go service that long-polls Telegram and directly queries Bing
and DuckDuckGo for images/videos, plus Wikimedia for images. This is metasearch
over external indexes, not a crawler or an index of the whole web.

```text
Telegram -> bot (FreshLabDev/tg, long polling)
             -> debounce / cancel stale -> search.Backend
                                            |
                 native (default) <----------+
                    | -> Bing + DuckDuckGo + Wikimedia
                    | -> bounded result cache / page sessions
                    | -> optional private SearXNG fallback
                    |
                    +-> frozen inline page + opaque next_offset
                    +-> DM/group numbered grid

SEARCH_BACKEND=searxng -> existing aggregator -> private SearXNG JSON API
```

Key rules the code respects:

- Native returns up to **24** results per inline page and **50** per search
  session. `MAX_RESULTS` can lower the page size and caps the DM/group grid
  separately. Pagination uses opaque cursors
  within Telegram's 64-byte limit. Returned pages are stable on replay, and
  later results do not reorder cards already shown.
- Retrieval is incremental inside the engine. Telegram receives one answer per
  inline query, then later pages through `next_offset`; the bot cannot freely
  append to an already-answered inline picker as a web page could.
- Native first-page collection defaults to `800ms`, source work to `3.5s`, and
  the overall request, including fallback, to `8s`. A first engine page around
  0.5 to 1 second with healthy sources is a test target, not a measured production
  result or a guarantee of when Telegram displays thumbnails.
- When enabled, SearXNG fallback supplements weak/empty native results within
  the same deadline. Explicit switchback keeps its **pinned engine sets** and
  never sends `categories`.
- Inline media URLs must be **public HTTPS** (Telegram fetches them
  server-side). Inline photos and video covers require JPEG-compatible URLs;
  a thumbnail working in the HTML prototype does not prove Telegram can send
  it. Source normalization filters unsupported formats or uses a compatible
  provider preview. `IMAGE_PROXY` remains `false` for private SearXNG.
- Source failures can leave usable partial results. Degraded responses are not
  treated as healthy complete results in the five-minute result cache.

---

## Configuration

All configuration is through environment variables (see
[`.env.example`](.env.example)). Only `BOT_TOKEN` is required by the bot. The
default fallback needs reachable private SearXNG; set `SEARCH_FALLBACK=none` to
run without it. Compose also requires a SearXNG secret.

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `BOT_TOKEN` | yes | - | Telegram bot token from BotFather (inline mode must be enabled) |
| `SEARCH_BACKEND` | no | `native` | `native` direct metasearch or `searxng` for explicit switchback |
| `SEARCH_FALLBACK` | no | `searxng` | `searxng` or `none`; used only by the native backend |
| `NATIVE_TIMEOUT` | no | `3.5s` | Native source work budget |
| `NATIVE_FIRST_PAGE_WAIT` | no | `800ms` | Initial inline collection window; enough results may return sooner |
| `SEARCH_MAX_CONCURRENT` | no | `8` | Global limit on concurrent native searches |
| `SEARXNG_URL` | no | `http://localhost:8080` | Fallback/switchback URL (Compose overrides to `http://searxng:8080`) |
| `SEARXNG_SECRET` | compose | - | Secret for bundled private SearXNG; also needed to resolve native-only Compose startup |
| `SEARXNG_IMAGE` | no | pinned `2026.6.24` digest | Proven immutable SearXNG image; override only after engine smoke |
| `ENGINES_IMAGES` | no | Bing, DuckDuckGo | SearXNG-only pinned core image engines |
| `ENGINES_VIDEOS` | no | YouTube, DuckDuckGo, Dailymotion, Bing Videos | SearXNG-only pinned core video engines |
| `ENGINES_IMAGES_DISCOVERY` | no | FindThatMeme, Pinterest, Giphy, Frinkiac, Wikimedia | SearXNG-only long-tail image engines |
| `ENGINES_VIDEOS_DISCOVERY` | no | Bilibili, PeerTube | SearXNG-only long-tail video engines |
| `SAFE_SEARCH` | no | `0` | Both backends: `0` off / `1` moderate / `2` strict |
| `IMAGE_PROXY` | no | `false` | SearXNG-only proxy; keep off unless SearXNG is public HTTPS |
| `LANGUAGE` | no | empty | Both backends: operator override; empty uses the user's saved language |
| `REQUEST_TIMEOUT` | no | `8s` | Overall search budget including optional fallback |
| `MAX_RESULTS` | no | `50` | Inline answer cap (Telegram hard limit is 50) |
| `DEBOUNCE_DELAY` | no | `150ms` | Inline debounce per user |
| `DISCOVERY_PERCENT` | no | `30` | SearXNG-only long-tail share for healthy core results |
| `DISCOVERY_WEAK_PERCENT` | no | `50` | SearXNG-only long-tail share for weak core results |
| `CACHE_TTL` | no | `5m` | Bounded in-memory result/session lifetime |
| `CACHE_SIZE` | no | `2048` | Native retained-result budget (including frozen pages); legacy cache entries |
| `WORKERS` | no | `32` | Update worker pool size |
| `INLINE_CACHE_TIME` | no | `300` | `answerInlineQuery` `cache_time` (seconds) |
| `STATS_CACHE_TTL` | no | `10m` | Stats snapshot cache TTL (floored at `10s`) |
| `HEALTH_ADDR` | no | `:8081` | Health endpoint listen address |
| `DATABASE_URL` | no | - | Postgres for private search analytics; empty disables `/stats` |
| `CORE_DATABASE_URL` | no | - | Shared cross-bot **core** Postgres (identity, presence, language); empty falls back to the Telegram hint |
| `VIDO_BOT_USERNAME` | with bridge | - | `@username` (no `@`) of the Vido bot |
| `VIDO_BRIDGE_ENABLED` | no | `false` | Enable owner-bound Vido intents and the Searchy delivery worker |
| `TELEGRAM_BOT_API_BASE_URL` | no | cloud API | Shared local Bot API base URL |
| `SHARED_MEDIA_CACHE_DIR` | no | `/shared-media-cache` | Read-only shared artifact root |

---

## Database

Searchy uses **two** Postgres connections, and **both are optional** — the bot
runs fine with neither. Search always works; only `/stats` and the cross-bot
saved language depend on them.

- **Search analytics** — `DATABASE_URL`. Holds only private analytics: counts
  and timings of searches and selections, **never** the query text. The schema
  is applied on startup and there is **no `users` table** — identity moved to
  core. In production this points at the shared **core** Postgres as the
  least-privilege role `searchy_core` with `search_path=searchy`, so the
  analytics tables live in a `searchy` schema there.
- **Shared `core` DB** — `CORE_DATABASE_URL`. In production both Searchy
  connections point to the same `core-postgres` instance with different
  `search_path` and privilege boundaries. Vido, Searchy, Quoto, Branchy, and
  makeitMD share the global Telegram identity hub. Searchy identifies
  itself as `searchy` and touches core only through its SECURITY DEFINER
  functions (`core.touch`, `core.set_language`, `core.effective_language`).

See [`docs/architecture.md`](docs/architecture.md) for the full two-database
model.

---

## Privacy

- The **query text is never stored** — not in analytics, not in logs, not in the
  in-memory grid sessions. Only counts, timings, category, result type, and the
  originating source/engine are recorded.
- Native page sessions use a process-local HMAC query identity, never raw
  query text. They expire, have a size bound, and do not survive a restart.
- Analytics rows key on the Telegram user id with no query, title, or URL.
- Both databases are optional; with neither configured, nothing is persisted.

---

## Deployment

Searchy makes only outbound connections (long polling to
Telegram, native search sources and optional private SearXNG), so **no public
ports are required**.
The image runs on `gcr.io/distroless/static-debian12:nonroot`.

`/healthz` on `HEALTH_ADDR` (default `:8081`) reports `ok` plus the build
version, commit, and date without exposing secrets. The container healthcheck
probes it via the binary's `-healthcheck` flag (the distroless image has no
shell or curl).

---

## Testing

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go test ./...
docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go vet ./...
docker compose -f deploy/docker-compose.yml config
```

---

## Docs

| Document | Purpose |
|:--|:--|
| [Architecture](docs/architecture.md) | Service structure, packages, and the two-database model |
| [Search integration](docs/search.md) | Native sources, pagination, media compatibility, fallback and switchback |
| [Telegram behavior](docs/telegram.md) | Commands, inline/DM/group search, and the menu |
| [Versioning](docs/versioning.md) | Pre-release and stable version line |
| [Release process](docs/releases.md) | Changelog and GitHub Release rules |

---

<p align="center">
  <a href="https://github.com/FreshLabDev/searchy/releases">Releases</a> ·
  <a href="CHANGELOG.md">Changelog</a> ·
  <a href="LICENSE">Apache-2.0</a> ·
  <a href="NOTICE">NOTICE</a>
</p>

<p align="center">
  Searchy is open source software by Asterfield.<br/>
  Copyright 2026 Asterfield.
</p>
