package native

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"searchy/internal/search"
)

type wikimedia struct{ http *fetcher }

func (w *wikimedia) Name() string              { return "Wikimedia" }
func (w *wikimedia) Category() search.Category { return search.CatImage }

func (w *wikimedia) SearchSource(ctx context.Context, request search.SourceRequest) (search.SourcePage, error) {
	offset, err := numericCursor(request.Cursor, 0)
	if err != nil {
		return search.SourcePage{}, err
	}
	l := resolveLocale(request.Language)
	params := url.Values{
		"action": {"query"}, "format": {"json"}, "generator": {"search"},
		"gsrsearch": {strings.TrimSpace(request.Query + " filetype:bitmap")}, "gsrnamespace": {"6"}, "gsrlimit": {"50"},
		"gsroffset": {strconv.Itoa(offset)}, "prop": {"imageinfo"},
		"iiprop": {"url|size|mime|thumbmime|mediatype"}, "iiurlwidth": {"1280"},
	}
	if l.language != "" {
		params.Set("uselang", l.language)
	}
	body, err := w.http.get(ctx, "https://commons.wikimedia.org/w/api.php?"+params.Encode(), l.acceptLanguage(), "")
	if err != nil {
		return search.SourcePage{}, err
	}
	page, err := parseWikimedia(body, w.Name(), offset)
	if err != nil {
		return search.SourcePage{}, err
	}
	// Commons indexes book OCR and descriptions as well as filenames. Require
	// meaningful filename coverage before letting its early positions compete
	// with dedicated image-search rankings.
	filtered := page.Results[:0]
	for _, result := range page.Results {
		if strings.TrimSpace(request.Query) == "" || coverage(request.Query, result.Title) >= 0.5 {
			filtered = append(filtered, result)
		}
	}
	page.Results = filtered
	return page, nil
}

type wikiPage struct {
	Title  string `json:"title"`
	Index  int    `json:"index"`
	Images []struct {
		URL         string `json:"url"`
		Thumb       string `json:"thumburl"`
		Description string `json:"descriptionurl"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		Size        int64  `json:"size"`
		MIME        string `json:"mime"`
		ThumbMIME   string `json:"thumbmime"`
		MediaType   string `json:"mediatype"`
	} `json:"imageinfo"`
}

func parseWikimedia(body []byte, engine string, offset int) (search.SourcePage, error) {
	var response struct {
		Batch    json.RawMessage `json:"batchcomplete"`
		Error    json.RawMessage `json:"error"`
		Warnings json.RawMessage `json:"warnings"`
		Continue struct {
			Offset *int `json:"gsroffset"`
		} `json:"continue"`
		Query *struct {
			Pages map[string]wikiPage `json:"pages"`
		} `json:"query"`
	}
	if json.Unmarshal(body, &response) != nil || (len(response.Error) > 0 && string(response.Error) != "null") || ((response.Query == nil || response.Query.Pages == nil) && len(response.Batch) == 0) {
		return search.SourcePage{}, errResponse
	}
	page := search.SourcePage{Degraded: len(response.Warnings) > 0 && string(response.Warnings) != "null"}
	var pages []wikiPage
	if response.Query != nil {
		for _, p := range response.Query.Pages {
			pages = append(pages, p)
		}
	}
	if len(pages) > pageSize {
		// We request the provider's 50-image ceiling; an unexpected larger page
		// cannot be truncated without dropping records at its continuation.
		return search.SourcePage{}, errResponse
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].Index != pages[j].Index {
			return pages[i].Index < pages[j].Index
		}
		return pages[i].Title < pages[j].Title
	})
	for _, p := range pages {
		if len(p.Images) == 0 {
			continue
		}
		image := p.Images[0]
		// A JPEG rendering of a document page is not an image-search result.
		if !wikiImageMIME(image.MIME) || (image.MediaType != "" && image.MediaType != "BITMAP" && image.MediaType != "DRAWING") {
			continue
		}
		origin, parseErr := url.Parse(image.URL)
		if parseErr != nil {
			continue
		}
		switch strings.ToLower(path.Ext(origin.Path)) {
		case ".pdf", ".djvu", ".djv", ".ps", ".eps":
			continue
		}
		thumb := image.Thumb
		if image.ThumbMIME != "" && image.ThumbMIME != "image/jpeg" {
			thumb = ""
		}
		original := image.URL
		if image.MIME != "" && image.MIME != "image/jpeg" {
			original = ""
		}
		if thumb == "" && image.Size <= 5<<20 {
			thumb = original
		}
		if photoURL(thumb) == "" && image.Size > 5<<20 {
			continue
		}
		if original == "" && thumb == "" {
			continue
		}
		r, ok := mediaResult(search.CatImage, engine, strings.TrimPrefix(p.Title, "File:"), original, thumb, image.Description, max(p.Index-1, offset), image.Width, image.Height)
		if !ok {
			continue
		}
		if image.Size > 5<<20 && r.MediaURL == original {
			r.MediaURL = r.ThumbURL
		}
		if validHTTPS(image.URL) {
			r.ID = resultID(search.CatImage, image.URL)
		}
		page.Results = append(page.Results, r)
	}
	if response.Continue.Offset != nil {
		next := *response.Continue.Offset
		if next <= offset || next > maxOffset {
			page.Degraded = true
		} else {
			page.NextCursor = strconv.Itoa(next)
		}
	}
	return page, nil
}

func wikiImageMIME(mime string) bool {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/png", "image/tiff", "image/webp", "image/avif", "image/svg+xml", "image/gif", "image/bmp":
		return true
	}
	return false
}
