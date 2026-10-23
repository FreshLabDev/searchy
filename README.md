<h1 align="center">searchy</h1>

<p align="center"><strong>Fast image &amp; video search in Telegram.</strong><br/>One Go bot with native metasearch, answering inline, in DMs, and in groups.</p>

<p align="center">
  <a href="https://github.com/FreshLabDev/searchy/releases"><img src="https://img.shields.io/github/v/release/FreshLabDev/searchy?include_prereleases&sort=semver&style=for-the-badge&label=latest&labelColor=0f172a&color=4c8c4a" alt="latest version"></a>
  <a href="docs/versioning.md"><img src="https://img.shields.io/badge/version-v0.2.3-4c8c4a?style=for-the-badge&labelColor=0f172a" alt="released version"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/FreshLabDev/searchy?style=for-the-badge&logo=go&logoColor=white&label=go&labelColor=0f172a&color=00ADD8" alt="go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-334155?style=for-the-badge&labelColor=0f172a" alt="license"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> ·
  <a href="#using-searchy">Using searchy</a> ·
  <a href="#how-it-works">How It Works</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#privacy">Privacy</a> ·
  <a href="#docs">Docs</a>
</p>

---

## The Problem

Finding an image or a video for a chat usually means leaving Telegram, opening a
browser and copying a link back. Public search APIs are rate-limited or paid,
and public SearXNG instances turn off their JSON API.

searchy queries image and video sources itself and answers right in the chat:

| Need | searchy approach |
|:--|:--|
| Search without leaving Telegram | Inline mode (`@bot query`) plus DM and group search |
| Images and videos only | Two categories, with `i:` / `v:` prefixes to narrow |
| A quick first page | The first page goes out as soon as it is useful; slower sources fill the next pages |
| No search service to run | Go adapters for Bing, DuckDuckGo and Wikimedia; private SearXNG is an optional fallback |
| Privacy by design | Stores counts and timings only — **never** the query text |

> **Result:** one Go service for image and video search inside Telegram.

---

## Status

| Channel | Version | Meaning |
|:--|:--|:--|
| Stable | `v0.2.3` | Existing SearXNG-based release; see the changelog |
| Pre-release | `v1.0.0-alpha.1` | Native backend by default, with SearXNG fallback and switchback |

The alpha runs on the main searchy instance for testing. Inline paging, DM and
group search and vido delivery have not yet been checked live in Telegram. See
[`docs/versioning.md`](docs/versioning.md) for the version line and
[`CHANGELOG.md`](CHANGELOG.md) for release history.

---

## Features

| Surface | What it does |
|:--|:--|
| **Inline search** | Type `@bot query` in any chat; results with thumbnails, more load as you scroll |
| **DM search** | Any text message runs a search and returns a single numbered result grid |
| **Group search** | `/search <query>` returns the same grid; anyone can page or open an item |
| **Category prefixes** | `i:` images only, `v:` videos only, otherwise both |
| **Video cards** | Cover + title with Open and Download buttons: the selector receives searchy delivery, while another group member is handed off to a private vido job |
| **Menu** | `/start` opens an inline panel: language, stats, help, about |
| **16 languages** | Full i18n; the chosen language is remembered across the sibling bots via **core** |
| **Private analytics** | Optional `/stats` panel (personal + global) built from counts only |

### Search prefixes

| You type | Searches |
|:--|:--|
| `cats` | images **and** videos |
| `i:cats` | images only |
| `v:cats` | videos only |

Prefixes are read only at the start and also work in `/search`.

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
Search runs inside the bot. The bundled SearXNG has no host ports and is asked
only when native sources come back empty or weak.

**Without SearXNG.** Set `SEARCH_FALLBACK=none` in `.env` and start only the bot.
`SEARXNG_SECRET` still has to be set, because Compose reads the whole file. An
already running SearXNG container keeps running:

```sh
docker compose -f deploy/docker-compose.yml up -d --build --no-deps bot
```

**Without Docker.** Export `BOT_TOKEN` and `SEARCH_FALLBACK=none`, then run
`make run`. It does not read `.env`.

**Back to SearXNG.** Set `SEARCH_BACKEND=searxng` and recreate the bot. SearXNG
must be running with the JSON API on; outside Compose, point `SEARXNG_URL` at
it. Details are in [Search integration](docs/search.md).

---

## Using searchy

- **Inline (any chat).** Type `@yourbot query`. searchy answers with image and
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

searchy is one Go service. It long-polls Telegram and queries Bing and
DuckDuckGo for images and videos, plus Wikimedia Commons for images. It
searches other indexes; it does not crawl the web.

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

- **Pages do not change once shown.** Native returns up to **24** results per
  inline page and ten pages per search session, **240** results at the default
  page size. `MAX_RESULTS` can lower the page size and caps the DM/group grid
  separately. A late source result goes to the
  next page, never into one Telegram already displayed. Pagination cursors are
  opaque and fit Telegram's 64-byte limit.
- **Time budgets.** The first page is collected for up to `800ms`, sources get
  `3.5s`, and the whole request, fallback included, gets `8s`.
- **Fallback.** With `SEARCH_FALLBACK=searxng`, private SearXNG fills in weak
  or empty native results within the same deadline. `SEARCH_BACKEND=searxng`
  switches back to the old aggregator, which keeps its pinned engine sets and
  never sends `categories`.
- **Telegram-compatible media.** Telegram fetches inline media itself, so every
  URL must be public HTTPS, and photos and video covers must be JPEG. Unsupported
  formats are filtered out or replaced with the provider's preview.
  `IMAGE_PROXY` stays `false` while SearXNG is private.
- **Partial results.** If a source fails, the others still answer. Such a page
  is served but not cached as a complete result.

**Video downloads go through vido.** For a selected video, searchy writes an
owner-bound intent to the shared core database. vido applies the user's
download settings and builds a DeliveryPlan; searchy then sends the file as a
separate message with its own bot token through the shared local Telegram Bot
API. If another group member presses Download on the same card, they get their
own vido DM link and nothing extra is posted in the group. Inline cards always
use that DM link, because Telegram does not tell another bot which chat the
result was sent to.

---

## Configuration

All configuration is through environment variables (see
[`.env.example`](.env.example)). Only `BOT_TOKEN` is required. The default
fallback expects a reachable private SearXNG; `SEARCH_FALLBACK=none` turns it
off. Compose also needs `SEARXNG_SECRET`.

### Bot

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `BOT_TOKEN` | yes | - | Telegram bot token from BotFather (inline mode must be enabled) |
| `LANGUAGE` | no | empty | Search language override for both backends; empty uses the user's language |
| `SAFE_SEARCH` | no | `0` | `0` off / `1` moderate / `2` strict, for both backends |
| `MAX_RESULTS` | no | `50` | Inline answer cap (Telegram hard limit is 50) |
| `DEBOUNCE_DELAY` | no | `150ms` | Inline debounce per user |
| `INLINE_CACHE_TIME` | no | `60` | `answerInlineQuery` `cache_time` (seconds); keep it below `CACHE_TTL` |
| `WORKERS` | no | `32` | Update worker pool size |
| `HEALTH_ADDR` | no | `:8081` | Health endpoint listen address |

### Search

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `SEARCH_BACKEND` | no | `native` | `native`, or `searxng` to switch back |
| `SEARCH_FALLBACK` | no | `searxng` | `searxng` or `none`; used only by the native backend |
| `NATIVE_FIRST_PAGE_WAIT` | no | `800ms` | Longest wait for the first inline page |
| `NATIVE_TIMEOUT` | no | `3.5s` | Time budget for native sources |
| `REQUEST_TIMEOUT` | no | `8s` | Whole search, fallback included |
| `SEARCH_MAX_CONCURRENT` | no | `8` | Concurrent native searches and simultaneous requests to sources |
| `CACHE_TTL` | no | `5m` | How long a page session lives after its last scroll |
| `CACHE_SIZE` | no | `2048` | Native: results kept in memory, shown pages included. SearXNG: cache entries |

### SearXNG (fallback and switchback only)

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `SEARXNG_URL` | no | `http://localhost:8080` | Fallback/switchback URL (Compose overrides to `http://searxng:8080`) |
| `SEARXNG_SECRET` | compose | - | Secret for the bundled SearXNG; Compose needs it even without SearXNG |
| `SEARXNG_IMAGE` | no | pinned `2026.6.24` digest | Pinned SearXNG image; change it only after checking the engines |
| `ENGINES_IMAGES` | no | Bing, DuckDuckGo | Core image engines |
| `ENGINES_VIDEOS` | no | YouTube, DuckDuckGo, Dailymotion, Bing Videos | Core video engines |
| `ENGINES_IMAGES_DISCOVERY` | no | FindThatMeme, Pinterest, Giphy, Frinkiac, Wikimedia | Long-tail image engines |
| `ENGINES_VIDEOS_DISCOVERY` | no | Bilibili, PeerTube | Long-tail video engines |
| `DISCOVERY_PERCENT` | no | `30` | Long-tail share when core results are healthy |
| `DISCOVERY_WEAK_PERCENT` | no | `50` | Long-tail share when core results are weak |
| `IMAGE_PROXY` | no | `false` | SearXNG image proxy; keep off unless SearXNG is public over HTTPS |

### Databases and vido

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `DATABASE_URL` | no | - | Postgres for private search analytics; empty disables `/stats` |
| `CORE_DATABASE_URL` | no | - | Shared cross-bot **core** Postgres (identity, presence, language); empty falls back to the Telegram hint |
| `STATS_CACHE_TTL` | no | `10m` | Stats snapshot cache TTL (floored at `10s`) |
| `VIDO_BRIDGE_ENABLED` | no | `false` | Enable owner-bound vido intents and the searchy delivery worker |
| `VIDO_BOT_USERNAME` | with bridge | - | `@username` (no `@`) of the vido bot |
| `TELEGRAM_BOT_API_BASE_URL` | no | cloud API | Shared local Bot API base URL |
| `SHARED_MEDIA_CACHE_DIR` | no | `/shared-media-cache` | Read-only shared artifact root |

---

## Database

searchy uses **two** Postgres connections, and **both are optional** — the bot
runs fine with neither. Search always works; only `/stats` and the cross-bot
saved language depend on them.

- **Search analytics** — `DATABASE_URL`. Holds only private analytics: counts
  and timings of searches and selections, **never** the query text. The schema
  is applied on startup and there is **no `users` table** — identity moved to
  core. In production this points at the shared **core** Postgres as the
  least-privilege role `searchy_core` with `search_path=searchy`, so the
  analytics tables live in a `searchy` schema there.
- **Shared `core` DB** — `CORE_DATABASE_URL`. In production both searchy
  connections point to the same `core-postgres` instance with different
  `search_path` and privilege boundaries. vido, searchy, quoto, branchy, and
  makeitMD share the global Telegram identity hub. searchy identifies
  itself as `searchy` and touches core only through its SECURITY DEFINER
  functions (`core.touch`, `core.set_language`, `core.effective_language`).

See [`docs/architecture.md`](docs/architecture.md) for the full two-database
model.

---

## Privacy

- The **query text is never stored** — not in analytics, not in logs, not in the
  in-memory grid sessions. Only counts, timings, category, result type, and the
  originating source/engine are recorded.
- Native page sessions are keyed by an HMAC of the query, not the text. They
  live in RAM, expire, and are gone after a restart.
- Analytics rows key on the Telegram user id with no query, title, or URL.
- Both databases are optional; with neither configured, nothing is persisted.

---

## Deployment

searchy makes only outbound connections (long polling to
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
  searchy is open source software by Asterfield.<br/>
  Copyright 2026 Asterfield.
</p>
