package bot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"searchy/internal/collage"
	"searchy/internal/i18n"
	"searchy/internal/search"
)

// The DM/group search surface renders results as a single numbered grid image
// (one upload, not a flood of photos), with inline buttons to page through 10 at
// a time and to pull any single item full. Pagination is purely local over a
// fetched pool — the query text is NEVER retained (privacy): a session holds the
// already-normalized results (media URLs), not what was typed.

const (
	gridPageSize = 10
	gridPrefix   = "g"
)

// gridSession is the transient, in-memory state behind one search's buttons.
// pages memoizes already-rendered page JPEGs so paging back (or a double-tap)
// doesn't re-download covers and re-encode the collage. Bounded: at most
// ceil(len(results)/gridPageSize) ≤ 5 entries per session.
type gridSession struct {
	results []search.MediaResult
	lang    string
	mu      sync.Mutex
	pages   map[int][]byte
}

// newToken returns a short, URL-safe id for a grid session (used in callback
// data, so it must stay well under Telegram's 64-byte limit).
func newToken() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a time-derived token; collisions are harmless (worst case a
		// stale session is reused and the user just re-searches).
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// gcb / parseGridCB encode grid callbacks as "g:<token>:<action>:<arg>" where
// action ∈ {p (page), i (item index), x (close)}. The token is base64url, which
// never contains ':', so a 4-way split is safe.
func gcb(tok, action string, arg int) string {
	return gridPrefix + ":" + tok + ":" + action + ":" + strconv.Itoa(arg)
}

func parseGridCB(data string) (tok, action string, arg int, ok bool) {
	p := strings.Split(data, ":")
	if len(p) != 4 || p[0] != gridPrefix {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(p[3])
	if err != nil {
		return "", "", 0, false
	}
	return p[1], p[2], n, true
}

// runGridSearch performs a search and posts the first page as a grid. Works in
// both DM and groups; `source` is "dm" or "group" for analytics only. threadID
// is the forum topic the request came from (0 outside topic groups), so replies
// land in the right place.
func (h *Handlers) runGridSearch(ctx context.Context, b *bot.Bot, chatID int64, threadID int, user *models.User, raw, source string) {
	lang := h.langResolve(ctx, user)
	cats, q := parseQuery(raw)
	if strings.TrimSpace(q) == "" {
		return
	}

	// Tell the chat we're working ("…is sending a photo").
	_, _ = b.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, MessageThreadID: threadID, Action: models.ChatActionUploadPhoto})

	sctx, cancel := context.WithTimeout(ctx, h.requestTimeout)
	start := time.Now()
	results, _ := h.agg.Search(sctx, q, cats, 0)
	cancel()
	h.recordSearch(user, categoryStr(cats), len(results), int(time.Since(start).Milliseconds()), source)

	if len(results) == 0 {
		h.replyRetry(ctx, b, chatID, threadID, lang, i18n.T(lang, "search.nothing", "query", escapeHTML(q)))
		return
	}

	tok := newToken()
	h.grids.Add(tok, &gridSession{results: results, lang: lang})
	h.sendGridPage(ctx, b, chatID, threadID, tok, 0)
}

// sendGridPage renders page `page` of a session and posts it as a NEW photo.
func (h *Handlers) sendGridPage(ctx context.Context, b *bot.Bot, chatID int64, threadID int, tok string, page int) {
	sess, ok := h.grids.Get(tok)
	if !ok {
		return
	}
	img, ok := h.renderGrid(ctx, b, chatID, threadID, sess, page)
	if !ok {
		h.replyRetry(ctx, b, chatID, threadID, sess.lang, i18n.T(sess.lang, "load.failed"))
		return
	}
	_, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Photo:           &models.InputFileUpload{Filename: "results.jpg", Data: bytes.NewReader(img)},
		Caption:         gridCaption(sess.lang, page, len(sess.results)),
		ParseMode:       models.ParseModeHTML,
		ReplyMarkup:     gridKeyboard(sess.lang, tok, page, len(sess.results)),
	})
	if err != nil {
		h.log.Warn("sendPhoto (grid) failed", "err", err)
	}
}

// editGridPage re-renders a different page and swaps it into the existing photo
// message in place (used by the ◀ / Next buttons). EditMessageMedia inherits the
// original message's topic, so only the in-flight chat action needs threadID.
func (h *Handlers) editGridPage(ctx context.Context, b *bot.Bot, chatID int64, msgID, threadID int, tok string, page int) {
	sess, ok := h.grids.Get(tok)
	if !ok {
		return
	}
	img, ok := h.renderGrid(ctx, b, chatID, threadID, sess, page)
	if !ok {
		return
	}
	_, err := b.EditMessageMedia(ctx, &bot.EditMessageMediaParams{
		ChatID:    chatID,
		MessageID: msgID,
		Media: &models.InputMediaPhoto{
			Media:           "attach://grid.jpg",
			MediaAttachment: bytes.NewReader(img),
			Caption:         gridCaption(sess.lang, page, len(sess.results)),
			ParseMode:       models.ParseModeHTML,
		},
		ReplyMarkup: gridKeyboard(sess.lang, tok, page, len(sess.results)),
	})
	if err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return // benign: a double-tap re-rendered the same page
		}
		h.log.Warn("editMessageMedia (grid) failed", "err", err)
	}
}

// renderGrid downloads the covers for one page and composes the collage. It
// re-announces the upload action because download+encode can take a few seconds.
func (h *Handlers) renderGrid(ctx context.Context, b *bot.Bot, chatID int64, threadID int, sess *gridSession, page int) ([]byte, bool) {
	total := len(sess.results)
	startIdx := page * gridPageSize
	if startIdx >= total {
		return nil, false
	}

	// Serve a memoized page (paging back, or a double-tap) without re-downloading
	// covers or re-encoding the collage.
	sess.mu.Lock()
	if cached, ok := sess.pages[page]; ok {
		sess.mu.Unlock()
		return cached, true
	}
	sess.mu.Unlock()

	endIdx := startIdx + gridPageSize
	if endIdx > total {
		endIdx = total
	}
	pageResults := sess.results[startIdx:endIdx]

	// Only announce the upload when there's real work to do (cache misses).
	_, _ = b.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, MessageThreadID: threadID, Action: models.ChatActionUploadPhoto})

	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	covers := h.fetchCovers(dctx, pageResults)
	cancel()

	cells := make([]collage.Cell, len(pageResults))
	for i, r := range pageResults {
		cells[i] = collage.Cell{
			Data:    covers[i],
			IsVideo: r.Category == search.CatVideo,
			Number:  startIdx + i + 1,
		}
	}
	img, err := collage.Render(cells)
	if err != nil {
		h.log.Warn("collage render failed", "err", err)
		return nil, false
	}

	sess.mu.Lock()
	if sess.pages == nil {
		sess.pages = make(map[int][]byte)
	}
	sess.pages[page] = img
	sess.mu.Unlock()
	return img, true
}

// sendGridPick sends a single chosen item full: an image as a photo, a video as
// a cover card with Open/Download buttons (same card as inline/DM video).
func (h *Handlers) sendGridPick(ctx context.Context, b *bot.Bot, chatID int64, threadID int, sess *gridSession, index int, user *models.User) {
	if index < 0 || index >= len(sess.results) {
		return
	}
	r := sess.results[index]
	lang := sess.lang

	_, _ = b.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, MessageThreadID: threadID, Action: models.ChatActionUploadPhoto})
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	switch r.Category {
	case search.CatVideo:
		token, mintErr := h.mintChatDownload(ctx, user, r, chatID)
		download := downloadButton{}
		if mintErr == nil {
			download.CallbackData = videoCallbackPrefix + token
		} else if h.vido != nil {
			h.log.Warn("vido card intent unavailable", "err", mintErr)
		}
		kb := videoButtons(r, lang, download)
		var sent *models.Message
		if data, ok := h.downloadImage(dctx, r.ThumbURL); ok {
			var err error
			sent, err = b.SendPhoto(ctx, &bot.SendPhotoParams{
				ChatID: chatID, MessageThreadID: threadID, Photo: &models.InputFileUpload{Filename: "cover.jpg", Data: bytes.NewReader(data)},
				Caption: videoCaption(r), ParseMode: models.ParseModeHTML, ReplyMarkup: kb,
			})
			if err != nil {
				h.log.Warn("sendPhoto (pick video) failed", "err", err)
			}
		} else {
			var err error
			sent, err = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, MessageThreadID: threadID, Text: videoCaption(r), ParseMode: models.ParseModeHTML, ReplyMarkup: kb})
			if err != nil {
				h.log.Warn("sendMessage (pick video) failed", "err", err)
			}
		}
		if token != "" && sent != nil {
			if err := h.vido.BindIntentMessage(ctx, token, user.ID, chatID, sent.ID); err != nil {
				h.log.Warn("bind vido card failed", "err", err)
			}
		}
	default: // image
		if data, ok := h.downloadImage(dctx, coverURL(r)); ok {
			if _, err := b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, MessageThreadID: threadID, Photo: &models.InputFileUpload{Filename: "image.jpg", Data: bytes.NewReader(data)}}); err != nil {
				h.log.Warn("sendPhoto (pick image) failed", "err", err)
			}
		} else {
			h.replyRetry(ctx, b, chatID, threadID, lang, i18n.T(lang, "load.failed"))
			return
		}
	}
	h.recordPick(user, r)
}

// onGridCallback routes the grid buttons. Anyone in a group may use them (the
// results are shared), so there's no owner check here.
func (h *Handlers) onGridCallback(ctx context.Context, b *bot.Bot, cq *models.CallbackQuery, tok, action string, arg int) {
	if cq.Message.Message == nil {
		h.answerCB(ctx, b, cq.ID, "", false)
		return
	}
	chatID := cq.Message.Message.Chat.ID
	msgID := cq.Message.Message.ID
	threadID := cq.Message.Message.MessageThreadID

	// Close must work even after the session expired/was evicted: it needs only
	// chatID/msgID from the callback, not the session.
	if action == "x" {
		_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
		h.answerCB(ctx, b, cq.ID, "", false)
		return
	}

	sess, ok := h.grids.Get(tok)
	if !ok {
		h.answerCB(ctx, b, cq.ID, i18n.T(h.langCached(&cq.From), "grid.expired"), true)
		return
	}

	switch action {
	case "p":
		h.answerCB(ctx, b, cq.ID, "", false)
		h.editGridPage(ctx, b, chatID, msgID, threadID, tok, arg)
	case "i":
		h.answerCB(ctx, b, cq.ID, "", false)
		h.sendGridPick(ctx, b, chatID, threadID, sess, arg, &cq.From)
	default:
		h.answerCB(ctx, b, cq.ID, "", false)
	}
}

// fetchCovers downloads each result's cover concurrently, aligned by index (nil
// where a download failed — the collage shows a numbered placeholder tile).
func (h *Handlers) fetchCovers(ctx context.Context, rs []search.MediaResult) [][]byte {
	out := make([][]byte, len(rs))
	var wg sync.WaitGroup
	for i := range rs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if data, ok := h.downloadImage(ctx, coverURL(rs[i])); ok {
				out[i] = data
			}
		}(i)
	}
	wg.Wait()
	return out
}

// coverURL is the best thumbnail to render in a grid cell: the cover for videos,
// the (CDN) image for photos.
func coverURL(r search.MediaResult) string {
	if r.Category == search.CatVideo {
		return r.ThumbURL
	}
	if r.MediaURL != "" {
		return r.MediaURL
	}
	return r.ThumbURL
}

// gridCaption is the vido-style header above the grid.
func gridCaption(lang string, page, total int) string {
	totalPages := (total + gridPageSize - 1) / gridPageSize
	return "<b>🔎 " + i18n.T(lang, "grid.title") + "</b>\n<i>" +
		i18n.T(lang, "grid.hint", "page", strconv.Itoa(page+1), "total", strconv.Itoa(totalPages)) + "</i>"
}

// gridKeyboard builds: rows of numbered buttons (one per item on the page), a
// nav row (◀ / Next 10), and a Close button.
func gridKeyboard(lang, tok string, page, total int) *models.InlineKeyboardMarkup {
	totalPages := (total + gridPageSize - 1) / gridPageSize
	startIdx := page * gridPageSize
	endIdx := startIdx + gridPageSize
	if endIdx > total {
		endIdx = total
	}

	var nums []models.InlineKeyboardButton
	for i := startIdx; i < endIdx; i++ {
		nums = append(nums, models.InlineKeyboardButton{Text: strconv.Itoa(i + 1), CallbackData: gcb(tok, "i", i)})
	}
	var rows [][]models.InlineKeyboardButton
	for i := 0; i < len(nums); i += 5 {
		end := i + 5
		if end > len(nums) {
			end = len(nums)
		}
		rows = append(rows, nums[i:end])
	}

	var nav []models.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, models.InlineKeyboardButton{Text: i18n.T(lang, "grid.prev"), CallbackData: gcb(tok, "p", page-1)})
	}
	if page+1 < totalPages {
		nav = append(nav, models.InlineKeyboardButton{Text: i18n.T(lang, "grid.next"), CallbackData: gcb(tok, "p", page+1)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "action.close"), CallbackData: gcb(tok, "x", 0)}})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// replyRetry sends an info message with a one-tap inline-search button, so an
// empty or failed result isn't a dead-end (the button matters most in groups,
// where there's no plain-text search). The prior query is intentionally not
// pre-filled — it's never stored (privacy).
func (h *Handlers) replyRetry(ctx context.Context, b *bot.Bot, chatID int64, threadID int, lang, text string) {
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "btn.search"), SwitchInlineQueryCurrentChat: strptr("")}},
	}}
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, MessageThreadID: threadID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: kb}); err != nil {
		h.log.Warn("sendMessage (retry) failed", "err", err)
	}
}

// recordPick logs a sent item (type + engine only — never the query/title/url).
func (h *Handlers) recordPick(u *models.User, r search.MediaResult) {
	if h.store == nil || u == nil {
		return
	}
	rtype := "photo"
	if r.Category == search.CatVideo {
		rtype = "video"
	}
	uid, eng := u.ID, r.Engine
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.store.RecordSelection(ctx, uid, rtype, eng)
	}()
}
