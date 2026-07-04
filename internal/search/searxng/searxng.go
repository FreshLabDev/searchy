// Package searxng implements a search.Provider backed by a self-hosted SearXNG
// instance via its JSON API (GET /search?format=json). It supports the images
// and videos categories and normalizes the (engine-dependent, frequently
// missing) fields into search.MediaResult.
//
// IMPORTANT operational notes baked into this client:
//   - SearXNG must have `search.formats: [html, json]` enabled, or it returns
//     HTTP 403 for format=json. We surface that as a clear error.
//   - For INLINE results Telegram fetches the media URL itself, so we use the
//     origin img_src (public HTTPS) rather than the SearXNG image proxy, which
//     would only be reachable if the instance itself were public.
package searxng

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"searchy/internal/search"
)

type Client struct {
	base          string
	http          *http.Client
	log           *slog.Logger
	enginesImages string
	enginesVideos string
	safeSearch    int
	imageProxy    bool
	language      string
}

type Options struct {
	BaseURL       string
	HTTP          *http.Client
	Logger        *slog.Logger
	EnginesImages string
	EnginesVideos string
	SafeSearch    int
	ImageProxy    bool
	Language      string
}

func New(o Options) *Client {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		base:          strings.TrimRight(o.BaseURL, "/"),
		http:          o.HTTP,
		log:           log,
		enginesImages: o.EnginesImages,
		enginesVideos: o.EnginesVideos,
		safeSearch:    o.SafeSearch,
		imageProxy:    o.ImageProxy,
		language:      o.Language,
	}
}

// rawResult mirrors the union of fields SearXNG emits across image/video
// engines. Every field is optional and engine-dependent — never assume presence.
type rawResult struct {
	Template     string          `json:"template"`
	URL          string          `json:"url"`
	Title        string          `json:"title"`
	Content      string          `json:"content"`
	Author       string          `json:"author"`
	ImgSrc       string          `json:"img_src"`
	ThumbnailSrc string          `json:"thumbnail_src"`
	Thumbnail    string          `json:"thumbnail"`
	IframeSrc    string          `json:"iframe_src"`
	Resolution   string          `json:"resolution"`
	ImgFormat    string          `json:"img_format"`
	Source       string          `json:"source"`
	Length       json.RawMessage `json:"length"` // string "3:21" or a number
	Engine       string          `json:"engine"`
	Engines      []string        `json:"engines"`
}

// engineOf returns the engine that produced a result (SearXNG may report a
// single `engine` or an `engines` list when several returned the same result).
func (r *rawResult) engineOf() string {
	if len(r.Engines) > 0 {
		return r.Engines[0]
	}
	return r.Engine
}

type response struct {
	Results      []rawResult     `json:"results"`
	Unresponsive json.RawMessage `json:"unresponsive_engines"`
}

// Search queries SearXNG for the given categories and page (0-based) and returns
// normalized results plus whether another page is likely available.
func (c *Client) Search(ctx context.Context, query string, cats []search.Category, page int) ([]search.MediaResult, bool, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	// IMPORTANT: pin a SMALL set of engines via `engines=` and DO NOT send
	// `categories`. `categories` is additive in SearXNG — it runs ALL enabled
	// engines for the category regardless of `engines`, firing ~15 engines at
	// once, which overloads this SearXNG instance's network layer (every engine
	// then fails with httpx.ProxyError; only single/few-engine queries succeed).
	// Sending only `engines` restricts to exactly these few → fast + reliable.
	if eng := c.enginesFor(cats); eng != "" {
		q.Set("engines", eng)
	} else {
		q.Set("categories", joinCategories(cats)) // fallback if no engines configured
	}
	q.Set("pageno", strconv.Itoa(page+1))
	q.Set("safesearch", strconv.Itoa(c.safeSearch))
	if c.language != "" {
		q.Set("language", c.language) // "all" = neutral, no language bias
	}
	if c.imageProxy {
		q.Set("image_proxy", "true")
	}

	endpoint := c.base + "/search?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	// A browser-like UA reduces the chance of SearXNG's bot detection rejecting us
	// even on a private instance with the limiter on.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; searchy-bot/1.0)")
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		// context.Canceled is the inline debouncer dropping a superseded query —
		// expected, not a failure. Never log the query text (full anonymity).
		if !errors.Is(err, context.Canceled) {
			c.log.Warn("searxng request failed", "ms", ms(start), "err", err)
		}
		return nil, false, err
	}
	// Always drain + close so the connection is reused.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusForbidden {
		c.log.Warn("searxng 403 (enable json format / check limiter)")
		return nil, false, fmt.Errorf("searxng 403: enable `search.formats: [html, json]` in settings.yml and restart, or whitelist this client in limiter.toml")
	}
	if resp.StatusCode != http.StatusOK {
		c.log.Warn("searxng non-200", "status", resp.StatusCode)
		return nil, false, fmt.Errorf("searxng status %d", resp.StatusCode)
	}

	var body response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		c.log.Warn("searxng decode failed", "err", err)
		return nil, false, fmt.Errorf("decode searxng json: %w", err)
	}

	out := make([]search.MediaResult, 0, len(body.Results))
	for i := range body.Results {
		if mr, ok := normalize(&body.Results[i]); ok {
			out = append(out, mr)
		}
	}

	unresponsive := strings.TrimSpace(string(body.Unresponsive))
	if unresponsive == "" || unresponsive == "[]" || unresponsive == "null" {
		c.log.Debug("searxng ok", "raw", len(body.Results), "usable", len(out), "ms", ms(start))
	} else {
		// Surface degraded engines — this is how we spot a flaky/blocked engine.
		// No query text logged (full anonymity).
		c.log.Warn("searxng degraded engines", "raw", len(body.Results), "usable", len(out), "ms", ms(start), "unresponsive", unresponsive)
	}

	// SearXNG doesn't report total pages reliably; if this page yielded results,
	// assume another page may exist (capped by the caller).
	return out, len(body.Results) > 0, nil
}

func ms(start time.Time) int64 { return time.Since(start).Milliseconds() }

// trustedOrigin lists image engines whose origin img_src is itself a reliable
// CDN (reachable by Telegram, reasonably sized) and higher-res than the
// thumbnail — for these we send the origin; for all others we send the CDN
// thumbnail_src (which Telegram can always fetch).
var trustedOrigin = map[string]bool{
	"unsplash": true,
	"flickr":   true,
}

func normalize(r *rawResult) (search.MediaResult, bool) {
	switch r.Template {
	case "images.html":
		// Telegram fetches photo_url server-side to SEND the image; it must be a
		// reachable JPEG <=5MB. Origin img_src often FAILS this (hotlink 403, or
		// huge — e.g. wikimedia originals are 16MB), which is why "some photos
		// don't load when selected". The CDN thumbnail_src is reliable & small,
		// so we use it as the photo for engines whose origin is unreliable, and
		// keep the higher-res origin only for engines with their own reliable CDN.
		eng := r.engineOf()
		photo := r.ThumbnailSrc
		if trustedOrigin[eng] && validURL(r.ImgSrc) {
			photo = r.ImgSrc
		}
		if !validURL(photo) {
			photo = r.ImgSrc // last resort
		}
		if !validURL(photo) {
			return search.MediaResult{}, false
		}
		thumb := r.ThumbnailSrc
		if !validURL(thumb) {
			thumb = photo
		}
		return search.MediaResult{
			Category: search.CatImage,
			ID:       hashID(firstNonEmpty(r.ImgSrc, photo)),
			Title:    clip(r.Title, 120),
			Author:   r.Source,
			MediaURL: photo,
			ThumbURL: thumb,
			PageURL:  r.URL,
			Engine:   eng,
		}, true

	case "videos.html":
		// Videos are rendered as a cover card (cover photo + "Open" button), so we
		// only need a usable cover (HTTPS, for Telegram to fetch it inline) and a
		// page link — the platform/embed is irrelevant. This deliberately accepts
		// videos from ANY platform (YouTube, TikTok, PeerTube, Dailymotion, …),
		// not just ones that expose an embeddable iframe.
		// The cover becomes the inline photo (Telegram validates it strictly) and
		// the page becomes the "Open" button URL (also validated) — both must be
		// strictly-valid HTTPS URLs or the whole answerInlineQuery is rejected.
		thumb := firstNonEmpty(r.Thumbnail, r.ThumbnailSrc)
		page := firstNonEmpty(r.URL, r.IframeSrc)
		if !validURL(thumb) || !validURL(page) {
			return search.MediaResult{}, false
		}
		return search.MediaResult{
			Category:    search.CatVideo,
			ID:          hashID(page),
			Title:       clip(firstNonEmpty(r.Title, "Video"), 120),
			Author:      firstNonEmpty(r.Author, host(page)), // show the platform if no author
			ThumbURL:    thumb,
			PageURL:     page,
			DurationSec: parseDuration(r.Length),
			Engine:      r.engineOf(),
		}, true
	}
	return search.MediaResult{}, false
}

func (c *Client) enginesFor(cats []search.Category) string {
	parts := make([]string, 0, 2)
	for _, cat := range cats {
		switch cat {
		case search.CatImage:
			if c.enginesImages != "" {
				parts = append(parts, c.enginesImages)
			}
		case search.CatVideo:
			if c.enginesVideos != "" {
				parts = append(parts, c.enginesVideos)
			}
		}
	}
	return strings.Join(parts, ",")
}

func joinCategories(cats []search.Category) string {
	s := make([]string, len(cats))
	for i, c := range cats {
		s[i] = string(c)
	}
	return strings.Join(s, ",")
}

// parseDuration accepts SearXNG's `length` field which may be a JSON string
// ("3:21", "1:02:33") or a number of seconds, and returns whole seconds.
func parseDuration(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	// Numeric form.
	if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	parts := strings.Split(s, ":")
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

func hashID(s string) string {
	sum := sha1.Sum([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:]) // 27 chars, well under 64
}

// validURL reports whether u is a strictly-valid HTTPS URL that Telegram will
// accept as an inline media/button URL. Telegram rejects the ENTIRE
// answerInlineQuery (WEBDOCUMENT_URL_INVALID / BUTTON_URL_INVALID) if even one
// result carries a malformed URL, so this is deliberately strict: https scheme,
// a real host, no spaces/control/unsafe characters, and a sane length.
func validURL(raw string) bool {
	if len(raw) < len("https://a.b") || len(raw) > 2000 {
		return false
	}
	if !strings.HasPrefix(raw, "https://") {
		return false
	}
	if strings.ContainsAny(raw, " \t\r\n\"<>\\^`{|}") {
		return false
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f { // control chars
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !strings.Contains(u.Host, ".") {
		return false
	}
	return true
}

// host returns the bare hostname (without "www.") of a URL, e.g. "tiktok.com".
func host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// clip truncates to at most max RUNES (not bytes), so cutting a long non-Latin
// title (Cyrillic/CJK/Arabic) never lands mid-rune and emits a U+FFFD.
func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(r[:max]))
}
