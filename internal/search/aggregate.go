package search

import (
	"context"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"
)

// Provider is anything that can return media results for a query. SearXNG is the
// only implementation today; the interface keeps the bot layer decoupled and
// makes it trivial to add more backends later.
type Provider interface {
	Search(ctx context.Context, query string, cats []Category, page int) ([]MediaResult, bool, error)
}

// Aggregator wraps a Provider with an in-process TTL cache, request
// de-duplication (singleflight collapses identical concurrent searches into one
// backend call), result de-duplication, and a hard result cap.
type Aggregator struct {
	provider   Provider
	cache      *lru.LRU[string, cacheEntry]
	sf         singleflight.Group
	maxResults int
	timeout    time.Duration
}

type cacheEntry struct {
	results []MediaResult
	hasMore bool
}

func NewAggregator(p Provider, cacheSize int, ttl time.Duration, maxResults int, timeout time.Duration) *Aggregator {
	return &Aggregator{
		provider:   p,
		cache:      lru.NewLRU[string, cacheEntry](cacheSize, nil, ttl),
		maxResults: maxResults,
		timeout:    timeout,
	}
}

// Search returns up to maxResults normalized, de-duplicated results for the
// query and the categories, plus whether a further page likely exists. Errors
// are intentionally swallowed into an empty result set so the caller (an inline
// answer) degrades gracefully rather than failing.
func (a *Aggregator) Search(ctx context.Context, query string, cats []Category, page int) ([]MediaResult, bool) {
	key := cacheKey(query, cats, page)

	if e, ok := a.cache.Get(key); ok {
		return e.results, e.hasMore
	}

	v, _, _ := a.sf.Do(key, func() (any, error) {
		// Re-check the cache: a concurrent caller may have just filled it.
		if e, ok := a.cache.Get(key); ok {
			return e, nil
		}
		// Detach the shared backend call from the winning caller's context.
		// singleflight hands the winner's result to every waiter, so if the
		// winner's ctx is cancelled mid-flight (e.g. the inline debouncer cancels
		// it when that user types another key), an unrelated waiter on the same
		// query would otherwise get a spurious empty result. WithoutCancel breaks
		// that coupling; WithTimeout keeps the detached call bounded.
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.timeout)
		defer cancel()
		raw, hasMore, err := a.provider.Search(bctx, query, cats, page)
		if err != nil {
			// Don't cache failures — let the next attempt retry.
			return cacheEntry{}, nil
		}
		e := cacheEntry{results: a.postprocess(raw), hasMore: hasMore}
		a.cache.Add(key, e)
		return e, nil
	})

	e, _ := v.(cacheEntry)
	return e.results, e.hasMore
}

// postprocess de-duplicates by result ID (a hash of the source URL), interleaves
// results round-robin across engines so a single high-volume engine (e.g. bing/
// ddg, which flood news/junk for some queries) can't bury higher-quality ones
// (e.g. unsplash/flickr/wikimedia photos), then caps to maxResults (Telegram
// allows at most 50 inline results per answer).
func (a *Aggregator) postprocess(in []MediaResult) []MediaResult {
	// 1) de-dupe by ID, preserving SearXNG's relative order within each engine.
	seen := make(map[string]struct{}, len(in))
	groups := make(map[string][]MediaResult)
	var order []string // engines in first-seen order
	for _, r := range in {
		if _, dup := seen[r.ID]; dup {
			continue
		}
		seen[r.ID] = struct{}{}
		key := r.Engine
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}

	// 2) round-robin interleave across engines.
	out := make([]MediaResult, 0, a.maxResults)
	for len(out) < a.maxResults {
		progressed := false
		for _, key := range order {
			g := groups[key]
			if len(g) == 0 {
				continue
			}
			out = append(out, g[0])
			groups[key] = g[1:]
			progressed = true
			if len(out) >= a.maxResults {
				break
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

func cacheKey(query string, cats []Category, page int) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(query)))
	b.WriteByte('|')
	for _, c := range cats {
		b.WriteString(string(c))
		b.WriteByte(',')
	}
	b.WriteByte('|')
	b.WriteString(itoa(page))
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
