package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"searchy/internal/i18n"
	"searchy/internal/search"
)

type backendFunc func(context.Context, search.PageRequest) (search.Page, error)

func (f backendFunc) SearchPage(ctx context.Context, req search.PageRequest) (search.Page, error) {
	return f(ctx, req)
}

type telegramCall struct {
	method string
	fields map[string]string
}

func newSearchTestHandlers(t *testing.T, o Options) (*Handlers, <-chan telegramCall) {
	t.Helper()
	calls := make(chan telegramCall, 32)
	api := telegramTestServer(t, func(method string, fields map[string]string) {
		calls <- telegramCall{method: method, fields: fields}
	})
	t.Cleanup(api.Close)
	o.Telegram = newTestClient(t, api.URL)
	o.HTTPClient = http.DefaultClient
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.MaxResults == 0 {
		o.MaxResults = 50
	}
	o.InlineCacheTime = 300
	o.RequestTimeout = 2 * time.Second
	return NewHandlers(o), calls
}

func receiveTelegramCall(t *testing.T, calls <-chan telegramCall, method string) map[string]string {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case call := <-calls:
			if call.method == method {
				return call.fields
			}
		case <-deadline.C:
			t.Fatalf("no Telegram %s call", method)
			return nil
		}
	}
}

func inlineResults(t *testing.T, fields map[string]string) []map[string]any {
	t.Helper()
	var results []map[string]any
	if err := json.Unmarshal([]byte(fields["results"]), &results); err != nil {
		t.Fatalf("invalid inline results: %v", err)
	}
	return results
}

func TestInlineUsesOpaqueBackendCursorAndOwner(t *testing.T) {
	for _, offset := range []string{"", "opaque:incoming/09=="} {
		t.Run("offset="+offset, func(t *testing.T) {
			const next = "opaque:next/+?=="
			var got search.PageRequest
			result := search.MediaResult{
				ID: "video-1", Category: search.CatVideo, Engine: "native-video",
				Title: "Cats <live>", ThumbURL: "https://media.example/cover.jpg",
				PageURL: "https://video.example/watch/1", Author: "A & B", DurationSec: 65,
			}
			h, calls := newSearchTestHandlers(t, Options{Backend: backendFunc(func(ctx context.Context, req search.PageRequest) (search.Page, error) {
				got = req
				if _, ok := ctx.Deadline(); !ok {
					t.Error("backend request has no deadline")
				}
				return search.Page{Results: []search.MediaResult{result}, NextOffset: next}, nil
			})})
			h.Route(context.Background(), &tg.Update{InlineQuery: &tg.InlineQuery{
				ID: "inline-1", Query: " V: literal  i: middle ", Offset: offset,
				From: tg.User{ID: 42, LanguageCode: "zh-CN"},
			}})
			if got.Query != "literal  i: middle" || !slices.Equal(got.Categories, []search.Category{search.CatVideo}) ||
				got.Language != "zh-CN" || got.UserID != 42 || got.Scope != "inline" ||
				got.Offset != offset || got.Limit != 24 || got.WaitAll {
				t.Fatalf("backend request = %+v", got)
			}
			fields := receiveTelegramCall(t, calls, "answerInlineQuery")
			if fields["next_offset"] != next || fields["is_personal"] != "true" || fields["cache_time"] != "300" {
				t.Fatalf("inline pagination/cache = %+v", fields)
			}
			rs := inlineResults(t, fields)
			if len(rs) != 1 || rs[0]["type"] != "photo" || rs[0]["photo_url"] != result.ThumbURL || rs[0]["caption"] != videoCaption(result) {
				t.Fatalf("video cover card = %+v", rs)
			}
			if meta, ok := h.resultMeta.Get(result.ID); !ok || meta.rtype != "video" || meta.engine != result.Engine {
				t.Fatalf("result analytics metadata = %+v, %v", meta, ok)
			}
		})
	}
}

func TestInlineMixedSearchShortCachesDegradedPage(t *testing.T) {
	for _, degraded := range []bool{false, true} {
		t.Run(fmt.Sprint(degraded), func(t *testing.T) {
			h, calls := newSearchTestHandlers(t, Options{MaxResults: 7, Backend: backendFunc(func(_ context.Context, req search.PageRequest) (search.Page, error) {
				if req.Query != "cats" || !slices.Equal(req.Categories, []search.Category{search.CatImage, search.CatVideo}) || req.Limit != 7 {
					t.Errorf("mixed inline request = %+v", req)
				}
				return search.Page{Results: []search.MediaResult{{
					ID: "image", Category: search.CatImage, Engine: "native-image",
					MediaURL: "https://media.example/image.jpg", ThumbURL: "https://media.example/thumb.jpg",
				}}, Degraded: degraded}, nil
			})})
			h.onInline(context.Background(), &tg.InlineQuery{ID: "mixed", Query: "cats", From: tg.User{ID: 7}})
			fields := receiveTelegramCall(t, calls, "answerInlineQuery")
			cacheTime := "300"
			if degraded {
				cacheTime = "1"
			}
			if fields["cache_time"] != cacheTime || fields["is_personal"] != "true" {
				t.Fatalf("cache fields = %+v", fields)
			}
			rs := inlineResults(t, fields)
			if len(rs) != 1 || rs[0]["type"] != "photo" || rs[0]["photo_url"] != "https://media.example/image.jpg" {
				t.Fatalf("image result = %+v", rs)
			}
			if _, ok := rs[0]["caption"]; ok {
				t.Fatal("image gained a caption")
			}
		})
	}
}

func TestInlineBackendErrorsAreLocalizedAndRetryable(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		key  string
	}{
		{"expired", fmt.Errorf("cursor: %w", search.ErrExpiredCursor), "search.expired"},
		{"invalid", search.ErrInvalidCursor, "search.invalid"},
		{"busy", search.ErrBusy, "search.busy"},
		{"deadline", context.DeadlineExceeded, "load.failed"},
		{"failed", errors.New("sensitive query and cursor"), "load.failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			var requests int
			h, calls := newSearchTestHandlers(t, Options{
				Logger: slog.New(slog.NewTextHandler(&logs, nil)),
				Backend: backendFunc(func(_ context.Context, req search.PageRequest) (search.Page, error) {
					requests++
					if req.Offset != "sensitive-cursor" {
						t.Errorf("offset was rewritten: %q", req.Offset)
					}
					return search.Page{}, tt.err
				}),
			})
			h.onInline(context.Background(), &tg.InlineQuery{
				ID: tt.name, Query: "sensitive-query", Offset: "sensitive-cursor", From: tg.User{ID: 42, LanguageCode: "ru"},
			})
			fields := receiveTelegramCall(t, calls, "answerInlineQuery")
			if requests != 1 || fields["next_offset"] != "" || fields["cache_time"] != "1" || fields["is_personal"] != "true" {
				t.Fatalf("error request/answer = %d / %+v", requests, fields)
			}
			rs := inlineResults(t, fields)
			if len(rs) != 1 || rs[0]["type"] != "article" || rs[0]["description"] != i18n.T("ru", tt.key) {
				t.Fatalf("error result = %+v", rs)
			}
			if strings.Contains(fields["results"], "sensitive") || strings.Contains(logs.String(), "sensitive") {
				t.Fatal("error response/log leaked query, cursor, or backend error")
			}
		})
	}
}

func TestInlineSupersededRequestsNeverAnswer(t *testing.T) {
	for _, newer := range []string{"new query", "", "I:"} {
		t.Run("new="+newer, func(t *testing.T) {
			started := make(chan struct{})
			finished := make(chan struct{})
			h, calls := newSearchTestHandlers(t, Options{Backend: backendFunc(func(ctx context.Context, req search.PageRequest) (search.Page, error) {
				if req.Query == "old query" {
					close(started)
					<-ctx.Done()
					// A backend can finish with partial data just as cancellation wins.
					return search.Page{Results: []search.MediaResult{{ID: "obsolete", Category: search.CatImage}}}, nil
				}
				return search.Page{}, nil
			})})
			go func() {
				defer close(finished)
				h.onInline(context.Background(), &tg.InlineQuery{ID: "old", Query: "old query", From: tg.User{ID: 42}})
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("old search never started")
			}
			h.onInline(context.Background(), &tg.InlineQuery{ID: "new", Query: newer, From: tg.User{ID: 42}})
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("obsolete search was not cancelled")
			}
			fields := receiveTelegramCall(t, calls, "answerInlineQuery")
			if fields["inline_query_id"] != "new" {
				t.Fatalf("answered obsolete request: %+v", fields)
			}
			select {
			case call := <-calls:
				t.Fatalf("unexpected extra answer: %+v", call)
			default:
			}
		})
	}
}

func TestInlineCancelledBackendDoesNotAnswer(t *testing.T) {
	h, calls := newSearchTestHandlers(t, Options{Backend: backendFunc(func(context.Context, search.PageRequest) (search.Page, error) {
		return search.Page{}, context.Canceled
	})})
	h.onInline(context.Background(), &tg.InlineQuery{ID: "cancelled", Query: "cats", From: tg.User{ID: 42}})
	select {
	case call := <-calls:
		t.Fatalf("cancelled search answered: %+v", call)
	default:
	}
}

type legacyProviderFunc func(context.Context, search.SearchRequest) (search.SearchResponse, error)

func (f legacyProviderFunc) Search(ctx context.Context, req search.SearchRequest) (search.SearchResponse, error) {
	return f(ctx, req)
}

func TestLegacyAggregatorCompatibilityAndPageLimit(t *testing.T) {
	var count atomic.Int32
	aggregator := search.NewAggregator(legacyProviderFunc(func(_ context.Context, req search.SearchRequest) (search.SearchResponse, error) {
		count.Add(1)
		if req.Page != 8 && req.Page != 9 {
			t.Errorf("unexpected legacy page %d", req.Page)
		}
		return search.SearchResponse{Results: []search.MediaResult{{
			ID: "legacy", Category: search.CatImage, Title: "cats",
			MediaURL: "https://media.example/cats.jpg", ThumbURL: "https://media.example/thumb.jpg", Engine: "legacy",
		}}, HasMore: true}, nil
	}), search.AggregatorOptions{
		CacheSize: 10, CacheTTL: time.Minute, MaxResults: 50, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h, calls := newSearchTestHandlers(t, Options{Aggregator: aggregator})
	for _, offset := range []string{"8", "9", "10"} {
		h.onInline(context.Background(), &tg.InlineQuery{ID: offset, Query: "i:cats", Offset: offset, From: tg.User{ID: 42}})
		fields := receiveTelegramCall(t, calls, "answerInlineQuery")
		wantNext := ""
		if offset == "8" {
			wantNext = "9"
		}
		if fields["next_offset"] != wantNext {
			t.Fatalf("legacy page %s next offset = %q, want %q", offset, fields["next_offset"], wantNext)
		}
		rs := inlineResults(t, fields)
		if offset == "10" && (len(rs) != 1 || rs[0]["description"] != i18n.T("en", "search.invalid")) {
			t.Fatalf("legacy page limit did not return helpful error: %+v", rs)
		}
	}
	if count.Load() != 4 { // two relevance pools for each of the two valid pages
		t.Fatalf("legacy provider called %d times, want 4", count.Load())
	}
}

func TestGridBackendWaitsForPoolAndPaginatesLocally(t *testing.T) {
	jpegData := testJPEG(t)
	cover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegData)
	}))
	defer cover.Close()
	var got search.PageRequest
	var requests int
	results := make([]search.MediaResult, 11)
	for i := range results {
		results[i] = search.MediaResult{ID: fmt.Sprint(i), Category: search.CatImage, MediaURL: cover.URL + "/image.jpg"}
	}
	h, calls := newSearchTestHandlers(t, Options{Backend: backendFunc(func(_ context.Context, req search.PageRequest) (search.Page, error) {
		requests++
		got = req
		return search.Page{Results: results, NextOffset: "unused-stream-cursor", Degraded: true}, nil
	})})
	h.Route(context.Background(), &tg.Update{Message: &tg.Message{
		Text: "/search I: cats  and  dogs", MessageThreadID: 7,
		Chat: tg.Chat{ID: -42, Type: tg.ChatSupergroup}, From: &tg.User{ID: 123, LanguageCode: "pl"},
	}})
	if got.Query != "cats  and  dogs" || !slices.Equal(got.Categories, []search.Category{search.CatImage}) ||
		got.Language != "pl" || got.UserID != 123 || got.Scope != "grid:-42:7" ||
		got.Limit != 50 || !got.WaitAll || got.Offset != "" {
		t.Fatalf("grid request = %+v", got)
	}
	fields := receiveTelegramCall(t, calls, "sendPhoto")
	if fields["message_thread_id"] != "7" {
		t.Fatalf("grid lost topic: %+v", fields)
	}
	var keyboard tg.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(fields["reply_markup"]), &keyboard); err != nil {
		t.Fatal(err)
	}
	tok, _, _, ok := parseGridCB(keyboard.InlineKeyboard[0][0].CallbackData)
	if !ok {
		t.Fatal("missing local grid callback")
	}
	sess, ok := h.grids.Get(tok)
	if !ok || len(sess.results) != 11 || sess.lang != "pl" {
		t.Fatalf("grid session = %+v, %v", sess, ok)
	}
	h.onGridCallback(context.Background(), &tg.CallbackQuery{
		ID: "page", From: tg.User{ID: 456},
		Message: tg.Message{MessageID: 10, MessageThreadID: 7, Chat: tg.Chat{ID: -42, Type: tg.ChatSupergroup}},
	}, tok, "p", 1)
	receiveTelegramCall(t, calls, "editMessageMedia")
	if requests != 1 {
		t.Fatalf("local grid paging repeated backend search %d times", requests)
	}
}

func TestGridBackendErrorsOfferLocalizedRetry(t *testing.T) {
	for _, tt := range []struct {
		err error
		key string
	}{
		{search.ErrBusy, "search.busy"},
		{search.ErrExpiredCursor, "search.expired"},
		{search.ErrInvalidCursor, "search.invalid"},
		{context.DeadlineExceeded, "load.failed"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			h, calls := newSearchTestHandlers(t, Options{Backend: backendFunc(func(_ context.Context, req search.PageRequest) (search.Page, error) {
				if !req.WaitAll || req.Scope != "grid:42:0" || req.UserID != 42 {
					t.Errorf("grid request = %+v", req)
				}
				return search.Page{}, tt.err
			})})
			h.runGridSearch(context.Background(), 42, 0, &tg.User{ID: 42, LanguageCode: "ru"}, "v:cats", "dm")
			fields := receiveTelegramCall(t, calls, "sendMessage")
			if fields["text"] != i18n.T("ru", tt.key) || !strings.Contains(fields["reply_markup"], "switch_inline_query_current_chat") {
				t.Fatalf("grid retry = %+v", fields)
			}
			if h.grids.Len() != 0 {
				t.Fatal("failed search created a grid session")
			}
		})
	}
}
