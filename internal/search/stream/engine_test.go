package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"searchy/internal/search"
)

type fakeSource struct {
	name string
	cat  search.Category
	fn   func(context.Context, search.SourceRequest) (search.SourcePage, error)
}

func (s fakeSource) Name() string              { return s.name }
func (s fakeSource) Category() search.Category { return s.cat }
func (s fakeSource) SearchSource(ctx context.Context, r search.SourceRequest) (search.SourcePage, error) {
	return s.fn(ctx, r)
}

type fakeBackend func(context.Context, search.PageRequest) (search.Page, error)

func (b fakeBackend) SearchPage(ctx context.Context, r search.PageRequest) (search.Page, error) {
	return b(ctx, r)
}

func sourceFunc(name string, fn func(context.Context, search.SourceRequest) (search.SourcePage, error)) search.Source {
	return fakeSource{name: name, cat: search.CatImage, fn: fn}
}

func media(start, count int) []search.MediaResult {
	out := make([]search.MediaResult, count)
	for i := range out {
		id := start + i
		out[i] = search.MediaResult{ID: fmt.Sprintf("%04d", id), Category: search.CatImage,
			Title: "healthy media", MediaURL: fmt.Sprintf("https://media.test/%d.jpg", id),
			PageURL: fmt.Sprintf("https://pages.test/%d", id), Engine: "fake", Width: 800, Height: 600}
	}
	return out
}

func constantSource(name string, results []search.MediaResult) search.Source {
	return sourceFunc(name, func(context.Context, search.SourceRequest) (search.SourcePage, error) {
		return search.SourcePage{Results: results}, nil
	})
}

func newEngine(t *testing.T, o Options) *Engine {
	t.Helper()
	if o.FirstPageWait == 0 {
		o.FirstPageWait = 25 * time.Millisecond
	}
	if o.NativeTimeout == 0 {
		o.NativeTimeout = 600 * time.Millisecond
	}
	if o.Timeout == 0 {
		o.Timeout = 900 * time.Millisecond
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	e := New(o)
	t.Cleanup(e.Close)
	return e
}

func request() search.PageRequest {
	return search.PageRequest{Query: "private query", Categories: []search.Category{search.CatImage}, Language: "en", Limit: 24, UserID: 1, Scope: "inline"}
}

type outcome struct {
	page search.Page
	err  error
}

func asyncPage(e *Engine, ctx context.Context, r search.PageRequest) <-chan outcome {
	ch := make(chan outcome, 1)
	go func() {
		p, err := e.SearchPage(ctx, r)
		ch <- outcome{p, err}
	}()
	return ch
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		var zero T
		return zero
	}
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}

func assertPending(t *testing.T, ch <-chan outcome) {
	t.Helper()
	select {
	case out := <-ch:
		t.Fatalf("page returned before pending provider: %+v, %v", out.page, out.err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestFirstUsefulPageAndPendingContinuation(t *testing.T) {
	started := make(chan struct{}, 1)
	gate := make(chan struct{})
	late := media(100, 20)
	for i := range late {
		late[i].RankScore = 100
	}
	e := newEngine(t, Options{Sources: []search.Source{
		constantSource("fast", media(0, 10)),
		sourceFunc("slow", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
			started <- struct{}{}
			select {
			case <-gate:
				return search.SourcePage{Results: late}, nil
			case <-ctx.Done():
				return search.SourcePage{}, ctx.Err()
			}
		}),
	}})
	r := request()
	start := time.Now()
	first, err := e.SearchPage(context.Background(), r)
	if err != nil || len(first.Results) != 10 || first.NextOffset == "" || len(first.NextOffset) >= 64 || first.Degraded {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("first page took %v, expected quality window", elapsed)
	}
	receive(t, started)
	r.Offset = first.NextOffset
	next := asyncPage(e, context.Background(), r)
	assertPending(t, next)
	close(gate)
	second := receive(t, next)
	if second.err != nil || len(second.page.Results) != 20 || second.page.NextOffset != "" {
		t.Fatalf("pending continuation: %+v, %v", second.page, second.err)
	}
	r.Offset = ""
	repeat, err := e.SearchPage(context.Background(), r)
	if err != nil || !reflect.DeepEqual(first, repeat) {
		t.Fatalf("late ranking changed frozen page: %+v, %v", repeat, err)
	}
	e.mu.Lock()
	if len(e.byID) != 1 {
		t.Errorf("pending snapshot poisoned shared session reuse: %d sessions", len(e.byID))
	}
	e.mu.Unlock()
	first.Results[0].Title = "caller mutation"
	first.Results[0].Engines = []string{"caller mutation"}
	again, _ := e.SearchPage(context.Background(), r)
	if again.Results[0].Title == "caller mutation" {
		t.Fatal("caller mutated cached page")
	}
}

func TestExactSourceContinuationsAndLeftovers(t *testing.T) {
	var mu sync.Mutex
	var cursors []string
	e := newEngine(t, Options{MaxResults: 200, Sources: []search.Source{sourceFunc("paged", func(_ context.Context, r search.SourceRequest) (search.SourcePage, error) {
		mu.Lock()
		cursors = append(cursors, r.Cursor)
		mu.Unlock()
		switch r.Cursor {
		case "":
			return search.SourcePage{Results: media(0, 37), NextCursor: "a"}, nil
		case "a":
			return search.SourcePage{Results: media(36, 29), NextCursor: "b"}, nil
		case "b":
			return search.SourcePage{Results: media(65, 6)}, nil
		default:
			return search.SourcePage{}, fmt.Errorf("unexpected cursor")
		}
	})}})
	r := request()
	seen := make(map[string]bool)
	for pages := 0; ; pages++ {
		if pages >= maxPages {
			t.Fatal("too many pages")
		}
		p, err := e.SearchPage(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		repeat, err := e.SearchPage(context.Background(), r)
		if err != nil || !reflect.DeepEqual(p, repeat) {
			t.Fatal("same offset did not return identical snapshot")
		}
		for _, result := range p.Results {
			if seen[result.ID] {
				t.Fatalf("duplicate %s", result.ID)
			}
			seen[result.ID] = true
		}
		if p.NextOffset == "" {
			break
		}
		r.Offset = p.NextOffset
	}
	if len(seen) != 71 {
		t.Fatalf("lost leftovers: got %d of 71", len(seen))
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(cursors, []string{"", "a", "b"}) {
		t.Fatalf("upstream cursors: %v", cursors)
	}
}

func TestCursorBindingAndLanguages(t *testing.T) {
	var languages sync.Map
	source := sourceFunc("language", func(_ context.Context, r search.SourceRequest) (search.SourcePage, error) {
		languages.Store(r.Language, true)
		return search.SourcePage{Results: media(0, 30)}, nil
	})
	e := newEngine(t, Options{Sources: []search.Source{source}})
	r := request()
	r.Language = " EN_GB "
	p, err := e.SearchPage(context.Background(), r)
	if err != nil || p.NextOffset == "" {
		t.Fatalf("first: %+v %v", p, err)
	}
	for _, change := range []func(*search.PageRequest){
		func(r *search.PageRequest) { r.UserID++ },
		func(r *search.PageRequest) { r.Scope = "another scope" },
		func(r *search.PageRequest) { r.Query = "another query" },
		func(r *search.PageRequest) { r.Language = "en-US" },
		func(r *search.PageRequest) { r.Categories = []search.Category{search.CatVideo} },
		func(r *search.PageRequest) { r.Limit = 25 },
		func(r *search.PageRequest) { r.WaitAll = true },
	} {
		wrong := r
		wrong.Offset = p.NextOffset
		change(&wrong)
		if _, err := e.SearchPage(context.Background(), wrong); !errors.Is(err, search.ErrInvalidCursor) {
			t.Fatalf("cross-boundary cursor accepted: %+v %v", wrong, err)
		}
	}
	r.Offset = p.NextOffset
	other := newEngine(t, Options{Sources: []search.Source{source}})
	if _, err := other.SearchPage(context.Background(), r); !errors.Is(err, search.ErrInvalidCursor) {
		t.Fatalf("cursor accepted by another backend: %v", err)
	}
	if _, err := e.SearchPage(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Offset = ""
	r.Language = "all"
	if _, err := e.SearchPage(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en-gb", "all"} {
		if _, ok := languages.Load(lang); !ok {
			t.Fatalf("source did not receive %q", lang)
		}
	}
	for _, malformed := range []string{"1", strings.Repeat("a", 44), strings.Repeat("a", 1000)} {
		r.Offset = malformed
		if _, err := e.SearchPage(context.Background(), r); !errors.Is(err, search.ErrInvalidCursor) {
			t.Fatalf("invalid cursor accepted: %v", err)
		}
	}
}

func TestSharedCancellationDoesNotCancelOtherSubscriber(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	gate := make(chan struct{})
	e := newEngine(t, Options{Sources: []search.Source{sourceFunc("shared", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-gate:
			return search.SourcePage{Results: media(0, 30)}, nil
		case <-ctx.Done():
			canceled <- struct{}{}
			return search.SourcePage{}, ctx.Err()
		}
	})}})
	r := request()
	ctx, cancel := context.WithCancel(context.Background())
	one := asyncPage(e, ctx, r)
	receive(t, started)
	r.UserID = 2
	two := asyncPage(e, context.Background(), r)
	eventually(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.subscribers == 2 })
	cancel()
	if out := receive(t, one); !errors.Is(out.err, context.Canceled) {
		t.Fatalf("canceled subscriber: %v", out.err)
	}
	select {
	case <-canceled:
		t.Fatal("one subscriber canceled shared upstream")
	default:
	}
	close(gate)
	if out := receive(t, two); out.err != nil || len(out.page.Results) != 24 {
		t.Fatalf("remaining subscriber: %+v %v", out.page, out.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("shared call count %d", calls.Load())
	}
}

func TestTypingCancelsUnusedLeaseAndPreservesOtherOwner(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			started := make(chan struct{}, 1)
			canceled := make(chan struct{}, 1)
			e := newEngine(t, Options{Sources: []search.Source{
				constantSource("fast", media(0, 10)),
				sourceFunc("slow", func(ctx context.Context, r search.SourceRequest) (search.SourcePage, error) {
					if r.Query == "old" {
						started <- struct{}{}
						<-ctx.Done()
						canceled <- struct{}{}
						return search.SourcePage{}, ctx.Err()
					}
					return search.SourcePage{}, nil
				}),
			}})
			r := request()
			r.Query = "old"
			p, err := e.SearchPage(context.Background(), r)
			if err != nil || len(p.Results) != 10 {
				t.Fatalf("old first page: %+v %v", p, err)
			}
			receive(t, started)
			if shared {
				r.UserID = 2
				if _, err := e.SearchPage(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			r.UserID = 1
			r.Query = "new"
			if _, err := e.SearchPage(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if shared {
				select {
				case <-canceled:
					t.Fatal("new typing canceled another user's background interest")
				default:
				}
				r.UserID = 2
				// A cache hit must also cancel the previous owner's lease.
				if _, err := e.SearchPage(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			receive(t, canceled)
		})
	}
}

func TestEmptyFailureAndDegradedFreshSearchesRetry(t *testing.T) {
	for _, mode := range []string{"error", "empty", "degraded"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			e := newEngine(t, Options{Sources: []search.Source{sourceFunc("retry", func(context.Context, search.SourceRequest) (search.SourcePage, error) {
				if calls.Add(1) == 1 {
					switch mode {
					case "error":
						return search.SourcePage{}, errors.New("upstream rejected private query")
					case "empty":
						return search.SourcePage{}, nil
					default:
						return search.SourcePage{Results: media(0, 30), Degraded: true}, nil
					}
				}
				return search.SourcePage{Results: media(0, 30)}, nil
			})}})
			r := request()
			old, err := e.SearchPage(context.Background(), r)
			if mode == "error" && !errors.Is(err, ErrUnavailable) || mode != "error" && err != nil {
				t.Fatalf("first response: %v", err)
			}
			fresh, err := e.SearchPage(context.Background(), r)
			if err != nil || len(fresh.Results) != 24 || fresh.Degraded || calls.Load() != 2 {
				t.Fatalf("fresh retry: %+v, %v, calls=%d", fresh, err, calls.Load())
			}
			if _, err := e.SearchPage(context.Background(), r); err != nil || calls.Load() != 2 {
				t.Fatalf("successful cache miss: %v calls=%d", err, calls.Load())
			}
			if old.NextOffset != "" {
				r.Offset = old.NextOffset
				p, err := e.SearchPage(context.Background(), r)
				if err != nil || len(p.Results) != 6 {
					t.Fatalf("previous successful cursor lost: %+v %v", p, err)
				}
			}
		})
	}
}

func TestFailedContinuationRetriesExactCursorWithoutLosingTail(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var cursors []string
	e := newEngine(t, Options{Sources: []search.Source{sourceFunc("retry-cursor", func(_ context.Context, r search.SourceRequest) (search.SourcePage, error) {
		mu.Lock()
		cursors = append(cursors, r.Cursor)
		mu.Unlock()
		n := calls.Add(1)
		if n == 1 {
			return search.SourcePage{Results: media(0, 30), NextCursor: "exact"}, nil
		}
		if n == 2 {
			return search.SourcePage{}, errors.New("retryable")
		}
		return search.SourcePage{Results: media(30, 20)}, nil
	})}})
	r := request()
	seen := make(map[string]bool)
	for {
		p, err := e.SearchPage(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range p.Results {
			if seen[item.ID] {
				t.Fatalf("duplicate %s", item.ID)
			}
			seen[item.ID] = true
		}
		if p.NextOffset == "" {
			break
		}
		r.Offset = p.NextOffset
	}
	if len(seen) != 50 {
		t.Fatalf("lost source leftovers: %d", len(seen))
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(cursors, []string{"", "exact", "exact"}) {
		t.Fatalf("retry skipped upstream cursor: %v", cursors)
	}
}

func TestUnseenCorroborationAndAuthoritativeRankFiltering(t *testing.T) {
	one, two := media(0, 30), media(0, 30)
	for i := range one {
		one[i].Engine = "one"
		two[i].Engine = "two"
		two[i].ID = "different-" + two[i].ID
		two[i].Positions = []int{2}
	}
	// The gate keeps both provider results inside the quality window.
	gate := make(chan struct{})
	var started atomic.Int32
	var rankSawVariants atomic.Bool
	wrap := func(results []search.MediaResult) func(context.Context, search.SourceRequest) (search.SourcePage, error) {
		return func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
			started.Add(1)
			select {
			case <-gate:
				return search.SourcePage{Results: results}, nil
			case <-ctx.Done():
				return search.SourcePage{}, ctx.Err()
			}
		}
	}
	e := newEngine(t, Options{Sources: []search.Source{sourceFunc("one", wrap(one)), sourceFunc("two", wrap(two))}, Rank: func(_ string, in []search.MediaResult, _ int) []search.MediaResult {
		if len(in) >= 59 {
			rankSawVariants.Store(true)
		}
		out := in[:0]
		for _, r := range in {
			if r.MediaURL != "https://media.test/0.jpg" {
				out = append(out, r)
			}
		}
		return out
	}})
	r := request()
	ch := asyncPage(e, context.Background(), r)
	eventually(t, func() bool { return started.Load() == 2 })
	close(gate)
	out := receive(t, ch)
	if out.err != nil {
		t.Fatal(out.err)
	}
	if !rankSawVariants.Load() {
		t.Fatal("ranker did not receive corroborating title/URL variants")
	}
	for _, result := range out.page.Results {
		if result.MediaURL == "https://media.test/0.jpg" {
			t.Fatal("rank-rejected result reintroduced")
		}
		if !slices.Contains(result.Engines, "one") || !slices.Contains(result.Engines, "two") {
			t.Fatalf("lost consensus metadata: %+v", result)
		}
	}
}

func TestStringBoundsAndCursorEncryption(t *testing.T) {
	for _, text := range []string{"a€b", "😀xyz", strings.Repeat("я", 10)} {
		for n := 0; n < len(text); n++ {
			if got := boundedString(text, n); len(got) > n || !utf8.ValidString(got) {
				t.Fatalf("invalid UTF8 truncation %q at %d: %q", text, n, got)
			}
		}
	}
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newEngine(t, Options{Logger: logger, Sources: []search.Source{sourceFunc("safe-source", func(_ context.Context, r search.SourceRequest) (search.SourcePage, error) {
		if r.Cursor == "" {
			return search.SourcePage{Results: media(0, 30), NextCursor: r.Query}, nil
		}
		return search.SourcePage{}, errors.New("https://upstream.test/?q=" + r.Query)
	})}})
	r := request()
	p, err := e.SearchPage(context.Background(), r)
	if err != nil || len(p.Results) != 24 {
		t.Fatalf("first: %+v %v", p, err)
	}
	e.mu.Lock()
	for _, s := range e.byID {
		if bytes.Contains(s.states[0].cursor, []byte(r.Query)) || e.openCursor(s.states[0].cursor) != r.Query {
			t.Fatal("provider cursor not encrypted correctly")
		}
	}
	e.mu.Unlock()
	e.Close()
	if strings.Contains(log.String(), r.Query) || strings.Contains(log.String(), "https://upstream.test") {
		t.Fatalf("private diagnostics: %s", log.String())
	}
	if !strings.Contains(log.String(), "source=safe-source") || !strings.Contains(log.String(), "duration_ms=") {
		t.Fatalf("missing safe diagnostics: %s", log.String())
	}
}

func TestPausedSourceDoesNotMarkPageDegraded(t *testing.T) {
	var calls atomic.Int32
	e := newEngine(t, Options{Sources: []search.Source{
		constantSource("healthy", media(0, 10)),
		sourceFunc("paused", func(context.Context, search.SourceRequest) (search.SourcePage, error) {
			calls.Add(1)
			return search.SourcePage{}, search.ErrSourcePaused
		}),
	}})
	p, err := e.SearchPage(context.Background(), request())
	if err != nil || len(p.Results) != 10 || p.Degraded || calls.Load() != 1 {
		t.Fatalf("page with a paused source: %+v %v calls=%d", p, err, calls.Load())
	}
}

func TestFailedSourceMarksPageDegraded(t *testing.T) {
	e := newEngine(t, Options{Sources: []search.Source{
		constantSource("healthy", media(0, 10)),
		sourceFunc("broken", func(context.Context, search.SourceRequest) (search.SourcePage, error) {
			return search.SourcePage{}, errors.New("upstream HTTP 202")
		}),
	}})
	p, err := e.SearchPage(context.Background(), request())
	if err != nil || len(p.Results) != 10 || !p.Degraded {
		t.Fatalf("page with a failed source: %+v %v", p, err)
	}
}
