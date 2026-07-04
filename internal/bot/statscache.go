package bot

import (
	"sync"
	"time"

	"searchy/internal/db"
)

// statsCache is an in-memory snapshot cache for /stats, mirroring vido's
// StatsSnapshotManager: a computed result is served for ttl, after which the
// next viewer triggers a single background refresh (stale-while-revalidate).
// This keeps the DB off the hot path — repeatedly toggling the My/Bot tabs no
// longer fires three queries (incl. a full-table peak-hour scan) per click.
//
// Key convention: a Telegram user id (always > 0) for personal stats, and 0 for
// the shared global snapshot.
type statsCache struct {
	mu       sync.Mutex
	snaps    map[int64]statsSnap
	inflight map[int64]bool
	ttl      time.Duration
}

type statsSnap struct {
	st        db.Stats
	updatedAt time.Time // when this snapshot was computed (shown as "updated HH:MM")
	expiresAt time.Time
}

func newStatsCache(ttl time.Duration) *statsCache {
	return &statsCache{
		snaps:    make(map[int64]statsSnap),
		inflight: make(map[int64]bool),
		ttl:      ttl,
	}
}

// get returns the cached snapshot (even if expired, so it can be served stale)
// and whether one exists.
func (c *statsCache) get(key int64) (statsSnap, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.snaps[key]
	return s, ok
}

// put stores a freshly computed snapshot, stamping its compute time and expiry.
func (c *statsCache) put(key int64, st db.Stats, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snaps[key] = statsSnap{st: st, updatedAt: now, expiresAt: now.Add(c.ttl)}
}

// beginRefresh claims the right to refresh key, returning false if a refresh is
// already in flight (so concurrent viewers don't stampede the DB).
func (c *statsCache) beginRefresh(key int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight[key] {
		return false
	}
	c.inflight[key] = true
	return true
}

func (c *statsCache) endRefresh(key int64) {
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
}
