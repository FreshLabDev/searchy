package native

import (
	"net/url"
	"sort"
	"strings"
	"unicode"

	"searchy/internal/search"
)

// Rank combines provider positions, literal title coverage and corroboration.
// The caller filters requested categories; when both remain, close-quality
// candidates receive a modest category-diversity preference.
func Rank(query string, candidates []search.MediaResult, limit int) []search.MediaResult {
	if limit <= 0 {
		return nil
	}
	unique := make(map[string]search.MediaResult, len(candidates))
	for _, r := range candidates {
		if (r.Category != search.CatImage && r.Category != search.CatVideo) || !validHTTPS(r.PageURL) || photoURL(r.ThumbURL) == "" || (r.Category == search.CatImage && photoURL(r.MediaURL) == "") || (r.MediaURL != "" && !validHTTPS(r.MediaURL)) {
			continue
		}
		key := canonicalURL(r.MediaURL)
		if r.Category == search.CatVideo {
			key = canonicalVideoURL(r.PageURL)
			// Fallback providers use their own IDs. Normalize these too so a
			// late variant can be recognized after an earlier page was frozen.
			r.ID = resultID(r.Category, r.PageURL)
		}
		// Provider IDs can identify the same origin behind different JPEG previews.
		if r.ID != "" {
			key = r.ID
		}
		key = string(r.Category) + "\x00" + key
		r.Engines = engineNames(r)
		r.Positions = append([]int(nil), r.Positions...)
		if old, ok := unique[key]; ok {
			old.SourceOrder = min(max(old.SourceOrder, 0), max(r.SourceOrder, 0))
			if coverage(query, r.Title) > coverage(query, old.Title) {
				old.Title = r.Title
			}
			if quality(r) > quality(old) {
				old.MediaURL, old.ThumbURL = r.MediaURL, r.ThumbURL
				old.Width, old.Height = r.Width, r.Height
			}
			if old.DurationSec == 0 {
				old.DurationSec = r.DurationSec
			}
			for _, name := range r.Engines {
				if !contains(old.Engines, name) {
					old.Engines = append(old.Engines, name)
				}
			}
			unique[key] = old
		} else {
			if r.ID == "" {
				r.ID = resultID(r.Category, r.MediaURL)
				if r.Category == search.CatVideo {
					r.ID = resultID(r.Category, r.PageURL)
				}
			}
			unique[key] = r
		}
	}
	ranked := make([]search.MediaResult, 0, len(unique))
	for _, r := range unique {
		position := max(r.SourceOrder, 0)
		r.RankScore = 0.60/(1+float64(position)/8) + 0.27*coverage(query, r.Title) + 0.08*float64(min(len(r.Engines), 3))/3 + 0.05*quality(r)
		ranked = append(ranked, r)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].RankScore != ranked[j].RankScore {
			return ranked[i].RankScore > ranked[j].RankScore
		}
		if ranked[i].ID != ranked[j].ID {
			return ranked[i].ID < ranked[j].ID
		}
		return ranked[i].PageURL < ranked[j].PageURL
	})
	return diversify(ranked, limit)
}

func quality(r search.MediaResult) float64 {
	if r.Width >= 640 && r.Height >= 360 {
		return 1
	}
	if r.Width >= 320 && r.Height >= 180 {
		return 0.5
	}
	return 0
}

func engineNames(r search.MediaResult) []string {
	var names []string
	for _, name := range append([]string{r.Engine}, r.Engines...) {
		if name != "" && !contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func diversify(ranked []search.MediaResult, limit int) []search.MediaResult {
	out := make([]search.MediaResult, 0, min(limit, len(ranked)))
	hosts := make(map[string]int)
	categories := make(map[search.Category]int)
	hasImages, hasVideos := false, false
	for _, r := range ranked {
		hasImages = hasImages || r.Category == search.CatImage
		hasVideos = hasVideos || r.Category == search.CatVideo
	}
	for len(ranked) > 0 && len(out) < limit {
		pick, best := 0, -1.0
		for i, r := range ranked {
			// Do not displace a much more relevant result solely for diversity.
			if r.RankScore < ranked[0].RankScore-0.20 {
				break
			}
			u, _ := url.Parse(r.PageURL)
			host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
			score := r.RankScore - 0.09*float64(hosts[host])
			if hasImages && hasVideos {
				other := search.CatImage
				if r.Category == search.CatImage {
					other = search.CatVideo
				}
				score -= 0.08 * float64(max(categories[r.Category]-categories[other], 0))
			}
			if score > best {
				pick, best = i, score
			}
		}
		r := ranked[pick]
		u, _ := url.Parse(r.PageURL)
		hosts[strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")]++
		categories[r.Category]++
		out = append(out, r)
		ranked = append(ranked[:pick], ranked[pick+1:]...)
	}
	return out
}

func coverage(query, title string) float64 {
	tokens := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(tokens) == 0 {
		return 0
	}
	words := make(map[string]bool)
	for _, word := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		words[word] = true
	}
	seen, matches := make(map[string]bool), 0
	for _, token := range tokens {
		if seen[token] {
			continue
		}
		seen[token] = true
		if words[token] || (isCJK(token) && strings.Contains(strings.ToLower(title), token)) {
			matches++
		}
	}
	return float64(matches) / float64(len(seen))
}

func isCJK(token string) bool {
	for _, r := range token {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return true
		}
	}
	return false
}
