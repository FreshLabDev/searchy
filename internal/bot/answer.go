package bot

import (
	"github.com/go-telegram/bot/models"

	"searchy/internal/i18n"
	"searchy/internal/search"
)

// buildInlineResults maps normalized media results to Telegram inline results.
//
//   - Images -> InlineQueryResultPhoto (origin JPEG URL), no caption.
//   - Videos -> InlineQueryResultPhoto of the COVER (thumbnail), with the watch
//     link and a Download button in the keyboard. We deliberately send the cover
//     as a photo rather than a bare link/embed, so the chat shows a clean card
//     (cover + title) with the link tucked into a button (vido style).
func buildInlineResults(results []search.MediaResult, lang string) []models.InlineQueryResult {
	out := make([]models.InlineQueryResult, 0, len(results))
	for _, r := range results {
		switch r.Category {
		case search.CatImage:
			// No caption on images — the photo speaks for itself. Title shows only
			// in the inline picker, never on the sent message.
			out = append(out, &models.InlineQueryResultPhoto{
				ID:           r.ID,
				PhotoURL:     r.MediaURL,
				ThumbnailURL: r.ThumbURL,
				Title:        r.Title,
			})

		case search.CatVideo:
			out = append(out, &models.InlineQueryResultPhoto{
				ID:           r.ID,
				PhotoURL:     r.ThumbURL, // the cover
				ThumbnailURL: r.ThumbURL,
				Title:        r.Title,
				Description:  metaLine(r),
				Caption:      videoCaption(r),
				ParseMode:    models.ParseModeHTML,
				ReplyMarkup:  videoButtons(r, lang),
			})
		}
	}
	return out
}

// videoButtons builds the video card keyboard: a "🔗 Open on <platform>" link
// button (vido style) and a "⬇️ Download" button. The Download button is a
// placeholder (callback "dl") until the @vido handoff is wired — see internal/vido.
func videoButtons(r search.MediaResult, lang string) *models.InlineKeyboardMarkup {
	rows := make([][]models.InlineKeyboardButton, 0, 2)
	if r.PageURL != "" {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: openLabel(r.PageURL, lang), URL: r.PageURL},
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: i18n.T(lang, "btn.download"), CallbackData: "dl"},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// promptResult is shown when the inline query is empty, nudging the user to type.
func promptResult(lang, botUsername string) []models.InlineQueryResult {
	return []models.InlineQueryResult{
		&models.InlineQueryResultArticle{
			ID:          "prompt",
			Title:       i18n.T(lang, "inline.prompt.title"),
			Description: i18n.T(lang, "inline.prompt.desc"),
			InputMessageContent: models.InputTextMessageContent{
				MessageText: i18n.T(lang, "help.hint"),
			},
		},
	}
}
