package bot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"searchy/internal/buildinfo"
	"searchy/internal/db"
	"searchy/internal/i18n"
)

// Menu navigation is callback-driven (vido style): /start posts a panel and each
// button edits it in place. Callback data: "m:<owner>:<action>" where action ∈
// {home, language, statsp, statsg, help, about, close} or "l|<code>".
//
// Formatting follows vido's rule exactly: a header of <b>title</b> + <i>hint</i>,
// then a <blockquote> of content lines. Toggle marks use ◉ / ◎.
const menuPrefix = "m"

func cb(owner int64, action string) string {
	return menuPrefix + ":" + strconv.FormatInt(owner, 10) + ":" + action
}

func parseMenuCB(data string) (owner int64, action string, ok bool) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 || parts[0] != menuPrefix {
		return 0, "", false
	}
	owner, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return owner, parts[2], true
}

func strptr(s string) *string { return &s }

// header builds vido's <b>title</b> + optional <i>hint</i>.
func header(lang, titleKey, hintKey string) string {
	s := "<b>" + i18n.T(lang, titleKey) + "</b>"
	if hintKey != "" {
		s += "\n<i>" + i18n.T(lang, hintKey) + "</i>"
	}
	return s
}

// blockquote wraps lines in Telegram's <blockquote>, vido style.
func blockquote(lines ...string) string {
	if len(lines) == 0 {
		return ""
	}
	return "<blockquote>" + strings.Join(lines, "\n") + "</blockquote>"
}

func tabMark(active bool) string {
	if active {
		return "◉ "
	}
	return "◎ "
}

func curMark(active bool) string {
	if active {
		return "◉ "
	}
	return ""
}

// homePanel — the /start landing panel.
func homePanel(lang, botUsername, vidoBotUsername string, owner int64) (string, *models.InlineKeyboardMarkup) {
	text := i18n.T(lang, "home.title") + "\n\n" +
		i18n.T(lang, "home.tagline") + "\n\n" +
		i18n.T(lang, "home.hint", "bot", botUsername)
	rows := [][]models.InlineKeyboardButton{
		{
			{Text: i18n.T(lang, "btn.language"), CallbackData: cb(owner, "language")},
			{Text: i18n.T(lang, "btn.stats"), CallbackData: cb(owner, "statsp")},
		},
		{
			{Text: i18n.T(lang, "btn.help"), CallbackData: cb(owner, "help")},
			{Text: i18n.T(lang, "btn.about"), CallbackData: cb(owner, "about")},
		},
	}
	if vidoBotUsername != "" {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: i18n.T(lang, "btn.video_settings"),
			URL:  "https://t.me/" + vidoBotUsername + "?start=settings",
		}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "action.close"), CallbackData: cb(owner, "close")}})
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	return text, kb
}

// languagePanel — the language picker (2 per row), current one marked with ◉.
func languagePanel(lang string, owner int64) (string, *models.InlineKeyboardMarkup) {
	text := header(lang, "language.title", "language.hint")
	opts := i18n.LANGUAGE_OPTIONS
	var rows [][]models.InlineKeyboardButton
	for i := 0; i < len(opts); i += 2 {
		row := []models.InlineKeyboardButton{
			{Text: curMark(opts[i].Code == lang) + opts[i].Label, CallbackData: cb(owner, "l|"+opts[i].Code)},
		}
		if i+1 < len(opts) {
			row = append(row, models.InlineKeyboardButton{
				Text: curMark(opts[i+1].Code == lang) + opts[i+1].Label, CallbackData: cb(owner, "l|"+opts[i+1].Code),
			})
		}
		rows = append(rows, row)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "action.back"), CallbackData: cb(owner, "home")}})
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// statsPanel — personal or global stats (vido formatting). No query text anywhere.
func statsPanel(lang string, owner int64, st db.Stats, global bool, updated string) (string, *models.InlineKeyboardMarkup) {
	titleKey, subKey := "stats.title.personal", "stats.subtitle.personal"
	if global {
		titleKey, subKey = "stats.title.global", "stats.subtitle.global"
	}
	var b strings.Builder
	b.WriteString(header(lang, titleKey, subKey))
	b.WriteString("\n\n")

	if st.Searches == 0 && st.Sent == 0 {
		b.WriteString(i18n.T(lang, "stats.empty"))
	} else {
		b.WriteString(blockquote(
			i18n.T(lang, "stats.field.searches", "count", i64(st.Searches)),
			i18n.T(lang, "stats.field.sent", "count", i64(st.Sent)),
			i18n.T(lang, "stats.field.breakdown", "images", i64(st.PhotoSent), "videos", i64(st.VideoSent)),
			i18n.T(lang, "stats.field.peak", "peak", peakLabel(st.PeakHour)),
		))
		if global && st.Users > 0 {
			b.WriteString("\n\n<b>" + i18n.T(lang, "stats.meta.title") + "</b>\n")
			b.WriteString(blockquote(i18n.T(lang, "stats.meta.users", "count", i64(st.Users))))
		}
		if updated != "" {
			b.WriteString("\n\n")
			b.WriteString(blockquote(i18n.T(lang, "stats.meta.updated", "value", updated)))
		}
	}

	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: tabMark(!global) + i18n.T(lang, "stats.button.personal"), CallbackData: cb(owner, "statsp")},
			{Text: tabMark(global) + i18n.T(lang, "stats.button.global"), CallbackData: cb(owner, "statsg")},
		},
		{
			{Text: i18n.T(lang, "action.back"), CallbackData: cb(owner, "home")},
			{Text: i18n.T(lang, "action.close"), CallbackData: cb(owner, "close")},
		},
	}}
	return b.String(), kb
}

// infoPanel — a title + blockquote(body) screen (help, about) with Back/Close.
func infoPanel(lang string, owner int64, titleKey, bodyKey string, bodyArgs ...string) (string, *models.InlineKeyboardMarkup) {
	text := header(lang, titleKey, "") + "\n\n" + blockquote(i18n.T(lang, bodyKey, bodyArgs...))
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: i18n.T(lang, "action.back"), CallbackData: cb(owner, "home")},
			{Text: i18n.T(lang, "action.close"), CallbackData: cb(owner, "close")},
		},
	}}
	return text, kb
}

func aboutBody(lang string, owner int64) (string, *models.InlineKeyboardMarkup) {
	return infoPanel(lang, owner, "about.title", "about.body", "version", aboutVersion(buildinfo.Version), "date", buildinfo.Date)
}

func aboutVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

// peakLabel renders an hour (0-23) as "HH:00", or "—" when there's no data.
func peakLabel(hour int) string {
	if hour < 0 || hour > 23 {
		return "—"
	}
	return fmt.Sprintf("%02d:00", hour)
}
