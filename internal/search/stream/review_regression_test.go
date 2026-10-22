package stream

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"searchy/internal/search"
)

func TestInitialSourcesPrecedeContinuationsWithOneSlot(t *testing.T) {
	for _, waitAll := range []bool{false, true} {
		t.Run(fmt.Sprint(waitAll), func(t *testing.T) {
			var mu sync.Mutex
			var order []string
			record := func(event string) {
				mu.Lock()
				order = append(order, event)
				mu.Unlock()
			}
			e := newEngine(t, Options{MaxConcurrent: 1, FirstPageWait: 20 * time.Millisecond, NativeTimeout: 150 * time.Millisecond, Timeout: 250 * time.Millisecond, Sources: []search.Source{
				sourceFunc("first", func(ctx context.Context, r search.SourceRequest) (search.SourcePage, error) {
					if r.Cursor == "" {
						record("first initial")
						return search.SourcePage{NextCursor: "tail"}, nil
					}
					record("first continuation")
					<-ctx.Done()
					return search.SourcePage{}, ctx.Err()
				}),
				sourceFunc("later", func(context.Context, search.SourceRequest) (search.SourcePage, error) {
					record("later initial")
					return search.SourcePage{Results: media(100, 10)}, nil
				}),
			}})
			r := request()
			r.WaitAll = waitAll
			start := time.Now()
			p, err := e.SearchPage(context.Background(), r)
			if err != nil || len(p.Results) != 10 || !p.Degraded {
				t.Fatalf("initial fairness: %+v %v", p, err)
			}
			if !waitAll && time.Since(start) > 80*time.Millisecond {
				t.Fatalf("initial results missed quality window: %v", time.Since(start))
			}
			eventually(t, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return len(order) == 3
			})
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(order, []string{"first initial", "later initial", "first continuation"}) {
				t.Fatalf("initial request starved by continuation: %v", order)
			}
		})
	}
}

func TestUsefulSingletonReturnsAtQualityWindow(t *testing.T) {
	good := media(0, 1)
	good[0].Title = "private query"
	var calls atomic.Int32
	e := newEngine(t, Options{FirstPageWait: 20 * time.Millisecond, NativeTimeout: 150 * time.Millisecond, Timeout: 250 * time.Millisecond, Sources: []search.Source{
		sourceFunc("useful", func(context.Context, search.SourceRequest) (search.SourcePage, error) {
			calls.Add(1)
			return search.SourcePage{Results: good}, nil
		}),
		sourceFunc("slow", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
			<-ctx.Done()
			return search.SourcePage{}, ctx.Err()
		}),
	}, Fallback: fakeBackend(func(context.Context, search.PageRequest) (search.Page, error) {
		t.Error("useful singleton triggered weak-result fallback")
		return search.Page{}, nil
	})})
	r := request()
	start := time.Now()
	p, err := e.SearchPage(context.Background(), r)
	elapsed := time.Since(start)
	if err != nil || len(p.Results) != 1 || !p.Degraded || p.NextOffset == "" {
		t.Fatalf("useful singleton: %+v %v", p, err)
	}
	if elapsed < 15*time.Millisecond || elapsed > 80*time.Millisecond {
		t.Fatalf("quality window=20ms, native=150ms, actual=%v", elapsed)
	}
	repeat, err := e.SearchPage(context.Background(), r)
	if err != nil || !reflect.DeepEqual(p, repeat) || calls.Load() != 1 {
		t.Fatalf("partial singleton was not shared/frozen: %+v %v calls=%d", repeat, err, calls.Load())
	}
}

func TestEmptyQueryRevokesReturnedOwnerLease(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			ended := make(chan struct{}, 1)
			var calls atomic.Int32
			e := newEngine(t, Options{NativeTimeout: time.Second, Sources: []search.Source{
				constantSource("fast", media(0, 10)),
				sourceFunc("leased", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
					calls.Add(1)
					<-ctx.Done()
					ended <- struct{}{}
					return search.SourcePage{}, ctx.Err()
				}),
			}})
			r := request()
			if p, err := e.SearchPage(context.Background(), r); err != nil || len(p.Results) != 10 {
				t.Fatalf("leased first page: %+v %v", p, err)
			}
			if shared {
				r.UserID = 2
				if _, err := e.SearchPage(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			e.mu.Lock()
			before := len(e.byID)
			e.mu.Unlock()
			empty := search.PageRequest{Query: " \t\n", UserID: 1, Scope: "inline"}
			p, err := e.SearchPage(context.Background(), empty)
			if err != nil || len(p.Results) != 0 || p.NextOffset != "" {
				t.Fatalf("empty owner request: %+v %v", p, err)
			}
			e.mu.Lock()
			if len(e.byID) != before || e.leases[e.ownerKey(1, "inline")] != nil {
				t.Error("empty request allocated a session or retained its owner lease")
			}
			if shared {
				l := e.leases[e.ownerKey(2, "inline")]
				if l == nil || l.s.job == nil || l.s.job.ctx.Err() != nil {
					t.Error("empty request canceled another owner's background interest")
				}
			}
			e.mu.Unlock()
			if shared {
				empty.UserID = 2
				if _, err := e.SearchPage(context.Background(), empty); err != nil {
					t.Fatal(err)
				}
			}
			receive(t, ended)
			if calls.Load() != 1 {
				t.Fatalf("empty request started upstream work: %d", calls.Load())
			}
		})
	}
}

func TestEmptyQueryCancelsActiveOwnerSubscriptions(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			started := make(chan struct{}, 1)
			ended := make(chan struct{}, 1)
			gate := make(chan struct{})
			e := newEngine(t, Options{Sources: []search.Source{sourceFunc("active", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
				started <- struct{}{}
				select {
				case <-gate:
					return search.SourcePage{Results: media(0, 10)}, nil
				case <-ctx.Done():
					ended <- struct{}{}
					return search.SourcePage{}, ctx.Err()
				}
			})}})
			r := request()
			one := asyncPage(e, context.Background(), r)
			receive(t, started)
			var two <-chan outcome
			if shared {
				r.UserID = 2
				two = asyncPage(e, context.Background(), r)
				eventually(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.subscribers == 2 })
			}
			if _, err := e.SearchPage(context.Background(), search.PageRequest{UserID: 1, Scope: "inline"}); err != nil {
				t.Fatal(err)
			}
			if out := receive(t, one); !errors.Is(out.err, context.Canceled) {
				t.Fatalf("empty request did not cancel active owner: %v", out.err)
			}
			if shared {
				select {
				case <-ended:
					t.Fatal("empty request canceled shared upstream")
				default:
				}
				close(gate)
				if out := receive(t, two); out.err != nil || len(out.page.Results) != 10 {
					t.Fatalf("remaining active owner: %+v %v", out.page, out.err)
				}
			} else {
				receive(t, ended)
			}
		})
	}
}

type checkedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *checkedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestSupersededEmptyQueryAndOtherScopePreserveLease(t *testing.T) {
	ended := make(chan struct{}, 1)
	e := newEngine(t, Options{NativeTimeout: time.Second, Sources: []search.Source{
		constantSource("fast", media(0, 10)),
		sourceFunc("leased", func(ctx context.Context, _ search.SourceRequest) (search.SourcePage, error) {
			<-ctx.Done()
			ended <- struct{}{}
			return search.SourcePage{}, ctx.Err()
		}),
	}})
	r := request()
	if _, err := e.SearchPage(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	checked := &checkedContext{Context: ctx, checked: make(chan struct{})}
	e.mu.Lock()
	ch := asyncPage(e, checked, search.PageRequest{UserID: r.UserID, Scope: r.Scope})
	receive(t, checked.checked)
	cancel()
	e.mu.Unlock()
	if out := receive(t, ch); !errors.Is(out.err, context.Canceled) {
		t.Fatalf("superseded empty context: %v", out.err)
	}
	if _, err := e.SearchPage(context.Background(), search.PageRequest{UserID: r.UserID, Scope: "group"}); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	l := e.leases[e.ownerKey(r.UserID, r.Scope)]
	intact := l != nil && l.s.job != nil && l.s.job.ctx.Err() == nil
	e.mu.Unlock()
	if !intact {
		t.Fatal("superseded empty request or different scope revoked live interest")
	}
	if _, err := e.SearchPage(context.Background(), search.PageRequest{UserID: r.UserID, Scope: r.Scope}); err != nil {
		t.Fatal(err)
	}
	receive(t, ended)
}

func TestEmptyQueryDoesNotStartOrAllocateSearch(t *testing.T) {
	e := newEngine(t, Options{Sources: []search.Source{sourceFunc("unused", func(context.Context, search.SourceRequest) (search.SourcePage, error) {
		t.Error("empty request called native source")
		return search.SourcePage{}, nil
	})}, Fallback: fakeBackend(func(context.Context, search.PageRequest) (search.Page, error) {
		t.Error("empty request called fallback")
		return search.Page{}, nil
	})})
	if _, err := e.SearchPage(context.Background(), search.PageRequest{UserID: 1, Scope: "inline"}); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.byID) != 0 || len(e.byKey) != 0 || len(e.leases) != 0 || len(e.owners) != 0 || e.jobs != 0 || e.used != 0 {
		t.Fatal("empty request allocated search state")
	}
}

func TestVideoFragmentRoutesRemainDistinctAcrossPages(t *testing.T) {
	results := media(0, 2)
	for i, route := range []string{"first", "second"} {
		results[i].Category = search.CatVideo
		results[i].PageURL = "https://video.example.com/#/" + route
		results[i].MediaURL = results[i].PageURL
		results[i].ThumbURL = "https://media.test/video.jpg"
		results[i].MIMEType = "text/html"
	}
	e := newEngine(t, Options{Sources: []search.Source{fakeSource{name: "hash videos", cat: search.CatVideo, fn: func(context.Context, search.SourceRequest) (search.SourcePage, error) {
		return search.SourcePage{Results: results}, nil
	}}}})
	r := request()
	r.Categories = []search.Category{search.CatVideo}
	r.Limit = 1
	first, err := e.SearchPage(context.Background(), r)
	if err != nil || len(first.Results) != 1 || first.NextOffset == "" {
		t.Fatalf("first hash route: %+v %v", first, err)
	}
	r.Offset = first.NextOffset
	second, err := e.SearchPage(context.Background(), r)
	if err != nil || len(second.Results) != 1 || second.NextOffset != "" {
		t.Fatalf("second hash route lost: %+v %v", second, err)
	}
	if first.Results[0].ID == second.Results[0].ID || first.Results[0].PageURL == second.Results[0].PageURL {
		t.Fatal("distinct video hash routes collapsed into one identity")
	}
}
