package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"searchy/internal/search"
	"searchy/internal/search/searxng"
)

type fallbackBackendFunc func(context.Context, search.PageRequest) (search.Page, error)

func (f fallbackBackendFunc) SearchPage(ctx context.Context, r search.PageRequest) (search.Page, error) {
	return f(ctx, r)
}

func legacyFixture(body string) search.Backend {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return fixtureResponse(body), nil })}
	provider := searxng.New(searxng.Options{HTTP: client, BaseURL: "https://searx.example.com", EnginesImages: "flickr,bing images", EnginesVideos: "youtube", Logger: logger})
	return search.LegacyBackend{Aggregator: search.NewAggregator(provider, search.AggregatorOptions{CacheSize: 10, CacheTTL: time.Minute, MaxResults: 50, Timeout: time.Second, Logger: logger})}
}

func TestFallbackNormalizesRealLegacyFlickrGeometry(t *testing.T) {
	const original = "https://images.example.com/original.jpg"
	const preview = "https://images.example.com/preview.jpg"
	for _, thumb := range []string{preview, "https://images.example.com/preview.png", original, ""} {
		t.Run(thumb, func(t *testing.T) {
			body := fmt.Sprintf(`{"results":[{"template":"images.html","url":"https://example.com/photo","img_src":%q,"thumbnail_src":%q,"title":"public fixture","engine":"flickr","engines":["flickr"],"positions":[1],"width":8000,"height":8000}]}`, original, thumb)
			legacy := legacyFixture(body)
			request := search.PageRequest{Query: "public fixture", Categories: []search.Category{search.CatImage}, Language: "en", Limit: 24}
			raw, err := legacy.SearchPage(context.Background(), request)
			if err != nil || len(raw.Results) != 1 || raw.Results[0].MediaURL != original || raw.Results[0].Width != 8000 {
				t.Fatalf("legacy fixture=%+v err=%v", raw, err)
			}
			page, err := NewFallback(legacy).SearchPage(context.Background(), request)
			if err != nil || page.Degraded != raw.Degraded || page.NextOffset != raw.NextOffset {
				t.Fatalf("page=%+v err=%v", page, err)
			}
			if thumb != preview {
				if len(page.Results) != 0 {
					t.Fatalf("incompatible legacy original survived: %+v", page.Results)
				}
				return
			}
			if len(page.Results) != 1 {
				t.Fatal(page)
			}
			r := page.Results[0]
			if r.MediaURL != preview || r.ThumbURL != preview || r.Width != 0 || r.Height != 0 || r.ID != resultID(search.CatImage, original) || r.PageURL != raw.Results[0].PageURL {
				t.Fatalf("fallback bypassed native normalization: %+v", r)
			}
			if raw.Results[0].MediaURL != original || raw.Results[0].Width != 8000 {
				t.Fatal("normalization mutated legacy cache")
			}
		})
	}
}

func TestFallbackAcceptsAlreadySelectedBingPreviewWithoutOriginalGeometry(t *testing.T) {
	const preview = "https://tse1.mm.bing.net/th?id=OIP.fixture&pid=Api"
	for _, size := range []int{0, 8000} {
		body := fmt.Sprintf(`{"results":[{"template":"images.html","url":"https://example.com/photo","img_src":"https://images.example.com/original.jpg","thumbnail_src":%q,"title":"public fixture","engine":"bing images","width":%d,"height":%d}]}`, preview, size, size)
		legacy := legacyFixture(body)
		request := search.PageRequest{Query: "public fixture", Categories: []search.Category{search.CatImage}, Language: "en"}
		raw, err := legacy.SearchPage(context.Background(), request)
		if err != nil || len(raw.Results) != 1 || raw.Results[0].MediaURL != preview || raw.Results[0].ThumbURL != preview {
			t.Fatalf("legacy Bing fixture=%+v err=%v", raw, err)
		}
		page, err := NewFallback(legacy).SearchPage(context.Background(), request)
		if err != nil || page.Degraded != raw.Degraded || len(page.Results) != 1 {
			t.Fatalf("known preview rejected: %+v %v", page, err)
		}
		if r := page.Results[0]; r.MediaURL != preview || r.Width != 0 || r.Height != 0 || r.ID != resultID(search.CatImage, preview) {
			t.Fatalf("preview kept original geometry: %+v", r)
		}
	}
}

func TestFallbackPreservesMetadataAndOwnsResultSlices(t *testing.T) {
	image := sample("fixture", "images.example.com")
	image.ID, image.Author, image.Caption = "legacy-image-id", "original author", "<b>original caption</b>"
	image.Engines, image.Positions = []string{"flickr", "bing images"}, []int{1, 3}
	image.Score, image.RankScore, image.Pool = 2.5, 0.7, search.PoolDiscovery
	image.SourceOrder = 5
	video := search.MediaResult{Category: search.CatVideo, ID: "legacy-video-id", Title: "public fixture", Author: "uploader", MediaURL: "https://www.youtube.com/embed/abcdefghijk", ThumbURL: "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg", PageURL: "https://youtu.be/abcdefghijk?si=token", Caption: "<i>video caption</i>", MIMEType: "text/html", DurationSec: 93, Engines: []string{"youtube"}, Positions: []int{2}, Engine: "youtube", Pool: search.PoolCore}
	input := []search.MediaResult{image, video}
	request := search.PageRequest{Query: "public fixture", Categories: []search.Category{search.CatImage, search.CatVideo}, Language: "fr-CA", Offset: "provider-cursor", UserID: 42, Scope: "inline", Limit: 2, WaitAll: true}
	backend := fallbackBackendFunc(func(_ context.Context, got search.PageRequest) (search.Page, error) {
		want := request
		want.UserID, want.Scope = 0, ""
		if !reflect.DeepEqual(got, want) {
			t.Errorf("anonymous fallback request changed constraints: %+v", got)
		}
		got.Categories[0] = search.CatVideo
		return search.Page{Results: input, NextOffset: "exact-next", Degraded: true}, nil
	})
	page, err := NewFallback(backend).SearchPage(context.Background(), request)
	if err != nil || len(page.Results) != 2 || !page.Degraded || page.NextOffset != "exact-next" || request.Categories[0] != search.CatImage {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	wantImage, wantVideo := image, video
	wantImage.ID = resultID(search.CatImage, image.MediaURL)
	wantVideo.ID, wantVideo.MediaURL = resultID(search.CatVideo, video.PageURL), video.ThumbURL
	if !reflect.DeepEqual(page.Results, []search.MediaResult{wantImage, wantVideo}) {
		t.Fatalf("fallback metadata changed: %+v", page.Results)
	}
	page.Results[0].Title = "caller mutation"
	page.Results[0].Engines[0] = "caller mutation"
	page.Results[0].Positions[0] = 99
	if input[0].Title != image.Title || input[0].Engines[0] != "flickr" || input[0].Positions[0] != 1 {
		t.Fatal("caller mutated backend-owned results")
	}
	input[1].Engines[0], input[1].Positions[0] = "backend mutation", 99
	if page.Results[1].Engines[0] != "youtube" || page.Results[1].Positions[0] != 2 {
		t.Fatal("backend mutated returned metadata")
	}
}

func TestFallbackNilAndSanitizedErrors(t *testing.T) {
	if NewFallback(nil) != nil {
		t.Fatal("nil backend was wrapped")
	}
	for _, expected := range []error{context.Canceled, context.DeadlineExceeded, search.ErrInvalidCursor, search.ErrExpiredCursor, search.ErrBusy, search.ErrUnavailable} {
		backend := fallbackBackendFunc(func(context.Context, search.PageRequest) (search.Page, error) {
			return search.Page{}, fmt.Errorf("private-query URL: %w", expected)
		})
		page, err := NewFallback(backend).SearchPage(context.Background(), search.PageRequest{})
		if err != expected || strings.Contains(err.Error(), "private-query") || !page.Degraded {
			t.Fatalf("error=%v page=%+v", err, page)
		}
	}
	backend := fallbackBackendFunc(func(context.Context, search.PageRequest) (search.Page, error) {
		return search.Page{}, errors.New("https://upstream.example.com/?q=private-query")
	})
	if _, err := NewFallback(backend).SearchPage(context.Background(), search.PageRequest{}); err != search.ErrUnavailable {
		t.Fatalf("unsanitized error=%v", err)
	}
}

func TestFallbackCancellationPreservesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend := fallbackBackendFunc(func(context.Context, search.PageRequest) (search.Page, error) {
		t.Fatal("cancelled request reached fallback")
		return search.Page{}, nil
	})
	if _, err := NewFallback(backend).SearchPage(ctx, search.PageRequest{}); err != context.Canceled {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	backend = fallbackBackendFunc(func(got context.Context, _ search.PageRequest) (search.Page, error) {
		if got != ctx {
			t.Error("caller context was detached")
		}
		cancel()
		return search.Page{Results: []search.MediaResult{sample("fixture", "images.example.com")}}, nil
	})
	page, err := NewFallback(backend).SearchPage(ctx, search.PageRequest{})
	if err != context.Canceled || len(page.Results) != 0 || !page.Degraded {
		t.Fatalf("cancelled response=%+v err=%v", page, err)
	}
}

func TestFallbackDistinguishesMalformedResultsFromFormatFiltering(t *testing.T) {
	for _, tc := range []struct {
		result   search.MediaResult
		degraded bool
	}{
		{search.MediaResult{Category: search.CatImage, MediaURL: "https://images.example.com/a.svg", ThumbURL: "https://images.example.com/a.png", PageURL: "https://example.com/photo"}, false},
		{search.MediaResult{Category: search.CatVideo, MediaURL: "https://example.com/embed/clip", ThumbURL: "https://images.example.com/a.png", PageURL: "https://example.com/watch"}, false},
		{search.MediaResult{Category: search.CatImage, MediaURL: "https://images.example.com/a.jpg", ThumbURL: "https://images.example.com/a.jpg", PageURL: "https://example.com/photo", Width: 8000, Height: 8000}, false},
		{search.MediaResult{Category: search.CatImage, MediaURL: "https://127.0.0.1/a.jpg", PageURL: "https://example.com/photo"}, true},
		{search.MediaResult{Category: search.CatImage, MediaURL: "https://images.example.com/a.jpg", PageURL: "http://example.com/photo"}, true},
		{search.MediaResult{Category: "unknown", MediaURL: "https://images.example.com/a.jpg", PageURL: "https://example.com/photo"}, true},
	} {
		backend := fallbackBackendFunc(func(context.Context, search.PageRequest) (search.Page, error) {
			return search.Page{Results: []search.MediaResult{tc.result}}, nil
		})
		page, err := NewFallback(backend).SearchPage(context.Background(), search.PageRequest{})
		if err != nil || len(page.Results) != 0 || page.Degraded != tc.degraded {
			t.Fatalf("page=%+v err=%v expected degraded=%v", page, err, tc.degraded)
		}
	}
}
