package search

import (
	"context"
	"log/slog"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"
)

const rrfK = 60.0

// Provider is a media backend that can execute one bounded relevance-pool
// request. The aggregator owns parallel pools, fallbacks, ranking and caching.
type Provider interface {
	Search(context.Context, SearchRequest) (SearchResponse, error)
}

type AggregatorOptions struct {
	CacheSize            int
	CacheTTL             time.Duration
	MaxResults           int
	Timeout              time.Duration
	DiscoveryPercent     int
	DiscoveryWeakPercent int
	Logger               *slog.Logger
}

// Aggregator combines the core and discovery pools, adds a bounded English
// fallback for weak localized results, and ranks the merged response.
type Aggregator struct {
	provider             Provider
	cache                *lru.LRU[string, cacheEntry]
	sf                   singleflight.Group
	maxResults           int
	timeout              time.Duration
	discoveryPercent     int
	discoveryWeakPercent int
	log                  *slog.Logger
}

type cacheEntry struct {
	results []MediaResult
	hasMore bool
}

func NewAggregator(p Provider, o AggregatorOptions) *Aggregator {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Aggregator{
		provider:             p,
		cache:                lru.NewLRU[string, cacheEntry](o.CacheSize, nil, o.CacheTTL),
		maxResults:           o.MaxResults,
		timeout:              o.Timeout,
		discoveryPercent:     clampPercent(o.DiscoveryPercent),
		discoveryWeakPercent: clampPercent(o.DiscoveryWeakPercent),
		log:                  log,
	}
}

// Search returns ranked results without surfacing backend failures to Telegram.
// Failed scheduled requests are not cached, so a subsequent search can retry.
func (a *Aggregator) Search(ctx context.Context, query string, cats []Category, page int, language string) ([]MediaResult, bool) {
	language = SearchLanguage(language)
	key := cacheKey(query, cats, page, language)
	if e, ok := a.cache.Get(key); ok {
		return e.results, e.hasMore
	}

	v, _, _ := a.sf.Do(key, func() (any, error) {
		if e, ok := a.cache.Get(key); ok {
			return e, nil
		}
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.timeout)
		defer cancel()

		e, cacheable := a.search(bctx, query, cats, page, language)
		if cacheable {
			a.cache.Add(key, e)
		}
		return e, nil
	})

	e, _ := v.(cacheEntry)
	return e.results, e.hasMore
}

func (a *Aggregator) search(ctx context.Context, query string, cats []Category, page int, language string) (cacheEntry, bool) {
	core, coreErr, discovery, discoveryErr := a.primaryPools(ctx, query, cats, page, language)
	if coreErr != nil && discoveryErr != nil {
		return cacheEntry{}, false
	}

	weak := make(map[Category]bool, len(cats))
	weakCats := make([]Category, 0, len(cats))
	coreUnique := mergeDuplicates(core.Results)
	for _, cat := range uniqueCategories(cats) {
		ranked := rankGroup(query, filterCategory(coreUnique, cat))
		weak[cat] = weakCategory(query, ranked, core.Degraded)
		if weak[cat] {
			weakCats = append(weakCats, cat)
		}
	}

	fallbackUsed := false
	var fallbackErr error
	if language != "en" && len(weakCats) > 0 && !(coreErr != nil && discoveryErr != nil) {
		fallbackUsed = true
		fallback, err := a.provider.Search(ctx, SearchRequest{
			Query: query, Categories: weakCats, Page: page, Language: "en", Pool: PoolCore,
		})
		fallbackErr = err
		if err == nil {
			core.Results = append(core.Results, fallback.Results...)
			core.HasMore = core.HasMore || fallback.HasMore
			core.RawCount += fallback.RawCount
			core.Degraded = core.Degraded || fallback.Degraded
		}
	}

	results := a.postprocess(query, cats, core.Results, discovery.Results, weak)
	a.log.Info("search ranked",
		"language", language,
		"fallback_en", fallbackUsed,
		"core", len(core.Results),
		"discovery", len(discovery.Results),
		"results", len(results),
		"weak", categoryNames(weakCats),
	)

	return cacheEntry{
		results: results,
		hasMore: core.HasMore || discovery.HasMore,
	}, coreErr == nil && discoveryErr == nil && fallbackErr == nil
}

func (a *Aggregator) primaryPools(ctx context.Context, query string, cats []Category, page int, language string) (SearchResponse, error, SearchResponse, error) {
	var core, discovery SearchResponse
	var coreErr, discoveryErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		core, coreErr = a.provider.Search(ctx, SearchRequest{
			Query: query, Categories: cats, Page: page, Language: language, Pool: PoolCore,
		})
	}()
	go func() {
		defer wg.Done()
		discovery, discoveryErr = a.provider.Search(ctx, SearchRequest{
			Query: query, Categories: cats, Page: page, Language: language, Pool: PoolDiscovery,
		})
	}()
	wg.Wait()
	return core, coreErr, discovery, discoveryErr
}

func (a *Aggregator) postprocess(query string, cats []Category, coreIn, discoveryIn []MediaResult, weak map[Category]bool) []MediaResult {
	core := mergeDuplicates(coreIn)
	discovery := mergeDuplicates(discoveryIn)

	// A result confirmed by both lanes belongs to core, but keeps the additional
	// consensus metadata contributed by discovery.
	coreIndex := make(map[string]int, len(core))
	for i := range core {
		coreIndex[core[i].ID] = i
	}
	uniqueDiscovery := discovery[:0]
	for _, r := range discovery {
		if i, ok := coreIndex[r.ID]; ok {
			mergeMedia(&core[i], r)
			continue
		}
		uniqueDiscovery = append(uniqueDiscovery, r)
	}
	discovery = uniqueDiscovery

	coreByCategory := make(map[Category][]MediaResult, len(cats))
	discoveryByCategory := make(map[Category][]MediaResult, len(cats))
	discoveryShare := make(map[Category]int, len(cats))
	for _, cat := range uniqueCategories(cats) {
		coreByCategory[cat] = rankGroup(query, filterCategory(core, cat))
		discoveryByCategory[cat] = prioritizeDiscovery(query, cat, rankGroup(query, filterCategory(discovery, cat)))
		share := a.discoveryPercent
		if weak[cat] {
			share = a.discoveryWeakPercent
		}
		discoveryShare[cat] = share
	}
	ordered := scheduleResults(query, uniqueCategories(cats), coreByCategory, discoveryByCategory, discoveryShare)
	return applyDomainDiversity(query, ordered, a.maxResults)
}

func mergeDuplicates(in []MediaResult) []MediaResult {
	out := make([]MediaResult, 0, len(in))
	indexes := make(map[string]int, len(in))
	for _, r := range in {
		if i, ok := indexes[r.ID]; ok {
			mergeMedia(&out[i], r)
			continue
		}
		r.Engines = uniqueStrings(append(r.Engines, r.Engine))
		indexes[r.ID] = len(out)
		out = append(out, r)
	}
	return out
}

func mergeMedia(dst *MediaResult, src MediaResult) {
	dst.Engines = uniqueStrings(append(dst.Engines, append(src.Engines, src.Engine)...))
	dst.Positions = uniqueInts(append(dst.Positions, src.Positions...))
	if src.Score > dst.Score {
		dst.Score = src.Score
		if src.Engine != "" {
			dst.Engine = src.Engine
		}
	}
	if src.SourceOrder < dst.SourceOrder {
		dst.SourceOrder = src.SourceOrder
	}
	if mediaQuality(src) > mediaQuality(*dst) {
		dst.MediaURL = src.MediaURL
		dst.ThumbURL = src.ThumbURL
		dst.PageURL = src.PageURL
		dst.Width = src.Width
		dst.Height = src.Height
	}
}

func rankGroup(query string, in []MediaResult) []MediaResult {
	if len(in) == 0 {
		return nil
	}
	maxScore := 0.0
	maxRRF := 0.0
	rrfs := make([]float64, len(in))
	for i, r := range in {
		if r.Score > maxScore {
			maxScore = r.Score
		}
		rrfs[i] = reciprocalRank(r.Positions)
		if rrfs[i] > maxRRF {
			maxRRF = rrfs[i]
		}
	}
	for i := range in {
		scoreNorm := normalized(in[i].Score, maxScore)
		rrfNorm := normalized(rrfs[i], maxRRF)
		backend := math.Max(scoreNorm, rrfNorm)
		consensus := float64(min(len(uniqueStrings(append(in[i].Engines, in[i].Engine))), 3)) / 3
		in[i].RankScore = 0.55*backend +
			0.25*titleCoverage(query, in[i].Title) +
			0.15*consensus +
			0.05*mediaQuality(in[i])
	}
	sort.SliceStable(in, func(i, j int) bool {
		if math.Abs(in[i].RankScore-in[j].RankScore) > 1e-12 {
			return in[i].RankScore > in[j].RankScore
		}
		if in[i].SourceOrder != in[j].SourceOrder {
			return in[i].SourceOrder < in[j].SourceOrder
		}
		return in[i].ID < in[j].ID
	})
	return in
}

func weakCategory(query string, ranked []MediaResult, degraded bool) bool {
	if len(ranked) < 10 {
		return true
	}
	top := ranked[:min(10, len(ranked))]
	relevant := 0
	hosts := make(map[string]int)
	for _, r := range top {
		if titleCoverage(query, r.Title) >= 0.5 {
			relevant++
		}
		if h := resultHost(r); h != "" {
			hosts[h]++
		}
	}
	if relevant < 3 {
		return true
	}
	for _, count := range hosts {
		if float64(count)/float64(len(top)) > 0.6 {
			return true
		}
	}
	return degraded && len(ranked) < 20
}

func prioritizeDiscovery(query string, category Category, in []MediaResult) []MediaResult {
	threshold := discoveryThreshold(category)
	strong := make([]MediaResult, 0, len(in))
	weak := make([]MediaResult, 0, len(in))
	for _, result := range in {
		if titleCoverage(query, result.Title) >= threshold {
			strong = append(strong, result)
		} else {
			weak = append(weak, result)
		}
	}
	return append(strong, weak...)
}

func scheduleResults(query string, categories []Category, core, discovery map[Category][]MediaResult, shares map[Category]int) []MediaResult {
	categories = uniqueCategories(categories)
	if len(categories) == 0 {
		return nil
	}
	coreIndex := make(map[Category]int, len(categories))
	discoveryIndex := make(map[Category]int, len(categories))
	categoryUsed := make(map[Category]int, len(categories))
	total := 0
	for _, cat := range categories {
		total += len(core[cat]) + len(discovery[cat])
	}
	out := make([]MediaResult, 0, total)
	discoveryUsed := 0

	available := func(cat Category) bool {
		return coreIndex[cat] < len(core[cat]) || discoveryIndex[cat] < len(discovery[cat])
	}
	for len(out) < total {
		cat := categories[0]
		if len(categories) > 1 {
			secondaryTarget := int(math.Floor(float64(len(out)+1) * 0.5))
			if categoryUsed[categories[1]] < secondaryTarget {
				cat = categories[1]
			}
			if !available(cat) {
				other := categories[0]
				if cat == other {
					other = categories[1]
				}
				cat = other
			}
		}
		if !available(cat) {
			break
		}

		categoryUsed[cat]++
		desiredDiscovery := 0.0
		for _, category := range categories {
			desiredDiscovery += float64(categoryUsed[category]*shares[category]) / 100
		}
		targetDiscovery := int(math.Floor(desiredDiscovery))
		useDiscovery := discoveryIndex[cat] < len(discovery[cat]) && discoveryUsed < targetDiscovery
		if useDiscovery && len(out) < 10 && titleCoverage(query, discovery[cat][discoveryIndex[cat]].Title) < discoveryThreshold(cat) && coreIndex[cat] < len(core[cat]) {
			useDiscovery = false
		}
		if coreIndex[cat] >= len(core[cat]) {
			useDiscovery = true
		}
		if useDiscovery {
			out = append(out, discovery[cat][discoveryIndex[cat]])
			discoveryIndex[cat]++
			discoveryUsed++
			continue
		}
		out = append(out, core[cat][coreIndex[cat]])
		coreIndex[cat]++
	}
	return out
}

func discoveryThreshold(category Category) float64 {
	if category == CatVideo {
		return 0.75
	}
	return 0.25
}

func applyDomainDiversity(query string, in []MediaResult, limit int) []MediaResult {
	remaining := append([]MediaResult(nil), in...)
	out := make([]MediaResult, 0, min(limit, len(in)))
	hosts := make(map[string]int)
	for len(remaining) > 0 && len(out) < limit {
		nextPosition := len(out) + 1
		hostLimit := 2
		if nextPosition > 10 {
			hostLimit = max(2, int(math.Floor(0.4*float64(nextPosition))))
		}
		pick := -1
		targetPool := remaining[0].Pool
		targetCategory := remaining[0].Category
		for i, r := range remaining {
			// Preserve the category and pool position chosen by the scheduler. Host
			// diversity may reorder peers, but must not pull a postponed discovery
			// item or the other media category into the first page.
			if r.Pool != targetPool || r.Category != targetCategory {
				continue
			}
			h := resultHost(r)
			if h == "" || hosts[h] < hostLimit {
				pick = i
				break
			}
		}
		// Relax the first-page cap one result at a time when its next admissible
		// alternative is unrelated but a blocked video remains an exact match.
		// This avoids filling a precise platform query with cross-host noise.
		if nextPosition <= 10 && (pick < 0 || titleCoverage(query, remaining[pick].Title) < 0.5) {
			for i, r := range remaining {
				if r.Category == CatVideo && titleCoverage(query, r.Title) >= 0.75 {
					pick = i
					break
				}
			}
		}
		if pick < 0 {
			pick = 0 // relax only when every remaining result violates the cap
		}
		r := remaining[pick]
		if pick > 0 {
			// Move the blocked head into the selected peer's future slot. This
			// preserves the scheduler's pool/category sequence and therefore its
			// first-page discovery and mixed-media quotas.
			remaining[pick] = remaining[0]
		}
		remaining = remaining[1:]
		out = append(out, r)
		if h := resultHost(r); h != "" {
			hosts[h]++
		}
	}
	return out
}

func titleCoverage(query, title string) float64 {
	query = strings.TrimSpace(strings.ToLower(query))
	title = strings.TrimSpace(strings.ToLower(title))
	if query == "" || title == "" {
		return 0
	}
	if strings.Contains(title, query) {
		return 1
	}
	queryTokens := tokens(query)
	if len(queryTokens) == 0 {
		return 0
	}
	titleTokens := make(map[string]struct{})
	titleSequence := tokens(title)
	for _, token := range titleSequence {
		titleTokens[token] = struct{}{}
	}
	matched := 0
	for _, token := range queryTokens {
		if _, ok := titleTokens[token]; ok {
			matched++
		}
	}
	coverage := float64(matched) / float64(len(queryTokens))
	if len(queryTokens) > 1 && len(titleSequence) > 1 {
		titlePairs := make(map[string]struct{}, len(titleSequence)-1)
		for i := 0; i+1 < len(titleSequence); i++ {
			titlePairs[titleSequence[i]+"\x00"+titleSequence[i+1]] = struct{}{}
		}
		matchedPairs := 0
		for i := 0; i+1 < len(queryTokens); i++ {
			if _, ok := titlePairs[queryTokens[i]+"\x00"+queryTokens[i+1]]; ok {
				matchedPairs++
			}
		}
		coverage += 0.25 * float64(matchedPairs) / float64(len(queryTokens)-1)
	}
	return math.Min(coverage, 1)
}

func tokens(value string) []string {
	parts := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	return uniqueStrings(parts)
}

func reciprocalRank(positions []int) float64 {
	total := 0.0
	for _, position := range positions {
		if position > 0 {
			total += 1 / (rrfK + float64(position))
		}
	}
	return total
}

func mediaQuality(r MediaResult) float64 {
	if r.Width <= 0 || r.Height <= 0 {
		if r.Category == CatVideo {
			return 0.6
		}
		return 0.5
	}
	shortSide := min(r.Width, r.Height)
	switch {
	case shortSide >= 720:
		return 1
	case shortSide >= 320:
		return 0.75
	default:
		return 0.25
	}
}

func resultHost(r MediaResult) string {
	for _, raw := range []string{r.PageURL, r.MediaURL, r.ThumbURL} {
		u, err := url.Parse(raw)
		if err == nil && u.Hostname() != "" {
			return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		}
	}
	return ""
}

func filterCategory(in []MediaResult, cat Category) []MediaResult {
	out := make([]MediaResult, 0, len(in))
	for _, r := range in {
		if r.Category == cat {
			out = append(out, r)
		}
	}
	return out
}

func uniqueCategories(in []Category) []Category {
	seen := make(map[Category]struct{}, len(in))
	out := make([]Category, 0, len(in))
	for _, cat := range in {
		if _, ok := seen[cat]; ok {
			continue
		}
		seen[cat] = struct{}{}
		out = append(out, cat)
	}
	return out
}

func categoryNames(in []Category) string {
	parts := make([]string, 0, len(in))
	for _, cat := range in {
		parts = append(parts, string(cat))
	}
	return strings.Join(parts, ",")
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
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

func uniqueInts(in []int) []int {
	seen := make(map[int]struct{}, len(in))
	out := make([]int, 0, len(in))
	for _, value := range in {
		if value <= 0 {
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

func normalized(value, maximum float64) float64 {
	if maximum <= 0 || value <= 0 {
		return 0
	}
	return value / maximum
}

func clampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func cacheKey(query string, cats []Category, page int, language string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(query)))
	b.WriteByte('|')
	for _, c := range cats {
		b.WriteString(string(c))
		b.WriteByte(',')
	}
	b.WriteByte('|')
	b.WriteString(itoa(page))
	b.WriteByte('|')
	b.WriteString(strings.ToLower(strings.TrimSpace(language)))
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
