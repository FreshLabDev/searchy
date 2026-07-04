package db

import "context"

// Identity, presence and language live in the shared "core" store (internal/core),
// keyed on the global Telegram id. This Store keeps only searchy's private search
// analytics (searches/selections) — no users table, no language column.

// RecordSearch logs one answered search — counts and time only, NO query text.
func (s *Store) RecordSearch(ctx context.Context, userID int64, category string, resultCount, durationMs int, source string) {
	if !s.ready() {
		return
	}
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO searches (user_id, category, result_count, duration_ms, source)
		VALUES ($1, $2, $3, $4, $5)`,
		userID, nz(category), resultCount, durationMs, nz(source))
}

// RecordSelection logs a result the user picked/sent — type + engine only.
func (s *Store) RecordSelection(ctx context.Context, userID int64, resultType, engine string) {
	if !s.ready() {
		return
	}
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO selections (user_id, result_type, engine) VALUES ($1, $2, $3)`,
		userID, nz(resultType), nz(engine))
}

// Stats holds the numbers shown on a /stats panel (personal or global).
type Stats struct {
	Searches  int64
	Users     int64 // global only
	Sent      int64
	PhotoSent int64
	VideoSent int64
	PeakHour  int // 0-23, or -1 if there's no data
}

// UserStats computes one user's stats.
func (s *Store) UserStats(ctx context.Context, userID int64) (Stats, bool) {
	if !s.ready() {
		return Stats{PeakHour: -1}, false
	}
	st := Stats{PeakHour: -1}
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM searches WHERE user_id=$1`, userID).Scan(&st.Searches)
	_ = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE result_type='photo'),
		       count(*) FILTER (WHERE result_type='video')
		FROM selections WHERE user_id=$1`, userID).
		Scan(&st.Sent, &st.PhotoSent, &st.VideoSent)
	st.PeakHour = s.peakHour(ctx, "WHERE user_id=$1", userID)
	return st, true
}

// GlobalStats computes all-users aggregate stats.
func (s *Store) GlobalStats(ctx context.Context) (Stats, bool) {
	if !s.ready() {
		return Stats{PeakHour: -1}, false
	}
	st := Stats{PeakHour: -1}
	_ = s.pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT user_id) FROM searches`).
		Scan(&st.Searches, &st.Users)
	_ = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE result_type='photo'),
		       count(*) FILTER (WHERE result_type='video')
		FROM selections`).
		Scan(&st.Sent, &st.PhotoSent, &st.VideoSent)
	st.PeakHour = s.peakHour(ctx, "", nil)
	return st, true
}

// peakHour returns the local hour (0-23) with the most searches, or -1.
func (s *Store) peakHour(ctx context.Context, where string, arg any) int {
	q := `SELECT EXTRACT(HOUR FROM created_at AT TIME ZONE 'Europe/Kyiv')::int
	      FROM searches ` + where + `
	      GROUP BY 1 ORDER BY count(*) DESC, 1 LIMIT 1`
	var h *int
	var err error
	if arg != nil {
		err = s.pool.QueryRow(ctx, q, arg).Scan(&h)
	} else {
		err = s.pool.QueryRow(ctx, q).Scan(&h)
	}
	if err != nil || h == nil {
		return -1
	}
	return *h
}

// nz maps "" to nil so empty strings store as SQL NULL.
func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}
