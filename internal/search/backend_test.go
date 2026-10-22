package search

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type deadlineProvider struct{}

func (deadlineProvider) Search(ctx context.Context, _ SearchRequest) (SearchResponse, error) {
	<-ctx.Done()
	return SearchResponse{}, ctx.Err()
}

func TestLegacyBackendRespectsRemainingDeadline(t *testing.T) {
	aggregator := NewAggregator(deadlineProvider{}, AggregatorOptions{
		CacheSize: 8, CacheTTL: time.Minute, MaxResults: 24, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (LegacyBackend{Aggregator: aggregator}).SearchPage(ctx, PageRequest{
		Query: "public benchmark", Categories: []Category{CatImage}, Language: "en",
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("fallback exceeded caller deadline: elapsed=%s err=%v", time.Since(start), err)
	}
}

func TestLegacyBackendRejectsInvalidContinuation(t *testing.T) {
	for _, offset := range []string{"-1", "10", "cursor", "999999999999999999999"} {
		_, err := (LegacyBackend{}).SearchPage(context.Background(), PageRequest{Offset: offset})
		if !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("offset %q accepted", offset)
		}
	}
}

func TestLegacyCacheKeysDoNotRetainQuery(t *testing.T) {
	a := cacheKey("Private benchmark words", []Category{CatImage}, 0, "ru")
	b := cacheKey("private benchmark words", []Category{CatImage}, 0, "ru")
	if len(a) != 64 || a != b || strings.Contains(a, "benchmark") {
		t.Fatal("cache key must contain only a canonical query digest")
	}
	if a == cacheKey("Private benchmark words", []Category{CatVideo}, 0, "ru") ||
		a == cacheKey("Private benchmark words", []Category{CatImage}, 1, "ru") ||
		a == cacheKey("Private benchmark words", []Category{CatImage}, 0, "en") {
		t.Fatal("cache key ignored search parameters")
	}
}

func TestLegacyBackendDoesNotCacheDegradedPools(t *testing.T) {
	provider := &fakeProvider{fn: func(request SearchRequest) (SearchResponse, error) {
		return SearchResponse{
			Results:  testResults(CatImage, request.Pool, string(request.Pool), 12, "wanted"),
			Degraded: request.Pool == PoolDiscovery,
		}, nil
	}}
	backend := LegacyBackend{Aggregator: testAggregator(provider, 24)}
	for range 2 {
		page, err := backend.SearchPage(context.Background(), PageRequest{
			Query: "wanted", Categories: []Category{CatImage}, Language: "en",
		})
		if err != nil || !page.Degraded || len(page.Results) == 0 {
			t.Fatalf("degraded usable pool: count=%d degraded=%t err=%v", len(page.Results), page.Degraded, err)
		}
	}
	if len(provider.requests()) != 4 {
		t.Fatal("degraded pools were cached as healthy")
	}
}

func TestLegacyBackendDistinguishesFailureFromEmptySearch(t *testing.T) {
	provider := &fakeProvider{fn: func(SearchRequest) (SearchResponse, error) {
		return SearchResponse{}, errors.New("upstream unavailable")
	}}
	page, err := (LegacyBackend{Aggregator: testAggregator(provider, 24)}).SearchPage(context.Background(), PageRequest{
		Query: "wanted", Categories: []Category{CatImage}, Language: "en",
	})
	if !errors.Is(err, ErrUnavailable) || !page.Degraded {
		t.Fatalf("failed pools returned a healthy empty page: degraded=%t err=%v", page.Degraded, err)
	}
}

func TestStandaloneLegacyBackendCoalescesColdRequests(t *testing.T) {
	release := make(chan struct{})
	provider := &fakeProvider{fn: func(request SearchRequest) (SearchResponse, error) {
		<-release
		return SearchResponse{Results: testResults(CatImage, request.Pool, string(request.Pool), 12, "wanted")}, nil
	}}
	backend := LegacyBackend{Aggregator: testAggregator(provider, 24), Shared: true}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			page, err := backend.SearchPage(context.Background(), PageRequest{Query: "wanted", Categories: []Category{CatImage}, Language: "en"})
			if err != nil || len(page.Results) == 0 {
				t.Errorf("shared search failed: %v", err)
			}
		}()
	}
	close(start)
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := len(provider.requests()); n != 2 {
		t.Fatalf("eight identical requests made %d provider calls, want two pools", n)
	}
}

func TestLegacyBackendEmptyQueryDoesNotStartRetrieval(t *testing.T) {
	provider := &fakeProvider{fn: func(SearchRequest) (SearchResponse, error) {
		return SearchResponse{}, errors.New("empty queries must not reach provider")
	}}
	backend := LegacyBackend{Aggregator: testAggregator(provider, 24), Shared: true}
	page, err := backend.SearchPage(context.Background(), PageRequest{Query: " \t ", UserID: 1, Scope: "inline"})
	if err != nil || len(page.Results) != 0 || len(provider.requests()) != 0 {
		t.Fatal("empty query started retrieval")
	}
}
