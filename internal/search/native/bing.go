package native

import (
	"context"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"searchy/internal/search"
)

type bing struct {
	http     *fetcher
	safe     int
	category search.Category
}

func (b *bing) Name() string              { return "Bing " + string(b.category) }
func (b *bing) Category() search.Category { return b.category }

var (
	bingImageAttributes = regexp.MustCompile(`(?is)\bm\s*=\s*(?:"([^"]+)"|'([^']+)')`)
	bingVideoAttributes = regexp.MustCompile(`(?is)\bvrhm\s*=\s*(?:"([^"]+)"|'([^']+)')`)
	bingVideoThumb      = regexp.MustCompile(`(?is)\b(?:data-src-hq|data-src|src)\s*=\s*(?:"(https://[^"]+)"|'(https://[^']+)')`)
)

func (b *bing) SearchSource(ctx context.Context, request search.SourceRequest) (search.SourcePage, error) {
	first, err := numericCursor(request.Cursor, 1)
	if err != nil {
		return search.SourcePage{}, err
	}
	l := resolveLocale(request.Language)
	params := url.Values{
		"q": {request.Query}, "first": {strconv.Itoa(first)}, "count": {"35"},
		"adlt": {[]string{"off", "moderate", "strict"}[b.safe]},
	}
	if l.market != "" {
		params.Set("mkt", l.market)
		params.Set("setlang", l.language)
		params.Set("cc", l.country)
	}
	endpoint := "/images/async"
	if b.category == search.CatVideo {
		endpoint = "/videos/asyncv2"
		params.Set("async", "content")
	} else {
		params.Set("mmasync", "1")
	}
	body, err := b.http.get(ctx, "https://www.bing.com"+endpoint+"?"+params.Encode(), l.acceptLanguage(), "")
	if err != nil {
		return search.SourcePage{}, err
	}
	results, consumed, rawCount, degraded, err := parseBing(body, b.category, b.Name(), first-1)
	if err != nil {
		return search.SourcePage{}, err
	}
	page := search.SourcePage{Results: results, Degraded: degraded}
	if (rawCount >= 35 || consumed < rawCount) && consumed > 0 && first+consumed <= maxOffset {
		page.NextCursor = strconv.Itoa(first + consumed)
	}
	return page, nil
}

type flexibleInt int

func (n *flexibleInt) UnmarshalJSON(data []byte) error {
	value := strings.Trim(string(data), `"`)
	i, _ := strconv.Atoi(value)
	*n = flexibleInt(max(i, 0))
	return nil
}

func parseBing(body []byte, category search.Category, engine string, offset int) ([]search.MediaResult, int, int, bool, error) {
	pattern := bingImageAttributes
	if category == search.CatVideo {
		pattern = bingVideoAttributes
	}
	matches := pattern.FindAllSubmatchIndex(body, -1)
	if len(matches) == 0 {
		text := strings.ToLower(string(body))
		// A challenge, consent page or changed markup must not become a cached
		// successful empty page. Only known result/empty containers are accepted.
		for _, marker := range []string{"dgcontrol_list", "mc_vtvc_noresults", "no results", "noresults", "b_no"} {
			if strings.Contains(text, marker) {
				return nil, 0, 0, false, nil
			}
		}
		return nil, 0, 0, false, errResponse
	}
	out := make([]search.MediaResult, 0, min(pageSize, len(matches)))
	consumed, decoded, degraded := 0, 0, false
	for i, match := range matches {
		start, end := match[2], match[3]
		if start < 0 {
			start, end = match[4], match[5]
		}
		var record struct {
			Media          string          `json:"murl"`
			Thumb          string          `json:"turl"`
			Page           string          `json:"purl"`
			Title          string          `json:"t"`
			AlternateTitle string          `json:"vt"`
			Description    string          `json:"desc"`
			Width          flexibleInt     `json:"w"`
			Height         flexibleInt     `json:"h"`
			Duration       json.RawMessage `json:"du"`
			ThumbnailID    string          `json:"thid"`
		}
		consumed++
		if json.Unmarshal([]byte(html.UnescapeString(string(body[start:end]))), &record) != nil {
			degraded = true
			continue
		}
		if record.Media == "" && record.Page == "" {
			degraded = true
			continue
		}
		decoded++
		if category == search.CatVideo {
			if !validHTTPS(record.Page) {
				record.Page = record.Media
			}
			segmentEnd := len(body)
			if i+1 < len(matches) {
				segmentEnd = matches[i+1][0]
			}
			for _, thumb := range bingVideoThumb.FindAllSubmatch(body[match[1]:segmentEnd], -1) {
				value := thumb[1]
				if len(value) == 0 {
					value = thumb[2]
				}
				if candidate := photoURL(html.UnescapeString(string(value))); candidate != "" && photoURL(record.Thumb) == "" {
					record.Thumb = candidate
				}
			}
			if photoURL(record.Thumb) == "" && record.ThumbnailID != "" && len(record.ThumbnailID) < 256 {
				record.Thumb = "https://tse1.mm.bing.net/th?" + url.Values{"id": {record.ThumbnailID}, "pid": {"2.1"}}.Encode()
			}
		}
		title := record.Title
		if title == "" {
			title = record.AlternateTitle
		}
		if title == "" {
			title = record.Description
		}
		if r, ok := mediaResult(category, engine, html.UnescapeString(title), record.Media, record.Thumb, record.Page, offset+i, int(record.Width), int(record.Height)); ok {
			r.DurationSec = duration(strings.Trim(string(record.Duration), `"`))
			out = append(out, r)
		}
		if len(out) == pageSize {
			break
		}
	}
	if decoded == 0 {
		return nil, 0, len(matches), true, errResponse
	}
	return out, consumed, len(matches), degraded, nil
}
