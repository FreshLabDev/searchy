package native_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"searchy/internal/search"
	"searchy/internal/search/native"
	"searchy/internal/search/stream"
)

type integrationTransport func(*http.Request) (*http.Response, error)

func (f integrationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type integrationBackend func(context.Context, search.PageRequest) (search.Page, error)

func (f integrationBackend) SearchPage(ctx context.Context, r search.PageRequest) (search.Page, error) {
	return f(ctx, r)
}

type gatedNativeVideo struct {
	release <-chan struct{}
	results []search.MediaResult
}

func (g gatedNativeVideo) Name() string              { return "gated native video" }
func (g gatedNativeVideo) Category() search.Category { return search.CatVideo }
func (g gatedNativeVideo) SearchSource(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
	select {
	case <-g.release:
		return search.SourcePage{Results: g.results}, nil
	case <-ctx.Done():
		return search.SourcePage{}, ctx.Err()
	}
}

func TestNativeRankStreamDoesNotRepeatLegacyYouTubeOnLaterPage(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%v", wrapped), func(t *testing.T) {
			const legacyPage = "https://youtu.be/aBc_dEf-G12?si=token"
			const nativePage = "https://www.youtube.com/shorts/aBc_dEf-G12?feature=share"
			const preview = "https://i.ytimg.com/vi/aBc_dEf-G12/hqdefault.jpg"
			client := &http.Client{Transport: integrationTransport(func(r *http.Request) (*http.Response, error) {
				body := fmt.Sprintf(`<div vrhm='{"murl":%q,"turl":%q,"vt":"public fixture"}'></div>`, nativePage, preview)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			var parsed search.SourcePage
			for _, source := range native.NewSources(native.Options{HTTP: client}) {
				if source.Name() != "Bing videos" {
					continue
				}
				var err error
				parsed, err = source.SearchSource(context.Background(), search.SourceRequest{Query: "public fixture", Language: "en"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(parsed.Results) != 1 {
				t.Fatalf("native fixture=%+v", parsed)
			}
			legacy := search.MediaResult{Category: search.CatVideo, ID: "raw-legacy-sha1-id", Title: "public fixture", PageURL: legacyPage, ThumbURL: preview, Engine: "youtube", Engines: []string{"youtube"}}
			var backend search.Backend = integrationBackend(func(_ context.Context, r search.PageRequest) (search.Page, error) {
				if r.UserID != 0 || r.Scope != "" {
					t.Error("fallback received owner identity")
				}
				return search.Page{Results: []search.MediaResult{legacy}}, nil
			})
			if wrapped {
				backend = native.NewFallback(backend)
			}
			release := make(chan struct{})
			engine := stream.New(stream.Options{
				Sources:  []search.Source{gatedNativeVideo{release: release, results: parsed.Results}},
				Fallback: backend, Rank: native.Rank, FirstPageWait: 10 * time.Millisecond,
				NativeTimeout: time.Second, Timeout: 2 * time.Second,
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			defer engine.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request := search.PageRequest{Query: "public fixture", Categories: []search.Category{search.CatVideo}, Language: "en", Limit: 1, UserID: 42, Scope: "inline"}
			first, err := engine.SearchPage(ctx, request)
			if err != nil || len(first.Results) != 1 || first.NextOffset == "" {
				t.Fatalf("first page=%+v err=%v", first, err)
			}
			if first.Results[0].ID != parsed.Results[0].ID || first.Results[0].PageURL != legacyPage {
				t.Fatalf("fallback identity or original delivery URL changed: %+v", first.Results[0])
			}
			close(release)
			request.Offset = first.NextOffset
			second, err := engine.SearchPage(ctx, request)
			if err != nil || len(second.Results) != 0 || second.NextOffset != "" {
				t.Fatalf("late native video repeated a delivered legacy clip: %+v err=%v", second, err)
			}
			if parsed.Results[0].PageURL != nativePage || legacy.ID != "raw-legacy-sha1-id" {
				t.Fatal("normalization mutated provider-owned data")
			}
		})
	}
}
