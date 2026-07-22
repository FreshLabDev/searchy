package search

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeProvider struct {
	mu        sync.Mutex
	calls     []SearchRequest
	fn        func(SearchRequest) (SearchResponse, error)
	active    atomic.Int32
	maxActive atomic.Int32
}

func (f *fakeProvider) Search(_ context.Context, request SearchRequest) (SearchResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, request)
	f.mu.Unlock()
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		maximum := f.maxActive.Load()
		if active <= maximum || f.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	return f.fn(request)
}

func (f *fakeProvider) requests() []SearchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SearchRequest(nil), f.calls...)
}

func testAggregator(provider Provider, maxResults int) *Aggregator {
	return NewAggregator(provider, AggregatorOptions{
		CacheSize: 64, CacheTTL: time.Minute, MaxResults: maxResults, Timeout: time.Second,
		DiscoveryPercent: 30, DiscoveryWeakPercent: 50,
	})
}

func TestSearchRunsParallelPoolsAndEnglishFallback(t *testing.T) {
	provider := &fakeProvider{fn: func(request SearchRequest) (SearchResponse, error) {
		switch {
		case request.Language == "ru" && request.Pool == PoolCore:
			return SearchResponse{Results: testResults(CatImage, PoolCore, "ru-core", 4, "wanted")}, nil
		case request.Language == "ru" && request.Pool == PoolDiscovery:
			return SearchResponse{Results: testResults(CatImage, PoolDiscovery, "ru-discovery", 4, "wanted")}, nil
		case request.Language == "en" && request.Pool == PoolCore:
			return SearchResponse{Results: testResults(CatImage, PoolCore, "en-core", 12, "wanted")}, nil
		default:
			return SearchResponse{}, fmt.Errorf("unexpected request: %+v", request)
		}
	}}

	results, _ := testAggregator(provider, 50).Search(context.Background(), "wanted", []Category{CatImage}, 0, "ru")
	if len(results) == 0 {
		t.Fatal("expected merged results")
	}
	if provider.maxActive.Load() < 2 {
		t.Fatalf("primary pools did not overlap, max active = %d", provider.maxActive.Load())
	}
	calls := provider.requests()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want core+discovery+English core", len(calls))
	}
	for _, call := range calls {
		if call.Language == "en" && call.Pool == PoolDiscovery {
			t.Fatal("English discovery fallback must not run")
		}
	}
}

func TestSearchCacheIsolatedByLanguage(t *testing.T) {
	provider := &fakeProvider{fn: func(request SearchRequest) (SearchResponse, error) {
		count := 12
		if request.Pool == PoolDiscovery {
			count = 6
		}
		return SearchResponse{Results: testResults(CatImage, request.Pool, request.Language+string(request.Pool), count, "wanted")}, nil
	}}
	aggregator := testAggregator(provider, 50)
	for _, language := range []string{"ru", "ru", "uk"} {
		aggregator.Search(context.Background(), "wanted", []Category{CatImage}, 0, language)
	}
	if calls := len(provider.requests()); calls != 4 {
		t.Fatalf("provider calls = %d, want two pools for each distinct language", calls)
	}
}

func TestSearchDoesNotCacheScheduledPoolFailure(t *testing.T) {
	provider := &fakeProvider{fn: func(request SearchRequest) (SearchResponse, error) {
		if request.Pool == PoolDiscovery {
			return SearchResponse{}, errors.New("discovery unavailable")
		}
		return SearchResponse{Results: testResults(CatImage, PoolCore, "core", 12, "wanted")}, nil
	}}
	aggregator := testAggregator(provider, 50)
	for range 2 {
		results, _ := aggregator.Search(context.Background(), "wanted", []Category{CatImage}, 0, "en")
		if len(results) == 0 {
			t.Fatal("successful core pool should still be returned")
		}
	}
	if calls := len(provider.requests()); calls != 4 {
		t.Fatalf("provider calls = %d, want failed pair retried", calls)
	}
}

func TestSearchDoesNotCacheEnglishFallbackFailure(t *testing.T) {
	provider := &fakeProvider{fn: func(request SearchRequest) (SearchResponse, error) {
		if request.Language == "en" {
			return SearchResponse{}, errors.New("fallback unavailable")
		}
		return SearchResponse{Results: testResults(CatImage, request.Pool, string(request.Pool), 4, "wanted")}, nil
	}}
	aggregator := testAggregator(provider, 50)
	for range 2 {
		results, _ := aggregator.Search(context.Background(), "wanted", []Category{CatImage}, 0, "ru")
		if len(results) == 0 {
			t.Fatal("localized pools should survive fallback failure")
		}
	}
	if calls := len(provider.requests()); calls != 6 {
		t.Fatalf("provider calls = %d, want primary pair and fallback retried", calls)
	}
}

func TestSearchReturnsEmptyWhenBothPoolsFail(t *testing.T) {
	provider := &fakeProvider{fn: func(SearchRequest) (SearchResponse, error) {
		return SearchResponse{}, errors.New("unavailable")
	}}
	results, hasMore := testAggregator(provider, 50).Search(context.Background(), "wanted", []Category{CatImage}, 0, "en")
	if len(results) != 0 || hasMore {
		t.Fatalf("results=%d hasMore=%v, want empty", len(results), hasMore)
	}
}

func TestRankingUsesConsensusRRFAndTitleCoverage(t *testing.T) {
	in := []MediaResult{
		media("single", CatImage, PoolCore, "https://single.example/a", "wanted", 1, []string{"one"}, []int{20}, 0),
		media("consensus", CatImage, PoolCore, "https://consensus.example/a", "wanted", 0.9, []string{"one", "two", "three"}, []int{2, 3, 4}, 1),
		media("irrelevant", CatImage, PoolCore, "https://irrelevant.example/a", "other words", 0.9, []string{"one"}, []int{1}, 2),
		media("rrf", CatImage, PoolCore, "https://rrf.example/a", "wanted", 0, []string{"one", "two"}, []int{1, 2}, 3),
	}
	ranked := rankGroup("wanted", in)
	if ranked[0].ID != "consensus" {
		t.Fatalf("top result = %s, want consensus", ranked[0].ID)
	}
	if ranked[len(ranked)-1].ID != "irrelevant" {
		t.Fatalf("last result = %s, want irrelevant title", ranked[len(ranked)-1].ID)
	}
	if ranked[2].ID == "rrf" && ranked[2].RankScore == 0 {
		t.Fatal("positions must contribute through RRF when score is missing")
	}
}

func TestRankingIsStableWithoutBackendMetadata(t *testing.T) {
	in := []MediaResult{
		{ID: "b", Category: CatImage, Title: "", SourceOrder: 2},
		{ID: "c", Category: CatImage, Title: "", SourceOrder: 1},
		{ID: "a", Category: CatImage, Title: "", SourceOrder: 1},
	}
	ranked := rankGroup("wanted", in)
	want := []string{"a", "c", "b"}
	for i := range want {
		if ranked[i].ID != want[i] {
			t.Fatalf("rank %d = %s, want %s", i, ranked[i].ID, want[i])
		}
	}
}

func TestPostprocessMergesDuplicateMetadata(t *testing.T) {
	aggregator := &Aggregator{maxResults: 10, discoveryPercent: 30, discoveryWeakPercent: 50}
	core := []MediaResult{
		media("same", CatImage, PoolCore, "https://same.example/a", "wanted", 0.5, []string{"bing"}, []int{1}, 1),
		media("same", CatImage, PoolCore, "https://same.example/a", "wanted", 0.8, []string{"ddg"}, []int{2}, 2),
	}
	out := aggregator.postprocess("wanted", []Category{CatImage}, core, nil, map[Category]bool{CatImage: false})
	if len(out) != 1 {
		t.Fatalf("results = %d, want one duplicate", len(out))
	}
	if out[0].Score != 0.8 || len(out[0].Engines) != 2 || len(out[0].Positions) != 2 {
		t.Fatalf("metadata not merged: %+v", out[0])
	}
}

func TestPostprocessKeepsMixedAndDiscoveryQuotas(t *testing.T) {
	aggregator := &Aggregator{maxResults: 40, discoveryPercent: 30, discoveryWeakPercent: 50}
	core := append(testResults(CatImage, PoolCore, "ci", 20, "wanted"), testResults(CatVideo, PoolCore, "cv", 20, "wanted")...)
	discovery := append(testResults(CatImage, PoolDiscovery, "di", 20, "wanted"), testResults(CatVideo, PoolDiscovery, "dv", 20, "wanted")...)

	normal := aggregator.postprocess("wanted", []Category{CatImage, CatVideo}, core, discovery, map[Category]bool{})
	assertFirstTenMix(t, normal, 3)
	weak := aggregator.postprocess("wanted", []Category{CatImage, CatVideo}, core, discovery, map[Category]bool{CatImage: true, CatVideo: true})
	assertFirstTenMix(t, weak, 5)
}

func TestPostprocessLimitsDominantHostAndIsDeterministic(t *testing.T) {
	aggregator := &Aggregator{maxResults: 20, discoveryPercent: 30, discoveryWeakPercent: 50}
	core := make([]MediaResult, 0, 20)
	for i := range 10 {
		core = append(core, media(fmt.Sprintf("same-%d", i), CatImage, PoolCore, "https://same.example/item", "wanted", 1, []string{"bing"}, []int{i + 1}, i))
	}
	for i := range 10 {
		core = append(core, media(fmt.Sprintf("other-%d", i), CatImage, PoolCore, fmt.Sprintf("https://host-%d.example/item", i), "wanted", 0.5, []string{"ddg"}, []int{i + 11}, i+10))
	}
	one := aggregator.postprocess("wanted", []Category{CatImage}, core, nil, map[Category]bool{})
	two := aggregator.postprocess("wanted", []Category{CatImage}, core, nil, map[Category]bool{})
	sameHost := 0
	for i := range 10 {
		if resultHost(one[i]) == "same.example" {
			sameHost++
		}
		if one[i].ID != two[i].ID {
			t.Fatalf("non-deterministic order at %d: %s != %s", i, one[i].ID, two[i].ID)
		}
	}
	if sameHost > 2 {
		t.Fatalf("dominant host count in first 10 = %d, want <= 2", sameHost)
	}
}

func TestDomainDiversityPreservesDiscoverySchedule(t *testing.T) {
	aggregator := &Aggregator{maxResults: 20, discoveryPercent: 30, discoveryWeakPercent: 50}
	core := testResults(CatImage, PoolCore, "core", 20, "wanted")
	for i := 0; i < 10; i++ {
		core[i].PageURL = "https://same.example/item"
	}
	discovery := testResults(CatImage, PoolDiscovery, "discovery", 20, "wanted")
	out := aggregator.postprocess("wanted", []Category{CatImage}, core, discovery, map[Category]bool{})
	if got := countPool(out[:10], PoolDiscovery); got != 3 {
		t.Fatalf("discovery results in first 10 = %d, want 3", got)
	}
	if got := countHost(out[:10], "same.example"); got > 2 {
		t.Fatalf("dominant host count in first 10 = %d, want <= 2", got)
	}
}

func TestDiscoveryQualityGateProtectsVideoFirstPage(t *testing.T) {
	aggregator := &Aggregator{maxResults: 30, discoveryPercent: 30, discoveryWeakPercent: 50}
	core := testResults(CatVideo, PoolCore, "core-video", 20, "wanted exact video")
	for i := range core {
		core[i].PageURL = "https://youtube.com/watch"
	}
	discovery := testResults(CatVideo, PoolDiscovery, "discovery-video", 20, "unrelated material")
	out := aggregator.postprocess("wanted exact video", []Category{CatVideo}, core, discovery, map[Category]bool{CatVideo: true})
	for i := range 10 {
		if out[i].Pool != PoolCore {
			t.Fatalf("irrelevant discovery displaced core at rank %d", i+1)
		}
	}
	if countPool(out[:20], PoolDiscovery) == 0 {
		t.Fatal("quality gate should postpone discovery, not remove it")
	}
}

func TestTitleCoverageRewardsOrderedQueryPhrase(t *testing.T) {
	query := "old cable access mascot commercial"
	phrase := titleCoverage(query, "Duck mascot commercial")
	bag := titleCoverage(query, "card access control cable")
	if phrase <= bag {
		t.Fatalf("phrase coverage %.3f must exceed unordered token coverage %.3f", phrase, bag)
	}
}

func TestVideoHostConcentrationDoesNotDisplaceExactResults(t *testing.T) {
	aggregator := &Aggregator{maxResults: 10, discoveryPercent: 30, discoveryWeakPercent: 50}
	core := make([]MediaResult, 0, 15)
	for i := range 10 {
		core = append(core, media(fmt.Sprintf("youtube-%d", i), CatVideo, PoolCore, "https://youtube.com/watch", "wanted video", 1-float64(i)/100, []string{"youtube"}, []int{i + 1}, i))
	}
	for i := range 5 {
		core = append(core, media(fmt.Sprintf("other-video-%d", i), CatVideo, PoolCore, fmt.Sprintf("https://video-%d.example/watch", i), "other", 0.1, []string{"other"}, []int{i + 20}, i+10))
	}
	out := aggregator.postprocess("wanted video", []Category{CatVideo}, core, nil, map[Category]bool{})
	if got := countHost(out, "youtube.com"); got != 10 {
		t.Fatalf("YouTube results in first page = %d, want 10 exact matches", got)
	}
}

func TestVideoHostCapUsesRelevantAlternatives(t *testing.T) {
	aggregator := &Aggregator{maxResults: 10, discoveryPercent: 30, discoveryWeakPercent: 50}
	core := make([]MediaResult, 0, 18)
	for i := range 10 {
		core = append(core, media(fmt.Sprintf("youtube-%d", i), CatVideo, PoolCore, "https://youtube.com/watch", "wanted video", 1-float64(i)/100, []string{"youtube"}, []int{i + 1}, i))
	}
	for i := range 8 {
		core = append(core, media(fmt.Sprintf("other-video-%d", i), CatVideo, PoolCore, fmt.Sprintf("https://video-%d.example/watch", i), "wanted video", 0.1, []string{"other"}, []int{i + 20}, i+10))
	}
	out := aggregator.postprocess("wanted video", []Category{CatVideo}, core, nil, map[Category]bool{})
	if got := countHost(out, "youtube.com"); got > 2 {
		t.Fatalf("YouTube results in first page = %d, want <= 2 with relevant alternatives", got)
	}
}

func TestWeakCategorySignals(t *testing.T) {
	if !weakCategory("wanted", rankGroup("wanted", testResults(CatImage, PoolCore, "few", 9, "wanted")), false) {
		t.Fatal("fewer than ten core results must be weak")
	}
	irrelevant := testResults(CatImage, PoolCore, "irrelevant", 12, "other")
	if !weakCategory("wanted", rankGroup("wanted", irrelevant), false) {
		t.Fatal("low title coverage must be weak")
	}
	healthy := testResults(CatImage, PoolCore, "healthy", 20, "wanted")
	if weakCategory("wanted", rankGroup("wanted", healthy), false) {
		t.Fatal("diverse relevant results must be healthy")
	}
	video := testResults(CatVideo, PoolCore, "video", 20, "wanted")
	for i := range video {
		video[i].PageURL = "https://youtube.com/watch"
	}
	if !weakCategory("wanted", rankGroup("wanted", video), false) {
		t.Fatal("one video platform occupying over 60% must be weak")
	}
}

func TestSearchLanguage(t *testing.T) {
	cases := map[string]string{
		"en": "en", "ru": "ru", "uk": "uk", "es": "es", "fr": "fr",
		"de": "de", "it": "it", "pl": "pl", "cs": "cs", "tr": "tr",
		"sv": "sv", "be": "be", "ca": "ca", "ja": "ja", "ar": "ar",
		"ru-RU": "ru", "zh": "zh-CN", "zh-Hans-CN": "zh-CN", "bogus": "en",
	}
	for input, want := range cases {
		if got := SearchLanguage(input); got != want {
			t.Errorf("SearchLanguage(%q) = %q, want %q", input, got, want)
		}
	}
}

func assertFirstTenMix(t *testing.T, results []MediaResult, wantDiscovery int) {
	t.Helper()
	images, videos, discovery := 0, 0, 0
	for _, result := range results[:10] {
		if result.Category == CatImage {
			images++
		} else if result.Category == CatVideo {
			videos++
		}
		if result.Pool == PoolDiscovery {
			discovery++
		}
	}
	if images != 5 || videos != 5 || discovery != wantDiscovery {
		t.Fatalf("first 10 images=%d videos=%d discovery=%d, want 5/5/%d", images, videos, discovery, wantDiscovery)
	}
}

func countPool(results []MediaResult, pool Pool) int {
	count := 0
	for _, result := range results {
		if result.Pool == pool {
			count++
		}
	}
	return count
}

func countHost(results []MediaResult, hostname string) int {
	count := 0
	for _, result := range results {
		if resultHost(result) == hostname {
			count++
		}
	}
	return count
}

func testResults(category Category, pool Pool, prefix string, count int, title string) []MediaResult {
	results := make([]MediaResult, 0, count)
	for i := range count {
		results = append(results, media(
			fmt.Sprintf("%s-%02d", prefix, i), category, pool,
			fmt.Sprintf("https://%s-%02d.example/item", prefix, i), title,
			1-float64(i)/100, []string{prefix}, []int{i + 1}, i,
		))
	}
	return results
}

func media(id string, category Category, pool Pool, pageURL, title string, score float64, engines []string, positions []int, order int) MediaResult {
	return MediaResult{
		ID: id, Category: category, Pool: pool, PageURL: pageURL, Title: title,
		Score: score, Engine: engines[0], Engines: engines, Positions: positions, SourceOrder: order,
		Width: 1280, Height: 720,
	}
}
