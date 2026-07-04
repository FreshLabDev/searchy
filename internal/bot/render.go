package bot

import (
	"fmt"
	"net/url"
	"strings"

	"searchy/internal/i18n"
	"searchy/internal/search"
)

// videoCaption builds an HTML caption for a video card: bold title + a metadata
// line (author · duration).
func videoCaption(r search.MediaResult) string {
	var b strings.Builder
	if r.Title != "" {
		b.WriteString("<b>")
		b.WriteString(escapeHTML(r.Title))
		b.WriteString("</b>")
	}
	if meta := metaLine(r); meta != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(escapeHTML(meta)) // metaLine is plain text; escape for the HTML caption
	}
	return clipRunes(b.String(), 1024)
}

// openLabel mirrors vido's source-button style ("🔗 Open on YouTube" /
// "🔗 Open original"), localized to the user's language. The platform name stays
// as-is; only the surrounding words are translated.
func openLabel(pageURL, lang string) string {
	host := hostOf(pageURL)
	switch {
	case strings.Contains(host, "youtube") || strings.Contains(host, "youtu.be"):
		return i18n.T(lang, "btn.open_platform", "platform", "YouTube")
	case strings.Contains(host, "dailymotion") || host == "dai.ly":
		return i18n.T(lang, "btn.open_platform", "platform", "Dailymotion")
	case strings.Contains(host, "vimeo"):
		return i18n.T(lang, "btn.open_platform", "platform", "Vimeo")
	default:
		return i18n.T(lang, "btn.open_original")
	}
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
}

// escapeHTML escapes the characters Telegram's HTML parse mode treats specially.
func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func metaLine(r search.MediaResult) string {
	parts := make([]string, 0, 3)
	// Plain text (no HTML escaping): used both as a plain inline Description and,
	// after escaping at the call site, inside an HTML caption.
	switch r.Category {
	case search.CatVideo:
		if r.Author != "" {
			parts = append(parts, r.Author)
		}
		if r.DurationSec > 0 {
			parts = append(parts, fmtDuration(r.DurationSec))
		}
	case search.CatImage:
		if r.Author != "" {
			parts = append(parts, r.Author)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func fmtDuration(sec int) string {
	if sec <= 0 {
		return ""
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// clipRunes truncates to at most max runes (not bytes) to avoid splitting a
// multi-byte character and to respect Telegram's character-based caption limit.
func clipRunes(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
