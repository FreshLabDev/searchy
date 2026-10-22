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
  as a cover photo with an "Open on <platform>" link and a "Download" URL button
  leading to an owner-bound Vido private-chat job. Telegram does not expose the
  destination chat to Vido, so inline mode never attempts direct delivery into
  the chosen chat.
- Up to `MAX_RESULTS` (Telegram's hard cap of 50) results per answer, with a
  `next_offset` to load more as the user scrolls. Native uses an opaque cursor
  bound to the current search and user; replay returns the same frozen page.
  The explicit SearXNG backend retains its legacy numeric paging.
- Native retrieval is incremental internally. Late source results enter later
  pages; the bot does not push additional cards into an already-answered inline
  picker or reorder cards already returned. Invalid/expired native cursors are
  handled explicitly; starting a fresh query starts a new search.
- Queries are debounced per user; a superseded keystroke is cancelled before it
  reaches the backend.
- An empty query returns a short prompt; a query with no results returns a single
  "nothing found" article.

### Search prefixes

| You type | Searches |
|:--|:--|
| `cats` | images and videos |
| `i:cats` | images only |
| `v:cats` | videos only |
| `f:cats` | images and videos, same as `cats` |
| `f: i:cats` or `i: f:cats` | images only |
| `f: v:cats` or `v: f:cats` | videos only |

`f:` is an alias for normal fast search with no AI; it does not reduce sources
or change ranking. Prefixes are read only at the start, in either mode/category
order, and work in inline search, DM text and `/search`. Ordinary query text is
not rewritten. This release adds no `ai:` mode or AI toggle.

### Media compatibility

Inline images and video-cover cards use `InlineQueryResultPhoto`, so Telegram
must be able to fetch a public HTTPS JPEG photo/cover, as well as a suitable
thumbnail. A browser displaying a WebP, SVG, GIF or PNG is not sufficient proof.
Native source normalization filters unsupported formats or selects a compatible
provider preview; it does not publish the lab's local thumbnail-conversion
endpoint. Validate the card by selecting and sending it in Telegram, not only
by inspecting the HTML prototype or inline picker.

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

The user who selected a video owns its Searchy delivery callback and receives a
new Searchy message in the same chat/topic. If another group member presses the
same Download button, Core validates the exact card and Searchy opens a newly
owner-bound Vido deep link for that person. Nothing extra is posted in the
group.

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
