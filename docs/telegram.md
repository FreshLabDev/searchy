# Telegram Integration

## Commands

| Command | Where | Purpose |
|:--|:--|:--|
| `/start` | private, groups | Opens the inline main menu (language · stats · help · about) |
| `/search <query>` | groups (and DM) | Runs a search and posts a numbered result grid |
| `/help` | any | Help panel (also reachable from the menu) |
| `/stats` | any | Personal stats panel (also reachable from the menu) |
| `/about` | any | About panel (also reachable from the menu) |

Only `/start` (private) and `/start` + `/search` (groups) are published to the
Telegram command menu via `setMyCommands`; the rest are reachable from the inline
menu buttons. Command menus are localized per language.

A command is handled only when it is bare or addressed to this bot
(`/search@yourbot`); a command explicitly addressed to another bot
(`/search@otherbot`, which Telegram still delivers in groups) is ignored. An
unknown command nudges the user in private chats only, to avoid spamming groups.

## Inline Search (primary surface)

Typing `@yourbot query` in any chat sends an `inline_query`. Inline mode must be
enabled for the bot in BotFather (`/setinline`) or these updates never arrive.

- Results are `InlineQueryResultPhoto` cards: images as their photo, and videos
  as a cover photo with an "Open on <platform>" link button and a "Download"
  placeholder button.
- Up to `MAX_RESULTS` (Telegram's hard cap of 50) results per answer, with a
  `next_offset` page index to load more as the user scrolls (bounded to 10
  pages).
- Queries are debounced per user; a superseded keystroke is cancelled before it
  reaches the backend.
- An empty query returns a short prompt; a query with no results returns a single
  "nothing found" article.

### Category prefixes

| You type | Searches |
|:--|:--|
| `cats` | images and videos |
| `i:cats` | images only |
| `v:cats` | videos only |

## DM and Group Search

- **DM:** any plain text message runs a search and returns one **numbered grid**
  image (a single upload, not a flood of photos).
- **Groups:** searching is explicit — `/search <query>`. Plain messages are
  ignored so the bot stays quiet. `/search` with no query offers a one-tap
  inline-search button instead.

The grid has:

- a row of numbered buttons (one per item on the page),
- a nav row (previous / next 10), and
- a Close button.

Paging edits the grid image in place. Tapping a number sends that item full: an
image as a photo, a video as a cover card with the same Open/Download buttons.
Grid buttons are **shared** — in a group, anyone can page or open an item, so
there is no per-user ownership check on them.

## The Menu

`/start` posts an inline panel; each button edits it in place (vido style):

- **Language** — a picker over 16 languages; the current one is marked. A choice
  is recorded as this bot's manual language claim in core.
- **Stats** — personal and global tabs (searches, results sent, image/video
  breakdown, peak hour). Served from a short-lived snapshot cache.
- **Help** / **About** — informational panels.
- **Close** — deletes the panel.

Menu callbacks are ownership-scoped: only the user who opened a panel can drive
it; another user gets a "not yours" toast. Panels use Telegram HTML parse mode
with link previews disabled.

## Language Resolution

The user's language is resolved, in order:

1. an in-memory per-user cache (fast path, used on every inline keystroke),
2. the shared **core** store's `core.effective_language` (personal scope), then
3. the Telegram client's `language_code` hint.

On first `/start`, the Telegram hint is auto-detected and persisted to core as
this bot's `auto` claim; an explicit picker choice is persisted as a `manual`
claim, which wins cross-bot conflicts.

## Privacy in the UI

The query text is never shown back from storage or pre-filled on retry — it is
never stored. Grid sessions hold already-normalized media URLs, not what the user
typed. User-controlled text (titles, authors) is HTML-escaped before formatting.
