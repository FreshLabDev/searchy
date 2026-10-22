// Package native provides bounded direct image and video search sources.
package native

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"searchy/internal/search"
)

type Options struct {
	HTTP       *http.Client
	SafeSearch int
}

// NewSources shares transport limits and DDG token acquisition across categories.
// Commons has no adult-content filter and is excluded from filtered searches.
func NewSources(o Options) []search.Source {
	h := newFetcher(o.HTTP)
	safe := o.SafeSearch
	if safe < 0 || safe > 2 {
		safe = 2
	}
	ddg := &duckDuckGo{http: h, safe: safe, entries: make(map[[32]byte]*tokenEntry)}
	_, _ = rand.Read(ddg.salt[:])
	sources := []search.Source{
		&bing{http: h, safe: safe, category: search.CatImage},
		&duckSource{ddg: ddg, category: search.CatImage},
		&bing{http: h, safe: safe, category: search.CatVideo},
		&duckSource{ddg: ddg, category: search.CatVideo},
	}
	if safe == 0 {
		sources = append(sources, &wikimedia{http: h})
	}
	return sources
}

const (
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	maxBody        = 4 << 20
	maxOffset      = 10000
	pageSize       = 50
	requestTimeout = 8 * time.Second
)

var (
	errUpstream = errors.New("search upstream unavailable")
	errResponse = errors.New("invalid search response")
	errTooLarge = errors.New("search response too large")
)

type fetcher struct {
	client http.Client
	active chan struct{}
}

func newFetcher(client *http.Client) *fetcher {
	var c http.Client
	if client != nil {
		c = *client
	}
	// Do not inherit a cookie jar that could retain query-bearing redirects.
	c.Jar = nil
	previous := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || len(via) == 0 || !allowedUpstream(req.URL) || req.URL.Hostname() != via[0].URL.Hostname() {
			return errUpstream
		}
		if previous != nil {
			if err := previous(req, via); err != nil {
				return errUpstream
			}
		}
		return nil
	}
	return &fetcher{client: c, active: make(chan struct{}, 8)}
}

func allowedUpstream(u *url.URL) bool {
	if u == nil || !validHTTPS(u.String()) {
		return false
	}
	switch u.Hostname() {
	case "www.bing.com", "duckduckgo.com", "commons.wikimedia.org":
		return true
	}
	return false
}

func (h *fetcher) get(ctx context.Context, raw, language, cookie string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	select {
	case h.active <- struct{}{}:
		defer func() { <-h.active }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil || !allowedUpstream(req.URL) {
		return nil, errUpstream
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json,text/html;q=0.9,*/*;q=0.5")
	req.Header.Set("Accept-Language", language)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if req.URL.Hostname() == "duckduckgo.com" {
		req.Header.Set("Referer", "https://duckduckgo.com/")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errUpstream
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search upstream HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBody {
		return nil, errTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if len(body) > maxBody {
		return nil, errTooLarge
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errUpstream
	}
	return body, nil
}

func numericCursor(raw string, initial int) (int, error) {
	if raw == "" {
		return initial, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < initial || n > maxOffset || strconv.Itoa(n) != raw {
		return 0, search.ErrInvalidCursor
	}
	return n, nil
}

type locale struct {
	language string
	country  string
	market   string
	duck     string
}

var locales = map[string]locale{
	"en": {"en", "US", "en-US", "us-en"},
	"ru": {"ru", "RU", "ru-RU", "ru-ru"},
	"uk": {"uk", "UA", "uk-UA", "ua-uk"},
	"es": {"es", "ES", "es-ES", "es-es"},
	"fr": {"fr", "FR", "fr-FR", "fr-fr"},
	"de": {"de", "DE", "de-DE", "de-de"},
	"it": {"it", "IT", "it-IT", "it-it"},
	"pl": {"pl", "PL", "pl-PL", "pl-pl"},
	"cs": {"cs", "CZ", "cs-CZ", "cz-cs"},
	"tr": {"tr", "TR", "tr-TR", "tr-tr"},
	"sv": {"sv", "SE", "sv-SE", "se-sv"},
	"be": {"be", "BY", "be-BY", "wt-wt"},
	"ca": {"ca", "ES", "ca-ES", "es-ca"},
	"zh": {"zh", "CN", "zh-CN", "cn-zh"},
	"ja": {"ja", "JP", "ja-JP", "jp-jp"},
	"ar": {"ar", "SA", "ar-SA", "xa-ar"},
}

func resolveLocale(raw string) locale {
	raw = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "_", "-")
	if raw == "all" {
		return locale{duck: "wt-wt"}
	}
	parts := strings.Split(raw, "-")
	l, ok := locales[parts[0]]
	if !ok {
		return locales["en"]
	}
	// Preserve an operator's region override, including script-qualified Chinese.
	for _, part := range parts[1:] {
		if len(part) == 2 && part[0] >= 'a' && part[0] <= 'z' && part[1] >= 'a' && part[1] <= 'z' {
			l.country = strings.ToUpper(part)
			l.market = l.language + "-" + l.country
			l.duck = part + "-" + l.language
			if l.language == "ja" {
				l.duck = "jp-jp"
			}
			if l.language == "zh" && (part == "tw" || part == "hk") {
				l.duck = part + "-tzh"
			}
			if l.language == "ar" {
				l.duck = "xa-ar"
			}
			if l.language == "be" {
				l.duck = "wt-wt"
			}
		}
	}
	return l
}

func (l locale) acceptLanguage() string {
	if l.market == "" {
		return "*"
	}
	return l.market + "," + l.language + ";q=0.9"
}
