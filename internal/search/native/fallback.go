package native

import (
	"context"
	"errors"
	"slices"

	"searchy/internal/search"
)

// NewFallback applies native media validation to an optional legacy backend.
// Standalone rollback can continue using the unwrapped legacy backend.
func NewFallback(backend search.Backend) search.Backend {
	if backend == nil {
		return nil
	}
	return fallback{backend: backend}
}

type fallback struct{ backend search.Backend }

func (f fallback) SearchPage(ctx context.Context, request search.PageRequest) (search.Page, error) {
	if err := ctx.Err(); err != nil {
		return search.Page{Degraded: true}, err
	}
	request.UserID, request.Scope = 0, ""
	request.Categories = slices.Clone(request.Categories)
	page, err := f.backend.SearchPage(ctx, request)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		// A backend's HTTP errors can contain a query-bearing URL. Preserve only
		// protocol sentinels, without retaining or returning that error text.
		for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded, search.ErrInvalidCursor, search.ErrExpiredCursor, search.ErrBusy, search.ErrUnavailable} {
			if errors.Is(err, sentinel) {
				return search.Page{Degraded: true}, sentinel
			}
		}
		return search.Page{Degraded: true}, search.ErrUnavailable
	}
	results := make([]search.MediaResult, 0, len(page.Results))
	for _, r := range page.Results {
		original, thumb := r.MediaURL, r.ThumbURL
		width, height := r.Width, r.Height
		if r.Category == search.CatImage && providerJPEGPreview(original) {
			// SearXNG already replaces Bing/DDG origins with this provider's
			// JPEG thumbnail, but can leave the original's dimensions attached.
			original, thumb = "", r.MediaURL
			width, height = 0, 0
		}
		normalized, ok := mediaResult(r.Category, r.Engine, r.Title, original, thumb, r.PageURL, r.SourceOrder, width, height)
		if !ok {
			if malformedFallbackResult(r) {
				page.Degraded = true
			}
			continue
		}
		r.ID = normalized.ID
		r.MediaURL, r.ThumbURL = normalized.MediaURL, normalized.ThumbURL
		r.Width, r.Height = normalized.Width, normalized.Height
		r.Engines = slices.Clone(r.Engines)
		r.Positions = slices.Clone(r.Positions)
		results = append(results, r)
	}
	page.Results = results
	return page, nil
}

func malformedFallbackResult(r search.MediaResult) bool {
	return (r.Category != search.CatImage && r.Category != search.CatVideo) ||
		!validHTTPS(r.PageURL) || (r.MediaURL == "" && r.ThumbURL == "") ||
		(r.MediaURL != "" && !validHTTPS(r.MediaURL)) ||
		(r.ThumbURL != "" && !validHTTPS(r.ThumbURL))
}
