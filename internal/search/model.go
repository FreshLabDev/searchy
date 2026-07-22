// Package search defines the media-search domain model and the provider
// abstraction the bot uses to fetch results. Providers (e.g. SearXNG) translate
// their backend's response into a flat slice of MediaResult, which the bot layer
// then renders into Telegram inline results or chat messages.
package search

import "strings"

// Category is the kind of media a query targets.
type Category string

const (
	CatImage Category = "images"
	CatVideo Category = "videos"
)

// Pool identifies the relevance lane used for a backend request.
type Pool string

const (
	PoolCore      Pool = "core"
	PoolDiscovery Pool = "discovery"
)

// SearchRequest is the provider-facing search contract. The aggregator issues
// one request per pool and may add an English core fallback when the user's
// locale produces a weak result set.
type SearchRequest struct {
	Query      string
	Categories []Category
	Page       int
	Language   string
	Pool       Pool
}

// SearchResponse carries normalized results plus backend quality signals. It
// deliberately contains no echoed query text.
type SearchResponse struct {
	Results  []MediaResult
	HasMore  bool
	RawCount int
	Degraded bool
}

// MediaResult is the normalized result every provider emits, independent of the
// backend that produced it. The bot layer maps it to a Telegram inline result.
type MediaResult struct {
	Category Category

	// ID is a stable, short identifier (<=64 bytes) used as the Telegram inline
	// result id. Derived by hashing the canonical source URL.
	ID string

	Title  string
	Author string // uploader / source site, optional

	// MediaURL is the direct media URL for images (JPEG) or the embed/iframe URL
	// for videos (used with mime_type=text/html).
	MediaURL string
	// ThumbURL is a JPEG thumbnail. Mandatory for video inline results.
	ThumbURL string
	// PageURL is the human-facing page (watch/source page). Used as the message
	// body for video results and as the target of the "Download" handoff button.
	PageURL string

	MIMEType    string // "text/html" for video embeds; empty for images
	DurationSec int    // video duration in seconds, 0 if unknown

	Width  int
	Height int

	// Engine remains the primary source recorded by the existing analytics.
	// Engines contains every SearXNG engine that confirmed the result.
	Engine    string
	Engines   []string
	Score     float64
	Positions []int
	Pool      Pool

	// RankScore and SourceOrder are transient ranking metadata. They remain only
	// in the in-process result/cache objects and are never persisted.
	RankScore   float64
	SourceOrder int

	// Caption is a pre-rendered HTML caption (<=1024 chars).
	Caption string
}

// SearchLanguage maps Searchy's 16 UI language codes to locales accepted by
// SearXNG. Unknown values fall back to English; Chinese needs an explicit script
// and region in the currently deployed SearXNG locale list.
func SearchLanguage(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	if code == "zh" {
		return "zh-CN"
	}
	switch code {
	case "en", "ru", "uk", "es", "fr", "de", "it", "pl", "cs", "tr", "sv", "be", "ca", "ja", "ar":
		return code
	default:
		return "en"
	}
}
