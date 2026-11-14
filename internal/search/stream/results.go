package stream

import (
	"crypto/sha256"
	"net/url"
	"strings"
	"unicode/utf8"

	"searchy/internal/search"
)

func resultKeys(r search.MediaResult) [][32]byte {
	keys := make([][32]byte, 0, 2)
	if r.ID != "" {
		keys = append(keys, sha256.Sum256([]byte("id/"+string(r.Category)+"/"+r.ID)))
	}
	raw := r.MediaURL
	if r.Category == search.CatVideo || raw == "" {
		raw = r.PageURL
	}
	if raw != "" {
		if u, err := url.Parse(raw); err == nil {
			if r.Category != search.CatVideo {
				u.Fragment = ""
			}
			u.Host = strings.ToLower(u.Host)
			raw = u.String()
		}
		keys = append(keys, sha256.Sum256([]byte("url/"+string(r.Category)+"/"+raw)))
	}
	return keys
}

func overlaps(keys [][32]byte, seen map[[32]byte]struct{}) bool {
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			return true
		}
	}
	return false
}

func (e *Engine) mergeLocked(s *session, query string, additions []search.MediaResult) {
	all := make([]search.MediaResult, 0, len(s.pool)+len(additions))
	for _, batch := range [][]search.MediaResult{s.pool, additions} {
		for _, r := range batch {
			keys := resultKeys(r)
			if len(keys) == 0 || overlaps(keys, s.delivered) {
				continue
			}
			all = append(all, r)
		}
	}
	rank := e.options.Rank
	if rank == nil {
		rank = defaultRank
	}
	// Rank the entire unseen tail, including corroborating provider variants.
	// The ranker can validate each variant and combine its relevance/consensus
	// metadata. Passing a page-size limit here would lose upstream leftovers.
	ranked := rank(query, cloneResults(all), len(all))
	ordered := make([]search.MediaResult, 0, len(all))
	indexes := make(map[[32]byte]int, 2*len(all))
	for _, r := range ranked {
		keys := resultKeys(r)
		if len(keys) == 0 || overlaps(keys, s.delivered) {
			continue
		}
		index := len(ordered)
		for _, key := range keys {
			if existing, ok := indexes[key]; ok {
				index = existing
				break
			}
		}
		for _, key := range keys {
			indexes[key] = index
		}
		if index < len(ordered) {
			mergeMetadata(&ordered[index], r)
		} else {
			ordered = append(ordered, r)
		}
	}
	n := min(len(ordered), s.budget-s.count)
	delta := n - len(s.pool)
	for e.used+delta > e.options.CacheSize {
		victim := e.victimLocked(s)
		if victim == nil {
			n = max(0, n-(e.used+delta-e.options.CacheSize))
			s.limited = true
			s.retry = true
			s.degraded = true
			break
		}
		e.removeLocked(victim)
	}
	e.used += n - len(s.pool)
	s.pool = cloneResults(ordered[:n])
}

// Count limits also apply before results cross the worker/session boundary.
// Small field caps keep a malformed provider from turning a result-count bound
// into unbounded retained strings or ranking metadata.
func boundedResults(in []search.MediaResult, limit int) []search.MediaResult {
	out := append([]search.MediaResult(nil), in[:min(limit, len(in))]...)
	for i := range out {
		r := &out[i]
		r.ID = boundedString(r.ID, 256)
		r.Title = boundedString(r.Title, 2048)
		r.Author = boundedString(r.Author, 512)
		r.Caption = boundedString(r.Caption, 4096)
		r.MediaURL = boundedURL(r.MediaURL)
		r.ThumbURL = boundedURL(r.ThumbURL)
		r.PageURL = boundedURL(r.PageURL)
		r.Engine = boundedString(r.Engine, 128)
		r.MIMEType = boundedString(r.MIMEType, 128)
		r.Engines = append([]string(nil), r.Engines[:min(16, len(r.Engines))]...)
		for j := range r.Engines {
			r.Engines[j] = boundedString(r.Engines[j], 128)
		}
		r.Positions = append([]int(nil), r.Positions[:min(32, len(r.Positions))]...)
	}
	return out
}

func boundedString(s string, limit int) string {
	if len(s) > limit {
		s = s[:limit]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.Clone(s)
}

func mergeMetadata(dst *search.MediaResult, src search.MediaResult) {
	engines := make([]string, 0, 16)
	for _, batch := range [][]string{{dst.Engine}, dst.Engines, {src.Engine}, src.Engines} {
		for _, engine := range batch {
			found := engine == ""
			for _, existing := range engines {
				found = found || existing == engine
			}
			if !found && len(engines) < 16 {
				engines = append(engines, engine)
			}
		}
	}
	dst.Engines = engines
	positions := append([]int(nil), dst.Positions...)
	for _, position := range src.Positions {
		found := false
		for _, existing := range positions {
			found = found || existing == position
		}
		if !found && len(positions) < 32 {
			positions = append(positions, position)
		}
	}
	dst.Positions = positions
	if src.Score > dst.Score {
		dst.Score = src.Score
	}
	if src.RankScore > dst.RankScore {
		dst.RankScore = src.RankScore
	}
	if src.SourceOrder < dst.SourceOrder {
		dst.SourceOrder = src.SourceOrder
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.Author == "" {
		dst.Author = src.Author
	}
	if dst.ThumbURL == "" {
		dst.ThumbURL = src.ThumbURL
	}
	if dst.MediaURL == "" || src.Width > 0 && src.Height > 0 && src.Width*src.Height > dst.Width*dst.Height {
		dst.MediaURL, dst.Width, dst.Height = src.MediaURL, src.Width, src.Height
	}
}

func boundedURL(s string) string {
	if len(s) > 8192 {
		return ""
	}
	return strings.Clone(s)
}
