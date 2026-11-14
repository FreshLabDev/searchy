package native

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"

	"searchy/internal/search"
)

func validHTTPS(raw string) bool {
	if len(raw) == 0 || len(raw) > 4096 || strings.ContainsAny(raw, "\\ \t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Host == "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(host, ".") || !strings.Contains(host, ".") {
		return false
	}
	// Providers never need literal hosts. Reject them, including IPv4-mapped IPv6,
	// rather than let Telegram or an image downloader reach a private endpoint.
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, suffix := range []string{".local", ".localhost", ".internal", ".lan", ".home", ".onion", ".test", ".invalid"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	numeric := true
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if r < '0' || r > '9' {
				numeric = false
			}
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return false
			}
		}
	}
	return !numeric && len(host) <= 253
}

func photoURL(raw string) string {
	if !validHTTPS(raw) {
		return ""
	}
	u, _ := url.Parse(raw)
	ext := strings.ToLower(path.Ext(u.Path))
	for _, key := range []string{"format", "fm", "fmt", "ext"} {
		if v := strings.ToLower(u.Query().Get(key)); v != "" && v != "jpg" && v != "jpeg" && v != "pjpg" {
			return ""
		}
	}
	switch ext {
	case ".svg", ".gif", ".webp", ".avif", ".png", ".bmp", ".tif", ".tiff", ".pdf", ".mp4", ".webm":
		return ""
	case ".jpg", ".jpeg":
		return raw
	}
	host := strings.ToLower(u.Hostname())
	if bingThumbnail(u) {
		return raw
	}
	if strings.HasSuffix(host, ".ytimg.com") || host == "i.ytimg.com" {
		return raw
	}
	// These origins explicitly negotiate JPEG through a documented format flag.
	if (host == "images.unsplash.com" || host == "images.pexels.com") && (u.Query().Get("fm") == "jpg" || u.Query().Get("format") == "jpg") {
		return raw
	}
	return ""
}

func firstPhoto(values ...string) string {
	for _, v := range values {
		if photo := photoURL(v); photo != "" {
			return photo
		}
	}
	return ""
}

func providerJPEGPreview(raw string) bool {
	if photoURL(raw) == "" {
		return false
	}
	u, _ := url.Parse(raw)
	return bingThumbnail(u)
}

// bingThumbnail matches Bing's JPEG thumbnail service in both of its URL shapes:
// /th?id=OIP.x (Bing Images) and /th/id/OVP.x (Bing video covers, also used by
// DuckDuckGo Videos).
func bingThumbnail(u *url.URL) bool {
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), ".mm.bing.net") {
		return false
	}
	if u.Path == "/th" || u.Path == "/th/" {
		return u.Query().Get("id") != ""
	}
	id, ok := strings.CutPrefix(u.Path, "/th/id/")
	return ok && id != "" && !strings.Contains(id, "/")
}

func resultID(category search.Category, raw string) string {
	identity := canonicalURL(raw)
	if category == search.CatVideo {
		identity = canonicalVideoURL(raw)
	}
	sum := sha256.Sum256([]byte(string(category) + "\x00" + identity))
	return hex.EncodeToString(sum[:12])
}

func canonicalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Host = strings.ToLower(u.Host)
	if u.Port() == "443" && u.Scheme == "https" {
		u.Host = u.Hostname()
	}
	u.Fragment = ""
	q := u.Query()
	for key := range q {
		if strings.HasPrefix(strings.ToLower(key), "utm_") || key == "fbclid" || key == "gclid" {
			q.Del(key)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func canonicalVideoURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if validHTTPS(raw) {
		id := ""
		switch strings.ToLower(u.Hostname()) {
		case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com":
			if u.Path == "/watch" {
				q, err := url.ParseQuery(u.RawQuery)
				if err == nil && len(q["v"]) == 1 {
					id = q.Get("v")
				}
			} else {
				parts := strings.Split(u.Path, "/")
				if len(parts) == 3 && (parts[1] == "shorts" || parts[1] == "embed" || parts[1] == "live") {
					id = parts[2]
				}
			}
		case "youtu.be":
			id = strings.TrimPrefix(u.Path, "/")
		}
		if validYouTubeID(id) {
			return "https://www.youtube.com/watch?v=" + id
		}
	}
	// Unknown platforms and routes may use any query parameter or fragment as
	// content identity. Preserve them rather than applying YouTube's rules.
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

func validYouTubeID(id string) bool {
	if len(id) != 11 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func photoGeometry(width, height int) bool {
	return width > 0 && height > 0 && width <= 10000 && height <= 10000 && width+height <= 10000 && max(width, height) <= 20*min(width, height)
}

func mediaResult(category search.Category, engine, title, original, thumb, page string, order, width, height int) (search.MediaResult, bool) {
	originalPhoto := ""
	if category == search.CatImage {
		if photoGeometry(width, height) {
			originalPhoto = photoURL(original)
		}
		thumb = photoURL(thumb)
		if originalPhoto == "" && thumb != "" && canonicalURL(thumb) == canonicalURL(original) {
			thumb = ""
		}
		thumb = firstPhoto(thumb, originalPhoto)
	} else {
		thumb = firstPhoto(thumb, original)
	}
	if thumb == "" || !validHTTPS(page) {
		return search.MediaResult{}, false
	}
	media := thumb
	key := original
	if category == search.CatVideo {
		key = page
	} else if category == search.CatImage {
		// A JPEG origin keeps full quality when its advertised dimensions fit
		// Telegram. Unknown or extreme dimensions use the provider's JPEG preview.
		if originalPhoto != "" {
			media = originalPhoto
		} else {
			// Source dimensions describe the original, not this separate preview.
			width, height = 0, 0
		}
	} else {
		return search.MediaResult{}, false
	}
	if !validHTTPS(key) {
		key = media
	}
	title = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\ue000' || r == '\ue001' {
			return -1
		}
		return r
	}, title))
	if title == "" {
		title = "Image"
		if category == search.CatVideo {
			title = "Video"
		}
	}
	if runes := []rune(title); len(runes) > 120 {
		title = string(runes[:120])
	}
	u, _ := url.Parse(page)
	return search.MediaResult{
		Category: category, ID: resultID(category, key), Title: title,
		MediaURL: media, ThumbURL: thumb, PageURL: page, Author: u.Hostname(),
		Engine: engine, Engines: []string{engine}, SourceOrder: max(order, 0),
		Width: max(width, 0), Height: max(height, 0), Pool: search.PoolCore,
	}, true
}

func duration(raw string) int {
	if len(raw) > 12 {
		return 0
	}
	total := 0
	for _, part := range strings.Split(raw, ":") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 || n > 86400 {
			return 0
		}
		total = total*60 + n
		if total > 86400 {
			return 0
		}
	}
	return total
}
