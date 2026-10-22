package stream

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"searchy/internal/search"
)

func TestHealthySingleSourceDoesNotCallFallback(t *testing.T) {
	var fallbackCalls atomic.Int32
	e := newEngine(t, Options{Sources: []search.Source{constantSource("healthy", media(0, 10))}, Fallback: fakeBackend(func(context.Context, search.PageRequest) (search.Page, error) {
		fallbackCalls.Add(1)
		return search.Page{}, errors.New("must not be called")
	})})
	p, err := e.SearchPage(context.Background(), request())
	if err != nil || len(p.Results) != 10 || fallbackCalls.Load() != 0 {
		t.Fatalf("healthy source: %+v %v fallback=%d", p, err, fallbackCalls.Load())
	}
}

func TestExactSingleResultDoesNotPayFallback(t *testing.T) {
	results := media(0, 1)
	results[0].Title = "An exact private query match"
	e := newEngine(t, Options{Sources: []search.Source{constantSource("exact", results)}, Fallback: fakeBackend(func(context.Context, search.PageRequest) (search.Page, error) {
		t.Error("exact healthy singleton called fallback")
		return search.Page{}, nil
	})})
	if p, err := e.SearchPage(context.Background(), request()); err != nil || len(p.Results) != 1 || p.Degraded {
		t.Fatalf("exact singleton: %+v %v", p, err)
	}
}

func TestFallbackIsFilteredByRankAndCategory(t *testing.T) {
	results := append(media(0, 10), search.MediaResult{Category: search.CatImage, ID: "unsafe", MediaURL: "http://unsafe.test/image.png"})
	video := media(100, 1)[0]
	video.Category = search.CatVideo
	results = append(results, video)
	e := newEngine(t, Options{Sources: []search.Source{constantSource("empty", nil)},
		Fallback: fakeBackend(func(context.Context, search.PageRequest) (search.Page, error) {
			return search.Page{Results: results}, nil
		}),
		Rank: func(_ string, in []search.MediaResult, _ int) []search.MediaResult {
			out := in[:0]
			for _, r := range in {
				if strings.HasPrefix(r.MediaURL, "https://") && strings.HasSuffix(r.MediaURL, ".jpg") {
					out = append(out, r)
				}
			}
			return out
		},
	})
	p, err := e.SearchPage(context.Background(), request())
	if err != nil || len(p.Results) != 10 {
		t.Fatalf("invalid fallback results escaped rank: %+v %v", p, err)
	}
	for _, r := range p.Results {
		if r.Category != search.CatImage || r.ID == "unsafe" {
			t.Fatalf("unsafe or wrong-category fallback media: %+v", r)
		}
	}
}

func TestFallbackAnonymousAndExactContinuation(t *testing.T) {
	var mu sync.Mutex
	var offsets []string
	e := newEngine(t, Options{MaxResults: 80, Sources: []search.Source{constantSource("empty", nil)}, Fallback: fakeBackend(func(_ context.Context, r search.PageRequest) (search.Page, error) {
		if r.UserID != 0 || r.Scope != "" || r.Language != "fr-ca" || !slices.Equal(r.Categories, []search.Category{search.CatImage}) {
			t.Errorf("fallback leaked owner or discarded request constraints: %+v", r)
		}
		mu.Lock()
		offsets = append(offsets, r.Offset)
		mu.Unlock()
		if r.Offset == "" {
			return search.Page{Results: media(0, 35), NextOffset: "opaque-exact"}, nil
		}
		if r.Offset != "opaque-exact" {
			t.Errorf("wrong fallback cursor %q", r.Offset)
		}
		return search.Page{Results: media(34, 26)}, nil
	})})
	r := request()
	r.Language = "fr-CA"
	seen := make(map[string]bool)
	for {
		p, err := e.SearchPage(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range p.Results {
			if seen[result.ID] {
				t.Fatalf("fallback duplicate %s", result.ID)
			}
			seen[result.ID] = true
		}
		if p.NextOffset == "" {
			break
		}
		r.Offset = p.NextOffset
	}
	if len(seen) != 60 {
		t.Fatalf("fallback lost leftovers: %d", len(seen))
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(offsets, []string{"", "opaque-exact"}) {
		t.Fatalf("fallback continuations %v", offsets)
	}
}

func TestFallbackBeforeFirstResponseAndLateNativeDedup(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	var fallbackCalls atomic.Int32
	e := newEngine(t, Options{MaxResults: 60, Sources: []search.Source{sourceFunc("late", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
		started <- struct{}{}
		select {
		case <-gate:
			results := media(12, 24)
			for i := range results {
				results[i].RankScore = 100
			}
			return search.SourcePage{Results: results}, nil
		case <-ctx.Done():
			return search.SourcePage{}, ctx.Err()
		}
	})}, Fallback: fakeBackend(func(context.Context, search.PageRequest) (search.Page, error) {
		fallbackCalls.Add(1)
		return search.Page{Results: media(0, 24)}, nil
	})})
	r := request()
	p, err := e.SearchPage(context.Background(), r)
	receive(t, started)
	if err != nil || len(p.Results) != 24 || p.NextOffset == "" || fallbackCalls.Load() != 1 {
		t.Fatalf("first fallback page: %+v %v", p, err)
	}
	r.Offset = p.NextOffset
	next := asyncPage(e, context.Background(), r)
	assertPending(t, next)
	close(gate)
	out := receive(t, next)
	if out.err != nil || len(out.page.Results) != 12 || out.page.NextOffset != "" {
		t.Fatalf("late native tail: %+v %v", out.page, out.err)
	}
	for _, r := range out.page.Results {
		for _, previous := range p.Results {
			if r.ID == previous.ID {
				t.Fatal("late native repeated fallback item")
			}
		}
	}
	r.Offset = ""
	repeat, err := e.SearchPage(context.Background(), r)
	if err != nil || !reflect.DeepEqual(p, repeat) {
		t.Fatal("late native results changed first snapshot")
	}
}

func TestFallbackErrorsRetryAndTotalBudget(t *testing.T) {
	var calls atomic.Int32
	e := newEngine(t, Options{Sources: []search.Source{constantSource("empty", nil)}, Fallback: fakeBackend(func(context.Context, search.PageRequest) (search.Page, error) {
		if calls.Add(1) == 1 {
			return search.Page{}, errors.New("backend failed")
		}
		return search.Page{Results: media(0, 10)}, nil
	})})
	r := request()
	if _, err := e.SearchPage(context.Background(), r); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fallback failure hidden: %v", err)
	}
	if p, err := e.SearchPage(context.Background(), r); err != nil || len(p.Results) != 10 || calls.Load() != 2 {
		t.Fatalf("fallback failure cached: %+v %v calls=%d", p, err, calls.Load())
	}
	e = newEngine(t, Options{Timeout: 60 * time.Millisecond, NativeTimeout: 20 * time.Millisecond,
		Sources: []search.Source{constantSource("empty", nil)}, Fallback: fakeBackend(func(ctx context.Context, _ search.PageRequest) (search.Page, error) {
			<-ctx.Done()
			return search.Page{}, ctx.Err()
		})})
	start := time.Now()
	if _, err := e.SearchPage(context.Background(), r); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fallback budget error: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 45*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("fallback exceeded total budget: %v", elapsed)
	}
}
