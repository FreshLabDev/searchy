package stream

import (
	"errors"
	"strings"
	"time"

	"searchy/internal/search"
)

// Only this bounded coordinator and its active calls retain query text. There
// is no queued closure or idle session containing it between Telegram requests.
func (e *Engine) run(s *session, j *job, query, language string, cats []search.Category, limit int, waitAll bool) {
	defer e.wg.Done()
	defer j.cancel()
	defer j.cancelNative()
	quality := time.NewTimer(e.options.FirstPageWait)
	defer quality.Stop()
	qualityC := quality.C
	nativeC := j.native.Done()
	defer func() {
		e.mu.Lock()
		if s.job == j {
			if j.ctx.Err() != nil || j.native.Err() != nil {
				for i := range s.states {
					state := &s.states[i]
					if state.pending || !state.done && !state.failed && state.calls == 0 {
						state.failed = true
						s.retry = true
						s.degraded = true
					}
				}
				if s.fallback.pending {
					s.fallback.failed = true
					s.retry = true
					s.degraded = true
				}
			}
			for i := range s.states {
				s.states[i].pending = false
			}
			s.fallback.pending = false
			s.job = nil
			e.clearLeasesLocked(s)
			if s.retry {
				e.unlinkKeyLocked(s)
			}
		}
		e.jobs--
		e.notifyLocked()
		e.mu.Unlock()
	}()
	for {
		e.mu.Lock()
		if e.closed || s.evicted || s.job != j || j.ctx.Err() != nil {
			e.mu.Unlock()
			return
		}
		// Every provider receives an initial opportunity. Continuations are
		// fetched only to fill a bounded lookahead, preserving their leftovers.
		if j.native.Err() == nil && !s.limited {
			initial := false
			for _, state := range s.states {
				if !state.done && !state.failed && state.calls == 0 {
					initial = true
					break
				}
			}
			for i := range s.states {
				state := &s.states[i]
				if state.done || state.failed || state.pending || state.calls >= maxPages {
					continue
				}
				// A fast provider's continuation must not take the last slot
				// before another provider has made its first request.
				if initial && state.calls > 0 {
					continue
				}
				if state.calls > 0 && (len(s.pool) >= j.target || s.count+len(s.pool) >= s.budget) {
					continue
				}
				if e.active >= e.options.MaxConcurrent || e.sources[i].active >= min(2, e.options.MaxConcurrent) {
					continue
				}
				cursor := e.openCursor(state.cursor)
				state.pending = true
				state.calls++
				e.active++
				e.sources[i].active++
				go e.callSource(s, j, i, query, language, cursor)
			}
		}
		nativePending := false
		if j.native.Err() == nil && !s.limited {
			for _, state := range s.states {
				if state.pending || !state.done && !state.failed && state.calls < maxPages && (state.calls == 0 || len(s.pool) < j.target && s.count+len(s.pool) < s.budget) {
					nativePending = true
				}
			}
		}
		windowPassed := time.Since(j.start) >= e.options.FirstPageWait
		weak := s.count == 0 && clearlyWeak(query, s.pool)
		// A healthy single provider does not pay the fallback's latency. Once a
		// fallback has supplied results its own exact continuation is preserved.
		wantFallback := e.options.Fallback != nil && !s.limited && !s.fallback.done && !s.fallback.failed && !s.fallback.pending && s.fallback.calls < maxPages && s.count+len(s.pool) < s.budget && (s.fallback.calls > 0 && len(s.pool) < j.target || weak && (windowPassed || !nativePending))
		if wantFallback && e.active < e.options.MaxConcurrent && e.fallbacks < min(2, e.options.MaxConcurrent) {
			cursor := e.openCursor(s.fallback.cursor)
			s.fallback.pending = true
			s.fallback.calls++
			e.active++
			e.fallbacks++
			go e.callFallback(s, j, query, language, cats, cursor, limit, waitAll)
		}
		fallbackPending := s.fallback.pending || wantFallback
		if !nativePending && !fallbackPending {
			e.mu.Unlock()
			return
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-j.ctx.Done():
		case <-nativeC:
			nativeC = nil
		case <-qualityC:
			qualityC = nil
			e.mu.Lock()
			e.notifyLocked()
			e.mu.Unlock()
		case <-changed:
		}
	}
}

func (e *Engine) callSource(s *session, j *job, index int, query, language, cursor string) {
	start := time.Now()
	page, err := e.sources[index].backend.SearchSource(j.native, search.SourceRequest{Query: query, Language: language, Cursor: cursor})
	if j.native.Err() != nil {
		err = j.native.Err()
	}
	// Clone while the provider still owns the returned slice; session storage
	// never aliases a provider's reusable response or the caller's results.
	page.Results = boundedResults(page.Results, s.budget)
	e.diagnose(e.sources[index].name, start, len(page.Results), page.Degraded, err, j.native.Err() != nil)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active--
	e.sources[index].active--
	if !s.evicted && s.job == j {
		state := &s.states[index]
		state.pending = false
		e.acceptLocked(s, state, query, cursor, page.NextCursor, page.Results, page.Degraded, err, e.sources[index].category)
	}
	e.notifyLocked()
}

func (e *Engine) callFallback(s *session, j *job, query, language string, cats []search.Category, cursor string, limit int, waitAll bool) {
	start := time.Now()
	// This shared cache receives only anonymous media. User-specific handoff
	// URLs must be created by the delivery layer after search has returned.
	page, err := e.options.Fallback.SearchPage(j.ctx, search.PageRequest{
		Query: query, Categories: append([]search.Category(nil), cats...), Language: language,
		Offset: cursor, Limit: limit, WaitAll: waitAll,
	})
	if j.ctx.Err() != nil {
		err = j.ctx.Err()
	}
	page.Results = boundedResults(page.Results, s.budget)
	filtered := page.Results[:0]
	for _, result := range page.Results {
		if containsCategory(cats, result.Category) {
			filtered = append(filtered, result)
		}
	}
	page.Results = filtered
	e.diagnose("fallback", start, len(page.Results), page.Degraded, err, j.ctx.Err() != nil)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active--
	e.fallbacks--
	if !s.evicted && s.job == j {
		s.fallback.pending = false
		e.acceptLocked(s, &s.fallback, query, cursor, page.NextOffset, page.Results, page.Degraded, err, "")
	}
	e.notifyLocked()
}

func (e *Engine) acceptLocked(s *session, state *continuation, query, cursor, next string, results []search.MediaResult, degraded bool, err error, category search.Category) {
	if errors.Is(err, search.ErrSourcePaused) {
		state.done = true
		return
	}
	if err != nil {
		state.failed = true
		if state.calls >= maxPages {
			state.done = true
		}
		s.retry = true
		s.degraded = true
		// Errors may contain echoed request URLs. They are deliberately not
		// retained, wrapped or logged, including through an injected logger.
		return
	}
	state.seen = append(state.seen, e.cursorDigest(cursor))
	if next == "" {
		state.done = true
		state.cursor = nil
	} else {
		nextKey := e.cursorDigest(next)
		for _, key := range state.seen {
			if key == nextKey {
				state.done = true
				degraded = true
				break
			}
		}
		if state.calls >= maxPages {
			state.done = true
		}
		if !state.done {
			sealed, sealErr := e.sealCursor(next)
			if sealErr != nil {
				state.done = true
				degraded = true
			} else {
				state.cursor = sealed
			}
		}
	}
	if degraded {
		s.retry = true
		s.degraded = true
	}
	filtered := results[:0]
	for _, r := range results {
		if category != "" && r.Category != category {
			continue
		}
		filtered = append(filtered, r)
	}
	e.mergeLocked(s, query, filtered)
}

func (e *Engine) diagnose(name string, start time.Time, count int, degraded bool, err error, canceled bool) {
	status := "ok"
	if degraded {
		status = "degraded"
	}
	if err != nil {
		status = "error"
	}
	if errors.Is(err, search.ErrSourcePaused) {
		status = "paused"
	}
	if canceled {
		status = "canceled"
	}
	e.options.Logger.Debug("search source", "source", name, "status", status, "results", count, "duration_ms", time.Since(start).Milliseconds())
}

func clearlyWeak(query string, pool []search.MediaResult) bool {
	if len(pool) == 0 {
		return true
	}
	if len(pool) >= 3 {
		return false
	}
	query = strings.ToLower(strings.TrimSpace(query))
	for _, r := range pool {
		if query != "" && strings.Contains(strings.ToLower(r.Title), query) {
			return false
		}
	}
	return true
}

var _ search.Backend = (*Engine)(nil)
