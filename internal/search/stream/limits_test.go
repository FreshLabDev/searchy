package stream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"searchy/internal/search"
)

func TestRequestLimitAndWaitAll(t *testing.T) {
	for _, waitAll := range []bool{false, true} {
		t.Run(fmt.Sprint(waitAll), func(t *testing.T) {
			var calls atomic.Int32
			e := newEngine(t, Options{MaxResults: 50, Sources: []search.Source{sourceFunc("paged", func(_ context.Context, r search.SourceRequest) (search.SourcePage, error) {
				calls.Add(1)
				if r.Cursor == "" {
					return search.SourcePage{Results: media(0, 35), NextCursor: "tail"}, nil
				}
				if r.Cursor != "tail" {
					t.Errorf("wrong source continuation %q", r.Cursor)
				}
				return search.SourcePage{Results: media(35, 15)}, nil
			})}})
			r := request()
			r.Limit, r.WaitAll = 7, waitAll
			seen := make(map[string]bool)
			for pages := 0; ; pages++ {
				if pages >= maxPages {
					t.Fatal("unbounded pages")
				}
				p, err := e.SearchPage(context.Background(), r)
				if err != nil || len(p.Results) > 7 {
					t.Fatalf("limit 7: %+v %v", p, err)
				}
				for _, result := range p.Results {
					if seen[result.ID] {
						t.Fatal("duplicate result")
					}
					seen[result.ID] = true
				}
				if p.NextOffset == "" {
					break
				}
				if waitAll {
					t.Fatal("grid returned continuation")
				}
				r.Offset = p.NextOffset
			}
			want := 50
			wantCalls := int32(2)
			if waitAll {
				want, wantCalls = 7, 1
			}
			if len(seen) != want || calls.Load() != wantCalls {
				t.Fatalf("got %d results / %d calls, want %d / %d", len(seen), calls.Load(), want, wantCalls)
			}
		})
	}
	for _, waitAll := range []bool{false, true} {
		e := newEngine(t, Options{Sources: []search.Source{constantSource("default", media(0, 60))}})
		r := request()
		r.Limit, r.WaitAll = 0, waitAll
		p, err := e.SearchPage(context.Background(), r)
		want := 24
		if waitAll {
			want = 50
		}
		if err != nil || len(p.Results) != want {
			t.Fatalf("default grid=%v: %d %v", waitAll, len(p.Results), err)
		}
	}
}

func TestWaitAllWaitsForBoundedCompletePool(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	e := newEngine(t, Options{MaxResults: 50, Sources: []search.Source{
		constantSource("fast", media(0, 45)),
		sourceFunc("late", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
			started <- struct{}{}
			select {
			case <-gate:
				return search.SourcePage{Results: media(100, 10)}, nil
			case <-ctx.Done():
				return search.SourcePage{}, ctx.Err()
			}
		}),
	}})
	r := request()
	r.WaitAll, r.Limit = true, 50
	ch := asyncPage(e, context.Background(), r)
	receive(t, started)
	assertPending(t, ch)
	close(gate)
	out := receive(t, ch)
	if out.err != nil || len(out.page.Results) != 50 || out.page.NextOffset != "" {
		t.Fatalf("complete grid: %+v %v", out.page, out.err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.jobs != 0 || len(e.leases) != 0 {
		t.Fatal("completed grid retained jobs or owner leases")
	}
}

func TestRepeatedCursorAndTenPageBounds(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t.Run(fmt.Sprint(repeat), func(t *testing.T) {
			var calls atomic.Int32
			e := newEngine(t, Options{MaxResults: 500, Sources: []search.Source{sourceFunc("loop", func(_ context.Context, r search.SourceRequest) (search.SourcePage, error) {
				call := int(calls.Add(1))
				next := fmt.Sprint(call)
				if repeat && call > 1 {
					next = r.Cursor
				}
				return search.SourcePage{Results: media((call-1)*4, 4), NextCursor: next}, nil
			})}})
			r := request()
			r.Limit = 1
			pages := 0
			for {
				p, err := e.SearchPage(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				pages++
				if p.NextOffset == "" {
					break
				}
				r.Offset = p.NextOffset
				if pages >= maxPages {
					t.Fatal("more than 10 visible pages")
				}
			}
			if pages > maxPages || calls.Load() > maxPages {
				t.Fatalf("unbounded chain: %d pages %d calls", pages, calls.Load())
			}
			if repeat && calls.Load() != 2 {
				t.Fatalf("repeated current cursor called %d times", calls.Load())
			}
		})
	}
}

func TestLRUCountsAllResultsAndPreservesRecentSession(t *testing.T) {
	e := newEngine(t, Options{CacheSize: 90, MaxResults: 30, Sources: []search.Source{constantSource("cache", media(0, 30))}})
	first := make(map[string]search.Page)
	for _, query := range []string{"one", "two", "three", "one", "four"} {
		r := request()
		r.Query = query
		p, err := e.SearchPage(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		first[query] = p
		e.mu.Lock()
		used := e.used
		total := 0
		for _, s := range e.byID {
			total += len(s.pool) + s.count
		}
		e.mu.Unlock()
		if used > 90 || total != used {
			t.Fatalf("result budget incorrect: %d accounted, %d actual", used, total)
		}
	}
	for _, query := range []string{"one", "two"} {
		r := request()
		r.Query, r.Offset = query, first[query].NextOffset
		_, err := e.SearchPage(context.Background(), r)
		if query == "two" && !errors.Is(err, search.ErrExpiredCursor) || query == "one" && err != nil {
			t.Fatalf("LRU cursor %s: %v", query, err)
		}
	}
}

func TestIdleTTLReapsAndCancelsBackgroundLease(t *testing.T) {
	canceled := make(chan struct{}, 1)
	e := newEngine(t, Options{CacheTTL: 80 * time.Millisecond, FirstPageWait: 5 * time.Millisecond, Sources: []search.Source{
		constantSource("fast", media(0, 24)),
		sourceFunc("blocked", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
			<-ctx.Done()
			canceled <- struct{}{}
			return search.SourcePage{}, ctx.Err()
		}),
	}})
	r := request()
	p, err := e.SearchPage(context.Background(), r)
	if err != nil || p.NextOffset == "" {
		t.Fatalf("first: %+v %v", p, err)
	}
	receive(t, canceled)
	eventually(t, func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return len(e.byID) == 0 && e.used == 0 && e.jobs == 0 && len(e.leases) == 0
	})
	r.Offset = p.NextOffset
	if _, err := e.SearchPage(context.Background(), r); !errors.Is(err, search.ErrExpiredCursor) {
		t.Fatalf("expired cursor: %v", err)
	}
}

func TestEmptySessionsHaveBoundedMetadata(t *testing.T) {
	e := newEngine(t, Options{CacheSize: 2, Sources: []search.Source{constantSource("empty", nil)}})
	for i := 0; i < 20; i++ {
		r := request()
		r.Query = fmt.Sprint(i)
		if _, err := e.SearchPage(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.byID) > 2 || e.used != 0 || len(e.byKey) != 0 || len(e.owners) != 0 || len(e.leases) != 0 {
		t.Fatalf("unbounded empty metadata: sessions=%d keys=%d used=%d owners=%d leases=%d", len(e.byID), len(e.byKey), e.used, len(e.owners), len(e.leases))
	}
}

func TestGlobalAndPerSourceConcurrency(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	var perSource [2]atomic.Int32
	var perPeak [2]atomic.Int32
	gate := make(chan struct{})
	var sources []search.Source
	for index := range 2 {
		sources = append(sources, sourceFunc(fmt.Sprint(index), func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
			n := active.Add(1)
			for n > peak.Load() && !peak.CompareAndSwap(peak.Load(), n) {
			}
			p := perSource[index].Add(1)
			for p > perPeak[index].Load() && !perPeak[index].CompareAndSwap(perPeak[index].Load(), p) {
			}
			defer active.Add(-1)
			defer perSource[index].Add(-1)
			select {
			case <-gate:
				return search.SourcePage{Results: media(index*100, 10)}, nil
			case <-ctx.Done():
				return search.SourcePage{}, ctx.Err()
			}
		}))
	}
	e := newEngine(t, Options{MaxConcurrent: 3, Sources: sources})
	var requests []<-chan outcome
	for i := range 8 {
		r := request()
		r.Query, r.UserID = fmt.Sprint(i), int64(i)
		requests = append(requests, asyncPage(e, context.Background(), r))
	}
	eventually(t, func() bool { return active.Load() == 3 })
	close(gate)
	for _, ch := range requests {
		out := receive(t, ch)
		if out.err != nil && !errors.Is(out.err, search.ErrBusy) {
			t.Fatal(out.err)
		}
	}
	if peak.Load() > 3 || perPeak[0].Load() > 2 || perPeak[1].Load() > 2 {
		t.Fatalf("concurrency limits: global=%d source=%d,%d", peak.Load(), perPeak[0].Load(), perPeak[1].Load())
	}
}

func TestCloseCancelsJobsAndContextIgnoringSourceKeepsItsSlot(t *testing.T) {
	gate := make(chan struct{})
	ended := make(chan struct{})
	var calls atomic.Int32
	e := newEngine(t, Options{MaxConcurrent: 1, NativeTimeout: 15 * time.Millisecond, Timeout: 25 * time.Millisecond, Sources: []search.Source{sourceFunc("bad-provider", func(context.Context, search.SourceRequest) (search.SourcePage, error) {
		calls.Add(1)
		defer close(ended)
		<-gate // deliberately violate the Source cancellation contract
		return search.SourcePage{}, nil
	})}})
	for i := range 3 {
		r := request()
		r.Query = fmt.Sprint(i)
		if _, err := e.SearchPage(context.Background(), r); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("blocked provider: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("uncancelable provider escaped global limit: %d", calls.Load())
	}
	start := time.Now()
	e.Close()
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Close blocked on uncooperative provider")
	}
	close(gate)
	receive(t, ended)
	if _, err := e.SearchPage(context.Background(), request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed engine accepted request: %v", err)
	}
}

func TestCanceledLastSubscriberReleasesUpstream(t *testing.T) {
	started := make(chan struct{}, 1)
	ended := make(chan struct{}, 1)
	e := newEngine(t, Options{Sources: []search.Source{sourceFunc("cancel", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
		started <- struct{}{}
		<-ctx.Done()
		ended <- struct{}{}
		return search.SourcePage{}, ctx.Err()
	})}})
	ctx, cancel := context.WithCancel(context.Background())
	ch := asyncPage(e, ctx, request())
	receive(t, started)
	cancel()
	if out := receive(t, ch); !errors.Is(out.err, context.Canceled) {
		t.Fatalf("canceled handler: %v", out.err)
	}
	receive(t, ended)
	eventually(t, func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.jobs == 0 && e.active == 0 && len(e.leases) == 0
	})
}

func TestConcurrentSameCursorReturnsSameSnapshot(t *testing.T) {
	e := newEngine(t, Options{Sources: []search.Source{constantSource("static", media(0, 50))}})
	r := request()
	p, err := e.SearchPage(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Offset = p.NextOffset
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := e.SearchPage(context.Background(), r)
			if err != nil || len(p.Results) != 24 || p.Results[0].ID != "0024" {
				t.Errorf("concurrent cursor: %+v %v", p, err)
			}
		}()
	}
	wg.Wait()
}

func TestCursorRequestsExtendSessionButNewSearchGetsFreshSnapshot(t *testing.T) {
	ttl := 300 * time.Millisecond
	e := newEngine(t, Options{CacheTTL: ttl, MaxResults: 200, Sources: []search.Source{constantSource("static", media(0, 100))}})
	r := request()
	first, err := e.SearchPage(context.Background(), r)
	if err != nil || first.NextOffset == "" {
		t.Fatalf("first: %+v %v", first, err)
	}
	cursor := first.NextOffset
	for i := 2; i <= 3; i++ {
		time.Sleep(200 * time.Millisecond) // each step is under the TTL, the sum is over it
		r.Offset = cursor
		p, err := e.SearchPage(context.Background(), r)
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		cursor = p.NextOffset
	}
	r.Offset = ""
	fresh, err := e.SearchPage(context.Background(), r)
	if err != nil || fresh.NextOffset == first.NextOffset {
		t.Fatalf("new search reused a snapshot older than the TTL: %+v %v", fresh, err)
	}
}
