package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"searchy/internal/search"
	"searchy/internal/search/stream"
)

func TestInlineEmptyQueryRevokesLeaseBeforePrompt(t *testing.T) {
	for _, query := range []string{"", " \t\n", "i:", "V:I:"} {
		t.Run(fmt.Sprintf("query=%q", query), func(t *testing.T) {
			var logs bytes.Buffer
			var got []search.PageRequest
			var calls <-chan telegramCall
			h, calls := newSearchTestHandlers(t, Options{
				Logger: slog.New(slog.NewTextHandler(&logs, nil)),
				Backend: backendFunc(func(ctx context.Context, req search.PageRequest) (search.Page, error) {
					if ctx.Err() != nil {
						t.Error("lease revocation used a canceled context")
					}
					select {
					case call := <-calls:
						t.Errorf("prompt sent before lease revocation: %+v", call)
					default:
					}
					got = append(got, req)
					return search.Page{}, errors.New("revocation detail must not be logged")
				}),
			})
			h.onInline(context.Background(), &tg.InlineQuery{
				ID: "empty", Query: query, Offset: "old-cursor", From: tg.User{ID: 42, LanguageCode: "ru"},
			})
			want := []search.PageRequest{{UserID: 42, Scope: "inline"}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("lease revocation request = %+v, want %+v", got, want)
			}
			fields := receiveTelegramCall(t, calls, "answerInlineQuery")
			assertInlinePrompt(t, fields, "empty")
			if logs.Len() != 0 {
				t.Fatalf("empty query produced logs: %s", &logs)
			}
			if h.resultMeta.Len() != 0 || h.grids.Len() != 0 {
				t.Fatal("empty query retained result metadata or a grid session")
			}
		})
	}
}

func TestInlineEmptyQueryHonorsCancellation(t *testing.T) {
	for _, cancelBefore := range []bool{false, true} {
		t.Run(fmt.Sprintf("beforeBackend=%t", cancelBefore), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelBefore {
				cancel()
			}
			var requests int
			h, calls := newSearchTestHandlers(t, Options{Backend: backendFunc(func(context.Context, search.PageRequest) (search.Page, error) {
				requests++
				cancel()
				return search.Page{}, nil
			})})
			h.onInline(ctx, &tg.InlineQuery{ID: "canceled", Query: "V:I:", From: tg.User{ID: 42}})
			wantRequests := 1
			if cancelBefore {
				wantRequests = 0
			}
			if requests != wantRequests {
				t.Fatalf("backend called %d times, want %d", requests, wantRequests)
			}
			select {
			case call := <-calls:
				t.Fatalf("canceled empty query answered: %+v", call)
			default:
			}
		})
	}
}

func TestInlineSupersededLeaseRevocationNeverAnswers(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	h, calls := newSearchTestHandlers(t, Options{Backend: backendFunc(func(ctx context.Context, req search.PageRequest) (search.Page, error) {
		if req.Query == "" {
			close(started)
			<-ctx.Done()
		}
		return search.Page{}, nil
	})})
	go func() {
		defer close(finished)
		h.onInline(context.Background(), &tg.InlineQuery{ID: "old-empty", Query: "i:", From: tg.User{ID: 42}})
	}()
	waitInlineLeaseEvent(t, started, "empty request did not reach the backend")
	h.onInline(context.Background(), &tg.InlineQuery{ID: "new", Query: "cats", From: tg.User{ID: 42}})
	waitInlineLeaseEvent(t, finished, "obsolete lease revocation did not stop")
	fields := receiveTelegramCall(t, calls, "answerInlineQuery")
	if fields["inline_query_id"] != "new" {
		t.Fatalf("answered obsolete empty request: %+v", fields)
	}
	select {
	case call := <-calls:
		t.Fatalf("unexpected extra answer: %+v", call)
	default:
	}
}

type inlineLeaseSource struct {
	name string
	fn   func(context.Context, search.SourceRequest) (search.SourcePage, error)
}

func (s inlineLeaseSource) Name() string              { return s.name }
func (s inlineLeaseSource) Category() search.Category { return search.CatImage }
func (s inlineLeaseSource) SearchSource(ctx context.Context, req search.SourceRequest) (search.SourcePage, error) {
	return s.fn(ctx, req)
}

type inlineLeaseCall struct {
	query    string
	ctx      context.Context
	canceled <-chan struct{}
}

func newInlineLeaseEngine(t *testing.T) (*stream.Engine, <-chan inlineLeaseCall) {
	t.Helper()
	started := make(chan inlineLeaseCall, 4)
	engine := stream.New(stream.Options{
		FirstPageWait: 10 * time.Millisecond, NativeTimeout: 5 * time.Second, Timeout: 6 * time.Second,
		Logger: discardLogger(),
		Sources: []search.Source{
			inlineLeaseSource{name: "fast", fn: func(context.Context, search.SourceRequest) (search.SourcePage, error) {
				results := make([]search.MediaResult, 10)
				for i := range results {
					results[i] = search.MediaResult{
						ID: fmt.Sprintf("fast-%d", i), Category: search.CatImage,
						MediaURL: fmt.Sprintf("https://media.example/%d.jpg", i), ThumbURL: "https://media.example/thumb.jpg",
					}
				}
				return search.SourcePage{Results: results}, nil
			}},
			inlineLeaseSource{name: "slow", fn: func(ctx context.Context, req search.SourceRequest) (search.SourcePage, error) {
				canceled := make(chan struct{})
				started <- inlineLeaseCall{query: req.Query, ctx: ctx, canceled: canceled}
				<-ctx.Done()
				close(canceled)
				return search.SourcePage{}, ctx.Err()
			}},
		},
	})
	t.Cleanup(engine.Close)
	return engine, started
}

func receiveInlineLeaseCall(t *testing.T, started <-chan inlineLeaseCall) inlineLeaseCall {
	t.Helper()
	select {
	case call := <-started:
		return call
	case <-time.After(time.Second):
		t.Fatal("slow source did not start")
		return inlineLeaseCall{}
	}
}

func waitInlineLeaseEvent(t *testing.T, event <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func assertInlinePrompt(t *testing.T, fields map[string]string, id string) {
	t.Helper()
	results := inlineResults(t, fields)
	if fields["inline_query_id"] != id || fields["next_offset"] != "" || fields["cache_time"] != "5" ||
		fields["is_personal"] != "true" || len(results) != 1 || results[0]["id"] != "prompt" {
		t.Fatalf("empty query did not return the prompt: %+v", fields)
	}
}

func answerPartialInline(t *testing.T, h *Handlers, calls <-chan telegramCall, owner int64, query string) {
	t.Helper()
	h.onInline(context.Background(), &tg.InlineQuery{ID: fmt.Sprint(owner), Query: query, From: tg.User{ID: owner}})
	fields := receiveTelegramCall(t, calls, "answerInlineQuery")
	if fields["next_offset"] == "" || fields["cache_time"] != "300" || len(inlineResults(t, fields)) != 10 {
		t.Fatalf("first handler did not return a partial page: %+v", fields)
	}
}

func TestInlineEmptyQueryCancelsCompletedHandlersLease(t *testing.T) {
	for _, empty := range []string{"", "V:I:"} {
		t.Run(fmt.Sprintf("query=%q", empty), func(t *testing.T) {
			engine, started := newInlineLeaseEngine(t)
			h, calls := newSearchTestHandlers(t, Options{Backend: engine})
			answerPartialInline(t, h, calls, 7, "cats")
			first := receiveInlineLeaseCall(t, started)
			answerPartialInline(t, h, calls, 8, "dogs")
			other := receiveInlineLeaseCall(t, started)
			if first.query != "cats" || other.query != "dogs" || first.ctx.Err() != nil || other.ctx.Err() != nil {
				t.Fatal("completed partial handlers did not retain their slow source leases")
			}

			h.onInline(context.Background(), &tg.InlineQuery{ID: "empty-7", Query: empty, From: tg.User{ID: 7}})
			assertInlinePrompt(t, receiveTelegramCall(t, calls, "answerInlineQuery"), "empty-7")
			waitInlineLeaseEvent(t, first.canceled, "empty query left the completed handler's unused background lease running")
			if other.ctx.Err() != nil {
				t.Fatal("empty query canceled another owner's background source")
			}

			h.onInline(context.Background(), &tg.InlineQuery{ID: "empty-8", Query: empty, From: tg.User{ID: 8}})
			assertInlinePrompt(t, receiveTelegramCall(t, calls, "answerInlineQuery"), "empty-8")
			waitInlineLeaseEvent(t, other.canceled, "second owner could not revoke its own background lease")
			select {
			case call := <-started:
				t.Fatalf("empty query started another upstream search: %q", call.query)
			default:
			}
		})
	}
}

func TestInlineEmptyQueryPreservesSharedOwnersLease(t *testing.T) {
	engine, started := newInlineLeaseEngine(t)
	h, calls := newSearchTestHandlers(t, Options{Backend: engine})
	answerPartialInline(t, h, calls, 7, "cats")
	slow := receiveInlineLeaseCall(t, started)
	answerPartialInline(t, h, calls, 8, "cats")
	select {
	case <-started:
		t.Fatal("identical query did not share the background source")
	default:
	}

	h.onInline(context.Background(), &tg.InlineQuery{ID: "empty-7", Query: "V:I:", From: tg.User{ID: 7}})
	assertInlinePrompt(t, receiveTelegramCall(t, calls, "answerInlineQuery"), "empty-7")
	if slow.ctx.Err() != nil {
		t.Fatal("revoking one owner canceled the source still leased by another")
	}

	h.onInline(context.Background(), &tg.InlineQuery{ID: "empty-8", From: tg.User{ID: 8}})
	assertInlinePrompt(t, receiveTelegramCall(t, calls, "answerInlineQuery"), "empty-8")
	waitInlineLeaseEvent(t, slow.canceled, "revoking the last owner left the shared background source running")
}
