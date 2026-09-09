// Command bot is the entrypoint for the media-search Telegram bot.
//
// It wires configuration, a tuned HTTP client (for SearXNG), the search
// aggregator (cache + singleflight), and the Telegram handlers, then runs in
// long-polling mode with graceful shutdown.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/FreshLabDev/tg"

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

// telegramStartupTimeout bounds the whole readiness check. The Bot API server
// shares a lifecycle with the bot and often loses the race by a few seconds,
// so tg.Preflight retries getMe within this budget rather than failing on the
// first refused connection.
const telegramStartupTimeout = 2 * time.Minute

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
		BaseURL:                cfg.SearxngURL,
		HTTP:                   client,
		Logger:                 logger,
		EnginesImages:          cfg.EnginesImages,
		EnginesVideos:          cfg.EnginesVideos,
		EnginesImagesDiscovery: cfg.EnginesImagesDiscovery,
		EnginesVideosDiscovery: cfg.EnginesVideosDiscovery,
		SafeSearch:             cfg.SafeSearch,
		ImageProxy:             cfg.ImageProxy,
		Language:               cfg.Language,
	})

	agg := search.NewAggregator(provider, search.AggregatorOptions{
		CacheSize:            cfg.CacheSize,
		CacheTTL:             cfg.CacheTTL,
		MaxResults:           cfg.MaxResults,
		Timeout:              cfg.RequestTimeout,
		DiscoveryPercent:     cfg.DiscoveryPercent,
		DiscoveryWeakPercent: cfg.DiscoveryWeakPercent,
		Logger:               logger,
	})

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// One Telegram client for the whole process: it is safe for concurrent use,
	// and the update workers share its connection pool.
	telegramHTTP := &http.Client{Timeout: 10 * time.Minute}
	api := tg.New(cfg.BotToken,
		tg.WithAPIBase(cfg.TelegramBotAPIBaseURL),
		tg.WithHTTPClient(telegramHTTP),
		tg.WithLogger(logger),
		// Telegram's own default omits update kinds a bot has to ask for, and
		// searchy lives on two of these: inline_query is its primary surface and
		// chosen_inline_result is the only way to learn what was sent.
		tg.WithAllowedUpdates("inline_query", "chosen_inline_result", "message", "callback_query"),
	)

	handlers := bothandlers.NewHandlers(bothandlers.Options{
		Aggregator:      agg,
		Telegram:        api,
		Logger:          logger,
		HTTPClient:      client,
		Store:           store,
		Core:            coreStore,
		Vido:            bridge,
		VidoBotUsername: cfg.VidoBotUsername,
		SharedCacheRoot: cfg.SharedMediaCacheDir,
		MaxResults:      cfg.MaxResults,
		InlineCacheTime: cfg.InlineCacheTime,
		Workers:         cfg.Workers,
		DebounceDelay:   cfg.DebounceDelay,
		RequestTimeout:  cfg.RequestTimeout,
		StatsCacheTTL:   cfg.StatsCacheTTL,
	})

	// Identify ourselves (for inline prompts and the /start text) and make sure no
	// webhook is set (a webhook + getUpdates would 409). GetMe is the token check:
	// fail fast and loud rather than running on with an empty username (which would
	// render "{bot}" prompts as a dangling "@" for the whole process life).
	startupCtx, cancel := context.WithTimeout(ctx, telegramStartupTimeout)
	me, err := api.Preflight(startupCtx, tg.Needs{
		Methods: requiredMethods(cfg.VidoBridgeEnabled),
		Wait:    telegramStartupTimeout,
	})
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			logger.Info("startup cancelled")
			return
		}
		logger.Error("telegram preflight (check BOT_TOKEN / connectivity / server version)", "err", err)
		os.Exit(1)
	}
	handlers.SetBotUsername(me.Username)
	logger.Info("authorized", "username", me.Username, "id", me.ID, "bot_api", tg.BotAPI)
	webhookCtx, webhookCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := api.DeleteWebhook(webhookCtx); err != nil {
		logger.Warn("deleteWebhook", "err", err)
	}
	webhookCancel()
	// Command menus are localized per language (16 × 2 scopes); register them off
	// the critical path so the handful of setMyCommands calls don't delay polling.
	go registerCommands(ctx, api, logger)
	go handlers.RunDeliveryWorker(ctx)
	go handlers.RunNotificationWorker(ctx)

	// Health endpoint for container orchestration.
	healthSrv := startHealthServer(logger, cfg.VidoBridgeEnabled, bridge)
	defer func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = healthSrv.Shutdown(shutdownCtx)
	}()

	logger.Info("starting", "version", buildinfo.Version, "commit", buildinfo.Commit, "built", buildinfo.Date,
		"searxng", cfg.SearxngURL, "workers", cfg.Workers, "cache_ttl", cfg.CacheTTL.String())
	if err := handlers.Run(ctx); err != nil { // blocks until ctx is cancelled (SIGINT/SIGTERM)
		logger.Error("polling stopped", "err", err)
	}
	logger.Info("shutdown complete")
}

// requiredMethods lists what searchy genuinely cannot work without, so a Bot
// API server too old to serve it refuses the start instead of letting the bot
// poll happily and answer nothing.
//
// The media sends below are only reachable through the Vido bridge, so they
// are required only when it is on: a searchy running without the bridge should
// not refuse to start over a delivery path it will never take.
func requiredMethods(vidoBridge bool) []string {
	methods := []string{
		"answerInlineQuery",      // the primary surface
		"answerCallbackQuery",    // every button in every panel
		"deleteMessage",          // Close
		"editMessageMedia",       // paging the result grid in place
		"editMessageReplyMarkup", // retracting a download action that did not bind
		"editMessageText",        // menu navigation
		"sendMessage",            // panels and errors
		"sendPhoto",              // the result grid and single picks
	}
	if vidoBridge {
		methods = append(methods, "sendVideo", "sendAudio", "sendDocument", "sendMediaGroup")
	}
	return methods
}

// registerCommands publishes a minimal command list: private chats see only
// /start; groups see /start + /search. Everything else (help/stats/about) is
// reachable from the in-chat menu buttons, so it's intentionally not listed.
// Descriptions are localized per language (language_code), with an English
// default for clients on any other language.
func registerCommands(ctx context.Context, api *tg.Client, logger *slog.Logger) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	set := func(scope, lang string, cmds []tg.BotCommand) {
		if err := api.SetMyCommandsForScope(cctx, cmds, &tg.BotCommandScope{Type: scope, LanguageCode: lang}); err != nil {
			logger.Warn("setMyCommands", "scope", scope, "lang", lang, "err", err)
		}
	}
	commands := func(lang string) (private, group []tg.BotCommand) {
		start := tg.BotCommand{Command: "start", Description: i18n.T(lang, "cmd.start")}
		return []tg.BotCommand{start},
			[]tg.BotCommand{start, {Command: "search", Description: i18n.T(lang, "cmd.search")}}
	}

	// English default (language_code=""), applied where no localized list matches.
	p, g := commands(i18n.DefaultLang)
	set("default", "", p)
	set("all_private_chats", "", p)
	set("all_group_chats", "", g)

	// Localized overrides per supported language.
	for _, opt := range i18n.LANGUAGE_OPTIONS {
		if opt.Code == i18n.DefaultLang {
			continue
		}
		p, g := commands(opt.Code)
		set("all_private_chats", opt.Code, p)
		set("all_group_chats", opt.Code, g)
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
