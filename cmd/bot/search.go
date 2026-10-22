package main

import (
	"context"
	"log/slog"
	"net/http"

	"searchy/internal/config"
	"searchy/internal/search"
	"searchy/internal/search/native"
	"searchy/internal/search/searxng"
	"searchy/internal/search/stream"
)

func searchBackend(cfg *config.Config, client *http.Client, logger *slog.Logger) (search.Backend, func()) {
	var legacy search.Backend
	if cfg.SearchBackend == "searxng" || cfg.SearchFallback == "searxng" {
		provider := searxng.New(searxng.Options{
			BaseURL: cfg.SearxngURL, HTTP: client, Logger: logger,
			EnginesImages: cfg.EnginesImages, EnginesVideos: cfg.EnginesVideos,
			EnginesImagesDiscovery: cfg.EnginesImagesDiscovery, EnginesVideosDiscovery: cfg.EnginesVideosDiscovery,
			SafeSearch: cfg.SafeSearch, ImageProxy: cfg.ImageProxy, Language: cfg.Language,
		})
		legacy = search.LegacyBackend{Shared: cfg.SearchBackend == "searxng", Aggregator: search.NewAggregator(provider, search.AggregatorOptions{
			CacheSize: cfg.CacheSize, CacheTTL: cfg.CacheTTL, MaxResults: cfg.MaxResults,
			Timeout: cfg.RequestTimeout, DiscoveryPercent: cfg.DiscoveryPercent,
			DiscoveryWeakPercent: cfg.DiscoveryWeakPercent, Logger: logger,
		})}
	}
	if cfg.SearchBackend == "searxng" {
		return legacy, func() {}
	}
	engine := stream.New(stream.Options{
		Sources: native.NewSources(native.Options{HTTP: client, SafeSearch: cfg.SafeSearch}),
		Rank:    native.Rank, Fallback: native.NewFallback(legacy),
		FirstPageWait: cfg.NativeFirstPageWait, NativeTimeout: cfg.NativeTimeout,
		Timeout: cfg.RequestTimeout, CacheTTL: cfg.CacheTTL, CacheSize: cfg.CacheSize,
		// PageRequest.Limit caps each answer; keep a separate pool for scrolling.
		MaxResults: 50, MaxConcurrent: cfg.SearchMaxConcurrent, Logger: logger,
	})
	var backend search.Backend = engine
	if cfg.Language != "" {
		backend = languageBackend{Backend: backend, language: cfg.Language}
	}
	return backend, engine.Close
}

type languageBackend struct {
	search.Backend
	language string
}

func (b languageBackend) SearchPage(ctx context.Context, request search.PageRequest) (search.Page, error) {
	request.Language = b.language
	return b.Backend.SearchPage(ctx, request)
}
