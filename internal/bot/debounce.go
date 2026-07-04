package bot

import (
	"context"
	"sync"
	"time"
)

// debouncer coalesces the rapid stream of inline_query updates a single user
// generates while typing. Each new query for a user cancels the previous one's
// context; the handler then waits `delay` and proceeds only if no newer query
// arrived in the meantime. This turns N searches-per-word into ~1, which is the
// single biggest win for both latency and backend load.
type debouncer struct {
	mu    sync.Mutex
	calls map[int64]inflight
	seq   uint64
	delay time.Duration
}

// inflight tracks a user's current in-flight gate. gen disambiguates entries so
// a finished/superseded call only removes its OWN map slot, never a newer one.
type inflight struct {
	cancel context.CancelFunc
	gen    uint64
}

func newDebouncer(delay time.Duration) *debouncer {
	return &debouncer{calls: make(map[int64]inflight), delay: delay}
}

// gate registers a new in-flight query for userID, cancelling any previous one.
// It returns a derived context, its cancel func, and ok: ok=true once the
// debounce window elapses without a newer query; ok=false means this query was
// superseded and must be abandoned. The caller MUST defer the returned cancel —
// that releases the context (detaching it from the parent) and prunes the map
// entry, so neither grows unbounded as distinct inline users accumulate.
func (d *debouncer) gate(parent context.Context, userID int64) (context.Context, context.CancelFunc, bool) {
	ctx, cancel := context.WithCancel(parent)

	d.mu.Lock()
	d.seq++
	gen := d.seq
	if prev, ok := d.calls[userID]; ok {
		prev.cancel()
	}
	d.calls[userID] = inflight{cancel: cancel, gen: gen}
	d.mu.Unlock()

	// Prune this user's entry once its context ends (superseded or handler done),
	// unless a newer query already replaced it.
	go func() {
		<-ctx.Done()
		d.mu.Lock()
		if cur, ok := d.calls[userID]; ok && cur.gen == gen {
			delete(d.calls, userID)
		}
		d.mu.Unlock()
	}()

	select {
	case <-time.After(d.delay):
		return ctx, cancel, true
	case <-ctx.Done():
		return ctx, cancel, false
	}
}
