// Package core is a nil-safe client for the shared cross-bot "core" Postgres
// (identity, presence, language), keyed on global Telegram ids. Like db.Store,
// every method is a harmless no-op when core is unconfigured or unreachable, so
// the bot keeps working and language reads degrade to the Telegram hint.
//
// Writes go exclusively through core's SECURITY DEFINER functions (core.touch,
// core.set_language, ...); this client only calls those functions and reads.
package core

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BotName is how searchy identifies itself in the core.bot registry.
const BotName = "searchy"

// Scope values for language preferences.
const (
	ScopeUser = "user"
	ScopeChat = "chat"
)

// Language sources, highest priority first (mirrors core.lang_source).
const (
	SourceManual  = "manual"
	SourceAuto    = "auto"
	SourceClient  = "client"
	SourceDefault = "default"
)

type Core struct {
	pool *pgxpool.Pool
}

// Open connects to the core Postgres. Returns (nil, nil) if url is empty (core
// integration disabled). A connect/ping failure returns an error so the caller
// can decide to continue without it.
func Open(ctx context.Context, url string) (*Core, error) {
	if url == "" {
		return nil, nil
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse core url: %w", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 1 // keep one warm so interactive language reads don't cold-connect
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect core: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping core: %w", err)
	}
	return &Core{pool: pool}, nil
}

func (c *Core) Close() {
	if c != nil && c.pool != nil {
		c.pool.Close()
	}
}

func (c *Core) ready() bool { return c != nil && c.pool != nil }

// TouchArgs is one interaction to record: identity + presence + language hint.
type TouchArgs struct {
	UserID       int64
	Username     string
	FirstName    string
	LastName     string
	TelegramLang string // Telegram language_code hint (weakest source, 'client')
	ChatID       *int64 // nil for DM/private → presence rolls up under chat 0
	ChatType     string
	ChatTitle    string
	ChatUsername string
	IsBot        bool
}

// Touch upserts person/name-history/chat/presence and captures the Telegram
// language hint in one server-side call. Best-effort; errors are swallowed.
func (c *Core) Touch(ctx context.Context, a TouchArgs) {
	if !c.ready() {
		return
	}
	_, _ = c.pool.Exec(ctx,
		`SELECT core.touch($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		BotName, a.UserID, ns(a.Username), ns(a.FirstName), ns(a.LastName),
		ns(a.TelegramLang), a.ChatID, ns(a.ChatType), ns(a.ChatTitle), ns(a.ChatUsername), a.IsBot)
}

// SetLanguage records this bot's language claim for a user (ScopeUser) or chat
// (ScopeChat). source is one of the Source* constants.
func (c *Core) SetLanguage(ctx context.Context, scope string, subjectID int64, lang, source string) {
	if !c.ready() {
		return
	}
	_, _ = c.pool.Exec(ctx, `SELECT core.set_language($1,$2,$3,$4,$5)`,
		BotName, scope, subjectID, lang, source)
}

// ClearLanguage removes this bot's language claim (next-priority observation, if
// any, resurfaces).
func (c *Core) ClearLanguage(ctx context.Context, scope string, subjectID int64) {
	if !c.ready() {
		return
	}
	_, _ = c.pool.Exec(ctx, `SELECT core.clear_language($1,$2,$3)`, BotName, scope, subjectID)
}

// EffectiveLanguage returns the resolved language for a user in an optional chat.
// prefer is ScopeUser (personal surfaces — settings/stats/DM) or ScopeChat (group
// broadcast). ok is false when core is unreachable or no preference is set.
func (c *Core) EffectiveLanguage(ctx context.Context, userID int64, chatID *int64, prefer string) (string, bool) {
	if !c.ready() {
		return "", false
	}
	var lang *string
	err := c.pool.QueryRow(ctx, `SELECT core.effective_language($1,$2,$3)`, userID, chatID, prefer).Scan(&lang)
	if err != nil || lang == nil || *lang == "" {
		return "", false
	}
	return *lang, true
}

// ns maps "" to nil so empty strings pass as SQL NULL.
func ns(s string) any {
	if s == "" {
		return nil
	}
	return s
}
