// Package vido builds the handoff to the separate @vido download bot.
//
// vido (a Python bot) does NOT accept a raw URL in its /start deep-link: its
// start payload is limited to 64 chars and it only resolves pre-registered
// `ia_<token>` tokens (stored in its SQLite DB, 6h TTL) or the literal
// "settings". So a working "Download" handoff requires minting a token on
// vido's side first, then deep-linking to it:
//
//	https://t.me/<vido_username>?start=ia_<token>
//
// In v1 we do not mint tokens (that needs a small HTTP endpoint added to vido
// that wraps its register_inline_deep_link). The Minter interface below is the
// seam for that future endpoint; until it's wired, callers fall back to opening
// the media's own page. See README "Roadmap → vido download handoff".
package vido

import (
	"context"
	"fmt"
)

// DeepLink returns the t.me handoff URL for an already-minted vido token.
func DeepLink(username, token string) string {
	return fmt.Sprintf("https://t.me/%s?start=ia_%s", username, token)
}

// Minter mints a vido deep-link token for a source URL. The future
// implementation will POST to a vido HTTP endpoint that calls
// register_inline_deep_link and returns the bare token (without the "ia_"
// prefix). Not used in v1.
type Minter interface {
	Mint(ctx context.Context, sourceURL string, userID int64) (token string, err error)
}
