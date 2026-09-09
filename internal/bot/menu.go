package bot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/FreshLabDev/tg"

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

// navRow is the row every subordinate panel ends with.
//
// Close only exists in a group. In a DM the conversation *is* the panel:
// there is nothing else in the chat and nothing to close, so the button
// offers to delete the only thing on screen. In a group the panel is one
// person's menu sitting in everyone else's feed, and leaving it there is the
// rude default — so there the button is not optional.
func navRow(lang string, owner int64, inGroup bool) []tg.InlineKeyboardButton {
	row := []tg.InlineKeyboardButton{
		{Text: i18n.T(lang, "action.back"), CallbackData: cb(owner, "home")},
	}
	if inGroup {
		row = append(row, tg.InlineKeyboardButton{
			Text: i18n.T(lang, "action.close"), CallbackData: cb(owner, "close"),
		})
	}
	return row
}

// homePanel — the /start landing panel. A DM and a group get different
// screens, the way vido splits its personal and group section builders: a DM
// is the user's own space, so language and personal statistics live there and
// there is nothing to close; a group is shared, so the panel offers searching,
// help and a way out, and sends the personal settings to a DM.
func homePanel(lang, botUsername, vidoBotUsername string, owner int64, inGroup bool) (string, *tg.InlineKeyboardMarkup) {
	if inGroup {
		return groupHome(lang, botUsername, owner)
	}
	return personalHome(lang, botUsername, vidoBotUsername, owner)
}

func personalHome(lang, botUsername, vidoBotUsername string, owner int64) (string, *tg.InlineKeyboardMarkup) {
	text := header(lang, "home.title", "home.tagline") + "\n\n" +
		blockquote(i18n.T(lang, "home.hint", "bot", botUsername))
	rows := [][]tg.InlineKeyboardButton{
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
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text: i18n.T(lang, "btn.video_settings"),
			URL:  "https://t.me/" + vidoBotUsername + "?start=settings",
		}})
	}
	return text, &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func groupHome(lang, botUsername string, owner int64) (string, *tg.InlineKeyboardMarkup) {
	text := header(lang, "home.title", "home.tagline") + "\n\n" +
		blockquote(i18n.T(lang, "home.hint.group", "bot", botUsername))
	rows := [][]tg.InlineKeyboardButton{
		// Inline search is the one surface that works for everyone in the
		// group at once, so it leads.
		{{Text: i18n.T(lang, "btn.search"), SwitchInlineQueryCurrentChat: strptr("")}},
		{
			{Text: i18n.T(lang, "btn.help"), CallbackData: cb(owner, "help")},
			{Text: i18n.T(lang, "btn.about"), CallbackData: cb(owner, "about")},
		},
	}
	if botUsername != "" {
		// Language and personal statistics are personal, so the group panel
		// points at the DM rather than putting one member's settings in
		// everyone's feed.
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text: i18n.T(lang, "btn.open_dm"),
			URL:  "https://t.me/" + botUsername,
		}})
	}
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text: i18n.T(lang, "action.close"), CallbackData: cb(owner, "close"),
	}})
	return text, &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// languagePanel — the language picker (2 per row), current one marked with ◉.
func languagePanel(lang string, owner int64, inGroup bool) (string, *tg.InlineKeyboardMarkup) {
	text := header(lang, "language.title", "language.hint")
	opts := i18n.LANGUAGE_OPTIONS
	var rows [][]tg.InlineKeyboardButton
	for i := 0; i < len(opts); i += 2 {
		row := []tg.InlineKeyboardButton{
			{Text: curMark(opts[i].Code == lang) + opts[i].Label, CallbackData: cb(owner, "l|"+opts[i].Code)},
		}
		if i+1 < len(opts) {
			row = append(row, tg.InlineKeyboardButton{
				Text: curMark(opts[i+1].Code == lang) + opts[i+1].Label, CallbackData: cb(owner, "l|"+opts[i+1].Code),
			})
		}
		rows = append(rows, row)
	}
	rows = append(rows, navRow(lang, owner, inGroup))
	return text, &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// statsPanel — personal or global stats (vido formatting). No query text anywhere.
func statsPanel(lang string, owner int64, st db.Stats, global bool, updated string, inGroup bool) (string, *tg.InlineKeyboardMarkup) {
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

	kb := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{
			{Text: tabMark(!global) + i18n.T(lang, "stats.button.personal"), CallbackData: cb(owner, "statsp")},
			{Text: tabMark(global) + i18n.T(lang, "stats.button.global"), CallbackData: cb(owner, "statsg")},
		},
		navRow(lang, owner, inGroup),
	}}
	return b.String(), kb
}

// infoPanel — a title + blockquote(body) screen (help) with Back, and Close
// only where there is something to close.
func infoPanel(lang string, owner int64, inGroup bool, titleKey, bodyKey string, bodyArgs ...string) (string, *tg.InlineKeyboardMarkup) {
	text := header(lang, titleKey, "") + "\n\n" + blockquote(i18n.T(lang, bodyKey, bodyArgs...))
	kb := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		navRow(lang, owner, inGroup),
	}}
	return text, kb
}

// aboutPanel is the family's About screen: name and version on one line, one
// line of purpose, then a blockquote of "key · value" facts. The repository is
// a link inside that text rather than a button of its own — two controls for
// one action is one too many.
func aboutPanel(lang string, owner int64, inGroup bool) (string, *tg.InlineKeyboardMarkup) {
	var b strings.Builder
	b.WriteString("<b>Searchy</b> · <i>v" + aboutVersion(buildinfo.Version) + "</i>\n")
	b.WriteString(i18n.T(lang, "about.tagline"))
	b.WriteString("\n\n")
	b.WriteString(blockquote(
		aboutField(lang, "about.field.search", i18n.T(lang, "about.value.search")),
		aboutField(lang, "about.field.source", `<a href="https://github.com/FreshLabDev/searchy">FreshLabDev/searchy</a> · Apache-2.0`),
		aboutField(lang, "about.field.admin", `<a href="https://t.me/amtiyo">@amtiyo</a>`),
	))
	kb := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		navRow(lang, owner, inGroup),
	}}
	return b.String(), kb
}

func aboutField(lang, labelKey, value string) string {
	return i18n.T(lang, labelKey) + " · " + value
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
