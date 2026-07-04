// Package search defines the media-search domain model and the provider
// abstraction the bot uses to fetch results. Providers (e.g. SearXNG) translate
// their backend's response into a flat slice of MediaResult, which the bot layer
// then renders into Telegram inline results or chat messages.
package search

// Category is the kind of media a query targets.
type Category string

const (
	CatImage Category = "images"
	CatVideo Category = "videos"
)

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

	// Engine is the SearXNG engine that produced this result (e.g. "unsplash",
	// "bing images"). Used to interleave results round-robin across engines so a
	// single high-volume engine can't bury higher-quality ones.
	Engine string

	// Caption is a pre-rendered HTML caption (<=1024 chars).
	Caption string
}
