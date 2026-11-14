package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"searchy/internal/config"
	"searchy/internal/search"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeFactoryUsesDirectSourcesAndLanguageOverride(t *testing.T) {
	for _, locale := range []struct{ language, market string }{
		{"de", "de-DE"}, {"all", ""}, {"en-GB", "en-GB"},
		{"fr-CA", "fr-CA"}, {"zh-Hans-TW", "zh-TW"},
	} {
		t.Run(locale.language, func(t *testing.T) {
			for key, value := range map[string]string{
				"BOT_TOKEN": "test", "SEARCH_BACKEND": "native", "SEARCH_FALLBACK": "none",
				"LANGUAGE": locale.language, "REQUEST_TIMEOUT": "1s", "NATIVE_TIMEOUT": "200ms", "NATIVE_FIRST_PAGE_WAIT": "10ms",
			} {
				t.Setenv(key, value)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var calls []string
			client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				calls = append(calls, r.URL.Host+r.URL.Path+"?mkt="+r.URL.Query().Get("mkt"))
				mu.Unlock()
				body := `{}`
				if r.URL.Host == "www.bing.com" {
					body = `<a class="iusc" m="{&quot;murl&quot;:&quot;https://images.unsplash.com/panda.jpg&quot;,&quot;turl&quot;:&quot;https://images.unsplash.com/panda.jpg&quot;,&quot;purl&quot;:&quot;https://example.org/panda&quot;,&quot;t&quot;:&quot;red panda&quot;,&quot;w&quot;:640,&quot;h&quot;:480}"></a>`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			backend, closeBackend := searchBackend(cfg, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
			defer closeBackend()
			page, err := backend.SearchPage(context.Background(), search.PageRequest{
				Query: "red panda", Categories: []search.Category{search.CatImage}, Language: "ru", UserID: 1, Scope: "inline", Limit: 24,
			})
			if err != nil || len(page.Results) != 1 {
				t.Fatalf("native backend produced %d results, err=%v", len(page.Results), err)
			}
			mu.Lock()
			defer mu.Unlock()
			seenBing := false
			for _, call := range calls {
				if strings.Contains(call, "localhost") || strings.Contains(call, "/search?") {
					t.Fatalf("native-only selection queried SearXNG: %s", call)
				}
				if strings.HasPrefix(call, "www.bing.com/images/") {
					seenBing = strings.HasSuffix(call, "?mkt="+locale.market)
				}
			}
			if !seenBing {
				t.Fatal("operator language override did not reach native source")
			}
		})
	}
}

func TestLegacyFactorySelectsExistingAggregator(t *testing.T) {
	t.Setenv("BOT_TOKEN", "test")
	t.Setenv("SEARCH_BACKEND", "searxng")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	backend, closeBackend := searchBackend(cfg, &http.Client{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer closeBackend()
	if legacy, ok := backend.(search.LegacyBackend); !ok || !legacy.Shared {
		t.Fatal("explicit SearXNG selection did not select legacy backend")
	}
}

func TestNativeFactorySeparatesPageAndGridLimits(t *testing.T) {
	for key, value := range map[string]string{
		"BOT_TOKEN": "test", "SEARCH_BACKEND": "native", "SEARCH_FALLBACK": "none",
		"MAX_RESULTS": "7", "REQUEST_TIMEOUT": "1s", "NATIVE_TIMEOUT": "200ms", "NATIVE_FIRST_PAGE_WAIT": "10ms",
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var fixture strings.Builder
	for i := range 35 {
		fmt.Fprintf(&fixture, `<a class="iusc" m="{&quot;murl&quot;:&quot;https://example.org/%d.jpg&quot;,&quot;turl&quot;:&quot;https://example.org/%d.jpg&quot;,&quot;purl&quot;:&quot;https://example.org/%d&quot;,&quot;t&quot;:&quot;red panda&quot;,&quot;w&quot;:640,&quot;h&quot;:480}"></a>`, i, i, i)
	}
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"batchcomplete":""}`
		if r.URL.Host == "duckduckgo.com" {
			body = `vqd="123-456"`
			if r.URL.Path == "/i.js" {
				body = `{"results":[]}`
			}
		}
		if r.URL.Host == "www.bing.com" {
			body = `<div class="dgControl_list">no results</div>`
			if r.URL.Query().Get("first") == "1" {
				body = fixture.String()
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	backend, closeBackend := searchBackend(cfg, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer closeBackend()
	r := search.PageRequest{Query: "red panda", Categories: []search.Category{search.CatImage}, Language: "en", UserID: 1, Scope: "inline", Limit: cfg.MaxResults}
	seen := make(map[string]bool)
	for pages := 0; ; pages++ {
		if pages >= 10 {
			t.Fatal("pagination did not terminate")
		}
		page, err := backend.SearchPage(context.Background(), r)
		if err != nil || len(page.Results) > cfg.MaxResults {
			t.Fatalf("page %d count=%d err=%v", pages, len(page.Results), err)
		}
		for _, result := range page.Results {
			if seen[result.ID] {
				t.Fatal("duplicate page result")
			}
			seen[result.ID] = true
		}
		if page.NextOffset == "" {
			break
		}
		r.Offset = page.NextOffset
	}
	if len(seen) != 35 {
		t.Fatalf("MAX_RESULTS=7 truncated the whole session: got %d results", len(seen))
	}
	r.Offset, r.Scope, r.WaitAll = "", "grid", true
	page, err := backend.SearchPage(context.Background(), r)
	if err != nil || len(page.Results) != cfg.MaxResults || page.NextOffset != "" {
		t.Fatalf("grid exceeded its own limit: count=%d err=%v", len(page.Results), err)
	}
}

func TestNativeFactoryNormalizesLegacyFallbackPhotos(t *testing.T) {
	for key, value := range map[string]string{
		"BOT_TOKEN": "test", "SEARCH_BACKEND": "native", "SEARCH_FALLBACK": "searxng",
		"SEARXNG_URL":     "http://localhost:8080",
		"REQUEST_TIMEOUT": "1s", "NATIVE_TIMEOUT": "200ms", "NATIVE_FIRST_PAGE_WAIT": "10ms",
		"ENGINES_IMAGES": "flickr", "ENGINES_IMAGES_DISCOVERY": "flickr",
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"batchcomplete":""}`
		switch r.URL.Hostname() {
		case "localhost":
			body = `{"results":[{"template":"images.html","engine":"flickr","engines":["flickr"],"title":"red panda","url":"https://flickr.com/photos/panda","img_src":"https://live.staticflickr.com/panda.jpg","thumbnail_src":"https://live.staticflickr.com/preview.jpg","width":8000,"height":8000}]}`
		case "www.bing.com":
			body = `<div class="dgControl_list">no results</div>`
		case "duckduckgo.com":
			body = `vqd="123-456"`
			if r.URL.Path == "/i.js" {
				body = `{"results":[]}`
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	backend, closeBackend := searchBackend(cfg, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer closeBackend()
	page, err := backend.SearchPage(context.Background(), search.PageRequest{
		Query: "red panda", Categories: []search.Category{search.CatImage}, Language: "en", UserID: 1, Scope: "inline", Limit: 24,
	})
	if err != nil || len(page.Results) != 1 || page.Results[0].MediaURL != "https://live.staticflickr.com/preview.jpg" {
		t.Fatalf("legacy fallback bypassed native normalization: %+v err=%v", page.Results, err)
	}
}

func TestNativePoolScrollsPastFiftyResults(t *testing.T) {
	for key, value := range map[string]string{
		"BOT_TOKEN": "test", "SEARCH_BACKEND": "native", "SEARCH_FALLBACK": "none",
		"REQUEST_TIMEOUT": "5s", "NATIVE_TIMEOUT": "3s", "NATIVE_FIRST_PAGE_WAIT": "1s",
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		if r.URL.Host == "www.bing.com" {
			first, _ := strconv.Atoi(r.URL.Query().Get("first"))
			var sb strings.Builder
			for i := range 35 {
				n := first + i
				fmt.Fprintf(&sb, `<a class="iusc" m="{&quot;murl&quot;:&quot;https://img%d.example.org/%d.jpg&quot;,&quot;turl&quot;:&quot;https://img%d.example.org/t%d.jpg&quot;,&quot;purl&quot;:&quot;https://example.org/%d&quot;,&quot;t&quot;:&quot;panda %d&quot;,&quot;w&quot;:640,&quot;h&quot;:480}"></a>`, n, n, n, n, n, n)
			}
			body = sb.String()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	backend, closeBackend := searchBackend(cfg, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer closeBackend()
	req := search.PageRequest{Query: "panda", Categories: []search.Category{search.CatImage}, Language: "en", UserID: 1, Scope: "inline", Limit: 24}
	total, pages := 0, 0
	for {
		page, err := backend.SearchPage(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		total += len(page.Results)
		pages++
		if page.NextOffset == "" || pages > 10 {
			break
		}
		req.Offset = page.NextOffset
	}
	if total != 240 || pages != 10 {
		t.Fatalf("scrolled %d results over %d pages, want 240 over 10", total, pages)
	}
}
