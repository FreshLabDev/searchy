package native

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"searchy/internal/search"
)

type duckDuckGo struct {
	http    *fetcher
	safe    int
	salt    [32]byte
	mu      sync.Mutex
	entries map[[32]byte]*tokenEntry
	running int
}

type tokenEntry struct {
	done     chan struct{}
	cancel   context.CancelFunc
	token    string
	err      error
	expires  time.Time
	waiters  int
	complete bool
}

type duckSource struct {
	ddg      *duckDuckGo
	category search.Category
}

func (d *duckSource) Name() string              { return "DuckDuckGo " + string(d.category) }
func (d *duckSource) Category() search.Category { return d.category }

var (
	vqdPattern = regexp.MustCompile(`\bvqd\s*[=:]\s*['"]([0-9-]{3,192})['"]`)
	validToken = regexp.MustCompile(`^[0-9]+(?:-[0-9]+){1,4}$`)
)

func (d *duckDuckGo) settings(l locale) (url.Values, string) {
	safe := []string{"-2", "-1", "1"}[d.safe]
	params := url.Values{"l": {l.duck}, "p": {safe}, "kp": {safe}}
	if l.language != "" {
		params.Set("ct", strings.ToUpper(l.language))
	}
	ad := l.language
	if l.language == "zh" {
		ad = strings.ReplaceAll(l.market, "-", "_")
	}
	cookie := "p=" + safe + "; ah=" + l.duck + "; l=" + l.duck
	if ad != "" {
		cookie += "; ad=" + ad
	}
	return params, cookie
}

// Only the salted request digest and a short-lived provider token are cached.
// An individual caller's cancellation does not cancel other token waiters.
func (d *duckDuckGo) token(ctx context.Context, request search.SourceRequest, l locale) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hash := hmac.New(sha256.New, d.salt[:])
	_, _ = hash.Write([]byte(request.Query + "\x00" + l.market + "\x00" + l.duck))
	var key [32]byte
	copy(key[:], hash.Sum(nil))
	d.mu.Lock()
	now := time.Now()
	for k, entry := range d.entries {
		if entry.complete && now.After(entry.expires) {
			delete(d.entries, k)
		}
	}
	entry := d.entries[key]
	if entry == nil {
		if d.running >= 8 {
			d.mu.Unlock()
			return "", search.ErrBusy
		}
		if len(d.entries) >= 128 {
			var oldest [32]byte
			var expiry time.Time
			for k, e := range d.entries {
				if e.complete && (expiry.IsZero() || e.expires.Before(expiry)) {
					oldest, expiry = k, e.expires
				}
			}
			if expiry.IsZero() {
				d.mu.Unlock()
				return "", search.ErrBusy
			}
			delete(d.entries, oldest)
		}
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		entry = &tokenEntry{done: make(chan struct{}), cancel: cancel}
		d.entries[key] = entry
		d.running++
		go func(e *tokenEntry) {
			defer cancel()
			params, cookie := d.settings(l)
			params.Set("q", request.Query)
			params.Set("ia", "images")
			params.Set("iar", "images")
			body, err := d.http.get(shared, "https://duckduckgo.com/?"+params.Encode(), l.acceptLanguage(), cookie)
			token := ""
			if err == nil {
				match := vqdPattern.FindSubmatch(body)
				if len(match) != 2 || !validToken.Match(match[1]) {
					err = errResponse
				} else {
					token = string(match[1])
				}
			}
			d.mu.Lock()
			e.token, e.err, e.complete = token, err, true
			e.expires = time.Now().Add(2 * time.Minute)
			d.running--
			if err != nil && d.entries[key] == e {
				delete(d.entries, key)
			}
			close(e.done)
			d.mu.Unlock()
		}(entry)
	}
	if entry.waiters >= 64 {
		d.mu.Unlock()
		return "", search.ErrBusy
	}
	entry.waiters++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		entry.waiters--
		if entry.waiters == 0 && !entry.complete {
			entry.cancel()
			if d.entries[key] == entry {
				delete(d.entries, key)
			}
		}
		d.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-entry.done:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return entry.token, entry.err
	}
}

type duckCursor struct {
	Token  string `json:"t"`
	Offset int    `json:"s"`
	Skip   int    `json:"i"`
}

func encodeDuckCursor(c duckCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeDuckCursor(raw string) (duckCursor, error) {
	var c duckCursor
	if raw == "" {
		return c, nil
	}
	if len(raw) > 512 {
		return c, search.ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, search.ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || encodeDuckCursor(c) != raw || !validToken.MatchString(c.Token) || c.Offset < 0 || c.Offset > maxOffset || c.Skip < 0 || c.Skip > 1000 {
		return duckCursor{}, search.ErrInvalidCursor
	}
	return c, nil
}

func (d *duckSource) SearchSource(ctx context.Context, request search.SourceRequest) (search.SourcePage, error) {
	cursor, err := decodeDuckCursor(request.Cursor)
	if err != nil {
		return search.SourcePage{}, err
	}
	if utf8.RuneCountInString(request.Query) >= 500 {
		return search.SourcePage{}, errorsQueryTooLong
	}
	l := resolveLocale(request.Language)
	if cursor.Token == "" {
		cursor.Token, err = d.ddg.token(ctx, request, l)
		if err != nil {
			return search.SourcePage{}, err
		}
	}
	params, cookie := d.ddg.settings(l)
	params.Set("q", request.Query)
	params.Set("vqd", cursor.Token)
	params.Set("o", "json")
	params.Set("s", strconv.Itoa(cursor.Offset))
	params.Set("u", "bing")
	params.Set("f", ",,,")
	params.Set("a", "h_")
	endpoint := "/i.js"
	if d.category == search.CatVideo {
		endpoint = "/v.js"
	}
	body, err := d.ddg.http.get(ctx, "https://duckduckgo.com"+endpoint+"?"+params.Encode(), l.acceptLanguage(), cookie)
	if err != nil {
		return search.SourcePage{}, err
	}
	var response duckResponse
	if json.Unmarshal(body, &response) != nil || response.Results == nil || (len(response.Error) != 0 && string(response.Error) != "null") || len(response.Results) > 1000 {
		return search.SourcePage{}, errResponse
	}
	if cursor.Skip > len(response.Results) {
		return search.SourcePage{}, errResponse
	}
	page := search.SourcePage{Results: make([]search.MediaResult, 0, min(pageSize, len(response.Results)))}
	consumed := cursor.Skip
	for consumed < len(response.Results) && len(page.Results) < pageSize {
		i := consumed
		consumed++
		var item duckRecord
		if json.Unmarshal(response.Results[i], &item) != nil {
			page.Degraded = true
			continue
		}
		if (d.category == search.CatImage && (item.Image == "" || item.URL == "")) || (d.category == search.CatVideo && item.Content == "") {
			page.Degraded = true
			continue
		}
		if result, ok := item.result(d.category, d.Name(), cursor.Offset+i); ok {
			page.Results = append(page.Results, result)
		}
	}
	if page.Degraded && len(page.Results) == 0 {
		return search.SourcePage{}, errResponse
	}
	if consumed < len(response.Results) {
		cursor.Skip = consumed
		page.NextCursor = encodeDuckCursor(cursor)
	} else if response.Next != "" {
		next, err := duckNextOffset(response.Next, endpoint, cursor.Offset)
		if err != nil {
			page.Degraded = true
			return page, nil
		}
		cursor.Offset, cursor.Skip = next, 0
		page.NextCursor = encodeDuckCursor(cursor)
	}
	return page, nil
}

var errorsQueryTooLong = errors.New("search query exceeds provider limit")

type duckResponse struct {
	Results []json.RawMessage `json:"results"`
	Next    string            `json:"next"`
	Error   json.RawMessage   `json:"error"`
}

type duckRecord struct {
	Title     string      `json:"title"`
	Image     string      `json:"image"`
	Thumbnail string      `json:"thumbnail"`
	URL       string      `json:"url"`
	Content   string      `json:"content"`
	Width     flexibleInt `json:"width"`
	Height    flexibleInt `json:"height"`
	Duration  string      `json:"duration"`
	Images    struct {
		Small  string `json:"small"`
		Medium string `json:"medium"`
		Large  string `json:"large"`
	} `json:"images"`
}

func (item duckRecord) result(category search.Category, engine string, order int) (search.MediaResult, bool) {
	thumb, page, media := item.Thumbnail, item.URL, item.Image
	if category == search.CatVideo {
		page = item.Content
		thumb = firstPhoto(item.Images.Large, item.Images.Medium, item.Images.Small, thumb)
		media = thumb
	}
	r, ok := mediaResult(category, engine, html.UnescapeString(item.Title), media, thumb, page, order, int(item.Width), int(item.Height))
	r.DurationSec = duration(item.Duration)
	return r, ok
}

// Extract only a bounded numeric offset from a provider link. No URL or echoed
// query enters a cursor, and requests always rebuild the pinned provider URL.
func duckNextOffset(raw, endpoint string, current int) (int, error) {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\\\r\n\t #") || (endpoint != "/i.js" && endpoint != "/v.js") {
		return 0, errResponse
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return 0, errResponse
	}
	if u.IsAbs() {
		if u.Scheme != "https" || u.Host != "duckduckgo.com" || u.Path != endpoint {
			return 0, errResponse
		}
	} else if u.Host != "" || (u.Path != endpoint && u.Path != strings.TrimPrefix(endpoint, "/")) {
		return 0, errResponse
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["s"]) != 1 {
		return 0, errResponse
	}
	n, err := numericCursor(q.Get("s"), 0)
	if err != nil || n <= current {
		return 0, errResponse
	}
	return n, nil
}
