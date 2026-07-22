// Package searxng implements the bounded SearXNG JSON provider used by Searchy.
// It sends explicit engine pools only and never sends categories.
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
	base                   string
	http                   *http.Client
	log                    *slog.Logger
	enginesImages          string
	enginesVideos          string
	enginesImagesDiscovery string
	enginesVideosDiscovery string
	safeSearch             int
	imageProxy             bool
	language               string
}

type Options struct {
	BaseURL                string
	HTTP                   *http.Client
	Logger                 *slog.Logger
	EnginesImages          string
	EnginesVideos          string
	EnginesImagesDiscovery string
	EnginesVideosDiscovery string
	SafeSearch             int
	ImageProxy             bool
	Language               string
}

func New(o Options) *Client {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		base:                   strings.TrimRight(o.BaseURL, "/"),
		http:                   o.HTTP,
		log:                    log,
		enginesImages:          o.EnginesImages,
		enginesVideos:          o.EnginesVideos,
		enginesImagesDiscovery: o.EnginesImagesDiscovery,
		enginesVideosDiscovery: o.EnginesVideosDiscovery,
		safeSearch:             o.SafeSearch,
		imageProxy:             o.ImageProxy,
		language:               strings.TrimSpace(o.Language),
	}
}

// rawResult mirrors the union of fields emitted by image and video engines.
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
	Width        json.RawMessage `json:"width"`
	Height       json.RawMessage `json:"height"`
	ImgFormat    string          `json:"img_format"`
	Source       string          `json:"source"`
	Length       json.RawMessage `json:"length"`
	Engine       string          `json:"engine"`
	Engines      []string        `json:"engines"`
	Score        float64         `json:"score"`
	Positions    []int           `json:"positions"`
}

func (r *rawResult) enginesOf() []string {
	engines := uniqueStrings(r.Engines)
	if len(engines) == 0 && strings.TrimSpace(r.Engine) != "" {
		engines = []string{strings.TrimSpace(r.Engine)}
	}
	return engines
}

func (r *rawResult) engineOf() string {
	engines := r.enginesOf()
	if len(engines) > 0 {
		return engines[0]
	}
	return ""
}

type response struct {
	Results      []rawResult     `json:"results"`
	Unresponsive json.RawMessage `json:"unresponsive_engines"`
}

// Search executes one pool request. POST keeps the query out of access-log URLs.
func (c *Client) Search(ctx context.Context, request search.SearchRequest) (search.SearchResponse, error) {
	engines := c.enginesFor(request.Pool, request.Categories)
	if engines == "" {
		return search.SearchResponse{}, fmt.Errorf("no pinned SearXNG engines configured for %s pool", request.Pool)
	}

	language := request.Language
	if c.language != "" {
		language = c.language
	}
	form := url.Values{}
	form.Set("q", request.Query)
	form.Set("format", "json")
	form.Set("engines", engines)
	form.Set("pageno", strconv.Itoa(request.Page+1))
	form.Set("safesearch", strconv.Itoa(c.safeSearch))
	if language != "" {
		form.Set("language", language)
	}
	if c.imageProxy {
		form.Set("image_proxy", "true")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/search", strings.NewReader(form.Encode()))
	if err != nil {
		return search.SearchResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "searchy-bot/0.2")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			c.log.Warn("searxng request failed", "pool", request.Pool, "language", language, "ms", ms(start), "err", err)
		}
		return search.SearchResponse{}, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusForbidden {
		c.log.Warn("searxng 403 (enable json format / check limiter)", "pool", request.Pool, "language", language)
		return search.SearchResponse{}, fmt.Errorf("searxng 403: enable `search.formats: [html, json]` in settings.yml and restart, or whitelist this client in limiter.toml")
	}
	if resp.StatusCode != http.StatusOK {
		c.log.Warn("searxng non-200", "pool", request.Pool, "language", language, "status", resp.StatusCode)
		return search.SearchResponse{}, fmt.Errorf("searxng status %d", resp.StatusCode)
	}

	var body response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		c.log.Warn("searxng decode failed", "pool", request.Pool, "language", language, "err", err)
		return search.SearchResponse{}, fmt.Errorf("decode searxng json: %w", err)
	}

	out := make([]search.MediaResult, 0, len(body.Results))
	for i := range body.Results {
		if result, ok := normalize(&body.Results[i], request.Pool, i); ok {
			out = append(out, result)
		}
	}
	degraded := hasUnresponsive(body.Unresponsive)
	level := c.log.Debug
	message := "searxng ok"
	if degraded {
		level = c.log.Warn
		message = "searxng degraded engines"
	}
	level(message,
		"pool", request.Pool,
		"language", language,
		"raw", len(body.Results),
		"usable", len(out),
		"ms", ms(start),
		"unresponsive", unresponsiveValue(body.Unresponsive),
	)
	return search.SearchResponse{
		Results: out, HasMore: len(body.Results) > 0, RawCount: len(body.Results), Degraded: degraded,
	}, nil
}

func ms(start time.Time) int64 { return time.Since(start).Milliseconds() }

var trustedOrigin = map[string]bool{
	"unsplash": true,
	"flickr":   true,
}

func normalize(r *rawResult, pool search.Pool, order int) (search.MediaResult, bool) {
	engine := r.engineOf()
	engines := r.enginesOf()
	width, height := dimensions(r.Width, r.Height, r.Resolution)
	base := search.MediaResult{
		Title:       clip(r.Title, 120),
		Engine:      engine,
		Engines:     engines,
		Score:       r.Score,
		Positions:   append([]int(nil), r.Positions...),
		Pool:        pool,
		Width:       width,
		Height:      height,
		SourceOrder: order,
	}

	switch r.Template {
	case "images.html":
		origin := firstValidURL(r.ImgSrc)
		thumb := firstValidURL(r.ThumbnailSrc, r.Thumbnail, origin)
		photo := thumb
		if trustedOrigin[engine] && origin != "" {
			photo = origin
		}
		if photo == "" {
			photo = origin
		}
		if photo == "" {
			return search.MediaResult{}, false
		}
		if thumb == "" {
			thumb = photo
		}
		base.Category = search.CatImage
		base.ID = hashID(firstNonEmpty(origin, photo))
		base.Author = r.Source
		base.MediaURL = photo
		base.ThumbURL = thumb
		base.PageURL = firstValidURL(r.URL)
		return base, true

	case "videos.html":
		thumb := firstValidURL(r.Thumbnail, r.ThumbnailSrc, r.ImgSrc)
		page := firstValidURL(r.URL, r.IframeSrc)
		if thumb == "" || page == "" {
			return search.MediaResult{}, false
		}
		base.Category = search.CatVideo
		base.ID = hashID(page)
		base.Title = clip(firstNonEmpty(r.Title, "Video"), 120)
		base.Author = firstNonEmpty(r.Author, host(page))
		base.ThumbURL = thumb
		base.PageURL = page
		base.DurationSec = parseDuration(r.Length)
		return base, true
	}
	return search.MediaResult{}, false
}

func (c *Client) enginesFor(pool search.Pool, categories []search.Category) string {
	parts := make([]string, 0, 8)
	for _, category := range categories {
		var configured string
		switch {
		case pool == search.PoolCore && category == search.CatImage:
			configured = c.enginesImages
		case pool == search.PoolCore && category == search.CatVideo:
			configured = c.enginesVideos
		case pool == search.PoolDiscovery && category == search.CatImage:
			configured = c.enginesImagesDiscovery
		case pool == search.PoolDiscovery && category == search.CatVideo:
			configured = c.enginesVideosDiscovery
		}
		parts = append(parts, strings.Split(configured, ",")...)
	}
	return strings.Join(uniqueStrings(parts), ",")
}

func parseDuration(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
		return n
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if n, err := strconv.Atoi(value); err == nil {
		return n
	}
	parts := strings.Split(value, ":")
	total := 0
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

func dimensions(widthRaw, heightRaw json.RawMessage, resolution string) (int, int) {
	width, height := rawInt(widthRaw), rawInt(heightRaw)
	if width > 0 && height > 0 {
		return width, height
	}
	resolution = strings.NewReplacer("×", "x", "X", "x", " ", "").Replace(resolution)
	parts := strings.SplitN(resolution, "x", 2)
	if len(parts) == 2 {
		if width <= 0 {
			width, _ = strconv.Atoi(parts[0])
		}
		if height <= 0 {
			height, _ = strconv.Atoi(parts[1])
		}
	}
	return max(width, 0), max(height, 0)
}

func rawInt(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if value, err := strconv.Atoi(number.String()); err == nil {
			return value
		}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		n, _ := strconv.Atoi(strings.TrimSpace(value))
		return n
	}
	return 0
}

func hashID(value string) string {
	sum := sha1.Sum([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

var httpsUpgradeHosts = []string{
	"bilibili.com",
	"hdslb.com",
	"bilivideo.com",
}

func firstValidURL(values ...string) string {
	for _, value := range values {
		if normalized := normalizeHTTPS(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

func normalizeHTTPS(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if strings.EqualFold(u.Scheme, "http") && canUpgradeHost(u.Hostname()) {
		u.Scheme = "https"
		raw = u.String()
	}
	if !validURL(raw) {
		return ""
	}
	return raw
}

func canUpgradeHost(raw string) bool {
	hostname := strings.ToLower(strings.TrimSuffix(raw, "."))
	for _, allowed := range httpsUpgradeHosts {
		if hostname == allowed || strings.HasSuffix(hostname, "."+allowed) {
			return true
		}
	}
	return false
}

func validURL(raw string) bool {
	if len(raw) < len("https://a.b") || len(raw) > 2000 || !strings.HasPrefix(raw, "https://") {
		return false
	}
	if strings.ContainsAny(raw, " \t\r\n\"<>\\^`{|}") {
		return false
	}
	for _, char := range raw {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.Host != "" && strings.Contains(u.Hostname(), ".")
}

func host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func clip(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func hasUnresponsive(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "[]" && trimmed != "null"
}

func unresponsiveValue(raw json.RawMessage) string {
	if !hasUnresponsive(raw) {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
