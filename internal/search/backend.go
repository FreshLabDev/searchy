package search

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// PageRequest is transient request input. Backends must not retain Query in
// cached sessions or logs; use a process-local digest to identify a search.
// An empty Query revokes this user/scope's interest in background search work
// without starting retrieval. Backends with no background leases do nothing.
type PageRequest struct {
	Query      string
	Categories []Category
	Language   string
	Offset     string
	UserID     int64
	Scope      string
	Limit      int
	WaitAll    bool // the DM/group grid needs one complete bounded result pool
}

type Page struct {
	Results    []MediaResult
	NextOffset string
	Degraded   bool
}

var (
	ErrExpiredCursor = errors.New("search page expired")
	ErrInvalidCursor = errors.New("invalid search cursor")
	ErrBusy          = errors.New("search capacity reached")
	ErrUnavailable   = errors.New("search sources unavailable")
)

type Backend interface {
	SearchPage(context.Context, PageRequest) (Page, error)
}

// Source cursors contain provider continuation state, never a URL with an
// echoed query. The caller supplies the query again on every Telegram request.
type SourceRequest struct {
	Query    string
	Language string
	Cursor   string
}

type SourcePage struct {
	Results    []MediaResult
	NextCursor string
	Degraded   bool
}

type Source interface {
	Name() string
	Category() Category
	SearchSource(context.Context, SourceRequest) (SourcePage, error)
}

// LegacyBackend keeps the existing aggregator available as an explicit backend
// and as a bounded fallback, without changing its ranking or engine pools.
type LegacyBackend struct {
	Aggregator *Aggregator
	Shared     bool // standalone switchback coalesces requests; fallback stays bounded
}

func (b LegacyBackend) SearchPage(ctx context.Context, r PageRequest) (Page, error) {
	page := 0
	if r.Offset != "" {
		n, err := strconv.Atoi(r.Offset)
		if err != nil || n < 0 || n >= 10 {
			return Page{}, ErrInvalidCursor
		}
		page = n
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if strings.TrimSpace(r.Query) == "" {
		return Page{}, nil
	}
	if b.Aggregator == nil {
		return Page{}, nil
	}
	// Fallback must inherit the remaining overall deadline. Aggregator.Search
	// intentionally detaches shared work from its caller, which is unsuitable
	// for a fallback started late in a native search's budget.
	lookup := b.Aggregator.SearchBounded
	if b.Shared {
		lookup = b.Aggregator.SearchShared
	}
	results, more, degraded := lookup(ctx, r.Query, r.Categories, page, r.Language)
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if degraded && len(results) == 0 {
		return Page{Degraded: true}, ErrUnavailable
	}
	// The legacy aggregator owns its page size. Keep its complete page to
	// avoid dropping the tail between independent upstream pages.
	next := ""
	if more && len(results) > 0 && page+1 < 10 {
		next = strconv.Itoa(page + 1)
	}
	return Page{Results: results, NextOffset: next, Degraded: degraded}, nil
}
