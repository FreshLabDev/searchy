// Package bot wires Telegram updates to the search backend and the DM shell:
// inline media search (the primary surface), a /start + buttons menu (language,
// stats, help, about — vido style), full i18n, and best-effort Postgres
// analytics (who searched what, what they sent).
package bot

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	lru "github.com/hashicorp/golang-lru/v2/expirable"

	"searchy/internal/core"
	"searchy/internal/db"
	"searchy/internal/i18n"
	"searchy/internal/search"
	vidobridge "searchy/internal/vido"
)

// maxPages bounds inline pagination so scrolling can't drive SearXNG forever.
const maxPages = 10

// resultMeta is what we remember about an answered inline result so that, when
// the user picks it, we can record what KIND of thing was sent — never the
// content itself (no title/url/query is ever stored).
type resultMeta struct {
	rtype  string // photo | video
	engine string
}

type Handlers struct {
	agg             *search.Aggregator
	deb             *debouncer
	log             *slog.Logger
	httpClient      *http.Client
	store           *db.Store
	core            *core.Core
	vido            *vidobridge.Bridge
	botUsername     string
	vidoBotUsername string
	sharedCacheRoot string
	maxResults      int
	inlineCacheTime int
	requestTimeout  time.Duration

	langCache  sync.Map // userID(int64) -> language(string)
	resultMeta *lru.LRU[string, resultMeta]
	grids      *lru.LRU[string, *gridSession] // token -> DM/group grid state
	statsCache *statsCache                    // snapshot cache for /stats panels
	jobWatch   sync.Map                       // jobID(int64) -> struct{}
}

type Options struct {
	Aggregator      *search.Aggregator
	Logger          *slog.Logger
	HTTPClient      *http.Client
	Store           *db.Store
	Core            *core.Core
	Vido            *vidobridge.Bridge
	BotUsername     string
	VidoBotUsername string
	SharedCacheRoot string
	MaxResults      int
	InlineCacheTime int
	DebounceDelay   time.Duration
	RequestTimeout  time.Duration
	StatsCacheTTL   time.Duration
}

func NewHandlers(o Options) *Handlers {
	return &Handlers{
		agg:             o.Aggregator,
		deb:             newDebouncer(o.DebounceDelay),
		log:             o.Logger,
		httpClient:      o.HTTPClient,
		store:           o.Store,
		core:            o.Core,
		vido:            o.Vido,
		botUsername:     o.BotUsername,
		vidoBotUsername: o.VidoBotUsername,
		sharedCacheRoot: o.SharedCacheRoot,
		maxResults:      o.MaxResults,
		inlineCacheTime: o.InlineCacheTime,
		requestTimeout:  o.RequestTimeout,
		resultMeta:      lru.NewLRU[string, resultMeta](8192, nil, time.Hour),
		// Sessions now memoize rendered page JPEGs, so cap the count to bound memory
		// (worst case ~5 pages × ~200 KB each per live session).
		grids:      lru.NewLRU[string, *gridSession](1024, nil, time.Hour),
		statsCache: newStatsCache(statsTTL(o.StatsCacheTTL)),
	}
}

func statsTTL(d time.Duration) time.Duration {
	if d <= 0 {
		return 10 * time.Minute
	}
	return d
}

// SetBotUsername sets the bot's @username (without @), used in inline prompts and
// the /start text. Called once at startup before Start.
func (h *Handlers) SetBotUsername(u string) { h.botUsername = u }

// Route is the bot's default handler; it dispatches by update type.
func (h *Handlers) Route(ctx context.Context, b *bot.Bot, update *models.Update) {
	switch {
	case update.InlineQuery != nil:
		h.onInline(ctx, b, update.InlineQuery)
	case update.ChosenInlineResult != nil:
		h.onChosen(update.ChosenInlineResult)
	case update.CallbackQuery != nil:
		h.onCallback(ctx, b, update.CallbackQuery)
	case update.Message != nil:
		h.onMessage(ctx, b, update.Message)
	}
}

// ---- inline search ----

func (h *Handlers) onInline(ctx context.Context, b *bot.Bot, iq *models.InlineQuery) {
	if iq.From == nil { // malformed update — From is required for debounce/analytics
		return
	}
	raw := strings.TrimSpace(iq.Query)
	if raw == "" {
		lang := h.langCached(iq.From)
		h.answer(ctx, b, iq.ID, promptResult(lang, h.botUsername), "", 5)
		return
	}
	// Resolve core once on a cold cache so a manual language choice also affects
	// inline search after restart. Subsequent keystrokes stay on the fast cache.
	lang := h.langResolve(ctx, iq.From)

	dctx, dcancel, ok := h.deb.gate(ctx, iq.From.ID)
	defer dcancel()
	if !ok {
		return // superseded while typing
	}

	cats, q := parseQuery(raw)
	if strings.TrimSpace(q) == "" {
		h.answer(ctx, b, iq.ID, promptResult(lang, h.botUsername), "", 5)
		return
	}
	page := decodeOffset(iq.Offset)

	sctx, cancel := context.WithTimeout(dctx, h.requestTimeout)
	defer cancel()
	start := time.Now()
	results, hasMore := h.agg.Search(sctx, q, cats, page, search.SearchLanguage(lang))
	if dctx.Err() != nil {
		return // superseded by a newer query
	}

	downloadURLs := h.inlineDownloadURLs(dctx, iq.From, results)
	inline := buildInlineResults(results, lang, downloadURLs)
	h.cacheMeta(results) // so chosen_inline_result can record what was sent
	h.log.Info("inline", "page", page, "results", len(inline), "ms", time.Since(start).Milliseconds())

	if page == 0 {
		h.recordSearch(iq.From, categoryStr(cats), len(inline), int(time.Since(start).Milliseconds()), "inline")
		h.touchCore(iq.From, nil) // inline has no chat context → DM/0 presence rollup
	}

	next := ""
	if hasMore && len(inline) > 0 && page+1 < maxPages {
		next = encodeOffset(page + 1)
	}
	if len(inline) == 0 && page == 0 {
		inline = emptyResult(lang, q)
	}
	h.answer(ctx, b, iq.ID, inline, next, h.inlineCacheTime)
}

func (h *Handlers) answer(ctx context.Context, b *bot.Bot, id string, results []models.InlineQueryResult, next string, cacheTime int) {
	_, err := b.AnswerInlineQuery(ctx, &bot.AnswerInlineQueryParams{
		InlineQueryID: id,
		Results:       results,
		CacheTime:     cacheTime,
		IsPersonal:    true,
		NextOffset:    next,
	})
	if err != nil {
		h.log.Warn("answerInlineQuery failed", "err", err)
	}
}

// onChosen records which result the user actually sent (needs inline feedback
// enabled in @BotFather). The query comes with the update; the rest from cache.
func (h *Handlers) onChosen(cr *models.ChosenInlineResult) {
	h.touchCore(&cr.From, nil) // a user sent a result → presence for this bot
	if h.store == nil {
		return
	}
	var m resultMeta
	if v, ok := h.resultMeta.Get(cr.ResultID); ok {
		m = v
	}
	userID := cr.From.ID // cr.Query is intentionally NOT recorded
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.store.RecordSelection(ctx, userID, m.rtype, m.engine)
	}()
}

// ---- callbacks (menu + owner-bound Vido downloads) ----

func (h *Handlers) onCallback(ctx context.Context, b *bot.Bot, cq *models.CallbackQuery) {
	if token, kind, ok := parseDownloadCB(cq.Data); ok {
		h.onDownloadCallback(ctx, b, cq, token, kind)
		return
	}

	// Grid buttons (DM/group result grid): page, pick, close.
	if tok, action, arg, ok := parseGridCB(cq.Data); ok {
		h.onGridCallback(ctx, b, cq, tok, action, arg)
		return
	}

	owner, action, ok := parseMenuCB(cq.Data)
	if !ok {
		h.answerCB(ctx, b, cq.ID, "", false)
		return
	}
	if cq.From.ID != owner {
		h.answerCB(ctx, b, cq.ID, i18n.T(h.langCached(&cq.From), "menu.notyours"), true)
		return
	}
	if cq.Message.Message == nil { // inaccessible (too old)
		h.answerCB(ctx, b, cq.ID, "", false)
		return
	}
	chatID := cq.Message.Message.Chat.ID
	msgID := cq.Message.Message.ID
	h.touchCore(&cq.From, &cq.Message.Message.Chat)
	lang := h.langResolve(ctx, &cq.From)

	switch {
	case action == "close":
		_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
		h.answerCB(ctx, b, cq.ID, "", false)
		return
	case action == "home":
		text, kb := homePanel(lang, h.botUsername, h.vidoBotUsername, owner)
		h.editPanel(ctx, b, chatID, msgID, text, kb)
	case action == "language":
		text, kb := languagePanel(lang, owner)
		h.editPanel(ctx, b, chatID, msgID, text, kb)
	case strings.HasPrefix(action, "l|"):
		code := action[2:]
		if i18n.IsSupported(code) {
			h.setLanguage(cq.From.ID, code, core.SourceManual)
			lang = code
			text, kb := languagePanel(lang, owner)
			h.editPanel(ctx, b, chatID, msgID, text, kb)
			h.answerCB(ctx, b, cq.ID, i18n.T(lang, "language.updated", "language", i18n.LabelOf(code)), false)
			return
		}
	case action == "statsp" || action == "statsg":
		global := action == "statsg"
		st, updated := h.statsView(ctx, cq.From.ID, global)
		text, kb := statsPanel(lang, owner, st, global, updated)
		h.editPanel(ctx, b, chatID, msgID, text, kb)
	case action == "help":
		text, kb := infoPanel(lang, owner, "help.title", "help.body", "bot", h.botUsername)
		h.editPanel(ctx, b, chatID, msgID, text, kb)
	case action == "about":
		text, kb := aboutBody(lang, owner)
		h.editPanel(ctx, b, chatID, msgID, text, kb)
	}
	h.answerCB(ctx, b, cq.ID, "", false)
}

func (h *Handlers) editPanel(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup) {
	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:             chatID,
		MessageID:          msgID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		ReplyMarkup:        kb,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: ptrBool(true)},
	})
	if err != nil {
		h.log.Warn("editMessageText failed", "err", err)
	}
}

func (h *Handlers) answerCB(ctx context.Context, b *bot.Bot, id, text string, alert bool) {
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: id,
		Text:            text,
		ShowAlert:       alert,
	})
}

func (h *Handlers) answerCBURL(ctx context.Context, b *bot.Bot, id, url string) {
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: id,
		URL:             url,
	})
}

// ---- messages (commands + DM search) ----

func (h *Handlers) onMessage(ctx context.Context, b *bot.Bot, msg *models.Message) {
	if msg.From == nil || msg.Chat.ID == 0 {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	h.touchCore(msg.From, &msg.Chat) // identity + presence for this bot/chat

	if strings.HasPrefix(text, "/") {
		cmd, arg, forUs := parseCommand(text, h.botUsername)
		if !forUs {
			return // a command explicitly addressed to a different bot (e.g. /x@otherbot)
		}
		switch cmd {
		case "start":
			lang := h.onStart(ctx, msg.From)
			t, kb := homePanel(lang, h.botUsername, h.vidoBotUsername, msg.From.ID)
			h.sendPanel(ctx, b, msg.Chat.ID, t, kb)
		case "search":
			// "/search <query>" runs a full grid search right here (the main way to
			// search in groups). With no query, offer a one-tap inline-search button.
			lang := h.langResolve(ctx, msg.From)
			if arg == "" {
				kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
					{{Text: i18n.T(lang, "btn.search"), SwitchInlineQueryCurrentChat: strptr("")}},
				}}
				h.sendPanel(ctx, b, msg.Chat.ID, i18n.T(lang, "search.usage", "bot", h.botUsername), kb)
				return
			}
			source := "group"
			if msg.Chat.Type == models.ChatTypePrivate {
				source = "dm"
			}
			h.runGridSearch(ctx, b, msg.Chat.ID, msg.MessageThreadID, msg.From, arg, source)
		case "help":
			lang := h.langResolve(ctx, msg.From)
			t, kb := infoPanel(lang, msg.From.ID, "help.title", "help.body", "bot", h.botUsername)
			h.sendPanel(ctx, b, msg.Chat.ID, t, kb)
		case "stats":
			lang := h.langResolve(ctx, msg.From)
			st, updated := h.statsView(ctx, msg.From.ID, false)
			t, kb := statsPanel(lang, msg.From.ID, st, false, updated)
			h.sendPanel(ctx, b, msg.Chat.ID, t, kb)
		case "about":
			lang := h.langResolve(ctx, msg.From)
			t, kb := aboutBody(lang, msg.From.ID)
			h.sendPanel(ctx, b, msg.Chat.ID, t, kb)
		default:
			// Unknown command: nudge in private chats only (don't spam groups).
			if msg.Chat.Type == models.ChatTypePrivate {
				lang := h.langResolve(ctx, msg.From)
				h.reply(ctx, b, msg.Chat.ID, i18n.T(lang, "cmd.unknown"))
			}
		}
		return
	}

	// Plain text triggers a search only in private chats; in groups, searching is
	// explicit via /search to avoid reacting to every message.
	if msg.Chat.Type != models.ChatTypePrivate {
		return
	}
	h.runGridSearch(ctx, b, msg.Chat.ID, msg.MessageThreadID, msg.From, text, "dm")
}

func (h *Handlers) sendPanel(ctx context.Context, b *bot.Bot, chatID int64, text string, kb *models.InlineKeyboardMarkup) {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		ReplyMarkup:        kb,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: ptrBool(true)},
	})
	if err != nil {
		h.log.Warn("sendMessage (panel) failed", "err", err)
	}
}

func (h *Handlers) reply(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	h.replyThread(ctx, b, chatID, 0, text)
}

// replyThread is reply scoped to a forum topic (threadID 0 outside topic groups).
func (h *Handlers) replyThread(ctx context.Context, b *bot.Bot, chatID int64, threadID int, text string) {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, MessageThreadID: threadID, Text: text, ParseMode: models.ParseModeHTML})
	if err != nil {
		h.log.Warn("sendMessage failed", "err", err)
	}
}

// ---- language + analytics helpers ----

// langCached returns the user's language from the in-memory cache, falling back
// to the Telegram-reported code (no DB). Used in the inline hot path.
func (h *Handlers) langCached(u *models.User) string {
	if u == nil {
		return i18n.DefaultLang
	}
	if v, ok := h.langCache.Load(u.ID); ok {
		return v.(string)
	}
	return i18n.Resolve(u.LanguageCode)
}

// coreUserLang reads the user's resolved personal language from core on a cache
// miss, memoizing it. Returns false when core is unset/unreachable or unset.
func (h *Handlers) coreUserLang(ctx context.Context, u *models.User) (string, bool) {
	if h.core == nil {
		return "", false
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if l, ok := h.core.EffectiveLanguage(c, u.ID, nil, core.ScopeUser); ok {
		h.langCache.Store(u.ID, l)
		return l, true
	}
	return "", false
}

// langResolve returns the user's language, consulting core on a cache miss (used
// on cold DM/menu paths, not the inline hot path). Personal surfaces resolve with
// the user's own language (prefer=user), so a personal manual choice wins even in
// a group.
func (h *Handlers) langResolve(ctx context.Context, u *models.User) string {
	if u == nil {
		return i18n.DefaultLang
	}
	if v, ok := h.langCache.Load(u.ID); ok {
		return v.(string)
	}
	if l, ok := h.coreUserLang(ctx, u); ok {
		return l
	}
	l := i18n.Resolve(u.LanguageCode)
	h.langCache.Store(u.ID, l)
	return l
}

// onStart resolves the user's language, auto-detecting from the Telegram hint on
// first contact (persisted to core as this bot's 'auto' claim), like vido.
func (h *Handlers) onStart(ctx context.Context, u *models.User) string {
	if u == nil {
		return i18n.DefaultLang
	}
	if v, ok := h.langCache.Load(u.ID); ok {
		return v.(string)
	}
	if l, ok := h.coreUserLang(ctx, u); ok {
		return l
	}
	l := i18n.Resolve(u.LanguageCode)
	h.setLanguage(u.ID, l, core.SourceAuto) // persist the auto-detected language
	return l
}

// setLanguage caches the user's language and records it as this bot's claim in
// core. source distinguishes an explicit pick (SourceManual) from auto-detection
// (SourceAuto) — manual wins cross-bot conflicts.
func (h *Handlers) setLanguage(userID int64, code, source string) {
	h.langCache.Store(userID, code)
	if h.core == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.core.SetLanguage(ctx, core.ScopeUser, userID, code, source)
	}()
}

// statsView returns the stats to render plus an "updated HH:MM" label. It serves
// a cached snapshot when one exists (refreshing it in the background once it has
// gone stale), and only blocks on the DB for a cold miss. Repeatedly toggling
// the My/Bot tabs therefore costs zero queries within the TTL window.
func (h *Handlers) statsView(ctx context.Context, userID int64, global bool) (db.Stats, string) {
	if h.store == nil {
		return db.Stats{}, ""
	}
	key := userID
	if global {
		key = 0 // shared global snapshot (real user ids are always > 0)
	}

	if snap, ok := h.statsCache.get(key); ok {
		if time.Now().After(snap.expiresAt) {
			h.refreshStatsAsync(key, global) // stale: serve now, refresh behind the scenes
		}
		return snap.st, kyivFmt(snap.updatedAt)
	}

	// Cold miss: the query is light (a few counts), so fetch synchronously and
	// show data immediately rather than a placeholder.
	c, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	now := time.Now()
	st := h.fetchStats(c, key, global)
	h.statsCache.put(key, st, now)
	return st, kyivFmt(now)
}

// refreshStatsAsync recomputes a snapshot in the background, deduped so
// concurrent viewers don't stampede the DB.
func (h *Handlers) refreshStatsAsync(key int64, global bool) {
	if !h.statsCache.beginRefresh(key) {
		return
	}
	go func() {
		defer h.statsCache.endRefresh(key)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.statsCache.put(key, h.fetchStats(ctx, key, global), time.Now())
	}()
}

// fetchStats runs the actual DB queries (key 0 = global).
func (h *Handlers) fetchStats(ctx context.Context, key int64, global bool) db.Stats {
	if global {
		st, _ := h.store.GlobalStats(ctx)
		return st
	}
	st, _ := h.store.UserStats(ctx, key)
	return st
}

// touchCore records identity + presence (+ the Telegram language hint) in the
// shared core store, best-effort and off the hot path. Private chats pass a nil
// chat so presence rolls up under chat 0 and the chat directory stays group-only.
func (h *Handlers) touchCore(u *models.User, chat *models.Chat) {
	if h.core == nil || u == nil {
		return
	}
	a := core.TouchArgs{
		UserID: u.ID, Username: u.Username, FirstName: u.FirstName,
		LastName: u.LastName, TelegramLang: u.LanguageCode, IsBot: u.IsBot,
	}
	if chat != nil && chat.Type != models.ChatTypePrivate && chat.ID != 0 {
		id := chat.ID
		a.ChatID = &id
		a.ChatType = string(chat.Type)
		a.ChatTitle = chat.Title
		a.ChatUsername = chat.Username
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.core.Touch(ctx, a)
	}()
}

func (h *Handlers) recordSearch(u *models.User, category string, count, ms int, source string) {
	if h.store == nil || u == nil {
		return
	}
	uid := u.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.store.RecordSearch(ctx, uid, category, count, ms, source)
	}()
}

func (h *Handlers) cacheMeta(results []search.MediaResult) {
	for _, r := range results {
		rtype := "photo"
		if r.Category == search.CatVideo {
			rtype = "video"
		}
		h.resultMeta.Add(r.ID, resultMeta{rtype: rtype, engine: r.Engine})
	}
}

// ---- small helpers ----

// parseCommand splits "/cmd@bot arg…" into the lowercased command name, the
// trimmed argument, and whether it's addressed to us (no @target, or @ourname).
// Exact-matching the name (vs HasPrefix) stops "/starting" or a sibling bot's
// "/search@otherbot foo" from hijacking our handlers.
func parseCommand(text, botUsername string) (cmd, arg string, forUs bool) {
	first := text
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		first = text[:i]
		arg = strings.TrimSpace(text[i+1:])
	}
	name := strings.TrimPrefix(first, "/")
	target := ""
	if at := strings.IndexByte(name, '@'); at >= 0 {
		target, name = name[at+1:], name[:at]
	}
	forUs = target == "" || strings.EqualFold(target, botUsername)
	return strings.ToLower(name), arg, forUs
}

func parseQuery(raw string) ([]search.Category, string) {
	low := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(low, "i:"):
		return []search.Category{search.CatImage}, strings.TrimSpace(raw[2:])
	case strings.HasPrefix(low, "v:"):
		return []search.Category{search.CatVideo}, strings.TrimSpace(raw[2:])
	default:
		return []search.Category{search.CatImage, search.CatVideo}, raw
	}
}

func categoryStr(cats []search.Category) string {
	if len(cats) >= 2 {
		return "mixed"
	}
	if len(cats) == 1 {
		if cats[0] == search.CatImage {
			return "images"
		}
		return "videos"
	}
	return ""
}

func decodeOffset(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func encodeOffset(page int) string { return strconv.Itoa(page) }

func emptyResult(lang, q string) []models.InlineQueryResult {
	return []models.InlineQueryResult{
		&models.InlineQueryResultArticle{
			ID:                  "empty",
			Title:               i18n.T(lang, "search.empty_title", "query", q),
			Description:         i18n.T(lang, "help.hint"),
			InputMessageContent: models.InputTextMessageContent{MessageText: i18n.T(lang, "help.hint")},
		},
	}
}

func ptrBool(b bool) *bool { return &b }

var kyivLoc = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// kyivFmt formats a time in Kyiv local time for the "updated HH:MM" stats label.
func kyivFmt(t time.Time) string { return t.In(kyivLoc).Format("15:04 02.01.2006") }
