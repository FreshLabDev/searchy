package searxng

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"searchy/internal/search"
)

func TestSearchPostsPinnedEnginesWithoutCategories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query leaked into URL: %q", r.URL.RawQuery)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("q"); got != "private needle" {
			t.Errorf("q = %q", got)
		}
		if got := r.Form.Get("engines"); got != "bing images,duckduckgo images" {
			t.Errorf("engines = %q", got)
		}
		if _, present := r.Form["categories"]; present {
			t.Error("Search must never send SearXNG categories")
		}
		if got := r.Form.Get("language"); got != "ru" {
			t.Errorf("language = %q, want ru", got)
		}
		if got := r.Form.Get("format"); got != "json" {
			t.Errorf("format = %q, want json", got)
		}
		if got := r.Form.Get("pageno"); got != "3" {
			t.Errorf("pageno = %q, want 3", got)
		}
		if got := r.Form.Get("safesearch"); got != "2" {
			t.Errorf("safesearch = %q, want 2", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"template":"images.html","title":"needle","img_src":"https://img.example/a.jpg","thumbnail_src":"https://thumb.example/a.jpg","engine":"bing images","score":2,"positions":[1]}]}`))
	}))
	t.Cleanup(server.Close)

	client := testClient(server.URL, server.Client())
	client.safeSearch = 2
	response, err := client.Search(context.Background(), search.SearchRequest{
		Query: "private needle", Categories: []search.Category{search.CatImage}, Page: 2, Language: "ru", Pool: search.PoolCore,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Score != 2 || response.Results[0].Positions[0] != 1 {
		t.Fatalf("metadata missing from response: %+v", response)
	}
}

func TestSearchSelectsDiscoveryPoolAndOperatorLanguage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.Form.Get("engines"); got != "findthatmeme,pinterest" {
			t.Errorf("engines = %q", got)
		}
		if got := r.Form.Get("language"); got != "all" {
			t.Errorf("operator language = %q, want all", got)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(server.Close)
	client := testClient(server.URL, server.Client())
	client.language = "all"
	_, err := client.Search(context.Background(), search.SearchRequest{
		Query: "needle", Categories: []search.Category{search.CatImage}, Language: "uk", Pool: search.PoolDiscovery,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSearchRejectsMissingPinnedEngines(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(server.Close)
	client := New(Options{BaseURL: server.URL, HTTP: server.Client()})
	_, err := client.Search(context.Background(), search.SearchRequest{
		Query: "redacted", Categories: []search.Category{search.CatVideo}, Pool: search.PoolDiscovery,
	})
	if err == nil {
		t.Fatal("expected missing pinned-engine error")
	}
	if called {
		t.Fatal("request was issued without pinned engines")
	}
}

func TestSearchMarksDegradedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[["google images","timeout"]]}`))
	}))
	t.Cleanup(server.Close)
	response, err := testClient(server.URL, server.Client()).Search(context.Background(), search.SearchRequest{
		Query: "redacted", Categories: []search.Category{search.CatImage}, Pool: search.PoolCore,
	})
	if err != nil || !response.Degraded {
		t.Fatalf("response=%+v err=%v, want degraded", response, err)
	}
}

func TestLogsNeverContainQuery(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		transport http.RoundTripper
	}{
		{name: "success", status: 200, body: `{"results":[]}`},
		{name: "degraded", status: 200, body: `{"results":[],"unresponsive_engines":[["bing images","timeout"]]}`},
		{name: "decode", status: 200, body: `{`},
		{name: "forbidden", status: 403, body: `forbidden`},
		{name: "timeout", transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("timeout")
		})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			var clientHTTP *http.Client
			baseURL := "https://searx.invalid"
			if tc.transport != nil {
				clientHTTP = &http.Client{Transport: tc.transport}
			} else {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				t.Cleanup(server.Close)
				baseURL, clientHTTP = server.URL, server.Client()
			}
			client := testClient(baseURL, clientHTTP)
			client.log = logger
			_, _ = client.Search(context.Background(), search.SearchRequest{
				Query: "private-search-needle", Categories: []search.Category{search.CatImage}, Language: "ru", Pool: search.PoolCore,
			})
			if strings.Contains(logs.String(), "private-search-needle") {
				t.Fatalf("query leaked into logs: %s", logs.String())
			}
		})
	}
}

func TestValidURL(t *testing.T) {
	good := []string{
		"https://example.com/a.jpg",
		"https://i.ytimg.com/vi/abc/hqdefault.jpg",
		"https://p16.tiktokcdn.com/cover.jpg?x=1",
	}
	bad := []string{
		"", "http://example.com/a.jpg", "https://localhost/a.jpg",
		"https://example.com/a b.jpg", "https://example.com/\na.jpg",
		"ftp://example.com/a.jpg", "https://", "data:image/png;base64,iVBOR",
		"https://user@example.com/a.jpg",
	}
	for _, value := range good {
		if !validURL(value) {
			t.Errorf("validURL(%q) = false", value)
		}
	}
	for _, value := range bad {
		if validURL(value) {
			t.Errorf("validURL(%q) = true", value)
		}
	}
}

func TestNormalizeImagePreservesRankingMetadataAndDimensions(t *testing.T) {
	raw := &rawResult{
		Template: "images.html", Engines: []string{"bing images", "duckduckgo images"}, Title: "A cat",
		ImgSrc: "https://origin.example.com/cat.jpg", ThumbnailSrc: "https://thumb.example.com/cat.jpg",
		Source: "example.com", URL: "https://example.com/page", Score: 3.5, Positions: []int{1, 2},
		Resolution: "1920 x 1080",
	}
	result, ok := normalize(raw, search.PoolCore, 7)
	if !ok {
		t.Fatal("expected image to normalize")
	}
	if result.MediaURL != raw.ThumbnailSrc || result.Width != 1920 || result.Height != 1080 {
		t.Fatalf("unexpected normalized image: %+v", result)
	}
	if result.Score != 3.5 || len(result.Engines) != 2 || result.SourceOrder != 7 || result.Pool != search.PoolCore {
		t.Fatalf("ranking metadata lost: %+v", result)
	}
}

func TestNormalizeVideoUsesValidCandidatesAndBilibiliUpgrade(t *testing.T) {
	raw := &rawResult{
		Template: "videos.html", Title: "Clip",
		Thumbnail: "http://invalid.example/cover.jpg", ThumbnailSrc: "//i0.hdslb.com/cover.jpg",
		URL: "http://www.bilibili.com/video/BV1abc", IframeSrc: "https://player.bilibili.com/player.html?bvid=BV1abc",
		Length: json.RawMessage(`"3:21"`), Engine: "bilibili",
	}
	result, ok := normalize(raw, search.PoolDiscovery, 0)
	if !ok {
		t.Fatal("expected Bilibili result to normalize")
	}
	if result.ThumbURL != "https://i0.hdslb.com/cover.jpg" {
		t.Errorf("thumb = %q", result.ThumbURL)
	}
	if result.PageURL != "https://www.bilibili.com/video/BV1abc" {
		t.Errorf("page = %q", result.PageURL)
	}
	if result.DurationSec != 201 {
		t.Errorf("duration = %d", result.DurationSec)
	}

	raw.URL = "http://unknown.example/video"
	result, ok = normalize(raw, search.PoolDiscovery, 0)
	if !ok || result.PageURL != raw.IframeSrc {
		t.Fatalf("expected second valid page candidate, got %+v", result)
	}
}

func TestNormalizeRejectsUnsafeMedia(t *testing.T) {
	image := &rawResult{Template: "images.html", ImgSrc: "http://unknown.example/a.jpg", ThumbnailSrc: "http://unknown.example/t.jpg"}
	if _, ok := normalize(image, search.PoolCore, 0); ok {
		t.Fatal("expected unsafe image to be dropped")
	}
	video := &rawResult{Template: "videos.html", Thumbnail: "https://thumb.example/a.jpg", URL: "http://unknown.example/v"}
	if _, ok := normalize(video, search.PoolCore, 0); ok {
		t.Fatal("expected video without a valid page URL to be dropped")
	}
}

func TestParseDurationAndDimensions(t *testing.T) {
	durations := map[string]int{`"3:21"`: 201, `"1:02:33"`: 3753, `90`: 90, `"bogus"`: 0}
	for input, want := range durations {
		if got := parseDuration(json.RawMessage(input)); got != want {
			t.Errorf("parseDuration(%s) = %d, want %d", input, got, want)
		}
	}
	width, height := dimensions(json.RawMessage(`"1280"`), json.RawMessage(`720`), "")
	if width != 1280 || height != 720 {
		t.Fatalf("dimensions = %dx%d", width, height)
	}
}

func testClient(baseURL string, client *http.Client) *Client {
	return New(Options{
		BaseURL: baseURL, HTTP: client,
		EnginesImages: "bing images,duckduckgo images", EnginesVideos: "youtube,duckduckgo videos",
		EnginesImagesDiscovery: "findthatmeme,pinterest", EnginesVideosDiscovery: "bilibili,sepiasearch",
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
