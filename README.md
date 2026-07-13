<h1 align="center">Searchy</h1>

<p align="center"><strong>Fast image &amp; video search in Telegram.</strong><br/>Small Go bot that queries a self-hosted SearXNG instance and answers inline, in DMs, and in groups.</p>

<p align="center">
  <a href="https://github.com/FreshLabDev/searchy/releases"><img src="https://img.shields.io/github/v/release/FreshLabDev/searchy?include_prereleases&sort=semver&style=for-the-badge&label=latest&labelColor=0f172a&color=4c8c4a" alt="latest version"></a>
  <a href="docs/versioning.md"><img src="https://img.shields.io/badge/version-v0.1.0--alpha.5-4c8c4a?style=for-the-badge&labelColor=0f172a" alt="current version"></a>
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
| Predictable speed | Per-user debounce, in-process cache, and `singleflight` de-duplication |
| A backend you control | Talks to a self-hosted **SearXNG** over its JSON API |
| Privacy by design | Stores counts and timings only — **never** the query text |

> **Result:** one small Go service that turns a self-hosted SearXNG instance into
> fast, in-chat image and video search.

---

## Status

| Channel | Version | Meaning |
|:--|:--|:--|
| Latest | `v0.1.0-alpha.5` | Durable Vido errors, recovery and duplicate prevention |

Searchy is an early alpha. The core inline, DM, and group search flows are
live-tested against Telegram and a self-hosted SearXNG. See
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
| **Video cards** | Cover + title with Open and owner-bound Download buttons powered by Vido |
| **Menu** | `/start` opens an inline panel: language, stats, help, about |
| **16 languages** | Full i18n; the chosen language is remembered across the sibling bots via **core** |
| **Private analytics** | Optional `/stats` panel (personal + global) built from counts only |

### Inline category prefixes

| You type | Searches |
|:--|:--|
| `cats` | images **and** videos |
| `i:cats` | images only |
| `v:cats` | videos only |

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

# 3. Start Searchy and its bundled SearXNG
make up
make logs
```

`make up` runs `docker compose -f deploy/docker-compose.yml up -d --build`.
SearXNG is reachable only on the internal Docker network — it is not published
to the host.

To run the bot locally against a SearXNG you already have, point `SEARXNG_URL`
at an instance with the JSON API enabled and run `make run`.

---

## Using Searchy

- **Inline (any chat).** Type `@yourbot query`. Results stream in as inline
  photo/video cards; keep scrolling to load more pages.
- **DM.** Send any text to search; the bot replies with one numbered grid image.
  Tap a number to pull that item full, or page through 10 at a time.
- **Group.** Add the bot and use `/search <query>` — plain messages are ignored
  so the bot stays quiet. The result grid's buttons are shared: anyone can page
  or open an item.
- **Menu.** `/start` opens an inline panel (language · stats · help · about). Each
  button edits the panel in place; only the user who opened it can drive it.

---

## How It Works

Search remains stateless and private. For a selected video, Searchy creates an
owner-bound intent in the shared core database; Vido applies the user's personal
download settings and builds a safe DeliveryPlan. Searchy sends the ready media
as a separate message with its own bot token through the shared local Telegram
Bot API. Inline download buttons use a Vido DM deep link because Telegram does
not expose the chosen chat id to another bot.

Searchy is one Go service that long-polls Telegram and calls a self-hosted
SearXNG over HTTP.

```text
Telegram ──update──► bot (go-telegram/bot, long polling)
                       │
          inline_query │ debounce(per user) → cancel stale
                       ▼
                  Aggregator ── cache (LRU+TTL) ── singleflight
                       │            (hit → instant)
                       ▼
                  SearXNG provider ──HTTPS──► self-hosted SearXNG ──► engines
                       │   (one call, pinned engines, JSON API)
                       ▼
     inline: InlineQueryResultPhoto (≤50, next_offset)  ·  DM/group: numbered grid
```

Key rules the code respects:

- **≤ 50** inline results per answer; the inline `next_offset` is a small page
  index, well under Telegram's 64-byte limit.
- SearXNG is queried with a **pinned engine set** (`engines=`), never
  `categories`, because `categories` fans out to every enabled engine and
  overloads a private instance.
- Inline media URLs must be **public HTTPS** (Telegram fetches them
  server-side), so `IMAGE_PROXY` defaults to `false`.
- Errors degrade gracefully: a timeout returns partial results, and a failed
  backend call returns an empty answer rather than an error to the user.

---

## Configuration

All configuration is through environment variables (see
[`.env.example`](.env.example)). Every value has a default, so the bot runs with
only `BOT_TOKEN` set.

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `BOT_TOKEN` | yes | - | Telegram bot token from BotFather (inline mode must be enabled) |
| `SEARXNG_URL` | no | `http://localhost:8080` | SearXNG base URL (compose overrides to `http://searxng:8080`) |
| `SEARXNG_SECRET` | compose | - | Secret for the bundled SearXNG service |
| `SEARXNG_TAG` | no | `latest` | SearXNG image tag (pin a dated tag) |
| `ENGINES_IMAGES` | no | `bing images,duckduckgo images,unsplash,wikicommons.images` | Pinned image engines |
| `ENGINES_VIDEOS` | no | `youtube,dailymotion,duckduckgo videos` | Pinned video engines |
| `SAFE_SEARCH` | no | `0` | `0` off / `1` moderate / `2` strict |
| `IMAGE_PROXY` | no | `false` | Route media URLs through SearXNG's proxy (keep off unless SearXNG is public HTTPS) |
| `LANGUAGE` | no | `all` | SearXNG search language; `all` = neutral (no filter) |
| `REQUEST_TIMEOUT` | no | `5s` | Per backend call; partial results on timeout (Go duration) |
| `MAX_RESULTS` | no | `50` | Inline answer cap (Telegram hard limit is 50) |
| `DEBOUNCE_DELAY` | no | `150ms` | Inline debounce per user |
| `CACHE_TTL` | no | `5m` | In-process result cache TTL |
| `CACHE_SIZE` | no | `2048` | In-process result cache entries |
| `WORKERS` | no | `32` | Update worker pool size |
| `INLINE_CACHE_TIME` | no | `300` | `answerInlineQuery` `cache_time` (seconds) |
| `STATS_CACHE_TTL` | no | `10m` | Stats snapshot cache TTL (floored at `10s`) |
| `HEALTH_ADDR` | no | `:8081` | Health endpoint listen address |
| `DATABASE_URL` | no | - | Postgres for private search analytics; empty disables `/stats` |
| `CORE_DATABASE_URL` | no | - | Shared cross-bot **core** Postgres (identity, presence, language); empty falls back to the Telegram hint |
| `POSTGRES_PASSWORD` | compose | - | Builds `DATABASE_URL` for the bundled dev Postgres |
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
- **Shared `core` DB** — `CORE_DATABASE_URL`. All the sibling bots (searchy,
  vido, quoto, branchy) share one **core** Postgres, keyed on the global Telegram
  id, for identity, presence, and the user's saved language. Searchy identifies
  itself as `searchy` and touches core only through its SECURITY DEFINER
  functions (`core.touch`, `core.set_language`, `core.effective_language`).

See [`docs/architecture.md`](docs/architecture.md) for the full two-database
model.

---

## Privacy

- The **query text is never stored** — not in analytics, not in logs, not in the
  in-memory grid sessions. Only counts, timings, category, result type, and the
  originating SearXNG engine are recorded.
- Analytics rows key on the Telegram user id with no query, title, or URL.
- Both databases are optional; with neither configured, nothing is persisted.

---

## Deployment

Searchy makes only outbound connections (long polling to
`api.telegram.org` and HTTP to SearXNG), so **no public ports are required**.
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
| [Search integration](docs/search.md) | SearXNG JSON API, engines, and result normalization |
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
  Searchy is open source software by FreshLab.<br/>
  Copyright 2026 FreshLab.
</p>
