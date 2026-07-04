package bot

import (
	"testing"
	"time"

	"searchy/internal/db"
)

func TestStatsCache(t *testing.T) {
	c := newStatsCache(50 * time.Millisecond)

	if _, ok := c.get(7); ok {
		t.Fatal("expected a miss on an empty cache")
	}

	now := time.Now()
	c.put(7, db.Stats{Searches: 3}, now)
	s, ok := c.get(7)
	if !ok || s.st.Searches != 3 {
		t.Fatalf("expected a hit with data, got %+v ok=%v", s, ok)
	}
	if time.Now().After(s.expiresAt) {
		t.Fatal("a freshly put snapshot must not already be expired")
	}
	if !s.updatedAt.Equal(now) {
		t.Fatalf("updatedAt = %v, want %v", s.updatedAt, now)
	}

	// beginRefresh dedups concurrent refreshers.
	if !c.beginRefresh(7) {
		t.Fatal("first beginRefresh should win")
	}
	if c.beginRefresh(7) {
		t.Fatal("a second beginRefresh should lose while one is in flight")
	}
	c.endRefresh(7)
	if !c.beginRefresh(7) {
		t.Fatal("beginRefresh should win again after endRefresh")
	}
	c.endRefresh(7)

	// An old snapshot reads back as expired (so statsView refreshes in the bg).
	c.put(9, db.Stats{}, now.Add(-time.Hour))
	old, _ := c.get(9)
	if !time.Now().After(old.expiresAt) {
		t.Fatal("a snapshot stamped an hour ago must be expired")
	}
}
