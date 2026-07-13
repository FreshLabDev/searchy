// Command bot is the entrypoint for the media-search Telegram bot.
//
// It wires configuration, a tuned HTTP client (for SearXNG), the search
// aggregator (cache + singleflight), and the Telegram handlers, then runs in
// long-polling mode with graceful shutdown.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	bothandlers "searchy/internal/bot"
	"searchy/internal/buildinfo"
	"searchy/internal/config"
	"searchy/internal/core"
	"searchy/internal/db"
	"searchy/internal/httpx"
	"searchy/internal/i18n"
	"searchy/internal/search"
	"searchy/internal/search/searxng"
	vidobridge "searchy/internal/vido"
)

const (
	telegramStartupTimeout      = 2 * time.Minute
	telegramStartupInitialDelay = time.Second
	telegramStartupMaxDelay     = 5 * time.Second
)

type telegramHTTPError struct {
	operation  string
	statusCode int
}

func (e *telegramHTTPError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.operation, e.statusCode)
}

type telegramPermanentError struct {
	message string
}

func (e *telegramPermanentError) Error() string {
	return e.message
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit (for container healthchecks)")
	flag.Parse()
	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	// Shared, tuned HTTP client for SearXNG (NOT for Telegram long polling, whose
	// held-open connections would trip ResponseHeaderTimeout).
	client := httpx.New()

	provider := searxng.New(searxng.Options{
		BaseURL:       cfg.SearxngURL,
		HTTP:          client,
		Logger:        logger,
		EnginesImages: cfg.EnginesImages,
		EnginesVideos: cfg.EnginesVideos,
		SafeSearch:    cfg.SafeSearch,
		ImageProxy:    cfg.ImageProxy,
		Language:      cfg.Language,
	})

	agg := search.NewAggregator(provider, cfg.CacheSize, cfg.CacheTTL, cfg.MaxResults, cfg.RequestTimeout)

	// Postgres for search analytics. Best-effort: if it's unset or unreachable,
	// the bot still runs (stats disabled).
	store, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		logger.Warn("postgres unavailable — running without persistence", "err", err)
		store = nil
	} else if store != nil {
		logger.Info("postgres connected")
		defer store.Close()
	}

	// Shared cross-bot core store (identity, presence, language). Best-effort: if
	// unset or unreachable, the bot still runs and language falls back to the
	// Telegram hint.
	coreStore, err := core.Open(context.Background(), cfg.CoreDatabaseURL)
	if err != nil {
		logger.Warn("core unavailable — running without shared identity", "err", err)
		coreStore = nil
	} else if coreStore != nil {
		logger.Info("core connected")
		defer coreStore.Close()
	}

	var bridge *vidobridge.Bridge
	if cfg.VidoBridgeEnabled {
		bridge, err = vidobridge.Open(context.Background(), cfg.CoreDatabaseURL)
		if err != nil {
			logger.Warn("vido bridge unavailable — search remains active", "err", err)
			bridge = nil
		} else if bridge == nil {
			logger.Warn("vido bridge disabled — CORE_DATABASE_URL is unset")
		} else {
			logger.Info("vido bridge pool initialized")
			defer bridge.Close()
		}
	}

	handlers := bothandlers.NewHandlers(bothandlers.Options{
		Aggregator:      agg,
		Logger:          logger,
		HTTPClient:      client,
		Store:           store,
		Core:            coreStore,
		Vido:            bridge,
		VidoBotUsername: cfg.VidoBotUsername,
		SharedCacheRoot: cfg.SharedMediaCacheDir,
		MaxResults:      cfg.MaxResults,
		InlineCacheTime: cfg.InlineCacheTime,
		DebounceDelay:   cfg.DebounceDelay,
		RequestTimeout:  cfg.RequestTimeout,
		StatsCacheTTL:   cfg.StatsCacheTTL,
	})

	opts := []bot.Option{
		bot.WithDefaultHandler(handlers.Route),
		bot.WithWorkers(cfg.Workers),
		// go-telegram/bot sends an unterminated multipart body for parameterless
		// calls. Telegram's cloud endpoint tolerates it, but the local Bot API
		// returns an empty response. Validate the token with telegramGetMe below.
		bot.WithSkipGetMe(),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"inline_query", "chosen_inline_result", "message", "callback_query"}),
		bot.WithErrorsHandler(func(err error) {
			logger.Warn("bot error", "err", err)
		}),
	}
	telegramClient := &http.Client{Timeout: 10 * time.Minute}
	opts = append(opts, bot.WithHTTPClient(time.Minute, telegramClient))
	if cfg.TelegramBotAPIBaseURL != "" {
		opts = append(opts, bot.WithServerURL(cfg.TelegramBotAPIBaseURL))
	}

	b, err := bot.New(cfg.BotToken, opts...)
	if err != nil {
		logger.Error("bot init", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Identify ourselves (for inline prompts and the /start text) and make sure no
	// webhook is set (a webhook + getUpdates would 409). GetMe is the token check:
	// fail fast and loud rather than running on with an empty username (which would
	// render "{bot}" prompts as a dangling "@" for the whole process life).
	startupCtx, cancel := context.WithTimeout(ctx, telegramStartupTimeout)
	me, err := telegramGetMeWithRetry(
		startupCtx,
		telegramClient,
		cfg.TelegramBotAPIBaseURL,
		cfg.BotToken,
		telegramStartupInitialDelay,
		telegramStartupMaxDelay,
		func(attempt int, delay time.Duration, retryErr error) {
			logger.Warn("Telegram Bot API not ready — retrying getMe",
				"attempt", attempt, "retry_in", delay.String(), "err", retryErr)
		},
	)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			logger.Info("startup cancelled")
			return
		}
		logger.Error("getMe (check BOT_TOKEN / connectivity)", "err", err)
		os.Exit(1)
	}
	handlers.SetBotUsername(me.Username)
	logger.Info("authorized", "username", me.Username, "id", me.ID)
	webhookCtx, webhookCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := telegramDeleteWebhook(webhookCtx, telegramClient, cfg.TelegramBotAPIBaseURL, cfg.BotToken); err != nil {
		logger.Warn("deleteWebhook", "err", err)
	}
	webhookCancel()
	// Command menus are localized per language (16 × 2 scopes); register them off
	// the critical path so the handful of setMyCommands calls don't delay polling.
	go registerCommands(ctx, b, logger)
	go handlers.RunDeliveryWorker(ctx, b)
	go handlers.RunNotificationWorker(ctx, b)

	// Health endpoint for container orchestration.
	healthSrv := startHealthServer(logger, cfg.VidoBridgeEnabled, bridge)
	defer func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = healthSrv.Shutdown(shutdownCtx)
	}()

	logger.Info("starting", "version", buildinfo.Version, "commit", buildinfo.Commit, "built", buildinfo.Date,
		"searxng", cfg.SearxngURL, "workers", cfg.Workers, "cache_ttl", cfg.CacheTTL.String())
	b.Start(ctx) // blocks until ctx is cancelled (SIGINT/SIGTERM)
	logger.Info("shutdown complete")
}

func telegramGetMe(ctx context.Context, client *http.Client, serverURL, token string) (*models.User, error) {
	endpoint, err := telegramAPIEndpoint(serverURL, token, "getMe")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &telegramPermanentError{message: "build getMe request"}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Do not return the URL: it contains the bot token.
		return nil, fmt.Errorf("perform getMe request: %T", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &telegramHTTPError{operation: "getMe", statusCode: resp.StatusCode}
	}
	var envelope struct {
		OK          bool        `json:"ok"`
		Result      models.User `json:"result"`
		Description string      `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode getMe response: %w", err)
	}
	if !envelope.OK {
		return nil, &telegramPermanentError{message: "getMe rejected the request"}
	}
	return &envelope.Result, nil
}

func telegramGetMeWithRetry(
	ctx context.Context,
	client *http.Client,
	serverURL, token string,
	initialDelay, maxDelay time.Duration,
	onRetry func(attempt int, delay time.Duration, err error),
) (*models.User, error) {
	delay := initialDelay
	if delay <= 0 {
		delay = time.Second
	}
	if maxDelay < delay {
		maxDelay = delay
	}

	for attempt := 1; ; attempt++ {
		me, err := telegramGetMe(ctx, client, serverURL, token)
		if err == nil {
			return me, nil
		}
		if !isRetryableTelegramStartupError(err) {
			return nil, err
		}
		if onRetry != nil {
			onRetry(attempt, delay, err)
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait for getMe: %w", ctx.Err())
		case <-timer.C:
		}
		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

func isRetryableTelegramStartupError(err error) bool {
	var statusErr *telegramHTTPError
	if errors.As(err, &statusErr) {
		return statusErr.statusCode == http.StatusRequestTimeout ||
			statusErr.statusCode == http.StatusTooManyRequests ||
			statusErr.statusCode >= http.StatusInternalServerError
	}
	var permanentErr *telegramPermanentError
	return !errors.As(err, &permanentErr)
}

func telegramDeleteWebhook(ctx context.Context, client *http.Client, serverURL, token string) error {
	endpoint, err := telegramAPIEndpoint(serverURL, token, "deleteWebhook")
	if err != nil {
		return errors.New("build deleteWebhook request")
	}
	endpoint += "?drop_pending_updates=false"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("build deleteWebhook request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("perform deleteWebhook request: %T", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("deleteWebhook returned HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		OK          bool   `json:"ok"`
		Result      bool   `json:"result"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("decode deleteWebhook response: %w", err)
	}
	if !envelope.OK || !envelope.Result {
		return fmt.Errorf("deleteWebhook rejected: %s", envelope.Description)
	}
	return nil
}

func telegramAPIEndpoint(serverURL, token, method string) (string, error) {
	if serverURL == "" {
		serverURL = "https://api.telegram.org"
	}
	base, err := url.Parse(serverURL)
	if err != nil ||
		(base.Scheme != "http" && base.Scheme != "https") ||
		base.Hostname() == "" ||
		base.User != nil ||
		base.RawQuery != "" ||
		base.Fragment != "" {
		return "", &telegramPermanentError{message: "invalid Telegram Bot API base URL"}
	}
	return strings.TrimRight(serverURL, "/") + "/bot" + token + "/" + method, nil
}

// registerCommands publishes a minimal command list: private chats see only
// /start; groups see /start + /search. Everything else (help/stats/about) is
// reachable from the in-chat menu buttons, so it's intentionally not listed.
// Descriptions are localized per language (language_code), with an English
// default for clients on any other language.
func registerCommands(ctx context.Context, b *bot.Bot, logger *slog.Logger) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	set := func(scope models.BotCommandScope, lang string, cmds []models.BotCommand) {
		if _, err := b.SetMyCommands(cctx, &bot.SetMyCommandsParams{Commands: cmds, Scope: scope, LanguageCode: lang}); err != nil {
			logger.Warn("setMyCommands", "lang", lang, "err", err)
		}
	}
	commands := func(lang string) (private, group []models.BotCommand) {
		start := models.BotCommand{Command: "start", Description: i18n.T(lang, "cmd.start")}
		return []models.BotCommand{start},
			[]models.BotCommand{start, {Command: "search", Description: i18n.T(lang, "cmd.search")}}
	}

	// English default (language_code=""), applied where no localized list matches.
	p, g := commands(i18n.DefaultLang)
	set(&models.BotCommandScopeDefault{}, "", p)
	set(&models.BotCommandScopeAllPrivateChats{}, "", p)
	set(&models.BotCommandScopeAllGroupChats{}, "", g)

	// Localized overrides per supported language.
	for _, opt := range i18n.LANGUAGE_OPTIONS {
		if opt.Code == i18n.DefaultLang {
			continue
		}
		p, g := commands(opt.Code)
		set(&models.BotCommandScopeAllPrivateChats{}, opt.Code, p)
		set(&models.BotCommandScopeAllGroupChats{}, opt.Code, g)
	}
}

// runHealthcheck performs an HTTP GET against the local health endpoint and
// returns a process exit code. Used by the container healthcheck since the
// distroless image has no shell or curl.
func runHealthcheck() int {
	port := healthPort(os.Getenv("HEALTH_ADDR"))
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return 0
	}
	return 1
}

// healthPort extracts just the port from HEALTH_ADDR, which may be "8081",
// ":8081", or host-qualified like "0.0.0.0:8081". We always probe 127.0.0.1, so
// only the port matters — naively concatenating a host-qualified addr onto a
// host prefix would build a bogus URL.
func healthPort(addr string) string {
	if addr == "" {
		return "8081"
	}
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	// Not host:port — treat the whole value (sans any leading ':') as the port.
	return strings.TrimPrefix(addr, ":")
}

func startHealthServer(logger *slog.Logger, bridgeEnabled bool, bridge *vidobridge.Bridge) *http.Server {
	addr := os.Getenv("HEALTH_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		bridgeStatus := "disabled"
		if bridgeEnabled {
			bridgeStatus = "degraded"
			pingCtx, cancel := context.WithTimeout(request.Context(), 750*time.Millisecond)
			if bridge != nil && bridge.Healthy(pingCtx) {
				bridgeStatus = "ok"
			}
			cancel()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          true,
			"version":     buildinfo.Version,
			"commit":      buildinfo.Commit,
			"built":       buildinfo.Date,
			"vido_bridge": bridgeStatus,
		})
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("health server", "err", err)
		}
	}()
	return srv
}
