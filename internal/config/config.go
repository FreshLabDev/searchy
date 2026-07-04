// Package config loads all runtime configuration from environment variables.
// Every value has a sensible default so the bot runs with only BOT_TOKEN set.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Telegram
	BotToken      string
	WebhookSecret string // only used in webhook mode (not wired in v1)

	// SearXNG backend
	SearxngURL    string
	EnginesImages string // comma-separated engine names pinned for images
	EnginesVideos string // comma-separated engine names pinned for videos
	SafeSearch    int    // 0=off, 1=moderate, 2=strict
	ImageProxy    bool   // route image URLs through SearXNG's proxy
	Language      string // SearXNG search language; "all" = neutral (no filter)

	// Search behaviour
	RequestTimeout time.Duration // per backend call
	MaxResults     int           // hard cap per inline answer (Telegram max 50)
	DebounceDelay  time.Duration // inline debounce per user

	// Cache
	CacheTTL  time.Duration
	CacheSize int

	// Concurrency / HTTP
	Workers int

	// Integrations
	VidoBotUsername string // @username (no @) of the vido download bot, for handoff

	// Postgres for search analytics (stats). Empty disables persistence
	// (the bot still works; stats just become unavailable).
	DatabaseURL string

	// Shared cross-bot "core" Postgres (identity, presence, language). Empty
	// disables core integration — the bot still works, and language falls back
	// to the Telegram hint. Uses a least-privilege role (searchy_core).
	CoreDatabaseURL string

	// Telegram inline caching
	InlineCacheTime int // answerInlineQuery cache_time (seconds)

	// Stats snapshot cache: how long a computed /stats result is served before a
	// background refresh (mirrors vido's STATS_REFRESH_SECONDS). Stats are
	// inherently slightly stale (the panel shows an "updated HH:MM"), so a few
	// minutes is invisible and spares the DB on every tab toggle.
	StatsCacheTTL time.Duration
}

func Load() (*Config, error) {
	c := &Config{
		BotToken:      os.Getenv("BOT_TOKEN"),
		WebhookSecret: os.Getenv("WEBHOOK_SECRET"),
		SearxngURL:    env("SEARXNG_URL", "http://localhost:8080"),
		// Pinned sets, sent via engines= WITHOUT categories (see provider). Kept
		// under the concurrency limit (~8 ok, ~15 overloads → proxy errors).
		// bing/ddg give volume; unsplash/flickr/wikicommons add CLEAN curated
		// photos (bing/ddg flood news/junk for place/topic queries). The bot
		// interleaves engines round-robin so curated photos aren't buried.
		// duckduckgo videos is a cross-platform meta-engine.
		// flickr dropped — it's slow (~1.5s, often times out). unsplash (0.43s) +
		// wikicommons (0.67s) give the clean curated photos fast.
		EnginesImages: env("ENGINES_IMAGES", "bing images,duckduckgo images,unsplash,wikicommons.images"),
		EnginesVideos: env("ENGINES_VIDEOS", "youtube,dailymotion,duckduckgo videos"),
		SafeSearch:    envInt("SAFE_SEARCH", 0),
		ImageProxy:    envBool("IMAGE_PROXY", false),
		// "all" = no language filter (neutral). Combined with Tor egress this
		// fully neutralizes geo/language bias. Override to e.g. "en"/"uk" if wanted.
		Language: env("LANGUAGE", "all"),
		// Ceiling for a SearXNG call; direct returns in ~0.8s, so this rarely bites
		// — it just prevents a hung call from blocking. Must exceed SearXNG's
		// max_request_timeout (3s).
		RequestTimeout:  envDur("REQUEST_TIMEOUT", 5*time.Second),
		MaxResults:      envInt("MAX_RESULTS", 50),
		DebounceDelay:   envDur("DEBOUNCE_DELAY", 150*time.Millisecond),
		CacheTTL:        envDur("CACHE_TTL", 5*time.Minute),
		CacheSize:       envInt("CACHE_SIZE", 2048),
		Workers:         envInt("WORKERS", 32),
		VidoBotUsername: strings.TrimPrefix(os.Getenv("VIDO_BOT_USERNAME"), "@"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		CoreDatabaseURL: os.Getenv("CORE_DATABASE_URL"),
		InlineCacheTime: envInt("INLINE_CACHE_TIME", 300),
		StatsCacheTTL:   envDur("STATS_CACHE_TTL", 10*time.Minute),
	}

	if c.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required")
	}
	if c.MaxResults > 50 {
		c.MaxResults = 50 // Telegram hard limit for answerInlineQuery
	}
	if c.MaxResults < 1 {
		c.MaxResults = 1 // 0 returns nothing; a negative cap panics make([]T, 0, n)
	}
	if c.Workers < 1 {
		c.Workers = 1 // 0 spawns no update consumers — the bot would silently go deaf
	}
	if c.StatsCacheTTL < 10*time.Second {
		c.StatsCacheTTL = 10 * time.Second // a tiny floor; 0 would defeat the cache
	}
	if c.SafeSearch < 0 || c.SafeSearch > 2 {
		c.SafeSearch = 0 // default: show everything, no safe search
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
